package gitsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/stretchr/testify/require"
)

func autoExcludeService(t *testing.T, denied *bool) (*Service, string) {
	t.Helper()
	service, _ := testService(t, true)
	stackRoot := t.TempDir()
	service.ConfigureStackAccess(func(host, stackPath string) (filesystem.FileSystem, string, error) {
		if host != "local" || stackPath != "compose" {
			return nil, "", os.ErrNotExist
		}
		name := "locked.conf"
		if !*denied {
			name = "\x00never-matches"
		}
		return denyReadFS{FileSystem: filesystem.NewLocal(stackRoot), baseName: name}, ".", nil
	}, func() []string { return []string{"local"} }, filepath.Join(t.TempDir(), "backups"))
	return service, stackRoot
}

// A file the host's ACLs hide is a fact about the host, not a fault Dockman can
// repair. It is held out in the very cycle that discovers it - the link never
// goes amber for it - and the operator is told once, by name.
func TestUnreadableFileIsHeldOutWithoutTurningTheLinkAmber(t *testing.T) {
	denied := true
	service, stackRoot := autoExcludeService(t, &denied)
	repository := prepareBindingRepository(t, service)
	remoteChange(t, repository.RemoteURL, "stacks/automation/compose.yml", "services:\n  b:\n    image: alpine:3.23\n")
	remoteChange(t, repository.RemoteURL, "stacks/automation/locked.conf", "hidden by an ACL\n")

	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"})
	require.NoError(t, err)
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)
	_, err = service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)

	// first cycle that actually sees the unreadable file: reported, and recorded
	reported, err := service.RunBindingAutoSyncNow(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", reported.State, "an unreadable local file must not hold the link amber")
	require.Equal(t, []string{"automation/locked.conf"}, reported.AutoExcluded)
	require.Contains(t, reported.Message, "automation/locked.conf")
	require.Contains(t, reported.Message, "cannot read")

	stored, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"automation/locked.conf"}, splitPatternLines(stored.AutoExcludedPaths))
	baseline, err := service.store.BindingBaseline(binding.ID)
	require.NoError(t, err)
	require.NotContains(t, baseline, "automation/locked.conf", "a held-out path must leave the baseline")

	// every later cycle is quiet, and the stack is green
	quiet, err := service.RunBindingAutoSyncNow(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "up_to_date", quiet.State)
	require.Empty(t, quiet.SyncFailed)
	require.Equal(t, stackSyncUpToDate, stackStateByPath(t, service, binding.ID)["automation/compose.yml"])

	// and the file itself was never touched in either direction
	contents, err := os.ReadFile(filepath.Join(stackRoot, "automation", "locked.conf"))
	require.NoError(t, err)
	require.Equal(t, "hidden by an ACL\n", string(contents))
}

// Deleting a held-out file must stay a non-event: it is outside synchronization,
// so it neither demands a decision nor comes back from Git on its own.
func TestDeletingAHeldOutFileNeitherBlocksNorRestores(t *testing.T) {
	denied := true
	service, stackRoot := autoExcludeService(t, &denied)
	repository := prepareBindingRepository(t, service)
	remoteChange(t, repository.RemoteURL, "stacks/automation/compose.yml", "services:\n  b:\n    image: alpine:3.23\n")
	remoteChange(t, repository.RemoteURL, "stacks/automation/locked.conf", "hidden by an ACL\n")

	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"})
	require.NoError(t, err)
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)
	_, err = service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	_, err = service.RunBindingAutoSyncNow(context.Background(), binding.ID)
	require.NoError(t, err)

	require.NoError(t, os.Remove(filepath.Join(stackRoot, "automation", "locked.conf")))
	after, err := service.RunBindingAutoSyncNow(context.Background(), binding.ID)
	require.NoError(t, err)

	require.Equal(t, "up_to_date", after.State, "a held-out file is outside synchronization; removing it decides nothing")
	require.Empty(t, after.HeldBack)
	require.Equal(t, stackSyncUpToDate, stackStateByPath(t, service, binding.ID)["automation/compose.yml"])
	_, statErr := os.Stat(filepath.Join(stackRoot, "automation", "locked.conf"))
	require.True(t, os.IsNotExist(statErr), "Dockman must not restore a file the operator removed from outside its scope")
}

// Fixing the ACL is all it takes: the path rejoins synchronization on its own.
func TestAPathThatBecomesReadableRejoinsSynchronization(t *testing.T) {
	denied := true
	service, stackRoot := autoExcludeService(t, &denied)
	repository := prepareBindingRepository(t, service)
	remoteChange(t, repository.RemoteURL, "stacks/automation/compose.yml", "services:\n  b:\n    image: alpine:3.23\n")
	remoteChange(t, repository.RemoteURL, "stacks/automation/locked.conf", "hidden by an ACL\n")

	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"})
	require.NoError(t, err)
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)
	_, err = service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	_, err = service.RunBindingAutoSyncNow(context.Background(), binding.ID)
	require.NoError(t, err)
	stored, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.NotEmpty(t, splitPatternLines(stored.AutoExcludedPaths))

	// the operator fixes the ACL
	denied = false
	_, err = service.RunBindingAutoSyncNow(context.Background(), binding.ID)
	require.NoError(t, err)

	recovered, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Empty(t, splitPatternLines(recovered.AutoExcludedPaths), "a readable path must rejoin synchronization on its own")
	baseline, err := service.store.BindingBaseline(binding.ID)
	require.NoError(t, err)
	require.Contains(t, baseline, "automation/locked.conf")
	_ = stackRoot
}

func TestMergeUnreadablePathsIsBoundedAndStable(t *testing.T) {
	merged, added, changed := mergeUnreadablePaths([]string{"b/two.conf"}, []string{"a/one.conf", "b/two.conf", " a/one.conf "})
	require.True(t, changed)
	require.Equal(t, []string{"a/one.conf"}, added)
	require.Equal(t, []string{"a/one.conf", "b/two.conf"}, merged)

	_, _, unchanged := mergeUnreadablePaths([]string{"a/one.conf"}, []string{"a/one.conf"})
	require.False(t, unchanged)

	full := make([]string, 0, maxAutoExcludedPaths)
	for i := 0; i < maxAutoExcludedPaths; i++ {
		full = append(full, filepath.ToSlash(filepath.Join("stack", "file", string(rune('a'+i%26))+string(rune('a'+i/26))))+".conf")
	}
	capped, _, _ := mergeUnreadablePaths(full, []string{"stack/overflow.conf"})
	require.Len(t, capped, maxAutoExcludedPaths, "the list must never grow past its bound")
}

// Saving a policy is the operator saying "look again": what was held out for
// being unreadable is dropped so the next cycle re-evaluates it, instead of
// carrying an exclusion whose reason is no longer visible anywhere.
func TestSavingAPolicyClearsWhatWasHeldOutForBeingUnreadable(t *testing.T) {
	denied := true
	service, _ := autoExcludeService(t, &denied)
	repository := prepareBindingRepository(t, service)
	remoteChange(t, repository.RemoteURL, "stacks/automation/compose.yml", "services:\n  b:\n    image: alpine:3.23\n")
	remoteChange(t, repository.RemoteURL, "stacks/automation/locked.conf", "hidden by an ACL\n")

	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"})
	require.NoError(t, err)
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)
	_, err = service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	_, err = service.RunBindingAutoSyncNow(context.Background(), binding.ID)
	require.NoError(t, err)
	held, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.NotEmpty(t, splitPatternLines(held.AutoExcludedPaths))

	view, err := service.UpdateBindingPolicy(binding.ID, BindingPolicyInput{
		Profile: syncProfileAllFiles, ExcludePatterns: []string{"/never-matches"},
	})
	require.NoError(t, err)
	require.Empty(t, view.AutoExcludedPaths)
	cleared, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Empty(t, splitPatternLines(cleared.AutoExcludedPaths))
}
