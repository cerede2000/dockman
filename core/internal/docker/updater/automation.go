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
	query := s.db.Where("host = ?", host)
	if len(activeContainerIDs) > 0 {
		query = query.Where("container_id NOT IN ?", activeContainerIDs)
	}
	return query.Delete(&UpdateScanResult{}).Error
}

type InventoryProvider func(context.Context, string) ([]UpdateEnrollment, error)
type ScanProvider func(context.Context, string, []string) ([]ContainerUpdateCheck, error)

type ScheduledScan struct {
	Schedule string    `json:"schedule"`
	NextRun  time.Time `json:"nextRun"`
	Targets  int       `json:"targets"`
}

type AutomationService struct {
	store     *ScanStore
	inventory InventoryProvider
	scan      ScanProvider
	scheduler gocron.Scheduler

	jobsMu  sync.Mutex
	runMu   sync.Mutex
	jobs    map[string]gocron.Job
	targets map[string]int
	running map[string]bool
}

func NewAutomationService(store *ScanStore, inventory InventoryProvider, scan ScanProvider) (*AutomationService, error) {
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
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
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
	return run, nil, err
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
