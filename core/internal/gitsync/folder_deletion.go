package gitsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	gitclient "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

const (
	deleteLinkedFolderConfirmText = "DELETE LOCAL LINKED FOLDER"
	deleteLinkedFolderGitConfirm  = "DELETE FOLDER FROM GIT"
)

type FolderLinkDeletionInput struct {
	Action       string `json:"action"`
	Confirmation string `json:"confirmation"`
}

type FolderLinkDeletionView struct {
	BindingID        string `json:"bindingId"`
	Host             string `json:"host"`
	StackPath        string `json:"stackPath"`
	RepositoryName   string `json:"repositoryName"`
	RepositoryBranch string `json:"repositoryBranch"`
	StackCount       int    `json:"stackCount"`
	State            string `json:"state"`
	LocalChanges     int    `json:"localChanges"`
	GitChanges       int    `json:"gitChanges"`
	Conflicts        int    `json:"conflicts"`
	UnreadableLocal  int    `json:"unreadableLocal"`
}

type FolderLinkDeletionResult struct {
	Action    string `json:"action"`
	CommitSHA string `json:"commitSha,omitempty"`
	Message   string `json:"message"`
}

// GuardFileDeletion is the server-side fail-closed protection for callers
// using the generic Files RPC directly. A Folder Link root must go through the
// consistency-aware endpoint which also removes the database link.
func (s *Service) GuardFileDeletion(host, path string) error {
	if !s.enabled {
		return nil
	}
	host = strings.TrimSpace(host)
	path = strings.Trim(filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(path)))), "/")
	bindings, err := s.store.ListBindingsForHost(host)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		root := strings.Trim(filepath.ToSlash(filepath.Clean(filepath.FromSlash(binding.StackPath))), "/")
		if path == root {
			return errors.New("this directory is a Git Folder Link root; use its protected deletion dialog to verify Git and remove the link safely")
		}
		if path != "." && strings.HasPrefix(root, path+"/") {
			return errors.New("this directory contains one or more Git Folder Links; remove each link through its protected deletion dialog before deleting the parent folder")
		}
	}
	return nil
}

func (s *Service) InspectFolderLinkDeletion(ctx context.Context, bindingID string) (FolderLinkDeletionView, error) {
	lock := s.repositoryLock("automation:" + bindingID)
	lock.Lock()
	defer lock.Unlock()
	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return FolderLinkDeletionView{}, err
	}
	if _, err := s.PullRepository(ctx, binding.RepositoryUUID); err != nil {
		return FolderLinkDeletionView{}, fmt.Errorf("refresh Git before folder deletion: %w", err)
	}
	return s.inspectFolderLinkDeletion(binding)
}

func (s *Service) inspectFolderLinkDeletion(binding StackBinding) (FolderLinkDeletionView, error) {
	repository, err := s.store.GetRepository(binding.RepositoryUUID)
	if err != nil {
		return FolderLinkDeletionView{}, err
	}
	_, localFiles, gitFiles, err := s.loadTransferTrees(binding.UUID, "stack_to_repository", TransferInput{})
	if err != nil {
		return FolderLinkDeletionView{}, err
	}
	baseline, err := s.store.BindingBaseline(binding.UUID)
	if err != nil {
		return FolderLinkDeletionView{}, err
	}
	paths := make(map[string]struct{}, len(localFiles)+len(gitFiles)+len(baseline))
	for path := range localFiles {
		paths[path] = struct{}{}
	}
	for path := range gitFiles {
		paths[path] = struct{}{}
	}
	for path := range baseline {
		paths[path] = struct{}{}
	}
	view := FolderLinkDeletionView{
		BindingID: binding.UUID, Host: binding.Host, StackPath: binding.StackPath,
		RepositoryName: repository.Name, RepositoryBranch: repository.DefaultBranch,
		StackCount: len(selectedComposePaths(binding)), State: "up_to_date",
	}
	for path := range paths {
		local, localExists := localFiles[path]
		remote, remoteExists := gitFiles[path]
		if localExists && local.open == nil {
			if local.skipReason == "permission" || local.skipReason == "unavailable" || local.skipReason == "large_directory" {
				view.UnreadableLocal++
			}
			continue
		}
		if remoteExists && remote.open == nil {
			continue
		}
		if localExists && remoteExists && local.sha == remote.sha {
			continue
		}
		base, tracked := baseline[path]
		localChanged := localExists != tracked || localExists && local.sha != base
		gitChanged := remoteExists != tracked || remoteExists && remote.sha != base
		if localChanged {
			view.LocalChanges++
		}
		if gitChanged {
			view.GitChanges++
		}
		if localChanged && gitChanged {
			view.Conflicts++
		}
	}
	switch {
	case view.Conflicts > 0:
		view.State = "conflict"
	case view.UnreadableLocal > 0:
		view.State = "blocked"
	case view.LocalChanges > 0 && view.GitChanges > 0:
		view.State = "diverged"
	case view.LocalChanges > 0:
		view.State = "local_changes"
	case view.GitChanges > 0:
		view.State = "git_changes"
	}
	return view, nil
}

func (s *Service) DeleteFolderLinkRoot(ctx context.Context, bindingID string, input FolderLinkDeletionInput) (FolderLinkDeletionResult, error) {
	action := strings.ToLower(strings.TrimSpace(input.Action))
	if action != "preserve_git" && action != "sync_git" && action != "delete_git" {
		return FolderLinkDeletionResult{}, errors.New("folder deletion action must be preserve_git, sync_git, or delete_git")
	}
	confirmation := deleteLinkedFolderConfirmText
	if action == "delete_git" {
		confirmation = deleteLinkedFolderGitConfirm
	}
	if input.Confirmation != confirmation {
		return FolderLinkDeletionResult{}, fmt.Errorf("type %q to confirm this folder deletion", confirmation)
	}
	automationLock := s.repositoryLock("automation:" + bindingID)
	automationLock.Lock()
	defer automationLock.Unlock()
	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return FolderLinkDeletionResult{}, err
	}
	if _, err := s.PullRepository(ctx, binding.RepositoryUUID); err != nil {
		return FolderLinkDeletionResult{}, fmt.Errorf("refresh Git before folder deletion: %w", err)
	}
	state, err := s.inspectFolderLinkDeletion(binding)
	if err != nil {
		return FolderLinkDeletionResult{}, fmt.Errorf("verify Folder Link consistency: %w", err)
	}
	result := FolderLinkDeletionResult{Action: action}
	if action == "sync_git" {
		if state.Conflicts > 0 {
			return result, errors.New("Git update refused: resolve Folder Link conflicts before deleting the local folder")
		}
		if state.UnreadableLocal > 0 {
			return result, fmt.Errorf("Git update refused: %d synchronized local item(s) cannot be read", state.UnreadableLocal)
		}
		preview, err := s.PreviewBinding(binding.UUID, "stack_to_repository", TransferInput{})
		if err != nil {
			return result, err
		}
		selected := make([]string, 0)
		for _, entry := range preview.Entries {
			if entry.Status == "add" || entry.Status == "modify" {
				selected = append(selected, entry.Path)
			}
		}
		if len(selected) > 0 {
			exported, err := s.ExportBinding(ctx, binding.UUID, TransferInput{PreviewToken: preview.PreviewToken, SelectedPaths: selected, CommitMessage: "chore(stack): sync before removing local Folder Link"})
			if err != nil {
				return result, fmt.Errorf("update Git before folder deletion: %w", err)
			}
			result.CommitSHA = exported.CommitSHA
		}
	}
	if action == "delete_git" {
		result.CommitSHA, err = s.deleteFolderLinkGitContent(ctx, binding)
		if err != nil {
			return result, err
		}
	}
	targetFS, targetRoot, err := s.resolveBindingStack(binding)
	if err != nil {
		return result, err
	}
	cleanRoot := filepath.Clean(targetRoot)
	if cleanRoot == "." || cleanRoot == string(filepath.Separator) || cleanRoot == "" {
		return result, errors.New("refusing to delete Dockman's complete stacks root; unlink individual stack folders instead")
	}
	if err := targetFS.RemoveAll(targetRoot); err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, fmt.Errorf("delete local linked folder: %w", err)
	}
	repositoryLock := s.repositoryLock(binding.RepositoryUUID)
	repositoryLock.Lock()
	err = s.deleteBindingLocked(binding, true)
	repositoryLock.Unlock()
	if err != nil {
		return result, fmt.Errorf("local folder deleted but Folder Link cleanup failed: %w", err)
	}
	if s.fileChangeNotify != nil {
		s.fileChangeNotify(binding.Host, binding.StackPath)
	}
	result.Message = "Local folder deleted and Folder Link removed; Git was preserved"
	if action == "sync_git" {
		result.Message = "Git updated, local folder deleted, and Folder Link removed"
	}
	if action == "delete_git" {
		result.Message = "Synchronized Git content and local folder deleted; Folder Link removed"
	}
	return result, nil
}

func (s *Service) deleteFolderLinkGitContent(ctx context.Context, binding StackBinding) (string, error) {
	lock := s.repositoryLock(binding.RepositoryUUID)
	lock.Lock()
	defer lock.Unlock()
	if _, err := s.fetchRepositoryLocked(ctx, binding.RepositoryUUID); err != nil {
		return "", err
	}
	status, err := s.RepositoryStatus(binding.RepositoryUUID)
	if err != nil {
		return "", err
	}
	if !status.Clean || status.Behind > 0 || status.Diverged {
		return "", errors.New("Git deletion refused: repository state changed; refresh and resolve it first")
	}
	_, _, files, err := s.loadTransferTrees(binding.UUID, "stack_to_repository", TransferInput{})
	if err != nil {
		return "", err
	}
	paths := make([]string, 0, len(files))
	for path, file := range files {
		if file.open != nil || isProvisionControlPath(path) {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return "", nil
	}
	repository, err := s.store.GetRepository(binding.RepositoryUUID)
	if err != nil {
		return "", err
	}
	repo, err := s.openRepository(repository)
	if err != nil {
		return "", err
	}
	temporaryRepo, checkoutPath, cleanup, err := temporaryRepositoryWorktree(repo, s.workspaceRoot)
	if err != nil {
		return "", err
	}
	defer cleanup()
	worktree, err := temporaryRepo.Worktree()
	if err != nil {
		return "", err
	}
	if err := worktree.Checkout(&gitclient.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(repository.DefaultBranch), Force: true}); err != nil {
		return "", fmt.Errorf("prepare temporary Git checkout: %w", err)
	}
	root, err := os.OpenRoot(checkoutPath)
	if err != nil {
		return "", err
	}
	for _, path := range paths {
		relative := filepath.FromSlash(path)
		if binding.SubPath != "." {
			relative = filepath.Join(filepath.FromSlash(binding.SubPath), relative)
		}
		if err := root.Remove(relative); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = root.Close()
			return "", fmt.Errorf("delete Git file %s: %w", path, err)
		}
	}
	if err := root.Close(); err != nil {
		return "", err
	}
	stagePath := binding.SubPath
	if stagePath == "." {
		stagePath = ""
	}
	if err := worktree.AddWithOptions(&gitclient.AddOptions{Path: stagePath}); err != nil {
		return "", err
	}
	message := s.commitMessageWithProvenance("chore(stack): delete Folder Link "+binding.StackPath, &binding)
	hash, err := worktree.Commit(message, s.bindingCommitOptions(repository, &binding, time.Now()))
	if err != nil {
		if errors.Is(err, gitclient.ErrEmptyCommit) {
			return "", nil
		}
		return "", err
	}
	auth, err := s.authForRepository(ctx, repository)
	if err != nil {
		return "", err
	}
	pushCtx, cancel := gitNetworkContext(ctx)
	defer cancel()
	if err := repo.PushContext(pushCtx, &gitclient.PushOptions{RemoteName: "origin", Auth: auth}); err != nil && !errors.Is(err, gitclient.NoErrAlreadyUpToDate) {
		return "", err
	}
	compactRepositoryObjects(repo, binding.RepositoryUUID)
	return hash.String(), nil
}
