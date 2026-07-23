package gitsync

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

const maxActivityDetailsSize = 16 << 10

type ActivityDetails struct {
	Message       string   `json:"message,omitempty"`
	Direction     string   `json:"direction,omitempty"`
	Action        string   `json:"action,omitempty"`
	Changed       int      `json:"changed,omitempty"`
	Conflicts     int      `json:"conflicts,omitempty"`
	Preserved     int      `json:"preserved,omitempty"`
	Paths         []string `json:"paths,omitempty"`
	DeploymentIDs []string `json:"deploymentIds,omitempty"`
}

type ActivityRecord struct {
	RepositoryID string
	BindingID    string
	ComposePath  string
	Type         string
	State        string
	Trigger      string
	CommitSHA    string
	BackupID     string
	Error        string
	Details      ActivityDetails
}

func (s *Service) recordActivity(record ActivityRecord) {
	if record.Trigger == "" {
		record.Trigger = "system"
	}
	if record.State == "" {
		record.State = "success"
	}
	raw, _ := json.Marshal(record.Details)
	if len(raw) > maxActivityDetailsSize {
		raw, _ = json.Marshal(ActivityDetails{Message: "Activity details were truncated"})
	}
	now := time.Now().UTC()
	if err := s.store.StartOperation(&Operation{
		UUID: uuid.NewString(), RepositoryUUID: record.RepositoryID, BindingUUID: record.BindingID,
		ComposePath: record.ComposePath, OperationType: record.Type, State: record.State,
		Trigger: record.Trigger, Details: string(raw), CommitSHA: record.CommitSHA,
		BackupUUID: record.BackupID, ErrorMessage: record.Error, StartedAt: &now, FinishedAt: &now,
	}); err != nil {
		log.Warn().Err(err).Str("activity", record.Type).Msg("unable to record Git synchronization activity")
	}
	_ = s.runRetentionMaintenance(now, false)
}

func (s *Service) runRetentionMaintenance(now time.Time, force bool) error {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if !force && !s.lastMaintenanceAt.IsZero() && now.Sub(s.lastMaintenanceAt) < 24*time.Hour {
		return nil
	}
	// A storage error must not turn every following Git event into another
	// cleanup attempt. Retry on the next daily window and keep foreground work
	// responsive while surfacing the failure in the logs.
	s.lastMaintenanceAt = now
	if err := s.store.PruneGitHistory(now.AddDate(0, 0, -s.historyRetentionDays)); err != nil {
		log.Warn().Err(err).Msg("unable to prune Git synchronization history")
		return err
	}
	if err := s.pruneExpiredBackups(now); err != nil {
		log.Warn().Err(err).Msg("unable to prune expired Git synchronization backups")
		return err
	}
	return nil
}
