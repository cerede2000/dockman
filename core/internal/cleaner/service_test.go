package cleaner

import (
	"math"
	"testing"
	"time"

	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

func TestNormalizeDiskUsageDoesNotExposeMobyActiveImagesAsReclaimable(t *testing.T) {
	usage := client.DiskUsageResult{}
	usage.Images.ActiveCount = 2
	usage.Images.TotalCount = 2
	usage.Images.TotalSize = 49 << 30
	usage.Images.Reclaimable = usage.Images.TotalSize // Moby 29 erroneous aggregate
	usage.Images.Items = []image.Summary{
		{ID: "active-a", Containers: 1, Size: 30 << 30, SharedSize: 10 << 30},
		{ID: "active-b", Containers: 2, Size: 29 << 30, SharedSize: 10 << 30},
	}

	normalizeDiskUsage(&usage)
	require.Zero(t, usage.Images.Reclaimable)
	require.Equal(t, int64(49<<30), usage.Images.TotalSize)
}

func TestNormalizeDiskUsageUsesOnlyConservativeUnusedImageBytes(t *testing.T) {
	usage := client.DiskUsageResult{}
	usage.Images.Items = []image.Summary{
		{ID: "active", Containers: 1, Size: 900, SharedSize: 100},
		{ID: "unused", Containers: 0, Size: 700, SharedSize: 250},
		{ID: "fully-shared", Containers: 0, Size: 250, SharedSize: 250},
		{ID: "unknown", Containers: -1, Size: 500, SharedSize: 0},
		{ID: "invalid", Containers: 0, Size: 100, SharedSize: 200},
	}
	usage.Images.Reclaimable = math.MaxInt64

	normalizeDiskUsage(&usage)
	require.Equal(t, int64(450), usage.Images.Reclaimable)
}

func TestNormalizeDiskUsageClampsUnknownAggregates(t *testing.T) {
	usage := client.DiskUsageResult{}
	usage.Containers.TotalSize, usage.Containers.Reclaimable = -1, -1
	usage.Volumes.TotalSize, usage.Volumes.Reclaimable = -1, -1
	usage.BuildCache.TotalSize, usage.BuildCache.Reclaimable = -1, -1

	normalizeDiskUsage(&usage)
	require.Zero(t, usage.Containers.TotalSize)
	require.Zero(t, usage.Containers.Reclaimable)
	require.Zero(t, usage.Volumes.TotalSize)
	require.Zero(t, usage.Volumes.Reclaimable)
	require.Zero(t, usage.BuildCache.TotalSize)
	require.Zero(t, usage.BuildCache.Reclaimable)
}

func TestNormalizedPruneCronSupportsSchedulesAndMigratesIntervals(t *testing.T) {
	daily, err := normalizedPruneCron(" 0  3  * * * ", 0)
	require.NoError(t, err)
	require.Equal(t, "0 3 * * *", daily)

	legacy, err := normalizedPruneCron("", 12*time.Hour)
	require.NoError(t, err)
	require.Equal(t, "@every 12h0m0s", legacy)

	_, err = normalizedPruneCron("*/30 * * * *", 0)
	require.ErrorContains(t, err, "once per hour")
	_, err = normalizedPruneCron("not a cron", 0)
	require.ErrorContains(t, err, "five-field")
}
