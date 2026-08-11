package app

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// watcherProbe records what the registry started and lets a test wait for a
// watcher to actually be running rather than sleeping and hoping.
type watcherProbe struct {
	started atomic.Int64
	stopped atomic.Int64

	mu      sync.Mutex
	running map[string]chan struct{}
}

func newWatcherProbe() *watcherProbe {
	return &watcherProbe{running: make(map[string]chan struct{})}
}

func (p *watcherProbe) watch(ctx context.Context, hostname string) {
	p.started.Add(1)
	p.mu.Lock()
	signal, ok := p.running[hostname]
	if !ok {
		signal = make(chan struct{}, 8)
		p.running[hostname] = signal
	}
	p.mu.Unlock()
	select {
	case signal <- struct{}{}:
	default:
	}
	<-ctx.Done()
	p.stopped.Add(1)
}

func (p *watcherProbe) awaitStart(t *testing.T, hostname string) {
	t.Helper()
	p.mu.Lock()
	signal, ok := p.running[hostname]
	if !ok {
		signal = make(chan struct{}, 8)
		p.running[hostname] = signal
	}
	p.mu.Unlock()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("no watcher started for host %q", hostname)
	}
}

func eventually(t *testing.T, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(message)
}

// The defect this replaces: watchers were mounted once, at boot, over the
// hosts connected at that moment. A host added from the interface afterwards
// - or one reconnected in the background once its daemon came up - was never
// watched, so it silently produced no OOM, unhealthy or restart notification
// and no automatic update-policy refresh.
func TestAHostConnectedLaterIsWatched(t *testing.T) {
	probe := newWatcherProbe()
	watchers := newHostWatchers(t.Context(), probe.watch)

	watchers.watch("added-later")

	probe.awaitStart(t, "added-later")
	require.Equal(t, 1, watchers.watched())
}

// Reconnection is the case that turns a fix into a leak if it is not handled:
// the same host connecting twice must end with one watcher, not two.
func TestReconnectingAHostReplacesItsWatcher(t *testing.T) {
	probe := newWatcherProbe()
	watchers := newHostWatchers(t.Context(), probe.watch)

	watchers.watch("flaky")
	probe.awaitStart(t, "flaky")
	watchers.watch("flaky")
	probe.awaitStart(t, "flaky")

	require.EqualValues(t, 2, probe.started.Load())
	eventually(t, func() bool { return probe.stopped.Load() == 1 },
		"the first watcher was left running alongside its replacement")
	require.Equal(t, 1, watchers.watched(), "one host, one watcher")
}

// A host that is disabled, deleted or renamed must stop being watched: its
// Docker client is closed, so the watcher would sit on a channel nothing ever
// writes to again.
func TestReleasingAHostStopsItsWatcher(t *testing.T) {
	probe := newWatcherProbe()
	watchers := newHostWatchers(t.Context(), probe.watch)

	watchers.watch("removed")
	probe.awaitStart(t, "removed")
	watchers.release("removed")

	eventually(t, func() bool { return probe.stopped.Load() == 1 }, "the watcher outlived its host")
	require.Zero(t, watchers.watched())
}

// Releasing then re-adding a host - what Toggle does - has to start a watcher
// again rather than leave the host silently unwatched.
func TestAHostWatchedAgainAfterBeingReleased(t *testing.T) {
	probe := newWatcherProbe()
	watchers := newHostWatchers(t.Context(), probe.watch)

	watchers.watch("toggled")
	probe.awaitStart(t, "toggled")
	watchers.release("toggled")
	eventually(t, func() bool { return probe.stopped.Load() == 1 }, "the first watcher did not stop")

	watchers.watch("toggled")
	probe.awaitStart(t, "toggled")
	require.Equal(t, 1, watchers.watched())
}

// A watcher that returns on its own (its events channel closed) must not leave
// a stale entry claiming the host is still watched, or reconnecting it would
// be treated as a replacement of something that is gone.
func TestAWatcherThatReturnsUnregistersItself(t *testing.T) {
	release := make(chan struct{})
	var started atomic.Int64
	watchers := newHostWatchers(t.Context(), func(context.Context, string) {
		started.Add(1)
		<-release
	})

	watchers.watch("short-lived")
	eventually(t, func() bool { return started.Load() == 1 }, "the watcher never started")
	require.Equal(t, 1, watchers.watched())

	close(release)
	eventually(t, func() bool { return watchers.watched() == 0 },
		"a watcher that returned stayed registered")
}

// Shutdown stops every watcher, and a host connecting during shutdown must not
// start one that would immediately have to be torn down.
func TestShutdownStopsEveryWatcher(t *testing.T) {
	probe := newWatcherProbe()
	ctx, shutdown := context.WithCancel(t.Context())
	watchers := newHostWatchers(ctx, probe.watch)

	for _, host := range []string{"one", "two", "three"} {
		watchers.watch(host)
		probe.awaitStart(t, host)
	}
	require.Equal(t, 3, watchers.watched())

	shutdown()
	eventually(t, func() bool { return probe.stopped.Load() == 3 }, "watchers survived shutdown")

	watchers.watch("late")
	require.Zero(t, watchers.watched(), "a host connecting during shutdown was still registered")
	require.EqualValues(t, 3, probe.started.Load(), "a host connecting during shutdown started a watcher")
}
