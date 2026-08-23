package gitsync

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	stackSyncPending       = "pending"
	stackSyncUnselected    = "unselected"
	stackSyncUpToDate      = "up_to_date"
	stackSyncChecking      = "checking"
	stackSyncLocalChanges  = "local_changes"
	stackSyncLocalDeleted  = "locally_deleted"
	stackSyncRemoteChanges = "remote_changes"
	stackSyncOrphaned      = "orphaned"
	stackSyncConflict      = "conflict"
	stackSyncError         = "error"
	stackPauseManual       = "manual"
	stackPauseRecovery     = "recovery"
)

var errNoTransferableLocalChanges = errors.New("no transferable local change was found for this stack")

type GitStackStatusView struct {
	BindingID                 string     `json:"bindingId"`
	Host                      string     `json:"host"`
	StackPath                 string     `json:"stackPath"`
	ComposePath               string     `json:"composePath"`
	FullComposePath           string     `json:"fullComposePath"`
	RepositoryID              string     `json:"repositoryId"`
	RepositoryName            string     `json:"repositoryName"`
	RepositoryBranch          string     `json:"repositoryBranch"`
	RepositorySubPath         string     `json:"repositorySubPath"`
	State                     string     `json:"state"`
	Selected                  bool       `json:"selected"`
	Error                     string     `json:"error,omitempty"`
	ConflictCount             int        `json:"conflictCount"`
	AutoSyncEnabled           bool       `json:"autoSyncEnabled"`
	StackAutoSyncEnabled      bool       `json:"stackAutoSyncEnabled"`
	BindingAutomationPaused   bool       `json:"bindingAutomationPaused"`
	BindingSyncState          string     `json:"bindingSyncState"`
	BindingSyncError          string     `json:"bindingSyncError,omitempty"`
	AutomationPaused          bool       `json:"automationPaused"`
	PauseReason               string     `json:"pauseReason,omitempty"`
	AutoDeployEnabled         bool       `json:"autoDeployEnabled"`
	AutoDeployRollbackEnabled bool       `json:"autoDeployRollbackEnabled"`
	AutoSyncInterval          int        `json:"autoSyncIntervalMinutes"`
	LastCheckedAt             *time.Time `json:"lastCheckedAt,omitempty"`
	LastSuccessAt             *time.Time `json:"lastSuccessAt,omitempty"`
	NextCheckAt               *time.Time `json:"nextCheckAt,omitempty"`
	LastCommit                string     `json:"lastCommit,omitempty"`
	DeployState               string     `json:"deployState"`
	DeployError               string     `json:"deployError,omitempty"`
	LastDeployAt              *time.Time `json:"lastDeployAt,omitempty"`
}

type GitStackPauseInput struct {
	Paused bool `json:"paused"`
}

type GitTrackedFilesInput struct {
	Host  string   `json:"host"`
	Paths []string `json:"paths"`
}

type GitTrackedFilesView struct {
	TrackedPaths []string             `json:"trackedPaths"`
	Files        []GitTrackedFileView `json:"files"`
}

type GitTrackedFileView struct {
	Path           string `json:"path"`
	BindingID      string `json:"bindingId,omitempty"`
	ComposePath    string `json:"composePath,omitempty"`
	RelativePath   string `json:"relativePath,omitempty"`
	Linked         bool   `json:"linked"`
	Tracked        bool   `json:"tracked"`
	Mutable        bool   `json:"mutable"`
	FolderLinkRoot bool   `json:"folderLinkRoot,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

type GitFileTrackingInput struct {
	Host      string `json:"host"`
	Path      string `json:"path"`
	BindingID string `json:"bindingId"`
	Tracked   bool   `json:"tracked"`
	Deleted   bool   `json:"deleted,omitempty"`
}

func initialStackSyncState(binding StackBinding) string {
	switch binding.InitialSyncState {
	case "reconciled", "imported", "exported":
		return stackSyncUpToDate
	default:
		// An initial-sync failure belongs to the Folder Link until a bounded
		// preview identifies an exact stack/file failure. Keeping new rows pending
		// prevents a transient repository error from painting every stack red and
		// from being resurrected during application startup reconciliation.
		return stackSyncPending
	}
}

func (s *Service) reconcileGitStackStatuses(binding StackBinding) error {
	return s.store.ReconcileGitStackStatuses(binding, selectedComposePaths(binding), initialStackSyncState(binding))
}

func (s *Service) InitializeGitStackStatuses() error {
	if !s.enabled {
		return nil
	}
	bindings, err := s.store.ListBindings()
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if err := s.reconcileGitStackStatuses(binding); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ListGitStackStatusViews(host string) ([]GitStackStatusView, error) {
	host = strings.TrimSpace(host)
	rows, err := s.store.ListGitStackStatuses(host)
	if err != nil {
		return nil, err
	}
	bindings, err := s.store.ListBindingsForHost(host)
	if err != nil {
		return nil, err
	}
	bindingByID := make(map[string]StackBinding, len(bindings))
	autoSyncByBinding := make(map[string]map[string]struct{}, len(bindings))
	repositoryIDs := make(map[string]struct{})
	for _, binding := range bindings {
		if host == "" || binding.Host == host {
			bindingByID[binding.UUID] = binding
			autoTargets := make(map[string]struct{})
			for _, composePath := range autoSyncComposePaths(binding) {
				autoTargets[composePath] = struct{}{}
			}
			autoSyncByBinding[binding.UUID] = autoTargets
			repositoryIDs[binding.RepositoryUUID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(repositoryIDs))
	for id := range repositoryIDs {
		ids = append(ids, id)
	}
	repositories, err := s.store.RepositoriesByIDs(ids)
	if err != nil {
		return nil, err
	}
	repositoryByID := make(map[string]Repository, len(repositories))
	for _, repository := range repositories {
		repositoryByID[repository.UUID] = repository
	}
	result := make([]GitStackStatusView, 0, len(rows))
	for _, row := range rows {
		binding, ok := bindingByID[row.BindingUUID]
		if !ok {
			continue
		}
		repository := repositoryByID[binding.RepositoryUUID]
		state := row.State
		stackError := row.ErrorMessage
		// Releases before the binding/stack error split copied one transient
		// repository failure into every stack row. Repair that legacy projection
		// on read: the Folder Link remains red, while a stack with a previous
		// successful check keeps its last known healthy state.
		if binding.AutoSyncState == "error" && state == stackSyncError && stackError != "" && stackError == binding.AutoSyncError {
			stackError = ""
			if row.LastSuccessAt != nil {
				state = stackSyncUpToDate
			} else {
				state = stackSyncPending
			}
		}
		if row.AutomationPaused && state == stackSyncChecking {
			state = stackSyncPending
		}
		deployEnabled := binding.AutoDeployEnabled && stringInSlice(row.ComposePath, splitPatternLines(binding.AutoDeployComposePaths))
		_, stackAutoSyncEnabled := autoSyncByBinding[binding.UUID][row.ComposePath]
		if binding.AutoSyncState == "syncing" && stackAutoSyncEnabled && !binding.AutoSyncPaused && !row.AutomationPaused {
			switch state {
			case stackSyncPending, stackSyncUpToDate, stackSyncRemoteChanges:
				state = stackSyncChecking
			}
		}
		deployState := row.DeployState
		if !deployEnabled {
			deployState = "disabled"
		} else if deployState == "" || deployState == "disabled" {
			deployState = "idle"
		}
		var nextCheck *time.Time
		if binding.AutoSyncEnabled && stackAutoSyncEnabled && !binding.AutoSyncPaused && !row.AutomationPaused {
			base := time.Now().UTC()
			if binding.LastAutoSyncAt != nil {
				base = *binding.LastAutoSyncAt
			}
			next := base.Add(time.Duration(normalizedAutoSyncInterval(binding.AutoSyncIntervalMinutes)) * time.Minute)
			nextCheck = &next
		}
		result = append(result, GitStackStatusView{
			BindingID: binding.UUID, Host: binding.Host, StackPath: binding.StackPath,
			ComposePath: row.ComposePath, FullComposePath: filepath.ToSlash(filepath.Join(binding.StackPath, row.ComposePath)),
			RepositoryID: repository.UUID, RepositoryName: repository.Name, RepositoryBranch: repository.DefaultBranch,
			RepositorySubPath: filepath.ToSlash(filepath.Join(binding.SubPath, row.ComposePath)),
			State:             state, Selected: true, Error: stackError, ConflictCount: row.ConflictCount,
			AutoSyncEnabled: binding.AutoSyncEnabled, StackAutoSyncEnabled: stackAutoSyncEnabled, BindingAutomationPaused: binding.AutoSyncPaused, BindingSyncState: binding.AutoSyncState, BindingSyncError: binding.AutoSyncError,
			AutomationPaused: row.AutomationPaused, PauseReason: row.PauseReason,
			AutoDeployEnabled: deployEnabled, AutoDeployRollbackEnabled: deployEnabled && binding.AutoDeployRollbackEnabled,
			AutoSyncInterval: normalizedAutoSyncInterval(binding.AutoSyncIntervalMinutes),
			LastCheckedAt:    row.LastCheckedAt, LastSuccessAt: row.LastSuccessAt, NextCheckAt: nextCheck,
			LastCommit: row.LastCommit, DeployState: deployState, DeployError: row.DeployError, LastDeployAt: row.LastDeployAt,
		})
	}
	existing := make(map[string]struct{}, len(result))
	for _, view := range result {
		existing[view.BindingID+"\x00"+view.ComposePath] = struct{}{}
	}
	for _, binding := range bindings {
		repository := repositoryByID[binding.RepositoryUUID]
		selected := make(map[string]struct{})
		for _, path := range selectedComposePaths(binding) {
			selected[path] = struct{}{}
		}
		for _, composePath := range splitPatternLines(binding.ComposePaths) {
			if _, approved := selected[composePath]; approved {
				continue
			}
			if _, present := existing[binding.UUID+"\x00"+composePath]; present {
				continue
			}
			result = append(result, GitStackStatusView{
				BindingID: binding.UUID, Host: binding.Host, StackPath: binding.StackPath,
				ComposePath: composePath, FullComposePath: filepath.ToSlash(filepath.Join(binding.StackPath, composePath)),
				RepositoryID: repository.UUID, RepositoryName: repository.Name, RepositoryBranch: repository.DefaultBranch,
				RepositorySubPath: filepath.ToSlash(filepath.Join(binding.SubPath, composePath)),
				State:             stackSyncUnselected, Selected: false, AutoSyncEnabled: binding.AutoSyncEnabled,
				BindingAutomationPaused: binding.AutoSyncPaused, BindingSyncState: binding.AutoSyncState,
				DeployState: "disabled",
			})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Host != result[j].Host {
			return result[i].Host < result[j].Host
		}
		return result[i].FullComposePath < result[j].FullComposePath
	})
	return result, nil
}

// GitTrackedFiles evaluates already displayed file paths against the exact
// effective Folder Link policy. It does not scan the stack tree or Git
// repository, so the Files view can render passive badges without adding a
// background inventory or one request per row.
func (s *Service) GitTrackedFiles(input GitTrackedFilesInput) (GitTrackedFilesView, error) {
	input.Host = strings.TrimSpace(input.Host)
	if input.Host == "" {
		return GitTrackedFilesView{}, errors.New("host is required")
	}
	if len(input.Paths) > 1000 {
		return GitTrackedFilesView{}, errors.New("at most 1000 file paths can be checked at once")
	}
	bindings, err := s.store.ListBindingsForHost(input.Host)
	if err != nil {
		return GitTrackedFilesView{}, err
	}
	type tracker struct {
		binding StackBinding
		policy  syncPolicy
		ignore  []ignoreRule
	}
	trackers := make([]tracker, 0, len(bindings))
	for _, binding := range bindings {
		targetFS, targetRoot, resolveErr := s.resolveBindingStack(binding)
		if resolveErr != nil {
			continue
		}
		repository, repositoryErr := s.store.GetRepository(binding.RepositoryUUID)
		if repositoryErr != nil {
			continue
		}
		policy, policyErr := policyFromBinding(binding, repository)
		if policyErr != nil {
			continue
		}
		ignore, ignoreErr := loadStackIgnoreRules(targetFS, targetRoot)
		if ignoreErr != nil {
			continue
		}
		trackers = append(trackers, tracker{binding: binding, policy: policy.withComposeDirectoryIndex(), ignore: ignore})
	}
	tracked := make([]string, 0, len(input.Paths))
	files := make([]GitTrackedFileView, 0, len(input.Paths))
	seen := make(map[string]struct{}, len(input.Paths))
	for _, raw := range input.Paths {
		fullPath := strings.Trim(filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(raw)))), "/")
		if fullPath == "" || fullPath == "." {
			continue
		}
		if err := validateRelativePath(fullPath, false); err != nil {
			return GitTrackedFilesView{}, fmt.Errorf("invalid file path %q: %w", raw, err)
		}
		if _, duplicate := seen[fullPath]; duplicate {
			continue
		}
		seen[fullPath] = struct{}{}
		view := GitTrackedFileView{Path: fullPath}
		for _, tracker := range trackers {
			binding := tracker.binding
			root := strings.Trim(filepath.ToSlash(filepath.Clean(filepath.FromSlash(binding.StackPath))), "/")
			relative := fullPath
			if root != "" && root != "." {
				if fullPath == root {
					view.Linked = true
					view.Tracked = true
					view.BindingID = binding.UUID
					view.FolderLinkRoot = true
					view.Reason = "This directory is the root of a Git Folder Link"
					tracked = append(tracked, fullPath)
					break
				}
				if !strings.HasPrefix(fullPath, root+"/") {
					continue
				}
				relative = strings.TrimPrefix(fullPath, root+"/")
			}
			composePaths := composePathsForFile(selectedComposePaths(binding), relative)
			if len(composePaths) == 0 {
				continue
			}
			view.Linked = true
			view.BindingID = binding.UUID
			view.ComposePath = composePaths[0]
			view.RelativePath = relative
			view.Mutable, view.Reason = mutableGitFilePolicyPath(tracker.policy, relative)
			view.Tracked = policyTracksLocalFile(tracker.policy, tracker.ignore, relative)
			if view.Tracked {
				tracked = append(tracked, fullPath)
			}
			break
		}
		files = append(files, view)
	}
	sort.Strings(tracked)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return GitTrackedFilesView{TrackedPaths: tracked, Files: files}, nil
}

func mutableGitFilePolicyPath(policy syncPolicy, relative string) (bool, string) {
	if policy.protectsCompose(relative) {
		return false, "Compose manifests are protected by the Folder Link"
	}
	if policy.protectsProvision(relative) {
		return false, "provisioning control files are protected"
	}
	if isSensitivePath(relative) {
		return false, "sensitive files require the separate one-time transfer confirmation"
	}
	if shouldSkipPath(relative, false) {
		return false, "Dockman internal and special paths are never synchronized"
	}
	return true, ""
}

// EnableGitStackSynchronization atomically approves a catalogued local stack.
// It does not export, import, deploy, or otherwise touch the stack files.
func (s *Service) EnableGitStackSynchronization(bindingID, composePath string) (GitStackStatusView, error) {
	automationLock := s.repositoryLock("automation:" + bindingID)
	automationLock.Lock()
	defer automationLock.Unlock()
	composePath = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(composePath))))
	if err := validateRelativePath(composePath, false); err != nil {
		return GitStackStatusView{}, fmt.Errorf("invalid Compose path: %w", err)
	}
	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return GitStackStatusView{}, err
	}
	previousAutoTargets := strings.Join(autoSyncComposePaths(binding), "\n")
	ownershipLock := s.repositoryLock("binding-ownership:" + binding.Host)
	ownershipLock.Lock()
	defer ownershipLock.Unlock()
	repositoryLock := s.repositoryLock(binding.RepositoryUUID)
	repositoryLock.Lock()
	defer repositoryLock.Unlock()
	if !stringInSlice(composePath, splitPatternLines(binding.ComposePaths)) {
		return GitStackStatusView{}, errors.New("stack is not present in this folder link catalog; refresh its files and retry")
	}
	selected := uniqueSortedStrings(append(selectedComposePaths(binding), composePath))
	binding.ComposeSelectionMode = composeSelectionSelected
	if len(selected) == len(splitPatternLines(binding.ComposePaths)) {
		binding.ComposeSelectionMode = composeSelectionAll
	}
	binding.SelectedComposePaths = strings.Join(selected, "\n")
	if err := s.validateBindingOwnership(binding, binding.UUID); err != nil {
		return GitStackStatusView{}, err
	}
	if strings.Join(autoSyncComposePaths(binding), "\n") != previousAutoTargets {
		binding.LastAutoSyncCommit = ""
	}
	if err := s.store.SaveBinding(&binding); err != nil {
		return GitStackStatusView{}, err
	}
	if err := s.reconcileGitStackStatuses(binding); err != nil {
		return GitStackStatusView{}, err
	}
	now := time.Now().UTC()
	state := stackSyncLocalChanges
	if !bindingComposeExistsLocally(s, binding, composePath) {
		state = stackSyncRemoteChanges
		if baseline, baselineErr := s.store.BindingBaseline(binding.UUID); baselineErr == nil {
			if _, tracked := baseline[composePath]; tracked {
				state = stackSyncLocalDeleted
			}
		}
	}
	if err := s.store.UpdateGitStackStatuses(binding.UUID, []string{composePath}, map[string]any{
		"state": state, "error_message": "", "conflict_count": 0, "last_checked_at": &now,
	}); err != nil {
		return GitStackStatusView{}, err
	}
	views, err := s.ListGitStackStatusViews(binding.Host)
	if err != nil {
		return GitStackStatusView{}, err
	}
	for _, view := range views {
		if view.BindingID == bindingID && view.ComposePath == composePath {
			return view, nil
		}
	}
	return GitStackStatusView{}, gorm.ErrRecordNotFound
}

func (s *Service) SetGitStackAutomationPause(bindingID, composePath string, paused bool) (GitStackStatusView, error) {
	automationLock := s.repositoryLock("automation:" + bindingID)
	if !automationLock.TryLock() {
		return GitStackStatusView{}, errors.New("automatic synchronization is currently running; retry when it finishes")
	}
	defer automationLock.Unlock()

	composePath = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(composePath))))
	if err := validateRelativePath(composePath, false); err != nil {
		return GitStackStatusView{}, fmt.Errorf("invalid Compose path: %w", err)
	}
	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return GitStackStatusView{}, err
	}
	if !stringInSlice(composePath, autoSyncComposePaths(binding)) {
		return GitStackStatusView{}, errors.New("stack is not enabled for automatic Git synchronization")
	}
	if err := s.reconcileGitStackStatuses(binding); err != nil {
		return GitStackStatusView{}, err
	}
	if err := s.store.SetGitStackPause(bindingID, composePath, paused); err != nil {
		return GitStackStatusView{}, err
	}
	views, err := s.ListGitStackStatusViews(binding.Host)
	if err != nil {
		return GitStackStatusView{}, err
	}
	for _, view := range views {
		if view.BindingID == bindingID && view.ComposePath == composePath {
			return view, nil
		}
	}
	return GitStackStatusView{}, gorm.ErrRecordNotFound
}

// ResumeGitStackAutomation safely resumes a manually paused stack. A local
// rollback/restore must first be published, otherwise a Git -> Dockman cycle
// could overwrite the explicit local recovery. The stack remains paused when
// the push fails or a conflict needs a decision.
func (s *Service) ResumeGitStackAutomation(ctx context.Context, bindingID, composePath string) (GitStackStatusView, bool, error) {
	automationLock := s.repositoryLock("automation:" + bindingID)
	if !automationLock.TryLock() {
		return GitStackStatusView{}, false, errors.New("automatic synchronization is currently running; retry when it finishes")
	}
	defer automationLock.Unlock()

	composePath = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(composePath))))
	if err := validateRelativePath(composePath, false); err != nil {
		return GitStackStatusView{}, false, fmt.Errorf("invalid Compose path: %w", err)
	}
	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return GitStackStatusView{}, false, err
	}
	if !stringInSlice(composePath, autoSyncComposePaths(binding)) {
		return GitStackStatusView{}, false, errors.New("stack is not enabled for automatic Git synchronization")
	}
	if err := s.reconcileGitStackStatuses(binding); err != nil {
		return GitStackStatusView{}, false, err
	}
	status, err := s.store.GitStackStatus(bindingID, composePath)
	if err != nil {
		return GitStackStatusView{}, false, err
	}
	pushed := false
	if status.State == stackSyncLocalChanges || status.State == stackSyncOrphaned {
		if _, err := s.PushGitStack(ctx, bindingID, composePath); err != nil {
			if !errors.Is(err, errNoTransferableLocalChanges) {
				return GitStackStatusView{}, false, fmt.Errorf("resume kept paused because local changes could not be pushed: %w", err)
			}
		} else {
			pushed = true
		}
	}
	if err := s.store.SetGitStackPause(bindingID, composePath, false); err != nil {
		return GitStackStatusView{}, pushed, fmt.Errorf("changes were pushed but automatic synchronization could not be resumed: %w", err)
	}
	view, err := s.gitStackStatusView(binding.Host, bindingID, composePath)
	return view, pushed, err
}

func (s *Service) gitStackStatusView(host, bindingID, composePath string) (GitStackStatusView, error) {
	views, err := s.ListGitStackStatusViews(host)
	if err != nil {
		return GitStackStatusView{}, err
	}
	for _, view := range views {
		if view.BindingID == bindingID && view.ComposePath == composePath {
			return view, nil
		}
	}
	return GitStackStatusView{}, gorm.ErrRecordNotFound
}

// PushGitStack performs the same preview-token protected export as the full
// Git settings view, but limits the transfer to the stack represented by one
// status indicator. It never auto-resolves conflicts or includes skipped
// sensitive/excluded files.
func (s *Service) PushGitStack(ctx context.Context, bindingID, composePath string) (TransferResult, error) {
	composePath = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(composePath))))
	if err := validateRelativePath(composePath, false); err != nil {
		return TransferResult{}, fmt.Errorf("invalid Compose path: %w", err)
	}
	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return TransferResult{}, err
	}
	if !stringInSlice(composePath, selectedComposePaths(binding)) {
		return TransferResult{}, errors.New("stack is not selected for Git synchronization")
	}
	preview, err := s.PreviewBinding(bindingID, "stack_to_repository", TransferInput{})
	if err != nil {
		return TransferResult{}, err
	}
	selected, conflicts := stackTransferPaths(preview, selectedComposePaths(binding), composePath)
	if conflicts > 0 {
		return TransferResult{}, errors.New("push refused: this stack has a conflict; review and resolve it before pushing")
	}
	if len(selected) == 0 {
		return TransferResult{}, errNoTransferableLocalChanges
	}
	result, err := s.ExportBinding(ctx, bindingID, TransferInput{PreviewToken: preview.PreviewToken, SelectedPaths: selected, compactResult: true})
	if err != nil {
		return TransferResult{}, err
	}
	// Re-evaluate every compact badge after the partial export so unrelated
	// local changes in the same folder link remain visible.
	_, _ = s.PreviewBinding(bindingID, "stack_to_repository", TransferInput{})
	result.Message = fmt.Sprintf("Stack pushed to Git (%d file(s))", len(selected))
	return result, nil
}

// PushGitStackAndResume serializes a manual status-indicator push with the
// automatic worker and clears a recovery pause only after the push succeeds.
func (s *Service) PushGitStackAndResume(ctx context.Context, bindingID, composePath string) (TransferResult, error) {
	automationLock := s.repositoryLock("automation:" + bindingID)
	if !automationLock.TryLock() {
		return TransferResult{}, errors.New("automatic synchronization is currently running; retry when it finishes")
	}
	defer automationLock.Unlock()

	composePath = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(composePath))))
	status, statusErr := s.store.GitStackStatus(bindingID, composePath)
	result, err := s.PushGitStack(ctx, bindingID, composePath)
	if err != nil {
		if errors.Is(err, errNoTransferableLocalChanges) {
			if statusErr == nil && status.AutomationPaused && status.PauseReason == stackPauseRecovery {
				if resumeErr := s.store.SetGitStackPause(bindingID, composePath, false); resumeErr != nil {
					return TransferResult{}, resumeErr
				}
				return TransferResult{Message: "Stack already matches Git; automatic synchronization resumed"}, nil
			}
			// PreviewBinding has just recomputed the authoritative stack state.
			// A stale local-change badge can therefore be acknowledged cleanly
			// when the only local files are protected or outside the effective
			// policy, instead of presenting a contradictory push error.
			return TransferResult{Message: "Stack already matches Git; ignored, excluded, or sensitive local files were not pushed"}, nil
		}
		return TransferResult{}, err
	}
	if statusErr == nil && status.AutomationPaused && status.PauseReason == stackPauseRecovery {
		result.Message += "; automatic synchronization resumed"
	}
	return result, nil
}

// SyncGitStackNow performs a safe, one-shot Git -> Dockman refresh for one
// selected stack. Unlike folder-link automation it remains available in
// manual mode, never deploys, and never propagates a deletion implicitly.
func (s *Service) SyncGitStackNow(ctx context.Context, bindingID, composePath string) (TransferResult, error) {
	automationLock := s.repositoryLock("automation:" + bindingID)
	if !automationLock.TryLock() {
		return TransferResult{}, errors.New("Git synchronization is currently running for this folder link; retry when it finishes")
	}
	defer automationLock.Unlock()

	composePath = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(composePath))))
	if err := validateRelativePath(composePath, false); err != nil {
		return TransferResult{}, fmt.Errorf("invalid Compose path: %w", err)
	}
	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return TransferResult{}, err
	}
	composePaths := selectedComposePaths(binding)
	if !stringInSlice(composePath, composePaths) {
		return TransferResult{}, errors.New("stack is not selected for Git synchronization")
	}
	// Refresh and fast-forward the compact local Git metadata first. The stack
	// preview must compare against the provider's current commit, not the last
	// scheduled fetch. PullRepository refuses ahead/diverged histories.
	if _, err := s.PullRepository(ctx, binding.RepositoryUUID); err != nil {
		return TransferResult{}, fmt.Errorf("refresh repository before stack synchronization: %w", err)
	}

	preview, err := s.PreviewBinding(bindingID, "repository_to_stack", TransferInput{})
	if err != nil {
		return TransferResult{}, err
	}
	selected, conflicts, preservedDeletion := stackImportPaths(preview, composePaths, composePath)
	if conflicts > 0 {
		return TransferResult{}, errors.New("synchronization refused: this stack has a conflict or a local deletion; review and resolve it before importing from Git")
	}
	if preservedDeletion {
		return TransferResult{}, errors.New("synchronization requires a decision: this stack or one of its files was deleted on Git and is preserved locally")
	}
	if len(selected) == 0 {
		return TransferResult{Message: "No Git change to import for this stack"}, nil
	}
	result, err := s.ImportBinding(ctx, bindingID, TransferInput{
		PreviewToken:       preview.PreviewToken,
		SelectedPaths:      selected,
		compactResult:      true,
		targetComposePaths: []string{composePath},
	})
	if err != nil {
		return TransferResult{}, err
	}
	result.Message = fmt.Sprintf("Stack synchronized from Git (%d file(s)); no deployment was triggered", len(selected))
	return result, nil
}

func stackTransferPaths(preview TransferPreview, composePaths []string, targetCompose string) ([]string, int) {
	selected := make([]string, 0)
	conflicts := 0
	for _, entry := range preview.Entries {
		if !stringInSlice(targetCompose, composePathsForFile(composePaths, entry.Path)) {
			continue
		}
		switch entry.Status {
		case "add", "modify":
			selected = append(selected, entry.Path)
		case "conflict":
			conflicts++
		}
	}
	sort.Strings(selected)
	return selected, conflicts
}

func stackImportPaths(preview TransferPreview, composePaths []string, targetCompose string) ([]string, int, bool) {
	selected := make([]string, 0)
	conflicts := 0
	preservedDeletion := false
	for _, entry := range preview.Entries {
		if !stringInSlice(targetCompose, composePathsForFile(composePaths, entry.Path)) {
			continue
		}
		switch entry.Status {
		case "add", "modify", "remove_control":
			if entry.ConflictKind == "destination_deleted" {
				conflicts++
				continue
			}
			selected = append(selected, entry.Path)
		case "conflict", "deleted_locally":
			conflicts++
		case "deleted_on_git":
			preservedDeletion = true
		}
	}
	sort.Strings(selected)
	return selected, conflicts, preservedDeletion
}

func (s *Service) recordPreviewStackStatuses(binding StackBinding, preview TransferPreview) {
	paths := selectedComposePaths(binding)
	if preview.automation {
		paths = s.activeAutomationComposePaths(binding)
	}
	now := time.Now().UTC()
	type aggregate struct {
		state     string
		conflicts int
		message   string
	}
	states := make(map[string]aggregate, len(paths))
	orphans := make(map[string]struct{}, len(preview.OrphanedComposePaths))
	for _, composePath := range preview.OrphanedComposePaths {
		orphans[composePath] = struct{}{}
	}
	for _, composePath := range paths {
		states[composePath] = aggregate{state: stackSyncUpToDate}
	}
	for _, entry := range preview.Entries {
		for _, composePath := range composePathsForFile(paths, entry.Path) {
			current := states[composePath]
			switch entry.Status {
			case "skipped_large_directory":
				if current.state != stackSyncConflict {
					current.state = stackSyncError
					current.message = "A large local data directory was automatically skipped while other stacks continued: " + entry.Path
				}
			case "skipped_permission":
				if current.state != stackSyncConflict {
					current.state = stackSyncError
					current.message = "A local stack item cannot be read; it was skipped while other stacks continued: " + entry.Path
				}
			case "conflict":
				current.state = stackSyncConflict
				current.conflicts++
			case "deleted_on_git":
				if current.state != stackSyncConflict {
					if _, orphaned := orphans[composePath]; orphaned {
						current.state = stackSyncOrphaned
					} else {
						current.state = stackSyncLocalChanges
					}
				}
			case "add", "modify":
				if current.state != stackSyncConflict {
					if current.state == stackSyncOrphaned || current.state == stackSyncLocalDeleted {
						break
					}
					if preview.Direction == "repository_to_stack" && entry.ConflictKind == "destination_deleted" {
						current.state = stackSyncLocalDeleted
					} else if preview.Direction == "stack_to_repository" {
						current.state = stackSyncLocalChanges
					} else {
						current.state = stackSyncRemoteChanges
					}
				}
			case "deleted_locally":
				if current.state != stackSyncConflict {
					current.state = stackSyncLocalDeleted
				}
			}
			states[composePath] = current
		}
	}
	for composePath, state := range states {
		updates := map[string]any{"state": state.state, "error_message": state.message, "conflict_count": state.conflicts, "last_checked_at": &now}
		if state.state == stackSyncUpToDate {
			updates["last_success_at"] = &now
		}
		_ = s.store.UpdateGitStackStatuses(binding.UUID, []string{composePath}, updates)
	}
}

func (s *Service) updateActiveStackStatuses(binding StackBinding, state, message, commit string, success bool) {
	paths := s.activeAutomationComposePaths(binding)
	now := time.Now().UTC()
	updates := map[string]any{"state": state, "error_message": message, "last_checked_at": &now}
	if state != stackSyncConflict {
		updates["conflict_count"] = 0
	}
	if success {
		updates["last_success_at"] = &now
		updates["last_commit"] = commit
	}
	_ = s.store.UpdateGitStackStatuses(binding.UUID, paths, updates)
}

func (s *Service) updateActiveStackStatusesPreservingLocal(binding StackBinding, state, message, commit string, success bool, preservedStates ...string) {
	paths := s.activeAutomationComposePaths(binding)
	now := time.Now().UTC()
	updates := map[string]any{"state": state, "error_message": message, "last_checked_at": &now}
	if state != stackSyncConflict {
		updates["conflict_count"] = 0
	}
	if success {
		updates["last_success_at"] = &now
		updates["last_commit"] = commit
	}
	// stackSyncConflict is preserved for the same reason as the local states: a
	// pending conflict is a decision the operator still owes. Overwriting it
	// with up_to_date on a cycle that never scanned shows green while two
	// versions genuinely disagree, and the next import would overwrite blind.
	excludedStates := append([]string{stackSyncLocalChanges, stackSyncLocalDeleted, stackSyncOrphaned, stackSyncConflict}, preservedStates...)
	_ = s.store.UpdateGitStackStatusesExcept(binding.UUID, paths, excludedStates, updates)
}

func (s *Service) activeAutomationComposePaths(binding StackBinding) []string {
	selected := autoSyncComposePaths(binding)
	paused, err := s.store.PausedComposePaths(binding.UUID)
	if err != nil || len(paused) == 0 {
		return selected
	}
	pausedSet := make(map[string]struct{}, len(paused))
	for _, path := range paused {
		pausedSet[path] = struct{}{}
	}
	active := make([]string, 0, len(selected))
	for _, path := range selected {
		if _, skip := pausedSet[path]; !skip {
			active = append(active, path)
		}
	}
	return active
}

func (s *Service) bindingHasActiveStackState(binding StackBinding, state string) bool {
	active := make(map[string]struct{})
	for _, path := range s.activeAutomationComposePaths(binding) {
		active[path] = struct{}{}
	}
	rows, err := s.store.GitStackStatuses(binding.UUID)
	if err != nil {
		return false
	}
	for _, row := range rows {
		if row.State == state {
			if _, selected := active[row.ComposePath]; selected {
				return true
			}
		}
	}
	return false
}

func composePathsForFile(composePaths []string, filePath string) []string {
	filePath = strings.Trim(filepath.ToSlash(filePath), "/")
	bestDepth := -1
	result := make([]string, 0, 1)
	for _, composePath := range composePaths {
		root := strings.Trim(filepath.ToSlash(filepath.Dir(filepath.FromSlash(composePath))), "/")
		if root == "." {
			root = ""
		}
		if root == "" || filePath == root || strings.HasPrefix(filePath, root+"/") {
			depth := strings.Count(root, "/")
			if root != "" {
				depth++
			}
			if depth > bestDepth {
				bestDepth = depth
				result = result[:0]
			}
			if depth == bestDepth {
				result = append(result, composePath)
			}
		}
	}
	return result
}

func stringInSlice(value string, values []string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// MarkLocalChange is called after successful user-driven file mutations. Most
// changes only require path comparisons and compact DB updates. Compose-file
// and directory mutations trigger one bounded catalog refresh so folder
// creation, copy, rename, and deletion cannot leave stale stack choices.
func (s *Service) MarkLocalChange(host, changedPath string) {
	if !s.enabled {
		return
	}
	changedPath = strings.Trim(filepath.ToSlash(changedPath), "/")
	bindings, err := s.store.ListBindingsForHost(host)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, binding := range bindings {
		prefix := strings.Trim(filepath.ToSlash(binding.StackPath), "/")
		if changedPath != prefix && !strings.HasPrefix(changedPath, prefix+"/") {
			continue
		}
		relative := strings.TrimPrefix(strings.TrimPrefix(changedPath, prefix), "/")
		if composeCatalogMayHaveChanged(s, binding, relative) {
			fresh, changed := s.reconcileLocalComposeCatalog(binding.UUID)
			if changed {
				binding = fresh
			}
		}
		if !s.bindingTracksLocalMutation(binding, relative) {
			continue
		}
		for _, composePath := range composePathsForFile(selectedComposePaths(binding), relative) {
			state := stackSyncLocalChanges
			if !bindingComposeExistsLocally(s, binding, composePath) {
				state = stackSyncLocalDeleted
			}
			_ = s.store.UpdateGitStackStatuses(binding.UUID, []string{composePath}, map[string]any{
				"state": state, "error_message": "", "conflict_count": 0, "last_checked_at": &now,
			})
		}
	}
}

// bindingTracksLocalMutation applies the same path policy as the transfer
// inventory before publishing an editor mutation as a Git change. In
// particular, Compose-only links must stay up to date when mutable application
// data outside their allow-list is edited. Directory operations remain
// conservative because a copy, rename, or deletion can affect several tracked
// files at once and already triggers the bounded Compose catalog refresh above.
func (s *Service) bindingTracksLocalMutation(binding StackBinding, relative string) bool {
	relative = strings.Trim(filepath.ToSlash(relative), "/")
	targetFS, targetRoot, err := s.resolveBindingStack(binding)
	if err != nil {
		return true
	}
	info, statErr := targetFS.Stat(targetFS.Join(targetRoot, filepath.FromSlash(relative)))
	if statErr == nil && info.IsDir() {
		return true
	}
	if isProvisionControlPath(relative) {
		// Provision manifests are Git-side controls and are never exported from
		// the live stack directory by collectStackFiles.
		return false
	}
	repository, err := s.store.GetRepository(binding.RepositoryUUID)
	if err != nil {
		return true
	}
	policy, err := policyFromBinding(binding, repository)
	if err != nil {
		return true
	}
	policy = policy.withComposeDirectoryIndex()
	ignoreRules, err := loadStackIgnoreRules(targetFS, targetRoot)
	if err != nil {
		// Keep the mutation visible if the policy itself cannot be read. The
		// subsequent preview will expose that actionable configuration error.
		return true
	}
	return policyTracksLocalFile(policy, ignoreRules, relative)
}

func policyTracksLocalFile(policy syncPolicy, ignoreRules []ignoreRule, relative string) bool {
	if isProvisionControlPath(relative) {
		return false
	}
	// Real environment files, private keys and other sensitive paths require a
	// separate one-time confirmation during an explicit transfer. They are not
	// part of normal synchronization, even if a broad include rule matches
	// them, so they must not produce a passive cloud badge or a pushable-change
	// status. Safe .env.example/.sample/.template/.dist files are deliberately
	// not classified as sensitive and continue through the regular policy.
	if isSensitivePath(relative) {
		return false
	}
	selected, _ := policy.selectsPath(relative, false)
	if !selected || shouldSkipPath(relative, false) {
		return false
	}
	if policy.exclusionApplies(relative, false, ignoreRules) &&
		!policy.protectsCompose(relative) {
		return false
	}
	return policy.includesFile(relative)
}

func bindingComposeExistsLocally(s *Service, binding StackBinding, composePath string) bool {
	targetFS, targetRoot, err := s.resolveBindingStack(binding)
	if err != nil {
		return false
	}
	info, err := targetFS.Stat(targetFS.Join(targetRoot, filepath.FromSlash(composePath)))
	return err == nil && info.Mode().IsRegular()
}

func isComposeCatalogPath(path string) bool {
	switch strings.ToLower(filepath.Base(filepath.FromSlash(path))) {
	case "compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml":
		return true
	default:
		return false
	}
}

func composeCatalogMayHaveChanged(s *Service, binding StackBinding, composePath string) bool {
	known := splitPatternLines(binding.ComposePaths)
	if isComposeCatalogPath(composePath) {
		if !stringInSlice(composePath, known) {
			return true
		}
		targetFS, targetRoot, err := s.resolveBindingStack(binding)
		if err != nil {
			return false
		}
		info, err := targetFS.Stat(targetFS.Join(targetRoot, filepath.FromSlash(composePath)))
		return err != nil || !info.Mode().IsRegular()
	}

	// Deleting or renaming a stack directory reports the directory itself,
	// not every Compose file it used to contain.
	prefix := strings.Trim(composePath, "/")
	for _, knownCompose := range known {
		if prefix == "" || strings.HasPrefix(knownCompose, prefix+"/") {
			return true
		}
	}

	// Copying or renaming a directory into a linked root can introduce one or
	// more Compose files at once. Stat only this user-mutated path; no periodic
	// filesystem scan is added.
	targetFS, targetRoot, err := s.resolveBindingStack(binding)
	if err != nil {
		return false
	}
	info, err := targetFS.Stat(targetFS.Join(targetRoot, filepath.FromSlash(composePath)))
	return err == nil && info.IsDir()
}

func (s *Service) reconcileLocalComposeCatalog(bindingID string) (StackBinding, bool) {
	automationLock := s.repositoryLock("automation:" + bindingID)
	automationLock.Lock()
	defer automationLock.Unlock()
	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return binding, false
	}
	repositoryLock := s.repositoryLock(binding.RepositoryUUID)
	repositoryLock.Lock()
	defer repositoryLock.Unlock()
	before := binding.ComposePaths + "\x00" + binding.SelectedComposePaths + "\x00" + binding.ComposeSelectionMode
	binding, _, err = s.refreshBindingComposeCatalogLocked(binding)
	if err != nil {
		return binding, false
	}
	after := binding.ComposePaths + "\x00" + binding.SelectedComposePaths + "\x00" + binding.ComposeSelectionMode
	return binding, before != after
}
