package docker

import (
	"bytes"
	"testing"

	v1 "github.com/RA341/dockman/generated/docker/v1"
	"github.com/RA341/dockman/internal/docker/updater"

	"github.com/stretchr/testify/require"
)

// Every stage the updater can report must have a wire value. A missing entry
// maps to UNSPECIFIED, which the view renders as the generic "Updating" - the
// row would silently go back to saying nothing useful for that phase.
func TestEveryUpdateStageHasAWireValue(t *testing.T) {
	stages := []updater.Stage{
		updater.StageQueued,
		updater.StagePulling,
		updater.StageRecreating,
		updater.StageVerifying,
		updater.StageUpToDate,
		updater.StageUpdated,
		updater.StageRolledBack,
		updater.StageFailed,
	}
	seen := make(map[v1.UpdateStage]updater.Stage, len(stages))
	for _, stage := range stages {
		wire, mapped := updateStages[stage]
		require.True(t, mapped, "stage %q has no wire value", stage)
		require.NotEqual(t, v1.UpdateStage_UPDATE_STAGE_UNSPECIFIED, wire, "stage %q maps to UNSPECIFIED", stage)
		require.NotContains(t, seen, wire, "stage %q reuses the value of %q", stage, seen[wire])
		seen[wire] = stage
	}
}

// The reporter exists only for the streaming update action. Every other caller
// passes a plain writer - a buffer in a test, a deployment log - and must get
// no reporter rather than a panic on a type assertion.
func TestUpdateProgressReporterIsAbsentWithoutAStream(t *testing.T) {
	require.Nil(t, updateProgressReporter(new(bytes.Buffer)))
	require.NotNil(t, updateProgressReporter(&LogStreamWriter{}))
}
