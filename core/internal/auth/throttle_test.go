package auth

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func testThrottle(t *testing.T) (*loginThrottle, func(time.Duration)) {
	t.Helper()
	moment := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	throttle := newLoginThrottle()
	throttle.now = func() time.Time { return moment }
	return throttle, func(d time.Duration) { moment = moment.Add(d) }
}

// bcrypt makes a guess cost something; nothing made a million guesses cost
// more than a million times that. A few typos still have to go through
// untouched, or the throttle becomes the problem it was meant to solve.
func TestThrottleLetsOrdinaryTyposThrough(t *testing.T) {
	throttle, _ := testThrottle(t)
	for i := 0; i < loginFreeAttempts; i++ {
		throttle.recordFailure("alice")
		require.Zero(t, throttle.retryAfter("alice"), "attempt %d must not be delayed", i+1)
	}
}

func TestThrottleBacksOffAndCaps(t *testing.T) {
	throttle, advance := testThrottle(t)
	for i := 0; i < loginFreeAttempts+1; i++ {
		throttle.recordFailure("alice")
	}
	require.Equal(t, loginBaseDelay, throttle.retryAfter("alice"))

	// Waiting it out and failing again doubles the next wait.
	advance(loginBaseDelay)
	throttle.recordFailure("alice")
	require.Equal(t, 2*loginBaseDelay, throttle.retryAfter("alice"))

	for i := 0; i < 20; i++ {
		advance(loginMaxDelay)
		throttle.recordFailure("alice")
	}
	require.Equal(t, loginMaxDelay, throttle.retryAfter("alice"),
		"the wait is capped so an account is slowed, never locked out")
}

func TestThrottleClearsOnSuccess(t *testing.T) {
	throttle, _ := testThrottle(t)
	for i := 0; i < loginFreeAttempts+3; i++ {
		throttle.recordFailure("alice")
	}
	require.NotZero(t, throttle.retryAfter("alice"))

	throttle.recordSuccess("alice")
	require.Zero(t, throttle.retryAfter("alice"), "the real user proving who they are ends it at once")
}

func TestThrottleForgetsAfterTheWindow(t *testing.T) {
	throttle, advance := testThrottle(t)
	for i := 0; i < loginFreeAttempts+5; i++ {
		throttle.recordFailure("alice")
	}
	require.NotZero(t, throttle.retryAfter("alice"))

	advance(loginAttemptTTL)
	require.Zero(t, throttle.retryAfter("alice"))
	require.Empty(t, throttle.attempts, "and the entry is gone, not merely ignored")
}

func TestThrottleIsPerUsername(t *testing.T) {
	throttle, _ := testThrottle(t)
	for i := 0; i < loginFreeAttempts+2; i++ {
		throttle.recordFailure("alice")
	}
	require.NotZero(t, throttle.retryAfter("alice"))
	require.Zero(t, throttle.retryAfter("bob"), "one account under attack must not lock the others")
}

// A safeguard that lets a stream of invented usernames exhaust the host is
// worse than the risk it covers.
func TestThrottleMemoryIsBounded(t *testing.T) {
	throttle, _ := testThrottle(t)
	for i := 0; i < maxTrackedLogins*2; i++ {
		throttle.recordFailure("user-" + strconv.Itoa(i))
	}
	require.LessOrEqual(t, len(throttle.attempts), maxTrackedLogins)
}

// Entries left by an attack are reclaimed once they go stale, so the bound
// above is not a one-way door that stops protecting real accounts.
func TestThrottleReclaimsStaleEntriesUnderPressure(t *testing.T) {
	throttle, advance := testThrottle(t)
	for i := 0; i < maxTrackedLogins; i++ {
		throttle.recordFailure("flood-" + strconv.Itoa(i))
	}
	require.Len(t, throttle.attempts, maxTrackedLogins)

	advance(loginAttemptTTL)
	for i := 0; i < loginFreeAttempts+1; i++ {
		throttle.recordFailure("alice")
	}
	require.NotZero(t, throttle.retryAfter("alice"), "a real account is still protected after a flood")
	require.Less(t, len(throttle.attempts), maxTrackedLogins)
}

func TestTooManyLoginAttemptsSaysHowLong(t *testing.T) {
	err := ErrTooManyLoginAttempts{RetryAfter: 4 * time.Second}
	require.Contains(t, err.Error(), "4s")
	require.NotContains(t, err.Error(), "user", "it must not hint at whether the username exists")
}

// fakeUserStore holds one user, so an unknown username takes the GetUser error
// path and a known one reaches the password comparison.
type fakeUserStore struct{ user *User }

func (f *fakeUserStore) NewUser(string, string) (*User, error) { return f.user, nil }
func (f *fakeUserStore) UpdateUser(*User) error                { return nil }
func (f *fakeUserStore) GetUser(username string) (*User, error) {
	if f.user != nil && f.user.Username == username {
		return f.user, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func throttledService(t *testing.T) *Service {
	t.Helper()
	hashed, err := encryptPassword("correct horse")
	require.NoError(t, err)
	return &Service{
		config:       &Config{},
		userStore:    &fakeUserStore{user: &User{Username: "alice", EncryptedPassword: hashed}},
		sessionStore: &fakeSessionStore{},
		throttle:     newLoginThrottle(),
	}
}

// An unknown username used to come back with a different message, and without
// running bcrypt at all, so it also answered faster. Either difference tells a
// caller which usernames exist.
func TestLoginAnswersTheSameForUnknownUserAndWrongPassword(t *testing.T) {
	service := throttledService(t)

	_, _, unknownErr := service.Login("mallory", "whatever")
	_, _, wrongErr := service.Login("alice", "whatever")

	require.Error(t, unknownErr)
	require.Error(t, wrongErr)
	require.Equal(t, unknownErr.Error(), wrongErr.Error())
}

func TestLoginRefusesOnceTheAttemptsRunOut(t *testing.T) {
	service := throttledService(t)
	for i := 0; i < loginFreeAttempts+1; i++ {
		_, _, err := service.Login("alice", "wrong")
		require.Error(t, err)
	}

	_, _, err := service.Login("alice", "wrong")
	var throttled ErrTooManyLoginAttempts
	require.ErrorAs(t, err, &throttled)
	require.Positive(t, throttled.RetryAfter)

	// The correct password is refused too while the wait runs: that is the
	// point, and it is why the wait is capped and clears on success.
	_, _, err = service.Login("alice", "correct horse")
	require.ErrorAs(t, err, &throttled)
}

func TestLoginSucceedsAndClearsTheCounter(t *testing.T) {
	service := throttledService(t)
	for i := 0; i < loginFreeAttempts; i++ {
		_, _, err := service.Login("alice", "wrong")
		require.Error(t, err)
	}

	_, _, err := service.Login("alice", "correct horse")
	require.NoError(t, err)
	require.Empty(t, service.throttle.attempts)
}
