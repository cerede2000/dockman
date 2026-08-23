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

type LocalDeletionActionInput struct {
	Action       string `json:"action"`
	Path         string `json:"path,omitempty"`
	Confirmation string `json:"confirmation"`
}

type LocalDeletionActionResult struct {
	Action      string `json:"action"`
	ComposePath string `json:"composePath"`
	CommitSHA   string `json:"commitSha,omitempty"`
	Message     string `json:"message"`
}

type LocalDeletedFileView struct {
	Path       string `json:"path"`
	GitChanged bool   `json:"gitChanged"`
}

type LocalDeletionView struct {
	ComposePath string                 `json:"composePath"`
	WholeStack  bool                   `json:"wholeStack"`
	Files       []LocalDeletedFileView `json:"files"`
}

// ListLocalStackDeletions is intentionally evaluated only when the user opens
// the status popover. It keeps the background status projection compact while
// still exposing every tracked file which needs an explicit decision.
func (s *Service) ListLocalStackDeletions(bindingID, composePath string) (LocalDeletionView, error) {
	composePath = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(composePath))))
	if err := validateRelativePath(composePath, false); err != nil {
		return LocalDeletionView{}, fmt.Errorf("invalid Compose path: %w", err)
	}
	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return LocalDeletionView{}, err
	}
	if !stringInSlice(composePath, selectedComposePaths(binding)) {
		return LocalDeletionView{}, errors.New("stack is not selected for Git synchronization")
	}
	preview, err := s.PreviewBinding(bindingID, "stack_to_repository", TransferInput{})
	if err != nil {
		return LocalDeletionView{}, err
	}
	view := LocalDeletionView{ComposePath: composePath, WholeStack: !bindingComposeExistsLocally(s, binding, composePath), Files: []LocalDeletedFileView{}}
	allCompose := selectedComposePaths(binding)
	for _, entry := range preview.Entries {
		if !stringInSlice(composePath, composePathsForFile(allCompose, entry.Path)) {
			continue
		}
		if entry.Status == "deleted_locally" || (entry.Status == "conflict" && entry.ConflictKind == "source_deleted_destination_changed") {
			view.Files = append(view.Files, LocalDeletedFileView{Path: entry.Path, GitChanged: entry.Status == "conflict"})
		}
	}
	return view, nil
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
	filePath := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(input.Path))))
	if strings.TrimSpace(input.Path) != "" {
		if err := validateRelativePath(filePath, false); err != nil {
			return LocalDeletionActionResult{}, fmt.Errorf("invalid deleted file path: %w", err)
		}
		if action != "restore" && action != "delete_git" && action != "exclude" {
			return LocalDeletionActionResult{}, errors.New("local file deletion action must be restore, delete_git, or exclude")
		}
	} else if action != "restore" && action != "delete_git" && action != "deselect" {
		return LocalDeletionActionResult{}, errors.New("local stack deletion action must be restore, delete_git, or deselect")
	}
	if action == "delete_git" && input.Confirmation != typedConfirmationText {
		return LocalDeletionActionResult{}, fmt.Errorf("type %q to confirm the Git deletion", typedConfirmationText)
	}

	automationLock := s.repositoryLock("automation:" + bindingID)
	if !waitForLock(automationLock, decisionLockBudget) {
		return LocalDeletionActionResult{}, errors.New("automatic synchronization is still running for this Folder Link; pause its automation if you need to decide now")
	}
	defer automationLock.Unlock()

	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return LocalDeletionActionResult{}, err
	}
	if !stringInSlice(composePath, selectedComposePaths(binding)) {
		return LocalDeletionActionResult{}, errors.New("stack is not selected for Git synchronization")
	}
	if strings.TrimSpace(input.Path) != "" {
		return s.resolveLocallyDeletedFile(ctx, binding, composePath, filePath, action)
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

func (s *Service) resolveLocallyDeletedFile(ctx context.Context, binding StackBinding, composePath, filePath, action string) (LocalDeletionActionResult, error) {
	if !stringInSlice(composePath, composePathsForFile(selectedComposePaths(binding), filePath)) {
		return LocalDeletionActionResult{}, errors.New("deleted file does not belong to this synchronized stack")
	}
	if _, err := s.PullRepository(ctx, binding.RepositoryUUID); err != nil {
		return LocalDeletionActionResult{}, fmt.Errorf("refresh repository before local deletion action: %w", err)
	}
	preview, err := s.PreviewBinding(binding.UUID, "stack_to_repository", TransferInput{})
	if err != nil {
		return LocalDeletionActionResult{}, err
	}
	deleted := false
	for _, entry := range preview.Entries {
		if entry.Path != filePath {
			continue
		}
		if entry.Status == "conflict" && entry.ConflictKind == "source_deleted_destination_changed" {
			return LocalDeletionActionResult{}, errors.New("file resolution refused: Git changed after the local deletion; compare and resolve the conflict")
		}
		deleted = entry.Status == "deleted_locally"
		break
	}
	if !deleted {
		if action == "delete_git" && !bindingFileExistsLocally(s, binding, filePath) {
			// Deletion is idempotent. A newly included local file may be removed
			// before its first push, in which case there is deliberately no
			// preview tombstone or remote object to delete.
			return s.deleteLocallyDeletedFileFromGit(ctx, binding, composePath, filePath)
		}
		return LocalDeletionActionResult{}, errors.New("file is no longer a pending local deletion; refresh its synchronization status")
	}

	switch action {
	case "restore":
		remotePreview, err := s.PreviewBinding(binding.UUID, "repository_to_stack", TransferInput{})
		if err != nil {
			return LocalDeletionActionResult{}, err
		}
		result, err := s.ImportBinding(ctx, binding.UUID, TransferInput{PreviewToken: remotePreview.PreviewToken, SelectedPaths: []string{filePath}, compactResult: true})
		if err != nil {
			return LocalDeletionActionResult{}, err
		}
		if err := s.settleBindingAfterOrphanDecision(binding.UUID); err != nil {
			return LocalDeletionActionResult{}, fmt.Errorf("file restored but synchronization state could not be refreshed: %w", err)
		}
		s.recordActivity(ActivityRecord{RepositoryID: binding.RepositoryUUID, BindingID: binding.UUID, ComposePath: composePath, Type: "local_deletion_resolve", Trigger: "manual", Details: ActivityDetails{Action: action, Message: result.Message, Paths: []string{filePath}}})
		return LocalDeletionActionResult{Action: action, ComposePath: composePath, Message: "File restored from Git; no Docker action was run"}, nil
	case "exclude":
		if _, err := s.AddBindingExclusion(binding.UUID, BindingExclusionInput{Path: filePath}); err != nil {
			return LocalDeletionActionResult{}, err
		}
		if err := s.settleBindingAfterOrphanDecision(binding.UUID); err != nil {
			return LocalDeletionActionResult{}, fmt.Errorf("file excluded but synchronization state could not be refreshed: %w", err)
		}
		s.recordActivity(ActivityRecord{RepositoryID: binding.RepositoryUUID, BindingID: binding.UUID, ComposePath: composePath, Type: "local_deletion_resolve", Trigger: "manual", Details: ActivityDetails{Action: action, Paths: []string{filePath}}})
		return LocalDeletionActionResult{Action: action, ComposePath: composePath, Message: "File excluded from synchronization; the Git copy was preserved"}, nil
	default:
		return s.deleteLocallyDeletedFileFromGit(ctx, binding, composePath, filePath)
	}
}

func (s *Service) deleteLocallyDeletedFileFromGit(ctx context.Context, binding StackBinding, composePath, filePath string) (LocalDeletionActionResult, error) {
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
	if local, exists := localFiles[filePath]; exists && local.open != nil {
		return LocalDeletionActionResult{}, errors.New("Git deletion refused: the file exists locally again")
	}
	baseline, err := s.store.BindingBaseline(binding.UUID)
	if err != nil {
		return LocalDeletionActionResult{}, err
	}
	remote, exists := repositoryFiles[filePath]
	if !exists || remote.open == nil {
		delete(baseline, filePath)
		if err := s.store.ReplaceBindingBaseline(binding.UUID, baseline); err != nil {
			return LocalDeletionActionResult{}, err
		}
		if _, err := s.removeDeletedFileExactInclusion(binding, filePath); err != nil {
			return LocalDeletionActionResult{}, err
		}
		if err := s.settleBindingAfterOrphanDecision(binding.UUID); err != nil {
			return LocalDeletionActionResult{}, fmt.Errorf("file was already absent from Git but synchronization state could not be refreshed: %w", err)
		}
		s.recordActivity(ActivityRecord{RepositoryID: binding.RepositoryUUID, BindingID: binding.UUID, ComposePath: composePath, Type: "local_deletion_resolve", Trigger: "manual", Details: ActivityDetails{Action: "delete_git", Message: "File was already absent from Git", Paths: []string{filePath}}})
		return LocalDeletionActionResult{Action: "delete_git", ComposePath: composePath, Message: "File deleted locally; it was already absent from Git, so no commit was needed"}, nil
	}
	baseSHA, tracked := baseline[filePath]
	if !tracked {
		return LocalDeletionActionResult{}, errors.New("Git deletion refused: file has no common synchronization baseline")
	}
	if remote.sha != baseSHA {
		return LocalDeletionActionResult{}, errors.New("Git deletion refused: Git changed after the local deletion; compare and resolve the conflict")
	}
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
	relative := filepath.FromSlash(filePath)
	if binding.SubPath != "." {
		relative = filepath.Join(filepath.FromSlash(binding.SubPath), relative)
	}
	removeErr := root.Remove(relative)
	closeErr := root.Close()
	if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return LocalDeletionActionResult{}, fmt.Errorf("remove Git file %s: %w", filePath, removeErr)
	}
	if closeErr != nil {
		return LocalDeletionActionResult{}, closeErr
	}
	stagePath := binding.SubPath
	if stagePath == "." {
		stagePath = ""
	}
	if err := worktree.AddWithOptions(&gitclient.AddOptions{Path: stagePath}); err != nil {
		return LocalDeletionActionResult{}, fmt.Errorf("stage Git file deletion: %w", err)
	}
	message := s.commitMessageWithProvenance("chore(stack): delete "+filePath+" from Git", &binding)
	hash, err := worktree.Commit(message, s.bindingCommitOptions(repositoryRow, &binding, time.Now()))
	if err != nil {
		return LocalDeletionActionResult{}, fmt.Errorf("commit Git file deletion: %w", err)
	}
	auth, err := s.authForRepository(ctx, repositoryRow)
	if err != nil {
		return LocalDeletionActionResult{}, err
	}
	pushCtx, cancelPush := gitNetworkContext(ctx)
	defer cancelPush()
	if err := repo.PushContext(pushCtx, &gitclient.PushOptions{RemoteName: "origin", Auth: auth}); err != nil && !errors.Is(err, gitclient.NoErrAlreadyUpToDate) {
		return LocalDeletionActionResult{}, fmt.Errorf("push Git file deletion: %w", err)
	}
	delete(baseline, filePath)
	if err := s.store.ReplaceBindingBaseline(binding.UUID, baseline); err != nil {
		return LocalDeletionActionResult{}, err
	}
	if _, err := s.removeDeletedFileExactInclusion(binding, filePath); err != nil {
		return LocalDeletionActionResult{}, err
	}
	compactRepositoryObjects(repo, binding.RepositoryUUID)
	if err := s.settleBindingAfterOrphanDecision(binding.UUID); err != nil {
		return LocalDeletionActionResult{}, fmt.Errorf("Git file deleted but synchronization state could not be refreshed: %w", err)
	}
	s.recordActivity(ActivityRecord{RepositoryID: binding.RepositoryUUID, BindingID: binding.UUID, ComposePath: composePath, Type: "local_deletion_resolve", Trigger: "manual", CommitSHA: hash.String(), Details: ActivityDetails{Action: "delete_git", Paths: []string{filePath}}})
	return LocalDeletionActionResult{Action: "delete_git", ComposePath: composePath, CommitSHA: hash.String(), Message: "File deleted from Git and committed; no Docker action was run"}, nil
}

func bindingFileExistsLocally(s *Service, binding StackBinding, relative string) bool {
	targetFS, targetRoot, err := s.resolveBindingStack(binding)
	if err != nil {
		return false
	}
	info, err := targetFS.Stat(targetFS.Join(targetRoot, filepath.FromSlash(relative)))
	return err == nil && info.Mode().IsRegular()
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
		binding.AutoDeployRollbackEnabled = false
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
	message := s.commitMessageWithProvenance("chore(stack): delete "+composePath+" from Git", &binding)
	hash, err := worktree.Commit(message, s.bindingCommitOptions(repositoryRow, &binding, time.Now()))
	if err != nil {
		return LocalDeletionActionResult{}, fmt.Errorf("commit Git stack deletion: %w", err)
	}
	auth, err := s.authForRepository(ctx, repositoryRow)
	if err != nil {
		return LocalDeletionActionResult{}, err
	}
	pushCtx, cancelPush := gitNetworkContext(ctx)
	defer cancelPush()
	if err := repo.PushContext(pushCtx, &gitclient.PushOptions{RemoteName: "origin", Auth: auth}); err != nil && !errors.Is(err, gitclient.NoErrAlreadyUpToDate) {
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
