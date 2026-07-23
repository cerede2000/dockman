package gitsync

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	gitclient "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

func prepareOrphanedStack(t *testing.T) (*Service, string, Repository, BindingView) {
	t.Helper()
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	stackDir := filepath.Join(stackRoot, "app")
	require.NoError(t, os.MkdirAll(stackDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackDir, "compose.yaml"), []byte("services:\n  app:\n    image: alpine:3.23\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stackDir, "config.yml"), []byte("enabled: true\n"), 0o640))
	require.NoError(t, os.WriteFile(filepath.Join(stackDir, ".env"), []byte("TOKEN=test-secret\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(stackDir, "runtime.bin"), []byte{0, 1, 2, 3}, 0o600))
	binding, err := service.CreateBindingContext(context.Background(), BindingInput{
		RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks", InitialSync: "stack_to_repository",
	})
	require.NoError(t, err)

	checkoutPath := t.TempDir()
	checkout, err := gitclient.PlainClone(checkoutPath, false, &gitclient.CloneOptions{URL: repository.RemoteURL, ReferenceName: plumbing.NewBranchReferenceName("main"), SingleBranch: true})
	require.NoError(t, err)
	worktree, err := checkout.Worktree()
	require.NoError(t, err)
	_, err = worktree.Remove("stacks/app/compose.yaml")
	require.NoError(t, err)
	_, err = worktree.Remove("stacks/app/config.yml")
	require.NoError(t, err)
	_, err = worktree.Commit("test: remove stack", &gitclient.CommitOptions{Author: &object.Signature{Name: "Test", Email: "test@example.invalid", When: time.Now()}})
	require.NoError(t, err)
	require.NoError(t, checkout.Push(&gitclient.PushOptions{}))
	return service, stackRoot, repository, binding
}

func TestOrphanArchiveRequiresConfirmationAndNeverRemovesBeforeBackup(t *testing.T) {
	service, stackRoot, repository, binding := prepareOrphanedStack(t)
	composePath := "app/compose.yaml"
	composeFile := filepath.Join(stackRoot, "app", "compose.yaml")

	_, err := service.PullRepository(context.Background(), repository.UUID)
	require.NoError(t, err)
	preview, err := service.PreviewBinding(binding.ID, "repository_to_stack", TransferInput{})
	require.NoError(t, err)
	require.Contains(t, preview.OrphanedComposePaths, composePath)
	require.Equal(t, 2, preview.Preserved)

	_, err = service.ResolveGitOrphan(context.Background(), binding.ID, composePath, OrphanActionInput{Action: "archive"})
	require.ErrorContains(t, err, orphanConfirmText)
	require.FileExists(t, composeFile)

	result, err := service.ResolveGitOrphan(context.Background(), binding.ID, composePath, OrphanActionInput{Action: "archive", Confirmation: orphanConfirmText})
	require.NoError(t, err)
	require.Contains(t, result.Backup, "archives/")
	archivePath := filepath.Join(service.backupRoot, filepath.FromSlash(result.Backup))
	require.FileExists(t, archivePath)
	handle, err := os.Open(archivePath)
	require.NoError(t, err)
	gzipReader, err := gzip.NewReader(handle)
	require.NoError(t, err)
	tarReader := tar.NewReader(gzipReader)
	archivedFiles := map[string]bool{}
	for {
		header, nextErr := tarReader.Next()
		if nextErr == io.EOF {
			break
		}
		require.NoError(t, nextErr)
		archivedFiles[header.Name] = true
	}
	require.NoError(t, gzipReader.Close())
	require.NoError(t, handle.Close())
	require.True(t, archivedFiles["app/.env"], "sensitive files must be archived before removal")
	require.True(t, archivedFiles["app/runtime.bin"], "files excluded from Git sync must still be archived")
	require.NoDirExists(t, filepath.Join(stackRoot, "app"))
	updated, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.NotContains(t, splitPatternLines(updated.ComposePaths), composePath)
}

func TestOrphanRestorePushesOnlyTheDeletedStackBackToGit(t *testing.T) {
	service, _, repository, binding := prepareOrphanedStack(t)
	result, err := service.ResolveGitOrphan(context.Background(), binding.ID, "app/compose.yaml", OrphanActionInput{Action: "restore"})
	require.NoError(t, err)
	require.Contains(t, result.Message, "pushed to Git")

	check, err := gitclient.PlainClone(t.TempDir(), false, &gitclient.CloneOptions{URL: repository.RemoteURL, ReferenceName: plumbing.NewBranchReferenceName("main"), SingleBranch: true})
	require.NoError(t, err)
	requireGitFileContent(t, check, "main", "stacks/app/compose.yaml", "services:\n  app:\n    image: alpine:3.23\n")
	requireGitFileContent(t, check, "main", "stacks/app/config.yml", "enabled: true\n")
}

func TestOrphanDeleteCreatesBackupAndRefusesDirtyEditor(t *testing.T) {
	service, stackRoot, _, binding := prepareOrphanedStack(t)
	service.dirtyEditorPaths = func(string) []string { return []string{"compose/app/config.yml"} }
	_, err := service.ResolveGitOrphan(context.Background(), binding.ID, "app/compose.yaml", OrphanActionInput{Action: "delete", Confirmation: orphanConfirmText})
	require.ErrorContains(t, err, "unsaved editor")
	require.DirExists(t, filepath.Join(stackRoot, "app"))

	service.dirtyEditorPaths = func(string) []string { return nil }
	result, err := service.ResolveGitOrphan(context.Background(), binding.ID, "app/compose.yaml", OrphanActionInput{Action: "delete", Confirmation: orphanConfirmText})
	require.NoError(t, err)
	require.NotContains(t, result.Backup, "archives/")
	require.FileExists(t, filepath.Join(service.backupRoot, filepath.FromSlash(result.Backup)))
	require.NoDirExists(t, filepath.Join(stackRoot, "app"))
}

func TestAutomaticSyncPreservesGitDeletedStackWithoutRepeatedInventory(t *testing.T) {
	service, stackRoot, _, binding := prepareOrphanedStack(t)
	row, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	row.AutoSyncEnabled = true
	row.AutoSyncState = "watching"
	require.NoError(t, service.store.SaveBinding(&row))

	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "blocked", result.State)
	require.Equal(t, 2, result.Preserved)
	require.FileExists(t, filepath.Join(stackRoot, "app", "compose.yaml"))
	updated, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.NotEmpty(t, updated.LastAutoSyncCommit)
	statuses, err := service.ListGitStackStatusViews("local")
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	require.Equal(t, stackSyncOrphaned, statuses[0].State)

	second, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "blocked", second.State)
	require.Contains(t, second.Message, "scan skipped")
	require.FileExists(t, filepath.Join(stackRoot, "app", "compose.yaml"))
	statuses, err = service.ListGitStackStatusViews("local")
	require.NoError(t, err)
	require.Equal(t, stackSyncOrphaned, statuses[0].State)
}

func TestOrphanLocalRemovalRefusesFolderLinkRoot(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte("services: {}\n"), 0o644))
	binding, err := service.CreateBindingContext(context.Background(), BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app", InitialSync: "stack_to_repository"})
	require.NoError(t, err)

	checkout, err := gitclient.PlainClone(t.TempDir(), false, &gitclient.CloneOptions{URL: repository.RemoteURL, ReferenceName: plumbing.NewBranchReferenceName("main"), SingleBranch: true})
	require.NoError(t, err)
	worktree, err := checkout.Worktree()
	require.NoError(t, err)
	_, err = worktree.Remove("stacks/app/compose.yaml")
	require.NoError(t, err)
	_, err = worktree.Commit("test: remove root stack", &gitclient.CommitOptions{Author: &object.Signature{Name: "Test", Email: "test@example.invalid", When: time.Now()}})
	require.NoError(t, err)
	require.NoError(t, checkout.Push(&gitclient.PushOptions{}))

	_, err = service.ResolveGitOrphan(context.Background(), binding.ID, "compose.yaml", OrphanActionInput{Action: "delete", Confirmation: orphanConfirmText})
	require.ErrorContains(t, err, "folder-link root")
	require.FileExists(t, filepath.Join(stackRoot, "app", "compose.yaml"))
}
