package auth

import (
	"fmt"
	"sync"
	"time"
)

// Password checking is deliberately slow - bcrypt at the default cost costs
// something like 50-100ms - which raises the price of guessing but does not cap
// it. Nothing else stood between an exposed Dockman and an unlimited stream of
// attempts.
//
// The counter is keyed by username rather than by client address on purpose:
// behind a Cloudflare tunnel or any reverse proxy every request arrives from
// the same address, so an address key would either throttle everyone at once or
// be trivially defeated by a forged header.
//
// That choice means somebody who knows a username can slow that account down.
// It cannot lock it out: the wait is capped, it expires on its own, and a
// correct password clears it immediately.
const (
	// Typing a password wrong a few times is ordinary. The wait starts after
	// that, so a legitimate user never meets it.
	loginFreeAttempts = 5
	loginBaseDelay    = time.Second
	loginMaxDelay     = time.Minute
	// An entry that has seen nothing for this long is forgotten.
	loginAttemptTTL = 15 * time.Minute
	// A bound on what a flood of distinct usernames can cost in memory. Past
	// it, tracking stops rather than growing - throttling is a safeguard, and a
	// safeguard that can exhaust the host is worse than the risk it covers.
	maxTrackedLogins = 1024
)

type loginAttempt struct {
	failures int
	last     time.Time
}

// loginThrottle holds nothing and does nothing until a login fails. There is no
// timer, no goroutine and no background sweep: entries are pruned on the way
// through, so an idle Dockman pays exactly nothing for it.
type loginThrottle struct {
	mu       sync.Mutex
	attempts map[string]loginAttempt
	now      func() time.Time
}

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{attempts: map[string]loginAttempt{}, now: time.Now}
}

// retryAfter reports how long this username must wait before another attempt is
// worth making. Zero means it may proceed.
func (t *loginThrottle) retryAfter(username string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	attempt, tracked := t.attempts[username]
	if !tracked {
		return 0
	}
	moment := t.now()
	if moment.Sub(attempt.last) >= loginAttemptTTL {
		delete(t.attempts, username)
		return 0
	}
	wait := attempt.last.Add(t.delayFor(attempt.failures)).Sub(moment)
	if wait <= 0 {
		return 0
	}
	return wait
}

func (t *loginThrottle) delayFor(failures int) time.Duration {
	if failures <= loginFreeAttempts {
		return 0
	}
	delay := loginBaseDelay
	for i := loginFreeAttempts + 1; i < failures; i++ {
		delay *= 2
		if delay >= loginMaxDelay {
			return loginMaxDelay
		}
	}
	return delay
}

func (t *loginThrottle) recordFailure(username string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	moment := t.now()
	attempt, tracked := t.attempts[username]
	if !tracked {
		if len(t.attempts) >= maxTrackedLogins {
			t.pruneLocked(moment)
		}
		if len(t.attempts) >= maxTrackedLogins {
			// Still full of live entries: stop tracking rather than grow.
			return
		}
	}
	if moment.Sub(attempt.last) >= loginAttemptTTL {
		attempt.failures = 0
	}
	attempt.failures++
	attempt.last = moment
	t.attempts[username] = attempt
}

func (t *loginThrottle) recordSuccess(username string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.attempts, username)
	t.pruneLocked(t.now())
}

func (t *loginThrottle) pruneLocked(moment time.Time) {
	for name, attempt := range t.attempts {
		if moment.Sub(attempt.last) >= loginAttemptTTL {
			delete(t.attempts, name)
		}
	}
}

// ErrTooManyLoginAttempts is what a throttled caller gets. It names the wait so
// a locked-out administrator knows this is a delay rather than a broken
// password, and it deliberately says nothing about whether the username exists.
type ErrTooManyLoginAttempts struct{ RetryAfter time.Duration }

func (e ErrTooManyLoginAttempts) Error() string {
	return fmt.Sprintf("too many failed sign-in attempts; try again in %s", e.RetryAfter.Round(time.Second))
}
