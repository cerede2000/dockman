package docker

import (
	"testing"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/require"
)

/*


- "traefik.enable=true"
- "traefik.http.routers.my-app.rule=Host(`myapp.example.com`)"

*/

func Test_extractTraefikLabel(t *testing.T) {
	labels := map[string]string{
		"traefik.enable":                       "true",
		"traefik.http.routers.my-service.rule": "Host(`myapp.localhost`, `api.localhost`) && PathPrefix(`/api`) ",
		"traefik.http.routers.my-app.rule":     "Host(`myapp.example.com`, `MYAPP.EXAMPLE.COM`)",
		"traefik.tcp.routers.secure.rule":      "HostSNI(`tcp.example.com`)",
	}

	hostsActual := extractTraefikLabel(labels)
	expectedHosts := []string{"api.localhost", "myapp.example.com", "myapp.localhost", "tcp.example.com"}
	require.Equal(t, expectedHosts, hostsActual)

	delete(labels, "traefik.enable")
	require.Equal(t, expectedHosts, extractTraefikLabel(labels), "router labels use Traefik's expose-by-default behavior")
	labels["traefik.enable"] = "true"

	labels["traefik.http.routers.my-service.rule"] = ""
	labels["traefik.http.routers.my-app.rule"] = ""
	labels["traefik.tcp.routers.secure.rule"] = ""
	hostsActual = extractTraefikLabel(labels)
	require.Nil(t, hostsActual)

	labels["traefik.enable"] = "false"
	hostsActual = extractTraefikLabel(labels)
	expectedHosts = []string{}
	require.Nil(t, hostsActual, "Host actual should be nil if traefic is disabled")
}

func TestBuildComposeStatusIndexIncludesNestedPrimaryPath(t *testing.T) {
	const primary = "/server/stacks/substacks/whoami/compose.yml"
	const override = "/server/stacks/substacks/whoami/compose.override.yml"
	containers := []container.Summary{
		{
			Labels: map[string]string{api.ConfigFilesLabel: primary + "," + override},
			State:  container.StateRunning,
			Health: &container.HealthSummary{Status: container.Healthy},
		},
		{
			Labels: map[string]string{api.ConfigFilesLabel: primary + "," + override},
			State:  container.StateExited,
			Status: "Exited (137) 2 minutes ago",
		},
		{State: container.StateRunning}, // not managed by Compose
	}

	byFile, primaryFiles := buildComposeStatusIndex(containers)

	require.Equal(t, map[string]struct{}{primary: {}}, primaryFiles)
	require.Equal(t, &stackStatus{up: 1, down: 1, healthy: 1}, byFile[primary])
	require.Equal(t, &stackStatus{up: 1, down: 1, healthy: 1}, byFile[override])
}

func TestStackStatusAcceptsRunningContainerWithoutHealthcheck(t *testing.T) {
	status := &stackStatus{}
	require.NotPanics(t, func() {
		status.add(container.Summary{State: container.StateRunning})
	})
	require.Equal(t, int32(1), status.up)
}

func TestComposeStatusSnapshotDoesNotRetainPreviousStoppedState(t *testing.T) {
	const composeFile = "/server/stacks/substacks/app/compose.yml"
	stopped, _ := buildComposeStatusIndex([]container.Summary{{
		Labels: map[string]string{api.ConfigFilesLabel: composeFile},
		State:  container.StateExited,
		Status: "Exited (1) 2 seconds ago",
	}})
	require.Equal(t, int32(1), stopped[composeFile].down)
	require.Zero(t, stopped[composeFile].unhealthy, "an exit code cannot reliably distinguish a manual stop from a crash")

	running, _ := buildComposeStatusIndex([]container.Summary{{
		Labels: map[string]string{api.ConfigFilesLabel: composeFile},
		State:  container.StateRunning,
	}})
	require.Equal(t, &stackStatus{up: 1}, running[composeFile],
		"each container-list response must replace, never merge with, the previous status snapshot")
}

func TestExitedContainersRemainStoppedRegardlessOfSignalExitCode(t *testing.T) {
	for _, statusLine := range []string{"Exited (0) 2 seconds ago", "Exited (1) 2 seconds ago", "Exited (137) 2 seconds ago", "Exited (143) 2 seconds ago"} {
		t.Run(statusLine, func(t *testing.T) {
			status := &stackStatus{}
			status.add(container.Summary{State: container.StateExited, Status: statusLine})
			require.Equal(t, &stackStatus{down: 1}, status)
		})
	}
}

func TestComposeStatusKeepsObservableFailuresSeparateFromStopped(t *testing.T) {
	status := &stackStatus{}
	status.add(container.Summary{State: container.StateDead})
	require.Equal(t, &stackStatus{down: 1, unhealthy: 1}, status)

	status = &stackStatus{}
	status.add(container.Summary{State: container.StateRunning, Health: &container.HealthSummary{Status: container.Unhealthy}})
	require.Equal(t, &stackStatus{up: 1, unhealthy: 1}, status)
}
