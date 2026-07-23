package gitsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const maxDeploymentLogSize = 256 << 10

type DeploymentView struct {
	ID          string    `json:"id"`
	CommitSHA   string    `json:"commitSha"`
	ComposePath string    `json:"composePath"`
	State       string    `json:"state"`
	Result      string    `json:"result,omitempty"`
	Logs        string    `json:"logs,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
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

func (w *limitedLogWriter) String() string { return string(w.data) }

func validateDeploymentTargets(binding StackBinding, enabled bool, requested []string) ([]string, error) {
	if !enabled {
		return nil, nil
	}
	if !binding.AutoSyncEnabled {
		return nil, errors.New("automatic deployment requires automatic Git synchronization")
	}
	available := make(map[string]struct{})
	for _, path := range splitPatternLines(binding.ComposePaths) {
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
	if len(result) == 0 {
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

func (s *Service) deployChangedStacks(ctx context.Context, binding StackBinding, commit string, changed []string) ([]string, error) {
	if s.validateCompose == nil || s.dryRunCompose == nil || s.deployCompose == nil || s.lockCompose == nil {
		return nil, errors.New("automatic deployment is not configured")
	}
	targets := deploymentTargetsForChanges(binding, changed)
	if len(targets) == 0 {
		return nil, nil
	}
	deployed := make([]string, 0, len(targets))
	for _, relative := range targets {
		filename := filepath.ToSlash(filepath.Join(binding.StackPath, relative))
		unlock, locked := s.lockCompose(binding.Host, filename)
		if !locked {
			return deployed, fmt.Errorf("stack %s already has an action in progress", filename)
		}
		logs := &limitedLogWriter{}
		deployment := Deployment{UUID: uuid.NewString(), RepositoryUUID: binding.RepositoryUUID, BindingUUID: binding.UUID, CommitSHA: commit, ComposeHash: relative, State: "validating"}
		if err := s.store.SaveDeployment(&deployment); err != nil {
			unlock()
			return deployed, err
		}
		err := s.validateCompose(ctx, binding.Host, filename)
		if err == nil {
			deployment.State = "dry_run"
			err = s.dryRunCompose(ctx, binding.Host, filename, logs)
		}
		if err == nil {
			deployment.State = "deploying"
			err = s.deployCompose(ctx, binding.Host, filename, logs)
		}
		unlock()
		deployment.Logs = logs.String()
		deployment.Result = "deployed"
		deployment.State = "success"
		if err != nil {
			deployment.State, deployment.Result = "failed", safeGitError(err)
		}
		if saveErr := s.store.SaveDeployment(&deployment); saveErr != nil && err == nil {
			err = saveErr
		}
		now := time.Now().UTC()
		_ = s.store.UpdateBindingAutoDeployState(binding.UUID, deployment.State, deployment.Result, &now)
		if err != nil {
			return deployed, fmt.Errorf("deploy %s: %w", relative, err)
		}
		deployed = append(deployed, relative)
	}
	return deployed, nil
}

var _ io.Writer = (*limitedLogWriter)(nil)
