package updater

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptrace"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const updateScanConcurrency = 8

// One image check is up to three requests in series: the manifest HEAD that
// answers 401, the token GET, and the authenticated HEAD. Each keeps the
// registryRequestTimeout (15 s) of the HTTP client, so the budget that wraps
// them has to be their sum. The scan used to give the three of them one 15 s
// context, which meant any registry that was merely slow ran out of time on
// the second or third request and reported a bare "context deadline exceeded",
// although no single request had exceeded its own timeout.
//
// 3 x 15 s = 45 s is therefore the longest an attempt can take without a
// single request overrunning; every request still stops at 15 s on its own, so
// the budget only has to be generous, not tight.
//
// A transient failure is replayed once, after registryRetryDelay and with a
// fresh budget (the first one has expired by construction). Worst case for one
// image: 45 s + 2 s + 45 s = 92 s, reached only when the registry answers
// slowly but alive on all three requests twice; a registry that does not answer
// at all costs 15 s + 2 s + 15 s = 32 s. With 8 workers and 28 images, a fully
// black-holed registry delays a scan by about 4 rounds x 32 s, far inside the
// 2 h budget of the scan itself. Permanent failures are never replayed.
//
// The replay is for scans, where nobody waits on one given image. A check that
// answers a person looking at the screen (CheckContainerImageUpdate, behind
// Dockman's own update check) does not replay, see replayPolicy: it takes at
// most one attempt, 45 s in the worst case and 15 s against a registry that
// does not answer, instead of 92 s.
const (
	registryRequestsPerCheck = 3
	registryCheckBudget      = registryRequestsPerCheck * registryRequestTimeout
	registryRetryDelay       = 2 * time.Second
)

// replayPolicy says whether a transient failure of an image check is replayed.
type replayPolicy bool

const (
	// replayTransient is for scans: the failure is replayed once.
	replayTransient replayPolicy = true
	// noReplay is for a check someone is waiting on, which reports the first
	// failure as it is rather than making the person wait for a second attempt.
	noReplay replayPolicy = false
)

// remoteCheckSettings are the tunables of one image check. The zero value of
// Service.remoteCheck selects the production ones; tests inject short ones.
type remoteCheckSettings struct {
	// budget covers every request of one attempt.
	budget time.Duration
	// retryDelay separates the attempt from its single replay.
	retryDelay time.Duration
	// lookup resolves the remote digest of an image.
	lookup func(ctx context.Context, image string) (string, error)
	// logger receives the line written when an attempt fails transiently. Nil
	// selects the process-wide logger.
	logger *zerolog.Logger
}

func (u *Service) remoteCheckSettings() remoteCheckSettings {
	if u.remoteCheck != nil {
		return *u.remoteCheck
	}
	return remoteCheckSettings{budget: registryCheckBudget, retryDelay: registryRetryDelay, lookup: RegistryManifestDigest}
}

// ContainerUpdateStatus is deliberately explicit: an unavailable check must
// never be presented as "up to date".
type ContainerUpdateStatus string

const (
	ContainerUpdateAvailable ContainerUpdateStatus = "available"
	ContainerUpdateCurrent   ContainerUpdateStatus = "current"
	ContainerUpdateSkipped   ContainerUpdateStatus = "skipped"
	ContainerUpdateError     ContainerUpdateStatus = "error"
)

type ContainerUpdateCheck struct {
	ContainerID      string                `json:"containerId"`
	ContainerName    string                `json:"containerName"`
	Image            string                `json:"image"`
	Status           ContainerUpdateStatus `json:"status"`
	CurrentDigest    string                `json:"currentDigest,omitempty"`
	RemoteDigest     string                `json:"remoteDigest,omitempty"`
	Reason           string                `json:"reason,omitempty"`
	CurrentTag       string                `json:"currentTag,omitempty"`
	LatestTag        string                `json:"latestTag,omitempty"`
	VersionPolicy    string                `json:"versionPolicy,omitempty"`
	VersionAvailable bool                  `json:"versionAvailable"`
	VersionReason    string                `json:"versionReason,omitempty"`

	// ErrorClass tells a transient registry failure from a permanent one. It
	// feeds the notification logic only: it is neither stored nor sent to the
	// UI, which keeps showing Reason.
	ErrorClass RegistryErrorClass `json:"-"`
}

type imageUpdateCheck struct {
	status       ContainerUpdateStatus
	remoteDigest string
	reason       string
	errorClass   RegistryErrorClass
}

type inspectedImage struct {
	repositoryDigests []string
	err               error
}

// CheckContainerUpdates performs an on-demand, read-only scan. Registry work
// is de-duplicated by image reference and bounded so a host with many
// containers cannot create an unbounded burst against the daemon/proxy.
func (u *Service) CheckContainerUpdates(ctx context.Context) ([]ContainerUpdateCheck, error) {
	return u.checkContainerUpdates(ctx, nil)
}

// CheckContainerUpdatesByID limits registry work to the explicitly selected
// containers. An empty selection performs no registry request.
func (u *Service) CheckContainerUpdatesByID(ctx context.Context, containerIDs []string) ([]ContainerUpdateCheck, error) {
	if len(containerIDs) == 0 {
		return []ContainerUpdateCheck{}, nil
	}
	selected := make(map[string]struct{}, len(containerIDs))
	for _, id := range containerIDs {
		selected[id] = struct{}{}
	}
	return u.checkContainerUpdates(ctx, selected)
}

// CheckContainerImageUpdate compares one already-resolved container image with
// its remote registry digest without applying container-level exclusion rules.
// It exists for dedicated, read-only flows such as Dockman's self-update check;
// the regular inventory continues to skip Dockman itself by design. It answers
// an interactive request, so a transient registry failure is not replayed.
func (u *Service) CheckContainerImageUpdate(ctx context.Context, row container.Summary) ContainerUpdateCheck {
	result := ContainerUpdateCheck{
		ContainerID: row.ID, ContainerName: summaryName(row), Image: row.Image,
	}
	if row.ImageID == "" {
		result.Status = ContainerUpdateSkipped
		result.Reason = "container has no resolved local image"
		return result
	}

	inspect, err := u.cli().ImageInspect(ctx, row.ImageID)
	if err != nil {
		result.Status = ContainerUpdateError
		result.Reason = "inspect running image: " + err.Error()
		return result
	}
	localDigests := repositoryDigestsForImage(inspect.RepoDigests, row.Image)
	if len(localDigests) == 0 {
		result.Status = ContainerUpdateSkipped
		result.Reason = "locally built image; no matching repository digest"
		return result
	}
	result.CurrentDigest = localDigests[0]

	remote := u.checkRemoteImage(ctx, row.Image, noReplay)
	result.Status, result.RemoteDigest, result.Reason = remote.status, remote.remoteDigest, remote.reason
	result.ErrorClass = remote.errorClass
	if result.Status == ContainerUpdateCurrent && !slices.Contains(localDigests, result.RemoteDigest) {
		result.Status = ContainerUpdateAvailable
	}
	return result
}

func (u *Service) checkContainerUpdates(ctx context.Context, selected map[string]struct{}) ([]ContainerUpdateCheck, error) {
	containers, err := u.cli().ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	if selected != nil {
		filtered := containers.Items[:0]
		for _, item := range containers.Items {
			if _, ok := selected[item.ID]; ok {
				filtered = append(filtered, item)
			}
		}
		containers.Items = filtered
	}

	// Inspect each distinct image before contacting a registry. Locally built
	// images have no repository digest matching the tag used by the container;
	// attempting to resolve that tag on Docker Hub would turn an ordinary local
	// image into a misleading authentication/not-found error.
	inspected := u.inspectImages(ctx, containers.Items)
	byImage := make(map[string][]container.Summary)
	for _, row := range containers.Items {
		state := inspected[row.ImageID]
		if state.err == nil && len(repositoryDigestsForImage(state.repositoryDigests, row.Image)) > 0 {
			byImage[row.Image] = append(byImage[row.Image], row)
		}
	}
	images := make([]string, 0, len(byImage))
	for image := range byImage {
		images = append(images, image)
	}
	slices.Sort(images)

	checks := make(map[string]imageUpdateCheck, len(images))
	jobs := make(chan string)
	var mu sync.Mutex
	var wg sync.WaitGroup
	workers := min(updateScanConcurrency, len(images))
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for image := range jobs {
				check := u.checkRemoteImage(ctx, image, replayTransient)
				mu.Lock()
				checks[image] = check
				mu.Unlock()
			}
		}()
	}
	for _, image := range images {
		jobs <- image
	}
	close(jobs)
	wg.Wait()

	results := make([]ContainerUpdateCheck, 0, len(containers.Items))
	for _, row := range containers.Items {
		name := summaryName(row)
		result := ContainerUpdateCheck{
			ContainerID: row.ID, ContainerName: name, Image: row.Image,
		}
		if hasDockmanLabel(&row) {
			result.Status = ContainerUpdateSkipped
			result.Reason = "Dockman uses its dedicated self-update action"
			results = append(results, result)
			continue
		}
		if hasDisableUpdateLabel(&row) {
			result.Status = ContainerUpdateSkipped
			result.Reason = DockmanUpdateDisableLabel + "=true"
			results = append(results, result)
			continue
		}

		inspect := inspected[row.ImageID]
		if inspect.err != nil {
			result.Status = ContainerUpdateError
			result.Reason = "inspect running image: " + inspect.err.Error()
			results = append(results, result)
			continue
		}
		localDigests := repositoryDigestsForImage(inspect.repositoryDigests, row.Image)
		if len(localDigests) == 0 {
			result.Status = ContainerUpdateSkipped
			result.Reason = "locally built image; no matching repository digest"
			results = append(results, result)
			continue
		}
		if len(localDigests) > 0 {
			result.CurrentDigest = localDigests[0]
		}

		remote := checks[row.Image]
		result.Status, result.RemoteDigest, result.Reason = remote.status, remote.remoteDigest, remote.reason
		result.ErrorClass = remote.errorClass
		if result.Status == ContainerUpdateSkipped {
			results = append(results, result)
			continue
		}
		if result.Status == ContainerUpdateError {
			results = append(results, result)
			continue
		}
		if slices.Contains(localDigests, result.RemoteDigest) {
			result.Status = ContainerUpdateCurrent
		} else {
			result.Status = ContainerUpdateAvailable
		}
		results = append(results, result)
	}

	slices.SortFunc(results, func(a, b ContainerUpdateCheck) int {
		return strings.Compare(strings.ToLower(a.ContainerName), strings.ToLower(b.ContainerName))
	})
	return results, nil
}

func (u *Service) inspectImages(ctx context.Context, containers []container.Summary) map[string]inspectedImage {
	ids := make([]string, 0, len(containers))
	seen := make(map[string]struct{}, len(containers))
	for _, row := range containers {
		if row.ImageID == "" {
			continue
		}
		if _, exists := seen[row.ImageID]; exists {
			continue
		}
		seen[row.ImageID] = struct{}{}
		ids = append(ids, row.ImageID)
	}
	slices.Sort(ids)

	result := make(map[string]inspectedImage, len(ids))
	jobs := make(chan string)
	var mu sync.Mutex
	var wg sync.WaitGroup
	workers := min(updateScanConcurrency, len(ids))
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				inspect, err := u.cli().ImageInspect(ctx, id)
				mu.Lock()
				result[id] = inspectedImage{repositoryDigests: inspect.RepoDigests, err: err}
				mu.Unlock()
			}
		}()
	}
	for _, id := range ids {
		jobs <- id
	}
	close(jobs)
	wg.Wait()
	return result
}

func (u *Service) checkRemoteImage(ctx context.Context, image string, replay replayPolicy) imageUpdateCheck {
	if image == "" || strings.HasPrefix(image, "sha256:") {
		return imageUpdateCheck{status: ContainerUpdateSkipped, reason: "image has no pullable repository tag"}
	}
	if strings.Contains(image, "@sha256:") {
		return imageUpdateCheck{status: ContainerUpdateSkipped, reason: "image is pinned to a digest"}
	}
	if strings.HasPrefix(image, "localhost/") || strings.HasPrefix(image, "localhost:") ||
		strings.HasPrefix(image, "127.0.0.1/") || strings.HasPrefix(image, "127.0.0.1:") {
		return imageUpdateCheck{status: ContainerUpdateSkipped, reason: "local registry image"}
	}
	remoteDigest, err := u.lookupRemoteDigest(ctx, image, replay)
	if err != nil {
		reason := "registry check: " + err.Error()
		return imageUpdateCheck{status: ContainerUpdateError, reason: reason, errorClass: classifyRegistryError(err)}
	}
	digest := strings.TrimPrefix(remoteDigest, "sha256:")
	if digest == "" {
		return imageUpdateCheck{status: ContainerUpdateError, reason: "registry returned no digest"}
	}
	return imageUpdateCheck{status: ContainerUpdateCurrent, remoteDigest: digest}
}

// lookupRemoteDigest runs one attempt, and replays it once when it failed for a
// reason that waiting can fix. The replay gets a context derived from the
// caller's, never the expired one of the first attempt, and the wait gives way
// to the caller's cancellation: a scan that is being stopped must not sleep.
func (u *Service) lookupRemoteDigest(ctx context.Context, image string, replay replayPolicy) (string, error) {
	settings := u.remoteCheckSettings()
	started := time.Now()
	digest, err := settings.attempt(ctx, image, 1, started, replay)
	if err == nil || replay == noReplay || !errors.Is(err, errTransientRegistry) || ctx.Err() != nil {
		return digest, err
	}
	timer := time.NewTimer(settings.retryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return "", err
	case <-timer.C:
	}
	return settings.attempt(ctx, image, 2, started, replay)
}

// attempt runs one lookup under its own budget. When it fails transiently it
// writes one line saying where it stopped (see logTransientFailure); nothing is
// written when it succeeds, nor when the failure is permanent, which already
// reaches the operator with its own message.
func (s remoteCheckSettings) attempt(ctx context.Context, image string, number int, checkStarted time.Time, replay replayPolicy) (string, error) {
	attemptStarted := time.Now()
	attemptCtx, cancel := context.WithTimeout(ctx, s.budget)
	defer cancel()
	trace := newRegistryTrace()
	attemptCtx = httptrace.WithClientTrace(attemptCtx, trace.clientTrace())
	digest, err := s.lookup(attemptCtx, image)
	// A caller that is shutting down is not a registry problem.
	if err != nil && errors.Is(err, errTransientRegistry) && ctx.Err() == nil {
		s.logTransientFailure(image, number, replay, checkStarted, attemptStarted, err, trace.report())
	}
	return digest, err
}

// logTransientFailure says where a transient failure stalled, which the error
// alone ("context deadline exceeded") never does: the registry host, the
// attempt (1 is about to be replayed unless the check is not replayed, 2 is final), the time spent so far in the
// check and in this attempt, the stage the last request had reached (dns,
// connect, tls, send, wait for the response, read) and how long each stage
// reached took. It carries the error and the host names only, never a header or
// a credential.
func (s remoteCheckSettings) logTransientFailure(image string, attempt int, replay replayPolicy, checkStarted, attemptStarted time.Time, err error, report traceReport) {
	logger := s.logger
	if logger == nil {
		logger = &log.Logger
	}
	event := logger.Warn().
		Int("attempt", attempt).
		Dur("elapsed_ms", time.Since(checkStarted)).
		Dur("attempt_ms", time.Since(attemptStarted)).
		Str("stopped_at", string(report.stage))
	if ref, refErr := parseRegistryImageReference(image); refErr == nil {
		event = event.Str("registry", ref.registry)
	}
	if report.hostPort != "" {
		event = event.Str("request_host", report.hostPort)
	}
	for _, stage := range report.stages {
		event = event.Dur(stage.name+"_ms", stage.duration)
	}
	message := "registry check failed transiently, replaying once"
	switch {
	case attempt > 1:
		message = "registry check failed transiently after its replay"
	case replay == noReplay:
		message = "registry check failed transiently, not replayed"
	}
	event.Err(err).Msg(message)
}

func repositoryDigestsForImage(repoDigests []string, image string) []string {
	target, err := parseRegistryImageReference(image)
	if err != nil {
		return nil
	}
	targetRepository := target.registry + "/" + target.repo
	result := make([]string, 0, len(repoDigests))
	for _, value := range repoDigests {
		parts := strings.SplitN(value, "@sha256:", 2)
		if len(parts) != 2 || parts[1] == "" {
			continue
		}
		ref, refErr := parseRegistryImageReference(parts[0])
		if refErr != nil || ref.registry+"/"+ref.repo != targetRepository {
			continue
		}
		if !slices.Contains(result, parts[1]) {
			result = append(result, parts[1])
		}
	}
	return result
}
