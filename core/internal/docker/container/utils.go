package container

import (
	"github.com/moby/moby/api/types/container"
)

func formatDiskIO(statsJSON container.StatsResponse) (uint64, uint64) {
	var blkRead, blkWrite uint64
	for _, bioEntry := range statsJSON.BlkioStats.IoServiceBytesRecursive {
		switch bioEntry.Op {
		case "read":
			blkRead += bioEntry.Value
		case "write":
			blkWrite += bioEntry.Value
		}
	}
	return blkRead, blkWrite
}

// Collect Network and Disk I/O
func formatNetwork(statsJSON container.StatsResponse) (uint64, uint64) {
	var rx, tx uint64
	for _, v := range statsJSON.Networks {
		rx += v.RxBytes
		tx += v.TxBytes
	}
	return rx, tx
}

func formatCPU(statsJSON container.StatsResponse) float64 {
	cpuDelta := float64(statsJSON.CPUStats.CPUUsage.TotalUsage - statsJSON.PreCPUStats.CPUUsage.TotalUsage)
	systemCpuDelta := float64(statsJSON.CPUStats.SystemUsage - statsJSON.PreCPUStats.SystemUsage)
	numberCPUs := float64(statsJSON.CPUStats.OnlineCPUs)
	if numberCPUs == 0.0 {
		numberCPUs = float64(len(statsJSON.CPUStats.CPUUsage.PercpuUsage))
	}

	var cpuPercent = 0.0
	// Avoid division by zero
	if systemCpuDelta > 0.0 && cpuDelta > 0.0 {
		cpuPercent = (cpuDelta / systemCpuDelta) * numberCPUs * 100.0
	}

	return cpuPercent
}

// Stats Stats holds metrics for a single Docker container.
type Stats struct {
	ID           string
	Name         string
	Image        string   // image reference the container was created from
	State        string   // running / exited / paused / restarting ...
	Health       string   // healthy / unhealthy / starting; empty when no healthcheck
	IPAddress    []string // container network IPs
	RestartCount int32
	CPUUsage     float64
	MemoryUsage  uint64 // in bytes
	MemoryLimit  uint64 // in bytes
	NetworkRx    uint64 // bytes received
	NetworkTx    uint64 // bytes sent
	BlockRead    uint64 // bytes read from block devices
	BlockWrite   uint64 // bytes written to block devices
	StartedAt    string // container start time, RFC3339 (empty if unknown)
}
