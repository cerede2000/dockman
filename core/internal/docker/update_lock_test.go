package docker

import (
	"testing"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
)

// Two updates on one stack already exclude each other, because this key is the
// same one withComposeActionLock takes. The gap was the fallback: a container
// whose config-files label cannot be resolved dropped straight to its own id,
// so a stack-level Compose action and an update of one of its containers took
// two different locks and ran side by side.
func TestContainerUpdateLockKeyFallsBackToTheStackBeforeTheContainer(t *testing.T) {
	service := &Service{Compose: nil}

	// No Dockman path, but Compose still names the stack.
	orphan := container.Summary{ID: "abc123", Labels: map[string]string{api.ProjectLabel: "media"}}
	if got := containerUpdateLockKey(service, orphan); got != "project:media" {
		t.Fatalf("a container of a known project must serialize on the project, got %q", got)
	}

	// Nothing at all: the container is the only identity left, and two updates
	// of that same container still exclude each other.
	loner := container.Summary{ID: "abc123"}
	if got := containerUpdateLockKey(service, loner); got != "container:abc123" {
		t.Fatalf("a container with no stack identity keeps its own lock, got %q", got)
	}
}
