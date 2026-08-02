package gitsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	gitclient "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/require"
)

func prepareTrackedLocalDeletion(t *testing.T) (*Service, string, Repository, BindingView) {
	t.Helper()
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	for _, folder := range []string{"alpha", "beta"} {
		require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, folder), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(stackRoot, folder, "compose.yml"), []byte("services: {}\n"), 0o644))
	}
	binding, err := service.CreateBindingContext(context.Background(), BindingInput{
		RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks", InitialSync: "stack_to_repository",
	})
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(filepath.Join(stackRoot, "beta")))
	service.MarkLocalChange("local", "compose/beta")
	return service, stackRoot, repository, binding
}

func TestLocalStackDeletionIsExplicitAndCanBeRestored(t *testing.T) {
	service, stackRoot, _, binding := prepareTrackedLocalDeletion(t)
	views, err := service.ListGitStackStatusViews("local")
	require.NoError(t, err)
	require.Equal(t, stackSyncLocalDeleted, findStackStatus(t, views, "beta/compose.yml").State)

	stackPreview, err := service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	require.Equal(t, "deleted_locally", findPreviewEntry(t, stackPreview, "beta/compose.yml").Status)
	require.Equal(t, 1, stackPreview.LocalDeletions)
	gitPreview, err := service.PreviewBinding(binding.ID, "repository_to_stack", TransferInput{})
	require.NoError(t, err)
	require.Equal(t, "destination_deleted", findPreviewEntry(t, gitPreview, "beta/compose.yml").ConflictKind)

	result, err := service.ResolveLocalStackDeletion(context.Background(), binding.ID, "beta/compose.yml", LocalDeletionActionInput{Action: "restore"})
	require.NoError(t, err)
	require.Contains(t, result.Message, "imported")
	require.FileExists(t, filepath.Join(stackRoot, "beta", "compose.yml"))
}

func TestLocalStackDeletionCanBeDeselectedWithoutChangingGit(t *testing.T) {
	service, _, _, binding := prepareTrackedLocalDeletion(t)
	result, err := service.ResolveLocalStackDeletion(context.Background(), binding.ID, "beta/compose.yml", LocalDeletionActionInput{Action: "deselect"})
	require.NoError(t, err)
	require.Contains(t, result.Message, "preserved")
	updated, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.NotContains(t, selectedComposePaths(updated), "beta/compose.yml")
	require.Contains(t, splitPatternLines(updated.ComposePaths), "beta/compose.yml")
}

func TestAutomaticSyncBlocksInsteadOfRestoringLocalStackDeletion(t *testing.T) {
	service, stackRoot, _, binding := prepareTrackedLocalDeletion(t)
	_, err := service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)

	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "blocked", result.State)
	require.Contains(t, result.Message, "locally deleted")
	require.NoFileExists(t, filepath.Join(stackRoot, "beta", "compose.yml"))
	views, err := service.ListGitStackStatusViews("local")
	require.NoError(t, err)
	require.Equal(t, stackSyncLocalDeleted, findStackStatus(t, views, "beta/compose.yml").State)
}

func TestCatalogRefreshRecoversMissedLocalDeletionEvent(t *testing.T) {
	service, _, _, binding := prepareTrackedLocalDeletion(t)
	require.NoError(t, service.store.UpdateGitStackStatuses(binding.ID, []string{"beta/compose.yml"}, map[string]any{"state": stackSyncUpToDate}))

	_, err := service.RefreshBindingComposeCatalog(binding.ID)
	require.NoError(t, err)
	views, err := service.ListGitStackStatusViews("local")
	require.NoError(t, err)
	require.Equal(t, stackSyncLocalDeleted, findStackStatus(t, views, "beta/compose.yml").State)
}

func TestLocalStackDeletionRequiresConfirmationBeforeDeletingGit(t *testing.T) {
	service, _, repository, binding := prepareTrackedLocalDeletion(t)
	remoteChange(t, repository.RemoteURL, "stacks/beta/remote-only.bin", "not selected by the synchronization policy\n")
	_, err := service.ResolveLocalStackDeletion(context.Background(), binding.ID, "beta/compose.yml", LocalDeletionActionInput{Action: "delete_git"})
	require.ErrorContains(t, err, deleteGitStackConfirmText)

	result, err := service.ResolveLocalStackDeletion(context.Background(), binding.ID, "beta/compose.yml", LocalDeletionActionInput{Action: "delete_git", Confirmation: deleteGitStackConfirmText})
	require.NoError(t, err)
	require.NotEmpty(t, result.CommitSHA)
	check, err := gitclient.PlainClone(t.TempDir(), false, &gitclient.CloneOptions{URL: repository.RemoteURL, ReferenceName: plumbing.NewBranchReferenceName("main"), SingleBranch: true})
	require.NoError(t, err)
	head, err := check.Head()
	require.NoError(t, err)
	commit, err := check.CommitObject(head.Hash())
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)
	_, err = tree.File("stacks/beta/compose.yml")
	require.Error(t, err)
	_, err = tree.File("stacks/beta/remote-only.bin")
	require.Error(t, err, "an explicit whole-stack Git deletion must not leave policy-skipped files behind")
	updated, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.NotContains(t, splitPatternLines(updated.ComposePaths), "beta/compose.yml")
}

func prepareTrackedFileDeletion(t *testing.T) (*Service, string, Repository, BindingView) {
	t.Helper()
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "alpha"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", "compose.yml"), []byte("services: {}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", "test.conf"), []byte("enabled=true\n"), 0o644))
	binding, err := service.CreateBindingContext(context.Background(), BindingInput{
		RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks", InitialSync: "stack_to_repository",
	})
	require.NoError(t, err)
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(stackRoot, "alpha", "test.conf")))
	service.MarkLocalChange("local", "compose/alpha/test.conf")
	return service, stackRoot, repository, binding
}

func TestLocalFileDeletionIsReportedOnItsOwningStack(t *testing.T) {
	service, _, _, binding := prepareTrackedFileDeletion(t)
	view, err := service.ListLocalStackDeletions(binding.ID, "alpha/compose.yml")
	require.NoError(t, err)
	require.False(t, view.WholeStack)
	require.Equal(t, []LocalDeletedFileView{{Path: "alpha/test.conf"}}, view.Files)
	statuses, err := service.ListGitStackStatusViews("local")
	require.NoError(t, err)
	require.Equal(t, stackSyncLocalDeleted, findStackStatus(t, statuses, "alpha/compose.yml").State)
}

func TestAutomaticSyncExposesLocalFileDeletionAsBlockedOnStack(t *testing.T) {
	service, _, _, binding := prepareTrackedFileDeletion(t)
	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "blocked", result.State)
	require.Contains(t, result.Message, "locally deleted synchronized file")
	statuses, err := service.ListGitStackStatusViews("local")
	require.NoError(t, err)
	status := findStackStatus(t, statuses, "alpha/compose.yml")
	require.Equal(t, stackSyncLocalDeleted, status.State)
	require.Equal(t, "blocked", status.BindingSyncState)
	require.Contains(t, status.BindingSyncError, "locally deleted synchronized file")
}

func TestLocalFileDeletionCanBeRestoredWithoutDockerAction(t *testing.T) {
	service, stackRoot, _, binding := prepareTrackedFileDeletion(t)
	result, err := service.ResolveLocalStackDeletion(context.Background(), binding.ID, "alpha/compose.yml", LocalDeletionActionInput{Action: "restore", Path: "alpha/test.conf"})
	require.NoError(t, err)
	require.Contains(t, result.Message, "restored")
	require.FileExists(t, filepath.Join(stackRoot, "alpha", "test.conf"))
	updated, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", updated.AutoSyncState)
}

func TestLocalFileDeletionCanBeExcludedWhileGitCopyIsPreserved(t *testing.T) {
	service, _, repository, binding := prepareTrackedFileDeletion(t)
	result, err := service.ResolveLocalStackDeletion(context.Background(), binding.ID, "alpha/compose.yml", LocalDeletionActionInput{Action: "exclude", Path: "alpha/test.conf"})
	require.NoError(t, err)
	require.Contains(t, result.Message, "preserved")
	updated, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Contains(t, splitPatternLines(updated.ExcludePatterns), "/alpha/test.conf")
	check, err := gitclient.PlainClone(t.TempDir(), false, &gitclient.CloneOptions{URL: repository.RemoteURL, ReferenceName: plumbing.NewBranchReferenceName("main"), SingleBranch: true})
	require.NoError(t, err)
	head, err := check.Head()
	require.NoError(t, err)
	commit, err := check.CommitObject(head.Hash())
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)
	_, err = tree.File("stacks/alpha/test.conf")
	require.NoError(t, err)
}

func TestLocalFileDeletionRequiresConfirmationAndCanBeCommitted(t *testing.T) {
	service, _, repository, binding := prepareTrackedFileDeletion(t)
	_, err := service.ResolveLocalStackDeletion(context.Background(), binding.ID, "alpha/compose.yml", LocalDeletionActionInput{Action: "delete_git", Path: "alpha/test.conf"})
	require.ErrorContains(t, err, deleteGitFileConfirmText)
	result, err := service.ResolveLocalStackDeletion(context.Background(), binding.ID, "alpha/compose.yml", LocalDeletionActionInput{Action: "delete_git", Path: "alpha/test.conf", Confirmation: deleteGitFileConfirmText})
	require.NoError(t, err)
	require.NotEmpty(t, result.CommitSHA)
	check, err := gitclient.PlainClone(t.TempDir(), false, &gitclient.CloneOptions{URL: repository.RemoteURL, ReferenceName: plumbing.NewBranchReferenceName("main"), SingleBranch: true})
	require.NoError(t, err)
	head, err := check.Head()
	require.NoError(t, err)
	commit, err := check.CommitObject(head.Hash())
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)
	_, err = tree.File("stacks/alpha/test.conf")
	require.Error(t, err)
	_, err = tree.File("stacks/alpha/compose.yml")
	require.NoError(t, err)
	status, err := service.store.GitStackStatus(binding.ID, "alpha/compose.yml")
	require.NoError(t, err)
	require.Equal(t, stackSyncUpToDate, status.State, "the one-step Git deletion must immediately clear the stale local-deletion indicator")
	updated, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", updated.AutoSyncState)
}

func TestLocalAndGitDeletionSucceedsWhenFileWasNeverPushed(t *testing.T) {
	service, stackRoot, _, binding := prepareTrackedLocalDeletion(t)
	path := filepath.Join(stackRoot, "alpha", "never-pushed.bin")
	require.NoError(t, os.WriteFile(path, []byte("temporary\n"), 0o644))
	_, err := service.SetGitFileTracking(GitFileTrackingInput{Host: "local", BindingID: binding.ID, Path: "compose/alpha/never-pushed.bin", Tracked: true})
	require.NoError(t, err)
	require.NoError(t, os.Remove(path))
	service.MarkLocalChange("local", "compose/alpha/never-pushed.bin")

	result, err := service.ResolveLocalStackDeletion(context.Background(), binding.ID, "alpha/compose.yml", LocalDeletionActionInput{Action: "delete_git", Path: "alpha/never-pushed.bin", Confirmation: deleteGitFileConfirmText})
	require.NoError(t, err)
	require.Contains(t, result.Message, "already absent from Git")
	require.Empty(t, result.CommitSHA)
	stored, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.NotContains(t, splitPatternLines(stored.IncludePatterns), "/alpha/never-pushed.bin")
	status, err := service.store.GitStackStatus(binding.ID, "alpha/compose.yml")
	require.NoError(t, err)
	require.Equal(t, stackSyncUpToDate, status.State)
}

func TestLocalFileDeletionRefusesToDeleteGitWhenRemoteChanged(t *testing.T) {
	service, _, repository, binding := prepareTrackedFileDeletion(t)
	remoteChange(t, repository.RemoteURL, "stacks/alpha/test.conf", "enabled=false\n")
	_, err := service.ResolveLocalStackDeletion(context.Background(), binding.ID, "alpha/compose.yml", LocalDeletionActionInput{Action: "delete_git", Path: "alpha/test.conf", Confirmation: deleteGitFileConfirmText})
	require.ErrorContains(t, err, "Git changed")
}

func findStackStatus(t *testing.T, views []GitStackStatusView, composePath string) GitStackStatusView {
	t.Helper()
	for _, view := range views {
		if view.ComposePath == composePath {
			return view
		}
	}
	t.Fatalf("stack status %s not found", composePath)
	return GitStackStatusView{}
}

func findPreviewEntry(t *testing.T, preview TransferPreview, path string) PreviewEntry {
	t.Helper()
	for _, entry := range preview.Entries {
		if entry.Path == path {
			return entry
		}
	}
	t.Fatalf("preview entry %s not found", path)
	return PreviewEntry{}
}
