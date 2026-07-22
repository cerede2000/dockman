package gitsync

import (
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	gitMemoryLogThreshold     = 32 << 20
	gitMemoryReleaseThreshold = 32 << 20
	gitHeapReleaseFloor       = 64 << 20
)

type gitMemorySnapshot struct {
	heapAlloc     uint64
	heapSys       uint64
	heapIdle      uint64
	heapReleased  uint64
	processRSS    uint64
	cgroupCurrent uint64
}

func captureGitMemory() gitMemorySnapshot {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return gitMemorySnapshot{
		heapAlloc: stats.HeapAlloc, heapSys: stats.HeapSys, heapIdle: stats.HeapIdle,
		heapReleased: stats.HeapReleased, processRSS: readLinuxRSS(),
		cgroupCurrent: readUintFile("/sys/fs/cgroup/memory.current"),
	}
}

func observeGitMemory(operation string) func() {
	before := captureGitMemory()
	startedAt := time.Now()
	return func() {
		after := captureGitMemory()
		reclaimable := positiveDifference(after.heapIdle, after.heapReleased)
		growth := positiveDifference(after.heapSys, before.heapSys)
		rssGrowth := positiveDifference(after.processRSS, before.processRSS)
		if after.heapSys >= gitHeapReleaseFloor && (reclaimable >= gitMemoryReleaseThreshold || growth >= gitMemoryReleaseThreshold || rssGrowth >= gitMemoryReleaseThreshold) {
			debug.FreeOSMemory()
			released := captureGitMemory()
			log.Info().Str("operation", operation).
				Int64("duration_ms", time.Since(startedAt).Milliseconds()).
				Uint64("heap_alloc_before", before.heapAlloc).Uint64("heap_alloc_after", released.heapAlloc).
				Uint64("heap_sys_before", before.heapSys).Uint64("heap_sys_after", released.heapSys).
				Uint64("rss_before", before.processRSS).Uint64("rss_after", released.processRSS).
				Uint64("cgroup_before", before.cgroupCurrent).Uint64("cgroup_after", released.cgroupCurrent).
				Msg("released transient Git synchronization memory")
			return
		}
		if growth >= gitMemoryLogThreshold || rssGrowth >= gitMemoryLogThreshold {
			log.Info().Str("operation", operation).
				Int64("duration_ms", time.Since(startedAt).Milliseconds()).
				Uint64("heap_alloc_before", before.heapAlloc).Uint64("heap_alloc_after", after.heapAlloc).
				Uint64("heap_sys_before", before.heapSys).Uint64("heap_sys_after", after.heapSys).
				Uint64("rss_before", before.processRSS).Uint64("rss_after", after.processRSS).
				Uint64("cgroup_before", before.cgroupCurrent).Uint64("cgroup_after", after.cgroupCurrent).
				Msg("observed Git synchronization memory growth")
		}
	}
}

func positiveDifference(value, baseline uint64) uint64 {
	if value <= baseline {
		return 0
	}
	return value - baseline
}

func readUintFile(path string) uint64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	value, _ := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return value
}

func readLinuxRSS() uint64 {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "VmRSS:" {
			kilobytes, _ := strconv.ParseUint(fields[1], 10, 64)
			return kilobytes << 10
		}
	}
	return 0
}
