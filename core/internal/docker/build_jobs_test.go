package docker

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func waitForBuildJob(t *testing.T, manager *DockerBuildJobManager, host, id string, terminal bool) DockerBuildJobView {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, found := manager.Get(host, id, 0)
		if !found {
			t.Fatal("build job disappeared")
		}
		if !terminal || job.Status == buildJobSucceeded || job.Status == buildJobFailed || job.Status == buildJobCanceled {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for build job")
	return DockerBuildJobView{}
}

func TestDockerBuildJobRunsIndependentlyAndRetainsProgress(t *testing.T) {
	release := make(chan struct{})
	manager := NewDockerBuildJobManager(func(ctx context.Context, host, filename, imageTag string, writer io.Writer) error {
		if host != "local" || filename != "compose/demo/Dockerfile" || imageTag != "demo:local" {
			t.Fatalf("unexpected build input: %q %q %q", host, filename, imageTag)
		}
		_, _ = io.WriteString(writer, "#1 loading context\n")
		select {
		case <-release:
			_, _ = io.WriteString(writer, "#2 exporting image\n")
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	started, err := manager.Start("local", "compose/demo/Dockerfile", "demo:local")
	if err != nil {
		t.Fatal(err)
	}
	running := waitForBuildJob(t, manager, "local", started.ID, false)
	if running.Status != buildJobQueued && running.Status != buildJobRunning {
		t.Fatalf("background job status = %q", running.Status)
	}
	if _, found := manager.Get("remote", started.ID, 0); found {
		t.Fatal("build job leaked across hosts")
	}
	close(release)
	completed := waitForBuildJob(t, manager, "local", started.ID, true)
	if completed.Status != buildJobSucceeded || !strings.Contains(completed.Log, "loading context") || !strings.Contains(completed.Log, "image build completed") {
		t.Fatalf("unexpected completed build: %#v", completed)
	}
	delta, found := manager.Get("local", started.ID, completed.NextOffset)
	if !found || delta.Log != "" {
		t.Fatalf("completed log offset was not resumable: %#v", delta)
	}
}

func TestDockerBuildJobCanBeCanceledWithoutDeletingProgress(t *testing.T) {
	manager := NewDockerBuildJobManager(func(ctx context.Context, _, _, _ string, writer io.Writer) error {
		_, _ = io.WriteString(writer, "waiting\n")
		<-ctx.Done()
		return ctx.Err()
	})
	job, err := manager.Start("local", "compose/demo/Dockerfile", "demo:local")
	if err != nil {
		t.Fatal(err)
	}
	if !manager.Cancel("local", job.ID) {
		t.Fatal("running build was not canceled")
	}
	completed := waitForBuildJob(t, manager, "local", job.ID, true)
	if completed.Status != buildJobCanceled || completed.CompletedAt == nil {
		t.Fatalf("unexpected canceled build: %#v", completed)
	}
}

func TestDockerBuildJobLogIsBounded(t *testing.T) {
	manager := NewDockerBuildJobManager(func(_ context.Context, _, _, _ string, writer io.Writer) error {
		_, _ = io.WriteString(writer, strings.Repeat("x", maxBuildJobLog+1024))
		return nil
	})
	job, err := manager.Start("local", "Dockerfile", "demo:local")
	if err != nil {
		t.Fatal(err)
	}
	completed := waitForBuildJob(t, manager, "local", job.ID, true)
	if len(completed.Log) > maxBuildJobLog || !completed.Truncated || completed.LogOffset == 0 {
		t.Fatalf("build log was not bounded: length=%d offset=%d truncated=%v", len(completed.Log), completed.LogOffset, completed.Truncated)
	}
}

func TestDockerBuildJobHTTPStartsListsAndCancels(t *testing.T) {
	manager := NewDockerBuildJobManager(func(ctx context.Context, _, _, _ string, _ io.Writer) error {
		<-ctx.Done()
		return ctx.Err()
	})
	handler := (&HandlerHttp{buildJobs: manager}).register()
	start := httptest.NewRecorder()
	handler.ServeHTTP(start, policyRequest(http.MethodPost, "/builds", `{"filename":"compose/demo/Dockerfile","imageTag":"demo:local"}`))
	if start.Code != http.StatusAccepted {
		t.Fatalf("start status %d: %s", start.Code, start.Body.String())
	}
	var job DockerBuildJobView
	if err := json.NewDecoder(start.Body).Decode(&job); err != nil || job.ID == "" {
		t.Fatalf("invalid start response: job=%#v err=%v", job, err)
	}
	list := httptest.NewRecorder()
	handler.ServeHTTP(list, policyRequest(http.MethodGet, "/builds", ""))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), job.ID) {
		t.Fatalf("list status %d: %s", list.Code, list.Body.String())
	}
	cancel := httptest.NewRecorder()
	handler.ServeHTTP(cancel, policyRequest(http.MethodDelete, "/builds/"+job.ID, ""))
	if cancel.Code != http.StatusAccepted {
		t.Fatalf("cancel status %d: %s", cancel.Code, cancel.Body.String())
	}
	completed := waitForBuildJob(t, manager, "local", job.ID, true)
	if completed.Status != buildJobCanceled {
		t.Fatalf("HTTP cancellation left status %q", completed.Status)
	}
}
