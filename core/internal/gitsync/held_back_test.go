package gitsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/stretchr/testify/require"
)

func stackStateByPath(t *testing.T, service *Service, bindingID string) map[string]string {
	t.Helper()
	rows, err := service.store.GitStackStatuses(bindingID)
	require.NoError(t, err)
	states := make(map[string]string, len(rows))
	for _, row := range rows {
		states[row.ComposePath] = row.State
	}
	return states
}

// The reported case: a file the host's ACLs hide from Dockman, deleted by hand.
// The deletion needs a decision, but it belongs to ONE stack. Every other stack
// of the link used to stop dead - showing pending remote changes that could
// never arrive, because the cycle returned before importing anything.
func TestAutoSyncKeepsUnaffectedStacksSynchronizedWhileOneAwaitsADecision(t *testing.T) {
	service, _ := testService(t, true)
	stackRoot := t.TempDir()
	service.ConfigureStackAccess(func(host, stackPath string) (filesystem.FileSystem, string, error) {
		if host != "local" || stackPath != "compose" {
			return nil, "", os.ErrNotExist
		}
		return denyReadFS{FileSystem: filesystem.NewLocal(stackRoot), baseName: "locked.conf"}, ".", nil
	}, func() []string { return []string{"local"} }, filepath.Join(t.TempDir(), "backups"))

	repository := prepareBindingRepository(t, service)
	remoteChange(t, repository.RemoteURL, "stacks/adguard/compose.yml", "services:\n  a:\n    image: alpine:3.23\n")
	remoteChange(t, repository.RemoteURL, "stacks/automation/compose.yml", "services:\n  b:\n    image: alpine:3.23\n")
	remoteChange(t, repository.RemoteURL, "stacks/automation/locked.conf", "hidden by an ACL\n")
	remoteChange(t, repository.RemoteURL, "stacks/whoami/compose.yml", "services:\n  c:\n    image: alpine:3.23\n")

	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"})
	require.NoError(t, err)
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)
	_, err = service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)

	// the user removes the file the ACLs kept Dockman from reading
	require.NoError(t, os.Remove(filepath.Join(stackRoot, "automation", "locked.conf")))
	// and meanwhile the two unrelated stacks get real work in Git
	remoteChange(t, repository.RemoteURL, "stacks/adguard/compose.yml", "services:\n  a:\n    image: alpine:3.24\n")
	remoteChange(t, repository.RemoteURL, "stacks/whoami/compose.yml", "services:\n  c:\n    image: alpine:3.24\n")

	result, err := service.RunBindingAutoSyncNow(context.Background(), binding.ID)
	require.NoError(t, err)

	// the link reports the decision, naming the only stack that needs it
	require.Equal(t, "blocked", result.State)
	require.Equal(t, []string{"automation/compose.yml"}, result.HeldBack)
	require.Contains(t, result.Message, "automation/compose.yml")

	// the unaffected stacks are green AND actually carry the new Git content
	states := stackStateByPath(t, service, binding.ID)
	require.Equal(t, stackSyncUpToDate, states["adguard/compose.yml"])
	require.Equal(t, stackSyncUpToDate, states["whoami/compose.yml"])
	require.Equal(t, stackSyncLocalDeleted, states["automation/compose.yml"])
	for _, stack := range []string{"adguard", "whoami"} {
		contents, readErr := os.ReadFile(filepath.Join(stackRoot, stack, "compose.yml"))
		require.NoError(t, readErr)
		require.Contains(t, string(contents), "alpine:3.24", stack+" must have received its Git change")
	}

	// the held-back stack was not touched: its deletion still awaits a decision
	_, statErr := os.Stat(filepath.Join(stackRoot, "automation", "locked.conf"))
	require.True(t, os.IsNotExist(statErr), "a local deletion must never be undone without an explicit decision")

	// and that decision is still reachable
	view, err := service.ListLocalStackDeletions(binding.ID, "automation/compose.yml")
	require.NoError(t, err)
	require.Len(t, view.Files, 1)
	require.Equal(t, "automation/locked.conf", view.Files[0].Path)
}

func TestHeldBackComposeStacksAttributesTheDecisionToItsOwnStack(t *testing.T) {
	paths := []string{"adguard/compose.yml", "automation/compose.yml", "whoami/compose.yml"}
	preview := TransferPreview{Entries: []PreviewEntry{
		{Path: "automation/locked.conf", Status: "add", ConflictKind: "destination_deleted"},
		{Path: "adguard/compose.yml", Status: "modify"},
		{Path: "whoami/notes.txt", Status: "add"},
	}}
	require.Equal(t, []string{"automation/compose.yml"}, heldBackComposeStacks(preview, paths))

	conflicting := TransferPreview{Entries: []PreviewEntry{
		{Path: "whoami/compose.yml", Status: "conflict", ConflictKind: "destination_changed"},
	}}
	require.Equal(t, []string{"whoami/compose.yml"}, heldBackComposeStacks(conflicting, paths))

	require.Empty(t, heldBackComposeStacks(TransferPreview{Entries: []PreviewEntry{
		{Path: "adguard/compose.yml", Status: "modify"},
	}}, paths))
}

func TestExcludeHeldBackStacksKeepsEveryOtherStackTransferable(t *testing.T) {
	paths := []string{"adguard/compose.yml", "automation/compose.yml"}
	selected := map[string]transferFile{
		"adguard/compose.yml":    {path: "adguard/compose.yml"},
		"adguard/config.yml":     {path: "adguard/config.yml"},
		"automation/compose.yml": {path: "automation/compose.yml"},
		"automation/locked.conf": {path: "automation/locked.conf"},
	}
	filtered, blocked := excludeHeldBackStacks(selected, paths, []string{"automation/compose.yml"})
	require.Equal(t, []string{"automation/compose.yml"}, blocked)
	require.Len(t, filtered, 2)
	require.Contains(t, filtered, "adguard/compose.yml")
	require.Contains(t, filtered, "adguard/config.yml")
	require.NotContains(t, filtered, "automation/locked.conf")

	unchanged, none := excludeHeldBackStacks(selected, paths, nil)
	require.Empty(t, none)
	require.Len(t, unchanged, 4)
}

// Upgrading must be enough: a link stopped by an older release, with Git
// unmoved since, would otherwise take the "no new Git commit" shortcut forever
// and never rescan the stacks it left behind.
func TestStartupSchedulesAFullScanForLinksStoppedOnADecision(t *testing.T) {
	service, db := testService(t, true)
	rows := []StackBinding{
		{UUID: "blocked-link", Host: "local", StackPath: "a", AutoSyncState: "blocked",
			AutoSyncError: "1 locally deleted synchronized file(s) require an explicit stack decision", LastAutoSyncCommit: "deadbeef"},
		{UUID: "conflicted-link", Host: "local", StackPath: "b", AutoSyncState: "conflict",
			AutoSyncError: "2 conflict(s) require a manual decision", LastAutoSyncCommit: "cafebabe"},
		{UUID: "preserved-link", Host: "local", StackPath: "c", AutoSyncState: "blocked",
			AutoSyncError: "3 Git deletion(s) preserved locally; choose restore", LastAutoSyncCommit: "feedface"},
		{UUID: "healthy-link", Host: "local", StackPath: "d", AutoSyncState: "up_to_date", LastAutoSyncCommit: "0ddba11"},
	}
	for i := range rows {
		require.NoError(t, db.Create(&rows[i]).Error)
	}

	unstuck, err := service.store.ClearStaleAutoSyncBlocks()
	require.NoError(t, err)
	require.Equal(t, int64(2), unstuck)

	commits := map[string]string{}
	var stored []StackBinding
	require.NoError(t, db.Find(&stored).Error)
	for _, row := range stored {
		commits[row.UUID] = row.LastAutoSyncCommit
	}
	require.Empty(t, commits["blocked-link"], "a blocked link must rescan after the upgrade")
	require.Empty(t, commits["conflicted-link"], "a conflicted link must rescan after the upgrade")
	// Remembering the commit is deliberate for preserved Git deletions: it keeps
	// the interval fetch-only until Git actually moves.
	require.Equal(t, "feedface", commits["preserved-link"])
	require.Equal(t, "0ddba11", commits["healthy-link"])
}
