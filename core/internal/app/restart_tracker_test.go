package app

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testRestartTracker(t *testing.T) (*restartTracker, func(time.Duration)) {
	t.Helper()
	moment := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	return newRestartTracker(func() time.Time { return moment }),
		func(d time.Duration) { moment = moment.Add(d) }
}

// The behaviour being preserved: a container coming back up with a higher
// restart count than the one taken when it died is an automatic restart, and
// worth reporting.
func TestAnIncreasedRestartCountIsReported(t *testing.T) {
	tracker, _ := testRestartTracker(t)

	tracker.observe("abc", 3)
	require.True(t, tracker.restarted("abc", 4))
}

// A container simply being started, with no prior count and no increase, is
// not a restart.
func TestAnOrdinaryStartIsNotReported(t *testing.T) {
	tracker, _ := testRestartTracker(t)

	require.False(t, tracker.restarted("fresh", 0), "a first start has nothing to compare against")

	tracker.observe("abc", 3)
	require.False(t, tracker.restarted("abc", 3), "the same count is not a restart")
}

// A crash loop reports, it does not flood.
func TestRepeatedRestartsAreSpacedOut(t *testing.T) {
	tracker, advance := testRestartTracker(t)

	tracker.observe("abc", 1)
	require.True(t, tracker.restarted("abc", 2))

	advance(restartNotifyGap / 2)
	tracker.observe("abc", 2)
	require.False(t, tracker.restarted("abc", 3), "two notices inside the quiet period")

	advance(restartNotifyGap * 2)
	tracker.observe("abc", 3)
	require.True(t, tracker.restarted("abc", 4), "the quiet period has passed")
}

// An explicit restart action is reported on its own, and starts the quiet
// period so the start that follows it is not reported a second time.
func TestAnExplicitRestartSilencesTheStartThatFollows(t *testing.T) {
	tracker, _ := testRestartTracker(t)

	tracker.observe("abc", 1)
	tracker.notified("abc")

	require.False(t, tracker.restarted("abc", 2),
		"the restart action was already reported; its start must not report again")
}

// destroy is the cheapest signal that a container is gone and must still clear
// it at once.
func TestForgettingAContainerDropsItImmediately(t *testing.T) {
	tracker, _ := testRestartTracker(t)

	tracker.observe("abc", 1)
	tracker.forget("abc")

	require.Empty(t, tracker.states)
}

// The residual leak this closes: a container removed while the daemon event
// stream was reconnecting never delivers its destroy, so nothing ever dropped
// its entry.
func TestAContainerThatDisappearedWithoutADestroyIsForgotten(t *testing.T) {
	tracker, advance := testRestartTracker(t)

	tracker.observe("vanished", 1)
	require.Len(t, tracker.states, 1)

	advance(restartMemory + time.Minute)
	tracker.observe("still-here", 1)

	require.NotContains(t, tracker.states, "vanished",
		"a container whose destroy was missed is remembered forever")
	require.Contains(t, tracker.states, "still-here")
}

// And a host churning containers faster than the sweep reclaims them must not
// be able to grow it without limit either.
func TestRestartMemoryIsCapped(t *testing.T) {
	tracker, _ := testRestartTracker(t)

	for i := range restartMemoryCap * 3 {
		tracker.observe("c"+strconv.Itoa(i), 1)
	}

	require.LessOrEqual(t, len(tracker.states), restartMemoryCap)
}
