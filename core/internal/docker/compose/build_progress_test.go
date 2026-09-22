package compose

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/stretchr/testify/require"
)

// progressRunner answers the Compose calls an action makes: the binary probe
// succeeds, `config --format json` prints the model it holds (or fails), and
// every command line is recorded so the test can read the progress flag the
// real action ran with.
type progressRunner struct {
	model     string
	configErr error
	// upStderr, when set, is what the up call prints before failing
	upStderr string
	calls    [][]string
}

func (r *progressRunner) Run(_ context.Context, cmd []string, _ string, _ []string, out io.Writer, errOut io.Writer) error {
	r.calls = append(r.calls, slices.Clone(cmd))
	if slices.Contains(cmd, "config") {
		if r.configErr != nil {
			return r.configErr
		}
		_, _ = io.WriteString(out, r.model)
		return nil
	}
	if slices.Contains(cmd, "up") && r.upStderr != "" {
		_, _ = io.WriteString(errOut, r.upStderr)
		return errors.New("exit status 1")
	}
	return nil
}

// progressOf returns the --progress flag of the recorded call running verb.
func (r *progressRunner) progressOf(t *testing.T, verb string) string {
	t.Helper()
	for _, call := range r.calls {
		if !slices.Contains(call, verb) || slices.Contains(call, "config") {
			continue
		}
		for _, arg := range call {
			if len(arg) > len("--progress=") && arg[:len("--progress=")] == "--progress=" {
				return arg
			}
		}
		t.Fatalf("the %s call carries no progress flag: %v", verb, call)
	}
	t.Fatalf("no %s call was made: %v", verb, r.calls)
	return ""
}

const buildableModel = `{"services":{"web":{"build":{"context":"."}},"db":{"image":"postgres:16"}}}`
const pulledModel = `{"services":{"web":{"image":"nginx:1.27"},"db":{"image":"postgres:16"}}}`

func progressService(t *testing.T, runner *progressRunner) *Service {
	root := t.TempDir()
	return &Service{
		TTY:    true,
		runner: runner,
		parser: func(string, string) (Host, error) {
			return Host{Fs: filesystem.NewLocal(root), Relpath: "app/compose.yaml"}, nil
		},
	}
}

// Reported: "up" on a compose file with a build section failed and never
// built the image. Under --progress=tty, Buildx bake needs a console that
// Dockman does not give Compose: "failed to get console: provided file is not
// a console". Reproduced in CI against the published image, in every setup.
func TestUpBuildsAStackWithABuildSectionUnderPlainProgress(t *testing.T) {
	runner := &progressRunner{model: buildableModel}
	require.NoError(t, progressService(t, runner).Up(context.Background(), "app/compose.yaml", io.Discard))

	require.Equal(t, "--progress=plain", runner.progressOf(t, "up"))
}

// Stacks of pulled images keep the tty display they always had.
func TestUpKeepsTheTtyDisplayWhenNothingIsBuilt(t *testing.T) {
	runner := &progressRunner{model: pulledModel}
	require.NoError(t, progressService(t, runner).Up(context.Background(), "app/compose.yaml", io.Discard))

	require.Equal(t, "--progress=tty", runner.progressOf(t, "up"))
}

// Plain works with or without a build; the up that follows reports why the
// model could not be read.
func TestUpFallsBackToPlainWhenTheModelCannotBeRead(t *testing.T) {
	runner := &progressRunner{configErr: errors.New("yaml: line 3: did not find expected key")}
	require.NoError(t, progressService(t, runner).Up(context.Background(), "app/compose.yaml", io.Discard))

	require.Equal(t, "--progress=plain", runner.progressOf(t, "up"))
}

// Redeploy builds a missing image even without its build switch, so it follows
// the stack, not the switch.
func TestRedeployFollowsTheStackNotTheBuildSwitch(t *testing.T) {
	for _, build := range []bool{true, false} {
		runner := &progressRunner{model: buildableModel}
		require.NoError(t, progressService(t, runner).Redeploy(context.Background(), "app/compose.yaml", io.Discard, false, build, false))
		require.Equal(t, "--progress=plain", runner.progressOf(t, "up"), "build switch %v", build)
	}

	runner := &progressRunner{model: pulledModel}
	require.NoError(t, progressService(t, runner).Redeploy(context.Background(), "app/compose.yaml", io.Discard, true, true, true))
	require.Equal(t, "--progress=tty", runner.progressOf(t, "up"))
}

// Update pulls (never builds: --ignore-buildable) then ups; only the up that
// may build changes mode.
func TestUpdatePullsUnderTtyAndBuildsUnderPlain(t *testing.T) {
	runner := &progressRunner{model: buildableModel}
	require.NoError(t, progressService(t, runner).Update(context.Background(), "app/compose.yaml", io.Discard))

	require.Equal(t, "--progress=tty", runner.progressOf(t, "pull"))
	require.Equal(t, "--progress=plain", runner.progressOf(t, "up"))
}

// The model is read without interpolation or env resolution, so reading it to
// choose a progress mode never exposes a stack's inline secrets.
func TestTheProgressProbeReadsTheModelWithoutResolvingValues(t *testing.T) {
	runner := &progressRunner{model: buildableModel}
	require.NoError(t, progressService(t, runner).Up(context.Background(), "app/compose.yaml", io.Discard))

	var probe []string
	for _, call := range runner.calls {
		if slices.Contains(call, "config") {
			probe = call
		}
	}
	require.NotNil(t, probe)
	require.Contains(t, probe, "--no-interpolate")
	require.Contains(t, probe, "--no-env-resolution")
}

// What a socket proxy blocking BuildKit's /grpc endpoint returns (reproduced
// in CI with LinuxServer's socket-proxy configured as the documentation said).
const proxyDenial = "#2 ERROR: Error response from daemon: <html><body><h1>403 Forbidden</h1>\nRequest forbidden by administrative rules.\n</body></html>"

// The bare 403 page names nothing; the error now says what to allow.
func TestARefusedBuildNamesTheProxyPermissionItNeeds(t *testing.T) {
	runner := &progressRunner{model: buildableModel, upStderr: proxyDenial}
	err := progressService(t, runner).Up(context.Background(), "app/compose.yaml", io.Discard)

	require.Error(t, err)
	require.Contains(t, err.Error(), "403 Forbidden", "the original error stays visible")
	require.Contains(t, err.Error(), "GRPC=1")
}

func TestAControlledDeploymentExplainsARefusedBuildToo(t *testing.T) {
	runner := &progressRunner{model: buildableModel, upStderr: proxyDenial}
	err := progressService(t, runner).UpWait(context.Background(), "app/compose.yaml", io.Discard)

	require.Error(t, err)
	require.Contains(t, err.Error(), "GRPC=1")
}

// A 403 on a stack that builds nothing is not a build permission: no hint that
// would send the operator to the wrong setting.
func TestARefusalWithoutABuildGetsNoBuildHint(t *testing.T) {
	runner := &progressRunner{model: pulledModel, upStderr: "Error response from daemon: <html><body><h1>403 Forbidden</h1></body></html>"}
	err := progressService(t, runner).Up(context.Background(), "app/compose.yaml", io.Discard)

	require.Error(t, err)
	require.NotContains(t, err.Error(), "GRPC=1")
}

// Only a refusal gets the hint; any other build failure reads as before.
func TestOtherBuildFailuresAreLeftAlone(t *testing.T) {
	runner := &progressRunner{model: buildableModel, upStderr: "failed to solve: process \"/bin/sh -c make\" did not complete successfully: exit code: 2"}
	err := progressService(t, runner).Up(context.Background(), "app/compose.yaml", io.Discard)

	require.Error(t, err)
	require.NotContains(t, err.Error(), "GRPC=1")
	require.Contains(t, err.Error(), "exit code: 2")
}
