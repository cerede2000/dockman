package compose

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/stretchr/testify/require"
)

// limitsRunner records every command with the environment it ran under and
// answers the Compose model probe. failOn makes the first command containing
// that word fail, to exercise the error paths.
type limitsRunner struct {
	mu     sync.Mutex
	model  string
	failOn string
	calls  []recordedCall
}

type recordedCall struct {
	cmd []string
	env []string
}

func (r *limitsRunner) Run(_ context.Context, cmd []string, _ string, env []string, out io.Writer, errOut io.Writer) error {
	r.mu.Lock()
	r.calls = append(r.calls, recordedCall{cmd: slices.Clone(cmd), env: slices.Clone(env)})
	failOn := r.failOn
	r.mu.Unlock()
	if slices.Contains(cmd, "config") {
		_, _ = io.WriteString(out, r.model)
		return nil
	}
	if failOn != "" && slices.Contains(cmd, failOn) {
		_, _ = io.WriteString(errOut, "boom: "+failOn)
		return errors.New("exit status 1")
	}
	return nil
}

func (r *limitsRunner) joined() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	lines := make([]string, 0, len(r.calls))
	for _, call := range r.calls {
		lines = append(lines, strings.Join(call.cmd, " "))
	}
	return lines
}

// find returns the first recorded call whose command line contains all parts.
func (r *limitsRunner) find(parts ...string) (recordedCall, int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, call := range r.calls {
		line := strings.Join(call.cmd, " ")
		matched := true
		for _, part := range parts {
			if !strings.Contains(line, part) {
				matched = false
				break
			}
		}
		if matched {
			return call, i, true
		}
	}
	return recordedCall{}, -1, false
}

func (r *limitsRunner) lastIndex(parts ...string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	last := -1
	for i, call := range r.calls {
		line := strings.Join(call.cmd, " ")
		matched := true
		for _, part := range parts {
			if !strings.Contains(line, part) {
				matched = false
				break
			}
		}
		if matched {
			last = i
		}
	}
	return last
}

func limitsService(t *testing.T, runner *limitsRunner, limits BuildLimits) *Service {
	t.Helper()
	root := t.TempDir()
	return &Service{
		TTY:         true,
		runner:      runner,
		hostname:    "limits-host-" + strings.ReplaceAll(t.Name(), "/", "-"),
		buildLimits: limits,
		parser: func(string, string) (Host, error) {
			return Host{Fs: filesystem.NewLocal(root), Relpath: "app/compose.yaml"}, nil
		},
	}
}

var oneAndAHalfCPUsTwoGiB = BuildLimits{CPUs: 1.5, MemoryBytes: 2 << 30}

// Reported upstream as RA341/dockman#195: a build pinned every core of the
// host. --cpu-quota on the build does nothing with BuildKit, so the limit is
// put on the builder: its container runs every RUN step.
func TestALimitedHostBuildsThroughABuilderCreatedWithItsLimits(t *testing.T) {
	runner := &limitsRunner{model: buildableModel}
	require.NoError(t, limitsService(t, runner, oneAndAHalfCPUsTwoGiB).Up(context.Background(), "app/compose.yaml", io.Discard))

	create, createAt, ok := runner.find("buildx", "create", "--name", limitedBuilderName)
	require.True(t, ok, "no builder was created: %v", runner.joined())
	require.Equal(t, []string{"env", "BUILDX_CONFIG=" + limitedBuilderConfig, "BUILDX_BUILDER="}, create.cmd[:3],
		"the builder lives in Dockman's own Buildx state, not the user's")
	line := strings.Join(create.cmd, " ")
	require.Contains(t, line, "--driver docker-container")
	require.Contains(t, line, "--driver-opt cpu-period=100000")
	require.Contains(t, line, "--driver-opt cpu-quota=150000")
	require.Contains(t, line, "--driver-opt memory=2147483648")

	up, upAt, ok := runner.find("up", "--build")
	require.True(t, ok)
	require.Contains(t, up.env, "BUILDX_BUILDER="+limitedBuilderName, "Compose's bake must pick the limited builder")
	require.Contains(t, up.env, "BUILDX_CONFIG="+limitedBuilderConfig)
	require.Less(t, createAt, upAt, "the builder exists before Compose builds")
}

// No BuildKit container idles between builds, and the cache stays.
func TestTheLimitedBuilderIsRemovedAfterTheBuildKeepingItsCache(t *testing.T) {
	runner := &limitsRunner{model: buildableModel}
	require.NoError(t, limitsService(t, runner, oneAndAHalfCPUsTwoGiB).Up(context.Background(), "app/compose.yaml", io.Discard))

	_, upAt, _ := runner.find("up", "--build")
	removeAt := runner.lastIndex("buildx", "rm", "--keep-state", limitedBuilderName)
	require.Greater(t, removeAt, upAt, "the builder is removed after the build: %v", runner.joined())
	require.Greater(t, runner.lastIndex("docker", "rm", "--force", limitedBuilderContainer), upAt)
}

// A failed build is exactly when a leftover builder would be forgotten.
func TestTheLimitedBuilderIsRemovedEvenWhenTheBuildFails(t *testing.T) {
	runner := &limitsRunner{model: buildableModel, failOn: "up"}
	err := limitsService(t, runner, oneAndAHalfCPUsTwoGiB).Up(context.Background(), "app/compose.yaml", io.Discard)
	require.Error(t, err)

	_, upAt, _ := runner.find("up", "--build")
	require.Greater(t, runner.lastIndex("buildx", "rm", "--keep-state", limitedBuilderName), upAt)
}

// A run that died mid-build left its builder with the old limits: it goes
// before a new one is created, so the current settings always apply.
func TestALeftoverBuilderIsClearedBeforeTheNewOneIsCreated(t *testing.T) {
	runner := &limitsRunner{model: buildableModel}
	require.NoError(t, limitsService(t, runner, oneAndAHalfCPUsTwoGiB).Up(context.Background(), "app/compose.yaml", io.Discard))

	_, createAt, _ := runner.find("buildx", "create")
	_, clearAt, _ := runner.find("buildx", "rm", "--keep-state")
	_, clearContainerAt, _ := runner.find("docker", "rm", "--force", limitedBuilderContainer)
	require.Less(t, clearAt, createAt)
	require.Less(t, clearContainerAt, createAt)
}

// Without limits nothing changes: no builder, no environment, the build goes
// where it always went.
func TestAnUnlimitedHostBuildsExactlyAsBefore(t *testing.T) {
	runner := &limitsRunner{model: buildableModel}
	require.NoError(t, limitsService(t, runner, BuildLimits{}).Up(context.Background(), "app/compose.yaml", io.Discard))

	_, _, created := runner.find("buildx", "create")
	require.False(t, created)
	up, _, _ := runner.find("up", "--build")
	require.Empty(t, up.env)
}

// A stack of pulled images has nothing to build: no builder is started for it.
func TestALimitedHostStartsNoBuilderForAStackThatBuildsNothing(t *testing.T) {
	runner := &limitsRunner{model: pulledModel}
	require.NoError(t, limitsService(t, runner, oneAndAHalfCPUsTwoGiB).Up(context.Background(), "app/compose.yaml", io.Discard))

	_, _, created := runner.find("buildx", "create")
	require.False(t, created)
}

// Git deployments with automatic rollback build through UpWait: capped too.
func TestAControlledDeploymentBuildsThroughTheLimitedBuilder(t *testing.T) {
	runner := &limitsRunner{model: buildableModel}
	require.NoError(t, limitsService(t, runner, oneAndAHalfCPUsTwoGiB).UpWait(context.Background(), "app/compose.yaml", io.Discard))

	up, _, ok := runner.find("up", "--wait")
	require.True(t, ok)
	require.Contains(t, up.env, "BUILDX_BUILDER="+limitedBuilderName)
}

// UpWait already runs plain: without limits it has no reason to read the model.
func TestAnUnlimitedControlledDeploymentDoesNotReadTheModel(t *testing.T) {
	runner := &limitsRunner{model: buildableModel}
	require.NoError(t, limitsService(t, runner, BuildLimits{}).UpWait(context.Background(), "app/compose.yaml", io.Discard))

	_, _, read := runner.find("config", "--format", "json")
	require.False(t, read)
}

// Redeploy builds a missing image even without its build switch.
func TestRedeployBuildsThroughTheLimitedBuilder(t *testing.T) {
	runner := &limitsRunner{model: buildableModel}
	require.NoError(t, limitsService(t, runner, oneAndAHalfCPUsTwoGiB).Redeploy(context.Background(), "app/compose.yaml", io.Discard, false, false, false))

	up, _, ok := runner.find("up", "--remove-orphans")
	require.True(t, ok)
	require.Contains(t, up.env, "BUILDX_BUILDER="+limitedBuilderName)
}

func TestBuildLimitsDriverOptions(t *testing.T) {
	require.Equal(t, []string{"--driver-opt", "cpu-period=100000", "--driver-opt", "cpu-quota=50000"},
		BuildLimits{CPUs: 0.5}.driverOpts())
	require.Equal(t, []string{"--driver-opt", "memory=536870912"},
		BuildLimits{MemoryBytes: 512 << 20}.driverOpts())
	require.Empty(t, BuildLimits{}.driverOpts())
}

func TestBuildLimitsValidation(t *testing.T) {
	require.NoError(t, BuildLimits{}.Validate(), "no limit is valid")
	require.NoError(t, BuildLimits{CPUs: 0.1, MemoryBytes: 256 << 20}.Validate())
	require.NoError(t, oneAndAHalfCPUsTwoGiB.Validate())
	require.Error(t, BuildLimits{CPUs: -1}.Validate())
	require.Error(t, BuildLimits{CPUs: 0.05}.Validate(), "BuildKit cannot run on a sliver of a core")
	require.Error(t, BuildLimits{MemoryBytes: 64 << 20}.Validate(), "nor in 64 MiB")
	require.Error(t, BuildLimits{MemoryBytes: -1}.Validate())
}

func TestBuildLimitsDescription(t *testing.T) {
	require.Equal(t, "1.5 CPU, 2 GiB", oneAndAHalfCPUsTwoGiB.String())
	require.Equal(t, "768 MiB", BuildLimits{MemoryBytes: 768 << 20}.String())
	require.Equal(t, "none", BuildLimits{}.String())
}

// One builder name per daemon: builds on a limited host take turns.
func TestLimitedBuildsOnOneHostTakeTurns(t *testing.T) {
	gate := make(chan struct{})
	entered := make(chan string, 2)
	svc := &Service{runner: &limitsRunner{}, hostname: "turns-host", buildLimits: oneAndAHalfCPUsTwoGiB}

	var wg sync.WaitGroup
	for _, name := range []string{"first", "second"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = svc.withLimitedBuilder(context.Background(), ".", nil, false, func(string) error {
				entered <- name
				<-gate
				return nil
			})
		}()
	}

	first := <-entered
	select {
	case second := <-entered:
		t.Fatalf("%s entered while %s was still building", second, first)
	case <-time.After(150 * time.Millisecond):
	}
	gate <- struct{}{}
	<-entered
	gate <- struct{}{}
	wg.Wait()
}

// A request that goes away while waiting for its turn stops waiting.
func TestWaitingForATurnEndsWithTheRequest(t *testing.T) {
	svc := &Service{runner: &limitsRunner{}, hostname: "cancel-host", buildLimits: oneAndAHalfCPUsTwoGiB}
	release, err := acquireLimitedBuild(context.Background(), svc.hostname)
	require.NoError(t, err)
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = svc.withLimitedBuilder(ctx, ".", nil, false, func(string) error { return nil })
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func filesBuildService(t *testing.T, runner *limitsRunner, limits BuildLimits) *Service {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "app", "Dockerfile"), []byte("FROM alpine\n"), 0o644))
	return &Service{
		runner:      runner,
		hostname:    "files-host-" + t.Name(),
		buildLimits: limits,
		parser: func(string, string) (Host, error) {
			return Host{Fs: filesystem.NewLocal(root), Relpath: "app/Dockerfile"}, nil
		},
	}
}

// Dockerfile builds from Files are capped the same way.
func TestAFilesBuildOnALimitedHostUsesTheLimitedBuilder(t *testing.T) {
	runner := &limitsRunner{}
	require.NoError(t, filesBuildService(t, runner, oneAndAHalfCPUsTwoGiB).RunDockerfileBuild(context.Background(), "app/Dockerfile", "demo:limited", "default", io.Discard))

	build, buildAt, ok := runner.find("buildx", "build", "--tag", "demo:limited")
	require.True(t, ok, "%v", runner.joined())
	require.Equal(t, []string{"env", "BUILDX_CONFIG=" + limitedBuilderConfig, "BUILDX_BUILDER="}, build.cmd[:3])
	require.Contains(t, strings.Join(build.cmd, " "), "--builder "+limitedBuilderName)
	require.Contains(t, build.cmd, "--load", "the image must land in the daemon")
	require.Greater(t, runner.lastIndex("buildx", "rm", "--keep-state", limitedBuilderName), buildAt)
	_, _, probed := runner.find("buildx", "ls")
	require.False(t, probed, "the daemon builder is not consulted on a limited host")
}

// Host networking is a BuildKit entitlement: the limited builder is created
// with it and the build asks for it, as the job-scoped builder did.
func TestALimitedFilesBuildKeepsHostNetworking(t *testing.T) {
	runner := &limitsRunner{}
	require.NoError(t, filesBuildService(t, runner, oneAndAHalfCPUsTwoGiB).RunDockerfileBuild(context.Background(), "app/Dockerfile", "demo:host", "host", io.Discard))

	create, _, _ := runner.find("buildx", "create", "--name", limitedBuilderName)
	require.Contains(t, create.cmd, "--allow-insecure-entitlement network.host")
	build, _, _ := runner.find("buildx", "build", "--tag", "demo:host")
	require.Contains(t, build.cmd, "--allow=network.host")
	require.Contains(t, build.cmd, "--network=host")
}

// Without limits, a Files build takes the path it always took.
func TestAnUnlimitedFilesBuildIsUnchanged(t *testing.T) {
	runner := &limitsRunner{}
	require.NoError(t, filesBuildService(t, runner, BuildLimits{}).RunDockerfileBuild(context.Background(), "app/Dockerfile", "demo:plain", "default", io.Discard))

	_, _, created := runner.find("buildx", "create", "--name", limitedBuilderName)
	require.False(t, created)
	build, _, _ := runner.find("buildx", "build", "--tag", "demo:plain")
	require.Equal(t, "BUILDX_CONFIG="+dockmanNativeBuildxConfig, build.cmd[1])
}

// Measured behind LinuxServer's socket-proxy: the limited builder does not use
// /grpc, but Buildx copies its configuration into the container it creates,
// a PUT on containers/{id}/archive refused without ALLOW_ARCHIVE=1.
func TestARefusedLimitedBuildNamesArchiveWritesNotGrpc(t *testing.T) {
	runner := &limitsRunner{model: buildableModel, failOn: "up"}
	svc := limitsService(t, runner, oneAndAHalfCPUsTwoGiB)
	svc.runner = &denyingRunner{limitsRunner: runner}
	err := svc.Up(context.Background(), "app/compose.yaml", io.Discard)

	require.Error(t, err)
	require.Contains(t, err.Error(), "ALLOW_ARCHIVE=1")
	require.NotContains(t, err.Error(), "GRPC=1")
}

// denyingRunner answers the up call with the proxy's refusal page.
type denyingRunner struct{ *limitsRunner }

func (r *denyingRunner) Run(ctx context.Context, cmd []string, wd string, env []string, out io.Writer, errOut io.Writer) error {
	if slices.Contains(cmd, "up") {
		r.limitsRunner.mu.Lock()
		r.limitsRunner.calls = append(r.limitsRunner.calls, recordedCall{cmd: slices.Clone(cmd), env: slices.Clone(env)})
		r.limitsRunner.mu.Unlock()
		_, _ = io.WriteString(errOut, proxyDenial)
		return errors.New("exit status 1")
	}
	return r.limitsRunner.Run(ctx, cmd, wd, env, out, errOut)
}
