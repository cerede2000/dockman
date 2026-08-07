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
	UpdateScheduleLabel    = "dockman.update.schedule"
	UpdateRollbackLabel    = "dockman.update.rollback"
	UpdateCleanupLabel     = "dockman.update.cleanup"
	UpdateCleanupKeepLabel = "dockman.update.cleanup.keep"
	UpdateVersionLabel     = "dockman.update.version"
	UpdatePrereleaseLabel  = "dockman.update.version.prerelease"
	composeProjectLabel    = "com.docker.compose.project"
	composeFilesLabel      = "com.docker.compose.project.config_files"
	composeServiceLabel    = "com.docker.compose.service"
	composeDependsLabel    = "com.docker.compose.depends_on"

	UpdateTargetContainer = "container"
	UpdateTargetStack     = "stack"
	VersionPolicyOff      = "off"
	VersionPolicyPatch    = "patch"
	VersionPolicyMinor    = "minor"
	VersionPolicyMajor    = "major"
)

// UpdatePolicy is the persistent, host-scoped opt-in configuration consumed by
// scheduled scans and protected automatic update execution.
type UpdatePolicy struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
	Host              string    `gorm:"not null;uniqueIndex:idx_update_policy_target" json:"host"`
	TargetType        string    `gorm:"not null;uniqueIndex:idx_update_policy_target" json:"targetType"`
	TargetKey         string    `gorm:"not null;uniqueIndex:idx_update_policy_target" json:"targetKey"`
	TargetName        string    `gorm:"not null" json:"targetName"`
	Enabled           bool      `gorm:"not null" json:"enabled"`
	Schedule          string    `gorm:"not null;default:''" json:"schedule"`
	RollbackEnabled   bool      `gorm:"not null" json:"rollbackEnabled"`
	CleanupEnabled    bool      `gorm:"not null;default:false" json:"cleanupEnabled"`
	CleanupKeep       int       `gorm:"not null" json:"cleanupKeep"`
	VersionPolicy     string    `gorm:"not null;default:'off'" json:"versionPolicy"`
	VersionPrerelease bool      `gorm:"not null;default:false" json:"versionPrerelease"`
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
			"target_name", "enabled", "schedule", "rollback_enabled", "cleanup_enabled", "cleanup_keep", "version_policy", "version_prerelease", "updated_at",
		}),
	}).Create(policy).Error
}

func (s *PolicyStore) Delete(host, targetType, targetKey string) error {
	return s.db.Where("host = ? AND target_type = ? AND target_key = ?", host, targetType, targetKey).
		Delete(&UpdatePolicy{}).Error
}

type PolicyTargetRef struct {
	TargetType string `json:"targetType"`
	TargetKey  string `json:"targetKey"`
}

func (s *PolicyStore) ApplyMany(host string, policies []UpdatePolicy, removals []PolicyTargetRef) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		store := PolicyStore{db: tx}
		for _, removal := range removals {
			if err := store.Delete(host, removal.TargetType, removal.TargetKey); err != nil {
				return err
			}
		}
		for index := range policies {
			if err := store.Upsert(&policies[index]); err != nil {
				return err
			}
		}
		return nil
	})
}

type PolicyService struct{ store *PolicyStore }

func NewPolicyService(store *PolicyStore) *PolicyService { return &PolicyService{store: store} }

func (s *PolicyService) List(host string) ([]UpdatePolicy, error) { return s.store.List(host) }

func (s *PolicyService) Save(policy *UpdatePolicy) error {
	if err := validatePolicy(policy); err != nil {
		return err
	}
	return s.store.Upsert(policy)
}

func (s *PolicyService) SaveMany(policies []UpdatePolicy, removals []PolicyTargetRef) error {
	if len(policies) == 0 || len(policies) > 500 {
		return errors.New("bulk policy update must contain between 1 and 500 targets")
	}
	seen := make(map[string]struct{}, len(policies))
	host := ""
	for index := range policies {
		if err := validatePolicy(&policies[index]); err != nil {
			return fmt.Errorf("policy %d: %w", index+1, err)
		}
		if index == 0 {
			host = policies[index].Host
		} else if policies[index].Host != host {
			return errors.New("all bulk policy targets must belong to the same Docker host")
		}
		key := policies[index].Host + "\x00" + policies[index].TargetType + "\x00" + policies[index].TargetKey
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("policy %d duplicates target %q", index+1, policies[index].TargetName)
		}
		seen[key] = struct{}{}
	}
	if len(removals) > 500 {
		return errors.New("bulk policy update cannot remove more than 500 previous overrides")
	}
	for index := range removals {
		removals[index].TargetType = strings.TrimSpace(removals[index].TargetType)
		removals[index].TargetKey = strings.TrimSpace(removals[index].TargetKey)
		if removals[index].TargetKey == "" || (removals[index].TargetType != UpdateTargetContainer && removals[index].TargetType != UpdateTargetStack) {
			return fmt.Errorf("removal %d is invalid", index+1)
		}
	}
	return s.store.ApplyMany(host, policies, removals)
}

func validatePolicy(policy *UpdatePolicy) error {
	if policy == nil {
		return errors.New("update policy is required")
	}
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
	if policy.CleanupKeep < 0 || policy.CleanupKeep > 10 {
		return errors.New("cleanup retention must be between 0 and 10 previous images")
	}
	policy.VersionPolicy = strings.ToLower(strings.TrimSpace(policy.VersionPolicy))
	if policy.VersionPolicy == "" {
		policy.VersionPolicy = VersionPolicyOff
	}
	if !validVersionPolicy(policy.VersionPolicy) {
		return errors.New("version discovery policy must be off, patch, minor or major")
	}
	if policy.Schedule != "" {
		normalized, err := NormalizeUpdateSchedule(policy.Schedule)
		if err != nil {
			return fmt.Errorf("invalid image check schedule: %w", err)
		}
		policy.Schedule = normalized
	}
	return nil
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
	ContainerID       string `json:"containerId"`
	ContainerName     string `json:"containerName"`
	Image             string `json:"image"`
	State             string `json:"state"`
	StackName         string `json:"stackName,omitempty"`
	StackKey          string `json:"stackKey,omitempty"`
	ServiceName       string `json:"-"`
	DependsOn         string `json:"-"`
	Enrolled          bool   `json:"enrolled"`
	Source            string `json:"source"`
	Reason            string `json:"reason,omitempty"`
	Schedule          string `json:"schedule,omitempty"`
	ScheduleError     string `json:"scheduleError,omitempty"`
	Rollback          bool   `json:"rollback"`
	CleanupEnabled    bool   `json:"cleanupEnabled"`
	CleanupKeep       int    `json:"cleanupKeep"`
	VersionPolicy     string `json:"versionPolicy"`
	VersionPrerelease bool   `json:"versionPrerelease"`
	PolicyTarget      string `json:"policyTarget,omitempty"`
	PolicyTargetID    string `json:"policyTargetId,omitempty"`
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
			DependsOn: strings.TrimSpace(item.Labels[composeDependsLabel]), Source: "none", Rollback: true, CleanupKeep: 1, VersionPolicy: VersionPolicyOff,
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
			if cleanup, ok := boolLabel(item.Labels, UpdateCleanupLabel); ok {
				row.CleanupEnabled = cleanup
			}
			if keep := strings.TrimSpace(item.Labels[UpdateCleanupKeepLabel]); row.CleanupEnabled && keep != "" {
				parsed, parseErr := strconv.Atoi(keep)
				if parseErr != nil || parsed < 0 || parsed > 10 {
					row.CleanupEnabled = false
					row.Reason = "safe cleanup disabled: invalid " + UpdateCleanupKeepLabel + ", use an integer between 0 and 10"
				} else {
					row.CleanupKeep = parsed
				}
			}
			if policy := strings.ToLower(strings.TrimSpace(item.Labels[UpdateVersionLabel])); policy != "" {
				if validVersionPolicy(policy) {
					row.VersionPolicy = policy
				} else {
					row.Reason = "version discovery disabled: invalid " + UpdateVersionLabel
				}
			}
			if prerelease, ok := boolLabel(item.Labels, UpdatePrereleaseLabel); ok {
				row.VersionPrerelease = prerelease
			}
			rows = append(rows, row)
			continue
		}

		// Deliberately below every explicit label: an operator who set
		// dockman.update on this container has already decided, and keeps the
		// final say. Without such a label, a container holding the daemon
		// socket is protected, because updating it would cut the connection
		// carrying the update itself.
		if ExposesDockerSocket(&item) {
			row.Source, row.Reason = "protected", "exposes the Docker socket; an automatic update would sever the daemon connection mid-operation. Set "+DockmanOptInUpdateLabel+"=true to override"
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
	row.CleanupEnabled = policy.CleanupEnabled
	row.CleanupKeep = policy.CleanupKeep
	row.VersionPolicy = policy.VersionPolicy
	row.VersionPrerelease = policy.VersionPrerelease
	row.PolicyTarget = policy.TargetType
	row.PolicyTargetID = policy.TargetKey
	if !policy.Enabled {
		row.Reason = "disabled by interface policy"
	}
}

func validVersionPolicy(value string) bool {
	return value == VersionPolicyOff || value == VersionPolicyPatch || value == VersionPolicyMinor || value == VersionPolicyMajor
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
