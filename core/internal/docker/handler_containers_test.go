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

func TestBuildComposeStatusIndexResolvesRelativeConfigFileFromWorkingDirectory(t *testing.T) {
	const composeFile = "/server/stacks/applemusicrip/compose.yml"
	byFile, primaryFiles := buildComposeStatusIndex([]container.Summary{{
		Labels: map[string]string{
			api.ConfigFilesLabel: "./compose.yml",
			api.WorkingDirLabel:  "/server/stacks/applemusicrip/",
		},
		State:  container.StateRunning,
		Health: &container.HealthSummary{Status: container.Healthy},
	}})

	require.Equal(t, map[string]struct{}{composeFile: {}}, primaryFiles)
	require.Equal(t, &stackStatus{up: 1, healthy: 1}, byFile[composeFile])
}

func TestNormalizeComposeConfigPathCanonicalizesEquivalentLabels(t *testing.T) {
	const expected = "/server/stacks/applemusicrip/compose.yml"
	require.Equal(t, expected, normalizeComposeConfigPath("compose.yml", "/server/stacks/applemusicrip"))
	require.Equal(t, expected, normalizeComposeConfigPath("./compose.yml", "/server/stacks/applemusicrip/"))
	require.Equal(t, expected, normalizeComposeConfigPath("/server/stacks/applemusicrip/./compose.yml", ""))
}

func TestBuildComposeStatusIndexIgnoresOneoffContainers(t *testing.T) {
	const composeFile = "/server/stacks/app/compose.yml"
	byFile, primaryFiles := buildComposeStatusIndex([]container.Summary{
		{
			ID: "service",
			Labels: map[string]string{
				api.ConfigFilesLabel: composeFile,
				api.OneoffLabel:      "False",
			},
			State: container.StateExited,
		},
		{
			ID: "oneoff",
			Labels: map[string]string{
				api.ConfigFilesLabel: composeFile,
				api.OneoffLabel:      "True",
			},
			State: container.StateRunning,
		},
	})

	require.Equal(t, map[string]struct{}{composeFile: {}}, primaryFiles)
	require.Equal(t, &stackStatus{down: 1}, byFile[composeFile])
}

func TestComposeStoppedOverrideIsScopedAndCleared(t *testing.T) {
	h := &Handler{composeStopped: make(map[composeStackKey]int64)}
	h.setComposeStopped("local", "compose/app/compose.yml", true)

	_, stopped := h.composeStoppedAt("local", "compose/app/compose.yml")
	require.True(t, stopped)
	_, stopped = h.composeStoppedAt("remote", "compose/app/compose.yml")
	require.False(t, stopped)
	_, stopped = h.composeStoppedAt("local", "compose/other/compose.yml")
	require.False(t, stopped)

	h.setComposeStopped("local", "compose/app/compose.yml", false)
	_, stopped = h.composeStoppedAt("local", "compose/app/compose.yml")
	require.False(t, stopped)
}

func TestComposeStoppedOverrideDetectsNewProjectContainers(t *testing.T) {
	const composeFile = "/server/stacks/app/compose.yml"
	containers := []container.Summary{
		{Created: 99, Labels: map[string]string{api.ConfigFilesLabel: composeFile}},
		{Created: 101, Labels: map[string]string{api.ConfigFilesLabel: "/server/stacks/other/compose.yml"}},
		{Created: 101, Labels: map[string]string{api.ConfigFilesLabel: composeFile, api.OneoffLabel: "True"}},
	}
	require.False(t, hasComposeContainerCreatedSince(containers, composeFile, 100))

	containers = append(containers, container.Summary{
		Created: 100,
		Labels:  map[string]string{api.ConfigFilesLabel: composeFile},
	})
	require.False(t, hasComposeContainerCreatedSince(containers, composeFile, 100), "same-second containers predate the completed down action")
	containers = append(containers, container.Summary{
		Created: 101,
		Labels:  map[string]string{api.ConfigFilesLabel: composeFile},
	})
	require.True(t, hasComposeContainerCreatedSince(containers, composeFile, 100))
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
