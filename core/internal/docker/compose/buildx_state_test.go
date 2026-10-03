package compose

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// Buildx keeps its state in a directory Dockman hands it, and until now that
// was /tmp whoever ran the build. Under a read-only root filesystem - the
// hardening switch of NAS appliances, where the deployment that found this
// came from - /tmp is not writable in this container, so Dockman's builds had
// nowhere to put that state. The entrypoint points TMPDIR at the writable
// configuration volume instead, and a local build must follow it.
func TestALocalBuildKeepsBuildxStateWhereTemporaryFilesAreAllowed(t *testing.T) {
	writable := t.TempDir()
	t.Setenv("TMPDIR", writable)

	service := NewComposeTerminal("local", nil, nil, nil)

	require.Equal(t, filepath.Join(writable, "dockman-buildx-native"), service.nativeBuildxConfig())
	require.Equal(t, filepath.Join(writable, "dockman-buildx-limited"), service.limitedBuilderConfig())
}

// An SSH host runs the build on its own machine, which knows nothing of this
// container's TMPDIR: its builds keep using its own /tmp.
func TestAnSSHHostKeepsBuildxStateInItsOwnTemporaryDirectory(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	service := NewComposeTerminal("nas", nil, nil, &ssh.Client{})

	require.Equal(t, "/tmp/dockman-buildx-native", service.nativeBuildxConfig())
	require.Equal(t, "/tmp/dockman-buildx-limited", service.limitedBuilderConfig())
}

// And the directory reaches the commands, not just the accessor.
func TestTheBuildCommandsCarryThatDirectory(t *testing.T) {
	writable := t.TempDir()
	root := t.TempDir()
	runner := &limitsRunner{model: buildableModel}
	service := &Service{
		TTY:         true,
		runner:      runner,
		hostname:    "state-host",
		tempBase:    writable,
		buildLimits: oneAndAHalfCPUsTwoGiB,
		parser: func(string, string) (Host, error) {
			return Host{Fs: filesystem.NewLocal(root), Relpath: "app/compose.yaml"}, nil
		},
	}

	require.NoError(t, service.Up(context.Background(), "app/compose.yaml", io.Discard))

	create, _, ok := runner.find("buildx", "create", "--name", limitedBuilderName)
	require.True(t, ok, "%v", runner.joined())
	require.Equal(t, "BUILDX_CONFIG="+filepath.Join(writable, "dockman-buildx-limited"), create.cmd[1])
	up, _, ok := runner.find("up", "--build")
	require.True(t, ok)
	require.Contains(t, up.env, "BUILDX_CONFIG="+filepath.Join(writable, "dockman-buildx-limited"))
	require.NotContains(t, strings.Join(create.cmd, " "), "/tmp/dockman-buildx",
		"nothing may fall back to a directory a read-only container cannot write")
}
