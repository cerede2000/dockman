package secrets

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The local check refuses a foreign mount sitting on .secrets rather than
// calling it absent, because writing plaintext into a directory somebody else
// mounted is not the same event as writing it onto an empty one. The remote
// check only compared strings, so on an SSH host every one of those cases came
// back as a plain "not mounted" and Dockman went on to materialize.
func TestClassifyRuntimeMountMatchesTheLocalVerdicts(t *testing.T) {
	const path = "/server/stacks/media/.secrets"

	t.Run("managed tmpfs", func(t *testing.T) {
		mounted, err := classifyRuntimeMount(path, path+" tmpfs dockman-secrets\n")
		require.NoError(t, err)
		require.True(t, mounted)
	})

	t.Run("not a mount point of its own", func(t *testing.T) {
		// findmnt --target answers with the enclosing mount when the path is
		// not itself mounted, which is the ordinary "nothing here yet" case.
		mounted, err := classifyRuntimeMount(path, "/ ext4 /dev/sda2\n")
		require.NoError(t, err)
		require.False(t, mounted)
	})

	t.Run("empty output", func(t *testing.T) {
		mounted, err := classifyRuntimeMount(path, "")
		require.NoError(t, err)
		require.False(t, mounted)
	})

	t.Run("foreign filesystem on the mount point", func(t *testing.T) {
		_, err := classifyRuntimeMount(path, path+" ext4 /dev/sdb1\n")
		require.ErrorContains(t, err, "unmanaged mount")
	})

	t.Run("tmpfs from another source", func(t *testing.T) {
		_, err := classifyRuntimeMount(path, path+" tmpfs somebody-else\n")
		require.ErrorContains(t, err, "unmanaged mount",
			"the source is what tells our tmpfs from any other")
	})

	t.Run("path with a space", func(t *testing.T) {
		const spaced = "/server/my stacks/.secrets"
		mounted, err := classifyRuntimeMount(spaced, spaced+" tmpfs dockman-secrets\n")
		require.NoError(t, err)
		require.True(t, mounted, "the target is the only field that may contain spaces")
	})

	t.Run("escaped target, which is what findmnt -r actually prints", func(t *testing.T) {
		mounted, err := classifyRuntimeMount("/server/my stacks/.secrets",
			`/server/my\x20stacks/.secrets tmpfs dockman-secrets`+"\n")
		require.NoError(t, err)
		require.True(t, mounted)
	})

	t.Run("unusable output", func(t *testing.T) {
		mounted, err := classifyRuntimeMount(path, "garbage\n")
		require.NoError(t, err)
		require.False(t, mounted, "an answer we cannot read is not a mount we can trust")
	})
}

// The path reaches a remote shell, so a stack directory carrying a quote must
// not be able to end the argument and append a command of its own.
func TestRemoteRuntimeMountCommandQuotesThePath(t *testing.T) {
	command := RemoteRuntimeMountCommand("/stacks/it's here/.secrets")
	require.Contains(t, command, `'/stacks/it'"'"'s here/.secrets'`)
	require.NotContains(t, command, "; ")
}
