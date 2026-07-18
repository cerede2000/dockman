package compose

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// HostStats is whole-host usage read from /proc through the host's runner:
// locally /proc/stat and /proc/meminfo are not namespaced, and on ssh hosts
// the same files are read remotely — no agent needed.
type HostStats struct {
	CPUPercent float64
	MemUsed    int64
	MemTotal   int64
	CPUs       int32
}

type cpuSample struct {
	idle  uint64
	total uint64
}

// previous CPU sample per host: each call reports usage since the last one
var (
	cpuSamplesMu sync.Mutex
	cpuSamples   = map[string]cpuSample{}
)

func (c *Service) HostStats(ctx context.Context) (HostStats, error) {
	out := new(bytes.Buffer)
	errW := new(bytes.Buffer)
	if err := c.runner.Run(ctx, []string{"cat", "/proc/stat", "/proc/meminfo"}, ".", out, errW); err != nil {
		if errW.Len() > 0 {
			return HostStats{}, fmt.Errorf("%s", errW.String())
		}
		return HostStats{}, err
	}
	return parseHostProc(c.hostname, out.String()), nil
}

// parseHostProc digests the concatenated /proc/stat + /proc/meminfo output.
// The CPU percentage is a delta against the previous sample stored for this
// host, so the first call after startup reports 0.
func parseHostProc(host, raw string) HostStats {
	var stats HostStats
	var sample cpuSample
	var memTotal, memAvail int64
	haveCPU := false

	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch {
		case fields[0] == "cpu":
			// aggregate line: user nice system idle iowait irq softirq steal...
			for i, f := range fields[1:] {
				v, err := strconv.ParseUint(f, 10, 64)
				if err != nil {
					continue
				}
				sample.total += v
				if i == 3 || i == 4 { // idle + iowait count as idle time
					sample.idle += v
				}
			}
			haveCPU = true
		case strings.HasPrefix(fields[0], "cpu"):
			// cpu0, cpu1... one per core
			stats.CPUs++
		case fields[0] == "MemTotal:":
			if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				memTotal = kb * 1024
			}
		case fields[0] == "MemAvailable:":
			if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				memAvail = kb * 1024
			}
		}
	}

	stats.MemTotal = memTotal
	if memTotal > memAvail {
		stats.MemUsed = memTotal - memAvail
	}

	if haveCPU {
		cpuSamplesMu.Lock()
		prev, ok := cpuSamples[host]
		cpuSamples[host] = sample
		cpuSamplesMu.Unlock()

		// counters reset on host reboot: skip that round instead of reporting garbage
		if ok && sample.total > prev.total && sample.idle >= prev.idle {
			dTotal := float64(sample.total - prev.total)
			dIdle := float64(sample.idle - prev.idle)
			pct := (1 - dIdle/dTotal) * 100
			stats.CPUPercent = min(max(pct, 0), 100)
		}
	}

	return stats
}
