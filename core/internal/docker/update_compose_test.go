package docker

import (
	"testing"

	"github.com/RA341/dockman/internal/docker/compose"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/stretchr/testify/require"
)

const (
	currentHash = "1111111111111111111111111111111111111111111111111111111111111111"
	staleHash   = "2222222222222222222222222222222222222222222222222222222222222222"
)

func runningContainer(service, hash, image string) container.Summary {
	return container.Summary{
		ID:    "id-" + service,
		Names: []string{"/" + service},
		Image: image,
		State: container.StateRunning,
		Labels: map[string]string{
			api.ServiceLabel:    service,
			api.ConfigHashLabel: hash,
		},
	}
}

func plainService() compose.ServicePlan {
	return compose.ServicePlan{ConfigHash: currentHash, Replicas: 1}
}

func serviceNames(targets []composeUpdateTarget) []string {
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		names = append(names, target.service)
	}
	return names
}

// A service that matches its manifest and runs one healthy container is the
// only case Dockman may replace on its own; everything else belongs to
// Compose. This is the whole point of the Deploy tab's Update button: it must
// not stop containers that have nothing to update.
func TestClassifyComposeUpdateSendsOnlyUnchangedServicesToTheImagePath(t *testing.T) {
	expected := map[string]compose.ServicePlan{
		"web": plainService(),
		"db":  plainService(),
	}
	running := []container.Summary{
		runningContainer("web", currentHash, "nginx:latest"),
		runningContainer("db", currentHash, "postgres:17"),
	}

	plan := classifyComposeUpdate(expected, running, nil)

	require.Empty(t, plan.composeServices)
	require.Empty(t, plan.orphans)
	require.Equal(t, []string{"db", "web"}, serviceNames(plan.imageTargets))
	require.False(t, plan.nothingToDo(), "there are still images to check")
}

func TestClassifyComposeUpdateSendsStructuralChangesToCompose(t *testing.T) {
	buildable := plainService()
	buildable.Buildable = true
	scaled := plainService()
	scaled.Replicas = 3
	unreadableScale := plainService()
	unreadableScale.Replicas = 0

	stopped := runningContainer("stopped", currentHash, "nginx:latest")
	stopped.State = container.StateExited
	socketBound := runningContainer("socket-proxy", currentHash, "proxy:1")
	socketBound.Mounts = []container.MountPoint{
		{Type: mount.TypeBind, Source: "/var/run/docker.sock", Destination: "/var/run/docker.sock"},
	}

	cases := map[string]struct {
		expected map[string]compose.ServicePlan
		running  []container.Summary
		reason   string
	}{
		"changed manifest": {
			expected: map[string]compose.ServicePlan{"web": plainService()},
			running:  []container.Summary{runningContainer("web", staleHash, "nginx:latest")},
			reason:   "a different config hash means the compose file moved",
		},
		"missing container": {
			expected: map[string]compose.ServicePlan{"web": plainService()},
			running:  nil,
			reason:   "a service with no container has to be created",
		},
		"stopped container": {
			expected: map[string]compose.ServicePlan{"stopped": plainService()},
			running:  []container.Summary{stopped},
			reason:   "up -d is what brings a stopped service back",
		},
		"locally built service": {
			expected: map[string]compose.ServicePlan{"web": buildable},
			running:  []container.Summary{runningContainer("web", currentHash, "proj-web:latest")},
			reason:   "the build section is excluded from the hash, only Compose can tell",
		},
		"replica set": {
			expected: map[string]compose.ServicePlan{"web": scaled},
			running:  []container.Summary{runningContainer("web", currentHash, "nginx:latest")},
			reason:   "scale is excluded from the hash, the missing replicas need Compose",
		},
		"unreadable replica count": {
			expected: map[string]compose.ServicePlan{"web": unreadableScale},
			running:  []container.Summary{runningContainer("web", currentHash, "nginx:latest")},
			reason:   "an unknown count must not be taken for one",
		},
		"image reference reduced to a digest": {
			expected: map[string]compose.ServicePlan{"web": plainService()},
			running:  []container.Summary{runningContainer("web", currentHash, "sha256:abc")},
			reason:   "the updater cannot pull a bare digest; Compose resolves it from the manifest",
		},
		"container carrying the docker socket": {
			expected: map[string]compose.ServicePlan{"socket-proxy": plainService()},
			running:  []container.Summary{socketBound},
			reason:   "Dockman must not replace the socket its own connection runs on",
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			plan := classifyComposeUpdate(testCase.expected, testCase.running, nil)
			require.Len(t, plan.composeServices, 1, testCase.reason)
			require.Empty(t, plan.imageTargets, testCase.reason)
		})
	}
}

func TestClassifyComposeUpdateReportsOrphansOnce(t *testing.T) {
	// A service dropped from the compose file leaves its container behind.
	// Compose removes it, but only when it is asked to look at the project,
	// so the plan has to record that there is something to remove.
	expected := map[string]compose.ServicePlan{"web": plainService()}
	running := []container.Summary{
		runningContainer("web", currentHash, "nginx:latest"),
		runningContainer("removed", currentHash, "old:1"),
		runningContainer("removed", currentHash, "old:1"),
	}

	plan := classifyComposeUpdate(expected, running, nil)

	require.Equal(t, []string{"removed"}, plan.orphans)
	require.Equal(t, []string{"web"}, serviceNames(plan.imageTargets))
	require.False(t, plan.nothingToDo())
}

func TestClassifyComposeUpdateHonoursTheSelectedServices(t *testing.T) {
	expected := map[string]compose.ServicePlan{
		"web": plainService(),
		"db":  plainService(),
	}
	running := []container.Summary{
		runningContainer("web", staleHash, "nginx:latest"),
		runningContainer("db", staleHash, "postgres:17"),
	}

	plan := classifyComposeUpdate(expected, running, []string{"web"})

	require.Equal(t, []string{"web"}, plan.composeServices)
	require.Equal(t, []string{"db"}, plan.outOfScope, "db changed too but was not selected")
}

func TestClassifyComposeUpdateFindsNothingToDoOnACurrentStack(t *testing.T) {
	// Not strictly "nothing": the images still have to be checked. What must
	// never happen is a container being stopped for a service that matches.
	plan := classifyComposeUpdate(nil, nil, nil)
	require.True(t, plan.nothingToDo())
}
