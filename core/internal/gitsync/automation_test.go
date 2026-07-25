package gitsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitclient "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/require"
)

func TestBindingAutomationConfigurationIsOptInAndBounded(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte("services: {}\n"), 0o644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)
	require.False(t, binding.AutoSyncEnabled)
	require.False(t, binding.AutoDeployRollbackEnabled)
	require.Equal(t, defaultAutoSyncIntervalMinutes, binding.AutoSyncIntervalMinutes)

	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 4})
	require.ErrorContains(t, err, "between 5 and 1440")
	updated, err := service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 10})
	require.NoError(t, err)
	require.True(t, updated.AutoSyncEnabled)
	require.Equal(t, "watching", updated.AutoSyncState)
	require.NoError(t, service.DeleteBinding(binding.ID, false))
	archived, err := service.store.ArchivedBinding("local", "compose/app")
	require.NoError(t, err)
	require.False(t, archived.AutoSyncEnabled, "relinking must never resume automation implicitly")
	require.False(t, archived.AutoSyncPaused, "relinking must never preserve a scheduler pause")
	require.Equal(t, "disabled", archived.AutoSyncState)
}

func TestFolderLinkPauseExcludesSchedulerAndResumeChecksImmediately(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte("services: {}\n"), 0o644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)
	establishBindingBaseline(t, service, binding.ID)
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)

	paused, err := service.SetBindingAutomationPause(context.Background(), binding.ID, true)
	require.NoError(t, err)
	require.True(t, paused.Binding.AutoSyncPaused)
	require.Equal(t, "watching", paused.Binding.AutoSyncState, "pausing must preserve the last operational synchronization state")
	statuses, err := service.ListGitStackStatusViews("local")
	require.NoError(t, err)
	require.NotEmpty(t, statuses)
	require.True(t, statuses[0].BindingAutomationPaused)
	require.Nil(t, statuses[0].NextCheckAt)
	due, err := service.store.ListAutoSyncBindings()
	require.NoError(t, err)
	require.Empty(t, due, "a paused folder link must add no scheduler work")
	require.Equal(t, autoSyncSchedulerMaxSleep, service.nextAutoSyncDelay(time.Now().UTC()))

	resumed, err := service.SetBindingAutomationPause(context.Background(), binding.ID, false)
	require.NoError(t, err)
	require.False(t, resumed.Binding.AutoSyncPaused)
	require.NotNil(t, resumed.Sync)
	require.Equal(t, "up_to_date", resumed.Sync.State)
	require.NotNil(t, resumed.Binding.LastAutoSyncAt, "resume must run the complete synchronization immediately")
	statuses, err = service.ListGitStackStatusViews("local")
	require.NoError(t, err)
	require.False(t, statuses[0].BindingAutomationPaused)
	require.NotNil(t, statuses[0].NextCheckAt)
	due, err = service.store.ListAutoSyncBindings()
	require.NoError(t, err)
	require.Len(t, due, 1)
}

func TestDisablingAutomationClearsFolderLinkPause(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte("services: {}\n"), 0o644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)
	_, err = service.SetBindingAutomationPause(context.Background(), binding.ID, true)
	require.NoError(t, err)

	disabled, err := service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: false, IntervalMinutes: 5})
	require.NoError(t, err)
	require.False(t, disabled.AutoSyncEnabled)
	require.False(t, disabled.AutoSyncPaused)
	require.Equal(t, "disabled", disabled.AutoSyncState)
}

func TestSavingUnchangedAutomationConfigurationPreservesOperationalIncidents(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte("services: {}\n"), 0o644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{
		Enabled: true, IntervalMinutes: 5, DeployEnabled: true, DeployRollback: true,
		DeployComposePaths: []string{"compose.yaml"},
	})
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, service.store.UpdateBindingAutoSyncState(binding.ID, "partial", "rollback needs attention", "", &now, nil))
	require.NoError(t, service.store.UpdateBindingAutoDeployState(binding.ID, "partial", "previous version restored", &now))

	updated, err := service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{
		Enabled: true, IntervalMinutes: 5, DeployEnabled: true, DeployRollback: true,
		DeployComposePaths: []string{"compose.yaml"},
	})
	require.NoError(t, err)
	require.Equal(t, "partial", updated.AutoSyncState)
	require.Equal(t, "rollback needs attention", updated.AutoSyncError)
	require.Equal(t, "partial", updated.AutoDeployState)
	require.Equal(t, "previous version restored", updated.AutoDeployError)
}

func TestManualResolutionClearsObsoleteFolderLinkConflictState(t *testing.T) {
	for _, keep := range []string{"git", "dockman"} {
		t.Run(keep, func(t *testing.T) {
			service, _ := testService(t, true)
			stackRoot := configureTestStack(t, service)
			repository := prepareBindingRepository(t, service)
			stackPath := filepath.Join(stackRoot, "app")
			require.NoError(t, os.MkdirAll(stackPath, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(stackPath, "compose.yaml"), []byte("services: {}\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(stackPath, "app.conf"), []byte("value=baseline\n"), 0o644))
			binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
			require.NoError(t, err)
			establishBindingBaseline(t, service, binding.ID)
			_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
			require.NoError(t, err)

			remoteChange(t, repository.RemoteURL, "stacks/app/app.conf", "value=git\n")
			require.NoError(t, os.WriteFile(filepath.Join(stackPath, "app.conf"), []byte("value=dockman\n"), 0o644))
			result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
			require.NoError(t, err)
			require.Equal(t, "conflict", result.State)

			direction := "repository_to_stack"
			if keep == "dockman" {
				direction = "stack_to_repository"
			}
			preview, err := service.PreviewBinding(binding.ID, direction, TransferInput{})
			require.NoError(t, err)
			require.Equal(t, 1, preview.Conflicts)
			input := TransferInput{PreviewToken: preview.PreviewToken, ResolvedPaths: []string{"app.conf"}, SelectedPaths: []string{"app.conf"}}
			if keep == "git" {
				_, err = service.ImportBinding(context.Background(), binding.ID, input)
			} else {
				_, err = service.ExportBinding(context.Background(), binding.ID, input)
			}
			require.NoError(t, err)

			updated, err := service.store.GetBinding(binding.ID)
			require.NoError(t, err)
			require.Equal(t, "watching", updated.AutoSyncState)
			require.Empty(t, updated.AutoSyncError)
			fresh, err := service.PreviewBinding(binding.ID, "repository_to_stack", TransferInput{})
			require.NoError(t, err)
			require.Zero(t, fresh.Conflicts)
			require.Zero(t, fresh.Changed)
		})
	}
}

func TestAutoSyncRestoresPreviousStackWhenHealthCheckedDeploymentFails(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	stackPath := filepath.Join(stackRoot, "app", "compose.yaml")
	previous := "services:\n  app:\n    image: alpine:3.22\n"
	imported := "services:\n  app:\n    image: alpine:3.23\n"
	require.NoError(t, os.MkdirAll(filepath.Dir(stackPath), 0o755))
	require.NoError(t, os.WriteFile(stackPath, []byte(previous), 0o644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)
	establishBindingBaseline(t, service, binding.ID)
	remoteChange(t, repository.RemoteURL, "stacks/app/compose.yaml", imported)

	var deployments []string
	service.ConfigureDeployment(
		func(_ context.Context, _, _ string) error { return nil },
		func(_ context.Context, _, _ string, _ io.Writer) error { return nil },
		func(_ context.Context, _, _ string, _ io.Writer) error {
			return errors.New("non-wait deployment must not be used with rollback protection")
		},
		func(_ context.Context, _, _ string, _ io.Writer) error {
			contents, readErr := os.ReadFile(stackPath)
			if readErr != nil {
				return readErr
			}
			deployments = append(deployments, string(contents))
			if string(contents) == imported {
				return errors.New("service did not become healthy before timeout")
			}
			return nil
		},
		func(_ context.Context, _, _ string, _ io.Writer) error { return nil },
		func(_, _ string) (func(), bool) { return func() {}, true },
	)
	updated, err := service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{
		Enabled: true, IntervalMinutes: 5, DeployEnabled: true, DeployRollback: true,
		DeployComposePaths: []string{"compose.yaml"},
	})
	require.NoError(t, err)
	require.True(t, updated.AutoDeployRollbackEnabled)

	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "partial", result.State)
	require.Equal(t, []string{"compose.yaml"}, result.DeployFailed)
	require.Equal(t, []string{"compose.yaml"}, result.RolledBack)
	require.Empty(t, result.RollbackFailed)
	require.Equal(t, []string{imported, previous}, deployments, "the imported version is checked first, then the restored version")

	contents, err := os.ReadFile(stackPath)
	require.NoError(t, err)
	require.Equal(t, previous, string(contents))
	baseline, err := service.store.BindingBaseline(binding.ID)
	require.NoError(t, err)
	previousHash := sha256.Sum256([]byte(previous))
	require.Equal(t, hex.EncodeToString(previousHash[:]), baseline["compose.yaml"])
	deploymentsView, err := service.ListBindingDeployments(binding.ID)
	require.NoError(t, err)
	require.Len(t, deploymentsView, 1)
	require.Equal(t, "rolled_back", deploymentsView[0].State)
	require.Contains(t, deploymentsView[0].Result, "previous version restored safely")

	// Background polling must not loop on an unchanged failing commit, while an
	// explicit Check now must retry after the operator fixes the environment.
	deploymentCount := len(deployments)
	skipped, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "partial", skipped.State)
	require.Len(t, deployments, deploymentCount)
	retried, err := service.RunBindingAutoSyncNow(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "partial", retried.State)
	require.Len(t, deployments, deploymentCount+2, "manual retry checks the failing version, then restores the previous version again")

	// Git is corrected in a new commit to the version already restored locally.
	// There is intentionally nothing to transfer or redeploy, but the active
	// rollback incident must be reconciled instead of keeping the link partial.
	remoteChange(t, repository.RemoteURL, "stacks/app/compose.yaml", previous)
	reconciled, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", reconciled.State)
	require.Equal(t, "Stack already matches Git", reconciled.Message)
	status, err := service.store.GitStackStatus(binding.ID, "compose.yaml")
	require.NoError(t, err)
	require.Equal(t, "idle", status.DeployState)
	require.Empty(t, status.DeployError)
	resolved, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", resolved.AutoSyncState)
	require.Equal(t, "watching", resolved.AutoDeployState)
	require.Empty(t, resolved.AutoDeployError)
}

func TestAutoRollbackRefusesToOverwriteAFileChangedAfterImport(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	stackPath := filepath.Join(stackRoot, "app", "compose.yaml")
	previous := "services:\n  app:\n    image: alpine:3.22\n"
	imported := "services:\n  app:\n    image: alpine:3.23\n"
	external := "services:\n  app:\n    image: editor-change\n"
	require.NoError(t, os.MkdirAll(filepath.Dir(stackPath), 0o755))
	require.NoError(t, os.WriteFile(stackPath, []byte(previous), 0o644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)
	establishBindingBaseline(t, service, binding.ID)
	remoteChange(t, repository.RemoteURL, "stacks/app/compose.yaml", imported)

	service.ConfigureDeployment(
		func(_ context.Context, _, _ string) error { return nil },
		func(_ context.Context, _, _ string, _ io.Writer) error { return nil },
		func(_ context.Context, _, _ string, _ io.Writer) error { return nil },
		func(_ context.Context, _, _ string, _ io.Writer) error {
			require.NoError(t, os.WriteFile(stackPath, []byte(external), 0o644))
			return errors.New("service did not become healthy before timeout")
		},
		func(_ context.Context, _, _ string, _ io.Writer) error { return nil },
		func(_, _ string) (func(), bool) { return func() {}, true },
	)
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{
		Enabled: true, IntervalMinutes: 5, DeployEnabled: true, DeployRollback: true,
		DeployComposePaths: []string{"compose.yaml"},
	})
	require.NoError(t, err)

	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "partial", result.State)
	require.Equal(t, []string{"compose.yaml"}, result.RollbackFailed)
	contents, err := os.ReadFile(stackPath)
	require.NoError(t, err)
	require.Equal(t, external, string(contents), "an external change must always win over automatic recovery")
	deploymentsView, err := service.ListBindingDeployments(binding.ID)
	require.NoError(t, err)
	require.Equal(t, "rollback_failed", deploymentsView[0].State)
	require.Contains(t, deploymentsView[0].Result, "changed after import")
}

func TestAutoSyncReconcilesIdenticalTreesBeforeCommitShortcut(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	contents := "services: {}\n"
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte(contents), 0o644))
	remoteChange(t, repository.RemoteURL, "stacks/app/compose.yaml", contents)
	autoReconcile := false
	binding, err := service.CreateBindingContext(context.Background(), BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app", AutoReconcile: &autoReconcile})
	require.NoError(t, err)
	require.Equal(t, "pending", binding.InitialSyncState)
	autoReconcile = true
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, AutoReconcile: &autoReconcile, IntervalMinutes: 5})
	require.NoError(t, err)

	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", result.State)
	require.Contains(t, result.Message, "baseline established automatically")
	baseline, err := service.store.BindingBaseline(binding.ID)
	require.NoError(t, err)
	require.NotEmpty(t, baseline["compose.yaml"])
}

func TestNextAutoSyncDelayTracksExactDeadline(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "app", "compose.yaml"), []byte("services: {}\n"), 0o644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)
	now := time.Now().UTC()
	attempted := now.Add(-4*time.Minute - 50*time.Second)
	require.NoError(t, service.store.UpdateBindingAutoSyncState(binding.ID, "watching", "", "", &attempted, nil))
	delay := service.nextAutoSyncDelay(now)
	require.InDelta(t, 10*time.Second, delay, float64(100*time.Millisecond))
}

func TestBindingAutomationImportsRemoteChangesWithBackup(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	stackPath := filepath.Join(stackRoot, "app", "compose.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(stackPath), 0o755))
	require.NoError(t, os.WriteFile(stackPath, []byte("services:\n  app:\n    image: alpine:3.22\n"), 0o644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)
	establishBindingBaseline(t, service, binding.ID)
	remoteChange(t, repository.RemoteURL, "stacks/app/compose.yaml", "services:\n  app:\n    image: alpine:3.23\n")
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)

	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", result.State)
	require.Equal(t, 1, result.Changed)
	require.NotEmpty(t, result.Backup)
	contents, err := os.ReadFile(stackPath)
	require.NoError(t, err)
	require.Contains(t, string(contents), "alpine:3.23")
	updated, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.NotNil(t, updated.LastAutoSyncSuccessAt)
	require.NotEmpty(t, updated.LastAutoSyncCommit)

	second, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "No new Git commit; stack scan skipped", second.Message)
}

func TestBindingAutomationLeavesAllFilesUntouchedOnConflict(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	stackPath := filepath.Join(stackRoot, "app", "compose.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(stackPath), 0o755))
	require.NoError(t, os.WriteFile(stackPath, []byte("services:\n  app:\n    image: alpine:3.22\n"), 0o644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose/app", SubPath: "stacks/app"})
	require.NoError(t, err)
	establishBindingBaseline(t, service, binding.ID)
	require.NoError(t, os.WriteFile(stackPath, []byte("services:\n  app:\n    image: local-change\n"), 0o644))
	remoteChange(t, repository.RemoteURL, "stacks/app/compose.yaml", "services:\n  app:\n    image: remote-change\n")
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)

	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "conflict", result.State)
	require.Equal(t, 1, result.Conflicts)
	contents, err := os.ReadFile(stackPath)
	require.NoError(t, err)
	require.Contains(t, string(contents), "local-change")
}

func TestBindingAutomationDiscoversAndDeploysNewGitStack(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"})
	require.NoError(t, err)
	binding, err = service.UpdateBindingComposeSelection(binding.ID, BindingComposeSelectionInput{Mode: composeSelectionSelected})
	require.NoError(t, err)
	remoteChange(t, repository.RemoteURL, "stacks/new-stack/compose.yml", "services:\n  app:\n    image: alpine:3.23\n")
	var actions []string
	service.ConfigureDeployment(
		func(_ context.Context, _, filename string) error {
			actions = append(actions, "validate:"+filename)
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
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{
		Enabled: true, IntervalMinutes: 5, DeployEnabled: true, DeployNewStacks: true,
	})
	require.NoError(t, err)
	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"new-stack/compose.yml"}, result.Deployed)
	require.Equal(t, []string{
		"validate:compose/new-stack/compose.yml",
		"dry-run:compose/new-stack/compose.yml",
		"deploy:compose/new-stack/compose.yml",
	}, actions)
	_, err = os.Stat(filepath.Join(stackRoot, "new-stack", "compose.yml"))
	require.NoError(t, err)
	updated, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Contains(t, splitPatternLines(updated.ComposePaths), "new-stack/compose.yml")
	require.Contains(t, splitPatternLines(updated.SelectedComposePaths), "new-stack/compose.yml")
	require.Contains(t, splitPatternLines(updated.AutoDeployComposePaths), "new-stack/compose.yml")
}

func TestAutoRollbackCleansUpFailedFirstDeploymentOfNewGitStack(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"})
	require.NoError(t, err)
	binding, err = service.UpdateBindingComposeSelection(binding.ID, BindingComposeSelectionInput{Mode: composeSelectionSelected})
	require.NoError(t, err)
	remoteChange(t, repository.RemoteURL, "stacks/new-stack/compose.yml", "services:\n  app:\n    image: alpine:3.23\n")

	var actions []string
	service.ConfigureDeployment(
		func(_ context.Context, _, filename string) error {
			actions = append(actions, "validate:"+filename)
			return nil
		},
		func(_ context.Context, _, filename string, _ io.Writer) error {
			actions = append(actions, "dry-run:"+filename)
			return nil
		},
		func(_ context.Context, _, _ string, _ io.Writer) error {
			return errors.New("non-wait deployment must not be used with rollback protection")
		},
		func(_ context.Context, _, filename string, _ io.Writer) error {
			actions = append(actions, "deploy-wait:"+filename)
			return errors.New("first deployment did not become healthy")
		},
		func(_ context.Context, _, filename string, _ io.Writer) error {
			actions = append(actions, "cleanup:"+filename)
			return nil
		},
		func(_, _ string) (func(), bool) { return func() {}, true },
	)
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{
		Enabled: true, IntervalMinutes: 5, DeployEnabled: true, DeployNewStacks: true, DeployRollback: true,
	})
	require.NoError(t, err)

	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"new-stack/compose.yml"}, result.RolledBack)
	require.Equal(t, []string{
		"validate:compose/new-stack/compose.yml",
		"dry-run:compose/new-stack/compose.yml",
		"deploy-wait:compose/new-stack/compose.yml",
		"cleanup:compose/new-stack/compose.yml",
	}, actions)
	require.NoFileExists(t, filepath.Join(stackRoot, "new-stack", "compose.yml"))
}

func TestAutoSyncDeploysNewStackWhenExistingStackValidationFails(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	existingPath := filepath.Join(stackRoot, "existing", "compose.yml")
	require.NoError(t, os.MkdirAll(filepath.Dir(existingPath), 0o755))
	require.NoError(t, os.WriteFile(existingPath, []byte("services:\n  app:\n    image: alpine:3.22\n"), 0o644))
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"})
	require.NoError(t, err)
	establishBindingBaseline(t, service, binding.ID)
	remoteChange(t, repository.RemoteURL, "stacks/existing/compose.yml", "services:\n  broken: [\n")
	remoteChange(t, repository.RemoteURL, "stacks/new-stack/compose.yml", "services:\n  app:\n    image: alpine:3.23\n")

	var actions []string
	service.ConfigureDeployment(
		func(_ context.Context, _, filename string) error {
			actions = append(actions, "validate:"+filename)
			if strings.Contains(filename, "existing/") {
				contents, readErr := os.ReadFile(existingPath)
				if readErr != nil {
					return readErr
				}
				if strings.Contains(string(contents), "broken") {
					return errors.New("invalid compose")
				}
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
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{
		Enabled: true, IntervalMinutes: 5, DeployEnabled: true, DeployNewStacks: true,
		DeployComposePaths: []string{"existing/compose.yml"},
	})
	require.NoError(t, err)

	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "partial", result.State)
	require.Equal(t, []string{"existing/compose.yml"}, result.SyncFailed)
	require.Empty(t, result.DeployFailed)
	require.Equal(t, []string{"new-stack/compose.yml"}, result.Deployed)
	require.Contains(t, result.Message, "1 invalid Compose stack(s) kept unchanged; 1 independent stack(s) deployed successfully")
	require.FileExists(t, filepath.Join(stackRoot, "new-stack", "compose.yml"))
	existingContents, err := os.ReadFile(existingPath)
	require.NoError(t, err)
	require.Contains(t, string(existingContents), "alpine:3.22", "the running stack must keep its last valid local Compose file")
	require.Equal(t, []string{
		"validate:compose/new-stack/compose.yml",
		"dry-run:compose/new-stack/compose.yml",
		"deploy:compose/new-stack/compose.yml",
	}, actions)
	updated, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Equal(t, "partial", updated.AutoSyncState)
	require.Equal(t, "success", updated.AutoDeployState)
	require.NotEmpty(t, updated.LastAutoSyncCommit)
	statuses, err := service.store.GitStackStatuses(binding.ID)
	require.NoError(t, err)
	states := make(map[string]string, len(statuses))
	for _, status := range statuses {
		states[status.ComposePath] = status.State
	}
	require.Equal(t, stackSyncError, states["existing/compose.yml"])
	require.Equal(t, stackSyncUpToDate, states["new-stack/compose.yml"])

	actionCount := len(actions)
	second, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "partial", second.State)
	require.Contains(t, second.Message, "no new Git commit, stack scan skipped")
	require.Len(t, actions, actionCount, "an unchanged failed Compose file must not create a retry loop")
	statuses, err = service.store.GitStackStatuses(binding.ID)
	require.NoError(t, err)
	for _, status := range statuses {
		if status.ComposePath == "existing/compose.yml" {
			require.Equal(t, stackSyncError, status.State, "the failed stack must stay visible until a correcting Git commit arrives")
		}
	}

	remoteChange(t, repository.RemoteURL, "stacks/existing/compose.yml", "services:\n  app:\n    image: alpine:3.24\n")
	third, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", third.State)
	require.Empty(t, third.SyncFailed)
	require.Equal(t, []string{"existing/compose.yml"}, third.Deployed)
	updated, err = service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", updated.AutoSyncState)
	statuses, err = service.store.GitStackStatuses(binding.ID)
	require.NoError(t, err)
	for _, status := range statuses {
		require.Equal(t, stackSyncUpToDate, status.State)
	}
}

func establishBindingBaseline(t *testing.T, service *Service, bindingID string) {
	t.Helper()
	preview, err := service.PreviewBinding(bindingID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	_, err = service.ExportBinding(context.Background(), bindingID, TransferInput{PreviewToken: preview.PreviewToken})
	require.NoError(t, err)
}

func remoteChange(t *testing.T, remoteURL, name, contents string) {
	t.Helper()
	root := t.TempDir()
	repo, err := gitclient.PlainClone(root, false, &gitclient.CloneOptions{
		URL: remoteURL, ReferenceName: plumbing.NewBranchReferenceName("main"), SingleBranch: true,
	})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755))
	commitTestFile(t, repo, root, name, contents)
	require.NoError(t, repo.Push(&gitclient.PushOptions{}))
}
