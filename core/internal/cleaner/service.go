package cleaner

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/RA341/dockman/internal/docker"
	"github.com/RA341/dockman/internal/docker/container"
	"github.com/RA341/dockman/pkg/syncmap"
	"github.com/dustin/go-humanize"
	"github.com/go-co-op/gocron/v2"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	defaultPruneCron = "0 3 * * *"
	minimumPruneGap  = time.Hour
)

type GetService func(host string) (*docker.Service, error)

type Service struct {
	cont  GetService
	store Store
	log   zerolog.Logger

	taskList syncmap.Map[string, gocron.Job]
	schd     gocron.Scheduler
	notify   func(context.Context, PruneResult, bool)
}

func (s *Service) SetNotifier(notify func(context.Context, PruneResult, bool)) {
	s.notify = notify
}

func NewService(cont GetService, store Store) *Service {
	s := &Service{
		cont:  cont,
		store: store,
		log:   log.With().Str("service", "docker cleaner").Logger(),
	}

	schd, err := gocron.NewScheduler()
	if err != nil {
		s.log.Fatal().Err(err).Msg("Failed to initialize task runner")
	}
	s.schd = schd
	schd.Start()

	s.StartEnabled()

	return s
}

func (s *Service) cli(hostname string) (*container.Service, error) {
	cont, err := s.cont(hostname)
	if err != nil {
		return nil, err
	}
	return cont.Container, nil
}

type DiskSpace struct {
	Containers string
	Image      string
	Volumes    string
	BuildCache string
}

func (s *Service) GetSystemStorage(ctx context.Context, hostname string) (client.DiskUsageResult, []network.Inspect, error) {
	cli, err := s.cli(hostname)
	if err != nil {
		return client.DiskUsageResult{}, nil, err
	}

	usage, err := cli.Cli().DiskUsage(ctx, client.DiskUsageOptions{
		Containers: true,
		Images:     true,
		BuildCache: true,
		Volumes:    true,
		Verbose:    true,
	})
	if err != nil {
		return client.DiskUsageResult{}, nil, err
	}
	normalizeDiskUsage(&usage)

	list, err := cli.NetworksList(ctx)
	if err != nil {
		return client.DiskUsageResult{}, nil, err
	}

	return usage, list, nil
}

func (s *Service) RunOnce(ctx context.Context, host string, pruneConfig *PruneConfig) error {
	log.Debug().Msg("running manual docker cleaner")

	cli, err := s.cli(host)
	if err != nil {
		if s.notify != nil {
			s.notify(ctx, PruneResult{Host: host, Err: err.Error()}, false)
		}
		return fmt.Errorf("could find docker client: %w", err)
	}

	var res PruneResult
	res.Host = host

	s.Prune(ctx, pruneConfig, cli.Client, &res)
	err = s.store.AddResult(&res)
	if err != nil {
		s.log.Err(err).Msg("Failed to add result for cleaner")
	}
	if s.notify != nil {
		s.notify(ctx, res, false)
	}

	return nil
}

func (s *Service) StartEnabled() {
	enabled, err := s.store.GetEnabled()
	if err != nil {
		log.Warn().Err(err).Msg("Failed to get enabled cleaner configs")
		return
	}

	for _, cf := range enabled {
		err := s.RunWithScheduler(cf.Host, false)
		if err != nil {
			log.Warn().Err(err).Str("host", cf.Host).Msg("Failed to run cleaner")
		}
	}
}

func (s *Service) RunWithScheduler(host string, edit bool) error {
	getConfig, err := s.store.GetConfig(host)
	if err != nil {
		return err
	}
	if !getConfig.Enabled {
		return fmt.Errorf("cleaner is disabled for host %q; enable it first", host)
	}
	cronExpression, err := normalizedPruneCron(getConfig.CronExpression, getConfig.Interval)
	if err != nil {
		return fmt.Errorf("invalid cleaner schedule for host %q: %w", host, err)
	}
	if getConfig.CronExpression != cronExpression {
		getConfig.CronExpression = cronExpression
		if err := s.store.UpdateConfig(&getConfig); err != nil {
			return fmt.Errorf("persist migrated cleaner schedule for host %q: %w", host, err)
		}
	}

	var jb gocron.Job
	jobDef := gocron.CronJob(cronExpression, false)
	task := gocron.NewTask(s.clean, host)

	val, ok := s.taskList.Load(host)
	if ok {
		if !edit {
			return val.RunNow()
		}

		jb, err = s.schd.Update(val.ID(), jobDef, task)
	} else {
		jb, err = s.schd.NewJob(jobDef, task)
	}
	if err != nil {
		return err
	}

	s.taskList.Store(host, jb)
	return nil
}

// RunScheduledNow executes an enabled cleaner immediately without changing
// its next cron occurrence. If the job was not registered yet (for example
// just after adding a host), it is registered first.
func (s *Service) RunScheduledNow(host string) error {
	if jb, ok := s.taskList.Load(host); ok {
		return jb.RunNow()
	}
	if err := s.RunWithScheduler(host, false); err != nil {
		return err
	}
	jb, ok := s.taskList.Load(host)
	if !ok {
		return fmt.Errorf("cleaner schedule was not created for host %q", host)
	}
	return jb.RunNow()
}

func normalizedPruneCron(expression string, legacyInterval time.Duration) (string, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		if legacyInterval > 0 {
			if legacyInterval < minimumPruneGap {
				legacyInterval = minimumPruneGap
			}
			expression = fmt.Sprintf("@every %s", legacyInterval)
		} else {
			expression = defaultPruneCron
		}
	}
	if len(expression) > 120 || strings.ContainsAny(expression, "\r\n\x00") {
		return "", fmt.Errorf("cron expression is empty, multiline, or too long")
	}
	schedule, err := cron.ParseStandard(expression)
	if err != nil {
		return "", fmt.Errorf("use a standard five-field cron expression: %w", err)
	}
	first := schedule.Next(time.Now())
	second := schedule.Next(first)
	if first.IsZero() || second.IsZero() {
		return "", fmt.Errorf("cron expression has no future execution")
	}
	if second.Sub(first) < minimumPruneGap {
		return "", fmt.Errorf("automatic pruning cannot run more often than once per hour")
	}
	if !strings.HasPrefix(expression, "@") {
		expression = strings.Join(strings.Fields(expression), " ")
	}
	return expression, nil
}

// normalizeDiskUsage removes impossible negative values and replaces Moby's
// misleading image aggregate with a conservative prune estimate derived from
// the verbose image inventory. Moby 29 can report TotalSize as Reclaimable
// when every image is active; summing only the unique bytes of unused images
// matches its legacy client calculation and never promises shared layers that
// an image prune may retain.
func normalizeDiskUsage(usage *client.DiskUsageResult) {
	usage.Images.Reclaimable = conservativeImageReclaimable(usage.Images.Items)
	usage.Images.TotalSize = nonNegative(usage.Images.TotalSize)
	usage.Images.ActiveCount = boundedCount(usage.Images.ActiveCount, usage.Images.TotalCount)
	usage.Images.TotalCount = nonNegative(usage.Images.TotalCount)

	usage.Containers.Reclaimable = nonNegative(usage.Containers.Reclaimable)
	usage.Containers.TotalSize = nonNegative(usage.Containers.TotalSize)
	usage.Containers.ActiveCount = boundedCount(usage.Containers.ActiveCount, usage.Containers.TotalCount)
	usage.Containers.TotalCount = nonNegative(usage.Containers.TotalCount)

	usage.Volumes.Reclaimable = nonNegative(usage.Volumes.Reclaimable)
	usage.Volumes.TotalSize = nonNegative(usage.Volumes.TotalSize)
	usage.Volumes.ActiveCount = boundedCount(usage.Volumes.ActiveCount, usage.Volumes.TotalCount)
	usage.Volumes.TotalCount = nonNegative(usage.Volumes.TotalCount)

	usage.BuildCache.Reclaimable = nonNegative(usage.BuildCache.Reclaimable)
	usage.BuildCache.TotalSize = nonNegative(usage.BuildCache.TotalSize)
	usage.BuildCache.ActiveCount = boundedCount(usage.BuildCache.ActiveCount, usage.BuildCache.TotalCount)
	usage.BuildCache.TotalCount = nonNegative(usage.BuildCache.TotalCount)
}

func conservativeImageReclaimable(images []image.Summary) int64 {
	var total int64
	for _, img := range images {
		// Negative means the daemon did not compute usage. Assume in-use rather
		// than making a destructive promise from incomplete information.
		if img.Containers != 0 || img.Size < 0 || img.SharedSize < 0 {
			continue
		}
		unique := img.Size - img.SharedSize
		if unique <= 0 {
			continue
		}
		if total > math.MaxInt64-unique {
			return math.MaxInt64
		}
		total += unique
	}
	return total
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func boundedCount(active, total int64) int64 {
	total = nonNegative(total)
	active = nonNegative(active)
	if active > total {
		return total
	}
	return active
}

// StopScheduler removes any scheduled cleaner job for the host. It is a no-op
// when nothing is scheduled, so it is safe to call whenever a config is saved
// with the cleaner disabled.
func (s *Service) StopScheduler(host string) {
	jb, ok := s.taskList.LoadAndDelete(host)
	if !ok {
		return
	}
	if err := s.schd.RemoveJob(jb.ID()); err != nil {
		s.log.Warn().Err(err).Str("host", host).Msg("failed to remove cleaner job")
	}
}

func (s *Service) clean(ctx context.Context, host string) {
	log.Debug().Msg("running automated docker cleaner")

	result := PruneResult{
		Host: host,
	}

	pruneConfig, err := s.store.GetConfig(host)
	if err != nil {
		s.log.Err(err).Msg("failed to get docker config")
		result.Err = err.Error()
		if storeErr := s.store.AddResult(&result); storeErr != nil {
			s.log.Err(storeErr).Msg("Failed to add cleaner error result")
		}
		if s.notify != nil {
			s.notify(ctx, result, true)
		}
		return
	}

	if !pruneConfig.Enabled {
		return
	}
	defer func() {
		if err := s.store.AddResult(&result); err != nil {
			s.log.Err(err).Msg("Failed to add result for cleaner")
		}
		if s.notify != nil {
			s.notify(ctx, result, true)
		}
	}()

	cli, err := s.cli(host)
	if err != nil {
		result.Err = err.Error()
		return
	}

	s.Prune(ctx, &pruneConfig, cli.Client, &result)

}

func (s *Service) Prune(
	ctx context.Context,
	opts *PruneConfig,
	cli *client.Client,
	result *PruneResult,
) {
	if opts.Containers {
		result.Containers = s.pruneContainers(ctx, cli)
	}

	if opts.Images {
		result.Images = s.pruneImages(ctx, cli)
	}

	if opts.BuildCache {
		result.BuildCache = s.pruneBuildCache(ctx, cli)
	}

	if opts.Networks {
		result.Networks = s.pruneNetworks(ctx, cli)
	}

	if opts.Volumes {
		result.Volumes = s.pruneVolumes(ctx, cli)
	}
}

func (s *Service) pruneVolumes(ctx context.Context, cli *client.Client) OpResult {
	prune, err := cli.VolumePrune(ctx, client.VolumePruneOptions{
		All: true,
	})
	var res OpResult
	if err != nil {
		res.Err = err.Error()
	} else {
		res.Success = fmt.Sprintf(
			"Deleted Volumes: %d\nReclaimed: %s",
			len(prune.Report.VolumesDeleted),
			humanize.Bytes(prune.Report.SpaceReclaimed),
		)
	}

	return res
}

func (s *Service) pruneNetworks(ctx context.Context, cli *client.Client) OpResult {
	networkReport, err := cli.NetworkPrune(ctx, client.NetworkPruneOptions{})

	var res OpResult
	if err != nil {
		res.Err = err.Error()
	} else {
		res.Success = fmt.Sprintf("Deleted Networks: %d", len(networkReport.Report.NetworksDeleted))
	}

	return res
}

func (s *Service) pruneBuildCache(ctx context.Context, cli *client.Client) OpResult {
	buildCacheOpts := client.BuildCachePruneOptions{
		// The storage card reports all unused cache records. Use the matching
		// prune scope instead of deleting dangling cache only.
		All: true,
	}
	rep, err := cli.BuildCachePrune(ctx, buildCacheOpts)

	var res OpResult
	if err != nil {
		res.Err = err.Error()
	} else {
		buildCacheReport := rep.Report
		res.Success = fmt.Sprintf(
			"Deleted Build Cache: %d\nReclaimed: %s\n",
			len(buildCacheReport.CachesDeleted),
			humanize.Bytes(buildCacheReport.SpaceReclaimed),
		)
	}

	return res
}

func (s *Service) pruneImages(ctx context.Context, cli *client.Client) OpResult {
	imageFilters := client.Filters{}
	// all unused images
	imageFilters.Add("dangling", "false")

	imageReport, err := cli.ImagePrune(ctx, client.ImagePruneOptions{
		Filters: imageFilters,
	})
	var res OpResult
	if err != nil {
		res.Err = err.Error()
	} else {
		res.Success = fmt.Sprintf(
			"Deleted Images: %d\nReclaimed: %s\n",
			len(imageReport.Report.ImagesDeleted),
			humanize.Bytes(imageReport.Report.SpaceReclaimed),
		)
	}

	return res
}

func (s *Service) pruneContainers(ctx context.Context, cli *client.Client) OpResult {
	containerReport, err := cli.ContainerPrune(ctx, client.ContainerPruneOptions{})
	var res OpResult
	if err != nil {
		res.Err = err.Error()
	} else {
		res.Success = fmt.Sprintf(
			"Deleted Containers: %d\nReclaimed: %s\n",
			len(containerReport.Report.ContainersDeleted),
			humanize.Bytes(containerReport.Report.SpaceReclaimed),
		)
	}

	return res
}
