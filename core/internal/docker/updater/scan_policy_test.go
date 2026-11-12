package updater

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
)

// listingDaemon is a daemon that knows one image and lists the containers it
// is given, enough to drive a scan or Dockman's own update check.
type listingDaemon struct {
	*imageDaemon
	items []container.Summary
}

func (d *listingDaemon) ContainerList(context.Context, client.ContainerListOptions) (client.ContainerListResult, error) {
	return client.ContainerListResult{Items: d.items}, nil
}

const policyTestImage = "registry.test/team/app:latest"

func policyTestDaemon() (*listingDaemon, container.Summary) {
	row := container.Summary{ID: "c1", Names: []string{"/app"}, Image: policyTestImage, ImageID: "sha256:local"}
	return &listingDaemon{
		imageDaemon: &imageDaemon{fakeDockerClient: newFakeDockerClient(), images: map[string]image.InspectResponse{
			"sha256:local": {ID: "sha256:local", RepoDigests: []string{"registry.test/team/app@sha256:aaa"}},
		}},
		items: []container.Summary{row},
	}, row
}

// A registry that answers 503 to every request, counting them.
func failingRegistry(t *testing.T, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	return server
}

// Dockman's own update check answers a request somebody is waiting on. A
// replay would make that person wait twice, and for much longer: so a
// transient failure there is one failed request, with no pause before the
// answer.
func TestTheInteractiveUpdateCheckIsNeverReplayed(t *testing.T) {
	var requests atomic.Int32
	server := failingRegistry(t, &requests)
	daemon, row := policyTestDaemon()
	service := scanWith(registryTestClient(server, 5*time.Second), time.Second, 3*time.Second)
	service.client = daemon
	logger, buffer := capturedLogger()
	service.remoteCheck.logger = logger

	started := time.Now()
	result := service.CheckContainerImageUpdate(context.Background(), row)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("the check waited %s: it paused for a replay it must not make", elapsed)
	}
	if result.Status != ContainerUpdateError || result.ErrorClass != RegistryErrorTransient {
		t.Fatalf("want a transient error, got %#v", result)
	}
	if !strings.Contains(result.Reason, "registry manifest request returned HTTP 503") {
		t.Fatalf("reason = %q", result.Reason)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("registry saw %d requests, want the single failed one", got)
	}
	lines := logLines(t, buffer)
	if len(lines) != 1 || lines[0]["message"] != "registry check failed transiently, not replayed" {
		t.Fatalf("want one line saying the failure was not replayed, got %s", buffer.String())
	}
}

// A scan has nobody waiting on one image: the same failure is replayed once.
func TestAScanReplaysATransientFailureOfAnImage(t *testing.T) {
	var requests atomic.Int32
	server := failingRegistry(t, &requests)
	daemon, row := policyTestDaemon()
	service := scanWith(registryTestClient(server, 5*time.Second), time.Second, 5*time.Millisecond)
	service.client = daemon

	results, err := service.CheckContainerUpdatesByID(context.Background(), []string{row.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Status != ContainerUpdateError || results[0].ErrorClass != RegistryErrorTransient {
		t.Fatalf("want one transient error, got %#v", results)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("registry saw %d requests, want the failed one and its replay", got)
	}
}

// The case the user's registry actually is: it accepts the connection and never
// answers, and the 15 s Client.Timeout of the HTTP client fires before the 45 s
// budget of the attempt. The error then reads "Client.Timeout exceeded", not a
// bare "context deadline exceeded", and is just as transient. Durations are
// scaled down: the client gives up after 300 ms, the budget is 5 s.
func TestAMuteRegistryBehindAShortClientTimeoutIsReplayedOnce(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		stall(w)
	}))
	t.Cleanup(server.Close)
	service := scanWith(registryTestClient(server, 300*time.Millisecond), 5*time.Second, 10*time.Millisecond)
	logger, buffer := capturedLogger()
	service.remoteCheck.logger = logger

	check := service.checkRemoteImage(context.Background(), policyTestImage, replayTransient)
	if check.status != ContainerUpdateError || check.errorClass != RegistryErrorTransient {
		t.Fatalf("want a transient error, got %#v", check)
	}
	if !strings.Contains(check.reason, "Client.Timeout exceeded") {
		t.Fatalf("the client timeout should be what fired, got %q", check.reason)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("registry saw %d requests, want 2", got)
	}
	lines := logLines(t, buffer)
	if len(lines) != 2 {
		t.Fatalf("%d log lines, want one per attempt: %s", len(lines), buffer.String())
	}
	for index, line := range lines {
		if line["stopped_at"] != "wait" || line["attempt"] != float64(index+1) {
			t.Errorf("line %d = %v, want attempt %d stopped at wait", index, line, index+1)
		}
		if attempt := line["attempt_ms"].(float64); attempt > 3000 {
			t.Errorf("line %d: the attempt lasted %.0f ms, the client timeout should have ended it near 300", index, attempt)
		}
	}
}
