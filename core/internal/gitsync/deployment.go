package gitsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	maxDeploymentLogSize = 256 << 10
	maxNewStacksPerSync  = 10
)

type DeploymentView struct {
	ID          string    `json:"id"`
	CommitSHA   string    `json:"commitSha"`
	ComposePath string    `json:"composePath"`
	State       string    `json:"state"`
	Result      string    `json:"result,omitempty"`
	Logs        string    `json:"logs,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

type deploymentBatchResult struct {
	Deployed       []string
	Failed         []string
	RolledBack     []string
	RollbackFailed []string
}

func (s *Service) ListBindingDeployments(bindingID string) ([]DeploymentView, error) {
	if _, err := s.store.GetBinding(bindingID); err != nil {
		return nil, err
	}
	rows, err := s.store.ListDeployments(bindingID, 10)
	if err != nil {
		return nil, err
	}
	result := make([]DeploymentView, 0, len(rows))
	for _, row := range rows {
		result = append(result, DeploymentView{ID: row.UUID, CommitSHA: row.CommitSHA, ComposePath: row.ComposeHash, State: row.State, Result: row.Result, Logs: row.Logs, CreatedAt: row.CreatedAt})
	}
	return result, nil
}

type limitedLogWriter struct{ data []byte }

var deploymentANSISequence = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

func (w *limitedLogWriter) Write(p []byte) (int, error) {
	n := len(p)
	if len(w.data) < maxDeploymentLogSize {
		remaining := maxDeploymentLogSize - len(w.data)
		if len(p) > remaining {
			p = p[:remaining]
		}
		w.data = append(w.data, p...)
	}
	return n, nil
}

func (w *limitedLogWriter) String() string { return sanitizeDeploymentOutput(string(w.data)) }

func sanitizeDeploymentOutput(value string) string {
	value = deploymentANSISequence.ReplaceAllString(value, "")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' || character >= 0x20 {
			return character
		}
		return -1
	}, value)
	for strings.Contains(value, "\n\n\n") {
		value = strings.ReplaceAll(value, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(value)
}

func validateDeploymentTargets(binding StackBinding, enabled, allowNew bool, requested []string) ([]string, error) {
	if !enabled {
		return nil, nil
	}
	if !binding.AutoSyncEnabled {
		return nil, errors.New("automatic deployment requires automatic Git synchronization")
	}
	available := make(map[string]struct{})
	for _, path := range selectedComposePaths(binding) {
		available[path] = struct{}{}
	}
	seen := make(map[string]struct{})
	result := make([]string, 0, len(requested))
	for _, raw := range requested {
		path := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(raw))))
		if _, ok := available[path]; !ok {
			return nil, fmt.Errorf("automatic deployment target is not a discovered Compose file: %s", path)
		}
		if _, ok := seen[path]; !ok {
			seen[path] = struct{}{}
			result = append(result, path)
		}
	}
	if len(result) == 0 && !allowNew {
		return nil, errors.New("select at least one Compose file for automatic deployment")
	}
	sort.Strings(result)
	return result, nil
}

func changedPreviewPaths(preview TransferPreview) []string {
	paths := make([]string, 0, preview.Changed)
	for _, entry := range preview.Entries {
		if entry.Status == "add" || entry.Status == "modify" {
			paths = append(paths, entry.Path)
		}
	}
	return paths
}

func newComposeDeploymentTargets(binding StackBinding, preview TransferPreview) ([]string, error) {
	if !binding.AutoDeployEnabled || !binding.AutoDeployNewStacks {
		return nil, nil
	}
	known := make(map[string]struct{})
	for _, path := range splitPatternLines(binding.ComposePaths) {
		known[path] = struct{}{}
	}
	targets := make([]string, 0)
	directories := make(map[string]struct{})
	for _, entry := range preview.Entries {
		path := filepath.ToSlash(filepath.Clean(filepath.FromSlash(entry.Path)))
		if entry.Status != "add" || entry.Directory || !isComposeDeploymentFile(path) {
			continue
		}
		if _, exists := known[path]; exists {
			continue
		}
		known[path] = struct{}{}
		targets = append(targets, path)
		directories[filepath.ToSlash(filepath.Dir(filepath.FromSlash(path)))] = struct{}{}
	}
	if len(directories) > maxNewStacksPerSync {
		return nil, fmt.Errorf("automatic deployment refused: one synchronization may add at most %d new stacks", maxNewStacksPerSync)
	}
	sort.Strings(targets)
	return targets, nil
}

func isComposeDeploymentFile(path string) bool {
	switch strings.ToLower(filepath.Base(filepath.FromSlash(path))) {
	case "compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml":
		return true
	default:
		return false
	}
}

func (s *Service) registerDiscoveredDeploymentTargets(binding StackBinding, targets []string) (StackBinding, error) {
	binding.ComposePaths = strings.Join(uniqueSortedStrings(append(splitPatternLines(binding.ComposePaths), targets...)), "\n")
	if normalizedComposeSelectionMode(binding.ComposeSelectionMode) == composeSelectionSelected {
		binding.SelectedComposePaths = strings.Join(uniqueSortedStrings(append(splitPatternLines(binding.SelectedComposePaths), targets...)), "\n")
	}
	binding.AutoDeployComposePaths = strings.Join(uniqueSortedStrings(append(splitPatternLines(binding.AutoDeployComposePaths), targets...)), "\n")
	binding.AutoDeployState = "pending"
	binding.AutoDeployError = "New Git stack discovered; waiting for controlled deployment"
	if err := s.store.SaveBinding(&binding); err != nil {
		return binding, err
	}
	if err := s.reconcileGitStackStatuses(binding); err != nil {
		return binding, err
	}
	return binding, nil
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func deploymentTargetsForChanges(binding StackBinding, changed []string) []string {
	result := make([]string, 0)
	for _, compose := range splitPatternLines(binding.AutoDeployComposePaths) {
		directory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(compose)))
		for _, path := range changed {
			clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
			if clean == compose || directory == "." || strings.HasPrefix(clean, directory+"/") {
				result = append(result, compose)
				break
			}
		}
	}
	sort.Strings(result)
	return result
}

func (s *Service) deployChangedStacks(ctx context.Context, binding StackBinding, commit string, changed []string, backupID ...string) (deploymentBatchResult, error) {
	if s.validateCompose == nil || s.dryRunCompose == nil || s.deployCompose == nil || s.lockCompose == nil {
		return deploymentBatchResult{}, errors.New("automatic deployment is not configured")
	}
	if binding.AutoDeployRollbackEnabled && (s.deployComposeWait == nil || s.cleanupCompose == nil) {
		return deploymentBatchResult{}, errors.New("automatic deployment rollback health check is not configured")
	}
	targets := deploymentTargetsForChanges(binding, changed)
	if len(targets) == 0 {
		return deploymentBatchResult{}, nil
	}
	result := deploymentBatchResult{Deployed: make([]string, 0, len(targets)), Failed: make([]string, 0), RolledBack: make([]string, 0), RollbackFailed: make([]string, 0)}
	rollbackBackupID := ""
	if len(backupID) > 0 {
		rollbackBackupID = backupID[0]
	}
	for _, relative := range targets {
		filename := filepath.ToSlash(filepath.Join(binding.StackPath, relative))
		unlock, locked := s.lockCompose(binding.Host, filename)
		if !locked {
			message := fmt.Sprintf("stack %s already has an action in progress", filename)
			result.Failed = append(result.Failed, relative)
			_ = s.store.UpdateGitStackStatuses(binding.UUID, []string{relative}, map[string]any{"deploy_state": "failed", "deploy_error": message})
			continue
		}
		logs := &limitedLogWriter{}
		deployment := Deployment{UUID: uuid.NewString(), RepositoryUUID: binding.RepositoryUUID, BindingUUID: binding.UUID, CommitSHA: commit, ComposeHash: relative, State: "validating"}
		if err := s.store.SaveDeployment(&deployment); err != nil {
			unlock()
			return result, err
		}
		_ = s.store.UpdateGitStackStatuses(binding.UUID, []string{relative}, map[string]any{"deploy_state": "validating", "deploy_error": ""})
		stage := "validation"
		err := s.validateCompose(ctx, binding.Host, filename)
		if err == nil {
			stage = "dry-run"
			deployment.State = "dry_run"
			err = s.dryRunCompose(ctx, binding.Host, filename, logs)
		}
		if err == nil {
			stage = "deployment"
			deployment.State = "deploying"
			deploy := s.deployCompose
			if binding.AutoDeployRollbackEnabled {
				deploy = s.deployComposeWait
			}
			err = deploy(ctx, binding.Host, filename, logs)
		}
		if err != nil && binding.AutoDeployRollbackEnabled {
			originalErr := sanitizeDeploymentOutput(safeGitError(fmt.Errorf("%s failed: %w", stage, err)))
			deployment.State = "rolling_back"
			_, _ = fmt.Fprintf(logs, "\n[dockman] %s; restoring the pre-import stack files\n", originalErr)
			hadPreviousCompose, rollbackErr := s.deploymentHadPreviousCompose(binding, rollbackBackupID, relative)
			if rollbackErr == nil && stage == "deployment" && !hadPreviousCompose {
				_, _ = fmt.Fprintln(logs, "[dockman] first deployment failed; removing its partial containers and networks before restoring the absent stack")
				rollbackErr = s.cleanupCompose(ctx, binding.Host, filename, logs)
			}
			var restored []string
			if rollbackErr == nil {
				restored, rollbackErr = s.rollbackDeploymentFiles(binding, rollbackBackupID, relative)
			}
			if rollbackErr == nil && hadPreviousCompose {
				rollbackErr = s.validateCompose(ctx, binding.Host, filename)
			}
			// Validation/dry-run failures never touched Docker. A real deployment
			// (including --wait/health failure) is rolled forward to the restored
			// configuration so partially changed containers are repaired.
			if rollbackErr == nil && stage == "deployment" && hadPreviousCompose {
				_, _ = fmt.Fprintln(logs, "[dockman] restored files validated; checking the previous deployment plan")
				rollbackErr = s.dryRunCompose(ctx, binding.Host, filename, logs)
			}
			if rollbackErr == nil && stage == "deployment" && hadPreviousCompose {
				_, _ = fmt.Fprintln(logs, "[dockman] redeploying the previous stack version and waiting for health")
				rollbackErr = s.deployComposeWait(ctx, binding.Host, filename, logs)
			}
			if rollbackErr == nil {
				deployment.State = "rolled_back"
				deployment.Result = fmt.Sprintf("%s; previous version restored safely (%d file(s))", originalErr, len(restored))
				result.RolledBack = append(result.RolledBack, relative)
			} else {
				deployment.State = "rollback_failed"
				deployment.Result = fmt.Sprintf("%s; automatic rollback failed: %s", originalErr, sanitizeDeploymentOutput(safeGitError(rollbackErr)))
				result.RollbackFailed = append(result.RollbackFailed, relative)
			}
		}
		unlock()
		deployment.Logs = logs.String()
		if err == nil {
			deployment.Result = "deployed"
			deployment.State = "success"
		} else if deployment.State != "rolled_back" && deployment.State != "rollback_failed" {
			deployment.State = "failed"
			deployment.Result = sanitizeDeploymentOutput(safeGitError(fmt.Errorf("%s failed: %w", stage, err)))
		}
		if saveErr := s.store.SaveDeployment(&deployment); saveErr != nil {
			return result, saveErr
		}
		now := time.Now().UTC()
		deployError := ""
		if deployment.State != "success" {
			deployError = deployment.Result
		}
		_ = s.store.UpdateGitStackStatuses(binding.UUID, []string{relative}, map[string]any{"deploy_state": deployment.State, "deploy_error": deployError, "last_deploy_at": &now})
		s.recordActivity(ActivityRecord{RepositoryID: binding.RepositoryUUID, BindingID: binding.UUID,
			ComposePath: relative, Type: "stack_deploy", Trigger: "automation", State: deployment.State,
			CommitSHA: commit, Error: deployError, Details: ActivityDetails{Action: stage, Paths: []string{relative}, DeploymentIDs: []string{deployment.UUID}}})
		if deployment.State != "success" {
			result.Failed = append(result.Failed, relative)
			continue
		}
		result.Deployed = append(result.Deployed, relative)
	}
	now := time.Now().UTC()
	state, message := "success", "deployed"
	if len(result.Failed) > 0 {
		state = "failed"
		if len(result.Deployed) > 0 || len(result.RolledBack) > 0 {
			state = "partial"
		}
		message = fmt.Sprintf("%d stack(s) deployed; %d stack(s) failed", len(result.Deployed), len(result.Failed))
		if len(result.RolledBack) > 0 {
			message += fmt.Sprintf(" (%d restored automatically)", len(result.RolledBack))
		}
		if len(result.RollbackFailed) > 0 {
			state = "failed"
			message += fmt.Sprintf("; %d rollback(s) failed", len(result.RollbackFailed))
		}
		if len(result.Failed) > 0 {
			message += ": " + strings.Join(result.Failed, ", ")
		}
	} else if len(result.Deployed) > 0 {
		message = fmt.Sprintf("%d stack(s) deployed", len(result.Deployed))
	}
	if err := s.store.UpdateBindingAutoDeployState(binding.UUID, state, message, &now); err != nil {
		return result, err
	}
	return result, nil
}

var _ io.Writer = (*limitedLogWriter)(nil)
