package docker

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/RA341/dockman/internal/docker/updater"
	"github.com/docker/docker/errdefs"
	"github.com/moby/moby/client"
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

type automaticUpdateUnit struct {
	key           string
	stackName     string
	transactional bool
	targets       []updater.UpdateExecutionTarget
}

// ExecuteAutomaticContainerUpdates keeps standalone/container policies
// isolated while executing every stack policy as one protected unit. A stack
// preloads all images before the first mutation, follows Compose dependency
// order and rolls already updated members back in reverse order when enabled.
func ExecuteAutomaticContainerUpdates(ctx context.Context, dkSrv *Service, targets []updater.UpdateExecutionTarget) []updater.UpdateExecutionOutcome {
	outcomes := make([]updater.UpdateExecutionOutcome, 0, len(targets))
	for _, unit := range groupAutomaticUpdateTargets(targets) {
		unitCtx, cancel := context.WithTimeout(ctx, time.Duration(max(20, len(unit.targets)*10))*time.Minute)
		if unit.transactional {
			outcomes = append(outcomes, executeAutomaticStackUnit(unitCtx, dkSrv, unit)...)
		} else {
			outcomes = append(outcomes, executeAutomaticContainerUnit(unitCtx, dkSrv, unit.targets[0]))
		}
		cancel()
	}
	return outcomes
}

// RemovePreviousImageIfUnused removes one exact rollback image without ever
// forcing Docker. Tagged images, images referenced by running or stopped
// containers, and parents needed by descendants are retained conservatively.
func RemovePreviousImageIfUnused(ctx context.Context, dkSrv *Service, imageID string) (bool, string, error) {
	imageID = strings.TrimSpace(imageID)
	if imageID == "" {
		return false, "empty previous image id", nil
	}
	filters := client.Filters{}
	filters.Add("ancestor", imageID)
	containers, err := dkSrv.Container.Cli().ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return false, "", fmt.Errorf("inspect previous image usage: %w", err)
	}
	if len(containers.Items) > 0 {
		return false, fmt.Sprintf("retained: still referenced by %d running or stopped container(s)", len(containers.Items)), nil
	}
	inspect, err := dkSrv.Container.Cli().ImageInspect(ctx, imageID)
	if errdefs.IsNotFound(err) {
		return true, "previous image was already absent", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("inspect previous image: %w", err)
	}
	for _, tag := range inspect.RepoTags {
		tag = strings.TrimSpace(tag)
		if tag != "" && tag != "<none>:<none>" {
			return false, "retained: image still has repository tag " + tag, nil
		}
	}
	if _, err := dkSrv.Container.Cli().ImageRemove(ctx, imageID, client.ImageRemoveOptions{}); err != nil {
		if errdefs.IsConflict(err) {
			return false, "retained by Docker because the image or one of its descendants is still referenced", nil
		}
		if errdefs.IsNotFound(err) {
			return true, "previous image was already absent", nil
		}
		return false, "", fmt.Errorf("remove previous image without force: %w", err)
	}
	return true, "previous image removed safely without force", nil
}

func executeAutomaticContainerUnit(ctx context.Context, dkSrv *Service, target updater.UpdateExecutionTarget) updater.UpdateExecutionOutcome {
	outcome := updater.UpdateExecutionOutcome{UpdateExecutionTarget: target, State: updater.ExecutionFailed}
	logs := &boundedUpdateWriter{}
	// A failure that happened before the container was touched is transient by
	// nature: an unreachable registry, a busy action lock, a listing that timed
	// out. Reporting those as failures armed the permanent execution block, so
	// a thirty-second network hiccup took a healthy container out of automatic
	// updates for good. Only a failure that reached the container itself is
	// worth blocking on.
	mutationAttempted := false
	err := withContainerUpdateLocks(ctx, dkSrv, []string{target.ContainerID}, func() error {
		if reason, err := validateAutomaticTarget(ctx, dkSrv, target); err != nil {
			return err
		} else if reason != "" {
			outcome.State, outcome.Message = updater.ExecutionSkipped, reason
			return nil
		}
		mutationAttempted = true
		result, updateErr := dkSrv.Updater.ForceUpdateContainer(ctx, func(pullCtx context.Context, imageTag string) error {
			return dkSrv.Compose.PullImage(pullCtx, imageTag, logs)
		}, logs, target.ContainerID, updater.ForceUpdateOptions{VerifyHealth: target.RollbackEnabled})
		outcome.PreviousImage = result.PreviousImage
		if updateErr == nil {
			if result.Updated {
				outcome.State, outcome.Message = updater.ExecutionUpdated, "container updated successfully"
			} else {
				outcome.State, outcome.Message = updater.ExecutionCurrent, "image became current before execution"
			}
		}
		return updateErr
	})
	if err != nil {
		outcome.Message = err.Error()
		switch {
		case updater.IsRolledBack(err):
			outcome.State = updater.ExecutionRolledBack
		case !mutationAttempted:
			outcome.State = updater.ExecutionSkipped
			outcome.Message = "update not attempted, will be retried: " + outcome.Message
		}
	}
	outcome.Logs = logs.String()
	return outcome
}

type appliedStackUpdate struct {
	index  int
	result updater.ForceUpdateResult
}

func executeAutomaticStackUnit(ctx context.Context, dkSrv *Service, unit automaticUpdateUnit) []updater.UpdateExecutionOutcome {
	targets := orderStackUpdateTargets(unit.targets)
	outcomes := make([]updater.UpdateExecutionOutcome, len(targets))
	ids := make([]string, 0, len(targets))
	for index, target := range targets {
		outcomes[index] = updater.UpdateExecutionOutcome{UpdateExecutionTarget: target, State: updater.ExecutionSkipped}
		ids = append(ids, target.ContainerID)
	}
	logs := &boundedUpdateWriter{}
	_, _ = fmt.Fprintf(logs, "Stack transaction %s: %d update target(s)\n", unit.stackName, len(targets))
	err := withContainerUpdateLocks(ctx, dkSrv, ids, func() error {
		for index, target := range targets {
			reason, validateErr := validateAutomaticTarget(ctx, dkSrv, target)
			if validateErr != nil {
				// Preflight: nothing has been changed yet, so this is retryable
				// rather than a reason to block the stack for good.
				outcomes[index].State, outcomes[index].Message = updater.ExecutionSkipped, "stack preflight validation failed, will be retried: "+validateErr.Error()
				markUnprocessedStackTargets(outcomes, 0, "stack transaction cancelled during preflight validation")
				return nil
			}
			if reason != "" {
				outcomes[index].State, outcomes[index].Message = updater.ExecutionSkipped, reason
				markUnprocessedStackTargets(outcomes, 0, "stack transaction cancelled because one member is no longer eligible")
				return nil
			}
		}

		pulled := make(map[string]struct{}, len(targets))
		for index, target := range targets {
			if _, ok := pulled[target.Image]; ok {
				continue
			}
			_, _ = fmt.Fprintf(logs, "Preloading %s...\n", target.Image)
			if pullErr := dkSrv.Compose.PullImage(ctx, target.Image, logs); pullErr != nil {
				// The pull runs before any container is touched, so a registry
				// outage must leave the stack retryable rather than blocked.
				outcomes[index].State = updater.ExecutionSkipped
				outcomes[index].Message = fmt.Sprintf("stack image preflight failed for %s, will be retried: %v", target.Image, pullErr)
				markUnprocessedStackTargets(outcomes, 0, "stack transaction cancelled before changing any container")
				return nil
			}
			pulled[target.Image] = struct{}{}
		}

		rollbackEnabled := true
		for _, target := range targets {
			rollbackEnabled = rollbackEnabled && target.RollbackEnabled
		}
		applied := make([]appliedStackUpdate, 0, len(targets))
		for index, target := range targets {
			result, updateErr := dkSrv.Updater.ForceUpdateContainer(ctx, func(context.Context, string) error { return nil }, logs, target.ContainerID, updater.ForceUpdateOptions{
				VerifyHealth: target.RollbackEnabled, ImagePrepared: true, ImageReference: target.Image,
			})
			outcomes[index].PreviousImage = result.PreviousImage
			if updateErr == nil {
				if result.Updated {
					outcomes[index].State = updater.ExecutionUpdated
					outcomes[index].Message = fmt.Sprintf("stack %s updated coherently", unit.stackName)
					applied = append(applied, appliedStackUpdate{index: index, result: result})
				} else {
					outcomes[index].State, outcomes[index].Message = updater.ExecutionCurrent, "image became current during stack execution"
				}
				continue
			}

			outcomes[index].State, outcomes[index].Message = updater.ExecutionFailed, updateErr.Error()
			if updater.IsRolledBack(updateErr) {
				outcomes[index].State = updater.ExecutionRolledBack
			}
			markUnprocessedStackTargets(outcomes, index+1, "stack transaction stopped after a member failed")
			if rollbackEnabled && len(applied) > 0 {
				rollbackAppliedStackTargets(ctx, dkSrv, unit, outcomes, applied, index, logs)
			} else if len(applied) == 0 {
				_, _ = fmt.Fprintln(logs, "No stack member was changed; rollback was not needed")
			} else if len(applied) > 0 {
				outcomes[index].Message += "; stack rollback is disabled, previously updated members were kept"
			}
			return nil
		}
		return nil
	})
	if err != nil {
		markUnprocessedStackTargets(outcomes, 0, "stack transaction could not acquire its action lock: "+err.Error())
		if len(outcomes) > 0 {
			// No container was touched: another action simply held the lock.
			outcomes[0].State, outcomes[0].Message = updater.ExecutionSkipped, "stack action lock unavailable, will be retried: "+err.Error()
		}
	}
	sharedLogs := logs.String()
	for index := range outcomes {
		outcomes[index].Logs = sharedLogs
	}
	return outcomes
}

func rollbackAppliedStackTargets(ctx context.Context, dkSrv *Service, unit automaticUpdateUnit, outcomes []updater.UpdateExecutionOutcome, applied []appliedStackUpdate, failedIndex int, logs *boundedUpdateWriter) {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(max(5, len(applied)*5))*time.Minute)
	defer cancel()
	_, _ = fmt.Fprintf(logs, "Rolling back %d previously updated stack member(s)...\n", len(applied))
	for index := len(applied) - 1; index >= 0; index-- {
		item := applied[index]
		restoredID, err := dkSrv.Updater.RestoreContainerImage(rollbackCtx, item.result.ContainerName, item.result.PreviousImage, true)
		if err != nil {
			outcomes[item.index].State = updater.ExecutionFailed
			outcomes[item.index].Message = fmt.Sprintf("stack rollback failed after %s failed: %v", outcomes[failedIndex].ContainerName, err)
			continue
		}
		outcomes[item.index].ContainerID = restoredID
		outcomes[item.index].State = updater.ExecutionRolledBack
		outcomes[item.index].Message = fmt.Sprintf("stack transaction rolled back after %s failed", outcomes[failedIndex].ContainerName)
	}
	_, _ = fmt.Fprintf(logs, "Stack %s rollback completed\n", unit.stackName)
}

func validateAutomaticTarget(ctx context.Context, dkSrv *Service, target updater.UpdateExecutionTarget) (string, error) {
	containers, err := dkSrv.Container.ContainerListByIDs(ctx, target.ContainerID)
	if err != nil {
		return "", err
	}
	if len(containers) != 1 {
		return "", fmt.Errorf("container %q no longer exists", target.ContainerName)
	}
	labels := containers[0].Labels
	if labels[updater.DockmanContainerLabel] == "true" {
		return "Dockman self-update is protected and requires its dedicated action", nil
	}
	if updater.HasDisableUpdateLabel(&containers[0]) {
		return "automatic update was disabled after the scan", nil
	}
	// Re-checked at execution time and not only at inventory time: the socket
	// may have been bound in since the scan, and this is the one target whose
	// update would take away the connection needed to roll it back.
	if _, optIn := labels[updater.DockmanOptInUpdateLabel]; !optIn && updater.ExposesDockerSocket(&containers[0]) {
		return "container exposes the Docker socket and is protected from automatic updates", nil
	}
	state := strings.ToLower(string(containers[0].State))
	if state == "paused" || state == "restarting" || state == "removing" || state == "dead" {
		return "container state changed after the scan: " + state, nil
	}
	return "", nil
}

func markUnprocessedStackTargets(outcomes []updater.UpdateExecutionOutcome, start int, message string) {
	for index := max(0, start); index < len(outcomes); index++ {
		if outcomes[index].State == updater.ExecutionSkipped && outcomes[index].Message == "" {
			outcomes[index].Message = message
		}
	}
}

func groupAutomaticUpdateTargets(targets []updater.UpdateExecutionTarget) []automaticUpdateUnit {
	units := make([]automaticUpdateUnit, 0, len(targets))
	stackIndexes := make(map[string]int)
	for _, target := range targets {
		if target.TargetType == updater.UpdateTargetStack && strings.TrimSpace(target.StackKey) != "" {
			if index, ok := stackIndexes[target.StackKey]; ok {
				units[index].targets = append(units[index].targets, target)
				continue
			}
			stackIndexes[target.StackKey] = len(units)
			units = append(units, automaticUpdateUnit{key: "stack:" + target.StackKey, stackName: target.StackName, transactional: true, targets: []updater.UpdateExecutionTarget{target}})
			continue
		}
		units = append(units, automaticUpdateUnit{key: "container:" + target.ContainerID, targets: []updater.UpdateExecutionTarget{target}})
	}
	slices.SortFunc(units, func(a, b automaticUpdateUnit) int { return strings.Compare(a.key, b.key) })
	return units
}

func orderStackUpdateTargets(targets []updater.UpdateExecutionTarget) []updater.UpdateExecutionTarget {
	remaining := append([]updater.UpdateExecutionTarget(nil), targets...)
	slices.SortFunc(remaining, func(a, b updater.UpdateExecutionTarget) int {
		return strings.Compare(strings.ToLower(a.ContainerName), strings.ToLower(b.ContainerName))
	})
	services := make(map[string]struct{}, len(remaining))
	for _, target := range remaining {
		if target.ServiceName != "" {
			services[target.ServiceName] = struct{}{}
		}
	}
	ordered := make([]updater.UpdateExecutionTarget, 0, len(remaining))
	done := make(map[string]struct{}, len(remaining))
	for len(remaining) > 0 {
		progress := false
		for index := 0; index < len(remaining); {
			dependencies := composeTargetDependencies(remaining[index].DependsOn, services)
			ready := true
			for _, dependency := range dependencies {
				if _, ok := done[dependency]; !ok {
					ready = false
					break
				}
			}
			if !ready {
				index++
				continue
			}
			target := remaining[index]
			ordered = append(ordered, target)
			if target.ServiceName != "" {
				done[target.ServiceName] = struct{}{}
			}
			remaining = append(remaining[:index], remaining[index+1:]...)
			progress = true
		}
		if !progress {
			// Cyclic or malformed metadata: deterministic order is safer than
			// dropping targets, and Compose would face the same invalid graph.
			ordered = append(ordered, remaining...)
			break
		}
	}
	return ordered
}

func composeTargetDependencies(value string, available map[string]struct{}) []string {
	var dependencies []string
	for _, item := range strings.Split(value, ",") {
		service := strings.TrimSpace(strings.SplitN(item, ":", 2)[0])
		if _, ok := available[service]; ok && !slices.Contains(dependencies, service) {
			dependencies = append(dependencies, service)
		}
	}
	return dependencies
}
