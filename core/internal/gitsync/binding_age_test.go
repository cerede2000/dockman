package gitsync

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/stretchr/testify/require"
)

// An age identity decrypts every SOPS source in the repository. Its
// conventional names carry no telling extension and contain neither "secret"
// nor "credential", so the name-based rules never saw one: age-key.txt was
// collected as ordinary text and pushed in the clear.
func TestAgeIdentityIsNeverTransferredEvenWithSensitiveOptIn(t *testing.T) {
	root := t.TempDir()
	stack := filepath.Join(root, "app")
	require.NoError(t, os.MkdirAll(stack, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(stack, "compose.yml"), []byte("services: {}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(stack, "age-key.txt"),
		[]byte("# created: 2026-01-01\n# public key: age1test\nAGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ\n"), 0o600))

	// includeSensitive=true: even the typed confirmation must not let it out.
	files, err := collectStackFiles(filesystem.NewLocal(root), "app", true)
	require.NoError(t, err)

	identity, listed := files["age-key.txt"]
	require.True(t, listed, "the file must be reported, not silently dropped")
	require.Equal(t, "age_identity", identity.skipReason)
	require.Nil(t, identity.open, "a skipped identity must carry no reader")

	compose, ok := files["compose.yml"]
	require.True(t, ok)
	require.Empty(t, compose.skipReason, "ordinary stack files are unaffected")
}

func TestIsAgeIdentityIgnoresOrdinaryAndOversizedFiles(t *testing.T) {
	reader := func(value string) func() (io.ReadCloser, error) {
		return func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(value)), nil }
	}
	require.False(t, isAgeIdentity(12, reader("hello world!")))
	require.False(t, isAgeIdentity(0, reader("")))
	require.False(t, isAgeIdentity(maxAgeIdentityScan+1, reader("AGE-SECRET-KEY-1XXXX")))
	require.True(t, isAgeIdentity(20, reader("AGE-SECRET-KEY-1XXXX")))
	require.True(t, isAgeIdentity(20, reader("age-secret-key-1xxxx")), "the marker is matched case-insensitively")
}
