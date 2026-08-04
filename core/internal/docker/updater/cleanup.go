package updater

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm/clause"
)

const maxImageCleanupHistory = 250

type UpdateImageCleanup struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	Host          string    `gorm:"not null;uniqueIndex:idx_update_image_cleanup_candidate" json:"host"`
	TargetKey     string    `gorm:"not null;uniqueIndex:idx_update_image_cleanup_candidate" json:"targetKey"`
	ContainerName string    `gorm:"not null" json:"containerName"`
	ImageID       string    `gorm:"not null;uniqueIndex:idx_update_image_cleanup_candidate" json:"imageId"`
	Retention     int       `gorm:"not null" json:"retention"`
	Status        string    `gorm:"not null;default:'pending'" json:"status"`
	Reason        string    `gorm:"not null;default:''" json:"reason,omitempty"`
}

type ImageCleanupProvider func(context.Context, string, string) (removed bool, reason string, err error)

func cleanupTargetKey(target UpdateExecutionTarget) string {
	if target.TargetType == UpdateTargetStack && target.StackKey != "" {
		service := target.ServiceName
		if service == "" {
			service = target.ContainerName
		}
		return "stack:" + target.StackKey + ":" + service
	}
	return "container:" + target.ContainerName
}

func (s *ScanStore) QueueImageCleanup(host string, outcome UpdateExecutionOutcome) error {
	if !outcome.CleanupEnabled || outcome.State != ExecutionUpdated || strings.TrimSpace(outcome.PreviousImage) == "" {
		return nil
	}
	row := UpdateImageCleanup{
		Host: host, TargetKey: cleanupTargetKey(outcome.UpdateExecutionTarget), ContainerName: outcome.ContainerName,
		ImageID: outcome.PreviousImage, Retention: outcome.CleanupKeep, Status: "pending",
		Reason: fmt.Sprintf("retained as one of %d configured rollback image(s)", outcome.CleanupKeep),
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "host"}, {Name: "target_key"}, {Name: "image_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"container_name", "retention", "status", "reason", "updated_at"}),
	}).Create(&row).Error
}

func (s *ScanStore) ImageCleanupState(host string) ([]UpdateImageCleanup, error) {
	var rows []UpdateImageCleanup
	err := s.db.Where("host = ?", host).Order("CASE WHEN status = 'pending' THEN 0 ELSE 1 END, created_at DESC").Limit(maxImageCleanupHistory).Find(&rows).Error
	return rows, err
}

func (s *ScanStore) pendingImageCleanup(host string) ([]UpdateImageCleanup, error) {
	var rows []UpdateImageCleanup
	err := s.db.Where("host = ? AND status = 'pending'", host).Order("created_at DESC").Find(&rows).Error
	return rows, err
}

func (s *ScanStore) markImageCleanup(id uint, removed bool, reason string) error {
	status := "pending"
	if removed {
		status = "removed"
	}
	return s.db.Model(&UpdateImageCleanup{}).Where("id = ?", id).Updates(map[string]any{
		"status": status, "reason": boundedExecutionText(reason),
	}).Error
}

func (s *ScanStore) pruneImageCleanupHistory(host string) error {
	var stale []UpdateImageCleanup
	if err := s.db.Where("host = ? AND status = 'removed'", host).Order("created_at DESC").Offset(maxImageCleanupHistory).Find(&stale).Error; err != nil {
		return err
	}
	if len(stale) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(stale))
	for _, row := range stale {
		ids = append(ids, row.ID)
	}
	return s.db.Delete(&UpdateImageCleanup{}, ids).Error
}

func (s *AutomationService) processImageCleanup(ctx context.Context, host string) error {
	if s.cleanupImage == nil {
		return errors.New("safe image cleanup is unavailable")
	}
	rows, err := s.store.pendingImageCleanup(host)
	if err != nil {
		return err
	}
	byTarget := make(map[string][]UpdateImageCleanup)
	for _, row := range rows {
		byTarget[row.TargetKey] = append(byTarget[row.TargetKey], row)
	}
	var cleanupErrors []error
	for _, candidates := range byTarget {
		slices.SortFunc(candidates, func(a, b UpdateImageCleanup) int { return b.CreatedAt.Compare(a.CreatedAt) })
		keep := candidates[0].Retention
		if keep < 0 {
			keep = 0
		}
		for _, candidate := range candidates[min(keep, len(candidates)):] {
			removed, reason, removeErr := s.cleanupImage(ctx, host, candidate.ImageID)
			if removeErr != nil {
				reason = "safe removal failed: " + removeErr.Error()
				cleanupErrors = append(cleanupErrors, fmt.Errorf("%s: %w", candidate.ContainerName, removeErr))
			}
			if strings.TrimSpace(reason) == "" {
				if removed {
					reason = "previous image removed safely"
				} else {
					reason = "previous image retained by Docker safety checks"
				}
			}
			if err := s.store.markImageCleanup(candidate.ID, removed, reason); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
	}
	if err := s.store.pruneImageCleanupHistory(host); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	return errors.Join(cleanupErrors...)
}

func imageCleanupStackEligible(outcomes []UpdateExecutionOutcome) map[string]bool {
	eligible := make(map[string]bool)
	for _, outcome := range outcomes {
		if outcome.TargetType != UpdateTargetStack || outcome.StackKey == "" {
			continue
		}
		if _, seen := eligible[outcome.StackKey]; !seen {
			eligible[outcome.StackKey] = true
		}
		if outcome.State != ExecutionUpdated && outcome.State != ExecutionCurrent {
			eligible[outcome.StackKey] = false
		}
	}
	return eligible
}

func (s *AutomationService) queueAndProcessImageCleanup(ctx context.Context, host string, outcomes []UpdateExecutionOutcome) error {
	if s.cleanupImage == nil {
		return nil
	}
	stackEligible := imageCleanupStackEligible(outcomes)
	for _, outcome := range outcomes {
		if outcome.TargetType == UpdateTargetStack && !stackEligible[outcome.StackKey] {
			continue
		}
		if err := s.store.QueueImageCleanup(host, outcome); err != nil {
			return err
		}
	}
	return s.processImageCleanup(ctx, host)
}
