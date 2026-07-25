package gitsync

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestNormalizeProvisionManifestRejectsUnsafeInput(t *testing.T) {
	mode := "0750"
	tests := []struct {
		name     string
		manifest provisionManifest
		error    string
	}{
		{name: "version", manifest: provisionManifest{Version: 2, Directories: []provisionDirectory{{Path: "data"}}}, error: "version must be 1"},
		{name: "traversal", manifest: provisionManifest{Version: 1, Directories: []provisionDirectory{{Path: "../data"}}}, error: "path traversal"},
		{name: "duplicate", manifest: provisionManifest{Version: 1, Directories: []provisionDirectory{{Path: "data"}}, Permissions: []provisionPermission{{Path: "data", Mode: mode}}}, error: "declared more than once"},
		{name: "owner pair", manifest: provisionManifest{Version: 1, Permissions: []provisionPermission{{Path: "config.yml", UID: func() *int { value := 1000; return &value }()}}}, error: "uid and gid"},
		{name: "special mode", manifest: provisionManifest{Version: 1, Permissions: []provisionPermission{{Path: "config.yml", Mode: "4755"}}}, error: "permission bits"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeProvisionManifest(test.manifest)
			require.ErrorContains(t, err, test.error)
		})
	}
}

func TestProvisionTransactionAppliesAndRollsBack(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "config.yml"), []byte("enabled: true\n"), 0644))
	targetFS := filesystem.NewLocal(root)
	operations, err := normalizeProvisionManifest(provisionManifest{Version: 1,
		Directories: []provisionDirectory{{Path: "data/cache", Mode: "0750"}},
		Permissions: []provisionPermission{{Path: "config.yml", Mode: "0600"}},
	})
	require.NoError(t, err)
	tx := &provisionTransaction{filesystem: targetFS}
	require.NoError(t, tx.apply(context.Background(), ".", operations))
	info, err := os.Stat(filepath.Join(root, "config.yml"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	info, err = os.Stat(filepath.Join(root, "data", "cache"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0750), info.Mode().Perm())

	require.NoError(t, tx.Rollback())
	info, err = os.Stat(filepath.Join(root, "config.yml"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0644), info.Mode().Perm())
	require.NoDirExists(t, filepath.Join(root, "data"))
}

func TestProvisionTransactionRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))
	operations, err := normalizeProvisionManifest(provisionManifest{Version: 1, Directories: []provisionDirectory{{Path: "escape/data"}}})
	require.NoError(t, err)
	tx := &provisionTransaction{filesystem: filesystem.NewLocal(root)}
	require.ErrorContains(t, validateProvisionStackRoot(tx.filesystem, ".", "escape"), "symbolic links are forbidden")
	err = tx.apply(context.Background(), ".", operations)
	require.ErrorContains(t, err, "symbolic links are forbidden")
	require.NoDirExists(t, filepath.Join(outside, "data"))
}

func TestProvisionControlFileHasAStableVirtualBaseline(t *testing.T) {
	file := transferFile{path: "app/provision.yml", sha: "new", size: 10, open: func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("version: 1")), nil
	}}
	preview := buildPreview("binding", "repository_to_stack", map[string]transferFile{file.path: file}, nil, nil)
	require.Equal(t, 1, preview.Changed)
	require.Equal(t, "add", preview.Entries[0].Status)

	preview = buildPreview("binding", "repository_to_stack", map[string]transferFile{file.path: file}, nil, map[string]string{file.path: "new"})
	require.Zero(t, preview.Changed)
	require.Equal(t, 1, preview.Unchanged)

	preview = buildPreview("binding", "repository_to_stack", nil, nil, map[string]string{file.path: "new"})
	require.Equal(t, 1, preview.Changed)
	require.Equal(t, "remove_control", preview.Entries[0].Status)
	require.Empty(t, changedPreviewPaths(preview), "removing only a provision control file must not redeploy the stack")
	require.Equal(t, []string{"app/config.yml"}, changedPreviewPaths(TransferPreview{Entries: []PreviewEntry{
		{Path: file.path, Status: "remove_control"},
		{Path: "app/config.yml", Status: "modify"},
	}}), "real stack changes in the same commit must still trigger deployment")
	selected, _, err := selectedTransferFiles(preview, nil, nil, nil)
	require.NoError(t, err)
	require.Contains(t, selected, file.path)
	require.NotContains(t, baselineAfterTransfer(map[string]string{file.path: "new"}, nil, nil, selected), file.path)
}

func TestApplyStackProvisioningLoadsGitOnlyManifest(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	remoteChange(t, repository.RemoteURL, "stacks/app/compose.yml", "services:\n  app:\n    image: alpine:3.23\n")
	remoteChange(t, repository.RemoteURL, "stacks/app/provision.yaml", "version: 1\ndirectories:\n  - path: data\n    mode: \"0750\"\npermissions:\n  - path: config.yml\n    mode: \"0600\"\n")
	_, err := service.FetchRepository(context.Background(), repository.UUID)
	require.NoError(t, err)
	status, err := service.PullRepository(context.Background(), repository.UUID)
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "config.yml"), []byte("ok\n"), 0644))
	binding := StackBinding{UUID: uuid.NewString(), RepositoryUUID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"}
	require.NoError(t, service.store.SaveBinding(&binding))
	logs := &strings.Builder{}
	tx, err := service.applyStackProvisioning(context.Background(), binding, status.Head, "app/compose.yml", logs)
	require.NoError(t, err)
	require.NotNil(t, tx)
	require.Contains(t, logs.String(), "applied app/provision.yaml securely")
	require.NoFileExists(t, filepath.Join(stackRoot, "app", "provision.yaml"))
	info, err := os.Stat(filepath.Join(stackRoot, "app", "config.yml"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	require.DirExists(t, filepath.Join(stackRoot, "app", "data"))
	require.NoError(t, tx.Rollback())
	require.NoDirExists(t, filepath.Join(stackRoot, "app", "data"))
}

func TestGitProvisionManifestIsTrackedWithoutBeingCopied(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	remoteChange(t, repository.RemoteURL, "stacks/app/compose.yml", "services:\n  app:\n    image: alpine:3.23\n")
	remoteChange(t, repository.RemoteURL, "stacks/app/provision.yml", "version: 1\ndirectories:\n  - path: data\n")
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0755))
	binding, err := service.CreateBindingContext(context.Background(), BindingInput{RepositoryID: repository.UUID,
		Host: "local", StackPath: "compose/app", SubPath: "stacks/app", InitialSync: "repository_to_stack"})
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(stackRoot, "app", "provision.yml"))
	baseline, err := service.store.BindingBaseline(binding.ID)
	require.NoError(t, err)
	require.NotEmpty(t, baseline["provision.yml"])
	preview, err := service.PreviewBinding(binding.ID, "repository_to_stack", TransferInput{})
	require.NoError(t, err)
	require.Zero(t, preview.Changed, "the Git-only manifest must not create a synchronization loop")
}
