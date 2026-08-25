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
