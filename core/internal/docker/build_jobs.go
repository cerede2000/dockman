package docker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	buildJobQueued    = "queued"
	buildJobRunning   = "running"
	buildJobSucceeded = "succeeded"
	buildJobFailed    = "failed"
	buildJobCanceled  = "canceled"

	// Build output can be extremely verbose (notably apt and compiler jobs).
	// Keep a useful one-megabyte tail without letting completed jobs retain
	// hundreds of megabytes in Dockman's heap until the next day.
	maxBuildJobLog       = 1 << 20
	maxRetainedBuildJobs = 20
	maxPendingBuildJobs  = 10
	buildJobRetention    = 6 * time.Hour
	buildJobTimeout      = 6 * time.Hour
)

type buildJobExecutor func(context.Context, string, string, string, string, io.Writer) error

type DockerBuildJobView struct {
	ID           string     `json:"id"`
	Host         string     `json:"-"`
	Filename     string     `json:"filename"`
	ImageTag     string     `json:"imageTag"`
	NetworkMode  string     `json:"networkMode"`
	Status       string     `json:"status"`
	Error        string     `json:"error,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	CompletedAt  *time.Time `json:"completedAt,omitempty"`
	LastOutputAt *time.Time `json:"lastOutputAt,omitempty"`
	Log          string     `json:"log,omitempty"`
	LogOffset    int64      `json:"logOffset,omitempty"`
	NextOffset   int64      `json:"nextOffset,omitempty"`
	Truncated    bool       `json:"truncated,omitempty"`
}

type dockerBuildJob struct {
	mu       sync.Mutex
	view     DockerBuildJobView
	log      []byte
	logBase  int64
	logTotal int64
	cancel   context.CancelFunc
}

func (j *dockerBuildJob) Write(payload []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	original := len(payload)
	j.logTotal += int64(original)
	now := time.Now()
	j.view.LastOutputAt = &now
	if original >= maxBuildJobLog {
		// Avoid first allocating the complete payload when a command flushes a
		// very large chunk in one write; only its bounded tail is useful.
		j.log = append(j.log[:0], payload[original-maxBuildJobLog:]...)
		j.logBase = j.logTotal - int64(len(j.log))
		return original, nil
	}
	if overflow := len(j.log) + original - maxBuildJobLog; overflow > 0 {
		copy(j.log, j.log[overflow:])
		j.log = j.log[:len(j.log)-overflow]
		j.logBase += int64(overflow)
	}
	j.log = append(j.log, payload...)
	return original, nil
}

func (j *dockerBuildJob) snapshot(after int64, includeLog bool) DockerBuildJobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	view := j.view
	if !includeLog {
		return view
	}
	requested := after
	if after < j.logBase {
		after = j.logBase
		view.Truncated = true
	}
	if after > j.logTotal {
		after = j.logTotal
	}
	view.LogOffset = after
	view.NextOffset = j.logTotal
	view.Log = string(j.log[after-j.logBase:])
	view.Truncated = view.Truncated || requested < j.logBase
	return view
}

type DockerBuildJobManager struct {
	mu      sync.Mutex
	jobs    map[string]*dockerBuildJob
	execute buildJobExecutor
	sem     chan struct{}
}

func NewDockerBuildJobManager(execute buildJobExecutor) *DockerBuildJobManager {
	return &DockerBuildJobManager{jobs: make(map[string]*dockerBuildJob), execute: execute, sem: make(chan struct{}, 2)}
}

func (m *DockerBuildJobManager) Start(host, filename, imageTag, networkMode string) (DockerBuildJobView, error) {
	if m == nil || m.execute == nil {
		return DockerBuildJobView{}, errors.New("background Docker build service is unavailable")
	}
	id, err := randomBuildJobID()
	if err != nil {
		return DockerBuildJobView{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), buildJobTimeout)
	job := &dockerBuildJob{view: DockerBuildJobView{
		ID: id, Host: host, Filename: filename, ImageTag: imageTag, NetworkMode: networkMode, Status: buildJobQueued, CreatedAt: time.Now(),
	}, cancel: cancel}
	m.mu.Lock()
	m.pruneLocked(time.Now())
	pending := 0
	for _, existing := range m.jobs {
		view := existing.snapshot(0, false)
		if view.Host == host && (view.Status == buildJobQueued || view.Status == buildJobRunning) {
			pending++
		}
	}
	if pending >= maxPendingBuildJobs {
		m.mu.Unlock()
		cancel()
		return DockerBuildJobView{}, fmt.Errorf("too many Docker builds are already queued for host %q", host)
	}
	m.jobs[id] = job
	m.mu.Unlock()
	go m.run(ctx, job)
	return job.snapshot(0, false), nil
}

func (m *DockerBuildJobManager) run(ctx context.Context, job *dockerBuildJob) {
	select {
	case m.sem <- struct{}{}:
		defer func() { <-m.sem }()
	case <-ctx.Done():
		m.complete(job, buildJobCanceled, ctx.Err())
		return
	}

	now := time.Now()
	job.mu.Lock()
	job.view.Status = buildJobRunning
	job.view.StartedAt = &now
	job.mu.Unlock()
	_, _ = fmt.Fprintf(job, "*** background image build started: %s ***\n", job.view.ImageTag)
	err := m.execute(ctx, job.view.Host, job.view.Filename, job.view.ImageTag, job.view.NetworkMode, job)
	status := buildJobSucceeded
	if err != nil {
		status = buildJobFailed
		if errors.Is(ctx.Err(), context.Canceled) {
			status = buildJobCanceled
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("build exceeded the %s safety limit: %w", buildJobTimeout, ctx.Err())
		}
	}
	m.complete(job, status, err)
}

func (m *DockerBuildJobManager) complete(job *dockerBuildJob, status string, err error) {
	if err != nil {
		_, _ = fmt.Fprintf(job, "\n*** image build %s: %v ***\n", status, err)
	} else {
		_, _ = fmt.Fprintln(job, "\n*** image build completed ***")
	}
	now := time.Now()
	job.mu.Lock()
	job.view.Status = status
	job.view.CompletedAt = &now
	if err != nil {
		job.view.Error = strings.TrimSpace(err.Error())
	}
	job.mu.Unlock()
	job.cancel()
}

func (m *DockerBuildJobManager) List(host string) []DockerBuildJobView {
	m.mu.Lock()
	m.pruneLocked(time.Now())
	jobs := make([]*dockerBuildJob, 0, len(m.jobs))
	for _, job := range m.jobs {
		if job.view.Host == host {
			jobs = append(jobs, job)
		}
	}
	m.mu.Unlock()
	views := make([]DockerBuildJobView, 0, len(jobs))
	for _, job := range jobs {
		views = append(views, job.snapshot(0, false))
	}
	slices.SortFunc(views, func(a, b DockerBuildJobView) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return views
}

func (m *DockerBuildJobManager) Get(host, id string, after int64) (DockerBuildJobView, bool) {
	m.mu.Lock()
	job, found := m.jobs[id]
	m.mu.Unlock()
	if !found || job.view.Host != host {
		return DockerBuildJobView{}, false
	}
	return job.snapshot(after, true), true
}

func (m *DockerBuildJobManager) Cancel(host, id string) bool {
	m.mu.Lock()
	job, found := m.jobs[id]
	m.mu.Unlock()
	if !found || job.view.Host != host {
		return false
	}
	view := job.snapshot(0, false)
	if view.Status != buildJobQueued && view.Status != buildJobRunning {
		return false
	}
	job.cancel()
	return true
}

func (m *DockerBuildJobManager) pruneLocked(now time.Time) {
	completed := make([]*dockerBuildJob, 0, len(m.jobs))
	for id, job := range m.jobs {
		view := job.snapshot(0, false)
		if view.CompletedAt != nil && now.Sub(*view.CompletedAt) > buildJobRetention {
			delete(m.jobs, id)
			continue
		}
		if view.CompletedAt != nil {
			completed = append(completed, job)
		}
	}
	if len(m.jobs) <= maxRetainedBuildJobs {
		return
	}
	slices.SortFunc(completed, func(a, b *dockerBuildJob) int { return a.view.CreatedAt.Compare(b.view.CreatedAt) })
	for _, job := range completed {
		if len(m.jobs) <= maxRetainedBuildJobs {
			break
		}
		delete(m.jobs, job.view.ID)
	}
}

func randomBuildJobID() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("create build job identifier: %w", err)
	}
	return hex.EncodeToString(value), nil
}
