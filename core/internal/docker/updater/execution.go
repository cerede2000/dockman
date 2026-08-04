package updater

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	ExecutionUpdated    = "updated"
	ExecutionCurrent    = "current"
	ExecutionRolledBack = "rolled_back"
	ExecutionFailed     = "failed"
	ExecutionSkipped    = "skipped"
	maxExecutionRuns    = 50
	maxExecutionLogs    = 32 << 10
)

type UpdateExecutionTarget struct {
	ContainerID     string
	ContainerName   string
	Image           string
	State           string
	RemoteDigest    string
	RollbackEnabled bool
	TargetType      string
	StackName       string
	StackKey        string
	ServiceName     string
	DependsOn       string
}

type UpdateExecutionOutcome struct {
	UpdateExecutionTarget
	State   string
	Message string
	Logs    string
}

type UpdateExecutionRun struct {
	ID          uint       `gorm:"primaryKey" json:"id"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   time.Time  `gorm:"not null" json:"startedAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	Host        string     `gorm:"not null;index" json:"host"`
	Schedule    string     `gorm:"not null" json:"schedule"`
	ScanRunID   uint       `gorm:"not null;index" json:"scanRunId"`
	Targets     int        `gorm:"not null" json:"targets"`
	Updated     int        `gorm:"not null" json:"updated"`
	Current     int        `gorm:"not null" json:"current"`
	RolledBack  int        `gorm:"not null" json:"rolledBack"`
	Failed      int        `gorm:"not null" json:"failed"`
	Skipped     int        `gorm:"not null" json:"skipped"`
}

func (UpdateExecutionRun) TableName() string { return "update_execution_runs" }

type UpdateExecutionResult struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	CreatedAt       time.Time `json:"createdAt"`
	RunID           uint      `gorm:"not null;index" json:"runId"`
	Host            string    `gorm:"not null;index" json:"host"`
	ContainerID     string    `gorm:"not null;index" json:"containerId"`
	ContainerName   string    `gorm:"not null" json:"containerName"`
	Image           string    `gorm:"not null" json:"image"`
	RemoteDigest    string    `gorm:"not null" json:"remoteDigest"`
	RollbackEnabled bool      `gorm:"not null" json:"rollbackEnabled"`
	TargetType      string    `gorm:"not null;default:'container'" json:"targetType"`
	StackName       string    `gorm:"not null;default:''" json:"stackName,omitempty"`
	StackKey        string    `gorm:"not null;default:''" json:"stackKey,omitempty"`
	State           string    `gorm:"not null" json:"state"`
	Message         string    `gorm:"not null;default:''" json:"message,omitempty"`
	Logs            string    `gorm:"not null;default:''" json:"logs,omitempty"`
}

func (UpdateExecutionResult) TableName() string { return "update_execution_results" }

type UpdateExecutionBlock struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	Host          string    `gorm:"not null;uniqueIndex:idx_update_execution_block" json:"host"`
	ContainerID   string    `gorm:"not null;uniqueIndex:idx_update_execution_block" json:"containerId"`
	ContainerName string    `gorm:"not null" json:"containerName"`
	Image         string    `gorm:"not null" json:"image"`
	RemoteDigest  string    `gorm:"not null" json:"remoteDigest"`
	TargetType    string    `gorm:"not null;default:'container'" json:"targetType"`
	StackName     string    `gorm:"not null;default:''" json:"stackName,omitempty"`
	StackKey      string    `gorm:"not null;default:'';index" json:"stackKey,omitempty"`
	Reason        string    `gorm:"not null" json:"reason"`
}

func (UpdateExecutionBlock) TableName() string { return "update_execution_blocks" }

type UpdateExecutor func(context.Context, string, []UpdateExecutionTarget) []UpdateExecutionOutcome
type ExecutionNotifier func(context.Context, UpdateExecutionRun, []UpdateExecutionOutcome) error

func (s *ScanStore) SaveExecution(run *UpdateExecutionRun, outcomes []UpdateExecutionOutcome) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(run).Error; err != nil {
			return err
		}
		for _, outcome := range outcomes {
			row := UpdateExecutionResult{
				RunID: run.ID, Host: run.Host, ContainerID: outcome.ContainerID,
				ContainerName: outcome.ContainerName, Image: outcome.Image,
				RemoteDigest: outcome.RemoteDigest, RollbackEnabled: outcome.RollbackEnabled,
				TargetType: outcome.TargetType, StackName: outcome.StackName, StackKey: outcome.StackKey,
				State: outcome.State, Message: boundedExecutionText(outcome.Message), Logs: boundedExecutionText(outcome.Logs),
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
			switch outcome.State {
			case ExecutionFailed, ExecutionRolledBack:
				block := UpdateExecutionBlock{Host: run.Host, ContainerID: outcome.ContainerID, ContainerName: outcome.ContainerName, Image: outcome.Image, RemoteDigest: outcome.RemoteDigest, TargetType: outcome.TargetType, StackName: outcome.StackName, StackKey: outcome.StackKey, Reason: boundedExecutionText(outcome.Message)}
				if err := tx.Where("host = ? AND container_id = ?", run.Host, outcome.ContainerID).Assign(block).FirstOrCreate(&block).Error; err != nil {
					return err
				}
			case ExecutionUpdated:
				// A successful recreate has a new container id. Remove the stale
				// pre-update scan row; the next inventory/scan will establish the
				// replacement's current state under its new id.
				if err := tx.Where("host = ? AND container_id = ?", run.Host, outcome.ContainerID).Delete(&UpdateScanResult{}).Error; err != nil {
					return err
				}
				if err := tx.Where("host = ? AND container_id = ?", run.Host, outcome.ContainerID).Delete(&UpdateExecutionBlock{}).Error; err != nil {
					return err
				}
			case ExecutionCurrent:
				if err := tx.Model(&UpdateScanResult{}).Where("host = ? AND container_id = ?", run.Host, outcome.ContainerID).Updates(map[string]any{
					"status": string(ContainerUpdateCurrent), "current_digest": outcome.RemoteDigest,
					"reason": "image became current before automatic execution",
				}).Error; err != nil {
					return err
				}
				if err := tx.Where("host = ? AND container_id = ?", run.Host, outcome.ContainerID).Delete(&UpdateExecutionBlock{}).Error; err != nil {
					return err
				}
			}
		}
		var stale []UpdateExecutionRun
		if err := tx.Where("host = ?", run.Host).Order("created_at DESC").Offset(maxExecutionRuns).Find(&stale).Error; err != nil {
			return err
		}
		if len(stale) == 0 {
			return nil
		}
		ids := make([]uint, 0, len(stale))
		for _, item := range stale {
			ids = append(ids, item.ID)
		}
		if err := tx.Where("run_id IN ?", ids).Delete(&UpdateExecutionResult{}).Error; err != nil {
			return err
		}
		return tx.Delete(&UpdateExecutionRun{}, ids).Error
	})
}

func (s *ScanStore) ExecutionState(host string) ([]UpdateExecutionRun, []UpdateExecutionResult, []UpdateExecutionBlock, error) {
	var runs []UpdateExecutionRun
	if err := s.db.Where("host = ?", host).Order("created_at DESC").Limit(maxExecutionRuns).Find(&runs).Error; err != nil {
		return nil, nil, nil, err
	}
	var results []UpdateExecutionResult
	if err := s.db.Where("host = ?", host).Order("created_at DESC").Limit(250).Find(&results).Error; err != nil {
		return nil, nil, nil, err
	}
	var blocks []UpdateExecutionBlock
	if err := s.db.Where("host = ?", host).Order("container_name").Find(&blocks).Error; err != nil {
		return nil, nil, nil, err
	}
	return runs, results, blocks, nil
}

func (s *ScanStore) ExecutionBlock(host, containerID, digest string) (UpdateExecutionBlock, bool, error) {
	var block UpdateExecutionBlock
	err := s.db.Where("host = ? AND container_id = ?", host, containerID).First(&block).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return block, false, nil
	}
	if err != nil {
		return block, false, err
	}
	return block, block.RemoteDigest == digest, nil
}

func (s *ScanStore) ClearExecutionBlock(host, containerID string) error {
	if strings.TrimSpace(host) == "" || strings.TrimSpace(containerID) == "" {
		return errors.New("host and container id are required")
	}
	var block UpdateExecutionBlock
	err := s.db.Where("host = ? AND container_id = ?", host, containerID).First(&block).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if block.TargetType == UpdateTargetStack && block.StackKey != "" {
		return s.db.Where("host = ? AND stack_key = ?", host, block.StackKey).Delete(&UpdateExecutionBlock{}).Error
	}
	return s.db.Where("host = ? AND container_id = ?", host, containerID).Delete(&UpdateExecutionBlock{}).Error
}

func boundedExecutionText(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxExecutionLogs {
		return value
	}
	return value[:maxExecutionLogs] + "\n[output truncated by Dockman]"
}
