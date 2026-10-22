package secrets

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// notMounted stands in for the mount table, which only exists on Linux. The
// path under test is the one taken when nothing is mounted yet.
func notMounted(t *testing.T) {
	t.Helper()
	previous := runtimePathIsMounted
	runtimePathIsMounted = func(string) (bool, error) { return false, nil }
	t.Cleanup(func() { runtimePathIsMounted = previous })
}

// Mounting needs root, so these cover the decision taken before the mount:
// what the service accepts to cover, and what it refuses.
//
// Reported from a homelab: the service had been down, the stack started
// anyway, and Docker created a directory for each bind mount whose source was
// missing. From then on every start refused - "refusing to cover a non-empty
// persistent .secrets directory" - and the outage was permanent until someone
// emptied the path by hand.
func TestTheEmptyMountPointsDockerLeavesAreCleared(t *testing.T) {
	notMounted(t)
	directory := filepath.Join(t.TempDir(), RuntimeDirectory)
	require.NoError(t, os.Mkdir(directory, 0o700))
	for _, name := range []string{"euro-office-jwt", "database-password"} {
		require.NoError(t, os.Mkdir(filepath.Join(directory, name), 0o755))
	}

	_, err := ensureRuntimeTmpfs(directory, 16)

	// The mount itself needs root; what matters here is that the path was
	// accepted and cleared instead of refused.
	if err != nil {
		require.NotContains(t, err.Error(), "refusing to cover")
	}
	remaining, readErr := os.ReadDir(directory)
	require.NoError(t, readErr)
	require.Empty(t, remaining, "the empty mount points must be gone")
}

// A file here is a secret written to persistent disk, which is exactly what
// the tmpfs exists to avoid. Covering it would hide it instead of dealing
// with it, so the refusal stands.
func TestASecretWrittenToDiskIsStillRefused(t *testing.T) {
	notMounted(t)
	directory := filepath.Join(t.TempDir(), RuntimeDirectory)
	require.NoError(t, os.Mkdir(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "token"), []byte("s3cr3t"), 0o600))

	_, err := ensureRuntimeTmpfs(directory, 16)
	require.ErrorContains(t, err, "refusing to cover")

	kept, readErr := os.ReadDir(directory)
	require.NoError(t, readErr)
	require.Len(t, kept, 1, "nothing is deleted on the refusal path")
}

// A directory holding something is not a mount point Docker made: it is data,
// and it is refused like a file.
func TestADirectoryHoldingSomethingIsRefused(t *testing.T) {
	notMounted(t)
	directory := filepath.Join(t.TempDir(), RuntimeDirectory)
	require.NoError(t, os.Mkdir(directory, 0o700))
	nested := filepath.Join(directory, "keys")
	require.NoError(t, os.Mkdir(nested, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(nested, "id"), []byte("s3cr3t"), 0o600))

	_, err := ensureRuntimeTmpfs(directory, 16)
	require.ErrorContains(t, err, "refusing to cover")
	_, statErr := os.Stat(filepath.Join(nested, "id"))
	require.NoError(t, statErr, "nothing is deleted on the refusal path")
}

// A symlink in place of the runtime directory is refused: it would put the
// secrets wherever it points.
func TestASymlinkedRuntimeDirectoryIsRefused(t *testing.T) {
	root := t.TempDir()
	elsewhere := filepath.Join(root, "elsewhere")
	require.NoError(t, os.Mkdir(elsewhere, 0o700))
	directory := filepath.Join(root, RuntimeDirectory)
	require.NoError(t, os.Symlink(elsewhere, directory))

	_, err := ensureRuntimeTmpfs(directory, 16)
	require.ErrorContains(t, err, "must be a real directory")
}
