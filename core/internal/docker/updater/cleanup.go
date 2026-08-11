package updater

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxImageCleanupHistory = 250

// maxAutomaticCleanupAttempts bounds how often an automatic run will ask the
// daemon to remove an image it has already refused.
//
// Docker refuses when the image is still referenced. That is often temporary -
// the container holding it is being replaced by the very update that queued
// this candidate - so retrying is right. But it is just as often permanent,
// and a permanent case used to be retried on every single automation cycle,
// forever, each time issuing an ImageRemove that could only fail again. A few
// attempts cover the temporary case; past them the row stays visible and
// actionable, and RetryImageCleanup gives the operator a fresh budget.
const maxAutomaticCleanupAttempts = 3

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
	// Attempts counts the removals the daemon has refused. It is what stops an
	// automatic run from reissuing a doomed ImageRemove on every cycle.
	Attempts int `gorm:"not null;default:0" json:"attempts"`
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
		Columns: []clause.Column{{Name: "host"}, {Name: "target_key"}, {Name: "image_id"}},
		// A re-queued candidate is a fresh chance, so its attempt budget starts
		// over with it.
		DoUpdates: clause.Assignments(map[string]any{
			"container_name": row.ContainerName, "retention": row.Retention,
			"status": row.Status, "reason": row.Reason, "attempts": 0, "updated_at": time.Now(),
		}),
	}).Create(&row).Error
}

func (s *ScanStore) ImageCleanupState(host string) ([]UpdateImageCleanup, error) {
	var rows []UpdateImageCleanup
	err := s.db.Where("host = ?", host).Order("CASE WHEN status = 'pending' THEN 0 ELSE 1 END, created_at DESC").Limit(maxImageCleanupHistory).Find(&rows).Error
	return rows, err
}

// pendingImageCleanup lists the candidates an automatic run should try.
// maxAttempts bounds it to those the daemon has not already refused too often;
// zero means no bound, which is what an explicit retry asks for.
func (s *ScanStore) pendingImageCleanup(host string, maxAttempts int) ([]UpdateImageCleanup, error) {
	query := s.db.Where("host = ? AND status = 'pending'", host)
	if maxAttempts > 0 {
		query = query.Where("attempts < ?", maxAttempts)
	}
	var rows []UpdateImageCleanup
	err := query.Order("created_at DESC").Find(&rows).Error
	return rows, err
}

// resetImageCleanupAttempts gives every pending candidate of a host a fresh
// budget. An operator asking for a retry has usually just removed whatever was
// holding the image.
func (s *ScanStore) resetImageCleanupAttempts(host string) error {
	return s.db.Model(&UpdateImageCleanup{}).
		Where("host = ? AND status = 'pending'", host).
		Update("attempts", 0).Error
}

func (s *ScanStore) markImageCleanup(id uint, removed bool, reason string) error {
	updates := map[string]any{"status": "pending", "reason": boundedExecutionText(reason)}
	if removed {
		updates["status"] = "removed"
	}
	query := s.db.Model(&UpdateImageCleanup{}).Where("id = ?", id)
	if !removed {
		// The refusal is what the budget counts; a success ends the row anyway.
		query = query.Update("attempts", gorm.Expr("attempts + 1"))
		if query.Error != nil {
			return query.Error
		}
		query = s.db.Model(&UpdateImageCleanup{}).Where("id = ?", id)
	}
	return query.Updates(updates).Error
}

// pruneImageCleanupHistory keeps the newest rows of a host and drops the rest,
// whatever their status. Capping only the removed ones left the pending rows -
// the ones that actually accumulate, since a candidate Docker will not remove
// stays pending - growing without any limit at all.
func (s *ScanStore) pruneImageCleanupHistory(host string) error {
	var stale []UpdateImageCleanup
	if err := s.db.Where("host = ?", host).
		Order("CASE WHEN status = 'pending' THEN 0 ELSE 1 END, created_at DESC").
		Offset(maxImageCleanupHistory).Find(&stale).Error; err != nil {
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

// processImageCleanup removes the rollback images a host no longer needs to
// keep. onDemand marks the operator's explicit retry: it ignores the automatic
// attempt budget and starts it over.
func (s *AutomationService) processImageCleanup(ctx context.Context, host string, onDemand bool) error {
	if s.cleanupImage == nil {
		return errors.New("safe image cleanup is unavailable")
	}
	budget := maxAutomaticCleanupAttempts
	if onDemand {
		if err := s.store.resetImageCleanupAttempts(host); err != nil {
			return err
		}
		budget = 0
	}
	// Every pending candidate is loaded, budget or not: retention is decided by
	// rank inside its target, so hiding the exhausted ones would shift what
	// counts as "one of the newest N" and could retire an image that should
	// have been kept. The budget only decides whether the daemon is asked
	// again, never what the retention window is.
	rows, err := s.store.pendingImageCleanup(host, 0)
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
			if budget > 0 && candidate.Attempts >= budget {
				// Docker has refused this one often enough. The row stays
				// pending and visible; an explicit retry can pick it up.
				continue
			}
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
	return s.processImageCleanup(ctx, host, false)
}
