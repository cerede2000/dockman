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
	Enabled               bool     `json:"enabled"`
	AutoReconcile         *bool    `json:"autoReconcile"`
	IntervalMinutes       int      `json:"intervalMinutes"`
	AutoSyncSelectionMode string   `json:"autoSyncSelectionMode"`
	AutoSyncComposePaths  []string `json:"autoSyncComposePaths"`
	DeployEnabled         bool     `json:"deployEnabled"`
	DeployNewStacks       bool     `json:"deployNewStacks"`
	DeployRollback        bool     `json:"deployRollback"`
	DeployComposePaths    []string `json:"deployComposePaths"`
}

type BindingAutomationPauseInput struct {
	Paused bool `json:"paused"`
}

type BindingAutomationPauseResult struct {
	Binding BindingView     `json:"binding"`
	Sync    *AutoSyncResult `json:"sync,omitempty"`
}

type AutoSyncResult struct {
	BindingID      string   `json:"bindingId"`
	State          string   `json:"state"`
	Changed        int      `json:"changed"`
	Conflicts      int      `json:"conflicts"`
	Preserved      int      `json:"preserved"`
	Backup         string   `json:"backup,omitempty"`
	Deployed       []string `json:"deployed,omitempty"`
	DeployFailed   []string `json:"deployFailed,omitempty"`
	RolledBack     []string `json:"rolledBack,omitempty"`
	RollbackFailed []string `json:"rollbackFailed,omitempty"`
	SyncFailed     []string `json:"syncFailed,omitempty"`
	Discovered     []string `json:"discovered,omitempty"`
	HeldBack       []string `json:"heldBack,omitempty"`
	AutoExcluded   []string `json:"autoExcluded,omitempty"`
	Message        string   `json:"message"`
}

func (s *Service) UpdateBindingAutomation(id string, input BindingAutomationInput) (BindingView, error) {
	automationLock := s.repositoryLock("automation:" + id)
	automationLock.Lock()
	defer automationLock.Unlock()
	row, err := s.store.GetBinding(id)
	if err != nil {
		return BindingView{}, err
	}
	previousAutoTargets := strings.Join(autoSyncComposePaths(row), "\n")
	if input.IntervalMinutes == 0 {
		input.IntervalMinutes = defaultAutoSyncIntervalMinutes
	}
	if !input.Enabled {
		input.DeployEnabled = false
		input.DeployRollback = false
		input.DeployComposePaths = nil
	}
	if !input.DeployEnabled {
		input.DeployNewStacks = false
		input.DeployRollback = false
	}
	if input.IntervalMinutes < minAutoSyncIntervalMinutes || input.IntervalMinutes > maxAutoSyncIntervalMinutes {
		return BindingView{}, fmt.Errorf("automatic synchronization interval must be between %d and %d minutes", minAutoSyncIntervalMinutes, maxAutoSyncIntervalMinutes)
	}
	wasAutoSyncEnabled := row.AutoSyncEnabled
	wasAutoDeployEnabled := row.AutoDeployEnabled
	row.AutoSyncEnabled = input.Enabled
	if !input.Enabled {
		row.AutoSyncPaused = false
	}
	if input.AutoReconcile != nil {
		row.AutoReconcileEnabled = *input.AutoReconcile
	}
	row.AutoSyncIntervalMinutes = input.IntervalMinutes
	if strings.TrimSpace(input.AutoSyncSelectionMode) != "" {
		mode, paths, selectionErr := normalizeComposeSelection(selectedComposePaths(row), input.AutoSyncSelectionMode, input.AutoSyncComposePaths, composeSelectionAll)
		if selectionErr != nil {
			return BindingView{}, fmt.Errorf("invalid automatic synchronization targets: %w", selectionErr)
		}
		row.AutoSyncSelectionMode = mode
		row.AutoSyncComposePaths = strings.Join(paths, "\n")
	}
	if strings.Join(autoSyncComposePaths(row), "\n") != previousAutoTargets {
		// The repository HEAD may already have been observed while this stack was
		// manual-only. Invalidate only the cheap commit shortcut so the newly
		// enabled target is inspected on the next regular cycle.
		row.LastAutoSyncCommit = ""
	}
	if input.Enabled {
		if !wasAutoSyncEnabled || row.AutoSyncState == "" || row.AutoSyncState == "disabled" {
			row.AutoSyncState = "watching"
			row.AutoSyncError = ""
		}
	} else {
		row.AutoSyncState = "disabled"
		row.AutoSyncError = ""
	}
	deployPaths, err := validateDeploymentTargets(row, input.DeployEnabled, input.DeployNewStacks, input.DeployComposePaths)
	if err != nil {
		return BindingView{}, err
	}
	autoTargets := make(map[string]struct{})
	for _, path := range autoSyncComposePaths(row) {
		autoTargets[path] = struct{}{}
	}
	for _, path := range deployPaths {
		if _, enabled := autoTargets[path]; !enabled {
			return BindingView{}, fmt.Errorf("automatic deployment target is excluded from automatic synchronization: %s", path)
		}
	}
	row.AutoDeployEnabled = input.DeployEnabled
	row.AutoDeployNewStacks = input.DeployNewStacks
	row.AutoDeployRollbackEnabled = input.DeployRollback
	row.AutoDeployComposePaths = strings.Join(deployPaths, "\n")
	if input.DeployEnabled {
		if !wasAutoDeployEnabled || row.AutoDeployState == "" || row.AutoDeployState == "disabled" {
			row.AutoDeployState = "watching"
			row.AutoDeployError = ""
		}
	} else {
		row.AutoDeployState = "disabled"
		row.AutoDeployError = ""
	}
	if err := s.store.SaveBinding(&row); err != nil {
		return BindingView{}, err
	}
	return s.bindingView(row)
}

// SetBindingAutomationPause suspends only the scheduler for a folder link.
// Resuming first performs the regular, complete synchronization while the
// scheduler still ignores the link, then reenables subsequent scheduled runs.
func (s *Service) SetBindingAutomationPause(ctx context.Context, id string, paused bool) (BindingAutomationPauseResult, error) {
	row, err := s.store.GetBinding(id)
	if err != nil {
		return BindingAutomationPauseResult{}, err
	}
	if !row.AutoSyncEnabled {
		return BindingAutomationPauseResult{}, errors.New("automatic synchronization is disabled for this folder link")
	}
	if row.AutoSyncPaused == paused {
		view, viewErr := s.bindingView(row)
		return BindingAutomationPauseResult{Binding: view}, viewErr
	}
	if paused {
		automationLock := s.repositoryLock("automation:" + id)
		if !automationLock.TryLock() {
			return BindingAutomationPauseResult{}, errors.New("automatic synchronization is currently running; retry when it finishes")
		}
		defer automationLock.Unlock()
		row, err = s.store.GetBinding(id)
		if err != nil {
			return BindingAutomationPauseResult{}, err
		}
		if !row.AutoSyncEnabled {
			return BindingAutomationPauseResult{}, errors.New("automatic synchronization is disabled for this folder link")
		}
		row.AutoSyncPaused = true
		if err := s.store.SaveBinding(&row); err != nil {
			return BindingAutomationPauseResult{}, err
		}
		view, err := s.bindingView(row)
		return BindingAutomationPauseResult{Binding: view}, err
	}

	// Keep AutoSyncPaused set during the immediate run. This prevents the
	// scheduler from racing the explicit resume check.
	syncResult, syncErr := s.RunBindingAutoSyncNow(ctx, id)
	automationLock := s.repositoryLock("automation:" + id)
	automationLock.Lock()
	defer automationLock.Unlock()
	row, reloadErr := s.store.GetBinding(id)
	if reloadErr != nil {
		return BindingAutomationPauseResult{}, reloadErr
	}
	row.AutoSyncPaused = false
	if err := s.store.SaveBinding(&row); err != nil {
		return BindingAutomationPauseResult{}, err
	}
	view, viewErr := s.bindingView(row)
	result := BindingAutomationPauseResult{Binding: view, Sync: &syncResult}
	if viewErr != nil {
		return BindingAutomationPauseResult{}, viewErr
	}
	if syncErr != nil {
		return result, fmt.Errorf("automatic synchronization resumed, but its immediate check failed: %w", syncErr)
	}
	return result, nil
}

// StartAutomation starts the low-frequency Git watcher. It only processes
// links that were explicitly enabled and stops with Dockman's server context.
func (s *Service) StartAutomation(ctx context.Context) {
	if !s.enabled {
		return
	}
	go func() {
		s.cleanupStaleProvisionStaging(time.Now())
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
	previous, _ := s.store.GetBinding(id)
	result, err := s.runBindingAutoSync(ctx, id, false)
	s.notifyAutomationResult(id, result, err, false, &previous)
	return result, err
}

// RunBindingAutoSyncNow is an explicit user-triggered synchronization. Unlike
// background polling, it always rebuilds the selected stack inventories and
// may retry a failed or rolled-back deployment on the same Git commit. The
// commit-only shortcut remains reserved for idle background polling.
func (s *Service) RunBindingAutoSyncNow(ctx context.Context, id string) (AutoSyncResult, error) {
	previous, _ := s.store.GetBinding(id)
	result, err := s.runBindingAutoSync(ctx, id, true)
	s.notifyAutomationResult(id, result, err, true, &previous)
	return result, err
}

func (s *Service) runBindingAutoSync(ctx context.Context, id string, explicit bool) (AutoSyncResult, error) {
	releaseMemory := observeGitMemory("automatic Git to Dockman synchronization")
	defer releaseMemory()
	automationLock := s.repositoryLock("automation:" + id)
	if !automationLock.TryLock() {
		return AutoSyncResult{}, errors.New("automatic synchronization is already running for this folder link")
	}
	defer automationLock.Unlock()

	// Read the binding only after serializing with policy/selection updates so
	// a stack disabled concurrently cannot be synchronized one final time from
	// a stale snapshot.
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

	attemptedAt := time.Now().UTC()
	if len(s.activeAutomationComposePaths(binding)) == 0 && !(binding.AutoDeployEnabled && binding.AutoDeployNewStacks) {
		message := "All selected stacks are paused or excluded from automatic synchronization; Git synchronization was skipped"
		_ = s.store.UpdateBindingAutoSyncState(id, "watching", message, "", &attemptedAt, nil)
		return AutoSyncResult{BindingID: id, State: "paused", Message: message}, nil
	}
	_ = s.store.UpdateBindingAutoSyncState(id, "syncing", "", "", &attemptedAt, nil)
	// "syncing" is a transient binding-level state. Do not persist "checking"
	// into every stack row: if a repository fetch fails, the last authoritative
	// per-stack state must survive instead of turning unrelated stacks red.
	// ListGitStackStatusViews projects the spinner from this binding state.
	result := AutoSyncResult{BindingID: id, State: "syncing"}
	var synchronizedCommit string
	skippedStackScan := false
	localDeletionBlock := false
	// Set when the block belongs to identified stacks rather than to the link.
	attributedBlock := false
	err = s.runBindingOperation(ctx, binding.RepositoryUUID, binding.UUID, "auto_sync", func(ctx context.Context) error {
		status, fetchErr := s.FetchRepository(ctx, binding.RepositoryUUID)
		if fetchErr != nil {
			return fetchErr
		}
		if status.Diverged || status.Ahead > 0 {
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
		// A red synchronization state must be re-evaluated even when Git did not
		// move. The underlying local permission, transient read failure or edited
		// file may have been fixed since the previous cycle. Healthy bindings keep
		// the cheap commit-only fast path, so there is no idle CPU regression.
		recheckSynchronizationError := s.bindingHasActiveStackState(binding, stackSyncError)
		// Deliberately NOT re-armed for a failed deployment: background polling
		// must not loop `docker compose up` on a stack that is broken. An
		// explicit "Check now" is what retries, once the operator has fixed
		// whatever broke - see the retry block further down.
		if binding.LastAutoSyncCommit != "" && binding.LastAutoSyncCommit == status.Head && !explicit && !recheckSynchronizationError {
			skippedStackScan = true
			if s.bindingHasActiveStackState(binding, stackSyncLocalDeleted) {
				localDeletionBlock = true
				result.State = "blocked"
				result.Message = "A synchronized stack was deleted locally; choose restore from Git, delete from Git, or stop synchronizing it"
			} else if binding.AutoSyncState == "blocked" && strings.Contains(binding.AutoSyncError, "Git deletion") {
				result.State = "blocked"
				result.Message = preservedDeletionMessage(binding.AutoSyncError) + "; no new Git commit, stack scan skipped"
			} else if binding.AutoSyncState == "partial" {
				result.State = "partial"
				result.Message = autoSyncMessageWithoutSkipSuffix(binding.AutoSyncError) + "; no new Git commit, stack scan skipped"
			} else {
				result.State = "up_to_date"
				result.Message = "No new Git commit; stack scan skipped"
			}
			return nil
		}

		// A corrected ACL is all it takes to bring a held-out path back. Free
		// while nothing is held out, which is the normal case.
		binding = s.refreshUnreadableExclusions(binding)

		preview, previewErr := s.PreviewBinding(id, "repository_to_stack", TransferInput{automation: true})
		if previewErr != nil {
			return previewErr
		}
		changed, conflicts, preserved, localDeletions, previewToken := preview.Changed, preview.Conflicts, preview.Preserved, preview.LocalDeletions, preview.PreviewToken
		result.Changed, result.Conflicts, result.Preserved = changed, conflicts, preserved
		// A folder can contain many thousands of entries. Do not retain the first
		// inventory while ImportBinding builds and validates its fresh inventory.
		changedPaths := changedPreviewPaths(preview)
		// Computed here, while the preview still holds its entries: they are
		// released a few lines below to keep one inventory in memory at a time.
		activeAutomationPaths := s.activeAutomationComposePaths(binding)
		heldBackStacks := heldBackComposeStacks(preview, activeAutomationPaths)
		// Reported once, then held out of both inventories so the stack stops
		// being red for a file the host will never let Dockman read.
		if updated, recorded := s.recordUnreadablePaths(binding, previewPathsWithStatus(preview, "skipped_permission")); recorded {
			binding = updated
			result.AutoExcluded = splitPatternLines(binding.AutoExcludedPaths)
		}
		newTargets, newTargetErr := newComposeDeploymentTargets(binding, preview, activeAutomationPaths)
		if newTargetErr != nil {
			return newTargetErr
		}
		if len(preview.ComposeErrors) > 0 {
			invalidComposePaths := make([]string, 0, len(preview.ComposeErrors))
			for composePath := range preview.ComposeErrors {
				invalidComposePaths = append(invalidComposePaths, composePath)
			}
			newTargets = excludeStringValues(newTargets, invalidComposePaths)
		}
		preview.Entries = nil
		// A conflict or a local deletion belongs to the stack that owns the file,
		// not to the Folder Link. Hold that stack exactly as it is and let every
		// other stack of the link import normally: before this, one deleted file
		// in one stack froze the whole link, and the untouched stacks kept
		// reporting remote changes that could never arrive.
		if conflicts > 0 || localDeletions > 0 {
			remaining := excludeStringValues(activeAutomationPaths, heldBackStacks)
			// Fail closed: if the decision cannot be attributed to a stack, or if
			// no stack is left to synchronize, keep the historical whole-link
			// stop rather than guess.
			if len(heldBackStacks) == 0 || len(remaining) == 0 {
				if conflicts > 0 {
					result.State = "conflict"
					result.Message = fmt.Sprintf("%d conflict(s) require a manual decision; no file was changed", conflicts)
				} else {
					localDeletionBlock = true
					result.State = "blocked"
					result.Message = fmt.Sprintf("%d locally deleted synchronized file(s) require an explicit stack decision; no file was restored", localDeletions)
				}
				return nil
			}
		}
		if len(newTargets) > 0 {
			result.Discovered = append([]string(nil), newTargets...)
			var registerErr error
			binding, registerErr = s.registerDiscoveredDeploymentTargets(binding, newTargets)
			if registerErr != nil {
				return registerErr
			}
		}
		if len(newTargets) == 0 && len(s.activeAutomationComposePaths(binding)) == 0 && len(preview.ComposeErrors) == 0 {
			result.State = "paused"
			result.Message = "All selected stacks are paused or excluded from automatic synchronization; no new Git stack was discovered"
			return nil
		}

		transfer, importErr := s.ImportBinding(ctx, id, TransferInput{PreviewToken: previewToken, compactResult: true, automation: true, heldBackComposePaths: heldBackStacks})
		if importErr != nil {
			return importErr
		}
		result.State = "up_to_date"
		result.Backup = transfer.Backup
		// A held-back stack keeps its files AND its previous deployment: nothing
		// of it was imported, so nothing of it may be deployed.
		changedPaths = excludeComposeStackPaths(changedPaths, heldBackStacks)
		readBlockedStacks := composePathsForFiles(activeAutomationPaths, transfer.ReadBlocked)
		result.SyncFailed = uniqueSortedStrings(append(append([]string(nil), transfer.ComposeBlocked...), readBlockedStacks...))
		if len(transfer.ComposeBlocked) > 0 {
			result.State = "partial"
			changedPaths = excludeComposeStackPaths(changedPaths, transfer.ComposeBlocked)
			result.Message = fmt.Sprintf("%d invalid Compose stack(s) kept unchanged; other safe changes were synchronized", len(transfer.ComposeBlocked))
		}
		if len(readBlockedStacks) > 0 {
			result.State = "partial"
			changedPaths = excludeComposeStackPaths(changedPaths, readBlockedStacks)
			result.Message = fmt.Sprintf("%d stack(s) contain protected local items or automatically excluded large data directories and were skipped; other stacks continued independently", len(readBlockedStacks))
			if len(transfer.ComposeBlocked) > 0 {
				result.Message = fmt.Sprintf("%d invalid Compose stack(s) and %d stack(s) with protected local items were skipped; other stacks continued independently", len(transfer.ComposeBlocked), len(readBlockedStacks))
			}
		}
		if len(transfer.EditorBlocked) > 0 {
			changedPaths = excludeComposeStackPaths(changedPaths, transfer.EditorBlocked)
			attributedBlock = true
			result.State = "blocked"
			result.Message = fmt.Sprintf("%d stack(s) kept unchanged while edited in Dockman (%s); every other stack was synchronized",
				len(transfer.EditorBlocked), strings.Join(transfer.EditorBlocked, ", "))
		}
		if preserved > 0 && len(transfer.EditorBlocked) == 0 {
			result.State = "blocked"
			result.Message = fmt.Sprintf("%d Git deletion(s) preserved locally; choose restore, archive, or explicit local deletion", preserved)
		} else if changed == 0 && len(readBlockedStacks) == 0 {
			result.Message = "Stack already matches Git"
		} else if len(transfer.EditorBlocked) == 0 && len(transfer.ComposeBlocked) == 0 && len(readBlockedStacks) == 0 {
			result.Message = fmt.Sprintf("%d file(s) synchronized from Git with backup; stack was not deployed", changed)
		}
		if changed == 0 && binding.AutoDeployEnabled && len(readBlockedStacks) == 0 {
			if clearErr := s.clearIdenticalRollbackStates(binding); clearErr != nil {
				return clearErr
			}
		}
		retryingStaleDeploy := changed == 0 && binding.AutoDeployEnabled && isRetryableAutoDeployState(binding.AutoDeployState)
		if retryingStaleDeploy {
			changedPaths = append(changedPaths, splitPatternLines(binding.AutoDeployComposePaths)...)
		}
		changedPaths = excludeComposeStackPaths(changedPaths, result.SyncFailed)
		deployAttempted := false
		if len(changedPaths) > 0 && binding.AutoDeployEnabled {
			deployment, deployErr := s.deployChangedStacks(ctx, binding, synchronizedCommit, changedPaths, transfer.Backup)
			if deployErr != nil {
				return deployErr
			}
			result.Deployed = deployment.Deployed
			result.DeployFailed = deployment.Failed
			result.RolledBack = deployment.RolledBack
			result.RollbackFailed = deployment.RollbackFailed
			deployAttempted = len(deployment.Deployed)+len(deployment.Failed)+len(deployment.RolledBack)+len(deployment.RollbackFailed) > 0
			if len(deployment.Failed) > 0 {
				result.State = "partial"
			}
			if (len(transfer.ComposeBlocked) > 0 || len(readBlockedStacks) > 0) && len(deployment.Failed) > 0 {
				result.Message = fmt.Sprintf("%d stack(s) skipped during synchronization; %d stack(s) deployed and %d additional stack(s) failed deployment", len(result.SyncFailed), len(deployment.Deployed), len(deployment.Failed))
			} else if len(readBlockedStacks) > 0 {
				result.Message = fmt.Sprintf("%d stack(s) with protected local items were skipped; %d independent stack(s) deployed successfully", len(readBlockedStacks), len(deployment.Deployed))
			} else if len(transfer.ComposeBlocked) > 0 {
				result.Message = fmt.Sprintf("%d invalid Compose stack(s) kept unchanged; %d independent stack(s) deployed successfully", len(transfer.ComposeBlocked), len(deployment.Deployed))
			} else if len(deployment.Failed) > 0 {
				result.Message = fmt.Sprintf("%d file(s) synchronized; %d stack(s) deployed and %d stack(s) failed independently", changed, len(deployment.Deployed), len(deployment.Failed))
				if len(deployment.RolledBack) > 0 {
					result.Message += fmt.Sprintf("; %d previous stack version(s) restored safely", len(deployment.RolledBack))
				}
				if len(deployment.RollbackFailed) > 0 {
					result.Message += fmt.Sprintf("; %d automatic rollback(s) require manual recovery", len(deployment.RollbackFailed))
				}
			} else if len(deployment.Deployed) > 0 {
				result.Message = fmt.Sprintf("%d file(s) synchronized and %d stack(s) deployed", changed, len(deployment.Deployed))
			}
		}
		// A re-arm that found nothing to deploy must not leave the previous
		// run's failure frozen. It means the paths that failed are no longer
		// deployment targets - deselected, removed, or excluded - so nothing
		// will ever carry that failure again, and without this the link would
		// now do a full scan every cycle forever, for nothing.
		if retryingStaleDeploy && !deployAttempted {
			if resolveErr := s.store.UpdateBindingAutoDeployState(binding.UUID, "watching",
				"No stack under automatic deployment still carries the previously reported failure; the deployment state was cleared", nil); resolveErr != nil {
				return resolveErr
			}
			binding.AutoDeployState = "watching"
			binding.AutoDeployError = ""
		}
		// Name what was deliberately left alone. The synchronization state is
		// not changed by this: leaving a stack out of automatic deployment is a
		// legitimate choice, and it must not paint a healthy link red. It must
		// not be silent either.
		// Green, but never silent: the operator is told once which paths left
		// synchronization, and why.
		if len(result.AutoExcluded) > 0 {
			result.Message += fmt.Sprintf("; %d local path(s) Dockman cannot read are now left out of synchronization until their permissions change: %s",
				len(result.AutoExcluded), strings.Join(result.AutoExcluded, ", "))
		}
		if unauthorized := unauthorizedDeploymentStacks(binding, changedPaths, activeAutomationPaths); len(unauthorized) > 0 {
			result.Message += fmt.Sprintf("; %d changed stack(s) not authorized for automatic deployment, still running their previous version: %s",
				len(unauthorized), strings.Join(unauthorized, ", "))
		}
		// Reported last so the decision the user has to take is the headline,
		// and named stack by stack: the other stacks are already synchronized.
		// Driven by what the preview attributed, NOT by what the transfer
		// filtered out: an unresolved conflict is never selected for transfer,
		// so its stack would be absent from the transfer's own list and the
		// decision would vanish from the result entirely.
		if len(heldBackStacks) > 0 {
			attributedBlock = true
			result.HeldBack = append([]string(nil), heldBackStacks...)
			if conflicts > 0 {
				result.State = "conflict"
				result.Message = fmt.Sprintf("%d conflict(s) require a manual decision on %s; every other stack was synchronized",
					conflicts, strings.Join(heldBackStacks, ", "))
			} else {
				localDeletionBlock = true
				result.State = "blocked"
				result.Message = fmt.Sprintf("%d locally deleted synchronized file(s) require an explicit decision on %s; no file was restored and every other stack was synchronized",
					localDeletions, strings.Join(heldBackStacks, ", "))
			}
		}
		return nil
	})
	if err != nil {
		message := safeGitError(err)
		_ = s.store.UpdateBindingAutoSyncState(id, "error", message, "", &attemptedAt, nil)
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
			// Only a link-wide block may repaint every stack. A block attributed
			// to named stacks - an open editor, a held-back decision - already
			// carries their own state, and painting the rest would report changes
			// that were in fact applied.
			if !preservedBlock && !localDeletionBlock && !attributedBlock {
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
	bindingState, bindingMessage := "up_to_date", ""
	if result.State == "partial" {
		bindingState, bindingMessage = "partial", result.Message
	}
	_ = s.store.UpdateBindingAutoSyncState(id, bindingState, bindingMessage, synchronizedCommit, &attemptedAt, &succeededAt)
	if skippedStackScan {
		if result.State == "partial" {
			s.updateActiveStackStatusesPreservingLocal(binding, stackSyncUpToDate, "", synchronizedCommit, true, stackSyncError)
		} else {
			s.updateActiveStackStatusesPreservingLocal(binding, stackSyncUpToDate, "", synchronizedCommit, true)
		}
	} else if len(result.SyncFailed) > 0 {
		safePaths := excludeStringValues(s.activeAutomationComposePaths(binding), result.SyncFailed)
		now := time.Now().UTC()
		_ = s.store.UpdateGitStackStatuses(binding.UUID, safePaths, map[string]any{"state": stackSyncUpToDate, "error_message": "", "conflict_count": 0, "last_checked_at": &now, "last_success_at": &now, "last_commit": synchronizedCommit})
	} else {
		s.updateActiveStackStatuses(binding, stackSyncUpToDate, "", synchronizedCommit, true)
	}
	return result, nil
}

func (s *Service) notifyAutomationResult(id string, result AutoSyncResult, syncErr error, explicit bool, previous *StackBinding) {
	if s.eventNotifier == nil {
		return
	}
	binding, err := s.store.GetBinding(id)
	if err != nil {
		return
	}
	emit := func(kind, title, message, severity string) {
		s.eventNotifier(AutomationEvent{Host: binding.Host, BindingID: id, Kind: kind, Title: title, Message: message, Severity: severity})
	}
	details := fmt.Sprintf("Folder Link: %s\nHost: %s\nState: %s\n%s", binding.StackPath, binding.Host, result.State, result.Message)
	previousState, previousError := "", ""
	if previous != nil {
		previousState, previousError = previous.AutoSyncState, previous.AutoSyncError
	}
	stateChanged := previousState != result.State || (strings.TrimSpace(binding.AutoSyncError) != "" && binding.AutoSyncError != previousError)
	if syncErr != nil || result.State == "error" || result.State == "partial" || result.State == "blocked" {
		if syncErr != nil && strings.TrimSpace(result.Message) == "" {
			details += "\nError: " + safeGitError(syncErr)
		}
		if explicit || stateChanged {
			emit("git.sync.failure", "Git synchronization needs attention", details, "error")
		}
	} else if result.State == "conflict" {
		if explicit || stateChanged {
			emit("git.conflict", "Git synchronization conflict", details, "warning")
		}
	} else if result.Changed > 0 || len(result.Deployed) > 0 || len(result.Discovered) > 0 || explicit || (stateChanged && (result.State == "up_to_date" || result.State == "watching")) {
		emit("git.sync.success", "Git synchronization completed", details, "success")
	}
	if len(result.Discovered) > 0 {
		emit("git.stack.discovered", "Git stack authorized for automatic deployment", details+"\nStacks: "+strings.Join(result.Discovered, ", "), "info")
	}
	if len(result.Deployed) > 0 {
		emit("git.deploy.success", "Git stack deployment completed", details+"\nDeployed: "+strings.Join(result.Deployed, ", "), "success")
	}
	if len(result.DeployFailed) > 0 {
		emit("git.deploy.failure", "Git stack deployment failed", details+"\nFailed: "+strings.Join(result.DeployFailed, ", "), "error")
	}
	if len(result.RolledBack) > 0 || len(result.RollbackFailed) > 0 {
		rollback := append(append([]string(nil), result.RolledBack...), result.RollbackFailed...)
		emit("git.rollback", "Git deployment rollback performed", details+"\nStacks: "+strings.Join(rollback, ", "), "warning")
	}
}

func preservedDeletionMessage(message string) string {
	return autoSyncMessageWithoutSkipSuffix(message)
}

func autoSyncMessageWithoutSkipSuffix(message string) string {
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

// BindingAutomationResetResult reports what a manual state reset cleared.
type BindingAutomationResetResult struct {
	Binding      BindingView `json:"binding"`
	ClearedStack int         `json:"clearedStackDeployments"`
	FullScan     bool        `json:"nextCheckIsFullScan"`
}

// ResetBindingAutomationState clears the RECORDED automation states of a
// folder link and forces the next check to be a complete scan.
//
// It exists because a reported state and the reality it describes can drift
// apart: a deployment that failed once, was repaired by hand, and left its red
// mark behind with nothing able to clear it. This resets what Dockman SAYS,
// never what it holds. No file is written, no stack is deployed, no Git
// history is touched, and every conclusion is recomputed by the next scan -
// which is why clearing the last observed commit matters: without it the cheap
// commit-only path would skip that scan and restore the same stale picture.
//
// Per-stack SYNCHRONIZATION states are deliberately left alone. A conflict, a
// local deletion or a preserved Git deletion is a pending decision, not a
// report, and clearing it would show green over a question nobody answered.
func (s *Service) ResetBindingAutomationState(id string) (BindingAutomationResetResult, error) {
	if !s.enabled {
		return BindingAutomationResetResult{}, errors.New("Git synchronization is disabled")
	}
	automationLock := s.repositoryLock("automation:" + id)
	if !automationLock.TryLock() {
		return BindingAutomationResetResult{}, errors.New("automatic synchronization is currently running; retry when it finishes")
	}
	defer automationLock.Unlock()

	row, err := s.store.GetBinding(id)
	if err != nil {
		return BindingAutomationResetResult{}, err
	}

	cleared, err := s.clearRecordedStackDeployFailures(row)
	if err != nil {
		return BindingAutomationResetResult{}, err
	}

	row.AutoSyncState = "idle"
	row.AutoSyncError = ""
	// The commit shortcut is what makes a stale picture survive: drop the last
	// observed commit so the next check rebuilds every conclusion from scratch.
	row.LastAutoSyncCommit = ""
	if row.AutoDeployEnabled {
		row.AutoDeployState = "watching"
	} else {
		row.AutoDeployState = "disabled"
	}
	row.AutoDeployError = ""
	if err := s.store.SaveBinding(&row); err != nil {
		return BindingAutomationResetResult{}, err
	}

	s.recordActivity(ActivityRecord{RepositoryID: row.RepositoryUUID, BindingID: row.UUID, Type: "automation_reset",
		Trigger: "manual", State: "success",
		Details: ActivityDetails{Action: "reset reported automation state; next check is a full scan"}})

	view, err := s.bindingView(row)
	return BindingAutomationResetResult{Binding: view, ClearedStack: cleared, FullScan: true}, err
}

// clearRecordedStackDeployFailures wipes per-stack deployment reports that
// describe a run that is over. Their synchronization state is untouched.
func (s *Service) clearRecordedStackDeployFailures(binding StackBinding) (int, error) {
	rows, err := s.store.GitStackStatuses(binding.UUID)
	if err != nil {
		return 0, err
	}
	stale := make([]string, 0)
	for _, row := range rows {
		if row.DeployState != "" && row.DeployState != "success" {
			stale = append(stale, row.ComposePath)
		}
	}
	if len(stale) == 0 {
		return 0, nil
	}
	if err := s.store.UpdateGitStackStatuses(binding.UUID, stale, map[string]any{"deploy_state": "", "deploy_error": ""}); err != nil {
		return 0, err
	}
	return len(stale), nil
}
