package updater

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	UpdateScheduleLabel = "dockman.update.schedule"
	UpdateRollbackLabel = "dockman.update.rollback"
	composeProjectLabel = "com.docker.compose.project"
	composeFilesLabel   = "com.docker.compose.project.config_files"
	composeServiceLabel = "com.docker.compose.service"
	composeDependsLabel = "com.docker.compose.depends_on"

	UpdateTargetContainer = "container"
	UpdateTargetStack     = "stack"
)

// UpdatePolicy is the persistent, host-scoped opt-in configuration consumed by
// scheduled scans and protected automatic update execution.
type UpdatePolicy struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	Host            string    `gorm:"not null;uniqueIndex:idx_update_policy_target" json:"host"`
	TargetType      string    `gorm:"not null;uniqueIndex:idx_update_policy_target" json:"targetType"`
	TargetKey       string    `gorm:"not null;uniqueIndex:idx_update_policy_target" json:"targetKey"`
	TargetName      string    `gorm:"not null" json:"targetName"`
	Enabled         bool      `gorm:"not null" json:"enabled"`
	Schedule        string    `gorm:"not null;default:''" json:"schedule"`
	RollbackEnabled bool      `gorm:"not null" json:"rollbackEnabled"`
}

type PolicyStore struct{ db *gorm.DB }

func NewPolicyStore(db *gorm.DB) *PolicyStore { return &PolicyStore{db: db} }

func (s *PolicyStore) List(host string) ([]UpdatePolicy, error) {
	var rows []UpdatePolicy
	err := s.db.Where("host = ?", host).Order("target_type, target_name").Find(&rows).Error
	return rows, err
}

func (s *PolicyStore) Upsert(policy *UpdatePolicy) error {
	return s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "host"}, {Name: "target_type"}, {Name: "target_key"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"target_name", "enabled", "schedule", "rollback_enabled", "updated_at",
		}),
	}).Create(policy).Error
}

func (s *PolicyStore) Delete(host, targetType, targetKey string) error {
	return s.db.Where("host = ? AND target_type = ? AND target_key = ?", host, targetType, targetKey).
		Delete(&UpdatePolicy{}).Error
}

type PolicyService struct{ store *PolicyStore }

func NewPolicyService(store *PolicyStore) *PolicyService { return &PolicyService{store: store} }

func (s *PolicyService) List(host string) ([]UpdatePolicy, error) { return s.store.List(host) }

func (s *PolicyService) Save(policy *UpdatePolicy) error {
	policy.Host = strings.TrimSpace(policy.Host)
	policy.TargetType = strings.TrimSpace(strings.ToLower(policy.TargetType))
	policy.TargetKey = strings.TrimSpace(policy.TargetKey)
	policy.TargetName = strings.TrimSpace(policy.TargetName)
	policy.Schedule = strings.TrimSpace(policy.Schedule)
	if policy.Host == "" || policy.TargetKey == "" || policy.TargetName == "" {
		return errors.New("host, target key and target name are required")
	}
	if len(policy.TargetKey) > 4096 || len(policy.TargetName) > 255 || len(policy.Schedule) > 255 {
		return errors.New("update policy fields exceed their allowed length")
	}
	if policy.TargetType != UpdateTargetContainer && policy.TargetType != UpdateTargetStack {
		return fmt.Errorf("unsupported update target type %q", policy.TargetType)
	}
	if policy.Schedule != "" {
		normalized, err := NormalizeUpdateSchedule(policy.Schedule)
		if err != nil {
			return fmt.Errorf("invalid image check schedule: %w", err)
		}
		policy.Schedule = normalized
	}
	return s.store.Upsert(policy)
}

func (s *PolicyService) Delete(host, targetType, targetKey string) error {
	if host == "" || targetKey == "" {
		return errors.New("host and target key are required")
	}
	if targetType != UpdateTargetContainer && targetType != UpdateTargetStack {
		return fmt.Errorf("unsupported update target type %q", targetType)
	}
	return s.store.Delete(host, targetType, targetKey)
}

type UpdateEnrollment struct {
	ContainerID    string `json:"containerId"`
	ContainerName  string `json:"containerName"`
	Image          string `json:"image"`
	State          string `json:"state"`
	StackName      string `json:"stackName,omitempty"`
	StackKey       string `json:"stackKey,omitempty"`
	ServiceName    string `json:"-"`
	DependsOn      string `json:"-"`
	Enrolled       bool   `json:"enrolled"`
	Source         string `json:"source"`
	Reason         string `json:"reason,omitempty"`
	Schedule       string `json:"schedule,omitempty"`
	ScheduleError  string `json:"scheduleError,omitempty"`
	Rollback       bool   `json:"rollback"`
	PolicyTarget   string `json:"policyTarget,omitempty"`
	PolicyTargetID string `json:"policyTargetId,omitempty"`
}

// Inventory resolves effective policy without contacting a registry. It is
// therefore cheap enough for an explicit page refresh and has no idle cost.
func (s *PolicyService) Inventory(ctx context.Context, host string, containers []container.Summary) ([]UpdateEnrollment, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	policies, err := s.store.List(host)
	if err != nil {
		return nil, err
	}
	byTarget := make(map[string]UpdatePolicy, len(policies))
	for _, policy := range policies {
		byTarget[policy.TargetType+"\x00"+policy.TargetKey] = policy
	}

	rows := make([]UpdateEnrollment, 0, len(containers))
	for _, item := range containers {
		name := summaryName(item)
		stackName, stackKey := stackIdentity(item.Labels)
		row := UpdateEnrollment{
			ContainerID: item.ID, ContainerName: name, Image: item.Image, State: string(item.State),
			StackName: stackName, StackKey: stackKey, ServiceName: strings.TrimSpace(item.Labels[composeServiceLabel]),
			DependsOn: strings.TrimSpace(item.Labels[composeDependsLabel]), Source: "none", Rollback: true,
		}

		if hasDockmanLabel(&item) {
			row.Source, row.Reason = "protected", "Dockman uses its dedicated self-update action"
			rows = append(rows, row)
			continue
		}
		if hasDisableUpdateLabel(&item) {
			row.Source, row.Reason = "disabled-label", DockmanUpdateDisableLabel+"=true"
			rows = append(rows, row)
			continue
		}
		if enabled, present := boolLabel(item.Labels, DockmanOptInUpdateLabel); present {
			if !enabled {
				row.Source, row.Reason = "disabled-label", DockmanOptInUpdateLabel+"=false"
				rows = append(rows, row)
				continue
			}
			row.Enrolled, row.Source = true, "label"
			row.Schedule = strings.TrimSpace(item.Labels[UpdateScheduleLabel])
			if _, err := NormalizeUpdateSchedule(row.Schedule); err != nil {
				row.ScheduleError = err.Error()
			}
			if rollback, ok := boolLabel(item.Labels, UpdateRollbackLabel); ok {
				row.Rollback = rollback
			}
			rows = append(rows, row)
			continue
		}

		if policy, ok := byTarget[UpdateTargetContainer+"\x00"+name]; ok {
			applyPolicy(&row, policy)
		} else if stackKey != "" {
			if policy, found := byTarget[UpdateTargetStack+"\x00"+stackKey]; found {
				applyPolicy(&row, policy)
			}
		}
		rows = append(rows, row)
	}
	slices.SortFunc(rows, func(a, b UpdateEnrollment) int {
		return strings.Compare(strings.ToLower(a.ContainerName), strings.ToLower(b.ContainerName))
	})
	return rows, nil
}

func applyPolicy(row *UpdateEnrollment, policy UpdatePolicy) {
	row.Enrolled = policy.Enabled
	row.Source = "interface"
	row.Schedule = policy.Schedule
	if _, err := NormalizeUpdateSchedule(row.Schedule); err != nil {
		row.ScheduleError = err.Error()
	}
	row.Rollback = policy.RollbackEnabled
	row.PolicyTarget = policy.TargetType
	row.PolicyTargetID = policy.TargetKey
	if !policy.Enabled {
		row.Reason = "disabled by interface policy"
	}
}

func stackIdentity(labels map[string]string) (string, string) {
	name := strings.TrimSpace(labels[composeProjectLabel])
	files := strings.TrimSpace(labels[composeFilesLabel])
	if name == "" {
		return "", ""
	}
	if files != "" {
		return name, name + "|" + files
	}
	return name, name
}

func boolLabel(labels map[string]string, key string) (bool, bool) {
	value, ok := labels[key]
	if !ok {
		return false, false
	}
	if strings.TrimSpace(value) == "" {
		return true, true
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, true
	}
	return parsed, true
}
