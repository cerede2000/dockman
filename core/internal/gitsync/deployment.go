package gitsync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	maxDeploymentLogSize = 256 << 10
	maxNewStacksPerSync  = 10
)

type DeploymentView struct {
	ID          string    `json:"id"`
	CommitSHA   string    `json:"commitSha"`
	ComposePath string    `json:"composePath"`
	State       string    `json:"state"`
	Result      string    `json:"result,omitempty"`
	Logs        string    `json:"logs,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

type deploymentBatchResult struct {
	Deployed       []string
	Failed         []string
	RolledBack     []string
	RollbackFailed []string
	// Deferred stacks were busy with another action and were not attempted.
	// They are NOT failures: nothing was tried, nothing broke, and the next
	// cycle will pick them up.
	Deferred []string
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
		result = append(result, DeploymentView{ID: row.UUID, CommitSHA: row.CommitSHA, ComposePath: row.ComposeHash, State: row.State,
			Result: sanitizeDeploymentOutput(row.Result), Logs: sanitizeDeploymentOutput(row.Logs), CreatedAt: row.CreatedAt})
	}
	return result, nil
}

type limitedLogWriter struct{ data []byte }

var deploymentANSISequence = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)

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

func (w *limitedLogWriter) String() string { return sanitizeDeploymentOutput(string(w.data)) }

func sanitizeDeploymentOutput(value string) string {
	value = deploymentANSISequence.ReplaceAllString(value, "")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' || character >= 0x20 {
			return character
		}
		return -1
	}, value)
	for strings.Contains(value, "\n\n\n") {
		value = strings.ReplaceAll(value, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(value)
}

func validateDeploymentTargets(binding StackBinding, enabled, allowNew bool, requested []string) ([]string, error) {
	if !enabled {
		return nil, nil
	}
	if !binding.AutoSyncEnabled {
		return nil, errors.New("automatic deployment requires automatic Git synchronization")
	}
	available := make(map[string]struct{})
	for _, path := range selectedComposePaths(binding) {
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
	if len(result) == 0 && !allowNew {
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

// newComposeDeploymentTargets returns the Compose files this synchronization
// must authorize for automatic deployment. Two shapes, one rule: a stack
// Dockman has never deployed itself, whose Compose file this synchronization
// brings in or changes.
//
//   - a Compose file that is not tracked by the link yet: a new Git stack.
//   - a Compose file the link already tracks but that was never an authorized
//     deployment target. That is the state of every stack imported by hand
//     before automation was switched on, which is the ordinary way a link
//     starts. Discovery only ever saw such a stack as an "add" during that
//     manual import, so it was never authorized and would synchronize forever
//     without deploying - a service added to it from Git never came up.
//
// Authorizing is driven by the Compose file itself, never by any other changed
// file in the stack: a FIRST authorization must follow an explicit change to
// what the stack runs, not a README landing beside it. Once authorized the
// stack follows the normal rule and redeploys whenever any of its files change.
//
// An already-tracked stack is only authorized when automatic synchronization
// actually covers it: a stack excluded from polling, or paused, stays out.
func newComposeDeploymentTargets(binding StackBinding, preview TransferPreview, automated []string) ([]string, error) {
	if !binding.AutoDeployEnabled || !binding.AutoDeployNewStacks {
		return nil, nil
	}
	tracked := make(map[string]struct{})
	for _, path := range splitPatternLines(binding.ComposePaths) {
		tracked[path] = struct{}{}
	}
	covered := make(map[string]struct{}, len(automated))
	for _, path := range automated {
		covered[path] = struct{}{}
	}
	authorized := make(map[string]struct{})
	for _, path := range splitPatternLines(binding.AutoDeployComposePaths) {
		authorized[path] = struct{}{}
	}
	targets := make([]string, 0)
	directories := make(map[string]struct{})
	for _, entry := range preview.Entries {
		path := filepath.ToSlash(filepath.Clean(filepath.FromSlash(entry.Path)))
		if entry.Directory || !isComposeDeploymentFile(path) {
			continue
		}
		if entry.Status != "add" && entry.Status != "modify" {
			continue
		}
		if _, exists := authorized[path]; exists {
			continue
		}
		if _, exists := tracked[path]; exists {
			if _, inScope := covered[path]; !inScope {
				continue
			}
		} else if entry.Status != "add" {
			continue
		}
		authorized[path] = struct{}{}
		targets = append(targets, path)
		directories[filepath.ToSlash(filepath.Dir(filepath.FromSlash(path)))] = struct{}{}
	}
	if len(directories) > maxNewStacksPerSync {
		return nil, fmt.Errorf("automatic deployment refused: one synchronization may authorize at most %d stacks", maxNewStacksPerSync)
	}
	sort.Strings(targets)
	return targets, nil
}

// unauthorizedDeploymentStacks lists the stacks this synchronization changed
// that automatic synchronization covers but automatic deployment is not
// authorized to touch. Saying nothing at all is how a link stays green for
// weeks while the running containers drift away from the files on disk.
func unauthorizedDeploymentStacks(binding StackBinding, changed, automated []string) []string {
	if !binding.AutoDeployEnabled {
		return nil
	}
	authorized := make(map[string]struct{})
	for _, path := range splitPatternLines(binding.AutoDeployComposePaths) {
		authorized[path] = struct{}{}
	}
	result := make([]string, 0)
	for _, path := range composePathsForFiles(automated, changed) {
		if _, ok := authorized[path]; !ok {
			result = append(result, path)
		}
	}
	return uniqueSortedStrings(result)
}

func isComposeDeploymentFile(path string) bool {
	switch strings.ToLower(filepath.Base(filepath.FromSlash(path))) {
	case "compose.yml", "compose.yaml", "docker-compose.yml", "docker-compose.yaml":
		return true
	default:
		return false
	}
}

func (s *Service) registerDiscoveredDeploymentTargets(binding StackBinding, targets []string) (StackBinding, error) {
	binding.ComposePaths = strings.Join(uniqueSortedStrings(append(splitPatternLines(binding.ComposePaths), targets...)), "\n")
	if normalizedComposeSelectionMode(binding.ComposeSelectionMode) == composeSelectionSelected {
		binding.SelectedComposePaths = strings.Join(uniqueSortedStrings(append(splitPatternLines(binding.SelectedComposePaths), targets...)), "\n")
	}
	if normalizedComposeSelectionMode(binding.AutoSyncSelectionMode) == composeSelectionSelected {
		binding.AutoSyncComposePaths = strings.Join(uniqueSortedStrings(append(splitPatternLines(binding.AutoSyncComposePaths), targets...)), "\n")
	}
	binding.AutoDeployComposePaths = strings.Join(uniqueSortedStrings(append(splitPatternLines(binding.AutoDeployComposePaths), targets...)), "\n")
	binding.AutoDeployState = "pending"
	binding.AutoDeployError = "Git stack authorized for automatic deployment; waiting for controlled deployment"
	ownershipLock := s.repositoryLock("binding-ownership:" + binding.Host)
	ownershipLock.Lock()
	defer ownershipLock.Unlock()
	if err := s.validateBindingOwnership(binding, binding.UUID); err != nil {
		return binding, fmt.Errorf("new Git stack would overlap another Folder Link: %w", err)
	}
	if err := s.store.SaveBinding(&binding); err != nil {
		return binding, err
	}
	if err := s.reconcileGitStackStatuses(binding); err != nil {
		return binding, err
	}
	return binding, nil
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; !ok {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

// terminalDeploymentStates are the values that describe a run that is OVER.
// Everything else is a stage a run passes through, and a stage found at rest
// means the process stopped inside it.
//
// Stated this way round on purpose. Restart recovery used to enumerate the
// transient states instead, and it drifted twice: "provisioning" and
// "rolling_back" were added to the deployment path and never added here, so a
// stop inside either survived every restart. Listing what is FINISHED means a
// stage added tomorrow is repaired by default rather than forgotten.
// rollbackBudget bounds the detached rollback. It is deliberately generous:
// restoring a stack pulls images and waits for health, and giving up half way
// is worse than taking a few minutes.
const rollbackBudget = 15 * time.Minute

var terminalDeploymentStates = map[string]struct{}{
	"success": {}, "failed": {}, "rolled_back": {}, "rollback_failed": {},
}

// terminalStackDeployStates is the same set for the per-stack report, plus the
// three values that mean "no run to speak of".
var terminalStackDeployStates = map[string]struct{}{
	"": {}, "idle": {}, "disabled": {},
	"success": {}, "failed": {}, "rolled_back": {}, "rollback_failed": {},
}

func terminalStateList(set map[string]struct{}) []string {
	states := make([]string, 0, len(set))
	for state := range set {
		states = append(states, state)
	}
	sort.Strings(states)
	return states
}

// isRetryableAutoDeployState reports a recorded automatic-deployment state
// that the next cycle is expected to re-attempt. It exists so the fast path
// that skips the stack scan and the block that performs the retry cannot
// drift apart again: they were, and the retry became unreachable.
func isRetryableAutoDeployState(state string) bool {
	return state == "failed" || state == "partial" || state == "pending"
}

// stacksAwaitingDeployRetry lists the controlled stacks whose OWN last
// deployment did not succeed.
//
// The retry used to re-arm EVERY authorized deployment path, so one stack
// failing meant every stack was redeployed on the next check. A single
// cancelled request then failed all of them at once - which is how one broken
// deployment turned into a folder link where every stack had gone red.
//
// A stack that deployed cleanly has nothing to retry. Deliberately not applied
// to the "pending" state: that one means imported changes are still waiting
// for their first deployment, and no per-stack failure records which. Narrowing
// it there would silently drop a deployment that was genuinely owed.
func (s *Service) stacksAwaitingDeployRetry(binding StackBinding) []string {
	authorized := splitPatternLines(binding.AutoDeployComposePaths)
	if len(authorized) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(authorized))
	for _, path := range authorized {
		allowed[path] = struct{}{}
	}
	rows, err := s.store.GitStackStatuses(binding.UUID)
	if err != nil {
		// Without the per-stack picture, keep the old broad behaviour rather
		// than risk dropping a retry that is owed.
		return authorized
	}
	pending := make([]string, 0, len(authorized))
	for _, row := range rows {
		if _, ok := allowed[row.ComposePath]; !ok {
			continue
		}
		if _, finished := terminalStackDeployStates[row.DeployState]; finished && row.DeployState != "failed" && row.DeployState != "rolled_back" && row.DeployState != "rollback_failed" {
			continue
		}
		pending = append(pending, row.ComposePath)
	}
	return uniqueSortedStrings(pending)
}

func deploymentTargetsForChanges(binding StackBinding, changed []string) []string {
	targets := splitPatternLines(binding.AutoDeployComposePaths)
	result := make([]string, 0)
	for _, path := range changed {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
		result = append(result, composePathsForFile(targets, clean)...)
	}
	return uniqueSortedStrings(result)
}

func (s *Service) deployChangedStacks(ctx context.Context, binding StackBinding, commit string, changed []string, backupID ...string) (deploymentBatchResult, error) {
	if s.validateCompose == nil || s.dryRunCompose == nil || s.deployCompose == nil || s.lockCompose == nil {
		return deploymentBatchResult{}, errors.New("automatic deployment is not configured")
	}
	if binding.AutoDeployRollbackEnabled && (s.deployComposeWait == nil || s.cleanupCompose == nil) {
		return deploymentBatchResult{}, errors.New("automatic deployment rollback health check is not configured")
	}
	targets := deploymentTargetsForChanges(binding, changed)
	if len(targets) == 0 {
		return deploymentBatchResult{}, nil
	}
	result := deploymentBatchResult{Deployed: make([]string, 0, len(targets)), Failed: make([]string, 0), RolledBack: make([]string, 0), RollbackFailed: make([]string, 0)}
	rollbackBackupID := ""
	if len(backupID) > 0 {
		rollbackBackupID = backupID[0]
	}
	for _, relative := range targets {
		filename := filepath.ToSlash(filepath.Join(binding.StackPath, relative))
		unlock, locked := s.lockCompose(binding.Host, filename)
		if !locked {
			// Busy is not broken. A stack being deployed by hand, updated by
			// the auto-updater, or touched by another cycle was recorded here
			// as a FAILED deployment: it painted the stack red, pushed the
			// whole link into a failed state, and - with the old broad retry -
			// dragged every other stack into a redeployment behind it. Nothing
			// was attempted, so nothing is reported as having gone wrong.
			result.Deferred = append(result.Deferred, relative)
			s.newDeployTracer(relative, commit, nil).note("deferred: another action already holds the lock on %s", filename)
			continue
		}
		logs := &limitedLogWriter{}
		trace := s.newDeployTracer(relative, commit, logs)
		trace.note("start: rollback=%t host=%s file=%s", binding.AutoDeployRollbackEnabled, binding.Host, filename)
		deployment := Deployment{UUID: uuid.NewString(), RepositoryUUID: binding.RepositoryUUID, BindingUUID: binding.UUID, CommitSHA: commit, ComposeHash: relative, State: "validating"}
		if err := s.store.SaveDeployment(&deployment); err != nil {
			unlock()
			return result, err
		}
		_ = s.store.UpdateGitStackStatuses(binding.UUID, []string{relative}, map[string]any{"deploy_state": "validating", "deploy_error": ""})
		stage := "provisioning"
		deployment.State = "provisioning"
		_ = s.store.UpdateGitStackStatuses(binding.UUID, []string{relative}, map[string]any{"deploy_state": "provisioning", "deploy_error": ""})
		stageStart := time.Now()
		provisioning, err := s.applyStackProvisioning(ctx, binding, commit, relative, logs)
		trace.stage(ctx, "provisioning", stageStart, err)
		provisionRolledBack := false
		if err == nil {
			stage = "validation"
			deployment.State = "validating"
			stageStart = time.Now()
			err = s.validateCompose(ctx, binding.Host, filename)
			trace.stage(ctx, "validation", stageStart, err)
		}
		if err == nil && s.pullCompose != nil {
			// The images have to be here BEFORE anything is dry-run against
			// them. Compose only SIMULATES a pull in dry-run mode: it prints
			// "Pulling" then "Pulled" and downloads nothing, so the recreate
			// step right after asks the daemon for an image that was never
			// fetched and gets "No such image".
			//
			// That made every Git commit which bumps an image tag fail its
			// dry-run and roll back - the deployment itself was never even
			// attempted, and it would have worked, because a real `compose up`
			// pulls what it is missing. A stack whose image was already local
			// passed, which is why this looked random.
			stage = "pull"
			deployment.State = "pulling"
			stageStart = time.Now()
			err = s.pullCompose(ctx, binding.Host, filename, logs)
			trace.stage(ctx, "pull", stageStart, err)
		}
		if err == nil {
			stage = "dry-run"
			deployment.State = "dry_run"
			stageStart = time.Now()
			err = s.dryRunCompose(ctx, binding.Host, filename, logs)
			trace.stage(ctx, "dry-run", stageStart, err)
		}
		if err == nil {
			stage = "deployment"
			deployment.State = "deploying"
			deploy := s.deployCompose
			mode := "no-wait"
			if binding.AutoDeployRollbackEnabled {
				deploy = s.deployComposeWait
				mode = "wait-for-health"
			}
			stageStart = time.Now()
			err = deploy(ctx, binding.Host, filename, logs)
			trace.stage(ctx, "deployment", stageStart, err)
			trace.note("deployment mode=%s", mode)
		}
		if err != nil && binding.AutoDeployRollbackEnabled {
			provisionRolledBack = true
			originalErr := sanitizeDeploymentOutput(safeGitError(fmt.Errorf("%s failed: %w", stage, err)))
			deployment.State = "rolling_back"
			// The rollback must NOT inherit the context that just died. A
			// deployment cancelled mid-flight - a browser that went away, a
			// proxy that timed out, a shutdown - is exactly when the previous
			// version has to be restored, and every Docker call here would
			// have failed instantly on the cancelled parent. That is what
			// turned "deployment cancelled" into "deployment AND rollback
			// failed", leaving the stack on the half-applied version.
			rollbackCtx, cancelRollback := context.WithTimeout(context.WithoutCancel(ctx), rollbackBudget)
			_, _ = fmt.Fprintf(logs, "\n[dockman] %s; restoring the pre-import stack files\n", originalErr)
			hadPreviousCompose := true
			var rollbackErr error
			if rollbackBackupID != "" {
				hadPreviousCompose, rollbackErr = s.deploymentHadPreviousCompose(binding, rollbackBackupID, relative)
			}
			if rollbackErr == nil && stage == "deployment" && !hadPreviousCompose {
				_, _ = fmt.Fprintln(logs, "[dockman] first deployment failed; removing its partial containers and networks before restoring the absent stack")
				rollbackErr = s.cleanupCompose(rollbackCtx, binding.Host, filename, logs)
			}
			var restored []string
			if provisioning != nil {
				if provisionRollbackErr := provisioning.RollbackMetadata(); provisionRollbackErr != nil {
					rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore provisioning metadata: %w", provisionRollbackErr))
				}
			}
			if rollbackBackupID != "" {
				var fileRollbackErr error
				restored, fileRollbackErr = s.rollbackDeploymentFiles(binding, rollbackBackupID, relative)
				rollbackErr = errors.Join(rollbackErr, fileRollbackErr)
			}
			if provisioning != nil {
				if provisionRollbackErr := provisioning.RollbackRemovedPaths(); provisionRollbackErr != nil {
					rollbackErr = errors.Join(rollbackErr, fmt.Errorf("restore provisioned removals: %w", provisionRollbackErr))
				}
			}
			if provisioning != nil {
				if provisionRollbackErr := provisioning.RollbackCreatedDirectories(); provisionRollbackErr != nil {
					rollbackErr = errors.Join(rollbackErr, fmt.Errorf("remove provisioned directories: %w", provisionRollbackErr))
				} else {
					_, _ = fmt.Fprintln(logs, "[dockman] provisioning permissions and directories restored")
				}
			}
			if rollbackErr == nil && hadPreviousCompose {
				rollbackErr = s.validateCompose(rollbackCtx, binding.Host, filename)
			}
			// Validation/dry-run failures never touched Docker. A real deployment
			// (including --wait/health failure) is rolled forward to the restored
			// configuration so partially changed containers are repaired.
			if rollbackErr == nil && stage == "deployment" && hadPreviousCompose {
				_, _ = fmt.Fprintln(logs, "[dockman] restored files validated; checking the previous deployment plan")
				rollbackErr = s.dryRunCompose(rollbackCtx, binding.Host, filename, logs)
			}
			if rollbackErr == nil && stage == "deployment" && hadPreviousCompose {
				_, _ = fmt.Fprintln(logs, "[dockman] redeploying the previous stack version and waiting for health")
				rollbackErr = s.deployComposeWait(rollbackCtx, binding.Host, filename, logs)
			}
			trace.note("rollback finished: previous compose present=%t restored=%d", hadPreviousCompose, len(restored))
			if rollbackErr == nil {
				deployment.State = "rolled_back"
				deployment.Result = fmt.Sprintf("%s; previous version restored safely (%d file(s))", originalErr, len(restored))
				result.RolledBack = append(result.RolledBack, relative)
			} else {
				deployment.State = "rollback_failed"
				deployment.Result = fmt.Sprintf("%s; automatic rollback failed: %s", originalErr, sanitizeDeploymentOutput(safeGitError(rollbackErr)))
				result.RollbackFailed = append(result.RollbackFailed, relative)
			}
			cancelRollback()
		}
		if provisioning != nil && !provisionRolledBack {
			if finalizeErr := provisioning.Commit(); finalizeErr != nil {
				_, _ = fmt.Fprintf(logs, "\n[dockman] provisioning cleanup failed: %v\n", finalizeErr)
				err = errors.Join(err, finalizeErr)
				stage = "provisioning cleanup"
			}
		}
		unlock()
		deployment.Logs = logs.String()
		if err == nil {
			deployment.Result = "deployed"
			deployment.State = "success"
		} else if deployment.State != "rolled_back" && deployment.State != "rollback_failed" {
			deployment.State = "failed"
			deployment.Result = sanitizeDeploymentOutput(safeGitError(fmt.Errorf("%s failed: %w", stage, err)))
		}
		trace.done(deployment.State)
		if saveErr := s.store.SaveDeployment(&deployment); saveErr != nil {
			return result, saveErr
		}
		now := time.Now().UTC()
		deployError := ""
		if deployment.State != "success" {
			deployError = deployment.Result
		}
		_ = s.store.UpdateGitStackStatuses(binding.UUID, []string{relative}, map[string]any{"deploy_state": deployment.State, "deploy_error": deployError, "last_deploy_at": &now})
		s.recordActivity(ActivityRecord{RepositoryID: binding.RepositoryUUID, BindingID: binding.UUID,
			ComposePath: relative, Type: "stack_deploy", Trigger: "automation", State: deployment.State,
			CommitSHA: commit, Error: deployError, Details: ActivityDetails{Action: stage, Paths: []string{relative}, DeploymentIDs: []string{deployment.UUID}}})
		if provisioning != nil {
			provisionState := "success"
			if binding.AutoDeployRollbackEnabled && deployment.State != "success" {
				provisionState = deployment.State
			}
			s.recordActivity(ActivityRecord{RepositoryID: binding.RepositoryUUID, BindingID: binding.UUID,
				ComposePath: relative, Type: "stack_provision", Trigger: "automation", State: provisionState,
				CommitSHA: commit, BackupID: provisioning.backupID, Error: deployError, Details: ActivityDetails{Action: provisioning.manifest,
					Changed: provisioning.operations, Paths: []string{relative}, DeploymentIDs: []string{deployment.UUID}}})
		}
		if deployment.State != "success" {
			result.Failed = append(result.Failed, relative)
			continue
		}
		result.Deployed = append(result.Deployed, relative)
	}
	now := time.Now().UTC()
	state, message := "success", "deployed"
	if len(result.Failed) == 0 && len(result.Deferred) > 0 {
		// Work is still owed, but nothing failed. "pending" is retryable, so
		// the next cycle picks these up without anything having gone red.
		state = "pending"
		message = fmt.Sprintf("%d stack(s) busy with another action and not deployed yet: %s",
			len(result.Deferred), strings.Join(result.Deferred, ", "))
		if len(result.Deployed) > 0 {
			message = fmt.Sprintf("%d stack(s) deployed; ", len(result.Deployed)) + message
		}
		if err := s.store.UpdateBindingAutoDeployState(binding.UUID, state, message, &now); err != nil {
			return result, err
		}
		return result, nil
	}
	if len(result.Failed) > 0 {
		state = "failed"
		if len(result.Deployed) > 0 || len(result.RolledBack) > 0 {
			state = "partial"
		}
		message = fmt.Sprintf("%d stack(s) deployed; %d stack(s) failed", len(result.Deployed), len(result.Failed))
		if len(result.RolledBack) > 0 {
			message += fmt.Sprintf(" (%d restored automatically)", len(result.RolledBack))
		}
		if len(result.RollbackFailed) > 0 {
			state = "failed"
			message += fmt.Sprintf("; %d rollback(s) failed", len(result.RollbackFailed))
		}
		if len(result.Failed) > 0 {
			message += ": " + strings.Join(result.Failed, ", ")
		}
	} else if len(result.Deployed) > 0 {
		message = fmt.Sprintf("%d stack(s) deployed", len(result.Deployed))
	}
	if err := s.store.UpdateBindingAutoDeployState(binding.UUID, state, message, &now); err != nil {
		return result, err
	}
	return result, nil
}

var _ io.Writer = (*limitedLogWriter)(nil)
