package app

import (
	"time"
)

const (
	// restartNotifyGap is the quiet period between two "restarted" notices for
	// the same container, so a crash loop reports rather than floods.
	restartNotifyGap = 10 * time.Second
	// restartMemory is how long a container is remembered after it last did
	// anything. A destroy event clears it immediately; this covers the destroy
	// that never arrives because the daemon event stream was reconnecting when
	// the container went away.
	restartMemory = time.Hour
	// restartMemoryCap bounds the map against a host that churns containers
	// faster than the sweep reclaims them.
	restartMemoryCap = 4096
	// restartSweepInterval paces the expiry scan so it stays amortised instead
	// of walking the whole map on every event.
	restartSweepInterval = restartMemory / 4
)

type restartState struct {
	count    int
	notified time.Time
	seen     time.Time
}

// restartTracker decides when a container's automatic restart is worth
// notifying about. It replaces two parallel maps that were cleared only on
// `destroy` - the one signal that does not arrive when a container is removed
// while the event stream is reconnecting, which left an entry behind for the
// life of the watcher.
type restartTracker struct {
	states    map[string]restartState
	now       func() time.Time
	lastSweep time.Time
}

func newRestartTracker(now func() time.Time) *restartTracker {
	if now == nil {
		now = time.Now
	}
	return &restartTracker{states: make(map[string]restartState), now: now}
}

// observe records a container's restart count without notifying: this is the
// baseline taken when it dies, against which the next start is compared.
func (t *restartTracker) observe(id string, count int) {
	moment := t.now()
	state := t.states[id]
	state.count = count
	state.seen = moment
	t.states[id] = state
	t.pruneLocked(moment)
}

// restarted records a container coming back up and reports whether that is a
// restart worth notifying about: the daemon's restart count has to have gone
// up since a count was actually taken, and the quiet period must have passed.
func (t *restartTracker) restarted(id string, count int) bool {
	moment := t.now()
	state, known := t.states[id]
	previous := state.count
	state.count = count
	state.seen = moment
	notify := known && count > previous && moment.Sub(state.notified) > restartNotifyGap
	if notify {
		state.notified = moment
	}
	t.states[id] = state
	t.pruneLocked(moment)
	return notify
}

// notified records an explicit restart action, which is reported on its own
// and starts the quiet period for the automatic detection above.
func (t *restartTracker) notified(id string) {
	moment := t.now()
	state := t.states[id]
	state.notified = moment
	state.seen = moment
	t.states[id] = state
	t.pruneLocked(moment)
}

// forget drops a container that is gone.
func (t *restartTracker) forget(id string) {
	delete(t.states, id)
}

func (t *restartTracker) pruneLocked(moment time.Time) {
	overCap := len(t.states) > restartMemoryCap
	if !overCap && moment.Sub(t.lastSweep) < restartSweepInterval {
		return
	}
	t.lastSweep = moment
	for id, state := range t.states {
		if moment.Sub(state.seen) >= restartMemory {
			delete(t.states, id)
		}
	}
	for id := range t.states {
		if len(t.states) <= restartMemoryCap {
			break
		}
		delete(t.states, id)
	}
}
