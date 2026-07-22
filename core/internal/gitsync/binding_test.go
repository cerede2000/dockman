package gitsync

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	gitclient "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

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
	require.FileExists(t, filepath.Join(workspace, "stacks", "app", "compose.yaml"))
	require.NoFileExists(t, filepath.Join(workspace, "stacks", "app", ".env"))
}

func TestRepositoryBindingPathsCannotOverlap(t *testing.T) {
	require.True(t, pathsOverlap(".", "stacks/app"))
	require.True(t, pathsOverlap("stacks", "stacks/app"))
	require.True(t, pathsOverlap("stacks/app", "stacks/app"))
	require.False(t, pathsOverlap("stacks/app", "stacks/database"))
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

	selected, pending, err := selectedTransferFiles(preview, source, []string{"two.yaml"})
	require.NoError(t, err)
	require.Equal(t, 2, pending)
	require.ElementsMatch(t, []string{"safe.yaml", "two.yaml"}, mapKeys(selected))

	next := baselineAfterTransfer(baseline, source, target, selected)
	require.Equal(t, "safe-new", next["safe.yaml"])
	require.Equal(t, "two-new", next["two.yaml"])
	require.Equal(t, "one-old", next["one.yaml"])
	require.Equal(t, "three-old", next["three.yaml"])
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
	two, err := os.ReadFile(filepath.Join(workspace, "stacks", "app", "two.yaml"))
	require.NoError(t, err)
	require.Equal(t, "value: dockman\n", string(two))
	one, err := os.ReadFile(filepath.Join(workspace, "stacks", "app", "one.yaml"))
	require.NoError(t, err)
	require.Equal(t, "value: git\n", string(one))

	remaining, err := service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	require.Equal(t, 2, remaining.Conflicts)
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
	_, err = service.UpdateBindingPolicy(binding.ID, BindingPolicyInput{Profile: syncProfileComposeConfig, ExcludePatterns: []string{"../outside"}})
	require.ErrorContains(t, err, "path traversal")
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
	require.Equal(t, []string{`cache\[1\]/`}, updated.ExcludePatterns)
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
	require.Equal(t, []string{"cache/", "settings.json"}, updated.ExcludePatterns)

	_, err = service.AddBindingExclusions(binding.ID, []BindingExclusionInput{{Path: "compose.yaml"}, {Path: "other.json"}})
	require.ErrorContains(t, err, "cannot be excluded")
	unchanged, err := service.ListBindings()
	require.NoError(t, err)
	require.Equal(t, []string{"cache/", "settings.json"}, unchanged[0].ExcludePatterns)

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
	workspace, err := service.repositoryPath(repository.UUID)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(workspace, "stacks", "app", "compose.yaml"))
	status, err := service.RepositoryStatus(repository.UUID)
	require.NoError(t, err)
	require.Equal(t, "up-to-date", status.State)

	repo, err := gitclient.PlainOpen(workspace)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "stacks", "app", "compose.yaml"), []byte("services:\n  app:\n    image: alpine:3.23\n"), 0644))
	worktree, err := repo.Worktree()
	require.NoError(t, err)
	_, err = worktree.Add("stacks/app/compose.yaml")
	require.NoError(t, err)
	commitTestFile(t, repo, workspace, "stacks/app/extra.yaml", "name: imported\n")
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
	require.FileExists(t, filepath.Join(service.backupRoot, imported.Backup))
}
