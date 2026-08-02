package gitsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	gitclient "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type denyAtomicReplacementFS struct {
	filesystem.FileSystem
}

func (d denyAtomicReplacementFS) OpenFile(filename string, flag int, perm fs.FileMode) (io.ReadWriteCloser, error) {
	if strings.Contains(filepath.Base(filename), ".dockman-git-") {
		return nil, fs.ErrPermission
	}
	return d.FileSystem.OpenFile(filename, flag, perm)
}

type denyReadFS struct {
	filesystem.FileSystem
	baseName string
}

type denyReadDirFS struct {
	filesystem.FileSystem
	baseName string
}

func (d denyReadDirFS) ReadDir(path string) ([]fs.DirEntry, error) {
	if filepath.Base(path) == d.baseName {
		return nil, fmt.Errorf("must not read %s: %w", path, fs.ErrPermission)
	}
	return d.FileSystem.ReadDir(path)
}

func (d denyReadFS) OpenFile(filename string, flag int, perm fs.FileMode) (io.ReadWriteCloser, error) {
	if filepath.Base(filename) == d.baseName && flag&os.O_WRONLY == 0 && flag&os.O_RDWR == 0 {
		return nil, fs.ErrPermission
	}
	return d.FileSystem.OpenFile(filename, flag, perm)
}

func configureTestStack(t *testing.T, service *Service) string {
	t.Helper()
	root := t.TempDir()
	service.ConfigureStackAccess(func(host, stackPath string) (filesystem.FileSystem, string, error) {
		if host != "local" {
			return nil, "", os.ErrNotExist
		}
		if stackPath == "compose" {
			return filesystem.NewLocal(root), ".", nil
		}
		if stackPath == "compose/app" {
			return filesystem.NewLocal(root), "app", nil
		}
		return nil, "", os.ErrNotExist
	}, func() []string { return []string{"local"} }, filepath.Join(t.TempDir(), "backups"))
	return root
}

func prepareBindingRepository(t *testing.T, service *Service) Repository {
	t.Helper()
	remotePath, _ := createTestRemote(t)
	row := Repository{UUID: uuid.NewString(), Name: "binding-repository", Provider: "test", RemoteURL: remotePath, DefaultBranch: "main", Mode: "managed", Status: "cloning"}
	require.NoError(t, service.store.SaveRepository(&row))
	require.NoError(t, service.cloneRepository(context.Background(), row))
	row.Status = "ready"
	require.NoError(t, service.store.SaveRepository(&row))
	return row
}

func TestBindingRejectsTraversalAndGitMetadata(t *testing.T) {
	service, _ := testService(t, true)
	configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)

	_, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "../config", SubPath: "."})
	require.ErrorContains(t, err, "path traversal")
	_, err = service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: ".git/hooks"})
	require.ErrorContains(t, err, ".git paths are forbidden")
}

func TestRepositoryExclusionsAreRootAnchoredAcrossBindings(t *testing.T) {
	repository := Repository{ExcludePatterns: "/README.md\n/shared/**"}
	policy, err := policyFromBinding(StackBinding{SubPath: ".", SyncProfile: syncProfileAllFiles}, repository)
	require.NoError(t, err)
	require.True(t, policy.excludesPath("README.md", false, nil))
	require.False(t, policy.excludesPath("nested/README.md", false, nil))
	require.True(t, policy.excludesPath("shared/cache.json", false, nil))

	nestedPolicy, err := policyFromBinding(StackBinding{SubPath: "stacks/app", SyncProfile: syncProfileAllFiles}, repository)
	require.NoError(t, err)
	require.False(t, nestedPolicy.excludesPath("README.md", false, nil), "a root-anchored rule must not hide a linked folder README")
}

func TestBindingAutomaticallyReconcilesIdenticalTrees(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	contents := "services:\n  app:\n    image: alpine:3.23\n"
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte(contents), 0o644))
	remoteChange(t, repository.RemoteURL, "stacks/app/compose.yaml", contents)

	binding, err := service.CreateBindingContext(context.Background(), BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)
	require.Equal(t, "reconciled", binding.InitialSyncState)
	baseline, err := service.store.BindingBaseline(binding.ID)
	require.NoError(t, err)
	require.NotEmpty(t, baseline["compose.yaml"])
}

func TestBindingCanInitializeFromDockmanOrGit(t *testing.T) {
	t.Run("Dockman to Git", func(t *testing.T) {
		service, _ := testService(t, true)
		stackRoot := configureTestStack(t, service)
		repository := prepareBindingRepository(t, service)
		contents := "services:\n  app:\n    image: alpine:3.23\n"
		require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte(contents), 0o644))
		binding, err := service.CreateBindingContext(context.Background(), BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app", InitialSync: "stack_to_repository"})
		require.NoError(t, err)
		require.Equal(t, "exported", binding.InitialSyncState)
		check, err := gitclient.PlainClone(t.TempDir(), false, &gitclient.CloneOptions{URL: repository.RemoteURL, ReferenceName: plumbing.NewBranchReferenceName("main"), SingleBranch: true})
		require.NoError(t, err)
		requireGitFileContent(t, check, "main", "stacks/app/compose.yaml", contents)
	})

	t.Run("Git to Dockman", func(t *testing.T) {
		service, _ := testService(t, true)
		stackRoot := configureTestStack(t, service)
		repository := prepareBindingRepository(t, service)
		contents := "services:\n  app:\n    image: alpine:3.24\n"
		remoteChange(t, repository.RemoteURL, "stacks/app/compose.yaml", contents)
		require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0o755))
		binding, err := service.CreateBindingContext(context.Background(), BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app", InitialSync: "repository_to_stack"})
		require.NoError(t, err)
		require.Equal(t, "imported", binding.InitialSyncState)
		actual, err := os.ReadFile(filepath.Join(stackRoot, "app", "compose.yaml"))
		require.NoError(t, err)
		require.Equal(t, contents, string(actual))
	})
}

func TestGitFirstImportCreatesMissingLocalFoldersForSelectedStacks(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := t.TempDir()
	service.ConfigureStackAccess(func(host, stackPath string) (filesystem.FileSystem, string, error) {
		if host != "local" || (stackPath != "compose" && !strings.HasPrefix(stackPath, "compose/")) {
			return nil, "", os.ErrNotExist
		}
		relative := strings.Trim(strings.TrimPrefix(stackPath, "compose"), "/")
		if relative == "" {
			relative = "."
		}
		return filesystem.NewLocal(stackRoot), relative, nil
	}, func() []string { return []string{"local"} }, filepath.Join(t.TempDir(), "backups"))
	repository := prepareBindingRepository(t, service)
	remoteChange(t, repository.RemoteURL, "alpha/compose.yml", "services:\n  alpha:\n    image: alpine:3.23\n")
	remoteChange(t, repository.RemoteURL, "alpha/.env.example", "PORT=8080\n")
	remoteChange(t, repository.RemoteURL, "beta/compose.yml", "services:\n  beta:\n    image: alpine:3.23\n")
	remoteChange(t, repository.RemoteURL, "ignored/compose.yml", "services:\n  ignored:\n    image: alpine:3.23\n")

	binding, err := service.CreateBindingContext(context.Background(), BindingInput{
		RepositoryID: repository.UUID, Host: "local", StackPath: "compose/git-import", SubPath: ".",
		InitialSync: "repository_to_stack", SyncProfile: syncProfileComposeOnly, ComposeSelectionMode: composeSelectionSelected,
		SelectedComposePaths: []string{"alpha/compose.yml", "beta/compose.yml"},
	})
	require.NoError(t, err)
	require.Equal(t, "imported", binding.InitialSyncState)
	require.Equal(t, syncProfileComposeOnly, binding.SyncProfile)
	require.FileExists(t, filepath.Join(stackRoot, "git-import", "alpha", "compose.yml"))
	require.FileExists(t, filepath.Join(stackRoot, "git-import", "alpha", ".env.example"))
	require.FileExists(t, filepath.Join(stackRoot, "git-import", "beta", "compose.yml"))
	require.NoFileExists(t, filepath.Join(stackRoot, "git-import", "ignored", "compose.yml"))
	require.Equal(t, []string{"alpha/compose.yml", "beta/compose.yml"}, binding.SelectedComposePaths)
}

func TestPreviewSkipsSensitiveFilesUnlessExplicitlyConfirmed(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte("services:\n  app:\n    image: alpine\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", ".env"), []byte("TOKEN=do-not-leak\n"), 0600))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)

	preview, err := service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	require.Equal(t, 1, preview.Changed)
	require.Equal(t, 1, preview.Skipped)
	require.Equal(t, "skipped_sensitive", preview.Entries[0].Status)

	_, err = service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{IncludeSensitive: true})
	require.ErrorContains(t, err, sensitiveConfirmText)
	preview, err = service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{IncludeSensitive: true, SensitiveConfirmation: sensitiveConfirmText})
	require.NoError(t, err)
	require.Equal(t, 2, preview.Changed)
	require.Zero(t, preview.Skipped)

	defaultPreview, err := service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte("services:\n  app:\n    image: alpine:3.22\n"), 0644))
	_, err = service.ExportBinding(context.Background(), binding.ID, TransferInput{PreviewToken: defaultPreview.PreviewToken})
	require.ErrorContains(t, err, "changed after the preview")
	defaultPreview, err = service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	_, err = service.ExportBinding(context.Background(), binding.ID, TransferInput{PreviewToken: defaultPreview.PreviewToken})
	require.NoError(t, err)
	workspace, err := service.repositoryPath(repository.UUID)
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(workspace, "stacks", "app", "compose.yaml"))
	require.NoFileExists(t, filepath.Join(workspace, "stacks", "app", ".env"))
	repo, err := gitclient.PlainOpen(workspace)
	require.NoError(t, err)
	requireGitFileContent(t, repo, "main", "stacks/app/compose.yaml", "services:\n  app:\n    image: alpine:3.22\n")
	tree, err := repositoryCommitTree(repo, "main")
	require.NoError(t, err)
	_, err = tree.File("stacks/app/.env")
	require.Error(t, err)
}

func TestEnvironmentTemplatesCanBeIncludedWithoutWeakeningSensitiveProtection(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte("services:\n  app:\n    image: alpine\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", ".env.example"), []byte("TOKEN=replace-me\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", ".env.production"), []byte("TOKEN=do-not-leak\n"), 0o600))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)
	_, err = service.UpdateBindingPolicy(binding.ID, BindingPolicyInput{IncludePatterns: []string{".env.example", ".env.production"}})
	require.NoError(t, err)

	preview, err := service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	require.Equal(t, 2, preview.Changed)
	require.Equal(t, 1, preview.Skipped)
	statuses := make(map[string]string, len(preview.Entries))
	for _, entry := range preview.Entries {
		statuses[entry.Path] = entry.Status
	}
	require.Equal(t, "add", statuses[".env.example"])
	require.Equal(t, "skipped_sensitive", statuses[".env.production"])
}

func TestComposeOnlyExplicitTemplatesDoNotTraverseProtectedSubdirectories(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := t.TempDir()
	protectedFS := denyReadDirFS{FileSystem: filesystem.NewLocal(stackRoot), baseName: "secret"}
	service.ConfigureStackAccess(func(host, stackPath string) (filesystem.FileSystem, string, error) {
		if host == "local" && stackPath == "compose/app" {
			return protectedFS, "app", nil
		}
		return nil, "", os.ErrNotExist
	}, func() []string { return []string{"local"} }, filepath.Join(t.TempDir(), "backups"))
	repository := prepareBindingRepository(t, service)

	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app", "secret"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte("services:\n  app:\n    image: alpine\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", ".env.example"), []byte("TOKEN=replace-me\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", ".env.template"), []byte("PORT=8080\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "secret", "private.key"), []byte("do-not-read\n"), 0o600))

	binding, err := service.CreateBinding(BindingInput{
		RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app",
		ComposeSelectionMode: composeSelectionSelected, SelectedComposePaths: []string{"compose.yaml"},
	})
	require.NoError(t, err)
	_, err = service.UpdateBindingPolicy(binding.ID, BindingPolicyInput{
		Profile: syncProfileComposeOnly,
		// Explicit template rules must override a broad environment exclusion,
		// without authorizing a recursive search through unrelated directories.
		IncludePatterns: []string{".env.example", ".env.template"},
		ExcludePatterns: []string{".env*"},
	})
	require.NoError(t, err)

	preview, err := service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	require.Equal(t, 3, preview.Changed)
	require.Zero(t, preview.Skipped)
	paths := make(map[string]struct{}, len(preview.Entries))
	for _, entry := range preview.Entries {
		paths[entry.Path] = struct{}{}
	}
	require.Contains(t, paths, "compose.yaml")
	require.Contains(t, paths, ".env.example")
	require.Contains(t, paths, ".env.template")
	require.NotContains(t, paths, "secret/private.key")

	result, err := service.ExportBinding(context.Background(), binding.ID, TransferInput{PreviewToken: preview.PreviewToken})
	require.NoError(t, err)
	require.NotEmpty(t, result.CommitSHA)
	workspace, err := service.repositoryPath(repository.UUID)
	require.NoError(t, err)
	repo, err := gitclient.PlainOpen(workspace)
	require.NoError(t, err)
	tree, err := repositoryCommitTree(repo, "main")
	require.NoError(t, err)
	for _, path := range []string{"stacks/app/compose.yaml", "stacks/app/.env.example", "stacks/app/.env.template"} {
		_, err = tree.File(path)
		require.NoError(t, err, path)
	}
	_, err = tree.File("stacks/app/secret/private.key")
	require.Error(t, err)
}

func TestComposeOnlyPushIgnoresMutableUnselectedFiles(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app", "runtime"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte("services:\n  app:\n    image: alpine\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", ".env.example"), []byte("TOKEN=replace-me\n"), 0o644))
	runtimeLog := filepath.Join(stackRoot, "app", "runtime", "application.log")
	require.NoError(t, os.WriteFile(runtimeLog, []byte("before preview\n"), 0o644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)
	_, err = service.UpdateBindingPolicy(binding.ID, BindingPolicyInput{Profile: syncProfileComposeOnly})
	require.NoError(t, err)

	preview, err := service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	require.Equal(t, 2, preview.Changed)
	require.Zero(t, preview.Skipped)
	for _, entry := range preview.Entries {
		require.NotEqual(t, "runtime/application.log", entry.Path)
	}

	// Application data may legitimately change between preview and click. It
	// is outside this allow-list profile and must not invalidate the push.
	require.NoError(t, os.WriteFile(runtimeLog, []byte("changed after preview and still ignored\n"), 0o644))
	result, err := service.ExportBinding(context.Background(), binding.ID, TransferInput{PreviewToken: preview.PreviewToken})
	require.NoError(t, err)
	require.NotEmpty(t, result.CommitSHA)
}

func TestManualExportRecordsConfiguredIdentityAndOrigin(t *testing.T) {
	service, _ := testService(t, true)
	service.ConfigureCommitProvenance("homelab-primary")
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	repository.CommitAuthorName = "Dockman Production"
	repository.CommitAuthorEmail = "dockman@example.test"
	require.NoError(t, service.store.SaveRepository(&repository))
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte("services:\n  app:\n    image: alpine\n"), 0o644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)
	preview, err := service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	result, err := service.ExportBinding(context.Background(), binding.ID, TransferInput{PreviewToken: preview.PreviewToken})
	require.NoError(t, err)

	workspace, err := service.repositoryPath(repository.UUID)
	require.NoError(t, err)
	repo, err := gitclient.PlainOpen(workspace)
	require.NoError(t, err)
	commit, err := repo.CommitObject(plumbing.NewHash(result.CommitSHA))
	require.NoError(t, err)
	require.Equal(t, "Dockman Production", commit.Author.Name)
	require.Equal(t, "dockman@example.test", commit.Author.Email)
	require.Contains(t, commit.Message, `Dockman-Origin: instance="homelab-primary"`)
	require.Contains(t, commit.Message, `host="local"`)
	require.Contains(t, commit.Message, `binding="`+binding.ID+`"`)
	require.Contains(t, commit.Message, `stack="compose/app"`)
}

func TestComposeOnlyDoesNotOpenUnselectedDirectories(t *testing.T) {
	stackRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "secrets"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "compose.yaml"), []byte("services: {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, ".env.example"), []byte("TOKEN=replace-me\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "secrets", "private.key"), []byte("secret\n"), 0o600))
	policy := syncPolicy{
		profile: syncProfileComposeOnly,
		compose: map[string]struct{}{"compose.yaml": {}},
		// A basename rule used to make the collector search every directory,
		// including protected application data, merely to find this template.
		includes: mustRules([]string{".env.example"}),
	}

	files, err := collectStackFiles(denyReadDirFS{FileSystem: filesystem.NewLocal(stackRoot), baseName: "secrets"}, ".", false, policy)
	require.NoError(t, err)
	require.NotNil(t, files["compose.yaml"].open)
	require.NotNil(t, files[".env.example"].open)
	require.NotContains(t, files, "secrets")
	require.NotContains(t, files, "secrets/private.key")
}

func TestComposeOnlyExplicitFileDoesNotOpenItsUnrelatedSiblingTree(t *testing.T) {
	stackRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "config", "locked"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "compose.yaml"), []byte("services: {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "config", "application.conf"), []byte("enabled=true\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "config", "locked", "secret.conf"), []byte("secret=true\n"), 0o600))
	policy := syncPolicy{
		profile:          syncProfileComposeOnly,
		compose:          map[string]struct{}{"compose.yaml": {}},
		includes:         mustRules([]string{"config/application.conf"}),
		excludes:         mustRules([]string{"config/**"}),
		selectionEnabled: true,
		selectedRoots:    map[string]struct{}{".": {}},
	}

	files, err := collectStackFiles(denyReadDirFS{FileSystem: filesystem.NewLocal(stackRoot), baseName: "locked"}, ".", false, policy)
	require.NoError(t, err)
	require.NotNil(t, files["compose.yaml"].open)
	require.NotNil(t, files["config/application.conf"].open)
	require.NotContains(t, files, "config/locked")
	require.NotContains(t, files, "config/locked/secret.conf")
}

func TestComposeOnlyRepositoryDoesNotLoadUnselectedGitTrees(t *testing.T) {
	repositoryRoot := t.TempDir()
	repository, err := gitclient.PlainInit(repositoryRoot, false)
	require.NoError(t, err)
	commitTestFile(t, repository, repositoryRoot, "compose.yaml", "services: {}\n")
	head, err := repository.Head()
	require.NoError(t, err)
	commit, err := repository.CommitObject(head.Hash())
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)

	// Missing tree objects make accidental traversal deterministic. Model the
	// reported 20,000-directory repository: the collector succeeds only when
	// Compose-only rejects every unrelated subtree before asking go-git to load
	// it, and returns only the selected Compose manifest.
	synthetic := *tree
	for index := 0; index < 20_000; index++ {
		synthetic.Entries = append(synthetic.Entries, object.TreeEntry{Name: fmt.Sprintf("large-data-%05d", index), Mode: filemode.Dir, Hash: plumbing.ZeroHash})
	}
	policy := syncPolicy{profile: syncProfileComposeOnly, compose: map[string]struct{}{"compose.yaml": {}}}
	policy.includes = mustRules([]string{".env.example"})

	files, err := collectRepositoryTreeFiles(repository, &synthetic, ".", false, policy)
	require.NoError(t, err)
	require.NotNil(t, files["compose.yaml"].open)
	require.Len(t, files, 1)
}

func TestPreviewTokenIgnoresSkippedMetadataButProtectsTransferableFiles(t *testing.T) {
	base := TransferPreview{BindingID: "binding", Direction: "stack_to_repository", DeletionMode: "non_destructive", Entries: []PreviewEntry{
		{Path: "compose.yaml", Status: "add", SourceSHA: "compose-v1", Size: 12},
		{Path: "runtime.log", Status: "skipped_type", Size: 10},
	}}
	mutableSkipped := base
	mutableSkipped.Entries = append([]PreviewEntry(nil), base.Entries...)
	mutableSkipped.Entries[1].Size = 100_000
	require.Equal(t, previewToken(base), previewToken(mutableSkipped))

	changedCompose := base
	changedCompose.Entries = append([]PreviewEntry(nil), base.Entries...)
	changedCompose.Entries[0].SourceSHA = "compose-v2"
	require.NotEqual(t, previewToken(base), previewToken(changedCompose))
}

func TestRepositoryBindingPathsCannotOverlap(t *testing.T) {
	require.True(t, pathsOverlap(".", "stacks/app"))
	require.True(t, pathsOverlap("stacks", "stacks/app"))
	require.True(t, pathsOverlap("stacks/app", "stacks/app"))
	require.False(t, pathsOverlap("stacks/app", "stacks/database"))
}

func TestSelectedParentBindingAllowsDisjointNestedFolderLink(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := t.TempDir()
	service.ConfigureStackAccess(func(host, stackPath string) (filesystem.FileSystem, string, error) {
		if host != "local" || (stackPath != "compose" && !strings.HasPrefix(stackPath, "compose/")) {
			return nil, "", os.ErrNotExist
		}
		root := strings.TrimPrefix(stackPath, "compose")
		root = strings.TrimPrefix(root, "/")
		if root == "" {
			root = "."
		}
		return filesystem.NewLocal(stackRoot), root, nil
	}, func() []string { return []string{"local"} }, filepath.Join(t.TempDir(), "backups"))
	for _, directory := range []string{"group/alpha", "group/beta"} {
		require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, filepath.FromSlash(directory)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(stackRoot, filepath.FromSlash(directory), "compose.yml"), []byte("services: {}\n"), 0o644))
	}

	firstRepository := prepareBindingRepository(t, service)
	remotePath, _ := createTestRemote(t)
	secondRepository := Repository{UUID: uuid.NewString(), Name: "binding-repository-second-branch", Provider: "test", RemoteURL: remotePath, DefaultBranch: "main", Mode: "managed", Status: "cloning"}
	require.NoError(t, service.store.SaveRepository(&secondRepository))
	require.NoError(t, service.cloneRepository(context.Background(), secondRepository))
	secondRepository.Status = "ready"
	require.NoError(t, service.store.SaveRepository(&secondRepository))

	parent, err := service.CreateBinding(BindingInput{
		RepositoryID: firstRepository.UUID, Host: "local", StackPath: "compose/group", SubPath: "stacks",
		ComposeSelectionMode: composeSelectionSelected, SelectedComposePaths: []string{"alpha/compose.yml"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"alpha/compose.yml"}, parent.SelectedComposePaths)

	child, err := service.CreateBinding(BindingInput{
		RepositoryID: secondRepository.UUID, Host: "local", StackPath: "compose/group/beta", SubPath: ".",
	})
	require.NoError(t, err, "an unowned sibling subtree must be linkable to another repository branch")
	require.Equal(t, "compose/group/beta", child.StackPath)

	_, err = service.UpdateBindingComposeSelection(parent.ID, BindingComposeSelectionInput{Mode: composeSelectionAll})
	require.ErrorContains(t, err, "overlaps files selected by existing link")
	_, err = service.EnableGitStackSynchronization(parent.ID, "beta/compose.yml")
	require.ErrorContains(t, err, "overlaps files selected by existing link")
	_, err = service.UpdateBindingPolicy(parent.ID, BindingPolicyInput{Profile: syncProfileComposeConfig, IncludePatterns: []string{"beta/config/**"}})
	require.ErrorContains(t, err, "overlaps files selected by existing link")
}

func TestNestedStackFoldersAreOfferedAsIndependentLinkTargets(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	for _, directory := range []string{"group/alpha", "group/beta"} {
		require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, filepath.FromSlash(directory)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(stackRoot, filepath.FromSlash(directory), "compose.yml"), []byte("services: {}\n"), 0o644))
	}
	targets, err := service.ListStackTargets()
	require.NoError(t, err)
	byPath := make(map[string]StackTarget, len(targets))
	for _, target := range targets {
		byPath[target.Path] = target
	}
	require.Equal(t, []string{"alpha/compose.yml", "beta/compose.yml"}, byPath["compose/group"].ComposePaths)
	require.Equal(t, []string{"compose.yml"}, byPath["compose/group/alpha"].ComposePaths)
	require.Equal(t, []string{"compose.yml"}, byPath["compose/group/beta"].ComposePaths)
}

func TestCompleteStacksFolderIsDiscoveredAndLinkedOnce(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	for _, stack := range []string{"app", "media/nested"} {
		directory := filepath.Join(stackRoot, filepath.FromSlash(stack))
		require.NoError(t, os.MkdirAll(directory, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "compose.yaml"), []byte("services:\n  app:\n    image: alpine\n"), 0644))
	}

	targets, err := service.ListStackTargets()
	require.NoError(t, err)
	require.NotEmpty(t, targets)
	require.Equal(t, "compose", targets[0].Path)
	require.Equal(t, "all_stacks", targets[0].Scope)
	require.Equal(t, 2, targets[0].StackCount)
	require.Equal(t, []string{"app/compose.yaml", "media/nested/compose.yaml"}, targets[0].ComposePaths)

	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"})
	require.NoError(t, err)
	require.Equal(t, targets[0].ComposePaths, binding.ComposePaths)
	require.Equal(t, composeSelectionAll, binding.ComposeSelectionMode)
	require.Equal(t, binding.ComposePaths, binding.SelectedComposePaths)
}

func TestAllStacksDiscoveryDoesNotLoseSiblingStacksBehindLargeDataTree(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)

	// This directory sorts before the stacks and contains enough children to
	// exhaust the bounded legacy depth-first scan.
	cacheRoot := filepath.Join(stackRoot, "aaa-data")
	require.NoError(t, os.MkdirAll(cacheRoot, 0o755))
	for index := 0; index < 1100; index++ {
		require.NoError(t, os.Mkdir(filepath.Join(cacheRoot, fmt.Sprintf("cache-%04d", index)), 0o755))
	}

	for index := 0; index < 19; index++ {
		directory := filepath.Join(stackRoot, fmt.Sprintf("stack-%02d", index))
		require.NoError(t, os.MkdirAll(directory, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "compose.yml"), []byte("services:\n  app:\n    image: alpine\n"), 0o644))
	}

	targets, err := service.ListStackTargets()
	require.NoError(t, err)
	require.NotEmpty(t, targets)
	require.Equal(t, "all_stacks", targets[0].Scope)
	require.Equal(t, 19, targets[0].StackCount)
	require.Len(t, targets[0].ComposePaths, 19)
}

func TestComposeSelectionPersistsAndSkipsUnselectedStackTrees(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	for _, stack := range []string{"alpha", "beta"} {
		directory := filepath.Join(stackRoot, stack)
		require.NoError(t, os.MkdirAll(directory, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "compose.yaml"), []byte("services: {}\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "settings.json"), []byte(`{"stack":"`+stack+`"}`), 0o644))
	}
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"})
	require.NoError(t, err)

	updated, err := service.UpdateBindingComposeSelection(binding.ID, BindingComposeSelectionInput{Mode: composeSelectionSelected, ComposePaths: []string{"alpha/compose.yaml"}})
	require.NoError(t, err)
	require.Equal(t, composeSelectionSelected, updated.ComposeSelectionMode)
	require.Equal(t, []string{"alpha/compose.yaml"}, updated.SelectedComposePaths)

	preview, err := service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	require.Equal(t, 2, preview.Changed)
	for _, entry := range preview.Entries {
		require.NotContains(t, entry.Path, "beta/")
	}

	listed, err := service.ListBindings()
	require.NoError(t, err)
	require.Equal(t, []string{"alpha/compose.yaml"}, listed[0].SelectedComposePaths)

	_, err = service.UpdateBindingComposeSelection(binding.ID, BindingComposeSelectionInput{Mode: composeSelectionSelected, ComposePaths: []string{"missing/compose.yaml"}})
	require.ErrorContains(t, err, "no longer available")
}

func TestRefreshComposeCatalogAddsNewLocalStackUnselected(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	for _, stack := range []string{"alpha", "beta"} {
		directory := filepath.Join(stackRoot, stack)
		require.NoError(t, os.MkdirAll(directory, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "compose.yaml"), []byte("services: {}\n"), 0o644))
	}
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"})
	require.NoError(t, err)
	require.Equal(t, composeSelectionAll, binding.ComposeSelectionMode)

	gamma := filepath.Join(stackRoot, "gamma")
	require.NoError(t, os.MkdirAll(gamma, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(gamma, "compose.yml"), []byte("services: {}\n"), 0o644))
	refreshed, err := service.RefreshBindingComposeCatalog(binding.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"alpha/compose.yaml", "beta/compose.yaml", "gamma/compose.yml"}, refreshed.ComposePaths)
	require.Equal(t, composeSelectionSelected, refreshed.ComposeSelectionMode)
	require.Equal(t, []string{"alpha/compose.yaml", "beta/compose.yaml"}, refreshed.SelectedComposePaths)

	preview, err := service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	for _, entry := range preview.Entries {
		require.NotContains(t, entry.Path, "gamma/", "new local stacks must not enter synchronization before approval")
	}

	approved, err := service.UpdateBindingComposeSelection(binding.ID, BindingComposeSelectionInput{
		Mode: composeSelectionAll, ComposePaths: refreshed.ComposePaths,
	})
	require.NoError(t, err)
	require.Equal(t, composeSelectionAll, approved.ComposeSelectionMode)
	require.Contains(t, approved.SelectedComposePaths, "gamma/compose.yml")

	require.NoError(t, os.RemoveAll(gamma))
	withoutGamma, err := service.RefreshBindingComposeCatalog(binding.ID)
	require.NoError(t, err)
	require.NotContains(t, withoutGamma.ComposePaths, "gamma/compose.yml")
	require.NotContains(t, withoutGamma.SelectedComposePaths, "gamma/compose.yml")
}

func TestBindingAppliesComposeSelectionBeforeInitialExport(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	for _, stack := range []string{"alpha", "beta"} {
		directory := filepath.Join(stackRoot, stack)
		require.NoError(t, os.MkdirAll(directory, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "compose.yaml"), []byte("services:\n  "+stack+":\n    image: alpine\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "settings.json"), []byte(`{"stack":"`+stack+`"}`), 0o644))
	}

	binding, err := service.CreateBindingContext(context.Background(), BindingInput{
		RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks",
		InitialSync: "stack_to_repository", ComposeSelectionMode: composeSelectionSelected,
		SelectedComposePaths: []string{"alpha/compose.yaml"},
	})
	require.NoError(t, err)
	require.Equal(t, "exported", binding.InitialSyncState)
	require.Equal(t, composeSelectionSelected, binding.ComposeSelectionMode)
	require.Equal(t, []string{"alpha/compose.yaml"}, binding.SelectedComposePaths)

	check, err := gitclient.PlainClone(t.TempDir(), false, &gitclient.CloneOptions{URL: repository.RemoteURL, ReferenceName: plumbing.NewBranchReferenceName("main"), SingleBranch: true})
	require.NoError(t, err)
	requireGitFileContent(t, check, "main", "stacks/alpha/settings.json", `{"stack":"alpha"}`)
	tree, err := repositoryCommitTree(check, "main")
	require.NoError(t, err)
	_, err = tree.File("stacks/beta/compose.yaml")
	require.Error(t, err, "an unselected stack must not be exported during link initialization")
}

func TestBindingRejectsUnavailableInitialComposeSelection(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "alpha"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", "compose.yaml"), []byte("services: {}\n"), 0o644))

	_, err := service.CreateBinding(BindingInput{
		RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks",
		ComposeSelectionMode: composeSelectionSelected, SelectedComposePaths: []string{"missing/compose.yaml"},
	})
	require.ErrorContains(t, err, "no longer available")
	bindings, listErr := service.ListBindings()
	require.NoError(t, listErr)
	require.Empty(t, bindings)
}

func TestRelinkingPreservesAnExistingComposeSelection(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	for _, stack := range []string{"alpha", "beta"} {
		directory := filepath.Join(stackRoot, stack)
		require.NoError(t, os.MkdirAll(directory, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "compose.yaml"), []byte("services: {}\n"), 0o644))
	}
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"})
	require.NoError(t, err)
	_, err = service.UpdateBindingComposeSelection(binding.ID, BindingComposeSelectionInput{Mode: composeSelectionSelected, ComposePaths: []string{"alpha/compose.yaml"}})
	require.NoError(t, err)
	require.NoError(t, service.store.DeleteBinding(binding.ID, false))

	relinked, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"})
	require.NoError(t, err)
	require.Equal(t, binding.ID, relinked.ID)
	require.Equal(t, composeSelectionSelected, relinked.ComposeSelectionMode)
	require.Equal(t, []string{"alpha/compose.yaml"}, relinked.SelectedComposePaths)
}

func TestComposeSelectionPrunesAutomaticDeploymentTargets(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	for _, stack := range []string{"alpha", "beta"} {
		directory := filepath.Join(stackRoot, stack)
		require.NoError(t, os.MkdirAll(directory, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "compose.yaml"), []byte("services: {}\n"), 0o644))
	}
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"})
	require.NoError(t, err)
	row, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	row.AutoSyncEnabled = true
	row.AutoDeployEnabled = true
	row.AutoDeployState = "watching"
	row.AutoDeployComposePaths = "alpha/compose.yaml\nbeta/compose.yaml"
	require.NoError(t, service.store.SaveBinding(&row))

	updated, err := service.UpdateBindingComposeSelection(binding.ID, BindingComposeSelectionInput{Mode: composeSelectionSelected, ComposePaths: []string{"alpha/compose.yaml"}})
	require.NoError(t, err)
	require.Equal(t, []string{"alpha/compose.yaml"}, updated.AutoDeployComposePaths)
	require.True(t, updated.AutoDeployEnabled)
}

type zeroStream struct{}

func (zeroStream) Read(buffer []byte) (int, error) {
	clear(buffer)
	return len(buffer), nil
}

func TestLargeFilesAreHashedAndTransferredWithBoundedBuffers(t *testing.T) {
	const size = int64(64 << 20)
	require.NoError(t, checkTransferLimit(1, size, size))
	file := transferFile{path: "large/config.bundle", size: size, mode: 0644, open: func() (io.ReadCloser, error) {
		return io.NopCloser(io.LimitReader(zeroStream{}, size)), nil
	}}
	var err error
	file.sha, err = hashTransferFile(file)
	require.NoError(t, err)
	require.NotEmpty(t, file.sha)
	require.NoError(t, streamTransferFile(file, io.Discard))
}

func TestStackImportUpdatesWritableExistingFileWhenAtomicReplacementIsDenied(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "app"), 0o755))
	destination := filepath.Join(root, "app", "compose.yaml")
	require.NoError(t, os.WriteFile(destination, []byte("services: {}\n"), 0o600))

	contents := "services:\n  app:\n    image: alpine:3.24\n"
	hash := sha256.Sum256([]byte(contents))
	file := transferFile{
		path: "app/compose.yaml",
		sha:  hex.EncodeToString(hash[:]),
		size: int64(len(contents)),
		mode: 0o644,
		open: func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(contents)), nil
		},
	}
	targetFS := denyAtomicReplacementFS{FileSystem: filesystem.NewLocal(root)}

	require.NoError(t, writeStackFiles(targetFS, ".", map[string]transferFile{file.path: file}))
	actual, err := os.ReadFile(destination)
	require.NoError(t, err)
	require.Equal(t, contents, string(actual))
	info, err := os.Stat(destination)
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(0o600), info.Mode().Perm(), "the compatibility write must preserve existing permissions")
}

func TestStackImportDoesNotUsePermissionFallbackToCreateMissingFile(t *testing.T) {
	root := t.TempDir()
	contents := "services: {}\n"
	hash := sha256.Sum256([]byte(contents))
	file := transferFile{
		path: "app/compose.yaml",
		sha:  hex.EncodeToString(hash[:]),
		size: int64(len(contents)),
		mode: 0o644,
		open: func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(contents)), nil
		},
	}
	targetFS := denyAtomicReplacementFS{FileSystem: filesystem.NewLocal(root)}

	err := writeStackFiles(targetFS, ".", map[string]transferFile{file.path: file})
	require.ErrorContains(t, err, "no writable regular file is available")
	_, statErr := os.Stat(filepath.Join(root, "app", "compose.yaml"))
	require.ErrorIs(t, statErr, fs.ErrNotExist)
}

func TestStackInventorySkipsUnreadableFileWithoutBlockingSiblingStacks(t *testing.T) {
	root := t.TempDir()
	for _, stack := range []string{"adguard", "whoami"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, stack), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, stack, "compose.yaml"), []byte("services: {}\n"), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "adguard", "AdGuardHome.yaml"), []byte("locked: true\n"), 0o600))
	targetFS := denyReadFS{FileSystem: filesystem.NewLocal(root), baseName: "AdGuardHome.yaml"}

	files, err := collectStackFiles(targetFS, ".", false)
	require.NoError(t, err)
	require.Equal(t, "permission", files["adguard/AdGuardHome.yaml"].skipReason)
	require.Nil(t, files["adguard/AdGuardHome.yaml"].open)
	require.NotNil(t, files["adguard/compose.yaml"].open)
	require.NotNil(t, files["whoami/compose.yaml"].open, "an unreadable AdGuard file must not hide another stack")
}

func TestStackInventoryCollapsesLargeDataDirectoryWithoutBlockingSiblingStacks(t *testing.T) {
	root := t.TempDir()
	for _, stack := range []string{"adguard", "whoami"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, stack), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, stack, "compose.yaml"), []byte("services: {}\n"), 0o644))
	}
	data := filepath.Join(root, "adguard", "data")
	require.NoError(t, os.MkdirAll(data, 0o755))
	for index := 0; index <= maxAutoDirectoryFiles; index++ {
		require.NoError(t, os.WriteFile(filepath.Join(data, fmt.Sprintf("query-%05d.log", index)), []byte("data\n"), 0o644))
	}
	policy := defaultSyncPolicy()
	policy.compose = map[string]struct{}{"adguard/compose.yaml": {}, "whoami/compose.yaml": {}}

	files, err := collectStackFiles(filesystem.NewLocal(root), ".", false, policy)
	require.NoError(t, err)
	require.Equal(t, "large_directory", files["adguard/data"].skipReason)
	require.True(t, files["adguard/data"].directory)
	require.NotNil(t, files["adguard/compose.yaml"].open)
	require.NotNil(t, files["whoami/compose.yaml"].open, "a large AdGuard data folder must not hide another stack")
	for path := range files {
		require.False(t, strings.HasPrefix(path, "adguard/data/"), "large directory children must be represented by one bounded marker")
	}
}

func TestStackInventoryLimitNamesOwningStackAndPath(t *testing.T) {
	policy := defaultSyncPolicy()
	policy.compose = map[string]struct{}{"adguard/compose.yaml": {}, "whoami/compose.yaml": {}}
	err := stackInventoryLimitError(policy, "adguard/config/filters.yaml", maxBindingFiles)
	require.ErrorContains(t, err, "stack adguard/compose.yaml")
	require.ErrorContains(t, err, "adguard/config/filters.yaml")
}

func TestGitImportPreviewSkipsUnreadableTargetAndKeepsOtherChangesTransferable(t *testing.T) {
	available := func(path, contents string) transferFile {
		hash := sha256.Sum256([]byte(contents))
		return transferFile{path: path, sha: hex.EncodeToString(hash[:]), size: int64(len(contents)), open: func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(contents)), nil
		}}
	}
	source := map[string]transferFile{
		"adguard/AdGuardHome.yaml": available("adguard/AdGuardHome.yaml", "new locked config\n"),
		"whoami/compose.yaml":      available("whoami/compose.yaml", "services:\n  app:\n    image: traefik/whoami\n"),
	}
	target := map[string]transferFile{
		"adguard/AdGuardHome.yaml": {path: "adguard/AdGuardHome.yaml", skipReason: "permission"},
		"whoami/compose.yaml":      available("whoami/compose.yaml", "services: {}\n"),
	}

	preview := buildPreview("binding", "repository_to_stack", source, target, map[string]string{
		"adguard/AdGuardHome.yaml": "previous",
		"whoami/compose.yaml":      target["whoami/compose.yaml"].sha,
	})
	require.Equal(t, 1, preview.Skipped)
	require.Equal(t, 1, preview.Changed)
	require.Equal(t, "skipped_permission", preview.Entries[0].Status)
	require.Equal(t, "modify", preview.Entries[1].Status)
}

func TestGitImportPreviewDoesNotWriteInsideAutomaticallySkippedDataDirectory(t *testing.T) {
	sourceFile := transferFile{path: "adguard/data/runtime.db", sha: "remote", size: 42, open: func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(strings.Repeat("x", 42))), nil
	}}
	preview := buildPreview("binding", "repository_to_stack", map[string]transferFile{sourceFile.path: sourceFile}, map[string]transferFile{
		"adguard/data": {path: "adguard/data", skipReason: "large_directory", directory: true},
	})

	require.Equal(t, 0, preview.Changed)
	require.Equal(t, 1, preview.Skipped)
	require.Equal(t, "skipped_large_directory", preview.Entries[0].Status)
}

func TestTransferInventoryRejectsSpecialFiles(t *testing.T) {
	require.True(t, isTransferFile(0644))
	require.False(t, isTransferFile(os.ModeSocket|0600))
	require.False(t, isTransferFile(os.ModeNamedPipe|0600))
	require.False(t, isTransferFile(os.ModeDevice|0600))
}

func TestStackInventoryReportsOversizedFilesWithoutReadingThem(t *testing.T) {
	stackRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "compose.yaml"), []byte("services:\n  app:\n    image: alpine\n"), 0644))
	largePath := filepath.Join(stackRoot, "generated.bundle")
	require.NoError(t, os.WriteFile(largePath, nil, 0644))
	require.NoError(t, os.Truncate(largePath, maxBindingFileSize+1))

	files, err := collectStackFiles(filesystem.NewLocal(stackRoot), ".", false)
	require.NoError(t, err)
	require.Equal(t, "oversized", files["generated.bundle"].skipReason)
	require.Nil(t, files["generated.bundle"].open)
	preview := buildPreview("binding", "stack_to_repository", files, nil)
	require.Equal(t, 1, preview.Skipped)
	require.Contains(t, preview.Entries, PreviewEntry{Path: "generated.bundle", Status: "skipped_oversized", Size: maxBindingFileSize + 1})
}

func TestPreviewBlocksOverwritingAChangedSynchronizationTarget(t *testing.T) {
	available := func(sha string) transferFile {
		return transferFile{sha: sha, open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("content")), nil }}
	}
	baseline := map[string]string{"compose.yaml": "base"}

	remoteChanged := buildPreview("binding", "stack_to_repository", map[string]transferFile{"compose.yaml": available("base")}, map[string]transferFile{"compose.yaml": available("remote")}, baseline)
	require.Equal(t, 1, remoteChanged.Conflicts)
	require.Equal(t, "conflict", remoteChanged.Entries[0].Status)

	localChanged := buildPreview("binding", "stack_to_repository", map[string]transferFile{"compose.yaml": available("local")}, map[string]transferFile{"compose.yaml": available("base")}, baseline)
	require.Zero(t, localChanged.Conflicts)
	require.Equal(t, "modify", localChanged.Entries[0].Status)

	bothEqual := buildPreview("binding", "stack_to_repository", map[string]transferFile{"compose.yaml": available("same")}, map[string]transferFile{"compose.yaml": available("same")}, baseline)
	require.Zero(t, bothEqual.Conflicts)
	require.Equal(t, 1, bothEqual.Unchanged)

	unknownBaseline := buildPreview("binding", "stack_to_repository", map[string]transferFile{"compose.yaml": available("local")}, map[string]transferFile{"compose.yaml": available("remote")}, nil)
	require.Equal(t, 1, unknownBaseline.Conflicts)
	require.Equal(t, "no_baseline", unknownBaseline.Entries[0].ConflictKind)
}

func TestPreviewPreservesGitDeletionAndFlagsModifiedLocalOrphan(t *testing.T) {
	available := func(content string) transferFile {
		hash := sha256.Sum256([]byte(content))
		return transferFile{sha: hex.EncodeToString(hash[:]), size: int64(len(content)), open: func() (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader(content)), nil
		}}
	}
	baseSHA := available("base").sha
	baseline := map[string]string{"alpha/compose.yml": baseSHA, "alpha/config.yml": baseSHA}
	local := map[string]transferFile{
		"alpha/compose.yml": available("base"),
		"alpha/config.yml":  available("locally modified"),
		"local-only.txt":    available("never synchronized"),
	}
	preview := buildPreview("binding", "repository_to_stack", map[string]transferFile{}, local, baseline)

	require.Equal(t, 2, preview.Preserved)
	require.Equal(t, 2, preview.Changed)
	require.Equal(t, 1, preview.Conflicts)
	entries := map[string]PreviewEntry{}
	for _, entry := range preview.Entries {
		require.NotEqual(t, "local-only.txt", entry.Path)
		entries[entry.Path] = entry
	}
	require.Equal(t, "deleted_on_git", entries["alpha/compose.yml"].Status)
	require.Equal(t, "conflict", entries["alpha/config.yml"].Status)
	require.Equal(t, "source_deleted_destination_changed", entries["alpha/config.yml"].ConflictKind)
}

func TestTransferSelectionLeavesUnresolvedConflictsPending(t *testing.T) {
	available := func(sha string) transferFile {
		return transferFile{sha: sha, open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("content")), nil }}
	}
	source := map[string]transferFile{
		"safe.yaml": available("safe-new"), "one.yaml": available("one-new"),
		"two.yaml": available("two-new"), "three.yaml": available("three-new"),
	}
	target := map[string]transferFile{
		"safe.yaml": available("safe-old"), "one.yaml": available("one-remote"),
		"two.yaml": available("two-remote"), "three.yaml": available("three-remote"),
	}
	baseline := map[string]string{"safe.yaml": "safe-old", "one.yaml": "one-old", "two.yaml": "two-old", "three.yaml": "three-old"}
	preview := buildPreview("binding", "stack_to_repository", source, target, baseline)
	require.Equal(t, 3, preview.Conflicts)

	selected, pending, err := selectedTransferFiles(preview, source, []string{"two.yaml"}, nil)
	require.NoError(t, err)
	require.Equal(t, 2, pending)
	require.ElementsMatch(t, []string{"safe.yaml", "two.yaml"}, mapKeys(selected))

	next := baselineAfterTransfer(baseline, source, target, selected)
	require.Equal(t, "safe-new", next["safe.yaml"])
	require.Equal(t, "two-new", next["two.yaml"])
	require.Equal(t, "one-old", next["one.yaml"])
	require.Equal(t, "three-old", next["three.yaml"])

	limited, pending, err := selectedTransferFiles(preview, source, []string{"two.yaml"}, []string{"two.yaml"})
	require.NoError(t, err)
	require.Equal(t, 2, pending)
	require.ElementsMatch(t, []string{"two.yaml"}, mapKeys(limited))

	limitedSafe, pending, err := selectedTransferFiles(preview, source, nil, []string{"safe.yaml"})
	require.NoError(t, err)
	require.Equal(t, 3, pending)
	require.ElementsMatch(t, []string{"safe.yaml"}, mapKeys(limitedSafe))
}

func TestPartialExportResolvesOnlyApprovedConflict(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	stackPath := filepath.Join(stackRoot, "app")
	require.NoError(t, os.MkdirAll(stackPath, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(stackPath, "compose.yaml"), []byte("services: {}\n"), 0644))
	for _, name := range []string{"one.yaml", "two.yaml", "three.yaml"} {
		require.NoError(t, os.WriteFile(filepath.Join(stackPath, name), []byte("value: baseline\n"), 0644))
	}
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)
	initial, err := service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	_, err = service.ExportBinding(context.Background(), binding.ID, TransferInput{PreviewToken: initial.PreviewToken})
	require.NoError(t, err)

	for _, name := range []string{"one.yaml", "two.yaml", "three.yaml"} {
		require.NoError(t, os.WriteFile(filepath.Join(stackPath, name), []byte("value: dockman\n"), 0644))
	}
	externalPath := t.TempDir()
	external, err := gitclient.PlainClone(externalPath, false, &gitclient.CloneOptions{URL: repository.RemoteURL, ReferenceName: plumbing.NewBranchReferenceName("main"), SingleBranch: true})
	require.NoError(t, err)
	for _, name := range []string{"one.yaml", "two.yaml", "three.yaml"} {
		commitTestFile(t, external, externalPath, filepath.ToSlash(filepath.Join("stacks", "app", name)), "value: git\n")
	}
	require.NoError(t, external.Push(&gitclient.PushOptions{}))
	_, err = service.PullRepository(context.Background(), repository.UUID)
	require.NoError(t, err)

	preview, err := service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	require.Equal(t, 3, preview.Conflicts)
	result, err := service.ExportBinding(context.Background(), binding.ID, TransferInput{PreviewToken: preview.PreviewToken, ResolvedPaths: []string{"two.yaml"}})
	require.NoError(t, err)
	require.Contains(t, result.Message, "2 conflict(s) remain pending")

	workspace, err := service.repositoryPath(repository.UUID)
	require.NoError(t, err)
	repo, err := gitclient.PlainOpen(workspace)
	require.NoError(t, err)
	requireGitFileContent(t, repo, "main", "stacks/app/two.yaml", "value: dockman\n")
	requireGitFileContent(t, repo, "main", "stacks/app/one.yaml", "value: git\n")

	remaining, err := service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	require.Equal(t, 2, remaining.Conflicts)

	// Resolve the remaining files with mixed decisions, as the automatic
	// conflict dialog does: Git wins for one file and Dockman for the other.
	gitPreview, err := service.PreviewBinding(binding.ID, "repository_to_stack", TransferInput{})
	require.NoError(t, err)
	_, err = service.ImportBinding(context.Background(), binding.ID, TransferInput{
		PreviewToken: gitPreview.PreviewToken, ResolvedPaths: []string{"one.yaml"}, SelectedPaths: []string{"one.yaml"},
	})
	require.NoError(t, err)
	dockmanPreview, err := service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	_, err = service.ExportBinding(context.Background(), binding.ID, TransferInput{
		PreviewToken: dockmanPreview.PreviewToken, ResolvedPaths: []string{"three.yaml"}, SelectedPaths: []string{"three.yaml"},
	})
	require.NoError(t, err)
	resolved, err := service.PreviewBinding(binding.ID, "repository_to_stack", TransferInput{})
	require.NoError(t, err)
	require.Zero(t, resolved.Conflicts)
}

func TestComparisonSideRejectsBinaryAndOversizedFiles(t *testing.T) {
	textFile := transferFile{sha: "text", size: 5, open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("hello")), nil }}
	side, comparable, reason, err := comparisonSide(textFile)
	require.NoError(t, err)
	require.True(t, comparable)
	require.Empty(t, reason)
	require.Equal(t, "hello", side.Content)

	binaryFile := transferFile{sha: "binary", size: 3, open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("a\x00b")), nil }}
	_, comparable, reason, err = comparisonSide(binaryFile)
	require.NoError(t, err)
	require.False(t, comparable)
	require.Contains(t, reason, "Binary")

	oversized := transferFile{sha: "large", size: maxComparisonFileSize + 1}
	_, comparable, reason, err = comparisonSide(oversized)
	require.NoError(t, err)
	require.False(t, comparable)
	require.Contains(t, reason, "limit")
}

func TestBindingUnlinkCleansBackups(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte("services: {}\n"), 0o644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)

	bindingBackupRoot := filepath.Join(service.backupRoot, binding.ID)
	archiveRoot := filepath.Join(service.backupRoot, "archives", binding.ID)
	require.NoError(t, os.MkdirAll(archiveRoot, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(archiveRoot, "orphan.tar.gz"), []byte("archive"), 0o600))

	require.NoError(t, service.DeleteBinding(binding.ID, false))
	_, err = os.Stat(bindingBackupRoot)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(archiveRoot)
	require.ErrorIs(t, err, os.ErrNotExist)

	// Repository deletion also cleans backups belonging to a previously
	// archived link, including archives left by an interrupted older version.
	require.NoError(t, os.MkdirAll(bindingBackupRoot, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(bindingBackupRoot, "legacy.tar.gz"), []byte("backup"), 0o600))
	require.NoError(t, service.DeleteRepository(repository.UUID))
	_, err = os.Stat(bindingBackupRoot)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func mapKeys(values map[string]transferFile) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func TestDockmanIgnoreExcludesFilesAndFolders(t *testing.T) {
	stackRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "cache"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, ".dockmanignore"), []byte("cache/\n*.log\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "compose.yaml"), []byte("services:\n  app:\n    image: alpine\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "runtime.log"), []byte("generated"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "cache", "large.bin"), []byte("generated"), 0644))

	files, err := collectStackFiles(filesystem.NewLocal(stackRoot), ".", false)
	require.NoError(t, err)
	require.Equal(t, "excluded", files["cache"].skipReason)
	require.Equal(t, "excluded", files["runtime.log"].skipReason)
	require.NotContains(t, files, "cache/large.bin")
	require.Contains(t, files, ".dockmanignore")
	require.Contains(t, files, "compose.yaml")
}

func TestComposeConfigPolicySupportsIncludesAndExcludes(t *testing.T) {
	stackRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "scripts", "nested"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "runtime", "cache"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "compose.yaml"), []byte("services: {}\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "photo.jpg"), []byte("not configuration"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "scripts", "nested", "prepare.py"), []byte("print('ok')\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "runtime", "cache", "state.json"), []byte("{}\n"), 0644))
	policy := syncPolicy{profile: syncProfileComposeConfig, includes: mustRules([]string{"scripts/**"}), excludes: mustRules([]string{"runtime/**"})}

	files, err := collectStackFiles(filesystem.NewLocal(stackRoot), ".", false, policy)
	require.NoError(t, err)
	require.NotNil(t, files["compose.yaml"].open)
	require.Equal(t, "type", files["photo.jpg"].skipReason)
	require.NotNil(t, files["scripts/nested/prepare.py"].open)
	require.Equal(t, "excluded", files["runtime"].skipReason)
	require.NotContains(t, files, "runtime/cache/state.json")
}

func TestComposeOnlyPolicyIncludesManifestsTemplatesAndExplicitAdditions(t *testing.T) {
	stackRoot := t.TempDir()
	files := make(map[string]string)
	files["compose.yaml"] = "services: {}\n"
	files["docker-compose.yml"] = "services: {}\n"
	files["override.yml"] = "services: {}\n"
	files["application.yaml"] = "enabled: true\n"
	files[".env.example"] = "TOKEN=replace-me\n"
	files[".env.sample"] = "TOKEN=replace-me\n"
	files[".env.prod.template"] = "TOKEN=replace-me\n"
	files["settings.json"] = "{}\n"
	files["application.conf"] = "enabled=true\n"
	files["notes.md"] = "documentation\n"
	for name, contents := range files {
		require.NoError(t, os.WriteFile(filepath.Join(stackRoot, name), []byte(contents), 0o644))
	}
	policy := syncPolicy{
		profile:  syncProfileComposeOnly,
		includes: mustRules([]string{"application.conf"}),
		compose:  map[string]struct{}{"compose.yaml": {}, "docker-compose.yml": {}},
	}

	collected, err := collectStackFiles(filesystem.NewLocal(stackRoot), ".", false, policy)
	require.NoError(t, err)
	for _, name := range []string{"compose.yaml", "docker-compose.yml", ".env.example", ".env.sample", ".env.prod.template", "application.conf"} {
		require.NotNil(t, collected[name].open, name)
	}
	for _, name := range []string{"override.yml", "application.yaml", "settings.json", "notes.md"} {
		require.NotContains(t, collected, name)
	}
}

func TestComposeOnlyPolicyFailsClosedWithoutCatalog(t *testing.T) {
	stackRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "secrets", "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "secrets", "nested", "compose.yml"), []byte("services: {}\n"), 0o644))

	_, err := collectStackFiles(denyReadDirFS{FileSystem: filesystem.NewLocal(stackRoot), baseName: "secrets"}, ".", false, syncPolicy{profile: syncProfileComposeOnly})
	require.ErrorContains(t, err, "no Compose stack is catalogued")
	require.ErrorContains(t, err, "refresh the Compose catalog")
}

func TestComposeOnlyAutomaticNewStackDiscoveryWorksFromEmptyCatalog(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0o755))
	remoteChange(t, repository.RemoteURL, "stacks/app/new/compose.yml", "services:\n  app:\n    image: alpine\n")
	_, err := service.PullRepository(context.Background(), repository.UUID)
	require.NoError(t, err)
	binding := StackBinding{
		UUID: uuid.NewString(), RepositoryUUID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app",
		SyncProfile: syncProfileComposeOnly, ComposeSelectionMode: composeSelectionSelected,
		AutoSyncEnabled: true, AutoDeployEnabled: true, AutoDeployNewStacks: true, Enabled: true,
	}
	require.NoError(t, service.store.SaveBinding(&binding))

	_, source, target, err := service.loadTransferTrees(binding.UUID, "repository_to_stack", TransferInput{automation: true})
	require.NoError(t, err)
	require.NotNil(t, source["new/compose.yml"].open)
	require.NotContains(t, target, "new/compose.yml")
	stored, err := service.store.GetBinding(binding.UUID)
	require.NoError(t, err)
	require.Empty(t, stored.ComposePaths, "discovery must remain transactional until the automatic preview is accepted")
}

func TestLargeDirectoryGuardRemainsActiveWithoutComposeCatalog(t *testing.T) {
	result := map[string]transferFile{
		"data/file.bin": {path: "data/file.bin"},
	}
	require.True(t, autoExcludeLargeDirectory(result, syncPolicy{profile: syncProfileComposeOnly}, "data", maxAutoDirectoryFiles+1))
	require.NotContains(t, result, "data/file.bin")
	require.Equal(t, "large_directory", result["data"].skipReason)
}

func TestComposeOnlyPolicyUsesCatalogAndTraversesPathIncludes(t *testing.T) {
	stackRoot := t.TempDir()
	for _, directory := range []string{"known", "config"} {
		require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, directory), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "compose.yml"), []byte("services: {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "known", "compose.yml"), []byte("services: {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "known", ".env.example"), []byte("TOKEN=example\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "config", "compose.yml"), []byte("services: {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "config", "application.conf"), []byte("enabled=true\n"), 0o644))

	policy := syncPolicy{
		profile:  syncProfileComposeOnly,
		includes: mustRules([]string{"config/*.conf"}),
		compose:  map[string]struct{}{"known/compose.yml": {}},
	}
	files, err := collectStackFiles(filesystem.NewLocal(stackRoot), ".", false, policy)
	require.NoError(t, err)
	require.NotContains(t, files, "compose.yml", "a root Compose name outside the catalog must stay excluded")
	require.NotContains(t, files, "config/compose.yml", "an explicitly traversed directory must not admit an uncatalogued Compose")
	require.NotNil(t, files["known/compose.yml"].open)
	require.NotNil(t, files["known/.env.example"].open)
	require.NotNil(t, files["config/application.conf"].open)
}

func TestRootComposeSelectionDoesNotSelectNestedStacks(t *testing.T) {
	policy := syncPolicy{
		selectionEnabled: true,
		selectedRoots: map[string]struct{}{
			".":        {},
			"selected": {},
		},
	}

	selected, traverse := policy.selectsPath("compose.yaml", false)
	require.True(t, selected)
	require.True(t, traverse)

	selected, traverse = policy.selectsPath("unselected", true)
	require.False(t, selected)
	require.False(t, traverse)

	selected, traverse = policy.selectsPath("selected", true)
	require.True(t, selected)
	require.True(t, traverse)

	selected, traverse = policy.selectsPath("selected/config/app.yml", false)
	require.True(t, selected)
	require.True(t, traverse)
}

func TestComposeFilesCannotBeExcludedByPolicyOrDockmanIgnore(t *testing.T) {
	stackRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "nested"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, ".dockmanignore"), []byte("*.yaml\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "nested", "compose.yaml"), []byte("services: {}\n"), 0644))
	policy := syncPolicy{profile: syncProfileComposeConfig, excludes: mustRules([]string{"**"}), compose: map[string]struct{}{"nested/compose.yaml": {}}}

	files, err := collectStackFiles(filesystem.NewLocal(stackRoot), ".", false, policy)
	require.NoError(t, err)
	require.NotNil(t, files["nested/compose.yaml"].open)
	require.NoError(t, validateComposeFiles(files))
	require.Equal(t, "excluded", files[".dockmanignore"].skipReason)
}

func TestBindingPolicyIsValidatedAndPersisted(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte("services: {}\n"), 0644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)

	updated, err := service.UpdateBindingPolicy(binding.ID, BindingPolicyInput{Profile: syncProfileAllFiles, IncludePatterns: []string{"scripts/**"}, ExcludePatterns: []string{"data/**", "*.log"}})
	require.NoError(t, err)
	require.Equal(t, syncProfileAllFiles, updated.SyncProfile)
	require.Equal(t, []string{"scripts/**"}, updated.IncludePatterns)
	require.Equal(t, []string{"data/**", "*.log"}, updated.ExcludePatterns)
	updated, err = service.UpdateBindingPolicy(binding.ID, BindingPolicyInput{Profile: syncProfileComposeOnly, IncludePatterns: []string{"application.conf"}})
	require.NoError(t, err)
	require.Equal(t, syncProfileComposeOnly, updated.SyncProfile)
	require.Equal(t, []string{"application.conf"}, updated.IncludePatterns)
	_, err = service.UpdateBindingPolicy(binding.ID, BindingPolicyInput{Profile: syncProfileComposeConfig, ExcludePatterns: []string{"../outside"}})
	require.ErrorContains(t, err, "path traversal")
}

func TestBindingPolicyChangeInvalidatesStaleRedProjection(t *testing.T) {
	service, _, binding := prepareMultiStackBinding(t)
	row, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	row.AutoSyncEnabled = true
	row.AutoSyncState = "error"
	row.AutoSyncError = "old configuration file error"
	row.LastAutoSyncCommit = strings.Repeat("a", 40)
	require.NoError(t, service.store.SaveBinding(&row))
	require.NoError(t, service.store.UpdateGitStackStatuses(binding.ID, []string{"alpha/compose.yml"}, map[string]any{
		"state": stackSyncConflict, "error_message": "old conflict", "conflict_count": 1,
	}))

	updated, err := service.UpdateBindingPolicy(binding.ID, BindingPolicyInput{Profile: syncProfileComposeOnly})
	require.NoError(t, err)
	require.Equal(t, syncProfileComposeOnly, updated.SyncProfile)
	persisted, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Empty(t, persisted.LastAutoSyncCommit, "the next cycle must inspect the new inventory even when Git did not move")
	require.Equal(t, "watching", persisted.AutoSyncState)
	require.Empty(t, persisted.AutoSyncError)
	status, err := service.store.GitStackStatus(binding.ID, "alpha/compose.yml")
	require.NoError(t, err)
	require.Equal(t, stackSyncPending, status.State)
	require.Empty(t, status.ErrorMessage)
	require.Zero(t, status.ConflictCount)

	// Saving the exact same policy is a no-op and must not hide a subsequently
	// established real conflict.
	require.NoError(t, service.store.UpdateGitStackStatuses(binding.ID, []string{"alpha/compose.yml"}, map[string]any{
		"state": stackSyncConflict, "error_message": "real Compose conflict", "conflict_count": 1,
	}))
	_, err = service.UpdateBindingPolicy(binding.ID, BindingPolicyInput{Profile: syncProfileComposeOnly})
	require.NoError(t, err)
	status, err = service.store.GitStackStatus(binding.ID, "alpha/compose.yml")
	require.NoError(t, err)
	require.Equal(t, stackSyncConflict, status.State)
	require.Equal(t, "real Compose conflict", status.ErrorMessage)
}

func TestBindingExclusionsCanBeAddedFromPreviewPaths(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app", "cache[1]"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte("services: {}\n"), 0644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)

	updated, err := service.AddBindingExclusion(binding.ID, BindingExclusionInput{Path: "cache[1]", Directory: true})
	require.NoError(t, err)
	require.Equal(t, []string{`/cache\[1\]/`}, updated.ExcludePatterns)
	policy, err := policyFromBinding(StackBinding{SyncProfile: updated.SyncProfile, IncludePatterns: strings.Join(updated.IncludePatterns, "\n"), ExcludePatterns: strings.Join(updated.ExcludePatterns, "\n"), ComposePaths: strings.Join(updated.ComposePaths, "\n")})
	require.NoError(t, err)
	require.True(t, matchesIgnoreRule(policy.excludes, "cache[1]", true))
	require.False(t, matchesIgnoreRule(policy.excludes, "cache1", true))

	_, err = service.AddBindingExclusion(binding.ID, BindingExclusionInput{Path: "compose.yaml"})
	require.ErrorContains(t, err, "Compose files cannot be excluded")
	_, err = service.AddBindingExclusion(binding.ID, BindingExclusionInput{Path: "../outside"})
	require.ErrorContains(t, err, "path traversal")
}

func TestBindingExclusionsCanBeAddedInOneBatch(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app", "cache"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte("services: {}\n"), 0644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)

	updated, err := service.AddBindingExclusions(binding.ID, []BindingExclusionInput{
		{Path: "cache", Directory: true},
		{Path: "settings.json"},
		{Path: "settings.json"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"/cache/", "/settings.json"}, updated.ExcludePatterns)

	_, err = service.AddBindingExclusions(binding.ID, []BindingExclusionInput{{Path: "compose.yaml"}, {Path: "other.json"}})
	require.ErrorContains(t, err, "cannot be excluded")
	unchanged, err := service.ListBindings()
	require.NoError(t, err)
	require.Equal(t, []string{"/cache/", "/settings.json"}, unchanged[0].ExcludePatterns)

	_, err = service.AddBindingExclusions(binding.ID, nil)
	require.ErrorContains(t, err, "at least one")
}

func TestBindingInclusionsCanBeAddedInOneBatch(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte("services: {}\n"), 0644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)

	updated, err := service.AddBindingInclusions(binding.ID, []string{"artifacts/run.bin", "settings[1].bin", "artifacts/run.bin"})
	require.NoError(t, err)
	require.Equal(t, []string{"artifacts/run.bin", `settings\[1\].bin`}, updated.IncludePatterns)

	policy, err := policyFromBinding(StackBinding{SyncProfile: updated.SyncProfile, IncludePatterns: strings.Join(updated.IncludePatterns, "\n")})
	require.NoError(t, err)
	require.True(t, policy.includesFile("artifacts/run.bin"))
	require.True(t, policy.includesFile("settings[1].bin"))
	require.False(t, policy.includesFile("settings1.bin"))

	_, err = service.AddBindingInclusions(binding.ID, nil)
	require.ErrorContains(t, err, "at least one")
	_, err = service.AddBindingInclusions(binding.ID, []string{"../outside"})
	require.ErrorContains(t, err, "path traversal")
}

func TestManualExportAndImportCreateRecoverableState(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0755))
	stackCompose := filepath.Join(stackRoot, "app", "compose.yaml")
	require.NoError(t, os.WriteFile(stackCompose, []byte("services:\n  app:\n    image: alpine:3.22\n"), 0644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)

	exportPreview, err := service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	exported, err := service.ExportBinding(context.Background(), binding.ID, TransferInput{PreviewToken: exportPreview.PreviewToken})
	require.NoError(t, err)
	require.NotEmpty(t, exported.CommitSHA)
	temporaryCheckouts, err := filepath.Glob(filepath.Join(service.workspaceRoot, ".dockman-export-*"))
	require.NoError(t, err)
	require.Empty(t, temporaryCheckouts)
	workspace, err := service.repositoryPath(repository.UUID)
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(workspace, "stacks", "app", "compose.yaml"))
	status, err := service.RepositoryStatus(repository.UUID)
	require.NoError(t, err)
	require.Equal(t, "up-to-date", status.State)

	repo, err := gitclient.PlainOpen(workspace)
	require.NoError(t, err)
	temporary, temporaryPath, cleanup := compactTestCheckout(t, repo, repository.DefaultBranch)
	commitTestFile(t, temporary, temporaryPath, "stacks/app/compose.yaml", "services:\n  app:\n    image: alpine:3.23\n")
	commitTestFile(t, temporary, temporaryPath, "stacks/app/extra.yaml", "name: imported\n")
	cleanup()
	require.NoError(t, repo.Push(&gitclient.PushOptions{}))

	importPreview, err := service.PreviewBinding(binding.ID, "repository_to_stack", TransferInput{})
	require.NoError(t, err)
	imported, err := service.ImportBinding(context.Background(), binding.ID, TransferInput{PreviewToken: importPreview.PreviewToken})
	require.NoError(t, err)
	require.NotEmpty(t, imported.Backup)
	contents, err := os.ReadFile(stackCompose)
	require.NoError(t, err)
	require.Contains(t, string(contents), "alpine:3.23")
	require.FileExists(t, filepath.Join(stackRoot, "app", "extra.yaml"))
	backup, err := service.store.GetBackup(imported.Backup)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(service.backupRoot, filepath.FromSlash(backup.ArchivePath)))
	restorePreview, err := service.PreviewBackupRestore(binding.ID, imported.Backup)
	require.NoError(t, err)
	require.Equal(t, 2, restorePreview.Restorable)
	require.Zero(t, restorePreview.Conflicts)
	restored, err := service.RestoreBackup(context.Background(), binding.ID, imported.Backup, BackupRestoreInput{PreviewToken: restorePreview.Token})
	require.NoError(t, err)
	require.NotEmpty(t, restored.SafetyBackupID)
	require.ElementsMatch(t, []string{"compose.yaml", "extra.yaml"}, restored.RestoredPaths)
	contents, err = os.ReadFile(stackCompose)
	require.NoError(t, err)
	require.Contains(t, string(contents), "alpine:3.22")
	require.NoFileExists(t, filepath.Join(stackRoot, "app", "extra.yaml"))
	restoredStatus, err := service.store.GitStackStatus(binding.ID, "compose.yaml")
	require.NoError(t, err)
	require.True(t, restoredStatus.AutomationPaused, "a backup restore must remain protected until it is pushed or explicitly resumed")
	require.Equal(t, stackPauseRecovery, restoredStatus.PauseReason)
	operations, err := service.ListBindingOperations(binding.ID, 100)
	require.NoError(t, err)
	require.Condition(t, func() bool {
		for _, operation := range operations {
			if operation.Type == "backup_restore" && operation.BackupID == imported.Backup {
				return true
			}
		}
		return false
	})
}
