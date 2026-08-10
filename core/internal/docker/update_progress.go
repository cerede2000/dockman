package docker

import (
	"io"

	v1 "github.com/RA341/dockman/generated/docker/v1"
	"github.com/RA341/dockman/internal/docker/updater"
)

// updateStages maps the updater's states onto the wire enum. An unlisted state
// arrives as UNSPECIFIED rather than being silently dropped, so a view can
// still show that something happened.
var updateStages = map[updater.Stage]v1.UpdateStage{
	updater.StageQueued:     v1.UpdateStage_UPDATE_STAGE_QUEUED,
	updater.StagePulling:    v1.UpdateStage_UPDATE_STAGE_PULLING,
	updater.StageRecreating: v1.UpdateStage_UPDATE_STAGE_RECREATING,
	updater.StageVerifying:  v1.UpdateStage_UPDATE_STAGE_VERIFYING,
	updater.StageUpToDate:   v1.UpdateStage_UPDATE_STAGE_UP_TO_DATE,
	updater.StageUpdated:    v1.UpdateStage_UPDATE_STAGE_UPDATED,
	updater.StageRolledBack: v1.UpdateStage_UPDATE_STAGE_ROLLED_BACK,
	updater.StageFailed:     v1.UpdateStage_UPDATE_STAGE_FAILED,
}

// updateProgressReporter forwards structured progress onto the same stream
// that carries the text, when that stream can take it. Any other writer - a
// buffer in a test, the deployment log of an automatic update - gets no
// reporter at all, which the updater treats as "do not report".
func updateProgressReporter(writer io.Writer) updater.ProgressReporter {
	stream, ok := writer.(*LogStreamWriter)
	if !ok {
		return nil
	}
	return func(progress updater.Progress) {
		stream.SendProgress(&v1.UpdateProgress{
			ContainerId:   progress.ContainerID,
			ContainerName: progress.ContainerName,
			Stack:         progress.Stack,
			Stage:         updateStages[progress.Stage],
			Detail:        progress.Detail,
		})
	}
}
