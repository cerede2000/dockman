package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeSessionStore is a minimal SessionStore that returns a fixed session,
// letting us exercise the middleware without a database.
type fakeSessionStore struct {
	session Session
	err     error
}

func (f *fakeSessionStore) NewSession(*Session) error                 { return nil }
func (f *fakeSessionStore) DeleteSession(uint) error                  { return nil }
func (f *fakeSessionStore) GetSession(uint) (Session, error)          { return f.session, f.err }
func (f *fakeSessionStore) GetSessionByToken(string) (Session, error) { return f.session, f.err }
func (f *fakeSessionStore) CleanupExpiredSessions() error             { return nil }

// TestMiddleware_PropagatesUserToContext guards against a regression where
// CheckAuth discarded the *http.Request returned by WithContext, so the
// authenticated user never reached downstream handlers.
func TestMiddleware_PropagatesUserToContext(t *testing.T) {
	want := User{Username: "alice"}
	srv := &Service{
		config: &Config{},
		sessionStore: &fakeSessionStore{
			session: Session{
				User:    want,
				Expires: time.Now().Add(time.Hour),
			},
		},
	}

	var got *User
	downstream := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if u, ok := r.Context().Value(KeyUserCtx).(*User); ok {
			got = u
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/api/protected/info", nil)
	req.AddCookie(&http.Cookie{Name: CookieHeaderAuth, Value: "any-token"})
	rec := httptest.NewRecorder()

	Middleware(srv, downstream).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, got, "authenticated user must reach the downstream handler via context")
	require.Equal(t, want.Username, got.Username)
}

// TestMiddleware_RejectsMissingCookie ensures unauthenticated requests are
// stopped with 401 and never reach the downstream handler.
func TestMiddleware_RejectsMissingCookie(t *testing.T) {
	srv := &Service{config: &Config{}, sessionStore: &fakeSessionStore{}}

	called := false
	downstream := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })

	req := httptest.NewRequest(http.MethodGet, "/api/protected/info", nil)
	rec := httptest.NewRecorder()

	Middleware(srv, downstream).ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.False(t, called, "downstream must not run for unauthenticated requests")
}

// The Secure attribute was commented out, so a Dockman serving HTTPS issued a
// session token the browser would send back in clear over any plain HTTP
// request to the same host. Setting it unconditionally was not an option
// either: a LAN deployment over HTTP would hand out a cookie nobody's browser
// returns, and nobody could log in.
func TestSessionCookiesCarrySecureExactlyWhenTheSessionIsOverTLS(t *testing.T) {
	expiry := time.Now().Add(time.Hour)

	// Dockman terminates TLS itself: always Secure.
	for _, cookie := range createAuthCookies("token", 1, expiry, requestIsHTTPS(true, http.Header{})) {
		if !cookie.Secure {
			t.Fatalf("%s must be Secure when Dockman serves TLS", cookie.Name)
		}
		if !cookie.HttpOnly {
			t.Fatalf("%s must stay HttpOnly", cookie.Name)
		}
	}

	// Plain HTTP on a LAN: no Secure, or the cookie is never sent back.
	for _, cookie := range createAuthCookies("token", 1, expiry, requestIsHTTPS(false, http.Header{})) {
		if cookie.Secure {
			t.Fatalf("%s must not be Secure on a plain HTTP deployment", cookie.Name)
		}
	}

	// Behind a proxy that terminated TLS.
	proxied := http.Header{"X-Forwarded-Proto": []string{"https"}}
	for _, cookie := range createAuthCookies("token", 1, expiry, requestIsHTTPS(false, proxied)) {
		if !cookie.Secure {
			t.Fatalf("%s must be Secure behind a TLS-terminating proxy", cookie.Name)
		}
	}
}

func TestForwardedProtoIsReadTheWayProxiesWriteIt(t *testing.T) {
	cases := map[string]bool{
		"https":       true,
		"HTTPS":       true,
		" https ":     true,
		"https, http": true, // first hop wins, as proxies chain it
		"http":        false,
		"http, https": false,
		"":            false,
		"httpsx":      false,
	}
	for value, want := range cases {
		got := requestIsHTTPS(false, http.Header{"X-Forwarded-Proto": []string{value}})
		if got != want {
			t.Fatalf("X-Forwarded-Proto %q: got %v, want %v", value, got, want)
		}
	}
}

// verifyCookie logged the failure at Error and then replaced it with a fixed
// string. The level made an expired cookie look like a malfunction, and the
// flattening left CheckAuth's deliberate Debug line with nothing to report.
// The reason has to reach the caller instead.
func TestVerifyCookieCarriesTheReasonToTheCaller(t *testing.T) {
	srv := &Service{
		config:       &Config{},
		sessionStore: &fakeSessionStore{err: errors.New("session not found")},
	}

	_, err := verifyCookie([]*http.Cookie{{Name: CookieHeaderAuth, Value: "whatever"}}, srv)
	require.ErrorContains(t, err, "session not found")
}
