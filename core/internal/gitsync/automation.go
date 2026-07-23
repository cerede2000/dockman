package gitsync

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	defaultAutoSyncIntervalMinutes = 15
	minAutoSyncIntervalMinutes     = 5
	maxAutoSyncIntervalMinutes     = 24 * 60
	autoSyncSchedulerMaxSleep      = 30 * time.Second
	autoSyncSchedulerMinSleep      = time.Second
)

type BindingAutomationInput struct {
	Enabled            bool     `json:"enabled"`
	AutoReconcile      *bool    `json:"autoReconcile"`
	IntervalMinutes    int      `json:"intervalMinutes"`
	DeployEnabled      bool     `json:"deployEnabled"`
	DeployNewStacks    bool     `json:"deployNewStacks"`
	DeployComposePaths []string `json:"deployComposePaths"`
}

type AutoSyncResult struct {
	BindingID string   `json:"bindingId"`
	State     string   `json:"state"`
	Changed   int      `json:"changed"`
	Conflicts int      `json:"conflicts"`
	Preserved int      `json:"preserved"`
	Backup    string   `json:"backup,omitempty"`
	Deployed  []string `json:"deployed,omitempty"`
	Message   string   `json:"message"`
}

func (s *Service) UpdateBindingAutomation(id string, input BindingAutomationInput) (BindingView, error) {
	automationLock := s.repositoryLock("automation:" + id)
	automationLock.Lock()
	defer automationLock.Unlock()
	row, err := s.store.GetBinding(id)
	if err != nil {
		return BindingView{}, err
	}
	if input.IntervalMinutes == 0 {
		input.IntervalMinutes = defaultAutoSyncIntervalMinutes
	}
	if !input.Enabled {
		input.DeployEnabled = false
		input.DeployComposePaths = nil
	}
	if !input.DeployEnabled {
		input.DeployNewStacks = false
	}
	if input.IntervalMinutes < minAutoSyncIntervalMinutes || input.IntervalMinutes > maxAutoSyncIntervalMinutes {
		return BindingView{}, fmt.Errorf("automatic synchronization interval must be between %d and %d minutes", minAutoSyncIntervalMinutes, maxAutoSyncIntervalMinutes)
	}
	row.AutoSyncEnabled = input.Enabled
	if input.AutoReconcile != nil {
		row.AutoReconcileEnabled = *input.AutoReconcile
	}
	row.AutoSyncIntervalMinutes = input.IntervalMinutes
	row.AutoSyncError = ""
	if input.Enabled {
		row.AutoSyncState = "watching"
	} else {
		row.AutoSyncState = "disabled"
	}
	deployPaths, err := validateDeploymentTargets(row, input.DeployEnabled, input.DeployNewStacks, input.DeployComposePaths)
	if err != nil {
		return BindingView{}, err
	}
	row.AutoDeployEnabled = input.DeployEnabled
	row.AutoDeployNewStacks = input.DeployNewStacks
	row.AutoDeployComposePaths = strings.Join(deployPaths, "\n")
	row.AutoDeployError = ""
	if input.DeployEnabled {
		row.AutoDeployState = "watching"
	} else {
		row.AutoDeployState = "disabled"
	}
	if err := s.store.SaveBinding(&row); err != nil {
		return BindingView{}, err
	}
	return s.bindingView(row)
}

// StartAutomation starts the low-frequency Git watcher. It only processes
// links that were explicitly enabled and stops with Dockman's server context.
func (s *Service) StartAutomation(ctx context.Context) {
	if !s.enabled {
		return
	}
	go func() {
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.runDueAutoSyncs(ctx, time.Now().UTC())
				timer.Reset(s.nextAutoSyncDelay(time.Now().UTC()))
			}
		}
	}()
}

func (s *Service) nextAutoSyncDelay(now time.Time) time.Duration {
	bindings, err := s.store.ListAutoSyncBindings()
	if err != nil || len(bindings) == 0 {
		return autoSyncSchedulerMaxSleep
	}
	delay := autoSyncSchedulerMaxSleep
	for _, binding := range bindings {
		if binding.LastAutoSyncAt == nil {
			return autoSyncSchedulerMinSleep
		}
		interval := normalizedAutoSyncInterval(binding.AutoSyncIntervalMinutes)
		candidate := binding.LastAutoSyncAt.Add(time.Duration(interval) * time.Minute).Sub(now)
		if candidate <= autoSyncSchedulerMinSleep {
			return autoSyncSchedulerMinSleep
		}
		if candidate < delay {
			delay = candidate
		}
	}
	return delay
}

func normalizedAutoSyncInterval(interval int) int {
	if interval < minAutoSyncIntervalMinutes || interval > maxAutoSyncIntervalMinutes {
		return defaultAutoSyncIntervalMinutes
	}
	return interval
}

func (s *Service) runDueAutoSyncs(ctx context.Context, now time.Time) {
	bindings, err := s.store.ListAutoSyncBindings()
	if err != nil {
		log.Error().Err(err).Msg("unable to list automatic Git synchronizations")
		return
	}
	for _, binding := range bindings {
		if ctx.Err() != nil {
			return
		}
		interval := normalizedAutoSyncInterval(binding.AutoSyncIntervalMinutes)
		if binding.LastAutoSyncAt != nil && now.Before(binding.LastAutoSyncAt.Add(time.Duration(interval)*time.Minute)) {
			continue
		}
		if _, err := s.RunBindingAutoSync(ctx, binding.UUID); err != nil {
			log.Warn().Err(err).Str("binding", binding.UUID).Msg("automatic Git synchronization failed")
		}
	}
}

func (s *Service) RunBindingAutoSync(ctx context.Context, id string) (AutoSyncResult, error) {
	releaseMemory := observeGitMemory("automatic Git to Dockman synchronization")
	defer releaseMemory()
	binding, err := s.store.GetBinding(id)
	if err != nil {
		return AutoSyncResult{}, err
	}
	if !binding.AutoSyncEnabled {
		return AutoSyncResult{}, errors.New("automatic synchronization is disabled for this folder link")
	}
	if err := s.reconcileGitStackStatuses(binding); err != nil {
		return AutoSyncResult{}, err
	}
	automationLock := s.repositoryLock("automation:" + id)
	if !automationLock.TryLock() {
		return AutoSyncResult{}, errors.New("automatic synchronization is already running for this folder link")
	}
	defer automationLock.Unlock()

	attemptedAt := time.Now().UTC()
	if len(s.activeAutomationComposePaths(binding)) == 0 && !(binding.AutoDeployEnabled && binding.AutoDeployNewStacks) {
		message := "All selected stacks are paused; Git synchronization was skipped"
		_ = s.store.UpdateBindingAutoSyncState(id, "watching", message, "", &attemptedAt, nil)
		return AutoSyncResult{BindingID: id, State: "paused", Message: message}, nil
	}
	_ = s.store.UpdateBindingAutoSyncState(id, "syncing", "", "", &attemptedAt, nil)
	// A file save already gives us authoritative local-dirty state. Preserve it
	// while checking Git so an unchanged remote commit cannot paint it green.
	s.updateActiveStackStatusesPreservingLocal(binding, stackSyncChecking, "", "", false)
	result := AutoSyncResult{BindingID: id, State: "syncing"}
	var synchronizedCommit string
	skippedStackScan := false
	localDeletionBlock := false
	err = s.runBindingOperation(ctx, binding.RepositoryUUID, binding.UUID, "auto_sync", func(ctx context.Context) error {
		status, fetchErr := s.FetchRepository(ctx, binding.RepositoryUUID)
		if fetchErr != nil {
			return fetchErr
		}
		if !status.Clean || status.Diverged || status.Ahead > 0 {
			result.State = "blocked"
			result.Message = "Repository state requires a manual decision before automatic synchronization"
			return nil
		}
		if status.Behind > 0 {
			pulledStatus, pullErr := s.PullRepository(ctx, binding.RepositoryUUID)
			if pullErr != nil {
				return pullErr
			}
			status = pulledStatus
		}
		synchronizedCommit = status.Head
		if binding.AutoReconcileEnabled {
			baseline, baselineErr := s.store.BindingBaseline(binding.UUID)
			if baselineErr != nil {
				return baselineErr
			}
			if len(baseline) == 0 {
				reconciled, reconcileErr := s.reconcileBindingIfIdentical(binding.UUID)
				if reconcileErr != nil {
					return reconcileErr
				}
				if reconciled {
					result.State = "up_to_date"
					result.Message = "Dockman and Git were identical; synchronization baseline established automatically"
					return nil
				}
			}
		}
		if binding.LastAutoSyncCommit != "" && binding.LastAutoSyncCommit == status.Head {
			skippedStackScan = true
			if s.bindingHasActiveStackState(binding, stackSyncLocalDeleted) {
				localDeletionBlock = true
				result.State = "blocked"
				result.Message = "A synchronized stack was deleted locally; choose restore from Git, delete from Git, or stop synchronizing it"
			} else if binding.AutoSyncState == "blocked" && strings.Contains(binding.AutoSyncError, "Git deletion") {
				result.State = "blocked"
				result.Message = preservedDeletionMessage(binding.AutoSyncError) + "; no new Git commit, stack scan skipped"
			} else {
				result.State = "up_to_date"
				result.Message = "No new Git commit; stack scan skipped"
			}
			return nil
		}

		preview, previewErr := s.PreviewBinding(id, "repository_to_stack", TransferInput{automation: true})
		if previewErr != nil {
			return previewErr
		}
		changed, conflicts, preserved, localDeletions, previewToken := preview.Changed, preview.Conflicts, preview.Preserved, preview.LocalDeletions, preview.PreviewToken
		result.Changed, result.Conflicts, result.Preserved = changed, conflicts, preserved
		// A folder can contain many thousands of entries. Do not retain the first
		// inventory while ImportBinding builds and validates its fresh inventory.
		changedPaths := changedPreviewPaths(preview)
		newTargets, newTargetErr := newComposeDeploymentTargets(binding, preview)
		if newTargetErr != nil {
			return newTargetErr
		}
		preview.Entries = nil
		if conflicts > 0 {
			result.State = "conflict"
			result.Message = fmt.Sprintf("%d conflict(s) require a manual decision; no file was changed", conflicts)
			return nil
		}
		if localDeletions > 0 {
			localDeletionBlock = true
			result.State = "blocked"
			result.Message = fmt.Sprintf("%d locally deleted synchronized file(s) require an explicit stack decision; no file was restored", localDeletions)
			return nil
		}
		if len(newTargets) > 0 {
			var registerErr error
			binding, registerErr = s.registerDiscoveredDeploymentTargets(binding, newTargets)
			if registerErr != nil {
				return registerErr
			}
		}
		if len(newTargets) == 0 && len(s.activeAutomationComposePaths(binding)) == 0 {
			result.State = "paused"
			result.Message = "All selected stacks are paused; no new Git stack was discovered"
			return nil
		}

		transfer, importErr := s.ImportBinding(ctx, id, TransferInput{PreviewToken: previewToken, compactResult: true, automation: true})
		if importErr != nil {
			return importErr
		}
		result.State = "up_to_date"
		result.Backup = transfer.Backup
		if len(transfer.EditorBlocked) > 0 {
			changedPaths = excludeComposeStackPaths(changedPaths, transfer.EditorBlocked)
			result.State = "blocked"
			result.Message = fmt.Sprintf("%d stack(s) kept unchanged while edited in Dockman; other safe changes were synchronized", len(transfer.EditorBlocked))
		}
		if preserved > 0 && len(transfer.EditorBlocked) == 0 {
			result.State = "blocked"
			result.Message = fmt.Sprintf("%d Git deletion(s) preserved locally; choose restore, archive, or explicit local deletion", preserved)
		} else if changed == 0 {
			result.Message = "Stack already matches Git"
		} else if len(transfer.EditorBlocked) == 0 {
			result.Message = fmt.Sprintf("%d file(s) synchronized from Git with backup; stack was not deployed", changed)
		}
		if changed == 0 && binding.AutoDeployEnabled && (binding.AutoDeployState == "failed" || binding.AutoDeployState == "pending") {
			changedPaths = append(changedPaths, splitPatternLines(binding.AutoDeployComposePaths)...)
		}
		if len(changedPaths) > 0 && binding.AutoDeployEnabled {
			deployed, deployErr := s.deployChangedStacks(ctx, binding, synchronizedCommit, changedPaths)
			if deployErr != nil {
				return deployErr
			}
			result.Deployed = deployed
			if len(deployed) > 0 {
				result.Message = fmt.Sprintf("%d file(s) synchronized and %d stack(s) deployed", changed, len(deployed))
			}
		}
		return nil
	})
	if err != nil {
		message := safeGitError(err)
		_ = s.store.UpdateBindingAutoSyncState(id, "error", message, "", &attemptedAt, nil)
		s.updateActiveStackStatuses(binding, stackSyncError, message, "", false)
		return AutoSyncResult{BindingID: id, State: "error", Message: message}, err
	}
	if result.State == "conflict" || result.State == "blocked" {
		preservedBlock := result.Preserved > 0 || (skippedStackScan && binding.AutoSyncState == "blocked" && strings.Contains(binding.AutoSyncError, "Git deletion"))
		commit := ""
		if preservedBlock {
			// The commit was inspected successfully. Remember it so subsequent
			// intervals stay fetch-only until Git changes again, while the compact
			// per-stack orphan status remains visible.
			commit = synchronizedCommit
		}
		_ = s.store.UpdateBindingAutoSyncState(id, result.State, result.Message, commit, &attemptedAt, nil)
		if result.State == "blocked" {
			if !preservedBlock && !localDeletionBlock {
				s.updateActiveStackStatuses(binding, stackSyncRemoteChanges, result.Message, "", false)
			}
		}
		return result, nil
	}
	if result.State == "paused" {
		_ = s.store.UpdateBindingAutoSyncState(id, "watching", result.Message, "", &attemptedAt, nil)
		return result, nil
	}
	succeededAt := time.Now().UTC()
	_ = s.store.UpdateBindingAutoSyncState(id, "up_to_date", "", synchronizedCommit, &attemptedAt, &succeededAt)
	if skippedStackScan {
		s.updateActiveStackStatusesPreservingLocal(binding, stackSyncUpToDate, "", synchronizedCommit, true)
	} else {
		s.updateActiveStackStatuses(binding, stackSyncUpToDate, "", synchronizedCommit, true)
	}
	return result, nil
}

func preservedDeletionMessage(message string) string {
	const suffix = "; no new Git commit, stack scan skipped"
	message = strings.TrimSpace(message)
	for strings.HasSuffix(message, suffix) {
		message = strings.TrimSpace(strings.TrimSuffix(message, suffix))
	}
	return message
}

func excludeComposeStackPaths(paths, blockedCompose []string) []string {
	blockedDirs := make([]string, 0, len(blockedCompose))
	for _, composePath := range blockedCompose {
		dir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(composePath)))
		if dir == "." {
			dir = ""
		}
		blockedDirs = append(blockedDirs, dir)
	}
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		blocked := false
		for _, dir := range blockedDirs {
			if (dir == "" && !strings.Contains(path, "/")) || (dir != "" && (path == dir || strings.HasPrefix(path, dir+"/"))) {
				blocked = true
				break
			}
		}
		if !blocked {
			result = append(result, path)
		}
	}
	return result
}
