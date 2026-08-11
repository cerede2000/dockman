package app

import (
	"context"
	"sync"
)

// hostWatchers keeps at most one container-event watcher alive per host.
//
// Watchers used to be started once, at boot, over the hosts that happened to
// be connected at that moment. A host added from the interface later - or one
// whose Docker daemon was not ready yet and was reconnected in the background
// - therefore ran with no watcher at all: no OOM, unhealthy or restart
// notification, and no automatic policy refresh when a container's update
// labels changed. Nothing reported the absence; it was simply silent.
//
// Keying by hostname and cancelling the previous watcher before starting a new
// one is what makes reconnection safe: a host that connects, drops and
// connects again ends up with exactly one watcher, not a growing pile of
// goroutines parked on a channel nothing writes to any more.
type hostWatchers struct {
	// start runs the watcher itself and returns when ctx is done. Injected so
	// the bookkeeping can be exercised without a Docker daemon.
	start func(ctx context.Context, hostname string)

	mu     sync.Mutex
	parent context.Context
	active map[string]watcherHandle
	// generation numbers the watchers so one finishing can tell whether it is
	// still the current watcher for its host, or a replaced one whose
	// successor is already running.
	generation uint64
}

type watcherHandle struct {
	cancel     context.CancelFunc
	generation uint64
}

func newHostWatchers(parent context.Context, start func(context.Context, string)) *hostWatchers {
	return &hostWatchers{
		start:  start,
		parent: parent,
		active: make(map[string]watcherHandle),
	}
}

// watch starts a watcher for hostname, replacing any watcher already running
// for it. Calling it again for a host that is already watched is the
// reconnection case, and it must not leave the previous one behind.
func (w *hostWatchers) watch(hostname string) {
	if w == nil || hostname == "" {
		return
	}
	w.mu.Lock()
	if w.parent.Err() != nil {
		// The server is shutting down; a new watcher would only have to be
		// torn down again.
		w.mu.Unlock()
		return
	}
	if previous, ok := w.active[hostname]; ok {
		previous.cancel()
	}
	ctx, cancel := context.WithCancel(w.parent)
	w.generation++
	generation := w.generation
	w.active[hostname] = watcherHandle{cancel: cancel, generation: generation}
	start := w.start
	w.mu.Unlock()

	go func() {
		defer w.finished(hostname, generation, cancel)
		start(ctx, hostname)
	}()
}

// release stops the watcher for a host that was disabled, deleted or renamed.
func (w *hostWatchers) release(hostname string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	handle, ok := w.active[hostname]
	if ok {
		delete(w.active, hostname)
	}
	w.mu.Unlock()
	if ok {
		handle.cancel()
	}
}

// finished drops the bookkeeping entry when a watcher returns on its own, but
// only if it is still the entry that watcher installed: a reconnection that
// already replaced it must not have its successor unregistered by the old one
// finishing.
func (w *hostWatchers) finished(hostname string, generation uint64, cancel context.CancelFunc) {
	cancel()
	w.mu.Lock()
	defer w.mu.Unlock()
	if current, ok := w.active[hostname]; ok && current.generation == generation {
		delete(w.active, hostname)
	}
}

// watched reports how many hosts are currently watched.
func (w *hostWatchers) watched() int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.active)
}
