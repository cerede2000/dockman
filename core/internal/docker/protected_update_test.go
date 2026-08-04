package docker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeProtectedComposeFiles(t *testing.T) {
	t.Parallel()

	files, err := normalizeProtectedComposeFiles("compose.yml,overrides/socketproxy.yml", "/srv/stacks/infrastructure")
	require.NoError(t, err)
	require.Equal(t, []string{
		"/srv/stacks/infrastructure/compose.yml",
		"/srv/stacks/infrastructure/overrides/socketproxy.yml",
	}, files)
}

func TestNormalizeProtectedComposeFilesRejectsUnsafeSeparator(t *testing.T) {
	t.Parallel()

	_, err := normalizeProtectedComposeFiles("/srv/stacks/infra:bad/compose.yml", "/srv/stacks/infra")
	require.ErrorContains(t, err, "invalid Compose file metadata")
}

func TestProtectedUpdateMountsAreBoundedAndReadOnly(t *testing.T) {
	t.Parallel()

	mounts, err := protectedUpdateMounts(protectedUpdateTarget{
		workingDir: "/srv/stacks/infrastructure",
		files:      []string{"/srv/stacks/infrastructure/compose.yml", "/srv/stacks/infrastructure/override.yml"},
	})
	require.NoError(t, err)
	require.Len(t, mounts, 2)
	require.Equal(t, "/var/run/docker.sock", mounts[0].Source)
	require.Equal(t, "/srv/stacks", mounts[1].Source)
	require.True(t, mounts[1].ReadOnly)
}

func TestProtectedUpdateMountsRefuseHostRoot(t *testing.T) {
	t.Parallel()

	_, err := protectedUpdateMounts(protectedUpdateTarget{workingDir: "/", files: []string{"/compose.yml"}})
	require.ErrorContains(t, err, "unsafe Compose bind path")
}
