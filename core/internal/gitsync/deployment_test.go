package gitsync

import (
	"fmt"
	"strings"
	"testing"

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
