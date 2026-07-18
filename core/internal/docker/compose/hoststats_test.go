package compose

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const procSampleA = `cpu  1000 0 500 8000 500 0 0 0 0 0
cpu0 500 0 250 4000 250 0 0 0 0 0
cpu1 500 0 250 4000 250 0 0 0 0 0
intr 12345
MemTotal:       32000000 kB
MemFree:         8000000 kB
MemAvailable:   20000000 kB
Buffers:          500000 kB
`

// +2000 total ticks, +1000 of them idle+iowait -> 50% busy
const procSampleB = `cpu  1600 0 900 8800 700 0 0 0 0 0
cpu0 800 0 450 4400 350 0 0 0 0 0
cpu1 800 0 450 4400 350 0 0 0 0 0
intr 12345
MemTotal:       32000000 kB
MemFree:         8000000 kB
MemAvailable:   20000000 kB
Buffers:          500000 kB
`

func TestParseHostProc(t *testing.T) {
	first := parseHostProc("test-host-a", procSampleA)

	require.EqualValues(t, 2, first.CPUs)
	require.EqualValues(t, int64(32000000)*1024, first.MemTotal)
	require.EqualValues(t, int64(12000000)*1024, first.MemUsed) // total - available
	// no previous sample: percentage not yet computable
	require.Zero(t, first.CPUPercent)

	second := parseHostProc("test-host-a", procSampleB)
	require.InDelta(t, 50.0, second.CPUPercent, 0.01)
}

func TestParseHostProcCounterReset(t *testing.T) {
	parseHostProc("test-host-b", procSampleB)
	// counters went backwards (host rebooted): report 0, not garbage
	reset := parseHostProc("test-host-b", procSampleA)
	require.Zero(t, reset.CPUPercent)
}

func TestParseHostProcGarbage(t *testing.T) {
	stats := parseHostProc("test-host-c", "not proc output at all")
	require.Zero(t, stats.CPUPercent)
	require.Zero(t, stats.MemTotal)
	require.Zero(t, stats.CPUs)
}
