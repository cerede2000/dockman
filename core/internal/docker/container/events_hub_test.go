package container

import (
	"strconv"
	"testing"
	"time"

	"github.com/moby/moby/api/types/events"
	"github.com/stretchr/testify/require"
)

func testHub(t *testing.T) (*eventsHub, func(time.Duration)) {
	t.Helper()
	moment := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	hub := newEventsHub()
	hub.now = func() time.Time { return moment }
	return hub, func(d time.Duration) { moment = moment.Add(d) }
}

func containerEvent(id, action string, nano int64) events.Message {
	return events.Message{
		Type:   events.ContainerEventType,
		Action: events.Action(action),
		Actor: events.Actor{
			ID:         id,
			Attributes: map[string]string{"name": id, "image": "img"},
		},
		TimeNano: nano,
	}
}

// The dedup window is the reason `recent` exists, so it has to keep working.
func TestTheSameEventDeliveredTwiceIsDroppedOnce(t *testing.T) {
	hub, _ := testHub(t)

	_, first := hub.filter(containerEvent("abc", "start", 1))
	_, second := hub.filter(containerEvent("abc", "start", 1))
	_, distinct := hub.filter(containerEvent("abc", "start", 2))

	require.True(t, first)
	require.False(t, second, "a repeated delivery of the same event must be dropped")
	require.True(t, distinct, "a distinct event must never be dropped")
}

// Health arrives on every probe; only transitions are interesting.
func TestOnlyHealthTransitionsGetThrough(t *testing.T) {
	hub, advance := testHub(t)

	_, first := hub.filter(containerEvent("abc", "health_status: healthy", 1))
	advance(time.Minute)
	_, repeat := hub.filter(containerEvent("abc", "health_status: healthy", 2))
	advance(time.Minute)
	event, changed := hub.filter(containerEvent("abc", "health_status: unhealthy", 3))

	require.True(t, first)
	require.False(t, repeat, "the same health status repeated must be dropped")
	require.True(t, changed)
	require.Equal(t, "unhealthy", event.Status)
}

// The defect: `recent` only ever dropped entries older than the dedup window,
// and only once past its cap. A burst of more than 512 distinct events inside
// that window - a large compose up, a restart storm - deleted nothing, so the
// map kept growing and every further event paid a full scan of it.
func TestRecentStaysBoundedThroughABurst(t *testing.T) {
	hub, _ := testHub(t)

	// Every event distinct, all inside the dedup window: nothing is stale.
	for i := range recentEventsCap * 4 {
		hub.filter(containerEvent("c"+strconv.Itoa(i), "start", int64(i)))
	}

	hub.mu.Lock()
	size := len(hub.recent)
	hub.mu.Unlock()
	require.LessOrEqual(t, size, recentEventsCap,
		"the dedup map grew past its cap during a burst")
}

// The defect: `lastHealth` was only ever cleared on `destroy`. A container
// destroyed while the events stream was down - a daemon or socket-proxy
// restart, a reconnect backoff - never delivered its destroy, so its entry
// stayed for the lifetime of the process.
func TestHealthMemoryForgetsContainersThatStoppedReporting(t *testing.T) {
	hub, advance := testHub(t)

	hub.filter(containerEvent("gone", "health_status: healthy", 1))
	hub.mu.Lock()
	require.Len(t, hub.lastHealth, 1)
	hub.mu.Unlock()

	// The container is destroyed while the stream is down: no destroy event
	// ever reaches Dockman. Some other container keeps the hub busy.
	advance(healthMemory + time.Minute)
	hub.filter(containerEvent("alive", "health_status: healthy", 2))

	hub.mu.Lock()
	defer hub.mu.Unlock()
	_, remembered := hub.lastHealth["gone"]
	require.False(t, remembered, "a container that stopped reporting is remembered forever")
	require.Contains(t, hub.lastHealth, "alive", "the containers still reporting must be kept")
}

// A flood of short-lived containers must not be able to grow the health map
// without limit either, even inside the memory window.
func TestHealthMemoryIsCapped(t *testing.T) {
	hub, _ := testHub(t)

	for i := range healthMemoryCap * 3 {
		hub.filter(containerEvent("c"+strconv.Itoa(i), "health_status: healthy", int64(i)))
	}

	hub.mu.Lock()
	defer hub.mu.Unlock()
	require.LessOrEqual(t, len(hub.lastHealth), healthMemoryCap)
}

// destroy is still the cheapest signal that a container is gone, and it must
// keep clearing the entry immediately rather than waiting for the window.
func TestDestroyClearsTheHealthEntryAtOnce(t *testing.T) {
	hub, _ := testHub(t)

	hub.filter(containerEvent("abc", "health_status: healthy", 1))
	hub.filter(containerEvent("abc", "destroy", 2))

	hub.mu.Lock()
	defer hub.mu.Unlock()
	require.NotContains(t, hub.lastHealth, "abc")
}
