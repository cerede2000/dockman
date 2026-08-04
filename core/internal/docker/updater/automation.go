package updater

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	DefaultUpdateSchedule = "0 4 * * *"
	maxUpdateScanRuns     = 50
	minimumUpdateScanGap  = 15 * time.Minute
)

type UpdateAutomationControl struct {
	ID              uint      `gorm:"primaryKey" json:"-"`
	UpdatedAt       time.Time `json:"updatedAt"`
	Host            string    `gorm:"not null;uniqueIndex" json:"host"`
	Paused          bool      `gorm:"not null;default:false" json:"paused"`
	MaxGroupsPerRun int       `gorm:"not null;default:0" json:"maxGroupsPerRun"`
}

type AutomationControlView struct {
	Paused          bool      `json:"paused"`
	MaxGroupsPerRun int       `json:"maxGroupsPerRun"`
	Running         bool      `json:"running"`
	UpdatedAt       time.Time `json:"updatedAt,omitempty"`
}

type UpdateScanResult struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	Host          string    `gorm:"not null;uniqueIndex:idx_update_scan_result" json:"host"`
	ContainerID   string    `gorm:"not null;uniqueIndex:idx_update_scan_result" json:"containerId"`
	ContainerName string    `gorm:"not null" json:"containerName"`
	Image         string    `gorm:"not null" json:"image"`
	Status        string    `gorm:"not null" json:"status"`
	CurrentDigest string    `gorm:"not null;default:''" json:"currentDigest,omitempty"`
	RemoteDigest  string    `gorm:"not null;default:''" json:"remoteDigest,omitempty"`
	Reason        string    `gorm:"not null;default:''" json:"reason,omitempty"`
	CheckedAt     time.Time `gorm:"not null" json:"checkedAt"`
}

type UpdateScanRun struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   time.Time  `gorm:"not null" json:"startedAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	Host        string     `gorm:"not null;index" json:"host"`
	Trigger     string     `gorm:"not null" json:"trigger"`
	Schedule    string     `gorm:"not null;default:''" json:"schedule,omitempty"`
	Targets     int        `gorm:"not null" json:"targets"`
	Available   int        `gorm:"not null" json:"available"`
	Current     int        `gorm:"not null" json:"current"`
	Skipped     int        `gorm:"not null" json:"skipped"`
	Errors      int        `gorm:"not null" json:"errors"`
	Error       string     `gorm:"not null;default:''" json:"error,omitempty"`
}

type ScanStore struct{ db *gorm.DB }

func NewScanStore(db *gorm.DB) *ScanStore { return &ScanStore{db: db} }

func (s *ScanStore) Save(run *UpdateScanRun, checks []ContainerUpdateCheck) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(run).Error; err != nil {
			return err
		}
		for _, check := range checks {
			row := UpdateScanResult{
				Host: run.Host, ContainerID: check.ContainerID, ContainerName: check.ContainerName,
				Image: check.Image, Status: string(check.Status), CurrentDigest: check.CurrentDigest,
				RemoteDigest: check.RemoteDigest, Reason: check.Reason, CheckedAt: *run.CompletedAt,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "host"}, {Name: "container_id"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"container_name", "image", "status", "current_digest", "remote_digest", "reason", "checked_at", "updated_at",
				}),
			}).Create(&row).Error; err != nil {
				return err
			}
		}
		var stale []UpdateScanRun
		if err := tx.Where("host = ?", run.Host).Order("created_at DESC").Offset(maxUpdateScanRuns).Find(&stale).Error; err != nil {
			return err
		}
		if len(stale) > 0 {
			ids := make([]uint, 0, len(stale))
			for _, old := range stale {
				ids = append(ids, old.ID)
			}
			if err := tx.Delete(&UpdateScanRun{}, ids).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *ScanStore) State(host string) ([]UpdateScanResult, []UpdateScanRun, error) {
	var results []UpdateScanResult
	if err := s.db.Where("host = ?", host).Order("container_name").Find(&results).Error; err != nil {
		return nil, nil, err
	}
	var runs []UpdateScanRun
	if err := s.db.Where("host = ?", host).Order("created_at DESC").Limit(maxUpdateScanRuns).Find(&runs).Error; err != nil {
		return nil, nil, err
	}
	return results, runs, nil
}

func (s *ScanStore) PruneResults(host string, activeContainerIDs []string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		results := tx.Where("host = ?", host)
		blocks := tx.Where("host = ?", host)
		if len(activeContainerIDs) > 0 {
			results = results.Where("container_id NOT IN ?", activeContainerIDs)
			blocks = blocks.Where("container_id NOT IN ?", activeContainerIDs)
		}
		if err := results.Delete(&UpdateScanResult{}).Error; err != nil {
			return err
		}
		return blocks.Delete(&UpdateExecutionBlock{}).Error
	})
}

type InventoryProvider func(context.Context, string) ([]UpdateEnrollment, error)
type ScanProvider func(context.Context, string, []string) ([]ContainerUpdateCheck, error)
type ScanNotifier func(context.Context, UpdateScanRun, []ContainerUpdateCheck) error

type ScheduledScan struct {
	Schedule string    `json:"schedule"`
	NextRun  time.Time `json:"nextRun"`
	Targets  int       `json:"targets"`
}

type AutomationService struct {
	store           *ScanStore
	inventory       InventoryProvider
	scan            ScanProvider
	notify          ScanNotifier
	execute         UpdateExecutor
	notifyExecution ExecutionNotifier
	cleanupImage    ImageCleanupProvider
	scheduler       gocron.Scheduler

	jobsMu  sync.Mutex
	runMu   sync.Mutex
	jobs    map[string]gocron.Job
	targets map[string]int
	running map[string]bool
}

func NewAutomationService(store *ScanStore, inventory InventoryProvider, scan ScanProvider) (*AutomationService, error) {
	if interrupted, err := store.RecoverInterruptedExecutions(); err != nil {
		return nil, fmt.Errorf("recover interrupted automatic update executions: %w", err)
	} else if interrupted > 0 {
		log.Warn().Int64("executions", interrupted).Msg("marked interrupted automatic update executions for review")
	}
	scheduler, err := gocron.NewScheduler()
	if err != nil {
		return nil, err
	}
	service := &AutomationService{
		store: store, inventory: inventory, scan: scan, scheduler: scheduler,
		jobs: make(map[string]gocron.Job), targets: make(map[string]int), running: make(map[string]bool),
	}
	scheduler.Start()
	return service, nil
}

func (s *AutomationService) Shutdown() error { return s.scheduler.Shutdown() }

func (s *AutomationService) SetNotifier(notifier ScanNotifier) { s.notify = notifier }

func (s *AutomationService) SetExecutor(executor UpdateExecutor) { s.execute = executor }

func (s *AutomationService) SetExecutionNotifier(notifier ExecutionNotifier) {
	s.notifyExecution = notifier
}

func (s *AutomationService) SetImageCleaner(cleaner ImageCleanupProvider) { s.cleanupImage = cleaner }

func NormalizeUpdateSchedule(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = DefaultUpdateSchedule
	}
	if len(value) > 120 || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("schedule is multiline or too long")
	}
	parsed, err := cron.ParseStandard(value)
	if err != nil {
		return "", fmt.Errorf("use a standard five-field cron expression: %w", err)
	}
	first := parsed.Next(time.Now())
	second := parsed.Next(first)
	if first.IsZero() || second.IsZero() || second.Sub(first) < minimumUpdateScanGap {
		return "", fmt.Errorf("automatic image checks cannot run more often than every %s", minimumUpdateScanGap)
	}
	return strings.Join(strings.Fields(value), " "), nil
}

func (s *AutomationService) ReconcileHost(ctx context.Context, host string) error {
	rows, err := s.inventory(ctx, host)
	if err != nil {
		return err
	}
	return s.ReconcileInventory(host, rows)
}

func (s *AutomationService) RefreshHost(host string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.ReconcileHost(ctx, host); err != nil {
			log.Warn().Err(err).Str("host", host).Msg("unable to refresh automatic image scan schedules")
		}
	}()
}

func (s *AutomationService) ReconcileInventory(host string, rows []UpdateEnrollment) error {
	targets := make(map[string]int)
	for _, row := range rows {
		if !row.Enrolled {
			continue
		}
		schedule, err := NormalizeUpdateSchedule(row.Schedule)
		if err != nil {
			log.Warn().Err(err).Str("host", host).Str("container", row.ContainerName).Msg("automatic image check schedule ignored")
			continue
		}
		targets[schedule]++
	}

	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	prefix := host + "\x00"
	for key, job := range s.jobs {
		if strings.HasPrefix(key, prefix) {
			schedule := strings.TrimPrefix(key, prefix)
			if targets[schedule] == 0 {
				_ = s.scheduler.RemoveJob(job.ID())
				delete(s.jobs, key)
				delete(s.targets, key)
			}
		}
	}
	for schedule := range targets {
		key := prefix + schedule
		s.targets[key] = targets[schedule]
		if _, exists := s.jobs[key]; exists {
			continue
		}
		job, err := s.scheduler.NewJob(
			gocron.CronJob(schedule, false),
			gocron.NewTask(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
				defer cancel()
				if _, _, runErr := s.run(ctx, host, schedule, "scheduled"); runErr != nil {
					log.Warn().Err(runErr).Str("host", host).Str("schedule", schedule).Msg("scheduled image update scan failed")
				}
			}),
			gocron.WithSingletonMode(gocron.LimitModeReschedule),
		)
		if err != nil {
			return err
		}
		s.jobs[key] = job
	}
	return nil
}

func (s *AutomationService) RunNow(ctx context.Context, host string) (UpdateScanRun, []ContainerUpdateCheck, error) {
	return s.run(ctx, host, "", "manual")
}

func (s *AutomationService) RunAutomaticNow(ctx context.Context, host string) (UpdateScanRun, []ContainerUpdateCheck, error) {
	control, err := s.Control(host)
	if err != nil {
		return UpdateScanRun{}, nil, err
	}
	if control.Paused {
		return UpdateScanRun{}, nil, errors.New("automatic updates are paused for this host")
	}
	return s.run(ctx, host, "", "manual-execution")
}

func (s *AutomationService) run(ctx context.Context, host, requestedSchedule, trigger string) (UpdateScanRun, []ContainerUpdateCheck, error) {
	run := UpdateScanRun{Host: host, Trigger: trigger, Schedule: requestedSchedule, StartedAt: time.Now()}
	s.runMu.Lock()
	if s.running[host] {
		s.runMu.Unlock()
		return run, nil, fmt.Errorf("an image update scan is already running for host %q", host)
	}
	s.running[host] = true
	s.runMu.Unlock()
	defer func() { s.runMu.Lock(); delete(s.running, host); s.runMu.Unlock() }()

	rows, err := s.inventory(ctx, host)
	if err == nil {
		ids := make([]string, 0, len(rows))
		activeIDs := make([]string, 0, len(rows))
		for _, row := range rows {
			if !row.Enrolled {
				continue
			}
			activeIDs = append(activeIDs, row.ContainerID)
			if requestedSchedule != "" {
				schedule, scheduleErr := NormalizeUpdateSchedule(row.Schedule)
				if scheduleErr != nil || schedule != requestedSchedule {
					continue
				}
			}
			ids = append(ids, row.ContainerID)
		}
		if pruneErr := s.store.PruneResults(host, activeIDs); pruneErr != nil {
			err = fmt.Errorf("prune stale image scan results: %w", pruneErr)
		}
		run.Targets = len(ids)
		var checks []ContainerUpdateCheck
		if err == nil && len(ids) > 0 {
			checks, err = s.scan(ctx, host, ids)
		}
		if err == nil {
			completed := time.Now()
			run.CompletedAt = &completed
			for _, check := range checks {
				switch check.Status {
				case ContainerUpdateAvailable:
					run.Available++
				case ContainerUpdateCurrent:
					run.Current++
				case ContainerUpdateSkipped:
					run.Skipped++
				case ContainerUpdateError:
					run.Errors++
				}
			}
			if saveErr := s.store.Save(&run, checks); saveErr != nil {
				return run, checks, saveErr
			}
			if (trigger == "scheduled" || trigger == "manual-execution") && s.execute != nil {
				control, controlErr := s.Control(host)
				if controlErr != nil {
					return run, checks, controlErr
				}
				if control.Paused {
					s.notifyScan(ctx, run, checks)
					return run, checks, nil
				}
				s.executeAvailable(ctx, run, rows, checks)
				// Availability mail would be misleading after the same run has
				// already applied the update. Keep registry failures in the scan
				// notification; execution has its own outcome notification.
				s.notifyScan(ctx, run, checksWithoutAvailable(checks))
			} else {
				s.notifyScan(ctx, run, checks)
			}
			return run, checks, nil
		}
	}
	completed := time.Now()
	run.CompletedAt = &completed
	if err != nil {
		run.Error = err.Error()
		run.Errors++
	}
	if saveErr := s.store.Save(&run, nil); saveErr != nil {
		return run, nil, errors.Join(err, saveErr)
	}
	s.notifyScan(ctx, run, nil)
	return run, nil, err
}

func (s *AutomationService) executeAvailable(ctx context.Context, scanRun UpdateScanRun, rows []UpdateEnrollment, checks []ContainerUpdateCheck) {
	byID := make(map[string]UpdateEnrollment, len(rows))
	for _, row := range rows {
		byID[row.ContainerID] = row
	}
	// A stack policy is one transaction. If one member/digest is circuit-broken,
	// do not silently update the remaining members as smaller partial groups.
	blockedStacks := make(map[string]struct{})
	for _, check := range checks {
		if check.Status != ContainerUpdateAvailable {
			continue
		}
		row, ok := byID[check.ContainerID]
		if !ok || row.PolicyTarget != UpdateTargetStack || row.StackKey == "" {
			continue
		}
		_, blocked, err := s.store.ExecutionBlock(scanRun.Host, check.ContainerID, check.RemoteDigest)
		if err != nil {
			log.Warn().Err(err).Str("host", scanRun.Host).Str("container", check.ContainerName).Msg("unable to inspect stack update circuit breaker")
			blockedStacks[row.StackKey] = struct{}{}
			continue
		}
		if blocked {
			blockedStacks[row.StackKey] = struct{}{}
		}
	}
	targets := make([]UpdateExecutionTarget, 0, scanRun.Available)
	for _, check := range checks {
		if check.Status != ContainerUpdateAvailable {
			continue
		}
		row, ok := byID[check.ContainerID]
		if !ok || !row.Enrolled || row.Source == "protected" || row.Source == "disabled-label" {
			continue
		}
		if _, blocked := blockedStacks[row.StackKey]; row.PolicyTarget == UpdateTargetStack && blocked {
			continue
		}
		_, blocked, err := s.store.ExecutionBlock(scanRun.Host, check.ContainerID, check.RemoteDigest)
		if err != nil {
			log.Warn().Err(err).Str("host", scanRun.Host).Str("container", check.ContainerName).Msg("unable to inspect automatic update circuit breaker")
			continue
		}
		if blocked {
			continue
		}
		targetType := row.PolicyTarget
		if targetType != UpdateTargetStack {
			targetType = UpdateTargetContainer
		}
		targets = append(targets, UpdateExecutionTarget{
			ContainerID: check.ContainerID, ContainerName: check.ContainerName, Image: check.Image,
			State: row.State, RemoteDigest: check.RemoteDigest, RollbackEnabled: row.Rollback,
			TargetType: targetType, StackName: row.StackName, StackKey: row.StackKey,
			ServiceName: row.ServiceName, DependsOn: row.DependsOn,
			CleanupEnabled: row.CleanupEnabled, CleanupKeep: row.CleanupKeep,
		})
	}
	if len(targets) == 0 {
		return
	}
	control, err := s.Control(scanRun.Host)
	if err != nil {
		log.Error().Err(err).Str("host", scanRun.Host).Msg("unable to load automatic update execution controls")
		return
	}
	targets = limitExecutionGroups(targets, control.MaxGroupsPerRun)
	if len(targets) == 0 {
		return
	}
	executionSchedule := scanRun.Schedule
	if executionSchedule == "" {
		executionSchedule = "manual"
	}
	executionRun := UpdateExecutionRun{Host: scanRun.Host, Schedule: executionSchedule, ScanRunID: scanRun.ID, StartedAt: time.Now(), Targets: len(targets)}
	if err := s.store.BeginExecution(&executionRun); err != nil {
		log.Error().Err(err).Str("host", scanRun.Host).Msg("unable to persist automatic update execution start")
		return
	}
	outcomes := s.execute(ctx, scanRun.Host, targets)
	seen := make(map[string]struct{}, len(outcomes))
	for _, outcome := range outcomes {
		seen[outcome.ContainerID] = struct{}{}
		switch outcome.State {
		case ExecutionUpdated:
			executionRun.Updated++
		case ExecutionCurrent:
			executionRun.Current++
		case ExecutionRolledBack:
			executionRun.RolledBack++
		case ExecutionSkipped:
			executionRun.Skipped++
		default:
			executionRun.Failed++
		}
	}
	for _, target := range targets {
		if _, ok := seen[target.ContainerID]; ok {
			continue
		}
		outcomes = append(outcomes, UpdateExecutionOutcome{UpdateExecutionTarget: target, State: ExecutionFailed, Message: "automatic update executor returned no outcome"})
		executionRun.Failed++
	}
	completed := time.Now()
	executionRun.CompletedAt = &completed
	if err := s.store.SaveExecution(&executionRun, outcomes); err != nil {
		log.Error().Err(err).Str("host", scanRun.Host).Msg("unable to persist automatic update execution")
		return
	}
	if cleanupErr := s.queueAndProcessImageCleanup(ctx, scanRun.Host, outcomes); cleanupErr != nil {
		log.Warn().Err(cleanupErr).Str("host", scanRun.Host).Msg("one or more previous images were retained after safe cleanup checks")
	}
	if s.notifyExecution != nil {
		if err := s.notifyExecution(ctx, executionRun, outcomes); err != nil {
			log.Warn().Err(err).Str("host", scanRun.Host).Msg("automatic update execution notification failed")
		}
	}
}

func limitExecutionGroups(targets []UpdateExecutionTarget, maximum int) []UpdateExecutionTarget {
	if maximum <= 0 {
		return targets
	}
	selected := make([]UpdateExecutionTarget, 0, len(targets))
	groups := make(map[string]struct{}, maximum)
	for _, target := range targets {
		key := "container:" + target.ContainerID
		if target.TargetType == UpdateTargetStack && target.StackKey != "" {
			key = "stack:" + target.StackKey
		}
		if _, exists := groups[key]; !exists {
			if len(groups) >= maximum {
				continue
			}
			groups[key] = struct{}{}
		}
		selected = append(selected, target)
	}
	return selected
}

func checksWithoutAvailable(checks []ContainerUpdateCheck) []ContainerUpdateCheck {
	filtered := make([]ContainerUpdateCheck, 0, len(checks))
	for _, check := range checks {
		if check.Status != ContainerUpdateAvailable {
			filtered = append(filtered, check)
		}
	}
	return filtered
}

func (s *AutomationService) notifyScan(ctx context.Context, run UpdateScanRun, checks []ContainerUpdateCheck) {
	if s.notify == nil {
		return
	}
	if err := s.notify(ctx, run, checks); err != nil {
		log.Warn().Err(err).Str("host", run.Host).Str("trigger", run.Trigger).Msg("image scan notification failed")
	}
}

func (s *AutomationService) State(host string) ([]UpdateScanResult, []UpdateScanRun, []ScheduledScan, error) {
	results, runs, err := s.store.State(host)
	if err != nil {
		return nil, nil, nil, err
	}
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	var schedules []ScheduledScan
	prefix := host + "\x00"
	for key, job := range s.jobs {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		next, nextErr := job.NextRun()
		if nextErr != nil {
			continue
		}
		schedules = append(schedules, ScheduledScan{Schedule: strings.TrimPrefix(key, prefix), NextRun: next, Targets: s.targets[key]})
	}
	slices.SortFunc(schedules, func(a, b ScheduledScan) int { return a.NextRun.Compare(b.NextRun) })
	return results, runs, schedules, nil
}

func (s *AutomationService) ExecutionState(host string) ([]UpdateExecutionRun, []UpdateExecutionResult, []UpdateExecutionBlock, error) {
	return s.store.ExecutionState(host)
}

func (s *AutomationService) ClearExecutionBlock(host, containerID string) error {
	return s.store.ClearExecutionBlock(host, containerID)
}

func (s *AutomationService) ImageCleanupState(host string) ([]UpdateImageCleanup, error) {
	return s.store.ImageCleanupState(host)
}

func (s *AutomationService) RetryImageCleanup(ctx context.Context, host string) error {
	return s.processImageCleanup(ctx, host)
}

func (s *AutomationService) Control(host string) (AutomationControlView, error) {
	var row UpdateAutomationControl
	if err := s.store.db.Where("host = ?", host).Limit(1).Find(&row).Error; err != nil {
		return AutomationControlView{}, err
	}
	s.runMu.Lock()
	running := s.running[host]
	s.runMu.Unlock()
	return AutomationControlView{Paused: row.Paused, MaxGroupsPerRun: row.MaxGroupsPerRun, Running: running, UpdatedAt: row.UpdatedAt}, nil
}

func (s *AutomationService) SaveControl(host string, paused bool, maxGroups int) (AutomationControlView, error) {
	if strings.TrimSpace(host) == "" {
		return AutomationControlView{}, errors.New("host is required")
	}
	if maxGroups < 0 || maxGroups > 1000 {
		return AutomationControlView{}, errors.New("maximum update groups per run must be between 0 and 1000")
	}
	row := UpdateAutomationControl{Host: host}
	if err := s.store.db.Where("host = ?", host).Assign(map[string]any{"paused": paused, "max_groups_per_run": maxGroups}).FirstOrCreate(&row).Error; err != nil {
		return AutomationControlView{}, err
	}
	return s.Control(host)
}
