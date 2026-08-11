package updater

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	containerSrv "github.com/RA341/dockman/internal/docker/container"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

// fakeDockerClient is an in-memory stand-in for the Docker daemon, holding
// just enough state for the recreate transaction: which containers exist,
// which are running, and what their health reports. Every call is recorded so
// a test can assert on what the transaction actually did to the host, not
// merely on the error it returned.
type fakeDockerClient struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer
	calls      []string
	nextID     int

	// health the daemon reports for containers this fake creates, mirroring the
	// HEALTHCHECK the new image would carry.
	createdHealth *container.Health

	// failures injected per operation, keyed by container id.
	failStart  map[string]error
	failRemove map[string]error
	failRename map[string]error
	failStop   map[string]error
	failCreate error

	// stopReallyStops mirrors a daemon that carried the stop out even though
	// the call reported an error, which is what a deadline reached mid-stop
	// looks like from here.
	stopReallyStops bool
}

type fakeContainer struct {
	name    string
	running bool
	status  container.ContainerState
	health  *container.Health
	config  *container.Config
}

func newFakeDockerClient() *fakeDockerClient {
	return &fakeDockerClient{
		containers: map[string]*fakeContainer{},
		failStart:  map[string]error{},
		failRemove: map[string]error{},
		failRename: map[string]error{},
		failStop:   map[string]error{},
	}
}

func (f *fakeDockerClient) add(id, name string, running bool, health *container.Health) {
	f.mu.Lock()
	defer f.mu.Unlock()
	status := container.ContainerState("exited")
	if running {
		status = container.ContainerState("running")
	}
	f.containers[id] = &fakeContainer{name: name, running: running, status: status, health: health, config: &container.Config{}}
}

func (f *fakeDockerClient) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeDockerClient) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeDockerClient) exists(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.containers[id]
	return ok
}

func (f *fakeDockerClient) isRunning(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.containers[id]
	return ok && item.running
}

func (f *fakeDockerClient) nameOf(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if item, ok := f.containers[id]; ok {
		return item.name
	}
	return ""
}

func (f *fakeDockerClient) ContainerList(context.Context, client.ContainerListOptions) (client.ContainerListResult, error) {
	return client.ContainerListResult{}, nil
}

func (f *fakeDockerClient) ContainerInspect(_ context.Context, containerID string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	f.record("inspect:" + containerID)
	f.mu.Lock()
	defer f.mu.Unlock()
	item, ok := f.containers[containerID]
	if !ok {
		return client.ContainerInspectResult{}, errors.New("no such container")
	}
	return client.ContainerInspectResult{Container: container.InspectResponse{
		ID:     containerID,
		Name:   "/" + item.name,
		State:  &container.State{Running: item.running, Status: item.status, Health: item.health},
		Config: item.config,
	}}, nil
}

func (f *fakeDockerClient) ContainerCreate(_ context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	f.record("create:" + options.Name)
	if f.failCreate != nil {
		return client.ContainerCreateResult{}, f.failCreate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// The daemon refuses a name already in use, and a leftover from a previous
	// attempt is exactly how that happens in practice.
	for _, existing := range f.containers {
		if existing.name == options.Name {
			return client.ContainerCreateResult{}, fmt.Errorf(
				"Conflict. The container name %q is already in use", "/"+options.Name)
		}
	}
	f.nextID++
	id := "new000000000" + string(rune('a'+f.nextID))
	f.containers[id] = &fakeContainer{
		name: options.Name, status: container.ContainerState("created"),
		health: f.createdHealth, config: options.Config,
	}
	return client.ContainerCreateResult{ID: id}, nil
}

func (f *fakeDockerClient) ContainerStart(_ context.Context, containerID string, _ client.ContainerStartOptions) (client.ContainerStartResult, error) {
	f.record("start:" + containerID)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failStart[containerID]; err != nil {
		return client.ContainerStartResult{}, err
	}
	if item, ok := f.containers[containerID]; ok {
		item.running = true
		item.status = container.ContainerState("running")
	}
	return client.ContainerStartResult{}, nil
}

func (f *fakeDockerClient) ContainerStop(_ context.Context, containerID string, _ client.ContainerStopOptions) (client.ContainerStopResult, error) {
	f.record("stop:" + containerID)
	f.mu.Lock()
	defer f.mu.Unlock()
	err := f.failStop[containerID]
	if err != nil && !f.stopReallyStops {
		return client.ContainerStopResult{}, err
	}
	if item, ok := f.containers[containerID]; ok {
		item.running = false
		item.status = container.ContainerState("exited")
	}
	return client.ContainerStopResult{}, err
}

func (f *fakeDockerClient) ContainerRemove(_ context.Context, containerID string, _ client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	f.record("remove:" + containerID)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failRemove[containerID]; err != nil {
		return client.ContainerRemoveResult{}, err
	}
	delete(f.containers, containerID)
	return client.ContainerRemoveResult{}, nil
}

func (f *fakeDockerClient) ContainerRename(_ context.Context, containerID string, options client.ContainerRenameOptions) (client.ContainerRenameResult, error) {
	f.record("rename:" + containerID + "->" + options.NewName)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failRename[containerID]; err != nil {
		return client.ContainerRenameResult{}, err
	}
	if item, ok := f.containers[containerID]; ok {
		item.name = options.NewName
	}
	return client.ContainerRenameResult{}, nil
}

func (f *fakeDockerClient) ImageList(context.Context, client.ImageListOptions) (client.ImageListResult, error) {
	return client.ImageListResult{}, nil
}

func (f *fakeDockerClient) ImageInspect(context.Context, string, ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	return client.ImageInspectResult{}, nil
}

// newTestUpdater wires a Service onto the fake daemon and a controllable event
// channel, with no real client and no real hub behind it.
func newTestUpdater(fake *fakeDockerClient, events <-chan containerSrv.Event) *Service {
	return &Service{
		client:    fake,
		subscribe: func() (<-chan containerSrv.Event, func()) { return events, func() {} },
	}
}

func healthy() *container.Health { return &container.Health{Status: container.Healthy} }

func noHealth() *container.Health { return &container.Health{Status: container.NoHealthcheck} }

func testSummary(id, name string) container.Summary {
	return container.Summary{ID: id, Names: []string{"/" + name}}
}

// signalOnReplacement waits for the replacement container to exist, then emits
// the given event on its behalf. It never touches t, because a require failure
// outside the test goroutine is undefined behaviour.
func signalOnReplacement(fake *fakeDockerClient, events chan<- containerSrv.Event, event containerSrv.Event) {
	go func() {
		for range 400 {
			if fake.exists("new000000000b") {
				events <- event
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
}

// The failure the whole subsystem exists to handle: the replacement starts,
// then dies before it can be trusted. The old container must come back and the
// replacement must be gone.
func TestRecreateRollsBackWhenReplacementDiesWithoutHealthcheck(t *testing.T) {
	fake := newFakeDockerClient()
	fake.createdHealth = noHealth()
	fake.add("old000000000a", "app", true, noHealth())
	events := make(chan containerSrv.Event, 4)
	service := newTestUpdater(fake, events)
	signalOnReplacement(fake, events, containerSrv.Event{Action: "die", ID: "new000000000"})

	err := service.ContainerRecreateWithOptions(t.Context(), "app:v2", testSummary("old000000000a", "app"), true)
	require.Error(t, err)
	require.True(t, IsRolledBack(err), "a dead replacement must be reported as a rollback, not a plain failure")
	require.False(t, fake.exists("new000000000b"), "the replacement must not survive its own failure")
	require.True(t, fake.isRunning("old000000000a"), "the original container must be running again")
	require.Equal(t, "app", fake.nameOf("old000000000a"), "the original must keep its name")
}

// The regression that made the previous behaviour indefensible: with no
// Dockman healthcheck label, the check returned nil straight after start and
// the old container was destroyed even though nothing had been verified.
func TestRecreateWaitsForHealthyBeforeDestroyingTheOldContainer(t *testing.T) {
	fake := newFakeDockerClient()
	fake.createdHealth = &container.Health{Status: container.Starting}
	fake.add("old000000000a", "app", true, healthy())
	events := make(chan containerSrv.Event, 4)
	service := newTestUpdater(fake, events)
	signalOnReplacement(fake, events, containerSrv.Event{Action: "health_status", Status: "healthy", ID: "new000000000"})

	require.NoError(t, service.ContainerRecreateWithOptions(t.Context(), "app:v2", testSummary("old000000000a", "app"), true))
	require.False(t, fake.exists("old000000000a"), "the old container is removed once the replacement is proven healthy")
	require.Equal(t, "app", fake.nameOf("new000000000b"), "the replacement takes over the name")
	// The order is the whole point: nothing destructive may precede the verdict.
	calls := fake.recorded()
	require.Less(t, indexOfCall(calls, "start:new000000000b"), indexOfCall(calls, "remove:old000000000a"))
}

func TestRecreateRollsBackWhenReplacementReportsUnhealthy(t *testing.T) {
	fake := newFakeDockerClient()
	fake.createdHealth = &container.Health{Status: container.Starting}
	fake.add("old000000000a", "app", true, healthy())
	events := make(chan containerSrv.Event, 4)
	service := newTestUpdater(fake, events)
	signalOnReplacement(fake, events, containerSrv.Event{Action: "health_status", Status: "unhealthy", ID: "new000000000"})

	err := service.ContainerRecreateWithOptions(t.Context(), "app:v2", testSummary("old000000000a", "app"), true)
	require.Error(t, err)
	require.True(t, IsRolledBack(err))
	require.False(t, fake.exists("new000000000b"))
	require.True(t, fake.isRunning("old000000000a"))
}

func indexOfCall(calls []string, call string) int {
	for index, item := range calls {
		if item == call {
			return index
		}
	}
	return -1
}

// Compensation used to run on the caller's context. A cancelled context is the
// ordinary way to reach this path, so the cleanup could not succeed and the
// replacement was left behind alongside the old container.
func TestRecreateCleansUpReplacementWhenContextIsCancelled(t *testing.T) {
	fake := newFakeDockerClient()
	fake.add("old000000000a", "app", true, healthy())
	events := make(chan containerSrv.Event)
	service := newTestUpdater(fake, events)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer cancel()
		for range 400 {
			if fake.exists("new000000000b") {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	err := service.ContainerRecreateWithOptions(ctx, "app:v2", testSummary("old000000000a", "app"), true)
	require.Error(t, err)
	require.False(t, fake.exists("new000000000b"), "the replacement must be removed even though ctx is dead")
	require.True(t, fake.exists("old000000000a"), "the original container must be preserved")
}

// A container that was already stopped is swapped create-before-remove and
// must stay stopped, without any healthcheck being demanded of it.
func TestRecreateSwapsStoppedContainerWithoutStartingIt(t *testing.T) {
	fake := newFakeDockerClient()
	fake.add("old000000000a", "app", false, noHealth())
	service := newTestUpdater(fake, make(chan containerSrv.Event))

	require.NoError(t, service.ContainerRecreateWithOptions(t.Context(), "app:v2", testSummary("old000000000a", "app"), true))
	require.False(t, fake.exists("old000000000a"))
	require.Equal(t, "app", fake.nameOf("new000000000b"))
	require.False(t, fake.isRunning("new000000000b"), "a stopped container must not be started by an update")
	require.NotContains(t, fake.recorded(), "start:new000000000b")
}

// Removing the old container can fail on a restrictive socket proxy. The
// replacement is then withdrawn and the original brought back up, rather than
// leaving two containers fighting over one name.
func TestRecreateRestoresOldContainerWhenItCannotBeRemoved(t *testing.T) {
	fake := newFakeDockerClient()
	fake.add("old000000000a", "app", true, healthy())
	fake.failRemove["old000000000a"] = errors.New("permission denied")
	events := make(chan containerSrv.Event, 4)
	service := newTestUpdater(fake, events)

	go func() {
		for range 200 {
			if fake.exists("new000000000b") {
				events <- containerSrv.Event{Action: "health_status", Status: "healthy", ID: "new000000000"}
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	err := service.ContainerRecreateWithOptions(t.Context(), "app:v2", testSummary("old000000000a", "app"), true)
	require.ErrorContains(t, err, "failed to remove old container")
	require.False(t, fake.exists("new000000000b"), "the replacement is withdrawn")
	require.True(t, fake.isRunning("old000000000a"), "the original is restarted")
}

// verifyHealth=false must remain exactly as permissive as before: it is how
// callers with rollback disabled ask for a straight swap.
func TestRecreateSkipsVerificationWhenHealthIsNotRequested(t *testing.T) {
	fake := newFakeDockerClient()
	fake.add("old000000000a", "app", true, noHealth())
	service := newTestUpdater(fake, make(chan containerSrv.Event))

	require.NoError(t, service.ContainerRecreateWithOptions(t.Context(), "app:v2", testSummary("old000000000a", "app"), false))
	require.False(t, fake.exists("old000000000a"))
	require.Equal(t, "app", fake.nameOf("new000000000b"))
}

// The stop was the one step that gave up without trying to put the service
// back. A daemon that carried the stop out and still reported an error - a
// deadline reached mid-stop - left the container down indefinitely.
func TestRecreateBringsTheContainerBackWhenStopReportsAnError(t *testing.T) {
	fake := newFakeDockerClient()
	fake.add("old000000000a", "app", true, noHealth())
	fake.failStop["old000000000a"] = errors.New("context deadline exceeded")
	fake.stopReallyStops = true
	service := newTestUpdater(fake, make(chan containerSrv.Event))

	err := service.ContainerRecreateWithOptions(t.Context(), "app:v2", testSummary("old000000000a", "app"), true)
	require.ErrorContains(t, err, "failed to stop container app")
	require.True(t, fake.isRunning("old000000000a"), "the service must not be left down")
	require.False(t, fake.exists("new000000000b"), "no replacement is created after a failed stop")
}

// A stop that genuinely did not take effect leaves the container running; the
// restore attempt must be harmless there too.
func TestRecreateLeavesARunningContainerAloneWhenStopHadNoEffect(t *testing.T) {
	fake := newFakeDockerClient()
	fake.add("old000000000a", "app", true, noHealth())
	fake.failStop["old000000000a"] = errors.New("permission denied")
	service := newTestUpdater(fake, make(chan containerSrv.Event))

	err := service.ContainerRecreateWithOptions(t.Context(), "app:v2", testSummary("old000000000a", "app"), true)
	require.ErrorContains(t, err, "left running")
	require.True(t, fake.isRunning("old000000000a"))
}

// The replacement is built under a temporary name before it takes the real
// one. That name used to be the container's name plus "_updated", the same
// string on every attempt: when a compensating removal failed - a daemon that
// is busy, a container stuck in Removing - the leftover sat there and every
// later attempt collided with it on creation. The service could then never be
// updated again, and nothing in the error pointed at the container to delete.
func TestARemovalThatFailedDoesNotBlockTheNextUpdate(t *testing.T) {
	fake := newFakeDockerClient()
	fake.add("old000000001", "web", true, noHealth())
	fake.createdHealth = noHealth()
	// The replacement will not start, and the cleanup that follows fails too,
	// so its container is left behind next to the original.
	fake.failStart["new000000000b"] = errors.New("no such image")
	fake.failRemove["new000000000b"] = errors.New("removal already in progress")

	events := make(chan containerSrv.Event, 4)
	updater := newTestUpdater(fake, events)

	firstErr := updater.ContainerRecreateWithOptions(t.Context(), "nginx:latest", testSummary("old000000001", "web"), false)
	require.Error(t, firstErr)
	require.True(t, fake.exists("new000000000b"), "this test needs the leftover the failure produces")
	require.True(t, fake.isRunning("old000000001"), "the original must have been brought back")

	// Second attempt, on the very same host state.
	secondErr := updater.ContainerRecreateWithOptions(t.Context(), "nginx:latest", testSummary("old000000001", "web"), false)

	require.NotContains(t, fmt.Sprint(secondErr), "already in use",
		"a leftover from the previous attempt blocked the retry")
	require.Equal(t, "web", fake.nameOf("new000000000c"),
		"the retry must have produced the replacement and given it the real name")
	require.False(t, fake.exists("old000000001"), "the original must have been swapped out")
}

// And the leftover has to be nameable: an operator reading the failure needs
// to know which container to delete.
func TestAFailedCleanupNamesTheContainerItLeftBehind(t *testing.T) {
	fake := newFakeDockerClient()
	fake.add("old000000001", "web", true, noHealth())
	fake.createdHealth = noHealth()
	fake.failStart["new000000000b"] = errors.New("no such image")
	fake.failRemove["new000000000b"] = errors.New("removal already in progress")

	events := make(chan containerSrv.Event, 4)
	err := newTestUpdater(fake, events).ContainerRecreateWithOptions(
		t.Context(), "nginx:latest", testSummary("old000000001", "web"), false)

	require.Error(t, err)
	leftover := fake.nameOf("new000000000b")
	require.NotEmpty(t, leftover)
	require.Contains(t, fmt.Sprint(err), leftover,
		"the error must name the container left behind so it can be removed")
}
