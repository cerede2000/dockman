package updater

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const retryTestDigest = "0123456789abcdef"

// registryTestClient reaches a local TLS server whatever host the image names,
// and trusts only that server's certificate. requestTimeout plays the part of
// the 15 s Timeout of the production client.
func registryTestClient(server *httptest.Server, requestTimeout time.Duration) *http.Client {
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = "example.com"
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	return &http.Client{Transport: transport, Timeout: requestTimeout}
}

// stall holds a request until the client gives up on it.
func stall(r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(5 * time.Second):
	}
}

func scanWith(client *http.Client, budget, retryDelay time.Duration) *Service {
	return &Service{remoteCheck: &remoteCheckSettings{
		budget: budget, retryDelay: retryDelay,
		lookup: func(ctx context.Context, image string) (string, error) {
			return registryManifestDigestWithClient(ctx, image, client)
		},
	}}
}

// The report that started this: ghcr.io answered slowly once, the whole
// check was lost, and the scan raised an error that vanished at the next scan.
// The first HEAD here stalls past the budget; the second one answers.
func TestATransientTimeoutIsReplayedOnceAndTheReplayCanSucceed(t *testing.T) {
	var heads atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if heads.Add(1) == 1 {
			stall(r)
			return
		}
		w.Header().Set("Docker-Content-Digest", "sha256:"+retryTestDigest)
	}))
	t.Cleanup(server.Close)
	// The client timeout is longer than the budget, so what fires is the
	// budget: the bare "context deadline exceeded" of the report.
	service := scanWith(registryTestClient(server, 5*time.Second), 150*time.Millisecond, 10*time.Millisecond)

	check := service.checkRemoteImage(context.Background(), "registry.test/team/app:latest")
	if check.status != ContainerUpdateCurrent || check.remoteDigest != retryTestDigest {
		t.Fatalf("the replay did not recover the check: %#v", check)
	}
	if got := heads.Load(); got != 2 {
		t.Fatalf("registry saw %d requests, want the stalled one and a single replay", got)
	}
}

// The replay is a fresh attempt, not the remains of the first one: a registry
// that keeps stalling is asked twice, never more, and the error says transient.
func TestAPersistentTransientFailureIsReplayedExactlyOnce(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int // 0 stalls instead of answering
		reason string
	}{
		{"stalling registry", 0, "registry check: Head"},
		{"503", http.StatusServiceUnavailable, "registry check: registry manifest request returned HTTP 503"},
		{"429", http.StatusTooManyRequests, "registry check: public registry rate limit reached"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var heads atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				heads.Add(1)
				if test.status == 0 {
					stall(r)
					return
				}
				w.WriteHeader(test.status)
			}))
			t.Cleanup(server.Close)
			service := scanWith(registryTestClient(server, 5*time.Second), 100*time.Millisecond, 5*time.Millisecond)

			check := service.checkRemoteImage(context.Background(), "registry.test/team/app:latest")
			if check.status != ContainerUpdateError || check.errorClass != RegistryErrorTransient {
				t.Fatalf("want a transient error, got %#v", check)
			}
			if len(check.reason) < len(test.reason) || check.reason[:len(test.reason)] != test.reason {
				t.Fatalf("reason = %q, want it to start with %q", check.reason, test.reason)
			}
			if got := heads.Load(); got != 2 {
				t.Fatalf("registry saw %d requests, want exactly 2", got)
			}
		})
	}
}

// Replaying a refusal would only delay the message that tells the operator to
// act, and would hammer a registry that already said no.
func TestAPermanentFailureIsNeverReplayed(t *testing.T) {
	SetRegistryCredentials(func(string) (string, string, bool) { return "robot", "t0ken", true })
	t.Cleanup(func() { SetRegistryCredentials(nil) })
	status := func(code int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
	}
	for _, test := range []struct {
		name     string
		handler  http.HandlerFunc
		want     RegistryErrorClass
		requests int32
	}{
		{"403", status(http.StatusForbidden), RegistryErrorAuthentication, 1},
		{"401 without a challenge", status(http.StatusUnauthorized), RegistryErrorOther, 1},
		{"404", status(http.StatusNotFound), RegistryErrorNotFound, 1},
		{"400", status(http.StatusBadRequest), RegistryErrorOther, 1},
		// A real refusal of the stored login is two requests (the challenge, then
		// the login being turned down), both part of the one attempt.
		{"stored credential turned down", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "" {
				w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusForbidden)
		}, RegistryErrorAuthentication, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				test.handler(w, r)
			}))
			t.Cleanup(server.Close)
			service := scanWith(registryTestClient(server, 5*time.Second), time.Second, 5*time.Millisecond)

			check := service.checkRemoteImage(context.Background(), "registry.test/team/app:latest")
			if check.status != ContainerUpdateError || check.errorClass != test.want {
				t.Fatalf("want a %q error, got %#v", test.want, check)
			}
			if got := requests.Load(); got != test.requests {
				t.Fatalf("registry saw %d requests, want %d: a permanent failure must not be replayed", got, test.requests)
			}
		})
	}
}

// A scan that is being stopped must not sleep through the replay delay, nor
// start a request afterwards.
func TestTheReplayWaitGivesWayToCancellation(t *testing.T) {
	var lookups atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	service := &Service{remoteCheck: &remoteCheckSettings{
		budget: time.Second, retryDelay: 3 * time.Second,
		lookup: func(context.Context, string) (string, error) {
			lookups.Add(1)
			time.AfterFunc(50*time.Millisecond, cancel)
			return "", classified(errors.New("slow registry"), errTransientRegistry)
		},
	}}

	started := time.Now()
	check := service.checkRemoteImage(ctx, "registry.test/team/app:latest")
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("the replay wait ignored the cancellation for %s", elapsed)
	}
	if check.status != ContainerUpdateError || lookups.Load() != 1 {
		t.Fatalf("a cancelled scan replayed: lookups=%d check=%#v", lookups.Load(), check)
	}
}

// Already cancelled before the failure comes back: no wait at all.
func TestACancelledScanDoesNotReplay(t *testing.T) {
	var lookups atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{remoteCheck: &remoteCheckSettings{
		budget: time.Second, retryDelay: time.Millisecond,
		lookup: func(context.Context, string) (string, error) {
			lookups.Add(1)
			cancel()
			return "", classified(errors.New("slow registry"), errTransientRegistry)
		},
	}}
	service.checkRemoteImage(ctx, "registry.test/team/app:latest")
	if lookups.Load() != 1 {
		t.Fatalf("lookups = %d, want 1", lookups.Load())
	}
}

// Three requests in series, each one alive and well inside its own timeout:
// the old single 15 s context refused this as soon as their sum went past
// it. Durations are scaled down: each request is allowed T, takes T/2, and
// the budget is 3T, while one request's worth of budget (T) is not enough for
// the three of them (1.5 T).
func TestThreeSlowButLiveRequestsFitInTheCheckBudget(t *testing.T) {
	const requestTimeout = 300 * time.Millisecond
	const requestDuration = requestTimeout / 2
	const realm = "https://registry.test/token"
	var heads, tokens atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(requestDuration)
		switch {
		case r.URL.Path == "/token":
			tokens.Add(1)
			_, _ = w.Write([]byte(`{"token":"granted"}`))
		case r.Header.Get("Authorization") == "Bearer granted":
			heads.Add(1)
			w.Header().Set("Docker-Content-Digest", "sha256:"+retryTestDigest)
		default:
			heads.Add(1)
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm+`",service="registry.test"`)
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(server.Close)
	service := scanWith(registryTestClient(server, requestTimeout), registryRequestsPerCheck*requestTimeout, 5*time.Millisecond)

	started := time.Now()
	check := service.checkRemoteImage(context.Background(), "registry.test/team/app:latest")
	if check.status != ContainerUpdateCurrent || check.remoteDigest != retryTestDigest {
		t.Fatalf("three slow requests did not fit the budget: %#v", check)
	}
	if elapsed := time.Since(started); elapsed <= requestTimeout {
		t.Fatalf("the check took %s, the test no longer exercises a sum above one request's budget", elapsed)
	}
	if heads.Load() != 2 || tokens.Load() != 1 {
		t.Fatalf("requests: %d manifest, %d token; want 2 and 1, with no replay", heads.Load(), tokens.Load())
	}
}

// The production numbers: the budget covers the three requests of a check at
// the timeout of each, and the replay waits for a couple of seconds.
func TestTheProductionBudgetCoversThreeRequestsAtTheirOwnTimeout(t *testing.T) {
	settings := (&Service{}).remoteCheckSettings()
	if settings.budget < 3*registryRequestTimeout {
		t.Errorf("budget %s is below three requests of %s", settings.budget, registryRequestTimeout)
	}
	if settings.retryDelay != 2*time.Second {
		t.Errorf("retry delay = %s, want 2s", settings.retryDelay)
	}
	if settings.lookup == nil {
		t.Error("the production lookup is missing")
	}
}
