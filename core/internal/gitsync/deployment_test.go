package gitsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestValidateDeploymentTargets(t *testing.T) {
	binding := StackBinding{AutoSyncEnabled: true, ComposePaths: "alpha/compose.yml\nbeta/compose.yaml"}
	got, err := validateDeploymentTargets(binding, true, false, []string{"beta/compose.yaml", "alpha/compose.yml", "alpha/compose.yml"})
	require.NoError(t, err)
	require.Equal(t, []string{"alpha/compose.yml", "beta/compose.yaml"}, got)
	_, err = validateDeploymentTargets(binding, true, false, []string{"unknown/compose.yml"})
	require.ErrorContains(t, err, "not a discovered Compose file")
	got, err = validateDeploymentTargets(binding, true, true, nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestNewComposeDeploymentTargets(t *testing.T) {
	binding := StackBinding{AutoDeployEnabled: true, AutoDeployNewStacks: true, ComposePaths: "existing/compose.yml"}
	preview := TransferPreview{Entries: []PreviewEntry{
		{Path: "new/compose.yml", Status: "add"},
		{Path: "other/docker-compose.yaml", Status: "add"},
		{Path: "modified/compose.yml", Status: "modify"},
		{Path: "new/readme.md", Status: "add"},
	}}
	got, err := newComposeDeploymentTargets(binding, preview)
	require.NoError(t, err)
	require.Equal(t, []string{"new/compose.yml", "other/docker-compose.yaml"}, got)
}

func TestNewComposeDeploymentTargetsAreBounded(t *testing.T) {
	binding := StackBinding{AutoDeployEnabled: true, AutoDeployNewStacks: true}
	preview := TransferPreview{}
	for i := 0; i <= maxNewStacksPerSync; i++ {
		preview.Entries = append(preview.Entries, PreviewEntry{Path: fmt.Sprintf("stack-%02d/compose.yml", i), Status: "add"})
	}
	_, err := newComposeDeploymentTargets(binding, preview)
	require.ErrorContains(t, err, "at most 10")
}

func TestDeploymentTargetsOnlyIncludeAffectedStacks(t *testing.T) {
	binding := StackBinding{AutoDeployComposePaths: "alpha/compose.yml\nbeta/compose.yaml"}
	require.Equal(t, []string{"alpha/compose.yml"}, deploymentTargetsForChanges(binding, []string{"alpha/config/app.yml"}))
	require.Empty(t, deploymentTargetsForChanges(binding, []string{"docs/readme.md"}))
}

func TestLimitedDeploymentLogWriter(t *testing.T) {
	w := &limitedLogWriter{}
	payload := strings.Repeat("x", maxDeploymentLogSize+1024)
	n, err := w.Write([]byte(payload))
	require.NoError(t, err)
	require.Equal(t, len(payload), n)
	require.Len(t, w.String(), maxDeploymentLogSize)
}

func TestDeploymentOutputRemovesTerminalControlSequences(t *testing.T) {
	raw := "\x1b[?25l\x1b[0G[+] up 0/1 \x1b[33m⠋\x1b[0m recreate\r\x1b[?25hfinal error"
	clean := sanitizeDeploymentOutput(raw)
	require.NotContains(t, clean, "\x1b")
	require.Contains(t, clean, "[+] up 0/1")
	require.Contains(t, clean, "final error")
}

func TestDeployChangedStacksContinuesAfterIndependentFailure(t *testing.T) {
	service, _ := testService(t, true)
	binding := StackBinding{
		UUID: uuid.NewString(), RepositoryUUID: uuid.NewString(), Host: "local", StackPath: "compose",
		AutoSyncEnabled: true, AutoDeployEnabled: true,
		ComposePaths:           "broken/compose.yml\nnew-stack/compose.yml",
		AutoDeployComposePaths: "broken/compose.yml\nnew-stack/compose.yml",
	}
	require.NoError(t, service.store.SaveBinding(&binding))
	require.NoError(t, service.store.ReconcileGitStackStatuses(binding, splitPatternLines(binding.ComposePaths), stackSyncPending))
	var actions []string
	service.ConfigureDeployment(
		func(_ context.Context, _, filename string) error {
			actions = append(actions, "validate:"+filename)
			if strings.Contains(filename, "broken/") {
				return errors.New("invalid compose")
			}
			return nil
		},
		func(_ context.Context, _, filename string, _ io.Writer) error {
			actions = append(actions, "dry-run:"+filename)
			return nil
		},
		func(_ context.Context, _, filename string, _ io.Writer) error {
			actions = append(actions, "deploy:"+filename)
			return nil
		},
		func(_ context.Context, _, filename string, _ io.Writer) error {
			actions = append(actions, "deploy-wait:"+filename)
			return nil
		},
		func(_ context.Context, _, filename string, _ io.Writer) error {
			actions = append(actions, "cleanup:"+filename)
			return nil
		},
		func(_, _ string) (func(), bool) { return func() {}, true },
	)

	result, err := service.deployChangedStacks(context.Background(), binding, "commit", []string{
		"broken/compose.yml", "new-stack/compose.yml",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"broken/compose.yml"}, result.Failed)
	require.Equal(t, []string{"new-stack/compose.yml"}, result.Deployed)
	require.Equal(t, []string{
		"validate:compose/broken/compose.yml",
		"validate:compose/new-stack/compose.yml",
		"dry-run:compose/new-stack/compose.yml",
		"deploy:compose/new-stack/compose.yml",
	}, actions)

	updated, err := service.store.GetBinding(binding.UUID)
	require.NoError(t, err)
	require.Equal(t, "partial", updated.AutoDeployState)
	require.Contains(t, updated.AutoDeployError, "1 stack(s) deployed; 1 stack(s) failed")
	statuses, err := service.store.GitStackStatuses(binding.UUID)
	require.NoError(t, err)
	require.Len(t, statuses, 2)
	require.Equal(t, "failed", statuses[0].DeployState)
	require.Equal(t, "success", statuses[1].DeployState)
}

func TestDeployChangedStacksRollsBackProvisioningMetadata(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	remoteChange(t, repository.RemoteURL, "stacks/app/compose.yml", "services:\n  app:\n    image: alpine:3.23\n")
	remoteChange(t, repository.RemoteURL, "stacks/app/provision.yml", "version: 1\ndirectories:\n  - path: data\n    mode: \"0750\"\npermissions:\n  - path: config.yml\n    mode: \"0600\"\n")
	_, err := service.FetchRepository(context.Background(), repository.UUID)
	require.NoError(t, err)
	status, err := service.PullRepository(context.Background(), repository.UUID)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yml"), []byte("services: {}\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "config.yml"), []byte("ok\n"), 0644))
	binding := StackBinding{UUID: uuid.NewString(), RepositoryUUID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks",
		AutoSyncEnabled: true, AutoDeployEnabled: true, AutoDeployRollbackEnabled: true,
		ComposePaths: "app/compose.yml", AutoDeployComposePaths: "app/compose.yml"}
	require.NoError(t, service.store.SaveBinding(&binding))
	require.NoError(t, service.store.ReconcileGitStackStatuses(binding, splitPatternLines(binding.ComposePaths), stackSyncPending))
	validations := 0
	service.ConfigureDeployment(
		func(_ context.Context, _, _ string) error {
			validations++
			if validations == 1 {
				info, statErr := os.Stat(filepath.Join(stackRoot, "app", "config.yml"))
				require.NoError(t, statErr)
				require.Equal(t, os.FileMode(0600), info.Mode().Perm())
				return errors.New("invalid compose")
			}
			return nil
		},
		func(context.Context, string, string, io.Writer) error { return nil },
		func(context.Context, string, string, io.Writer) error { return nil },
		func(context.Context, string, string, io.Writer) error { return nil },
		func(context.Context, string, string, io.Writer) error { return nil },
		func(_, _ string) (func(), bool) { return func() {}, true },
	)
	result, err := service.deployChangedStacks(context.Background(), binding, status.Head, []string{"app/provision.yml"})
	require.NoError(t, err)
	require.Equal(t, []string{"app/compose.yml"}, result.RolledBack)
	info, err := os.Stat(filepath.Join(stackRoot, "app", "config.yml"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0644), info.Mode().Perm())
	require.NoDirExists(t, filepath.Join(stackRoot, "app", "data"))
}
