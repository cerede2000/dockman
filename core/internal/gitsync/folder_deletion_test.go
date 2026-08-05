package gitsync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	gitclient "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/require"
)

func prepareFolderLinkDeletion(t *testing.T) (*Service, string, Repository, BindingView) {
	t.Helper()
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
	binding, err := service.CreateBindingContext(context.Background(), BindingInput{
		RepositoryID: repository.UUID, Host: "local", StackPath: "compose/alpha", SubPath: "alpha",
		InitialSync: "repository_to_stack", SyncProfile: syncProfileComposeOnly,
		ComposeSelectionMode: composeSelectionSelected, SelectedComposePaths: []string{"compose.yml"},
	})
	require.NoError(t, err)
	require.Equal(t, "imported", binding.InitialSyncState)
	return service, stackRoot, repository, binding
}

func remoteFolderFile(t *testing.T, repository Repository, path string) (string, bool) {
	t.Helper()
	check, err := gitclient.PlainClone(t.TempDir(), false, &gitclient.CloneOptions{URL: repository.RemoteURL, ReferenceName: plumbing.NewBranchReferenceName("main"), SingleBranch: true})
	require.NoError(t, err)
	head, err := check.Head()
	require.NoError(t, err)
	commit, err := check.CommitObject(head.Hash())
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)
	file, err := tree.File(path)
	if err != nil {
		return "", false
	}
	contents, err := file.Contents()
	require.NoError(t, err)
	return contents, true
}

func TestFolderLinkRootGenericDeletionIsBlocked(t *testing.T) {
	service, _, _, _ := prepareFolderLinkDeletion(t)
	require.ErrorContains(t, service.GuardFileDeletion("local", "compose/alpha"), "protected deletion dialog")
	require.ErrorContains(t, service.GuardFileDeletion("local", "compose"), "contains one or more Git Folder Links")
	require.NoError(t, service.GuardFileDeletion("local", "compose/beta"))
}

func TestFolderLinkDeletionPreservesGitAndForgetsBinding(t *testing.T) {
	service, stackRoot, repository, binding := prepareFolderLinkDeletion(t)
	state, err := service.InspectFolderLinkDeletion(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", state.State)
	result, err := service.DeleteFolderLinkRoot(context.Background(), binding.ID, FolderLinkDeletionInput{Action: "preserve_git", Confirmation: typedConfirmationText})
	require.NoError(t, err)
	require.Contains(t, result.Message, "Git was preserved")
	require.NoDirExists(t, filepath.Join(stackRoot, "alpha"))
	_, err = service.store.GetBinding(binding.ID)
	require.Error(t, err)
	_, exists := remoteFolderFile(t, repository, "alpha/compose.yml")
	require.True(t, exists)
}

func TestFolderLinkDeletionCanSyncGitBeforeUnlink(t *testing.T) {
	service, stackRoot, repository, binding := prepareFolderLinkDeletion(t)
	updated := "services:\n  alpha:\n    image: alpine:3.24\n"
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", "compose.yml"), []byte(updated), 0o644))
	state, err := service.InspectFolderLinkDeletion(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "local_changes", state.State)
	result, err := service.DeleteFolderLinkRoot(context.Background(), binding.ID, FolderLinkDeletionInput{Action: "sync_git", Confirmation: typedConfirmationText})
	require.NoError(t, err)
	require.NotEmpty(t, result.CommitSHA)
	contents, exists := remoteFolderFile(t, repository, "alpha/compose.yml")
	require.True(t, exists)
	require.Equal(t, updated, contents)
	require.NoDirExists(t, filepath.Join(stackRoot, "alpha"))
}

func TestFolderLinkDeletionCanRemoveSynchronizedGitContent(t *testing.T) {
	service, stackRoot, repository, binding := prepareFolderLinkDeletion(t)
	result, err := service.DeleteFolderLinkRoot(context.Background(), binding.ID, FolderLinkDeletionInput{Action: "delete_git", Confirmation: typedConfirmationText})
	require.NoError(t, err)
	require.NotEmpty(t, result.CommitSHA)
	_, exists := remoteFolderFile(t, repository, "alpha/compose.yml")
	require.False(t, exists)
	require.NoDirExists(t, filepath.Join(stackRoot, "alpha"))
	_, err = service.store.GetBinding(binding.ID)
	require.Error(t, err)
}
