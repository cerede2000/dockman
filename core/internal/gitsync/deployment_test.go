package gitsync

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateDeploymentTargets(t *testing.T) {
	binding := StackBinding{AutoSyncEnabled: true, ComposePaths: "alpha/compose.yml\nbeta/compose.yaml"}
	got, err := validateDeploymentTargets(binding, true, []string{"beta/compose.yaml", "alpha/compose.yml", "alpha/compose.yml"})
	require.NoError(t, err)
	require.Equal(t, []string{"alpha/compose.yml", "beta/compose.yaml"}, got)
	_, err = validateDeploymentTargets(binding, true, []string{"unknown/compose.yml"})
	require.ErrorContains(t, err, "not a discovered Compose file")
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
