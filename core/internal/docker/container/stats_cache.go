package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/RA341/dockman/pkg/fileutil"
	"github.com/RA341/dockman/pkg/syncmap"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/rs/zerolog/log"
)

// statsConcurrency bounds how many stats reads run at once. Each read makes
// the daemon precollect a sample (~1s), so the bound must comfortably exceed
// the typical container count for the whole batch to complete in one ~1s
// wave — while still capping the fan-out against remote/SSH hosts.
const statsConcurrency = 32

// inspectData caches the inspect-only fields served with stats. An entry is
// valid while the summary's Status text ("Up 3 minutes", "Exited (0)...") is
// unchanged: the daemon rewrites it whenever the underlying state moves —
// including restarts ("Up 1 second") — so the cache refreshes itself exactly
// when it could be stale instead of paying one ContainerInspect per container
// on every poll.
type inspectData struct {
	status       string
	startedAt    string
	restartCount int32
}

// hostStatsCache carries per-host inspect data between stats requests. It is
// keyed package-wide by the moby client — one per connected host — because
// container.Service is rebuilt on every RPC and cannot hold state itself.
type hostStatsCache struct {
	mu       sync.Mutex
	inspects map[string]inspectData
}

var hostCaches syncmap.Map[*client.Client, *hostStatsCache]

func cacheFor(cli *client.Client) *hostStatsCache {
	cache, _ := hostCaches.LoadOrStore(cli, &hostStatsCache{
		inspects: make(map[string]inspectData),
	})
	return cache
}

// prune drops cached state for containers that no longer exist, so the map
// doesn't grow forever as containers are recreated. Call it only with a full
// host listing: a filtered subset would evict live neighbors.
func (c *hostStatsCache) prune(live map[string]struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.inspects {
		if _, ok := live[id]; !ok {
			delete(c.inspects, id)
		}
	}
}

// collectStats gathers stats for the given containers with bounded concurrency,
// preserving input order. Containers that fail are skipped, not fatal.
func (s *Service) collectStats(ctx context.Context, containers []container.Summary) []Stats {
	cache := cacheFor(s.Client)

	results := make([]*Stats, len(containers))
	sem := make(chan struct{}, statsConcurrency)
	var wg sync.WaitGroup
	for i, cont := range containers {
		wg.Add(1)
		go func(i int, cont container.Summary) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			stat, err := s.statsFor(ctx, cache, cont)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Warn().Err(err).Str("container", cont.ID[:12]).Msg("could not collect stats, skipping...")
				}
				return
			}
			results[i] = &stat
		}(i, cont)
	}
	wg.Wait()

	out := make([]Stats, 0, len(containers))
	for _, r := range results {
		if r != nil {
			out = append(out, *r)
		}
	}
	return out
}

func (s *Service) statsFor(ctx context.Context, cache *hostStatsCache, info container.Summary) (Stats, error) {
	stat := Stats{
		ID:        info.ID[:12],
		Name:      info.Names[0],
		Image:     info.Image,
		State:     string(info.State),
		Health:    summaryHealth(info),
		IPAddress: summaryIPs(info),
	}

	// inspect-only fields, served from cache while the container's status text
	// is unchanged; non-fatal so a race with a disappearing container can't
	// take the whole table down
	if insp, err := s.inspectDataFor(ctx, cache, info); err == nil {
		stat.StartedAt = insp.startedAt
		stat.RestartCount = insp.restartCount
	} else if !errors.Is(err, context.Canceled) {
		log.Debug().Err(err).Str("container", stat.ID).Msg("could not inspect container for stats")
	}

	// a container that isn't running has no live metrics to read
	if stat.State != "running" {
		return stat, nil
	}

	statsJSON, err := s.readStats(ctx, info.ID)
	if err != nil {
		return Stats{}, err
	}

	stat.CPUUsage = formatCPU(statsJSON)
	stat.MemoryUsage = formatMemory(statsJSON)
	stat.MemoryLimit = statsJSON.MemoryStats.Limit
	stat.NetworkRx, stat.NetworkTx = formatNetwork(statsJSON)
	stat.BlockRead, stat.BlockWrite = formatDiskIO(statsJSON)

	return stat, nil
}

func (s *Service) readStats(ctx context.Context, id string) (container.StatsResponse, error) {
	// IncludePreviousSample makes the daemon return a reading that carries its
	// own precpu sample, so the CPU percentage is computed exactly like
	// `docker stats` from a single self-contained response. The daemon needs
	// ~1s to build it, but every container is read concurrently so a poll
	// costs ~1s wall time regardless of container count.
	resp, err := s.Client.ContainerStats(ctx, id, client.ContainerStatsOptions{
		IncludePreviousSample: true,
	})
	if err != nil {
		return container.StatsResponse{}, fmt.Errorf("failed to get stats for cont %s: %w", id[:12], err)
	}
	defer fileutil.Close(resp.Body)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return container.StatsResponse{}, fmt.Errorf("failed to read body for cont %s: %w", id[:12], err)
	}
	var statsJSON container.StatsResponse
	if err := json.Unmarshal(body, &statsJSON); err != nil {
		return container.StatsResponse{}, fmt.Errorf("failed to unmarshal body for cont %s: %w", id[:12], err)
	}
	return statsJSON, nil
}

func (s *Service) inspectDataFor(ctx context.Context, cache *hostStatsCache, info container.Summary) (inspectData, error) {
	cache.mu.Lock()
	cached, ok := cache.inspects[info.ID]
	cache.mu.Unlock()
	if ok && cached.status == info.Status {
		return cached, nil
	}

	inspect, err := s.Client.ContainerInspect(ctx, info.ID, client.ContainerInspectOptions{})
	if err != nil {
		return inspectData{}, err
	}

	data := inspectData{
		status:       info.Status,
		restartCount: int32(inspect.Container.RestartCount),
	}
	if state := inspect.Container.State; state != nil {
		data.startedAt = state.StartedAt
	}

	cache.mu.Lock()
	cache.inspects[info.ID] = data
	cache.mu.Unlock()
	return data, nil
}

func summaryHealth(info container.Summary) string {
	if info.Health.Status != container.NoHealthcheck {
		return string(info.Health.Status)
	}
	return ""
}

func summaryIPs(info container.Summary) []string {
	var ips []string
	for _, netConf := range info.NetworkSettings.Networks {
		ip := netConf.IPAddress.String()
		if ip != "invalid IP" && ip != "" {
			ips = append(ips, ip)
		}
	}
	// map iteration order is random; keep the column stable between polls
	slices.Sort(ips)
	return ips
}
