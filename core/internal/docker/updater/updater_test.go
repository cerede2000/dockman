package updater

import (
	"net/netip"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/require"
)

func TestValidateHealthcheckHostAllowsOnlyContainerAndLoopbackAddresses(t *testing.T) {
	inspect := &container.InspectResponse{NetworkSettings: &container.NetworkSettings{
		Networks: map[string]*network.EndpointSettings{"default": {IPAddress: netip.MustParseAddr("172.18.0.4")}},
	}}
	require.NoError(t, validateHealthcheckHost("localhost", inspect))
	require.NoError(t, validateHealthcheckHost("127.0.0.1", inspect))
	require.NoError(t, validateHealthcheckHost("172.18.0.4", inspect))
	require.Error(t, validateHealthcheckHost("169.254.169.254", inspect))
	require.Error(t, validateHealthcheckHost("internal.example", inspect))
}

func TestSummaryNameFallsBackToShortID(t *testing.T) {
	require.Equal(t, "0123456789ab", summaryName(container.Summary{ID: "0123456789abcdef"}))
	require.Equal(t, "named", summaryName(container.Summary{ID: "0123456789abcdef", Names: []string{"/named"}}))
}

func TestPreparedUpdateKeepsScannedTagAfterPullMovesIt(t *testing.T) {
	current := container.Summary{Image: "sha256:old-image-without-a-tag"}
	options := ForceUpdateOptions{ImagePrepared: true, ImageReference: "linuxserver/prowlarr:latest"}
	require.Equal(t, "linuxserver/prowlarr:latest", forceUpdateImageReference(current, options))
	require.Equal(t, "sha256:old-image-without-a-tag", forceUpdateImageReference(current, ForceUpdateOptions{}))
	require.Equal(t, "sha256:old-image-without-a-tag", forceUpdateImageReference(current, ForceUpdateOptions{ImageReference: "unprepared:latest"}))
}

func TestAbsentOptionalHealthcheckLabelsAreNoOps(t *testing.T) {
	service := &Service{}
	inspect := &container.InspectResponse{Config: &container.Config{Labels: map[string]string{}}}
	require.NoError(t, service.containerHealthCheckUptime(t.Context(), "unused", inspect))
	require.NoError(t, service.containerHealthCheckPing(t.Context(), inspect))
	require.NoError(t, service.containerHealthCheckUptime(t.Context(), "unused", nil))
	require.NoError(t, service.containerHealthCheckPing(t.Context(), nil))
}

// The manual update path drives the Docker API through whatever connection
// Dockman holds. For a socket proxy that connection is the container being
// replaced, so the update severs its own means of finishing or rolling back.
func TestManualUpdateRefusesAContainerCarryingTheDockerSocket(t *testing.T) {
	proxy := container.Summary{ID: "proxy", Names: []string{"/socket-proxy"}, Mounts: []container.MountPoint{
		{Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"},
	}}
	err := guardProtectedInfrastructure(&proxy)
	require.ErrorContains(t, err, "socket-proxy")
	require.ErrorContains(t, err, "protected update")

	// The explicit opt-in remains the way through.
	proxy.Labels = map[string]string{DockmanOptInUpdateLabel: "true"}
	require.NoError(t, guardProtectedInfrastructure(&proxy))

	// An ordinary container is untouched.
	require.NoError(t, guardProtectedInfrastructure(&container.Summary{ID: "app", Names: []string{"/app"}}))
}

// The manual update path had no self-update guard at all: the loop-based path
// checks the label, the automatic path re-checks it at execution time, but the
// button in the Monitor goes through ContainersForceUpdate, which checked
// neither. Recreating Dockman through the Docker API kills the process on its
// own ContainerStop - no replacement, no rollback, a stopped container and a
// host needing a manual docker start.
func TestManualUpdateRefusesDockmanItself(t *testing.T) {
	dockman := container.Summary{ID: "self", Names: []string{"/dockman"},
		Labels: map[string]string{DockmanContainerLabel: "true"}}
	err := guardProtectedInfrastructure(&dockman)
	require.ErrorContains(t, err, "dockman")
	require.ErrorContains(t, err, "Dockman update action")

	// It holds without the socket mounted, which is the case that the socket
	// check misses: a Dockman reaching its daemon through a proxy.
	require.NotContains(t, dockman.Mounts, container.MountPoint{Source: "/var/run/docker.sock"})

	// And the opt-in label must NOT open this one: there is no safe way to do it.
	dockman.Labels[DockmanOptInUpdateLabel] = "true"
	require.Error(t, guardProtectedInfrastructure(&dockman), "no label may authorise Dockman to destroy itself")
}
