package updater

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// traceClock stands in for time.Now so that a "15 second stall" takes no time.
type traceClock struct{ now time.Time }

func (c *traceClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestTrace() (*registryTrace, *httptrace.ClientTrace, *traceClock) {
	clock := &traceClock{now: time.Unix(1_700_000_000, 0)}
	trace := newRegistryTrace()
	trace.now = func() time.Time { return clock.now }
	return trace, trace.clientTrace(), clock
}

// A request that was given up on reaches the log as "context deadline
// exceeded", whatever it was waiting for. The accumulator is what says what: it
// is fed the events the HTTP transport emits, simulated here with a clock that
// moves only when the test says so, and it must name the stage the request was
// in and how long each stage it went through took.
func TestTheTraceNamesTheStageARequestStalledIn(t *testing.T) {
	ms := time.Millisecond
	for _, test := range []struct {
		name   string
		events func(clock *traceClock, ct *httptrace.ClientTrace)
		stage  registryStage
		stages []stageTime
	}{
		{
			"nothing happened yet",
			func(*traceClock, *httptrace.ClientTrace) {},
			stageNone, nil,
		},
		{
			"the resolver never answers",
			func(clock *traceClock, ct *httptrace.ClientTrace) {
				ct.GetConn("ghcr.io:443")
				ct.DNSStart(httptrace.DNSStartInfo{Host: "ghcr.io"})
				clock.advance(5000 * ms)
			},
			stageDNS, []stageTime{{"dns", 5000 * ms}},
		},
		{
			"the connection is never established",
			func(clock *traceClock, ct *httptrace.ClientTrace) {
				ct.GetConn("ghcr.io:443")
				ct.DNSStart(httptrace.DNSStartInfo{Host: "ghcr.io"})
				clock.advance(10 * ms)
				ct.DNSDone(httptrace.DNSDoneInfo{})
				ct.ConnectStart("tcp", "140.82.121.34:443")
				clock.advance(15000 * ms)
			},
			stageConnect, []stageTime{{"dns", 10 * ms}, {"connect", 15000 * ms}},
		},
		{
			"the connection is refused",
			func(clock *traceClock, ct *httptrace.ClientTrace) {
				ct.GetConn("ghcr.io:443")
				ct.ConnectStart("tcp", "127.0.0.1:443")
				clock.advance(1 * ms)
				ct.ConnectDone("tcp", "127.0.0.1:443", errors.New("connection refused"))
			},
			stageConnect, []stageTime{{"connect", 1 * ms}},
		},
		{
			"the handshake stalls",
			func(clock *traceClock, ct *httptrace.ClientTrace) {
				ct.GetConn("ghcr.io:443")
				ct.DNSStart(httptrace.DNSStartInfo{Host: "ghcr.io"})
				clock.advance(10 * ms)
				ct.DNSDone(httptrace.DNSDoneInfo{})
				ct.ConnectStart("tcp", "140.82.121.34:443")
				clock.advance(20 * ms)
				ct.ConnectDone("tcp", "140.82.121.34:443", nil)
				ct.TLSHandshakeStart()
				clock.advance(15000 * ms)
			},
			stageTLS, []stageTime{{"dns", 10 * ms}, {"connect", 20 * ms}, {"tls", 15000 * ms}},
		},
		{
			"the registry accepts the request and never answers",
			func(clock *traceClock, ct *httptrace.ClientTrace) {
				ct.GetConn("ghcr.io:443")
				ct.DNSStart(httptrace.DNSStartInfo{Host: "ghcr.io"})
				clock.advance(10 * ms)
				ct.DNSDone(httptrace.DNSDoneInfo{})
				ct.ConnectStart("tcp", "140.82.121.34:443")
				clock.advance(20 * ms)
				ct.ConnectDone("tcp", "140.82.121.34:443", nil)
				ct.TLSHandshakeStart()
				clock.advance(30 * ms)
				ct.TLSHandshakeDone(tls.ConnectionState{}, nil)
				ct.GotConn(httptrace.GotConnInfo{})
				ct.WroteRequest(httptrace.WroteRequestInfo{})
				clock.advance(15000 * ms)
			},
			stageWait, []stageTime{{"dns", 10 * ms}, {"connect", 20 * ms}, {"tls", 30 * ms}, {"wait", 15000 * ms}},
		},
		{
			"a reused connection has no dns, connect or tls to account for",
			func(clock *traceClock, ct *httptrace.ClientTrace) {
				ct.GetConn("ghcr.io:443")
				ct.GotConn(httptrace.GotConnInfo{Reused: true})
				ct.WroteRequest(httptrace.WroteRequestInfo{})
				clock.advance(7 * time.Second)
			},
			stageWait, []stageTime{{"wait", 7 * time.Second}},
		},
		{
			"the response starts and then stalls",
			func(clock *traceClock, ct *httptrace.ClientTrace) {
				ct.GetConn("ghcr.io:443")
				ct.GotConn(httptrace.GotConnInfo{Reused: true})
				ct.WroteRequest(httptrace.WroteRequestInfo{})
				clock.advance(120 * ms)
				ct.GotFirstResponseByte()
				clock.advance(15000 * ms)
			},
			stageRead, []stageTime{{"wait", 120 * ms}},
		},
		{
			"the request is still being written",
			func(clock *traceClock, ct *httptrace.ClientTrace) {
				ct.GetConn("ghcr.io:443")
				ct.GotConn(httptrace.GotConnInfo{Reused: true})
				clock.advance(3 * time.Second)
			},
			stageSend, nil,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			trace, ct, clock := newTestTrace()
			test.events(clock, ct)
			report := trace.report()
			if report.stage != test.stage {
				t.Errorf("stopped at %q, want %q", report.stage, test.stage)
			}
			if !slices.Equal(report.stages, test.stages) {
				t.Errorf("stages = %v, want %v", report.stages, test.stages)
			}
		})
	}
}

// The three requests of a check (manifest, token, manifest again) feed one
// accumulator: the time adds up, and the stage and host are those of the last
// request, which is the one that stalled.
func TestTheTraceAddsUpTheRequestsOfACheckAndNamesTheLastOne(t *testing.T) {
	trace, ct, clock := newTestTrace()
	ms := time.Millisecond

	// Manifest request, answered 401.
	ct.GetConn("ghcr.io:443")
	ct.DNSStart(httptrace.DNSStartInfo{})
	clock.advance(10 * ms)
	ct.DNSDone(httptrace.DNSDoneInfo{})
	ct.ConnectStart("tcp", "a")
	clock.advance(20 * ms)
	ct.ConnectDone("tcp", "a", nil)
	ct.TLSHandshakeStart()
	clock.advance(30 * ms)
	ct.TLSHandshakeDone(tls.ConnectionState{}, nil)
	ct.GotConn(httptrace.GotConnInfo{})
	ct.WroteRequest(httptrace.WroteRequestInfo{})
	clock.advance(100 * ms)
	ct.GotFirstResponseByte()

	// Token request, to the endpoint the 401 pointed at; it stalls in dns.
	ct.GetConn("auth.example:443")
	ct.DNSStart(httptrace.DNSStartInfo{})
	clock.advance(5000 * ms)

	report := trace.report()
	if report.stage != stageDNS || report.hostPort != "auth.example:443" {
		t.Errorf("stopped at %q on %q, want dns on auth.example:443", report.stage, report.hostPort)
	}
	want := []stageTime{{"dns", 5010 * ms}, {"connect", 20 * ms}, {"tls", 30 * ms}, {"wait", 100 * ms}}
	if !slices.Equal(report.stages, want) {
		t.Errorf("stages = %v, want %v", report.stages, want)
	}
}

// A dual-stack dial opens two connections at once. The stage ran from the first
// start to the last end, not twice over.
func TestTheTraceCountsParallelDialsOnce(t *testing.T) {
	trace, ct, clock := newTestTrace()
	ct.GetConn("ghcr.io:443")
	ct.ConnectStart("tcp", "[2606::1]:443")
	clock.advance(10 * time.Millisecond)
	ct.ConnectStart("tcp", "140.82.121.34:443")
	clock.advance(20 * time.Millisecond)
	ct.ConnectDone("tcp", "140.82.121.34:443", nil)
	clock.advance(5 * time.Millisecond)
	ct.ConnectDone("tcp", "[2606::1]:443", errors.New("operation was canceled"))
	clock.advance(time.Hour)

	if got := trace.report().stages; !slices.Equal(got, []stageTime{{"connect", 35 * time.Millisecond}}) {
		t.Fatalf("stages = %v, want one connect of 35ms", got)
	}
}

// What a request that failed left open belongs to that request: the next one
// starts from nothing, and the time of the one before stops counting.
func TestTheTraceClosesWhatTheLastRequestLeftOpen(t *testing.T) {
	trace, ct, clock := newTestTrace()
	ct.GetConn("ghcr.io:443")
	ct.GotConn(httptrace.GotConnInfo{})
	ct.WroteRequest(httptrace.WroteRequestInfo{})
	clock.advance(2 * time.Second) // never answered
	ct.GetConn("ghcr.io:443")
	clock.advance(10 * time.Second)

	report := trace.report()
	if report.stage != stageNone {
		t.Errorf("stage = %q, want none for a request that has not begun", report.stage)
	}
	if want := []stageTime{{"wait", 2 * time.Second}}; !slices.Equal(report.stages, want) {
		t.Errorf("stages = %v, want %v", report.stages, want)
	}
}

// capturedLogger returns a logger writing JSON lines to a buffer.
func capturedLogger() (*zerolog.Logger, *bytes.Buffer) {
	var buffer bytes.Buffer
	logger := zerolog.New(&buffer)
	return &logger, &buffer
}

func logLines(t *testing.T, buffer *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", raw, err)
		}
		lines = append(lines, line)
	}
	return lines
}

// A server that accepts the connection, completes TLS and reads the request,
// and then says nothing: the case the user's check keeps hitting, and the one
// "context deadline exceeded" cannot tell from a lost DNS query.
func TestACheckThatStallsWaitingForTheResponseSaysSo(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { stall(r) }))
	t.Cleanup(server.Close)
	service := scanWith(registryTestClient(server, 5*time.Second), 450*time.Millisecond, 10*time.Millisecond)
	logger, buffer := capturedLogger()
	service.remoteCheck.logger = logger

	check := service.checkRemoteImage(context.Background(), "registry.test/team/app:latest", replayTransient)
	if check.status != ContainerUpdateError || check.errorClass != RegistryErrorTransient {
		t.Fatalf("want a transient error, got %#v", check)
	}
	lines := logLines(t, buffer)
	if len(lines) != 2 {
		t.Fatalf("%d log lines, want one at the replay and one at the final failure: %s", len(lines), buffer.String())
	}
	t.Logf("log line of the first attempt: %s", strings.Split(strings.TrimSpace(buffer.String()), "\n")[0])
	for index, line := range lines {
		if line["level"] != "warn" || line["registry"] != "registry.test" || line["stopped_at"] != "wait" {
			t.Errorf("line %d = %v, want a warn for registry.test stopped at wait", index, line)
		}
		if line["attempt"] != float64(index+1) {
			t.Errorf("line %d attempt = %v, want %d", index, line["attempt"], index+1)
		}
		for _, field := range []string{"elapsed_ms", "attempt_ms", "connect_ms", "tls_ms", "wait_ms", "request_host", "error"} {
			if _, present := line[field]; !present {
				t.Errorf("line %d lacks %q: %v", index, field, line)
			}
		}
		if _, present := line["dns_ms"]; present {
			t.Errorf("line %d reports a dns stage the connection never went through: %v", index, line)
		}
	}
	if lines[0]["message"] != "registry check failed transiently, replaying once" || lines[1]["message"] != "registry check failed transiently after its replay" {
		t.Errorf("messages = %q, %q", lines[0]["message"], lines[1]["message"])
	}
	// The total keeps growing across the replay; each attempt is bounded by the budget.
	if lines[1]["elapsed_ms"].(float64) <= lines[0]["elapsed_ms"].(float64) {
		t.Errorf("elapsed did not grow across the replay: %v then %v", lines[0]["elapsed_ms"], lines[1]["elapsed_ms"])
	}
}

// An address that does not take the connection: the stage is connect.
func TestACheckThatCannotConnectSaysSo(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddress := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, closedAddress)
		},
	}}
	service := scanWith(client, 3*time.Second, 5*time.Millisecond)
	logger, buffer := capturedLogger()
	service.remoteCheck.logger = logger

	check := service.checkRemoteImage(context.Background(), "registry.test/team/app:latest", replayTransient)
	if check.errorClass != RegistryErrorTransient {
		t.Fatalf("want a transient error, got %#v", check)
	}
	lines := logLines(t, buffer)
	if len(lines) != 2 {
		t.Fatalf("%d log lines, want 2: %s", len(lines), buffer.String())
	}
	t.Logf("log line of a refused connection: %s", strings.Split(strings.TrimSpace(buffer.String()), "\n")[0])
	for index, line := range lines {
		if line["stopped_at"] != "connect" {
			t.Errorf("line %d stopped at %v, want connect: %v", index, line["stopped_at"], line)
		}
		if _, present := line["connect_ms"]; !present {
			t.Errorf("line %d lacks connect_ms: %v", index, line)
		}
		for _, field := range []string{"tls_ms", "wait_ms"} {
			if _, present := line[field]; present {
				t.Errorf("line %d reports %s for a connection that never opened: %v", index, field, line)
			}
		}
	}
}

// Logging is for the operator who has to understand a failure. A check that
// works says nothing, even when it needed its replay to get there; a failure
// that is permanent already has its message; a scan being stopped is not a
// registry problem.
func TestOnlyATransientFailureOfARegistryCheckIsLogged(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Docker-Content-Digest", "sha256:"+retryTestDigest)
		}))
		t.Cleanup(server.Close)
		service := scanWith(registryTestClient(server, 5*time.Second), time.Second, 5*time.Millisecond)
		logger, buffer := capturedLogger()
		service.remoteCheck.logger = logger

		if check := service.checkRemoteImage(context.Background(), "registry.test/team/app:latest", replayTransient); check.status != ContainerUpdateCurrent {
			t.Fatalf("check = %#v", check)
		}
		if buffer.Len() != 0 {
			t.Fatalf("a successful check logged: %s", buffer.String())
		}
	})
	t.Run("success after a replay logs only the failed attempt", func(t *testing.T) {
		var heads atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if heads.Add(1) == 1 {
				stall(r)
				return
			}
			w.Header().Set("Docker-Content-Digest", "sha256:"+retryTestDigest)
		}))
		t.Cleanup(server.Close)
		service := scanWith(registryTestClient(server, 5*time.Second), 450*time.Millisecond, 10*time.Millisecond)
		logger, buffer := capturedLogger()
		service.remoteCheck.logger = logger

		if check := service.checkRemoteImage(context.Background(), "registry.test/team/app:latest", replayTransient); check.status != ContainerUpdateCurrent {
			t.Fatalf("check = %#v", check)
		}
		lines := logLines(t, buffer)
		if len(lines) != 1 || lines[0]["attempt"] != float64(1) {
			t.Fatalf("want the line of attempt 1 only, got %s", buffer.String())
		}
	})
	t.Run("permanent failure", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
		t.Cleanup(server.Close)
		service := scanWith(registryTestClient(server, 5*time.Second), time.Second, 5*time.Millisecond)
		logger, buffer := capturedLogger()
		service.remoteCheck.logger = logger

		if check := service.checkRemoteImage(context.Background(), "registry.test/team/app:latest", replayTransient); check.errorClass != RegistryErrorAuthentication {
			t.Fatalf("check = %#v", check)
		}
		if buffer.Len() != 0 {
			t.Fatalf("a permanent failure logged: %s", buffer.String())
		}
	})
	t.Run("cancelled scan", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		logger, buffer := capturedLogger()
		service := &Service{remoteCheck: &remoteCheckSettings{
			budget: time.Second, retryDelay: time.Millisecond, logger: logger,
			lookup: func(context.Context, string) (string, error) {
				cancel()
				return "", classified(errors.New("slow registry"), errTransientRegistry)
			},
		}}
		service.checkRemoteImage(ctx, "registry.test/team/app:latest", replayTransient)
		if buffer.Len() != 0 {
			t.Fatalf("a cancelled scan logged: %s", buffer.String())
		}
	})
}

// The line carries the error and names, never what a request carried.
func TestTheLogLineNeverCarriesAHeaderOrACredential(t *testing.T) {
	SetRegistryCredentials(func(string) (string, string, bool) { return "robot", "t0ken-secret", true })
	t.Cleanup(func() { SetRegistryCredentials(nil) })
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		stall(r)
	}))
	t.Cleanup(server.Close)
	service := scanWith(registryTestClient(server, 5*time.Second), 450*time.Millisecond, 10*time.Millisecond)
	logger, buffer := capturedLogger()
	service.remoteCheck.logger = logger

	service.checkRemoteImage(context.Background(), "registry.test/team/app:latest", replayTransient)
	if buffer.Len() == 0 {
		t.Fatal("expected the transient failure to be logged")
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("robot:t0ken-secret"))
	for _, secret := range []string{"t0ken-secret", "robot", "Authorization", "Basic ", encoded} {
		if strings.Contains(buffer.String(), secret) {
			t.Fatalf("the log line contains %q: %s", secret, buffer.String())
		}
	}
}

// The callbacks of an HTTP trace are called by the transport from the
// goroutines that dial, handshake and write, which are not the one that reads
// the report. Two callbacks must therefore never be inside the accumulator at
// the same time. The first callback is held inside the clock, which is read
// under the lock, while a second one is started: if the lock works, the second
// cannot finish until the first lets go; if it does not, it finishes at once.
func TestTheTraceCallbacksNeverRunAtTheSameTime(t *testing.T) {
	trace := newRegistryTrace()
	ct := trace.clientTrace()
	insideFirst := make(chan struct{})
	secondFinished := make(chan struct{})
	var calls atomic.Int32
	var overlapped atomic.Bool
	trace.now = func() time.Time {
		if calls.Add(1) == 1 {
			close(insideFirst)
			select {
			case <-secondFinished:
				overlapped.Store(true)
			case <-time.After(100 * time.Millisecond):
			}
		}
		return time.Unix(1_700_000_000, 0)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ct.DNSStart(httptrace.DNSStartInfo{})
	}()
	go func() {
		defer wg.Done()
		<-insideFirst
		ct.ConnectStart("tcp", "ghcr.io:443")
		close(secondFinished)
	}()
	wg.Wait()
	if overlapped.Load() {
		t.Fatal("a callback ran to completion while another one was inside the accumulator")
	}
}

// And what many goroutines add up must be exact: every dial left open is
// counted once, and every balanced start and end cancels out.
func TestTheTraceCountsExactlyWhenCalledFromManyGoroutines(t *testing.T) {
	trace := newRegistryTrace()
	ct := trace.clientTrace()
	const goroutines, rounds = 16, 3000
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				ct.ConnectStart("tcp", "ghcr.io:443")
				ct.DNSStart(httptrace.DNSStartInfo{})
				ct.DNSDone(httptrace.DNSDoneInfo{})
			}
		}()
	}
	wg.Wait()

	trace.mu.Lock()
	defer trace.mu.Unlock()
	if trace.connect.open != goroutines*rounds {
		t.Errorf("%d dials counted open, want exactly %d", trace.connect.open, goroutines*rounds)
	}
	if trace.dns.open != 0 {
		t.Errorf("%d lookups counted open, want none: every start had its end", trace.dns.open)
	}
}
