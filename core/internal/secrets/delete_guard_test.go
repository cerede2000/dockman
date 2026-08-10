package secrets

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Deleting .secrets itself is not an ordinary folder deletion: it is the live
// mount point the host daemon owns. RemoveAll would wipe the materialized
// plaintext of a running stack and then fail on the mount with EBUSY, leaving
// the stack without its secrets for nothing.
func TestRuntimeDirectoryOwnerRecognisesTheMountPoint(t *testing.T) {
	stack, isRuntime := runtimeDirectoryOwner("media/plex/.secrets")
	require.True(t, isRuntime)
	require.Equal(t, "media/plex", stack)

	stack, isRuntime = runtimeDirectoryOwner("/media/plex/.secrets/")
	require.True(t, isRuntime, "leading and trailing separators must not hide it")
	require.Equal(t, "media/plex", stack)

	_, isRuntime = runtimeDirectoryOwner("media/plex")
	require.False(t, isRuntime)
	_, isRuntime = runtimeDirectoryOwner("media/plex/.secrets/nested")
	require.False(t, isRuntime, "only the directory itself, not what is inside it")
	_, isRuntime = runtimeDirectoryOwner("")
	require.False(t, isRuntime)
}
