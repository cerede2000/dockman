package gitsync

import (
	"context"
	"errors"
	"fmt"
	"io"
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
