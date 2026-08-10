package updater

import (
	"strings"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
)

// Stage is where one container stands in its update.
type Stage string

const (
	StageQueued     Stage = "queued"
	StagePulling    Stage = "pulling"
	StageRecreating Stage = "recreating"
	StageVerifying  Stage = "verifying"
	// Terminal stages.
	StageUpToDate   Stage = "up-to-date"
	StageUpdated    Stage = "updated"
	StageRolledBack Stage = "rolled-back"
	StageFailed     Stage = "failed"
)

// Progress is one state change for one container.
type Progress struct {
	ContainerID   string
	ContainerName string
	Stack         string
	Stage         Stage
	Detail        string
}

// ProgressReporter receives update progress.
//
// Implementations MUST be safe for concurrent use: stacks update in parallel
// and every one of them reports on the same stream.
type ProgressReporter func(Progress)

// reportStage sends one stage for a container, tolerating a caller that asked
// for no reporting at all - which is every path other than the streaming
// update action.
func reportStage(report ProgressReporter, cur container.Summary, stage Stage, detail string) {
	if report == nil {
		return
	}
	report(Progress{
		ContainerID:   cur.ID,
		ContainerName: summaryName(cur),
		Stack:         strings.TrimSpace(cur.Labels[api.ProjectLabel]),
		Stage:         stage,
		Detail:        detail,
	})
}
