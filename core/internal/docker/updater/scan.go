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

// CheckContainerUpdates performs an on-demand, read-only scan. Registry work
// is de-duplicated by image reference and bounded so a host with many
// containers cannot create an unbounded burst against the daemon/proxy.
func (u *Service) CheckContainerUpdates(ctx context.Context) ([]ContainerUpdateCheck, error) {
	containers, err := u.cli().ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	byImage := make(map[string][]container.Summary)
	for _, row := range containers.Items {
		byImage[row.Image] = append(byImage[row.Image], row)
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

		remote := checks[row.Image]
		result.Status, result.RemoteDigest, result.Reason = remote.status, remote.remoteDigest, remote.reason
		if result.Status == ContainerUpdateSkipped {
			results = append(results, result)
			continue
		}

		inspect, inspectErr := u.cli().ImageInspect(ctx, row.ImageID)
		if inspectErr != nil {
			result.Status = ContainerUpdateError
			result.Reason = "inspect running image: " + inspectErr.Error()
			results = append(results, result)
			continue
		}
		localDigests := repositoryDigests(inspect.RepoDigests)
		if len(localDigests) > 0 {
			result.CurrentDigest = localDigests[0]
		}
		if result.Status == ContainerUpdateError {
			mustReport := strings.Contains(result.Reason, "authentication required") ||
				strings.Contains(result.Reason, "rate limit")
			if len(localDigests) == 0 && !mustReport {
				result.Status = ContainerUpdateSkipped
				result.Reason = "image has no repository digest and its tag could not be resolved; treating it as local"
			}
			results = append(results, result)
			continue
		}
		if len(localDigests) == 0 {
			// This is also what Docker exposes after a mutable tag moved away
			// from the image still used by a running container. A resolvable
			// registry digest therefore means that the running image differs.
			result.Status = ContainerUpdateAvailable
			result.Reason = "running image has no repository digest"
		} else if slices.Contains(localDigests, result.RemoteDigest) {
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

func repositoryDigests(repoDigests []string) []string {
	result := make([]string, 0, len(repoDigests))
	for _, value := range repoDigests {
		parts := strings.SplitN(value, "@sha256:", 2)
		if len(parts) == 2 && parts[1] != "" && !slices.Contains(result, parts[1]) {
			result = append(result, parts[1])
		}
	}
	return result
}
