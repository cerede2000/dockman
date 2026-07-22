package gitsync

import (
	"context"
	"errors"
	"fmt"
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
	Enabled         bool `json:"enabled"`
	IntervalMinutes int  `json:"intervalMinutes"`
}

type AutoSyncResult struct {
	BindingID string `json:"bindingId"`
	State     string `json:"state"`
	Changed   int    `json:"changed"`
	Conflicts int    `json:"conflicts"`
	Backup    string `json:"backup,omitempty"`
	Message   string `json:"message"`
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
	if input.IntervalMinutes < minAutoSyncIntervalMinutes || input.IntervalMinutes > maxAutoSyncIntervalMinutes {
		return BindingView{}, fmt.Errorf("automatic synchronization interval must be between %d and %d minutes", minAutoSyncIntervalMinutes, maxAutoSyncIntervalMinutes)
	}
	row.AutoSyncEnabled = input.Enabled
	row.AutoSyncIntervalMinutes = input.IntervalMinutes
	row.AutoSyncError = ""
	if input.Enabled {
		row.AutoSyncState = "watching"
	} else {
		row.AutoSyncState = "disabled"
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
	binding, err := s.store.GetBinding(id)
	if err != nil {
		return AutoSyncResult{}, err
	}
	if !binding.AutoSyncEnabled {
		return AutoSyncResult{}, errors.New("automatic synchronization is disabled for this folder link")
	}
	automationLock := s.repositoryLock("automation:" + id)
	if !automationLock.TryLock() {
		return AutoSyncResult{}, errors.New("automatic synchronization is already running for this folder link")
	}
	defer automationLock.Unlock()

	attemptedAt := time.Now().UTC()
	_ = s.store.UpdateBindingAutoSyncState(id, "syncing", "", "", &attemptedAt, nil)
	result := AutoSyncResult{BindingID: id, State: "syncing"}
	var synchronizedCommit string
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
		if binding.LastAutoSyncCommit != "" && binding.LastAutoSyncCommit == status.Head {
			result.State = "up_to_date"
			result.Message = "No new Git commit; stack scan skipped"
			return nil
		}

		preview, previewErr := s.PreviewBinding(id, "repository_to_stack", TransferInput{})
		if previewErr != nil {
			return previewErr
		}
		result.Changed, result.Conflicts = preview.Changed, preview.Conflicts
		if preview.Conflicts > 0 {
			result.State = "conflict"
			result.Message = fmt.Sprintf("%d conflict(s) require a manual decision; no file was changed", preview.Conflicts)
			return nil
		}

		transfer, importErr := s.ImportBinding(ctx, id, TransferInput{PreviewToken: preview.PreviewToken})
		if importErr != nil {
			return importErr
		}
		result.State = "up_to_date"
		result.Backup = transfer.Backup
		if preview.Changed == 0 {
			result.Message = "Stack already matches Git"
		} else {
			result.Message = fmt.Sprintf("%d file(s) synchronized from Git with backup; stack was not deployed", preview.Changed)
		}
		return nil
	})
	if err != nil {
		message := safeGitError(err)
		_ = s.store.UpdateBindingAutoSyncState(id, "error", message, "", &attemptedAt, nil)
		return AutoSyncResult{BindingID: id, State: "error", Message: message}, err
	}
	if result.State == "conflict" || result.State == "blocked" {
		_ = s.store.UpdateBindingAutoSyncState(id, result.State, result.Message, "", &attemptedAt, nil)
		return result, nil
	}
	succeededAt := time.Now().UTC()
	_ = s.store.UpdateBindingAutoSyncState(id, "up_to_date", "", synchronizedCommit, &attemptedAt, &succeededAt)
	return result, nil
}
