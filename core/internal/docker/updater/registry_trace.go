package updater

import (
	"crypto/tls"
	"net/http/httptrace"
	"sync"
	"time"
)

// registryStage is where a registry request had got to. A check that fails
// with a bare "context deadline exceeded" says nothing about that; the stage
// tells a resolver that loses queries (dns) from a path that does not carry
// packets (connect), a handshake that stalls (tls) and a registry that accepts
// the request and never answers (wait).
type registryStage string

const (
	stageNone    registryStage = "none"    // nothing reached yet
	stageDNS     registryStage = "dns"     // resolving the host name
	stageConnect registryStage = "connect" // opening the TCP connection
	stageTLS     registryStage = "tls"     // TLS handshake
	stageSend    registryStage = "send"    // connection ready, request being written
	stageWait    registryStage = "wait"    // request sent, waiting for the response
	stageRead    registryStage = "read"    // response started, reading it
)

// traceSpan adds up the time spent in one stage. Starts and ends are counted
// because a dual-stack dial opens two connections at once: the span runs from
// the first start until the last end.
type traceSpan struct {
	reached bool
	total   time.Duration
	open    int
	since   time.Time
}

func (s *traceSpan) start(now time.Time) {
	if s.open == 0 {
		s.since = now
	}
	s.open++
	s.reached = true
}

func (s *traceSpan) end(now time.Time) {
	if s.open == 0 {
		return
	}
	s.open--
	if s.open == 0 {
		s.total += now.Sub(s.since)
	}
}

func (s *traceSpan) endAll(now time.Time) {
	if s.open > 0 {
		s.open = 1
		s.end(now)
	}
}

func (s *traceSpan) elapsed(now time.Time) time.Duration {
	if s.open > 0 {
		return s.total + now.Sub(s.since)
	}
	return s.total
}

// registryTrace is the accumulator of one attempt of a registry check. It is
// attached to the attempt's context with httptrace, so the three requests of
// the check (manifest, token, manifest again) feed the same one, and it is
// read once, when the attempt has failed. It starts no goroutine and no timer
// and nothing is shared between checks: the callbacks only note a time and a
// stage under a mutex, which is as cheap as an HTTP trace can be. Nothing in it
// is ever a header or a credential.
type registryTrace struct {
	// now is time.Now, replaced by tests.
	now func() time.Time

	mu       sync.Mutex
	stage    registryStage
	hostPort string
	dns      traceSpan
	connect  traceSpan
	tls      traceSpan
	wait     traceSpan
}

func newRegistryTrace() *registryTrace {
	return &registryTrace{now: time.Now, stage: stageNone}
}

// clientTrace returns the hooks to hand to httptrace.WithClientTrace.
func (t *registryTrace) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn: func(hostPort string) {
			// A new request: whatever the previous one left open is over.
			t.update(func(now time.Time) {
				t.dns.endAll(now)
				t.connect.endAll(now)
				t.tls.endAll(now)
				t.wait.endAll(now)
				t.hostPort = hostPort
				t.stage = stageNone
			})
		},
		DNSStart: func(httptrace.DNSStartInfo) {
			t.update(func(now time.Time) { t.dns.start(now); t.stage = stageDNS })
		},
		DNSDone: func(httptrace.DNSDoneInfo) {
			t.update(func(now time.Time) { t.dns.end(now) })
		},
		ConnectStart: func(_, _ string) {
			t.update(func(now time.Time) { t.connect.start(now); t.stage = stageConnect })
		},
		ConnectDone: func(_, _ string, _ error) {
			t.update(func(now time.Time) { t.connect.end(now) })
		},
		TLSHandshakeStart: func() {
			t.update(func(now time.Time) { t.tls.start(now); t.stage = stageTLS })
		},
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			t.update(func(now time.Time) { t.tls.end(now) })
		},
		GotConn: func(httptrace.GotConnInfo) {
			t.update(func(time.Time) { t.stage = stageSend })
		},
		WroteRequest: func(httptrace.WroteRequestInfo) {
			t.update(func(now time.Time) { t.wait.start(now); t.stage = stageWait })
		},
		GotFirstResponseByte: func() {
			t.update(func(now time.Time) { t.wait.end(now); t.stage = stageRead })
		},
	}
}

func (t *registryTrace) update(apply func(now time.Time)) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	apply(now)
}

// stageTime is the time spent in one stage that was reached.
type stageTime struct {
	name     string
	duration time.Duration
}

// traceReport is what a failed attempt tells about its last request.
type traceReport struct {
	// stage is where the attempt was when it ended.
	stage registryStage
	// hostPort is the host the last request was addressed to: the registry, or
	// the token endpoint it sent the client to.
	hostPort string
	// stages lists, in order, the stages that were reached and how long each
	// took (the one the attempt stopped in, up to the moment of the report).
	stages []stageTime
}

func (t *registryTrace) report() traceReport {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	report := traceReport{stage: t.stage, hostPort: t.hostPort}
	for _, entry := range []struct {
		name string
		span *traceSpan
	}{{"dns", &t.dns}, {"connect", &t.connect}, {"tls", &t.tls}, {"wait", &t.wait}} {
		if entry.span.reached {
			report.stages = append(report.stages, stageTime{name: entry.name, duration: entry.span.elapsed(now)})
		}
	}
	return report
}
