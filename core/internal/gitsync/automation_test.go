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

	"github.com/RA341/dockman/internal/host/filesystem"
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

func TestAutomaticSyncImportsOnlyItsPerStackTargets(t *testing.T) {
	service, stackRoot, binding := prepareMultiStackBinding(t)
	establishBindingBaseline(t, service, binding.ID)
	_, err := service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{
		Enabled:               true,
		IntervalMinutes:       5,
		AutoSyncSelectionMode: composeSelectionSelected,
		AutoSyncComposePaths:  []string{"alpha/compose.yml"},
	})
	require.NoError(t, err)
	repository, err := service.store.GetRepository(binding.RepositoryID)
	require.NoError(t, err)
	remoteChange(t, repository.RemoteURL, "stacks/alpha/compose.yml", "services:\n  alpha:\n    image: alpine:3.24\n")
	remoteChange(t, repository.RemoteURL, "stacks/beta/compose.yml", "services:\n  beta:\n    image: alpine:3.24\n")

	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", result.State)
	alpha, err := os.ReadFile(filepath.Join(stackRoot, "alpha", "compose.yml"))
	require.NoError(t, err)
	require.Contains(t, string(alpha), "alpine:3.24")
	beta, err := os.ReadFile(filepath.Join(stackRoot, "beta", "compose.yml"))
	require.NoError(t, err)
	require.Equal(t, "services: {}\n", string(beta), "a manual-only stack must not be touched by the scheduler")

	// The same Git commit was already observed while beta was manual-only.
	// Enabling it must invalidate the commit shortcut and import it without
	// requiring another remote commit.
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{
		Enabled:               true,
		IntervalMinutes:       5,
		AutoSyncSelectionMode: composeSelectionAll,
	})
	require.NoError(t, err)
	result, err = service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", result.State)
	beta, err = os.ReadFile(filepath.Join(stackRoot, "beta", "compose.yml"))
	require.NoError(t, err)
	require.Contains(t, string(beta), "alpine:3.24")
}

func TestExplicitCheckFetchesRemoteComposeAfterIgnoredLocalEdit(t *testing.T) {
	service, stackRoot, bindingView := prepareMultiStackBinding(t)
	binding, err := service.store.GetBinding(bindingView.ID)
	require.NoError(t, err)
	binding.SyncProfile = syncProfileComposeOnly
	require.NoError(t, service.store.SaveBinding(&binding))
	establishBindingBaseline(t, service, binding.UUID)
	_, err = service.UpdateBindingAutomation(binding.UUID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)

	// An unrelated mutable file in the same stack is outside the Compose-only
	// inventory and must neither create a local push nor hide a remote commit.
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "alpha", "data"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", "data", "runtime.db"), []byte("local runtime state\n"), 0o644))
	service.MarkLocalChange("local", "compose/alpha/data/runtime.db")
	status, err := service.store.GitStackStatus(binding.UUID, "alpha/compose.yml")
	require.NoError(t, err)
	require.Equal(t, stackSyncUpToDate, status.State)

	repository, err := service.store.GetRepository(binding.RepositoryUUID)
	require.NoError(t, err)
	remoteChange(t, repository.RemoteURL, "stacks/alpha/compose.yml", "services:\n  alpha:\n    image: alpine:3.24\n")
	result, err := service.RunBindingAutoSyncNow(context.Background(), binding.UUID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", result.State)
	contents, err := os.ReadFile(filepath.Join(stackRoot, "alpha", "compose.yml"))
	require.NoError(t, err)
	require.Contains(t, string(contents), "alpine:3.24")
	updated, err := service.store.GetBinding(binding.UUID)
	require.NoError(t, err)
	require.NotEmpty(t, updated.LastAutoSyncCommit, "an explicit check must fetch and record the new remote commit")
	status, err = service.store.GitStackStatus(binding.UUID, "alpha/compose.yml")
	require.NoError(t, err)
	require.Equal(t, stackSyncUpToDate, status.State)
	require.Equal(t, updated.LastAutoSyncCommit, status.LastCommit)
}

func TestComposeOnlyExplicitCheckImportsChangedEnvironmentTemplates(t *testing.T) {
	service, stackRoot, bindingView := prepareMultiStackBinding(t)
	binding, err := service.store.GetBinding(bindingView.ID)
	require.NoError(t, err)
	binding.SyncProfile = syncProfileComposeOnly
	require.NoError(t, service.store.SaveBinding(&binding))
	templates := map[string]string{
		".env.example":       "PORT=8080\n",
		".env.sample":        "LOG_LEVEL=info\n",
		".env.prod.template": "WORKERS=2\n",
	}
	for name, contents := range templates {
		require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", name), []byte(contents), 0o644))
	}
	establishBindingBaseline(t, service, binding.UUID)
	_, err = service.UpdateBindingAutomation(binding.UUID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)

	repository, err := service.store.GetRepository(binding.RepositoryUUID)
	require.NoError(t, err)
	remoteChange(t, repository.RemoteURL, "stacks/alpha/.env.example", "PORT=9090\n")
	remoteChange(t, repository.RemoteURL, "stacks/alpha/.env.sample", "LOG_LEVEL=debug\n")
	remoteChange(t, repository.RemoteURL, "stacks/alpha/.env.prod.template", "WORKERS=4\n")
	result, err := service.RunBindingAutoSyncNow(context.Background(), binding.UUID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", result.State)
	require.Equal(t, 3, result.Changed)
	for name, expected := range map[string]string{
		".env.example": "PORT=9090\n", ".env.sample": "LOG_LEVEL=debug\n", ".env.prod.template": "WORKERS=4\n",
	} {
		contents, readErr := os.ReadFile(filepath.Join(stackRoot, "alpha", name))
		require.NoError(t, readErr)
		require.Equal(t, expected, string(contents), name)
	}
	second, err := service.RunBindingAutoSyncNow(context.Background(), binding.UUID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", second.State)
	require.Zero(t, second.Changed)
	require.NotContains(t, second.Message, "stack scan skipped", "Check now must perform an authoritative inventory even when the commit is unchanged")
}

func TestAutomaticSyncCanKeepEveryLinkedStackManualOnly(t *testing.T) {
	service, _, binding := prepareMultiStackBinding(t)
	_, err := service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{
		Enabled:               true,
		IntervalMinutes:       5,
		AutoSyncSelectionMode: composeSelectionSelected,
		AutoSyncComposePaths:  []string{},
	})
	require.NoError(t, err)

	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "paused", result.State)
	require.Contains(t, result.Message, "excluded from automatic synchronization")
	row, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Empty(t, service.activeAutomationComposePaths(row))
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
	// There is nothing to transfer, but the active rollback incident is retried
	// once so the deployment can be verified before the link becomes healthy.
	remoteChange(t, repository.RemoteURL, "stacks/app/compose.yaml", previous)
	reconciled, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", reconciled.State)
	require.Equal(t, "0 file(s) synchronized and 1 stack(s) deployed", reconciled.Message)
	require.Len(t, deployments, deploymentCount+3, "the corrected commit is health-checked before clearing the rollback incident")
	status, err := service.store.GitStackStatus(binding.ID, "compose.yaml")
	require.NoError(t, err)
	require.Equal(t, "success", status.DeployState)
	require.Empty(t, status.DeployError)
	resolved, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", resolved.AutoSyncState)
	require.Equal(t, "success", resolved.AutoDeployState)
	require.Equal(t, "1 stack(s) deployed", resolved.AutoDeployError)
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
	require.NotContains(t, second.Message, "stack scan skipped", "red synchronization states are checked automatically even when Git did not move")
	require.Len(t, actions, actionCount, "rechecking an unchanged invalid Compose file must not deploy it")
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

func TestRepositoryFailureDoesNotPaintUnchangedStacksRed(t *testing.T) {
	service, _, binding := prepareMultiStackBinding(t)
	establishBindingBaseline(t, service, binding.ID)
	_, err := service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)
	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", result.State)

	repository, err := service.store.GetRepository(binding.RepositoryID)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(repository.RemoteURL))
	_, err = service.RunBindingAutoSync(context.Background(), binding.ID)
	require.Error(t, err)

	rows, err := service.store.GitStackStatuses(binding.ID)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	for _, row := range rows {
		require.Equal(t, stackSyncUpToDate, row.State, "a repository-level failure must preserve the last authoritative stack state")
		require.Empty(t, row.ErrorMessage)
	}
	failedBinding, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Equal(t, "error", failedBinding.AutoSyncState)
	require.NotEmpty(t, failedBinding.AutoSyncError, "the failure remains visible on the Folder Link")
}

func TestComposeOnlyStaysGreenAcrossUnchangedCyclesAndIgnoresDataTrees(t *testing.T) {
	service, stackRoot, binding := prepareMultiStackBinding(t)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "alpha", "data"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", "data", "runtime.db"), []byte("mutable data\n"), 0o600))
	service.ConfigureStackAccess(func(host, stackPath string) (filesystem.FileSystem, string, error) {
		return denyReadDirFS{FileSystem: filesystem.NewLocal(stackRoot), baseName: "data"}, ".", nil
	}, func() []string { return []string{"local"} }, filepath.Join(t.TempDir(), "backups"))

	_, err := service.UpdateBindingPolicy(binding.ID, BindingPolicyInput{Profile: syncProfileComposeOnly})
	require.NoError(t, err)
	establishBindingBaseline(t, service, binding.ID)
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)

	for cycle := 0; cycle < 3; cycle++ {
		result, runErr := service.RunBindingAutoSync(context.Background(), binding.ID)
		require.NoError(t, runErr)
		require.Equal(t, "up_to_date", result.State)
		rows, rowsErr := service.store.GitStackStatuses(binding.ID)
		require.NoError(t, rowsErr)
		for _, row := range rows {
			require.Equal(t, stackSyncUpToDate, row.State, "cycle %d must not invent a stack error", cycle+1)
			require.Empty(t, row.ErrorMessage)
		}
	}
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

// The ordinary way a Folder Link starts: import the stack by hand once, then
// switch automation on. The stack is tracked but was never an authorized
// deployment target, so a service added to its Compose file from Git used to
// land on disk while nothing was ever deployed - the synchronization even
// reported success. It must now be authorized and deployed under the same
// controlled path as a newly discovered stack.
func TestAutoSyncDeploysAServiceAddedToAManuallyImportedStack(t *testing.T) {
	service, _ := testService(t, true)
	configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	remoteChange(t, repository.RemoteURL, "stacks/web/compose.yml", "services:\n  app:\n    image: alpine:3.23\n")
	_, err := service.FetchRepository(context.Background(), repository.UUID)
	require.NoError(t, err)
	_, err = service.PullRepository(context.Background(), repository.UUID)
	require.NoError(t, err)
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"})
	require.NoError(t, err)

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

	preview, err := service.PreviewBinding(binding.ID, "repository_to_stack", TransferInput{})
	require.NoError(t, err)
	_, err = service.ImportBinding(context.Background(), binding.ID, TransferInput{PreviewToken: preview.PreviewToken})
	require.NoError(t, err)
	require.Empty(t, actions, "a manual import must not deploy anything by itself")

	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{
		Enabled: true, IntervalMinutes: 5, DeployEnabled: true, DeployNewStacks: true,
	})
	require.NoError(t, err)
	imported, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"web/compose.yml"}, splitPatternLines(imported.ComposePaths))
	require.Empty(t, splitPatternLines(imported.AutoDeployComposePaths), "the stack starts unauthorized, which is the whole point")

	remoteChange(t, repository.RemoteURL, "stacks/web/compose.yml",
		"services:\n  app:\n    image: alpine:3.23\n  db:\n    image: postgres:18\n")

	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"web/compose.yml"}, result.Discovered)
	require.Equal(t, []string{"web/compose.yml"}, result.Deployed)
	require.Equal(t, []string{
		"validate:compose/web/compose.yml",
		"dry-run:compose/web/compose.yml",
		"deploy:compose/web/compose.yml",
	}, actions)

	authorized, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Contains(t, splitPatternLines(authorized.AutoDeployComposePaths), "web/compose.yml")
}

// Leaving a stack out of automatic deployment is a legitimate choice. Doing it
// silently is not: the link stayed green while the containers drifted away from
// the files, which is exactly how the gap above went unnoticed.
func TestAutoSyncNamesTheStacksItWasNotAuthorizedToDeploy(t *testing.T) {
	service, _ := testService(t, true)
	configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	remoteChange(t, repository.RemoteURL, "stacks/web/compose.yml", "services:\n  app:\n    image: alpine:3.23\n")
	remoteChange(t, repository.RemoteURL, "stacks/db/compose.yml", "services:\n  db:\n    image: postgres:18\n")
	_, err := service.FetchRepository(context.Background(), repository.UUID)
	require.NoError(t, err)
	_, err = service.PullRepository(context.Background(), repository.UUID)
	require.NoError(t, err)
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"})
	require.NoError(t, err)

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
	preview, err := service.PreviewBinding(binding.ID, "repository_to_stack", TransferInput{})
	require.NoError(t, err)
	_, err = service.ImportBinding(context.Background(), binding.ID, TransferInput{PreviewToken: preview.PreviewToken})
	require.NoError(t, err)

	// web is authorized by hand; db is deliberately left out, and automatic
	// authorization of never-deployed stacks is off.
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{
		Enabled: true, IntervalMinutes: 5, DeployEnabled: true,
		DeployComposePaths: []string{"web/compose.yml"},
	})
	require.NoError(t, err)

	actions = nil
	remoteChange(t, repository.RemoteURL, "stacks/db/compose.yml",
		"services:\n  db:\n    image: postgres:18\n  cache:\n    image: valkey:9\n")

	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Empty(t, actions, "an unauthorized stack must still not be deployed")
	require.Empty(t, result.Deployed)
	require.Contains(t, result.Message, "db/compose.yml")
	require.Contains(t, result.Message, "not authorized for automatic deployment")
	require.NotContains(t, result.Message, "web/compose.yml")
}

// An explicit check re-arms a failed or partial deployment by re-adding every
// controlled path. When none of them resolves to a target any more - the stack
// was deselected, removed, or excluded since - deployChangedStacks returned
// before it could touch the binding, and the red state survived the very
// action meant to clear it. Pressing "Check now" simply did nothing, for ever.
func TestStaleAutoDeployStateIsResolvedWhenNothingIsLeftToDeploy(t *testing.T) {
	service, _, bindingView := prepareMultiStackBinding(t)
	binding, err := service.store.GetBinding(bindingView.ID)
	require.NoError(t, err)
	establishBindingBaseline(t, service, binding.UUID)
	_, err = service.UpdateBindingAutomation(binding.UUID, BindingAutomationInput{
		Enabled:               true,
		IntervalMinutes:       5,
		AutoSyncSelectionMode: composeSelectionAll,
	})
	require.NoError(t, err)

	// Settle the link so the commit shortcut applies from now on.
	result, err := service.RunBindingAutoSync(context.Background(), binding.UUID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", result.State)
	settled, err := service.store.GetBinding(binding.UUID)
	require.NoError(t, err)
	require.NotEmpty(t, settled.LastAutoSyncCommit)

	// A previous run ended partial. The stacks it names are no longer under
	// automatic deployment, which is the state an operator lands in after
	// repairing things by hand.
	settled.AutoDeployEnabled = true
	settled.AutoDeployComposePaths = ""
	settled.AutoDeployState = "partial"
	settled.AutoDeployError = "1 stack(s) deployed; 1 stack(s) failed"
	require.NoError(t, service.store.SaveBinding(&settled))

	// The operator presses "Check now": the one action that retries.
	_, err = service.RunBindingAutoSyncNow(context.Background(), binding.UUID)
	require.NoError(t, err)

	after, err := service.store.GetBinding(binding.UUID)
	require.NoError(t, err)
	require.NotEqual(t, "partial", after.AutoDeployState,
		"a stale partial deploy must not survive an explicit check that has nothing left to deploy")
	require.Equal(t, "watching", after.AutoDeployState)
	require.NotContains(t, after.AutoDeployError, "failed")
}

// The manual way out, for every stale picture the automatic one cannot reach.
func TestResetBindingAutomationStateClearsReportsAndForcesFullScan(t *testing.T) {
	service, _, bindingView := prepareMultiStackBinding(t)
	binding, err := service.store.GetBinding(bindingView.ID)
	require.NoError(t, err)
	establishBindingBaseline(t, service, binding.UUID)
	_, err = service.UpdateBindingAutomation(binding.UUID, BindingAutomationInput{
		Enabled: true, IntervalMinutes: 5, AutoSyncSelectionMode: composeSelectionAll,
	})
	require.NoError(t, err)
	_, err = service.RunBindingAutoSync(context.Background(), binding.UUID)
	require.NoError(t, err)

	settled, err := service.store.GetBinding(binding.UUID)
	require.NoError(t, err)
	require.NotEmpty(t, settled.LastAutoSyncCommit)
	settled.AutoDeployEnabled = true
	settled.AutoDeployState = "partial"
	settled.AutoDeployError = "1 stack(s) deployed; 1 stack(s) failed"
	settled.AutoSyncState = "partial"
	settled.AutoSyncError = "something went wrong once"
	require.NoError(t, service.store.SaveBinding(&settled))
	require.NoError(t, service.store.UpdateGitStackStatuses(binding.UUID, []string{"alpha/compose.yml"},
		map[string]any{"deploy_state": "failed", "deploy_error": "boom"}))

	// A conflict is a pending decision, not a report: it must survive.
	require.NoError(t, service.store.UpdateGitStackStatuses(binding.UUID, []string{"beta/compose.yml"},
		map[string]any{"state": stackSyncConflict, "error_message": "needs a decision"}))

	before, err := service.store.GitStackStatuses(binding.UUID)
	require.NoError(t, err)
	expectedCleared := 0
	for _, row := range before {
		if row.DeployState != "" && row.DeployState != "success" {
			expectedCleared++
		}
	}
	require.GreaterOrEqual(t, expectedCleared, 1)

	result, err := service.ResetBindingAutomationState(binding.UUID)
	require.NoError(t, err)
	require.True(t, result.FullScan)
	require.Equal(t, expectedCleared, result.ClearedStack)

	after, err := service.store.GetBinding(binding.UUID)
	require.NoError(t, err)
	require.Equal(t, "watching", after.AutoDeployState)
	require.Empty(t, after.AutoDeployError)
	require.Equal(t, "idle", after.AutoSyncState)
	require.Empty(t, after.AutoSyncError)
	require.Empty(t, after.LastAutoSyncCommit, "the commit shortcut must not skip the next scan")

	alpha, err := service.store.GitStackStatus(binding.UUID, "alpha/compose.yml")
	require.NoError(t, err)
	require.Empty(t, alpha.DeployState)
	require.Empty(t, alpha.DeployError)

	beta, err := service.store.GitStackStatus(binding.UUID, "beta/compose.yml")
	require.NoError(t, err)
	require.Equal(t, stackSyncConflict, beta.State, "an unresolved conflict is a decision, not a report: it must survive a reset")
}

// Everything a stop can freeze mid-flight must come back with a terminal state.
// Two of these survived every restart: nothing else ever writes a terminal
// state onto a run that is already over.
func TestRestartRecoveryClearsEveryInFlightDeploymentState(t *testing.T) {
	service, _ := testService(t, true)
	const binding = "link-1"

	// The last entry is not a state this code writes today. It stands in for
	// the one somebody adds tomorrow: recovery selects on what is NOT finished,
	// so it must be repaired without anyone remembering to list it.
	for _, state := range []string{"validating", "provisioning", "dry_run", "deploying", "rolling_back", "some_future_stage"} {
		require.NoError(t, service.store.SaveDeployment(&Deployment{
			UUID: "deployment-" + state, RepositoryUUID: "repo-1", BindingUUID: binding,
			ComposeHash: state + "/compose.yml", State: state,
		}))
		require.NoError(t, service.store.db.Save(&GitStackStatus{
			BindingUUID: binding, ComposePath: state + "/compose.yml",
			State: stackSyncUpToDate, DeployState: state,
		}).Error)
	}
	// A finished run must be left exactly as it is.
	require.NoError(t, service.store.SaveDeployment(&Deployment{
		UUID: "deployment-success", RepositoryUUID: "repo-1", BindingUUID: binding,
		ComposeHash: "done/compose.yml", State: "success", Result: "deployed",
	}))
	require.NoError(t, service.store.db.Save(&GitStackStatus{
		BindingUUID: binding, ComposePath: "done/compose.yml",
		State: stackSyncUpToDate, DeployState: "success",
	}).Error)

	_, err := service.RecoverInterruptedOperations()
	require.NoError(t, err)

	rows, err := service.store.ListDeployments(binding, 100)
	require.NoError(t, err)
	byID := make(map[string]Deployment, len(rows))
	for _, row := range rows {
		byID[row.UUID] = row
	}
	for _, state := range []string{"validating", "provisioning", "dry_run", "deploying", "some_future_stage"} {
		require.Equal(t, "failed", byID["deployment-"+state].State, "a %q deployment must not survive a restart", state)
	}
	require.Equal(t, "rollback_failed", byID["deployment-rolling_back"].State,
		"an interrupted rollback is not a plain failure: nobody knows whether the previous version was restored")
	require.Equal(t, "success", byID["deployment-success"].State, "a finished run must be left alone")

	for _, state := range []string{"validating", "provisioning", "dry_run", "deploying", "rolling_back", "some_future_stage"} {
		status, statusErr := service.store.GitStackStatus(binding, state+"/compose.yml")
		require.NoError(t, statusErr)
		require.Equal(t, "failed", status.DeployState, "a stack left %q by a restart must be retryable", state)
		require.NotEmpty(t, status.DeployError)
	}
	done, err := service.store.GitStackStatus(binding, "done/compose.yml")
	require.NoError(t, err)
	require.Equal(t, "success", done.DeployState)

	// The values that mean "no run to speak of" are finished states too: a
	// restart must not paint an idle or unmanaged stack as a failed deployment.
	for _, quiet := range []string{"", "idle", "disabled"} {
		require.NoError(t, service.store.db.Save(&GitStackStatus{
			BindingUUID: binding, ComposePath: "quiet-" + quiet + "/compose.yml",
			State: stackSyncUpToDate, DeployState: "idle",
		}).Error)
		// Written the way the code writes it: the column defaults to
		// "disabled", so a struct save cannot carry an empty value.
		require.NoError(t, service.store.UpdateGitStackStatuses(binding,
			[]string{"quiet-" + quiet + "/compose.yml"}, map[string]any{"deploy_state": quiet}))
	}
	_, err = service.RecoverInterruptedOperations()
	require.NoError(t, err)
	for _, quiet := range []string{"", "idle", "disabled"} {
		status, statusErr := service.store.GitStackStatus(binding, "quiet-"+quiet+"/compose.yml")
		require.NoError(t, statusErr)
		require.Equal(t, quiet, status.DeployState, "a stack with no run in progress must be left alone")
	}
}

// "checking" is written immediately before initializeBinding runs, in the same
// request. At rest it can only mean initialization never finished - the process
// stopped, or an error return skipped the state write - and nothing re-runs
// that function. The link then showed neither success nor failure for ever.
func TestRestartRecoveryRepairsInterruptedInitialSync(t *testing.T) {
	service, _ := testService(t, true)
	interrupted := StackBinding{UUID: "link-checking", RepositoryUUID: "repo-1", Host: "local",
		StackPath: "compose", InitialSyncState: "checking"}
	require.NoError(t, service.store.SaveBinding(&interrupted))
	settled := StackBinding{UUID: "link-imported", RepositoryUUID: "repo-1", Host: "local",
		StackPath: "other", InitialSyncState: "imported"}
	require.NoError(t, service.store.SaveBinding(&settled))

	_, err := service.RecoverInterruptedOperations()
	require.NoError(t, err)

	after, err := service.store.GetBinding("link-checking")
	require.NoError(t, err)
	require.Equal(t, "error", after.InitialSyncState,
		"a link stopped while initializing must say so instead of staying silent for ever")
	require.NotEmpty(t, after.InitialSyncError)

	untouched, err := service.store.GetBinding("link-imported")
	require.NoError(t, err)
	require.Equal(t, "imported", untouched.InitialSyncState, "a finished initialization must be left alone")
}

// The rollback used to inherit the very context whose death triggered it. A
// deployment cancelled mid-flight - a closed tab, a reverse-proxy timeout, a
// shutdown - therefore could not restore anything: every Docker call failed
// instantly on the cancelled parent, and "deployment cancelled" was reported
// to the operator as "deployment AND automatic rollback failed", leaving the
// stack sitting on the half-applied version.
func TestRollbackSurvivesTheCancellationThatTriggeredIt(t *testing.T) {
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	liveRollbackCalls := 0
	deadRollbackCalls := 0
	countRollback := func(callCtx context.Context) {
		if callCtx.Err() == nil {
			liveRollbackCalls++
		} else {
			deadRollbackCalls++
		}
	}
	cancelledDuringDeploy := false

	service.ConfigureDeployment(
		func(callCtx context.Context, _, _ string) error {
			if cancelledDuringDeploy {
				countRollback(callCtx)
			}
			return callCtx.Err()
		},
		func(callCtx context.Context, _, _ string, _ io.Writer) error {
			if cancelledDuringDeploy {
				countRollback(callCtx)
			}
			return callCtx.Err()
		},
		func(_ context.Context, _, _ string, _ io.Writer) error {
			return errors.New("non-wait deployment must not be used with rollback protection")
		},
		func(callCtx context.Context, _, _ string, _ io.Writer) error {
			contents, readErr := os.ReadFile(stackPath)
			if readErr != nil {
				return readErr
			}
			if string(contents) == imported {
				// The browser goes away exactly here, mid-deployment.
				cancel()
				cancelledDuringDeploy = true
				return callCtx.Err()
			}
			countRollback(callCtx)
			return callCtx.Err()
		},
		func(callCtx context.Context, _, _ string, _ io.Writer) error {
			if cancelledDuringDeploy {
				countRollback(callCtx)
			}
			return callCtx.Err()
		},
		func(_, _ string) (func(), bool) { return func() {}, true },
	)
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{
		Enabled: true, IntervalMinutes: 5, DeployEnabled: true, DeployRollback: true,
		DeployComposePaths: []string{"compose.yaml"},
	})
	require.NoError(t, err)

	_, _ = service.RunBindingAutoSync(ctx, binding.ID)

	require.Positive(t, liveRollbackCalls, "the rollback must run on a live context, not the one that just died")
	require.Zero(t, deadRollbackCalls, "no rollback step may inherit the cancelled context")

	status, err := service.store.GitStackStatus(binding.ID, "compose.yaml")
	require.NoError(t, err)
	require.NotEqual(t, "rollback_failed", status.DeployState,
		"a cancelled deployment must not be reported as an unrecoverable rollback failure")
}
