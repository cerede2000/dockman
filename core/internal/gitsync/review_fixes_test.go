package gitsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
)

func reviewFile(path, contents string) transferFile {
	sum := sha256.Sum256([]byte(contents))
	return transferFile{path: path, sha: hex.EncodeToString(sum[:]), size: int64(len(contents)),
		open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(contents)), nil }}
}

func reviewLink(t *testing.T) (*Service, string, Repository, BindingView) {
	t.Helper()
	service, _ := testService(t, true)
	stackRoot := configureTestStack(t, service)
	repository := prepareBindingRepository(t, service)
	remoteChange(t, repository.RemoteURL, "stacks/adguard/compose.yml", "services:\n  a:\n    image: alpine:3.23\n")
	remoteChange(t, repository.RemoteURL, "stacks/automation/compose.yml", "services:\n  b:\n    image: alpine:3.23\n")
	remoteChange(t, repository.RemoteURL, "stacks/whoami/compose.yml", "services:\n  c:\n    image: alpine:3.23\n")
	binding, err := service.CreateBinding(BindingInput{RepositoryID: repository.UUID, Host: "local", StackPath: "compose", SubPath: "stacks"})
	require.NoError(t, err)
	_, err = service.UpdateBindingAutomation(binding.ID, BindingAutomationInput{Enabled: true, IntervalMinutes: 5})
	require.NoError(t, err)
	_, err = service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	return service, stackRoot, repository, binding
}

// One compose file open with unsaved changes used to repaint EVERY stack of
// the link as "remote changes" - including the ones that had just been
// imported successfully - while the message claimed the opposite.
func TestAnOpenEditorOnlyHoldsBackItsOwnStack(t *testing.T) {
	service, stackRoot, repository, binding := reviewLink(t)
	service.ConfigureEditorCoherence(func(string) []string {
		return []string{"compose/automation/compose.yml"}
	}, nil)
	for _, stack := range []string{"adguard", "automation", "whoami"} {
		remoteChange(t, repository.RemoteURL, "stacks/"+stack+"/compose.yml", "services:\n  s:\n    image: alpine:3.24\n")
	}

	result, err := service.RunBindingAutoSyncNow(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "blocked", result.State)
	require.Contains(t, result.Message, "automation/compose.yml")

	states := stackStateByPath(t, service, binding.ID)
	require.Equal(t, stackSyncUpToDate, states["adguard/compose.yml"])
	require.Equal(t, stackSyncUpToDate, states["whoami/compose.yml"])
	require.Equal(t, stackSyncRemoteChanges, states["automation/compose.yml"], "the edited stack keeps its pending change visible")

	for _, stack := range []string{"adguard", "whoami"} {
		contents, readErr := os.ReadFile(filepath.Join(stackRoot, stack, "compose.yml"))
		require.NoError(t, readErr)
		require.Contains(t, string(contents), "alpine:3.24", stack+" must have been imported")
	}
	edited, err := os.ReadFile(filepath.Join(stackRoot, "automation", "compose.yml"))
	require.NoError(t, err)
	require.Contains(t, string(edited), "alpine:3.23", "the edited stack must never be overwritten")
}

// A conflict is a decision the operator still owes. A cycle that never scanned
// used to erase it, showing green while two versions genuinely disagreed.
func TestAPendingConflictSurvivesASkippedScan(t *testing.T) {
	service, _, _, binding := reviewLink(t)
	require.NoError(t, service.store.UpdateGitStackStatuses(binding.ID, []string{"adguard/compose.yml"},
		map[string]any{"state": stackSyncConflict, "conflict_count": 2, "error_message": "2 conflicts to decide"}))

	result, err := service.RunBindingAutoSync(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Contains(t, result.Message, "stack scan skipped")

	rows, err := service.store.GitStackStatuses(binding.ID)
	require.NoError(t, err)
	for _, row := range rows {
		if row.ComposePath == "adguard/compose.yml" {
			require.Equal(t, stackSyncConflict, row.State)
			require.Equal(t, 2, row.ConflictCount)
			return
		}
	}
	t.Fatal("adguard/compose.yml status row is missing")
}

// An unresolved conflict is never selected for transfer, so the stack owning it
// is absent from the transfer's own filtered list. Reporting from that list
// made the conflict vanish from the result while every other stack synced.
func TestAConflictStaysVisibleWhileTheOtherStacksSynchronize(t *testing.T) {
	service, stackRoot, repository, binding := reviewLink(t)
	local := filepath.Join(stackRoot, "adguard", "compose.yml")
	require.NoError(t, os.WriteFile(local, []byte("services:\n  a:\n    image: changed-locally\n"), 0o644))
	remoteChange(t, repository.RemoteURL, "stacks/whoami/compose.yml", "services:\n  c:\n    image: alpine:3.24\n")

	result, err := service.RunBindingAutoSyncNow(context.Background(), binding.ID)
	require.NoError(t, err)
	require.Equal(t, "conflict", result.State)
	require.Equal(t, []string{"adguard/compose.yml"}, result.HeldBack)

	states := stackStateByPath(t, service, binding.ID)
	require.Equal(t, stackSyncConflict, states["adguard/compose.yml"])
	require.Equal(t, stackSyncUpToDate, states["whoami/compose.yml"])

	kept, err := os.ReadFile(local)
	require.NoError(t, err)
	require.Contains(t, string(kept), "changed-locally", "a conflicted file must never be overwritten")
	synced, err := os.ReadFile(filepath.Join(stackRoot, "whoami", "compose.yml"))
	require.NoError(t, err)
	require.Contains(t, string(synced), "alpine:3.24")
}

// Deleted locally AND changed on Git since is the riskiest shape of the two,
// not the safest: restoring it silently would undo a deliberate deletion with
// content the operator never saw. The export direction always raised a
// conflict here; the import direction restored without asking.
func TestADeletionRacedByAGitChangeRequiresADecision(t *testing.T) {
	git := reviewFile("web/config.yml", "version from git\n")

	sameOnGit := buildPreview("b", "repository_to_stack",
		map[string]transferFile{"web/config.yml": git}, map[string]transferFile{},
		map[string]string{"web/config.yml": git.sha})
	require.Equal(t, "add", sameOnGit.Entries[0].Status)
	require.Equal(t, "destination_deleted", sameOnGit.Entries[0].ConflictKind)
	require.Equal(t, 1, sameOnGit.LocalDeletions)
	require.Equal(t, 0, sameOnGit.Conflicts)

	changedOnGit := buildPreview("b", "repository_to_stack",
		map[string]transferFile{"web/config.yml": git}, map[string]transferFile{},
		map[string]string{"web/config.yml": "the-baseline-git-has-moved-past"})
	require.Equal(t, "conflict", changedOnGit.Entries[0].Status)
	require.Equal(t, "destination_deleted_source_changed", changedOnGit.Entries[0].ConflictKind)
	require.Equal(t, 1, changedOnGit.Conflicts)

	// a file that was never synchronized stays an ordinary add
	neverSynced := buildPreview("b", "repository_to_stack",
		map[string]transferFile{"web/config.yml": git}, map[string]transferFile{}, map[string]string{})
	require.Equal(t, "add", neverSynced.Entries[0].Status)
	require.Empty(t, neverSynced.Entries[0].ConflictKind)
	require.Equal(t, 0, neverSynced.Conflicts)
}

func TestADirectoryRuleDoesNotMatchAFileOfTheSameName(t *testing.T) {
	rules, err := rulesFromPatterns([]string{"/data/"})
	require.NoError(t, err)
	policy := syncPolicy{profile: syncProfileComposeConfig, excludes: rules}
	require.True(t, policy.excludesPath("data", true, nil), "the directory itself")
	require.True(t, policy.excludesPath("data/runtime.db", false, nil), "and its subtree")
	require.False(t, policy.excludesPath("data", false, nil), "but not a file that carries the name")
}

// An explicit decision used to be refused the instant an automatic cycle held
// the link's lock. A cycle holds it for its whole run - scan, import, and a
// controlled deployment that waits up to a minute per stack - so on a link
// that rescans every interval there was barely a window to decide anything.
func TestAnExplicitDecisionWaitsForARunningCycle(t *testing.T) {
	lock := &sync.Mutex{}
	lock.Lock()
	released := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		lock.Unlock()
		close(released)
	}()
	require.True(t, waitForLock(lock, 2*time.Second), "a decision must outlast a short cycle")
	<-released
	lock.Unlock()

	// Bounded on purpose: the caller is a request, and blocking it for the
	// length of a deployment would be worse than telling the operator what to do.
	held := &sync.Mutex{}
	held.Lock()
	defer held.Unlock()
	start := time.Now()
	require.False(t, waitForLock(held, 200*time.Millisecond))
	require.Less(t, time.Since(start), 2*time.Second, "the wait must stay bounded")
}

// A linked folder can sit inside a very large shared repository. The
// ahead/behind walk is bounded on purpose, but giving up used to be
// indistinguishable from "no distance": both counters came back zero, every
// guard that reads them passed, and the automatic cycle skipped its pull
// because Behind was not greater than zero. The link stopped synchronizing and
// said nothing at all.
func TestAnUnmeasurableDistanceIsReportedRatherThanReadAsZero(t *testing.T) {
	service, _ := testService(t, true)
	repository := prepareBindingRepository(t, service)
	repo, err := service.openRepository(repository)
	require.NoError(t, err)

	head, err := repo.Reference(plumbing.NewBranchReferenceName(repository.DefaultBranch), true)
	require.NoError(t, err)

	// a hash that is reachable from nothing: the walk drains without ever
	// finding it, which is the same exit as exhausting the bound
	unreachable := plumbing.NewHash("0123456789abcdef0123456789abcdef01234567")
	distance, measured := commitDistance(repo, head.Hash(), unreachable)
	require.False(t, measured, "an unfinished walk must say so")
	require.Zero(t, distance)

	// and a real ancestor is still measured exactly
	self, selfMeasured := commitDistance(repo, head.Hash(), head.Hash())
	require.True(t, selfMeasured)
	require.Zero(t, self)
}

// The managed key is created and kept at 0600, but a key file the operator
// points Dockman at was never inspected at all - while the secrets subsystem
// refuses one with any group or other bits. Anyone who can read it can decrypt
// every stored Git credential.
func TestAnExposedMasterKeyIsReported(t *testing.T) {
	directory := t.TempDir()
	exposed := filepath.Join(directory, "shared.key")
	require.NoError(t, os.WriteFile(exposed, []byte(strings.Repeat("k", 32)), 0o644))

	var reported bytes.Buffer
	previous := log.Logger
	log.Logger = zerolog.New(&reported)
	defer func() { log.Logger = previous }()

	vault, keyFile, err := LoadOrCreateVault(directory, exposed)
	require.NoError(t, err)
	require.NotNil(t, vault)
	require.Equal(t, exposed, keyFile)
	require.Contains(t, reported.String(), "readable beyond its owner")
	require.Contains(t, reported.String(), "chmod 600")

	// a key only its owner can read says nothing
	reported.Reset()
	private := filepath.Join(directory, "private.key")
	require.NoError(t, os.WriteFile(private, []byte(strings.Repeat("k", 32)), 0o600))
	_, _, err = LoadOrCreateVault(directory, private)
	require.NoError(t, err)
	require.Empty(t, reported.String())

	// and the managed key is created restricted, so it never warns either
	reported.Reset()
	_, managedPath, err := LoadOrCreateVault(directory, "")
	require.NoError(t, err)
	info, err := os.Stat(managedPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.Empty(t, reported.String())
}
