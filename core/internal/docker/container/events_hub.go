package container

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RA341/dockman/pkg/syncmap"
	"github.com/moby/moby/api/types/events"
	"github.com/moby/moby/client"
	"github.com/rs/zerolog/log"
)

// Event is a filtered daemon event relevant to container state, ready for
// the UI to react to (and for the activity log to persist later).
type Event struct {
	// base action: create/start/stop/die/kill/restart/pause/unpause/destroy/
	// rename/update/oom/health_status
	Action string
	// health_status only: healthy / unhealthy / ...
	Status   string
	ID       string // 12-char container id
	Name     string
	Image    string
	TimeNano int64
}

// allowedActions lists the container actions worth reacting to; everything
// else (exec_*, attach, top, archive...) is noise for state-driven views.
var allowedActions = map[string]struct{}{
	"create": {}, "start": {}, "stop": {}, "die": {}, "kill": {},
	"restart": {}, "pause": {}, "unpause": {}, "destroy": {},
	"rename": {}, "update": {}, "oom": {}, "health_status": {},
}

// eventsHub fans a single daemon /events subscription out to every listener
// of a host. It lives package-wide keyed by the moby client — one per
// connected host — because the request-scoped services are rebuilt on every
// RPC. The daemon subscription starts with the first listener, reconnects
// with backoff when the daemon drops it, and stops with the last listener.
const (
	// recentDedupWindow is how long a delivered event is remembered so an
	// identical redelivery can be recognised.
	recentDedupWindow = 5 * time.Second
	// recentEventsCap is the hard ceiling on that memory. Dropping the stale
	// entries is the normal way back under it; a burst of more than this many
	// distinct events inside the dedup window has none to drop, and used to
	// simply grow - which also turned every further event into a full scan of
	// the map. Past the cap the oldest entries go, dedup being best-effort.
	recentEventsCap = 512
	// healthMemory is how long a container's last health status is remembered
	// once it stops being reported. The daemon repeats health_status on every
	// probe, so a container that has said nothing for this long is gone,
	// stopped, or has no healthcheck running - in every case nothing is left to
	// compare a transition against.
	healthMemory = 30 * time.Minute
	// healthMemoryCap bounds that memory against a flood of short-lived
	// containers, all of them still inside the window.
	healthMemoryCap = 4096
	// healthSweepInterval paces the expiry scan so it stays amortised rather
	// than costing a full map walk on every event.
	healthSweepInterval = healthMemory / 4
)

type healthState struct {
	status string
	at     time.Time
}

type eventsHub struct {
	mu          sync.Mutex
	subscribers map[chan Event]struct{}
	stop        context.CancelFunc

	// the daemon repeats health_status on every probe; only transitions are
	// interesting
	//
	// This used to be cleared on `destroy` alone, which is a signal that never
	// arrives when the container is destroyed while the stream is down - a
	// daemon or socket-proxy restart, a reconnect backoff. The entry then
	// stayed for the lifetime of the process. Each entry now carries when it
	// was last confirmed, so it can be forgotten on its own.
	lastHealth map[string]healthState
	// transport-level dedup, Dockhand-style: the same (container, action,
	// timestamp) delivered twice is dropped, distinct events never are
	recent map[string]int64
	// lastHealthSweep paces the health map's expiry scan
	lastHealthSweep time.Time
	// injectable clock; the maps age, so their tests must not have to sleep
	now func() time.Time
}

func newEventsHub() *eventsHub {
	return &eventsHub{
		subscribers: make(map[chan Event]struct{}),
		lastHealth:  make(map[string]healthState),
		recent:      make(map[string]int64),
		now:         time.Now,
	}
}

var eventHubs syncmap.Map[*client.Client, *eventsHub]

// ReleaseClientState drops package-wide caches when a host connection is
// closed or replaced. Without this hook, reconnecting a host leaves the old
// moby client and its event/cache state reachable forever.
func ReleaseClientState(cli *client.Client) {
	if cli == nil {
		return
	}
	if hub, ok := eventHubs.Load(cli); ok {
		hub.mu.Lock()
		if hub.stop != nil {
			hub.stop()
			hub.stop = nil
		}
		hub.subscribers = make(map[chan Event]struct{})
		hub.mu.Unlock()
	}
	eventHubs.Delete(cli)
	hostCaches.Delete(cli)
}

func hubFor(cli *client.Client) *eventsHub {
	hub, _ := eventHubs.LoadOrStore(cli, newEventsHub())
	return hub
}

// SubscribeEvents delivers this host's filtered container events until the
// returned cancel function is called. A slow consumer drops events rather
// than blocking the other listeners.
func (s *Service) SubscribeEvents() (<-chan Event, func()) {
	hub := hubFor(s.Client)

	ch := make(chan Event, 16)
	hub.mu.Lock()
	hub.subscribers[ch] = struct{}{}
	if hub.stop == nil {
		runCtx, cancel := context.WithCancel(context.Background())
		hub.stop = cancel
		go hub.run(runCtx, s.Client)
	}
	hub.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			hub.mu.Lock()
			delete(hub.subscribers, ch)
			if len(hub.subscribers) == 0 && hub.stop != nil {
				hub.stop()
				hub.stop = nil
			}
			hub.mu.Unlock()
		})
	}
	return ch, unsubscribe
}

func (h *eventsHub) run(ctx context.Context, cli *client.Client) {
	filters := client.Filters{}
	filters.Add("type", string(events.ContainerEventType))

	backoff := time.Second
	for {
		res := cli.Events(ctx, client.EventsListOptions{Filters: filters})

	stream:
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-res.Messages:
				if !ok {
					break stream
				}
				backoff = time.Second
				if ev, ok := h.filter(msg); ok {
					h.broadcast(ev)
				}
			case err, ok := <-res.Err:
				if ctx.Err() != nil {
					return
				}
				if ok && err != nil {
					log.Warn().Err(err).Msg("docker events stream interrupted, reconnecting")
				}
				break stream
			}
		}

		// the client does not reopen the stream itself; back off and resubscribe
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (h *eventsHub) broadcast(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subscribers {
		select {
		case ch <- ev:
		default: // slow consumer: drop rather than block the hub
		}
	}
}

// pruneLocked keeps both maps bounded. It runs on the way through, on the
// lock the caller already holds: no timer, no goroutine, no background sweep,
// so an idle Dockman pays nothing for it.
func (h *eventsHub) pruneLocked(moment time.Time) {
	now := moment.UnixNano()
	if len(h.recent) > recentEventsCap {
		for key, at := range h.recent {
			if now-at > int64(recentDedupWindow) {
				delete(h.recent, key)
			}
		}
		// A burst of distinct events inside the dedup window leaves nothing
		// stale to drop. Dedup is best-effort protection against a duplicated
		// delivery, never a correctness requirement, so the map is trimmed
		// rather than allowed to grow.
		for key := range h.recent {
			if len(h.recent) <= recentEventsCap {
				break
			}
			delete(h.recent, key)
		}
	}

	// The health map is swept on a slow cadence rather than on every event:
	// scanning it each time would be the O(n)-per-event cost this is meant to
	// remove. Being over the cap forces a sweep regardless.
	overCap := len(h.lastHealth) > healthMemoryCap
	if !overCap && moment.Sub(h.lastHealthSweep) < healthSweepInterval {
		return
	}
	h.lastHealthSweep = moment
	for id, state := range h.lastHealth {
		if moment.Sub(state.at) >= healthMemory {
			delete(h.lastHealth, id)
		}
	}
	for id := range h.lastHealth {
		if len(h.lastHealth) <= healthMemoryCap {
			break
		}
		delete(h.lastHealth, id)
	}
}

func (h *eventsHub) filter(msg events.Message) (Event, bool) {
	if msg.Type != events.ContainerEventType {
		return Event{}, false
	}

	// health arrives as "health_status: healthy"
	action, status, _ := strings.Cut(string(msg.Action), ": ")
	if _, ok := allowedActions[action]; !ok {
		return Event{}, false
	}

	id := msg.Actor.ID
	if len(id) > 12 {
		id = id[:12]
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	moment := h.now()

	if action == "health_status" {
		if previous, known := h.lastHealth[id]; known && previous.status == status {
			// Same status, but it was just confirmed: keep the memory alive so
			// a container that is reporting steadily is never forgotten.
			h.lastHealth[id] = healthState{status: status, at: moment}
			return Event{}, false
		}
		h.lastHealth[id] = healthState{status: status, at: moment}
	}
	if action == "destroy" {
		delete(h.lastHealth, id)
	}

	key := id + "|" + string(msg.Action) + "|" + strconv.FormatInt(msg.TimeNano, 10)
	now := moment.UnixNano()
	if last, ok := h.recent[key]; ok && now-last < int64(recentDedupWindow) {
		return Event{}, false
	}
	h.recent[key] = now
	h.pruneLocked(moment)

	return Event{
		Action:   action,
		Status:   status,
		ID:       id,
		Name:     msg.Actor.Attributes["name"],
		Image:    msg.Actor.Attributes["image"],
		TimeNano: msg.TimeNano,
	}, true
}
