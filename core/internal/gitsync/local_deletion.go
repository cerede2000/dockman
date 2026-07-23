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
	"github.com/go-git/go-git/v5/plumbing/object"
)

const deleteGitStackConfirmText = "DELETE STACK FROM GIT"

type LocalDeletionActionInput struct {
	Action       string `json:"action"`
	Confirmation string `json:"confirmation"`
}

type LocalDeletionActionResult struct {
	Action      string `json:"action"`
	ComposePath string `json:"composePath"`
	CommitSHA   string `json:"commitSha,omitempty"`
	Message     string `json:"message"`
}

// ResolveLocalStackDeletion applies an explicit decision when a selected stack
// disappeared locally but remains present in Git. Nothing invokes Compose or
// removes Docker resources.
func (s *Service) ResolveLocalStackDeletion(ctx context.Context, bindingID, composePath string, input LocalDeletionActionInput) (LocalDeletionActionResult, error) {
	composePath = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(composePath))))
	if err := validateRelativePath(composePath, false); err != nil {
		return LocalDeletionActionResult{}, fmt.Errorf("invalid Compose path: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(input.Action))
	if action != "restore" && action != "delete_git" && action != "deselect" {
		return LocalDeletionActionResult{}, errors.New("local deletion action must be restore, delete_git, or deselect")
	}
	if action == "delete_git" && input.Confirmation != deleteGitStackConfirmText {
		return LocalDeletionActionResult{}, fmt.Errorf("type %q to confirm the Git stack deletion", deleteGitStackConfirmText)
	}

	automationLock := s.repositoryLock("automation:" + bindingID)
	if !automationLock.TryLock() {
		return LocalDeletionActionResult{}, errors.New("automatic synchronization is currently running; retry when it finishes")
	}
	defer automationLock.Unlock()

	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return LocalDeletionActionResult{}, err
	}
	if !stringInSlice(composePath, selectedComposePaths(binding)) {
		return LocalDeletionActionResult{}, errors.New("stack is not selected for Git synchronization")
	}
	if bindingComposeExistsLocally(s, binding, composePath) {
		return LocalDeletionActionResult{}, errors.New("stack exists locally again; refresh its synchronization status")
	}
	if action == "deselect" {
		return s.deselectLocallyDeletedStack(binding, composePath)
	}
	if _, err := s.PullRepository(ctx, binding.RepositoryUUID); err != nil {
		return LocalDeletionActionResult{}, fmt.Errorf("refresh repository before local deletion action: %w", err)
	}
	if action == "restore" {
		preview, err := s.PreviewBinding(bindingID, "repository_to_stack", TransferInput{})
		if err != nil {
			return LocalDeletionActionResult{}, err
		}
		paths, conflicts := stackTransferPaths(preview, selectedComposePaths(binding), composePath)
		if conflicts > 0 {
			return LocalDeletionActionResult{}, errors.New("restore refused: Git changed after the local deletion; review and resolve the conflict")
		}
		if len(paths) == 0 {
			return LocalDeletionActionResult{}, errors.New("restore refused: no Git file was found for this stack")
		}
		result, err := s.ImportBinding(ctx, bindingID, TransferInput{PreviewToken: preview.PreviewToken, SelectedPaths: paths, compactResult: true})
		if err != nil {
			return LocalDeletionActionResult{}, err
		}
		if err := s.settleBindingAfterOrphanDecision(bindingID); err != nil {
			return LocalDeletionActionResult{}, fmt.Errorf("stack restored but synchronization state could not be refreshed: %w", err)
		}
		s.recordActivity(ActivityRecord{RepositoryID: binding.RepositoryUUID, BindingID: binding.UUID,
			ComposePath: composePath, Type: "local_deletion_resolve", Trigger: "manual",
			Details: ActivityDetails{Action: action, Message: result.Message, Paths: []string{composePath}}})
		return LocalDeletionActionResult{Action: action, ComposePath: composePath, Message: result.Message}, nil
	}

	return s.deleteLocallyDeletedStackFromGit(ctx, binding, composePath)
}

func (s *Service) deselectLocallyDeletedStack(binding StackBinding, composePath string) (LocalDeletionActionResult, error) {
	repositoryLock := s.repositoryLock(binding.RepositoryUUID)
	repositoryLock.Lock()
	defer repositoryLock.Unlock()
	selected := make([]string, 0)
	for _, path := range selectedComposePaths(binding) {
		if path != composePath {
			selected = append(selected, path)
		}
	}
	binding.ComposeSelectionMode = composeSelectionSelected
	binding.SelectedComposePaths = strings.Join(uniqueSortedStrings(selected), "\n")
	deploy := make([]string, 0)
	for _, path := range splitPatternLines(binding.AutoDeployComposePaths) {
		if path != composePath {
			deploy = append(deploy, path)
		}
	}
	binding.AutoDeployComposePaths = strings.Join(deploy, "\n")
	if binding.AutoDeployEnabled && len(deploy) == 0 && !binding.AutoDeployNewStacks {
		binding.AutoDeployEnabled = false
		binding.AutoDeployState = "disabled"
		binding.AutoDeployError = ""
	}
	if err := s.store.SaveBinding(&binding); err != nil {
		return LocalDeletionActionResult{}, err
	}
	if err := s.reconcileGitStackStatuses(binding); err != nil {
		return LocalDeletionActionResult{}, err
	}
	if err := s.settleBindingAfterOrphanDecision(binding.UUID); err != nil {
		return LocalDeletionActionResult{}, fmt.Errorf("stack deselected but synchronization state could not be refreshed: %w", err)
	}
	s.recordActivity(ActivityRecord{RepositoryID: binding.RepositoryUUID, BindingID: binding.UUID,
		ComposePath: composePath, Type: "local_deletion_resolve", Trigger: "manual",
		Details: ActivityDetails{Action: "deselect", Paths: []string{composePath}}})
	return LocalDeletionActionResult{Action: "deselect", ComposePath: composePath, Message: "Stack removed from synchronization selection; Git files were preserved"}, nil
}

func (s *Service) deleteLocallyDeletedStackFromGit(ctx context.Context, binding StackBinding, composePath string) (LocalDeletionActionResult, error) {
	repositoryLock := s.repositoryLock(binding.RepositoryUUID)
	repositoryLock.Lock()
	defer repositoryLock.Unlock()
	status, err := s.fetchRepositoryLocked(ctx, binding.RepositoryUUID)
	if err != nil {
		return LocalDeletionActionResult{}, err
	}
	if !status.Clean || status.Ahead > 0 || status.Behind > 0 || status.Diverged {
		return LocalDeletionActionResult{}, errors.New("Git deletion refused: repository state changed; pull and retry")
	}
	binding, localFiles, repositoryFiles, err := s.loadTransferTrees(binding.UUID, "stack_to_repository", TransferInput{})
	if err != nil {
		return LocalDeletionActionResult{}, err
	}
	if local, exists := localFiles[composePath]; exists && local.open != nil {
		return LocalDeletionActionResult{}, errors.New("Git deletion refused: the stack exists locally again")
	}
	baseline, err := s.store.BindingBaseline(binding.UUID)
	if err != nil {
		return LocalDeletionActionResult{}, err
	}
	if _, tracked := baseline[composePath]; !tracked {
		return LocalDeletionActionResult{}, errors.New("Git deletion refused: stack has no common synchronization baseline")
	}
	allCompose := splitPatternLines(binding.ComposePaths)
	paths := make([]string, 0)
	for path, file := range repositoryFiles {
		if file.open != nil && stringInSlice(composePath, composePathsForFile(allCompose, path)) {
			paths = append(paths, path)
		}
	}
	if !stringInSlice(composePath, paths) {
		return LocalDeletionActionResult{}, errors.New("Git deletion refused: Compose file is no longer present in Git")
	}
	sort.Strings(paths)

	repositoryRow, err := s.store.GetRepository(binding.RepositoryUUID)
	if err != nil {
		return LocalDeletionActionResult{}, err
	}
	repo, err := s.openRepository(repositoryRow)
	if err != nil {
		return LocalDeletionActionResult{}, err
	}
	temporaryRepo, checkoutPath, cleanup, err := temporaryRepositoryWorktree(repo, s.workspaceRoot)
	if err != nil {
		return LocalDeletionActionResult{}, err
	}
	defer cleanup()
	worktree, err := temporaryRepo.Worktree()
	if err != nil {
		return LocalDeletionActionResult{}, err
	}
	if err := worktree.Checkout(&gitclient.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(repositoryRow.DefaultBranch), Force: true}); err != nil {
		return LocalDeletionActionResult{}, fmt.Errorf("prepare temporary Git checkout: %w", err)
	}
	root, err := os.OpenRoot(checkoutPath)
	if err != nil {
		return LocalDeletionActionResult{}, err
	}
	stackDirectory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(composePath)))
	dedicatedDirectory := stackDirectory != "." && stackDirectory != ""
	if dedicatedDirectory {
		for _, candidate := range allCompose {
			if candidate == composePath {
				continue
			}
			candidateDirectory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(candidate)))
			if candidateDirectory == stackDirectory || strings.HasPrefix(candidateDirectory, stackDirectory+"/") {
				dedicatedDirectory = false
				break
			}
		}
	}
	if dedicatedDirectory {
		relative := filepath.FromSlash(stackDirectory)
		if binding.SubPath != "." {
			relative = filepath.Join(filepath.FromSlash(binding.SubPath), relative)
		}
		if err := root.RemoveAll(relative); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = root.Close()
			return LocalDeletionActionResult{}, fmt.Errorf("remove Git stack directory %s: %w", stackDirectory, err)
		}
	} else {
		for _, path := range paths {
			relative := filepath.FromSlash(path)
			if binding.SubPath != "." {
				relative = filepath.Join(filepath.FromSlash(binding.SubPath), relative)
			}
			if err := root.Remove(relative); err != nil && !errors.Is(err, os.ErrNotExist) {
				_ = root.Close()
				return LocalDeletionActionResult{}, fmt.Errorf("remove Git file %s: %w", path, err)
			}
		}
	}
	if err := root.Close(); err != nil {
		return LocalDeletionActionResult{}, err
	}
	stagePath := binding.SubPath
	if stagePath == "." {
		stagePath = ""
	}
	if err := worktree.AddWithOptions(&gitclient.AddOptions{Path: stagePath}); err != nil {
		return LocalDeletionActionResult{}, fmt.Errorf("stage Git stack deletion: %w", err)
	}
	hash, err := worktree.Commit("chore(stack): delete "+composePath+" from Git", &gitclient.CommitOptions{Author: &object.Signature{Name: "Dockman Git Sync", Email: "dockman@localhost.invalid", When: time.Now().UTC()}})
	if err != nil {
		return LocalDeletionActionResult{}, fmt.Errorf("commit Git stack deletion: %w", err)
	}
	auth, err := s.authForRepository(ctx, repositoryRow)
	if err != nil {
		return LocalDeletionActionResult{}, err
	}
	if err := repo.PushContext(ctx, &gitclient.PushOptions{RemoteName: "origin", Auth: auth}); err != nil && !errors.Is(err, gitclient.NoErrAlreadyUpToDate) {
		return LocalDeletionActionResult{}, fmt.Errorf("push Git stack deletion: %w", err)
	}
	for path := range baseline {
		if stringInSlice(composePath, composePathsForFile(allCompose, path)) {
			delete(baseline, path)
		}
	}
	if err := s.store.ReplaceBindingBaseline(binding.UUID, baseline); err != nil {
		return LocalDeletionActionResult{}, err
	}
	if _, _, err := s.refreshBindingComposeCatalogLocked(binding); err != nil {
		return LocalDeletionActionResult{}, err
	}
	compactRepositoryObjects(repo, binding.RepositoryUUID)
	if err := s.settleBindingAfterOrphanDecision(binding.UUID); err != nil {
		return LocalDeletionActionResult{}, fmt.Errorf("Git stack deleted but synchronization state could not be refreshed: %w", err)
	}
	s.recordActivity(ActivityRecord{RepositoryID: binding.RepositoryUUID, BindingID: binding.UUID,
		ComposePath: composePath, Type: "local_deletion_resolve", Trigger: "manual", CommitSHA: hash.String(),
		Details: ActivityDetails{Action: "delete_git", Paths: []string{composePath}}})
	return LocalDeletionActionResult{Action: "delete_git", ComposePath: composePath, CommitSHA: hash.String(), Message: "Stack deleted from Git and committed; no Docker action was run"}, nil
}
