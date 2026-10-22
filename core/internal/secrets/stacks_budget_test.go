package secrets

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/stretchr/testify/require"
)

// The stack catalog of the interface walks the same tree as the host runtime,
// with a budget of directories rather than entries. A stack's data used to
// spend it: one homelab had a runner workspace of 187 000 files beside its
// compose file, and the stacks queued behind it were never listed - the
// screen offered a catalog missing exactly the stacks an operator would want
// to encrypt.
func TestAStacksDataDoesNotSpendTheCatalogBudget(t *testing.T) {
	root := t.TempDir()

	// A stack whose data is a wide tree, as a runner leaves it. The search is
	// breadth-first, so this costs the budget that the stacks sitting deeper
	// in the tree need.
	runner := filepath.Join(root, "a-runner")
	require.NoError(t, os.MkdirAll(runner, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(runner, "compose.yaml"), []byte("services: {}\n"), 0o600))
	for index := 0; index < maxStackDiscoveryDirectories+200; index++ {
		require.NoError(t, os.MkdirAll(filepath.Join(runner, "data", fmt.Sprintf("job-%04d", index)), 0o700))
	}

	// a stack grouped two folders deep, the layout the interface encourages
	nested := filepath.Join(root, "group", "media", "z-ytzero")
	require.NoError(t, os.MkdirAll(nested, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(nested, "compose.yaml"), []byte("services: {}\n"), 0o600))

	listed := discoverAliasStacks(filesystem.NewLocal(root), root, "compose", maxStackDiscoveryResults)

	paths := make([]string, 0, len(listed))
	for _, option := range listed {
		paths = append(paths, option.Path)
	}
	require.Contains(t, paths, "compose/a-runner")
	require.Contains(t, paths, "compose/group/media/z-ytzero",
		"a grouped stack must be listed even when another stack holds a large data tree: %v", paths)
}
