package gitsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func prepareMultiStackBinding(t *testing.T) (*Service, string, BindingView) {
	t.Helper()
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	for _, folder := range []string{"alpha", "beta"} {
		require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, folder), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(stackRoot, folder, "compose.yml"), []byte("services: {}\n"), 0o644))
	}
	binding, err := service.CreateBinding(BindingInput{
		RepositoryID: repository.UUID,
		Host:         "local",
		StackPath:    "compose",
		SubPath:      "stacks",
	})
	require.NoError(t, err)
	return service, stackRoot, binding
}

func TestGitStackStatusIndexTracksExactNestedStack(t *testing.T) {
	service, _, binding := prepareMultiStackBinding(t)

	views, err := service.ListGitStackStatusViews("local")
	require.NoError(t, err)
	require.Len(t, views, 2)
	require.Equal(t, []string{"compose/alpha/compose.yml", "compose/beta/compose.yml"}, []string{
		views[0].FullComposePath,
		views[1].FullComposePath,
	})

	service.MarkLocalChange("local", "compose/beta/settings/config.json")
	views, err = service.ListGitStackStatusViews("local")
	require.NoError(t, err)
	require.Equal(t, stackSyncPending, views[0].State)
	require.Equal(t, stackSyncLocalChanges, views[1].State)

	service.MarkLocalChange("another-host", "compose/alpha/compose.yml")
	views, err = service.ListGitStackStatusViews("local")
	require.NoError(t, err)
	require.Equal(t, stackSyncPending, views[0].State)
	require.Equal(t, binding.ID, views[0].BindingID)
}

func TestNewComposeCreatedByEditorIsCataloguedUnselectedAndCanBeEnabled(t *testing.T) {
	service, stackRoot, binding := prepareMultiStackBinding(t)
	gammaDir := filepath.Join(stackRoot, "gamma")
	require.NoError(t, os.MkdirAll(gammaDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(gammaDir, "compose.yml"), []byte("services: {}\n"), 0o644))

	service.MarkLocalChange("local", "compose/gamma/compose.yml")
	updated, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Contains(t, splitPatternLines(updated.ComposePaths), "gamma/compose.yml")
	require.NotContains(t, selectedComposePaths(updated), "gamma/compose.yml")

	views, err := service.ListGitStackStatusViews("local")
	require.NoError(t, err)
	require.Len(t, views, 3)
	var grey GitStackStatusView
	for _, view := range views {
		if view.ComposePath == "gamma/compose.yml" {
			grey = view
		}
	}
	require.Equal(t, stackSyncUnselected, grey.State)
	require.False(t, grey.Selected)

	enabled, err := service.EnableGitStackSynchronization(binding.ID, "gamma/compose.yml")
	require.NoError(t, err)
	require.True(t, enabled.Selected)
	require.Equal(t, stackSyncLocalChanges, enabled.State)
	updated, err = service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Contains(t, selectedComposePaths(updated), "gamma/compose.yml")

	require.NoError(t, os.Remove(filepath.Join(gammaDir, "compose.yml")))
	service.MarkLocalChange("local", "compose/gamma/compose.yml")
	updated, err = service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.NotContains(t, splitPatternLines(updated.ComposePaths), "gamma/compose.yml")
	views, err = service.ListGitStackStatusViews("local")
	require.NoError(t, err)
	require.Len(t, views, 2)
}

func TestDeletingStackDirectoryRemovesStaleComposeChoice(t *testing.T) {
	service, stackRoot, binding := prepareMultiStackBinding(t)

	require.NoError(t, os.RemoveAll(filepath.Join(stackRoot, "beta")))
	service.MarkLocalChange("local", "compose/beta")

	updated, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"alpha/compose.yml"}, splitPatternLines(updated.ComposePaths))
	require.Equal(t, []string{"alpha/compose.yml"}, selectedComposePaths(updated))
	views, err := service.ListGitStackStatusViews("local")
	require.NoError(t, err)
	require.Len(t, views, 1)
	require.Equal(t, "alpha/compose.yml", views[0].ComposePath)
}

func TestCopiedStackDirectoryIsCataloguedWithoutPageRefresh(t *testing.T) {
	service, stackRoot, binding := prepareMultiStackBinding(t)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "gamma"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "gamma", "compose.yaml"), []byte("services: {}\n"), 0o644))

	service.MarkLocalChange("local", "compose/gamma")

	updated, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Contains(t, splitPatternLines(updated.ComposePaths), "gamma/compose.yaml")
	require.NotContains(t, selectedComposePaths(updated), "gamma/compose.yaml")
}

func TestLocalDeletionKeepsStackCataloguedWhenGitStillContainsIt(t *testing.T) {
	service, stackRoot, binding := prepareMultiStackBinding(t)
	repository, err := service.store.GetRepository(binding.RepositoryID)
	require.NoError(t, err)
	remoteChange(t, repository.RemoteURL, "stacks/beta/compose.yml", "services: {}\n")
	_, err = service.PullRepository(context.Background(), repository.UUID)
	require.NoError(t, err)

	require.NoError(t, os.RemoveAll(filepath.Join(stackRoot, "beta")))
	service.MarkLocalChange("local", "compose/beta")

	updated, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Contains(t, splitPatternLines(updated.ComposePaths), "beta/compose.yml",
		"a Git-only stack must remain selectable so it can be imported or automatically deployed")
}

func TestGitStackPauseOnlyExcludesThatStackFromAutomation(t *testing.T) {
	service, _, binding := prepareMultiStackBinding(t)

	paused, err := service.SetGitStackAutomationPause(binding.ID, "alpha/compose.yml", true)
	require.NoError(t, err)
	require.True(t, paused.AutomationPaused)
	require.Equal(t, []string{"beta/compose.yml"}, service.activeAutomationComposePaths(StackBinding{
		UUID:                 binding.ID,
		ComposePaths:         "alpha/compose.yml\nbeta/compose.yml",
		ComposeSelectionMode: composeSelectionAll,
	}))

	// A non-nil empty target list means all stacks are paused. It must not
	// accidentally update every row in the binding.
	_, err = service.SetGitStackAutomationPause(binding.ID, "beta/compose.yml", true)
	require.NoError(t, err)
	require.NoError(t, service.store.UpdateGitStackStatuses(binding.ID, []string{}, map[string]any{"state": stackSyncChecking}))
	rows, err := service.store.GitStackStatuses(binding.ID)
	require.NoError(t, err)
	for _, row := range rows {
		require.NotEqual(t, stackSyncChecking, row.State)
	}
}

func TestUnchangedRemoteCheckPreservesKnownLocalChanges(t *testing.T) {
	service, _, bindingView := prepareMultiStackBinding(t)
	binding, err := service.store.GetBinding(bindingView.ID)
	require.NoError(t, err)

	service.MarkLocalChange("local", "compose/beta/config.json")
	service.updateActiveStackStatusesPreservingLocal(binding, stackSyncChecking, "", "", false)
	service.updateActiveStackStatusesPreservingLocal(binding, stackSyncUpToDate, "", "commit", true)

	rows, err := service.store.GitStackStatuses(binding.UUID)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.Equal(t, stackSyncUpToDate, rows[0].State)
	require.Equal(t, stackSyncLocalChanges, rows[1].State)
}

func TestAutomationSkipsCleanlyWhenEveryStackIsPaused(t *testing.T) {
	service, _, binding := prepareMultiStackBinding(t)
	updated, err := service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)
	for _, composePath := range updated.ComposePaths {
		_, err = service.SetGitStackAutomationPause(binding.ID, composePath, true)
		require.NoError(t, err)
	}

	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "paused", result.State)
	require.Contains(t, result.Message, "All selected stacks are paused")
}
