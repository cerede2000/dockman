package updater

import (
	"context"
	"testing"

	containerSrv "github.com/RA341/dockman/internal/docker/container"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

// stabilityClient answers only what the stability check asks of it.
type stabilityClient struct {
	dockerClient
	state   *container.State
	inspect error
	calls   int
}

func (c *stabilityClient) ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	c.calls++
	if c.inspect != nil {
		return client.ContainerInspectResult{}, c.inspect
	}
	return client.ContainerInspectResult{
		Container: container.InspectResponse{ID: "abc123", Name: "/probe", State: c.state},
	}, nil
}

func stabilityService(t *testing.T, cli dockerClient) *Service {
	t.Helper()
	return &Service{client: cli}
}

// The hub drops events for a slow consumer rather than blocking the other
// listeners, and an automatic run subscribes one consumer per container while
// the same hub carries every other container's traffic. A dropped "die" made
// the stability window elapse in silence and the update was declared a
// success - a container that crashed on boot passing as healthy, with no
// rollback. Exactly the failure this check exists to catch.
func TestStabilityWindowAsksTheDaemonWhenNoEventArrives(t *testing.T) {
	cli := &stabilityClient{state: &container.State{Running: false, Status: "exited"}}
	service := stabilityService(t, cli)
	events := make(chan containerSrv.Event)

	err := service.waitForContainerStability(context.Background(), events, "abc123")
	require.Error(t, err, "silence is not proof that the container survived")
	require.Contains(t, err.Error(), "exited")
	require.Equal(t, 1, cli.calls, "the daemon must be asked before declaring success")
}

func TestStabilityWindowAcceptsARunningContainer(t *testing.T) {
	cli := &stabilityClient{state: &container.State{Running: true, Status: "running"}}
	service := stabilityService(t, cli)
	events := make(chan containerSrv.Event)

	require.NoError(t, service.waitForContainerStability(context.Background(), events, "abc123"))
	require.Equal(t, 1, cli.calls)
}

// A delivered die event still fails immediately, without waiting out the
// window or asking the daemon.
func TestStabilityWindowStillFailsFastOnADeliveredDie(t *testing.T) {
	cli := &stabilityClient{state: &container.State{Running: true, Status: "running"}}
	service := stabilityService(t, cli)
	events := make(chan containerSrv.Event, 1)
	events <- containerSrv.Event{ID: "abc123", Action: "die"}

	err := service.waitForContainerStability(context.Background(), events, "abc123")
	require.Error(t, err)
	require.Contains(t, err.Error(), "die")
	require.Zero(t, cli.calls, "a delivered die needs no confirmation")
}

// An unreachable daemon is not evidence of health either.
func TestStabilityWindowFailsWhenTheDaemonCannotAnswer(t *testing.T) {
	cli := &stabilityClient{inspect: context.DeadlineExceeded}
	service := stabilityService(t, cli)
	events := make(chan containerSrv.Event)

	err := service.waitForContainerStability(context.Background(), events, "abc123")
	require.Error(t, err)
}

// recoveryClient answers the two calls the sweep makes.
type recoveryClient struct {
	dockerClient
	items   []container.Summary
	renames map[string]string
	failAll error
}

func (c *recoveryClient) ContainerList(context.Context, client.ContainerListOptions) (client.ContainerListResult, error) {
	if c.failAll != nil {
		return client.ContainerListResult{}, c.failAll
	}
	return client.ContainerListResult{Items: c.items}, nil
}

func (c *recoveryClient) ContainerRename(_ context.Context, id string, opts client.ContainerRenameOptions) (client.ContainerRenameResult, error) {
	if c.renames == nil {
		c.renames = map[string]string{}
	}
	c.renames[id] = opts.NewName
	return client.ContainerRenameResult{}, nil
}

func summary(id string, names ...string) container.Summary {
	return container.Summary{ID: id, Names: names}
}

// The window between removing the old container and renaming the replacement
// is two instructions wide, but what it leaves is durable: the service runs,
// healthy, under a temporary name nothing will ever rename - and Compose then
// creates a SECOND container for that service on the next up.
func TestInterruptedReplacementTakesItsRealNameWhenItIsFree(t *testing.T) {
	cli := &recoveryClient{items: []container.Summary{
		summary("c1", "/adguard.dockman-update-a1b2c3d4"),
		summary("c2", "/unrelated"),
	}}
	service := &Service{client: cli}

	recovered, err := service.RecoverInterruptedReplacements(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, recovered)
	require.Equal(t, "adguard", cli.renames["c1"], "the replacement must take the name its update was going to give it")
}

// Guessing which of the two is the real service would be worse than saying so.
func TestInterruptedReplacementIsLeftAloneWhenTheNameIsTaken(t *testing.T) {
	cli := &recoveryClient{items: []container.Summary{
		summary("c1", "/adguard.dockman-update-a1b2c3d4"),
		summary("c2", "/adguard"),
	}}
	service := &Service{client: cli}

	recovered, err := service.RecoverInterruptedReplacements(context.Background())
	require.NoError(t, err)
	require.Zero(t, recovered)
	require.Empty(t, cli.renames, "an occupied target name must never be guessed at")
}

// The pattern is Dockman's own and must not touch anything else.
func TestRecoveryIgnoresContainersThatAreNotDockmanReplacements(t *testing.T) {
	cli := &recoveryClient{items: []container.Summary{
		summary("c1", "/adguard.dockman-update-notahex"),
		summary("c2", "/my.dockman-update-backup"),
		summary("c3", "/plain"),
		summary("c4", "/adguard_updated"),
	}}
	service := &Service{client: cli}

	recovered, err := service.RecoverInterruptedReplacements(context.Background())
	require.NoError(t, err)
	require.Zero(t, recovered)
	require.Empty(t, cli.renames)
}

func TestRecoveryReportsAListingFailure(t *testing.T) {
	cli := &recoveryClient{failAll: context.DeadlineExceeded}
	service := &Service{client: cli}
	_, err := service.RecoverInterruptedReplacements(context.Background())
	require.Error(t, err)
}
