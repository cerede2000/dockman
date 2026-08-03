package gitsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gitclient "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
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

func TestManualStackSyncFetchesCurrentGitCommitAndOnlyImportsTargetStack(t *testing.T) {
	service, stackRoot, repository, binding := prepareTrackedLocalDeletion(t)
	externalPath := t.TempDir()
	external, err := gitclient.PlainClone(externalPath, false, &gitclient.CloneOptions{
		URL: repository.RemoteURL, ReferenceName: plumbing.NewBranchReferenceName("main"), SingleBranch: true,
	})
	require.NoError(t, err)
	commitTestFile(t, external, externalPath, "stacks/alpha/compose.yml", "services:\n  alpha:\n    image: alpine:3.23\n")
	require.NoError(t, external.Push(&gitclient.PushOptions{}))

	stored, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.False(t, stored.AutoSyncEnabled, "the one-shot action must work for manual folder links")

	result, err := service.SyncGitStackNow(context.Background(), binding.ID, "alpha/compose.yml")
	require.NoError(t, err)
	require.Contains(t, result.Message, "Stack synchronized from Git")
	contents, err := os.ReadFile(filepath.Join(stackRoot, "alpha", "compose.yml"))
	require.NoError(t, err)
	require.Contains(t, string(contents), "alpine:3.23")
	require.NoDirExists(t, filepath.Join(stackRoot, "beta"), "an unrelated local deletion must not be restored")
	status, err := service.store.GitStackStatus(binding.ID, "alpha/compose.yml")
	require.NoError(t, err)
	require.Equal(t, stackSyncUpToDate, status.State)
}

func TestComposeOnlyMutationStatusMatchesTransferPolicy(t *testing.T) {
	service, stackRoot, bindingView := prepareMultiStackBinding(t)
	binding, err := service.store.GetBinding(bindingView.ID)
	require.NoError(t, err)
	binding.SyncProfile = syncProfileComposeOnly
	require.NoError(t, service.store.SaveBinding(&binding))

	setAlphaState := func(state string) {
		require.NoError(t, service.store.UpdateGitStackStatuses(binding.UUID, []string{"alpha/compose.yml"}, map[string]any{"state": state}))
	}
	alphaState := func() string {
		status, statusErr := service.store.GitStackStatus(binding.UUID, "alpha/compose.yml")
		require.NoError(t, statusErr)
		return status.State
	}

	setAlphaState(stackSyncUpToDate)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "alpha", "data"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", "data", "runtime.db"), []byte("mutable\n"), 0o644))
	service.MarkLocalChange("local", "compose/alpha/data/runtime.db")
	require.Equal(t, stackSyncUpToDate, alphaState(), "a file outside the Compose-only inventory must not advertise a push")

	setAlphaState(stackSyncUpToDate)
	binding.IncludePatterns = "alpha/**"
	require.NoError(t, service.store.SaveBinding(&binding))
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "alpha", "test"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", "test", ".env"), []byte("TOKEN=do-not-sync\n"), 0o600))
	service.MarkLocalChange("local", "compose/alpha/test/.env")
	require.Equal(t, stackSyncUpToDate, alphaState(), "a broad include must not turn a protected environment file into a normal Git change")

	binding.IncludePatterns = ""
	require.NoError(t, service.store.SaveBinding(&binding))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", ".env.example"), []byte("PORT=8080\n"), 0o644))
	service.MarkLocalChange("local", "compose/alpha/.env.example")
	require.Equal(t, stackSyncLocalChanges, alphaState(), "built-in environment templates are part of Compose-only synchronization")

	setAlphaState(stackSyncUpToDate)
	binding.IncludePatterns = "alpha/application.conf"
	require.NoError(t, service.store.SaveBinding(&binding))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", "application.conf"), []byte("enabled=true\n"), 0o644))
	service.MarkLocalChange("local", "compose/alpha/application.conf")
	require.Equal(t, stackSyncLocalChanges, alphaState(), "an explicit include must opt a file into Compose-only synchronization")

	setAlphaState(stackSyncUpToDate)
	binding.IncludePatterns = ""
	require.NoError(t, service.store.SaveBinding(&binding))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, ".dockmanignore"), []byte("alpha/.env.example\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", ".env.example"), []byte("PORT=9090\n"), 0o644))
	service.MarkLocalChange("local", "compose/alpha/.env.example")
	require.Equal(t, stackSyncUpToDate, alphaState(), ".dockmanignore must suppress the mutation indicator exactly like preview and push")

	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", "compose.yml"), []byte("services:\n  alpha:\n    image: alpine\n"), 0o644))
	service.MarkLocalChange("local", "compose/alpha/compose.yml")
	require.Equal(t, stackSyncLocalChanges, alphaState(), "the catalogued Compose manifest must remain tracked")
}

func TestGitTrackedFilesUsesEffectivePolicyAndSelectedStacks(t *testing.T) {
	service, stackRoot, binding := prepareMultiStackBinding(t)
	_, err := service.UpdateBindingPolicy(binding.ID, BindingPolicyInput{Profile: syncProfileComposeOnly, IncludePatterns: []string{"alpha/test/**"}})
	require.NoError(t, err)
	_, err = service.UpdateBindingComposeSelection(binding.ID, BindingComposeSelectionInput{
		Mode: composeSelectionSelected, ComposePaths: []string{"alpha/compose.yml"},
	})
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "alpha", "test"), 0o755))
	for _, path := range []string{"alpha/.env.example", "alpha/runtime.db", "alpha/test/.env", "alpha/test/.env.example", "beta/.env.example"} {
		require.NoError(t, os.WriteFile(filepath.Join(stackRoot, filepath.FromSlash(path)), []byte("test\n"), 0o644))
	}

	result, err := service.GitTrackedFiles(GitTrackedFilesInput{Host: "local", Paths: []string{
		"compose/alpha/.env.example", "compose/alpha/runtime.db", "compose/alpha/test/.env", "compose/alpha/test/.env.example",
		"compose/beta/.env.example", "another/.env.example",
	}})
	require.NoError(t, err)
	require.Equal(t, []string{"compose/alpha/.env.example", "compose/alpha/test/.env.example"}, result.TrackedPaths)
	require.Len(t, result.Files, 6)
	byPath := make(map[string]GitTrackedFileView, len(result.Files))
	for _, file := range result.Files {
		byPath[file.Path] = file
	}
	require.True(t, byPath["compose/alpha/.env.example"].Linked)
	require.True(t, byPath["compose/alpha/.env.example"].Mutable)
	require.Equal(t, binding.ID, byPath["compose/alpha/.env.example"].BindingID)
	require.Equal(t, "alpha/compose.yml", byPath["compose/alpha/.env.example"].ComposePath)
	require.False(t, byPath["compose/alpha/test/.env"].Mutable)
	require.Contains(t, byPath["compose/alpha/test/.env"].Reason, "sensitive")
	require.False(t, byPath["compose/beta/.env.example"].Linked)
	require.False(t, byPath["another/.env.example"].Linked)
}

func TestGitTrackedFilesIdentifiesExactFolderLinkRoot(t *testing.T) {
	service, _, binding := prepareMultiStackBinding(t)
	result, err := service.GitTrackedFiles(GitTrackedFilesInput{Host: "local", Paths: []string{"compose"}})
	require.NoError(t, err)
	require.Equal(t, []string{"compose"}, result.TrackedPaths)
	require.Len(t, result.Files, 1)
	require.True(t, result.Files[0].Linked)
	require.True(t, result.Files[0].FolderLinkRoot)
	require.Equal(t, binding.ID, result.Files[0].BindingID)
	require.Contains(t, result.Files[0].Reason, "Folder Link")
}

func TestFilesContextCanAddAndRemoveAnOrdinaryGitFile(t *testing.T) {
	service, stackRoot, binding := prepareMultiStackBinding(t)
	_, err := service.UpdateBindingPolicy(binding.ID, BindingPolicyInput{Profile: syncProfileComposeOnly})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", "runtime.bin"), []byte("payload\n"), 0o644))

	added, err := service.SetGitFileTracking(GitFileTrackingInput{Host: "local", BindingID: binding.ID, Path: "compose/alpha/runtime.bin", Tracked: true})
	require.NoError(t, err)
	require.True(t, added.Tracked)
	stored, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Contains(t, splitPatternLines(stored.IncludePatterns), "/alpha/runtime.bin")
	tracked, err := service.GitTrackedFiles(GitTrackedFilesInput{Host: "local", Paths: []string{"compose/alpha/runtime.bin"}})
	require.NoError(t, err)
	require.Equal(t, []string{"compose/alpha/runtime.bin"}, tracked.TrackedPaths)

	removed, err := service.SetGitFileTracking(GitFileTrackingInput{Host: "local", BindingID: binding.ID, Path: "compose/alpha/runtime.bin", Tracked: false})
	require.NoError(t, err)
	require.False(t, removed.Tracked)
	stored, err = service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.NotContains(t, splitPatternLines(stored.IncludePatterns), "/alpha/runtime.bin")
	require.Contains(t, splitPatternLines(stored.ExcludePatterns), "/alpha/runtime.bin")
	tracked, err = service.GitTrackedFiles(GitTrackedFilesInput{Host: "local", Paths: []string{"compose/alpha/runtime.bin"}})
	require.NoError(t, err)
	require.Empty(t, tracked.TrackedPaths)
}

func TestDeletingAFileRemovesOnlyItsPreciseContextMenuRule(t *testing.T) {
	service, stackRoot, binding := prepareMultiStackBinding(t)
	_, err := service.UpdateBindingPolicy(binding.ID, BindingPolicyInput{Profile: syncProfileComposeOnly, IncludePatterns: []string{"alpha/**"}})
	require.NoError(t, err)
	path := filepath.Join(stackRoot, "alpha", "one-off.bin")
	require.NoError(t, os.WriteFile(path, []byte("payload\n"), 0o644))
	_, err = service.SetGitFileTracking(GitFileTrackingInput{Host: "local", BindingID: binding.ID, Path: "compose/alpha/one-off.bin", Tracked: true})
	require.NoError(t, err)
	require.NoError(t, os.Remove(path))
	service.MarkLocalChange("local", "compose/alpha/one-off.bin")
	_, err = service.SetGitFileTracking(GitFileTrackingInput{Host: "local", BindingID: binding.ID, Path: "compose/alpha/one-off.bin", Deleted: true})
	require.NoError(t, err)

	stored, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"alpha/**"}, splitPatternLines(stored.IncludePatterns), "the broad operator rule must be preserved while the one-file rule is removed")
	require.NoError(t, os.WriteFile(path, []byte("recreated\n"), 0o644))
	tracked, err := service.GitTrackedFiles(GitTrackedFilesInput{Host: "local", Paths: []string{"compose/alpha/one-off.bin"}})
	require.NoError(t, err)
	require.Equal(t, []string{"compose/alpha/one-off.bin"}, tracked.TrackedPaths, "the remaining broad rule must still apply")
}

func TestDeletingAFileWithOnlyAPreciseRuleDoesNotTrackItsRecreation(t *testing.T) {
	service, stackRoot, binding := prepareMultiStackBinding(t)
	_, err := service.UpdateBindingPolicy(binding.ID, BindingPolicyInput{Profile: syncProfileComposeOnly})
	require.NoError(t, err)
	path := filepath.Join(stackRoot, "alpha", "one-off.bin")
	require.NoError(t, os.WriteFile(path, []byte("payload\n"), 0o644))
	_, err = service.SetGitFileTracking(GitFileTrackingInput{Host: "local", BindingID: binding.ID, Path: "compose/alpha/one-off.bin", Tracked: true})
	require.NoError(t, err)
	require.NoError(t, os.Remove(path))
	service.MarkLocalChange("local", "compose/alpha/one-off.bin")
	_, err = service.SetGitFileTracking(GitFileTrackingInput{Host: "local", BindingID: binding.ID, Path: "compose/alpha/one-off.bin", Deleted: true})
	require.NoError(t, err)

	stored, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Empty(t, stored.IncludePatterns)
	require.NoError(t, os.WriteFile(path, []byte("recreated\n"), 0o644))
	tracked, err := service.GitTrackedFiles(GitTrackedFilesInput{Host: "local", Paths: []string{"compose/alpha/one-off.bin"}})
	require.NoError(t, err)
	require.Empty(t, tracked.TrackedPaths)
}

func TestExactFilesContextExclusionOverridesABroadInclude(t *testing.T) {
	service, stackRoot, binding := prepareMultiStackBinding(t)
	_, err := service.UpdateBindingPolicy(binding.ID, BindingPolicyInput{Profile: syncProfileComposeOnly, IncludePatterns: []string{"alpha/**"}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", "application.conf"), []byte("enabled=true\n"), 0o644))

	_, err = service.SetGitFileTracking(GitFileTrackingInput{Host: "local", BindingID: binding.ID, Path: "compose/alpha/application.conf", Tracked: false})
	require.NoError(t, err)
	tracked, err := service.GitTrackedFiles(GitTrackedFilesInput{Host: "local", Paths: []string{"compose/alpha/application.conf"}})
	require.NoError(t, err)
	require.Empty(t, tracked.TrackedPaths)
	preview, err := service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	for _, entry := range preview.Entries {
		if entry.Path == "alpha/application.conf" {
			require.Equal(t, "skipped_excluded", entry.Status, "the exact context-menu exclusion must also apply to transfer inventory")
		}
	}
}

func TestFilesContextCannotChangeProtectedGitFiles(t *testing.T) {
	service, stackRoot, binding := prepareMultiStackBinding(t)
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", ".env"), []byte("TOKEN=secret\n"), 0o600))
	_, err := service.SetGitFileTracking(GitFileTrackingInput{Host: "local", BindingID: binding.ID, Path: "compose/alpha/.env", Tracked: true})
	require.ErrorContains(t, err, "sensitive")
	_, err = service.SetGitFileTracking(GitFileTrackingInput{Host: "local", BindingID: binding.ID, Path: "compose/alpha/compose.yml", Tracked: false})
	require.ErrorContains(t, err, "Compose manifests")
}

func TestManualStackPushReconcilesStaleSensitiveOnlyChange(t *testing.T) {
	service, stackRoot, _, binding := prepareTrackedLocalDeletion(t)
	require.NoError(t, os.MkdirAll(filepath.Join(stackRoot, "alpha", "test"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", "test", ".env"), []byte("TOKEN=do-not-sync\n"), 0o600))
	require.NoError(t, service.store.UpdateGitStackStatuses(binding.ID, []string{"alpha/compose.yml"}, map[string]any{"state": stackSyncLocalChanges}))

	result, err := service.PushGitStackAndResume(context.Background(), binding.ID, "alpha/compose.yml")
	require.NoError(t, err)
	require.Contains(t, result.Message, "sensitive local files were not pushed")
	status, err := service.store.GitStackStatus(binding.ID, "alpha/compose.yml")
	require.NoError(t, err)
	require.Equal(t, stackSyncUpToDate, status.State)
}

func TestStackCheckingStateIsProjectedWithoutOverwritingStoredState(t *testing.T) {
	service, _, binding := prepareMultiStackBinding(t)
	row, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	row.AutoSyncEnabled = true
	row.AutoSyncState = "syncing"
	require.NoError(t, service.store.SaveBinding(&row))
	require.NoError(t, service.store.UpdateGitStackStatuses(binding.ID, binding.SelectedComposePaths, map[string]any{"state": stackSyncUpToDate}))

	views, err := service.ListGitStackStatusViews("local")
	require.NoError(t, err)
	require.NotEmpty(t, views)
	for _, view := range views {
		require.Equal(t, stackSyncChecking, view.State)
	}
	stored, err := service.store.GitStackStatuses(binding.ID)
	require.NoError(t, err)
	for _, status := range stored {
		require.Equal(t, stackSyncUpToDate, status.State, "checking is a transient projection, not a persisted destructive state")
	}
}

func TestLegacyBindingErrorCopiedToStacksIsProjectedAtFolderLevelOnly(t *testing.T) {
	service, _, binding := prepareMultiStackBinding(t)
	row, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	row.AutoSyncState = "error"
	row.AutoSyncError = "temporary repository connection failure"
	require.NoError(t, service.store.SaveBinding(&row))
	now := time.Now().UTC()
	require.NoError(t, service.store.UpdateGitStackStatuses(binding.ID, binding.SelectedComposePaths, map[string]any{
		"state": stackSyncError, "error_message": row.AutoSyncError, "last_success_at": &now,
	}))

	views, err := service.ListGitStackStatusViews("local")
	require.NoError(t, err)
	require.NotEmpty(t, views)
	for _, view := range views {
		require.Equal(t, stackSyncUpToDate, view.State)
		require.Empty(t, view.Error)
		require.Equal(t, "error", view.BindingSyncState)
		require.Equal(t, row.AutoSyncError, view.BindingSyncError)
	}
}

func TestInitialFolderLinkFailureDoesNotInitializeEveryStackAsError(t *testing.T) {
	require.Equal(t, stackSyncPending, initialStackSyncState(StackBinding{InitialSyncState: "error"}))
	require.Equal(t, stackSyncPending, initialStackSyncState(StackBinding{InitialSyncState: "checking"}))
	require.Equal(t, stackSyncUpToDate, initialStackSyncState(StackBinding{InitialSyncState: "reconciled"}))
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

func TestPerStackAutoSyncPolicyKeepsManualSynchronizationAvailable(t *testing.T) {
	service, _, binding := prepareMultiStackBinding(t)

	updated, err := service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{
		Enabled:               true,
		IntervalMinutes:       5,
		AutoSyncSelectionMode: composeSelectionSelected,
		AutoSyncComposePaths:  []string{"alpha/compose.yml"},
	})
	require.NoError(t, err)
	require.Equal(t, composeSelectionSelected, updated.AutoSyncSelectionMode)
	require.Equal(t, []string{"alpha/compose.yml"}, updated.AutoSyncComposePaths)
	require.Equal(t, []string{"alpha/compose.yml", "beta/compose.yml"}, updated.SelectedComposePaths,
		"the folder-link selection must remain independent from the automatic policy")

	row, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"alpha/compose.yml"}, service.activeAutomationComposePaths(row))

	views, err := service.ListGitStackStatusViews("local")
	require.NoError(t, err)
	require.Len(t, views, 2)
	require.True(t, views[0].Selected)
	require.True(t, views[0].StackAutoSyncEnabled)
	require.True(t, views[1].Selected, "manual Git synchronization must remain available")
	require.False(t, views[1].StackAutoSyncEnabled)
	require.Nil(t, views[1].NextCheckAt)
	_, err = service.SetGitStackAutomationPause(binding.ID, "beta/compose.yml", true)
	require.ErrorContains(t, err, "not enabled for automatic Git synchronization",
		"temporary pause must not be confused with the persistent manual-only policy")
}

func TestLegacyAutoSyncPolicyDefaultsToAllSelectedStacks(t *testing.T) {
	service, _, binding := prepareMultiStackBinding(t)
	row, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	row.AutoSyncSelectionMode = ""
	row.AutoSyncComposePaths = ""
	require.NoError(t, service.store.SaveBinding(&row))

	require.Equal(t, []string{"alpha/compose.yml", "beta/compose.yml"}, service.activeAutomationComposePaths(row))
	view, err := service.bindingView(row)
	require.NoError(t, err)
	require.Equal(t, composeSelectionAll, view.AutoSyncSelectionMode)
	require.Equal(t, []string{"alpha/compose.yml", "beta/compose.yml"}, view.AutoSyncComposePaths)
}

func TestAutoDeployTargetMustAlsoBeAnAutoSyncTarget(t *testing.T) {
	service, _, binding := prepareMultiStackBinding(t)
	_, err := service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{
		Enabled:               true,
		IntervalMinutes:       5,
		AutoSyncSelectionMode: composeSelectionSelected,
		AutoSyncComposePaths:  []string{"alpha/compose.yml"},
		DeployEnabled:         true,
		DeployComposePaths:    []string{"beta/compose.yml"},
	})
	require.ErrorContains(t, err, "excluded from automatic synchronization")
}

func TestManualStackPushResumesRecoveryPause(t *testing.T) {
	service, stackRoot, _, binding := prepareTrackedLocalDeletion(t)
	composePath := filepath.Join(stackRoot, "alpha", "compose.yml")
	require.NoError(t, os.WriteFile(composePath, []byte("services:\n  alpha:\n    image: alpine:3.23\n"), 0o644))
	service.MarkLocalChange("local", "compose/alpha/compose.yml")
	require.NoError(t, service.store.SetGitStackPauseReason(binding.ID, "alpha/compose.yml", true, stackPauseRecovery))

	result, err := service.PushGitStackAndResume(context.Background(), binding.ID, "alpha/compose.yml")
	require.NoError(t, err)
	require.Contains(t, result.Message, "automatic synchronization resumed")
	status, err := service.store.GitStackStatus(binding.ID, "alpha/compose.yml")
	require.NoError(t, err)
	require.False(t, status.AutomationPaused)
}

func TestManualPauseIsNotClearedByPushAlone(t *testing.T) {
	service, stackRoot, _, binding := prepareTrackedLocalDeletion(t)
	composePath := filepath.Join(stackRoot, "alpha", "compose.yml")
	require.NoError(t, os.WriteFile(composePath, []byte("services:\n  alpha:\n    image: alpine:3.25\n"), 0o644))
	service.MarkLocalChange("local", "compose/alpha/compose.yml")
	_, err := service.SetGitStackAutomationPause(binding.ID, "alpha/compose.yml", true)
	require.NoError(t, err)

	_, err = service.PushGitStackAndResume(context.Background(), binding.ID, "alpha/compose.yml")
	require.NoError(t, err)
	status, err := service.store.GitStackStatus(binding.ID, "alpha/compose.yml")
	require.NoError(t, err)
	require.True(t, status.AutomationPaused)
	require.Equal(t, stackPauseManual, status.PauseReason)
}

func TestCompletePushClearsOnlyItsResolvedDeploymentWarning(t *testing.T) {
	service, stackRoot, _, binding := prepareTrackedLocalDeletion(t)
	updated, err := service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{
		Enabled: true, IntervalMinutes: 5, DeployEnabled: true,
		DeployComposePaths: []string{"alpha/compose.yml", "beta/compose.yml"},
	})
	require.NoError(t, err)
	require.True(t, updated.AutoDeployEnabled)
	require.NoError(t, service.store.UpdateGitStackStatuses(binding.ID, []string{"alpha/compose.yml"}, map[string]any{"deploy_state": "rolled_back", "deploy_error": "previous version restored"}))
	require.NoError(t, service.store.UpdateGitStackStatuses(binding.ID, []string{"beta/compose.yml"}, map[string]any{"deploy_state": "failed", "deploy_error": "independent failure"}))
	require.NoError(t, service.store.UpdateBindingAutoDeployState(binding.ID, "partial", "two incidents", nil))

	alphaPath := filepath.Join(stackRoot, "alpha", "compose.yml")
	require.NoError(t, os.WriteFile(alphaPath, []byte("services:\n  alpha:\n    image: alpine:fixed\n"), 0o644))
	service.MarkLocalChange("local", "compose/alpha/compose.yml")
	_, err = service.PushGitStack(context.Background(), binding.ID, "alpha/compose.yml")
	require.NoError(t, err)

	alpha, err := service.store.GitStackStatus(binding.ID, "alpha/compose.yml")
	require.NoError(t, err)
	require.Equal(t, "idle", alpha.DeployState)
	require.Empty(t, alpha.DeployError)
	beta, err := service.store.GitStackStatus(binding.ID, "beta/compose.yml")
	require.NoError(t, err)
	require.Equal(t, "failed", beta.DeployState, "another stack's failure must remain visible")
	row, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", row.AutoDeployState)
	require.Contains(t, row.AutoDeployError, "1 stack")
}

func TestPartialSettingsPushKeepsRecoveryPausedUntilStackIsComplete(t *testing.T) {
	service, stackRoot, _, binding := prepareTrackedLocalDeletion(t)
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", "compose.yml"), []byte("services:\n  alpha:\n    image: alpine:3.26\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", "settings.yml"), []byte("enabled: true\n"), 0o644))
	service.MarkLocalChange("local", "compose/alpha/compose.yml")
	require.NoError(t, service.store.SetGitStackPauseReason(binding.ID, "alpha/compose.yml", true, stackPauseRecovery))

	preview, err := service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	_, err = service.ExportBinding(context.Background(), binding.ID, TransferInput{
		PreviewToken: preview.PreviewToken, SelectedPaths: []string{"alpha/compose.yml"},
	})
	require.NoError(t, err)
	status, err := service.store.GitStackStatus(binding.ID, "alpha/compose.yml")
	require.NoError(t, err)
	require.True(t, status.AutomationPaused, "a partial stack export must not resume recovery automation")

	remaining, err := service.PreviewBinding(binding.ID, "stack_to_repository", TransferInput{})
	require.NoError(t, err)
	_, err = service.ExportBinding(context.Background(), binding.ID, TransferInput{
		PreviewToken: remaining.PreviewToken, SelectedPaths: []string{"alpha/settings.yml"},
	})
	require.NoError(t, err)
	status, err = service.store.GitStackStatus(binding.ID, "alpha/compose.yml")
	require.NoError(t, err)
	require.False(t, status.AutomationPaused, "the final successful stack export should resume recovery automation")
}

func TestResumePushesLocalRecoveryBeforeUnpausing(t *testing.T) {
	service, stackRoot, _, binding := prepareTrackedLocalDeletion(t)
	composePath := filepath.Join(stackRoot, "alpha", "compose.yml")
	require.NoError(t, os.WriteFile(composePath, []byte("services:\n  alpha:\n    image: alpine:3.24\n"), 0o644))
	service.MarkLocalChange("local", "compose/alpha/compose.yml")
	_, err := service.SetGitStackAutomationPause(binding.ID, "alpha/compose.yml", true)
	require.NoError(t, err)

	view, pushed, err := service.ResumeGitStackAutomation(context.Background(), binding.ID, "alpha/compose.yml")
	require.NoError(t, err)
	require.True(t, pushed)
	require.False(t, view.AutomationPaused)
}

func TestResumeKeepsPauseWhenLocalPushCannotComplete(t *testing.T) {
	service, stackRoot, repository, binding := prepareTrackedLocalDeletion(t)
	require.NoError(t, os.WriteFile(filepath.Join(stackRoot, "alpha", "compose.yml"), []byte("services:\n  alpha:\n    image: alpine:local\n"), 0o644))
	service.MarkLocalChange("local", "compose/alpha/compose.yml")
	_, err := service.SetGitStackAutomationPause(binding.ID, "alpha/compose.yml", true)
	require.NoError(t, err)
	remoteChange(t, repository.RemoteURL, "stacks/alpha/compose.yml", "services:\n  alpha:\n    image: alpine:remote\n")

	_, pushed, err := service.ResumeGitStackAutomation(context.Background(), binding.ID, "alpha/compose.yml")
	require.ErrorContains(t, err, "kept paused")
	require.False(t, pushed)
	status, statusErr := service.store.GitStackStatus(binding.ID, "alpha/compose.yml")
	require.NoError(t, statusErr)
	require.True(t, status.AutomationPaused)
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
