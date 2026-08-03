package updater

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const updateScanConcurrency = 8

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
	ContainerID   string                `json:"containerId"`
	ContainerName string                `json:"containerName"`
	Image         string                `json:"image"`
	Status        ContainerUpdateStatus `json:"status"`
	CurrentDigest string                `json:"currentDigest,omitempty"`
	RemoteDigest  string                `json:"remoteDigest,omitempty"`
	Reason        string                `json:"reason,omitempty"`
}

type imageUpdateCheck struct {
	status       ContainerUpdateStatus
	remoteDigest string
	reason       string
}

type inspectedImage struct {
	repositoryDigests []string
	err               error
}

// CheckContainerUpdates performs an on-demand, read-only scan. Registry work
// is de-duplicated by image reference and bounded so a host with many
// containers cannot create an unbounded burst against the daemon/proxy.
func (u *Service) CheckContainerUpdates(ctx context.Context) ([]ContainerUpdateCheck, error) {
	containers, err := u.cli().ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
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
				check := u.checkRemoteImage(ctx, image)
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

func (u *Service) checkRemoteImage(ctx context.Context, image string) imageUpdateCheck {
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
	lookupCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	remoteDigest, err := RegistryManifestDigest(lookupCtx, image)
	if err != nil {
		reason := "registry check: " + err.Error()
		return imageUpdateCheck{status: ContainerUpdateError, reason: reason}
	}
	digest := strings.TrimPrefix(remoteDigest, "sha256:")
	if digest == "" {
		return imageUpdateCheck{status: ContainerUpdateError, reason: "registry returned no digest"}
	}
	return imageUpdateCheck{status: ContainerUpdateCurrent, remoteDigest: digest}
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
