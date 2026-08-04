package docker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/RA341/dockman/internal/docker/updater"
)

const automaticUpdateLogLimit = 32 << 10

type boundedUpdateWriter struct {
	data []byte
}

func (w *boundedUpdateWriter) Write(p []byte) (int, error) {
	original := len(p)
	remaining := automaticUpdateLogLimit - len(w.data)
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		w.data = append(w.data, p...)
	}
	return original, nil
}

func (w *boundedUpdateWriter) String() string {
	value := strings.TrimSpace(string(w.data))
	if len(w.data) == automaticUpdateLogLimit {
		value += "\n[output truncated by Dockman]"
	}
	return value
}

// ExecuteAutomaticContainerUpdates reuses the exact pull/recreate path and
// stack locks used by the manual Monitor action. Targets are deliberately
// processed independently so one broken image cannot prevent unrelated
// updates in the same schedule.
func ExecuteAutomaticContainerUpdates(ctx context.Context, dkSrv *Service, targets []updater.UpdateExecutionTarget) []updater.UpdateExecutionOutcome {
	outcomes := make([]updater.UpdateExecutionOutcome, 0, len(targets))
	for _, target := range targets {
		outcome := updater.UpdateExecutionOutcome{UpdateExecutionTarget: target, State: updater.ExecutionFailed}
		logs := &boundedUpdateWriter{}
		targetCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
		err := withContainerUpdateLocks(targetCtx, dkSrv, []string{target.ContainerID}, func() error {
			containers, listErr := dkSrv.Container.ContainerListByIDs(targetCtx, target.ContainerID)
			if listErr != nil {
				return listErr
			}
			if len(containers) != 1 {
				return fmt.Errorf("container %q no longer exists", target.ContainerName)
			}
			labels := containers[0].Labels
			if labels[updater.DockmanContainerLabel] == "true" {
				outcome.State, outcome.Message = updater.ExecutionSkipped, "Dockman self-update is protected and requires its dedicated action"
				return nil
			}
			if labels[updater.DockmanUpdateDisableLabel] == "true" {
				outcome.State, outcome.Message = updater.ExecutionSkipped, "automatic update was disabled after the scan"
				return nil
			}
			state := strings.ToLower(string(containers[0].State))
			if state == "paused" || state == "restarting" || state == "removing" || state == "dead" {
				outcome.State, outcome.Message = updater.ExecutionSkipped, "container state changed after the scan: "+state
				return nil
			}
			result, updateErr := dkSrv.Updater.ForceUpdateContainer(
				targetCtx,
				func(pullCtx context.Context, imageTag string) error {
					return dkSrv.Compose.PullImage(pullCtx, imageTag, logs)
				},
				logs,
				target.ContainerID,
				updater.ForceUpdateOptions{VerifyHealth: target.RollbackEnabled},
			)
			if updateErr == nil {
				if result.Updated {
					outcome.State, outcome.Message = updater.ExecutionUpdated, "container updated successfully"
				} else {
					outcome.State, outcome.Message = updater.ExecutionCurrent, "image became current before execution"
				}
			}
			return updateErr
		})
		cancel()
		if err != nil {
			outcome.Message = err.Error()
			if updater.IsRolledBack(err) {
				outcome.State = updater.ExecutionRolledBack
			}
		}
		outcome.Logs = logs.String()
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}
