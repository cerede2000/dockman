package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeStack creates a deployable stack: a compose file, and the encrypted
// runtime pair when asked for one.
func writeStack(t *testing.T, root, relative string, encrypted bool) string {
	t.Helper()
	directory := filepath.Join(root, relative)
	require.NoError(t, os.MkdirAll(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "compose.yaml"), []byte("services: {}\n"), 0o600))
	if encrypted {
		require.NoError(t, os.WriteFile(filepath.Join(directory, SOPSInlineMarkerFile), []byte("version=1\n"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(directory, SOPSSourceFile), []byte("ciphertext"), 0o600))
	}
	return directory
}

// fill puts count files in a stack's data directory, the way a running
// container does.
func fill(t *testing.T, stack, name string, count int) {
	t.Helper()
	data := filepath.Join(stack, name)
	require.NoError(t, os.MkdirAll(data, 0o700))
	for index := 0; index < count; index++ {
		require.NoError(t, os.WriteFile(filepath.Join(data, fmt.Sprintf("file-%d", index)), []byte("x"), 0o600))
	}
}

// Reported from a homelab: the service refused to start at all - "stack root
// holds more than 200000 entries" - because one stack's CI runner workspace
// held 187 000 files. None of it could contain a stack, and every other
// stack lost its secrets over it.
func TestAStacksOwnDataIsNotSearchedForStacks(t *testing.T) {
	root := t.TempDir()
	runner := writeStack(t, root, "gitrunner", false)
	fill(t, runner, "data", 400)
	// even a marker buried in that data belongs to the runner's files, not to
	// a stack anyone deploys
	require.NoError(t, os.WriteFile(filepath.Join(runner, "data", SOPSInlineMarkerFile), []byte("version=1\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(runner, "data", SOPSSourceFile), []byte("ciphertext"), 0o600))
	secretive := writeStack(t, root, "myfin", true)
	fill(t, secretive, "data", 50)

	discovery, err := discoverEncryptedStacks(root)
	require.NoError(t, err)
	require.Equal(t, []string{secretive}, discovery.Ready)
	require.Empty(t, discovery.Problems)
	require.False(t, discovery.Truncated)
	require.Less(t, discovery.Visited, 20,
		"the search must stop at each stack, not read its data: %d entries", discovery.Visited)
}

// Stacks grouped in folders are still found: a folder without a compose file
// is not a stack, so the search goes through it.
func TestStacksGroupedInFoldersAreStillFound(t *testing.T) {
	root := t.TempDir()
	first := writeStack(t, root, "apps/first", true)
	second := writeStack(t, root, "apps/media/second", true)
	plain := writeStack(t, root, "apps/third", false)
	fill(t, plain, "config", 30)

	discovery, err := discoverEncryptedStacks(root)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{first, second}, discovery.Ready)
}

// The budget is a safety net, not a refusal: what was found is materialized,
// and the truncation is reported instead of taking the whole pass down.
func TestABudgetRunningOutKeepsWhatWasFound(t *testing.T) {
	root := t.TempDir()
	writeStack(t, root, "a-first", true)
	for index := 0; index < 40; index++ {
		writeStack(t, root, fmt.Sprintf("filler-%02d", index), false)
	}

	discovery, err := discoverEncryptedStacksBounded(root, 5)
	require.NoError(t, err, "a root too large to search is not a reason to mount nothing")
	require.True(t, discovery.Truncated)
	require.ErrorContains(t, errors.Join(discovery.Problems...), "the rest was not searched")
	require.Equal(t, []string{filepath.Join(root, "a-first")}, discovery.Ready,
		"the stacks found before the budget ran out are still materialized")
}

// A stack whose own directory is the root keeps working: pruning there would
// find nothing at all.
func TestAStackAtTheRootIsStillFound(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, SOPSInlineMarkerFile), []byte("version=1\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, SOPSSourceFile), []byte("ciphertext"), 0o600))
	beside := writeStack(t, root, "other", true)

	discovery, err := discoverEncryptedStacks(root)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{root, beside}, discovery.Ready)
}

// An encrypted stack with no compose file is still a stack: the marker alone
// is what claims encryption.
func TestAMarkerWithoutAComposeFileIsStillClaimed(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "orphan")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, SOPSInlineMarkerFile), []byte("version=1\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, SOPSSourceFile), []byte("ciphertext"), 0o600))

	discovery, err := discoverEncryptedStacks(root)
	require.NoError(t, err)
	require.Equal(t, []string{directory}, discovery.Ready)
}
