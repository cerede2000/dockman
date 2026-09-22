package compose

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

// BuildLimits caps the resources of the image builds Dockman runs on a host:
// Compose stacks with a build section and Dockerfile builds from Files.
//
// Build flags cannot do it. `docker build --cpu-quota` belonged to the legacy
// builder; BuildKit ignores it, Buildx only brought a --resource flag back in
// 0.35, and Compose passes no such limit to the bake it delegates builds to.
// The limit is therefore put on the builder itself: a docker-container
// builder created with cpu-quota/memory driver options runs every RUN step
// inside that container's cgroup.
type BuildLimits struct {
	// CPUs is a number of cores, fractional allowed; 0 means no limit.
	CPUs float64
	// MemoryBytes is the builder's memory limit; 0 means no limit.
	MemoryBytes int64
}

// Bounds the host settings accept. Below these the builder itself - BuildKit
// and its RUN containers - cannot work, and the build would fail in a way
// that points nowhere near the setting.
const (
	MinBuildCPUs        = 0.1
	MinBuildMemoryBytes = 256 << 20
)

// Active reports whether builds on the host run capped at all.
func (l BuildLimits) Active() bool { return l.CPUs > 0 || l.MemoryBytes > 0 }

// Validate refuses limits a builder could not run under.
func (l BuildLimits) Validate() error {
	if l.CPUs < 0 || math.IsNaN(l.CPUs) || math.IsInf(l.CPUs, 0) {
		return fmt.Errorf("build CPU limit must be a positive number of cores, or 0 for no limit")
	}
	if l.CPUs > 0 && l.CPUs < MinBuildCPUs {
		return fmt.Errorf("build CPU limit must be at least %.1f core", MinBuildCPUs)
	}
	if l.MemoryBytes < 0 {
		return fmt.Errorf("build memory limit must be positive, or 0 for no limit")
	}
	if l.MemoryBytes > 0 && l.MemoryBytes < MinBuildMemoryBytes {
		return fmt.Errorf("build memory limit must be at least %d MiB", MinBuildMemoryBytes>>20)
	}
	return nil
}

func (l BuildLimits) String() string {
	var parts []string
	if l.CPUs > 0 {
		parts = append(parts, strconv.FormatFloat(l.CPUs, 'f', -1, 64)+" CPU")
	}
	if l.MemoryBytes > 0 {
		parts = append(parts, formatBuildMemory(l.MemoryBytes))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func formatBuildMemory(b int64) string {
	if b%(1<<30) == 0 {
		return strconv.FormatInt(b>>30, 10) + " GiB"
	}
	return strconv.FormatInt(b>>20, 10) + " MiB"
}

// cfsPeriodMicros is the CFS period the CPU quota is expressed against: one
// core is a quota equal to the period.
const cfsPeriodMicros = 100000

// driverOpts are the docker-container driver options carrying the limits.
func (l BuildLimits) driverOpts() []string {
	var opts []string
	if l.CPUs > 0 {
		quota := int64(math.Round(l.CPUs * cfsPeriodMicros))
		opts = append(opts,
			"--driver-opt", "cpu-period="+strconv.Itoa(cfsPeriodMicros),
			"--driver-opt", "cpu-quota="+strconv.FormatInt(quota, 10))
	}
	if l.MemoryBytes > 0 {
		opts = append(opts, "--driver-opt", "memory="+strconv.FormatInt(l.MemoryBytes, 10))
	}
	return opts
}

// SetBuildLimits applies the host's build limits to this service's builds.
func (c *Service) SetBuildLimits(limits BuildLimits) { c.buildLimits = limits }

// The limited builder is recreated for every build and removed right after
// with --keep-state: no BuildKit container idles between builds, the limits
// in force are always the current settings, and the build cache survives in
// the builder's state volume. Its own Buildx state directory keeps it apart
// from the user's builders and from the one Files builds use without limits.
const (
	limitedBuilderName   = "dockman-limited"
	limitedBuilderConfig = "/tmp/dockman-buildx-limited"
	// the container the docker-container driver creates for node 0
	limitedBuilderContainer = "buildx_buildkit_" + limitedBuilderName + "0"
)

func limitedBuildxArgs(args ...string) []string {
	return append([]string{"env", "BUILDX_CONFIG=" + limitedBuilderConfig, "BUILDX_BUILDER="}, args...)
}

// limitedBuilderEnv routes a Compose command's builds to the limited builder:
// Compose hands builds to `docker buildx bake`, which honours both.
func limitedBuilderEnv() []string {
	return []string{"BUILDX_CONFIG=" + limitedBuilderConfig, "BUILDX_BUILDER=" + limitedBuilderName}
}

// One limited builder exists per daemon, under a fixed name, so builds on a
// host take turns. That is also the point of limiting them: two builds at
// once would share nothing but the machine they were meant to spare.
var limitedBuildSlots sync.Map // hostname -> chan struct{} (one slot)

func acquireLimitedBuild(ctx context.Context, host string) (func(), error) {
	slot, _ := limitedBuildSlots.LoadOrStore(host, make(chan struct{}, 1))
	ch := slot.(chan struct{})
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for another build on this host to finish: %w", ctx.Err())
	}
}

// withLimitedBuilder runs build with a docker-container builder capped to the
// host's limits, created just before and removed just after.
func (c *Service) withLimitedBuilder(ctx context.Context, wd string, stream io.Writer, allowHostNetwork bool, build func(builder string) error) error {
	release, err := acquireLimitedBuild(ctx, c.hostname)
	if err != nil {
		return err
	}
	defer release()

	// A run that died mid-build leaves the builder behind - its state entry,
	// or only its container once /tmp is gone. Recreating over it would keep
	// the old limits, so both go first. Neither can be anyone else's: the
	// name is Dockman's own.
	c.removeLimitedBuilder(wd)

	args := []string{"docker", "buildx", "create", "--name", limitedBuilderName, "--driver", "docker-container"}
	args = append(args, c.buildLimits.driverOpts()...)
	if allowHostNetwork {
		args = append(args, "--buildkitd-flags", "--allow-insecure-entitlement network.host")
	}
	var output bytes.Buffer
	if err := c.runner.Run(ctx, limitedBuildxArgs(args...), wd, nil, io.Discard, &output); err != nil {
		message := strings.TrimSpace(output.String())
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("create the limited builder: %s", message)
	}
	// Removal must happen even when the request that started the build is
	// gone: a builder left running is exactly what this design avoids.
	defer c.removeLimitedBuilder(wd)

	if stream != nil {
		_, _ = fmt.Fprintf(stream, "*** Build limits: %s ***\n", c.buildLimits)
	}
	return build(limitedBuilderName)
}

// removeLimitedBuilder drops the builder and its container but keeps the
// state volume, which holds the build cache.
func (c *Service) removeLimitedBuilder(wd string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = c.runner.Run(ctx, limitedBuildxArgs("docker", "buildx", "rm", "--force", "--keep-state", limitedBuilderName), wd, nil, io.Discard, io.Discard)
	_ = c.runner.Run(ctx, []string{"docker", "rm", "--force", limitedBuilderContainer}, wd, nil, io.Discard, io.Discard)
}
