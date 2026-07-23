package gitsync

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	stackSyncPending       = "pending"
	stackSyncUpToDate      = "up_to_date"
	stackSyncChecking      = "checking"
	stackSyncLocalChanges  = "local_changes"
	stackSyncRemoteChanges = "remote_changes"
	stackSyncConflict      = "conflict"
	stackSyncError         = "error"
)

type GitStackStatusView struct {
	BindingID         string     `json:"bindingId"`
	Host              string     `json:"host"`
	StackPath         string     `json:"stackPath"`
	ComposePath       string     `json:"composePath"`
	FullComposePath   string     `json:"fullComposePath"`
	RepositoryID      string     `json:"repositoryId"`
	RepositoryName    string     `json:"repositoryName"`
	RepositoryBranch  string     `json:"repositoryBranch"`
	RepositorySubPath string     `json:"repositorySubPath"`
	State             string     `json:"state"`
	Error             string     `json:"error,omitempty"`
	ConflictCount     int        `json:"conflictCount"`
	AutoSyncEnabled   bool       `json:"autoSyncEnabled"`
	AutomationPaused  bool       `json:"automationPaused"`
	AutoDeployEnabled bool       `json:"autoDeployEnabled"`
	AutoSyncInterval  int        `json:"autoSyncIntervalMinutes"`
	LastCheckedAt     *time.Time `json:"lastCheckedAt,omitempty"`
	LastSuccessAt     *time.Time `json:"lastSuccessAt,omitempty"`
	NextCheckAt       *time.Time `json:"nextCheckAt,omitempty"`
	LastCommit        string     `json:"lastCommit,omitempty"`
	DeployState       string     `json:"deployState"`
	DeployError       string     `json:"deployError,omitempty"`
	LastDeployAt      *time.Time `json:"lastDeployAt,omitempty"`
}

type GitStackPauseInput struct {
	Paused bool `json:"paused"`
}

func initialStackSyncState(binding StackBinding) string {
	switch binding.InitialSyncState {
	case "reconciled", "imported", "exported":
		return stackSyncUpToDate
	case "error":
		return stackSyncError
	default:
		return stackSyncPending
	}
}

func (s *Service) reconcileGitStackStatuses(binding StackBinding) error {
	return s.store.ReconcileGitStackStatuses(binding, selectedComposePaths(binding), initialStackSyncState(binding))
}

func (s *Service) InitializeGitStackStatuses() error {
	if !s.enabled {
		return nil
	}
	bindings, err := s.store.ListBindings()
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if err := s.reconcileGitStackStatuses(binding); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ListGitStackStatusViews(host string) ([]GitStackStatusView, error) {
	host = strings.TrimSpace(host)
	rows, err := s.store.ListGitStackStatuses(host)
	if err != nil {
		return nil, err
	}
	bindings, err := s.store.ListBindingsForHost(host)
	if err != nil {
		return nil, err
	}
	bindingByID := make(map[string]StackBinding, len(bindings))
	repositoryIDs := make(map[string]struct{})
	for _, binding := range bindings {
		if host == "" || binding.Host == host {
			bindingByID[binding.UUID] = binding
			repositoryIDs[binding.RepositoryUUID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(repositoryIDs))
	for id := range repositoryIDs {
		ids = append(ids, id)
	}
	repositories, err := s.store.RepositoriesByIDs(ids)
	if err != nil {
		return nil, err
	}
	repositoryByID := make(map[string]Repository, len(repositories))
	for _, repository := range repositories {
		repositoryByID[repository.UUID] = repository
	}
	result := make([]GitStackStatusView, 0, len(rows))
	for _, row := range rows {
		binding, ok := bindingByID[row.BindingUUID]
		if !ok {
			continue
		}
		repository := repositoryByID[binding.RepositoryUUID]
		state := row.State
		if row.AutomationPaused && state == stackSyncChecking {
			state = stackSyncPending
		}
		deployEnabled := binding.AutoDeployEnabled && stringInSlice(row.ComposePath, splitPatternLines(binding.AutoDeployComposePaths))
		deployState := row.DeployState
		if !deployEnabled {
			deployState = "disabled"
		} else if deployState == "" || deployState == "disabled" {
			deployState = "idle"
		}
		var nextCheck *time.Time
		if binding.AutoSyncEnabled && !row.AutomationPaused {
			base := time.Now().UTC()
			if binding.LastAutoSyncAt != nil {
				base = *binding.LastAutoSyncAt
			}
			next := base.Add(time.Duration(normalizedAutoSyncInterval(binding.AutoSyncIntervalMinutes)) * time.Minute)
			nextCheck = &next
		}
		result = append(result, GitStackStatusView{
			BindingID: binding.UUID, Host: binding.Host, StackPath: binding.StackPath,
			ComposePath: row.ComposePath, FullComposePath: filepath.ToSlash(filepath.Join(binding.StackPath, row.ComposePath)),
			RepositoryID: repository.UUID, RepositoryName: repository.Name, RepositoryBranch: repository.DefaultBranch,
			RepositorySubPath: filepath.ToSlash(filepath.Join(binding.SubPath, row.ComposePath)),
			State:             state, Error: row.ErrorMessage, ConflictCount: row.ConflictCount,
			AutoSyncEnabled: binding.AutoSyncEnabled, AutomationPaused: row.AutomationPaused,
			AutoDeployEnabled: deployEnabled, AutoSyncInterval: normalizedAutoSyncInterval(binding.AutoSyncIntervalMinutes),
			LastCheckedAt: row.LastCheckedAt, LastSuccessAt: row.LastSuccessAt, NextCheckAt: nextCheck,
			LastCommit: row.LastCommit, DeployState: deployState, DeployError: row.DeployError, LastDeployAt: row.LastDeployAt,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Host != result[j].Host {
			return result[i].Host < result[j].Host
		}
		return result[i].FullComposePath < result[j].FullComposePath
	})
	return result, nil
}

func (s *Service) SetGitStackAutomationPause(bindingID, composePath string, paused bool) (GitStackStatusView, error) {
	automationLock := s.repositoryLock("automation:" + bindingID)
	if !automationLock.TryLock() {
		return GitStackStatusView{}, errors.New("automatic synchronization is currently running; retry when it finishes")
	}
	defer automationLock.Unlock()

	composePath = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(composePath))))
	if err := validateRelativePath(composePath, false); err != nil {
		return GitStackStatusView{}, fmt.Errorf("invalid Compose path: %w", err)
	}
	binding, err := s.store.GetBinding(bindingID)
	if err != nil {
		return GitStackStatusView{}, err
	}
	if !stringInSlice(composePath, selectedComposePaths(binding)) {
		return GitStackStatusView{}, errors.New("stack is not selected for Git synchronization")
	}
	if err := s.reconcileGitStackStatuses(binding); err != nil {
		return GitStackStatusView{}, err
	}
	if err := s.store.SetGitStackPause(bindingID, composePath, paused); err != nil {
		return GitStackStatusView{}, err
	}
	views, err := s.ListGitStackStatusViews(binding.Host)
	if err != nil {
		return GitStackStatusView{}, err
	}
	for _, view := range views {
		if view.BindingID == bindingID && view.ComposePath == composePath {
			return view, nil
		}
	}
	return GitStackStatusView{}, gorm.ErrRecordNotFound
}

func (s *Service) recordPreviewStackStatuses(binding StackBinding, preview TransferPreview) {
	paths := selectedComposePaths(binding)
	if preview.automation {
		paths = s.activeAutomationComposePaths(binding)
	}
	now := time.Now().UTC()
	type aggregate struct {
		state     string
		conflicts int
	}
	states := make(map[string]aggregate, len(paths))
	for _, composePath := range paths {
		states[composePath] = aggregate{state: stackSyncUpToDate}
	}
	for _, entry := range preview.Entries {
		for _, composePath := range composePathsForFile(paths, entry.Path) {
			current := states[composePath]
			switch entry.Status {
			case "conflict":
				current.state = stackSyncConflict
				current.conflicts++
			case "add", "modify":
				if current.state != stackSyncConflict {
					if preview.Direction == "stack_to_repository" {
						current.state = stackSyncLocalChanges
					} else {
						current.state = stackSyncRemoteChanges
					}
				}
			}
			states[composePath] = current
		}
	}
	for composePath, state := range states {
		updates := map[string]any{"state": state.state, "error_message": "", "conflict_count": state.conflicts, "last_checked_at": &now}
		if state.state == stackSyncUpToDate {
			updates["last_success_at"] = &now
		}
		_ = s.store.UpdateGitStackStatuses(binding.UUID, []string{composePath}, updates)
	}
}

func (s *Service) updateActiveStackStatuses(binding StackBinding, state, message, commit string, success bool) {
	paths := s.activeAutomationComposePaths(binding)
	now := time.Now().UTC()
	updates := map[string]any{"state": state, "error_message": message, "last_checked_at": &now}
	if state != stackSyncConflict {
		updates["conflict_count"] = 0
	}
	if success {
		updates["last_success_at"] = &now
		updates["last_commit"] = commit
	}
	_ = s.store.UpdateGitStackStatuses(binding.UUID, paths, updates)
}

func (s *Service) updateActiveStackStatusesPreservingLocal(binding StackBinding, state, message, commit string, success bool) {
	paths := s.activeAutomationComposePaths(binding)
	now := time.Now().UTC()
	updates := map[string]any{"state": state, "error_message": message, "last_checked_at": &now}
	if state != stackSyncConflict {
		updates["conflict_count"] = 0
	}
	if success {
		updates["last_success_at"] = &now
		updates["last_commit"] = commit
	}
	_ = s.store.UpdateGitStackStatusesExcept(binding.UUID, paths, []string{stackSyncLocalChanges}, updates)
}

func (s *Service) activeAutomationComposePaths(binding StackBinding) []string {
	selected := selectedComposePaths(binding)
	paused, err := s.store.PausedComposePaths(binding.UUID)
	if err != nil || len(paused) == 0 {
		return selected
	}
	pausedSet := make(map[string]struct{}, len(paused))
	for _, path := range paused {
		pausedSet[path] = struct{}{}
	}
	active := make([]string, 0, len(selected))
	for _, path := range selected {
		if _, skip := pausedSet[path]; !skip {
			active = append(active, path)
		}
	}
	return active
}

func composePathsForFile(composePaths []string, filePath string) []string {
	filePath = strings.Trim(filepath.ToSlash(filePath), "/")
	bestDepth := -1
	result := make([]string, 0, 1)
	for _, composePath := range composePaths {
		root := strings.Trim(filepath.ToSlash(filepath.Dir(filepath.FromSlash(composePath))), "/")
		if root == "." {
			root = ""
		}
		if root == "" || filePath == root || strings.HasPrefix(filePath, root+"/") {
			depth := strings.Count(root, "/")
			if root != "" {
				depth++
			}
			if depth > bestDepth {
				bestDepth = depth
				result = result[:0]
			}
			if depth == bestDepth {
				result = append(result, composePath)
			}
		}
	}
	return result
}

func stringInSlice(value string, values []string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// MarkLocalChange is called after successful user-driven file mutations. It
// performs only path comparisons and compact DB updates; it never reads files.
func (s *Service) MarkLocalChange(host, changedPath string) {
	if !s.enabled {
		return
	}
	changedPath = strings.Trim(filepath.ToSlash(changedPath), "/")
	bindings, err := s.store.ListBindingsForHost(host)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, binding := range bindings {
		prefix := strings.Trim(filepath.ToSlash(binding.StackPath), "/")
		if changedPath != prefix && !strings.HasPrefix(changedPath, prefix+"/") {
			continue
		}
		relative := strings.TrimPrefix(strings.TrimPrefix(changedPath, prefix), "/")
		for _, composePath := range composePathsForFile(selectedComposePaths(binding), relative) {
			_ = s.store.UpdateGitStackStatuses(binding.UUID, []string{composePath}, map[string]any{
				"state": stackSyncLocalChanges, "error_message": "", "conflict_count": 0, "last_checked_at": &now,
			})
		}
	}
}
