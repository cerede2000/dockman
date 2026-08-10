package docker

import (
	"bufio"
	"cmp"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	v1 "github.com/RA341/dockman/generated/docker/v1"
	dockerpc "github.com/RA341/dockman/generated/docker/v1/v1connect"
	"github.com/RA341/dockman/internal/docker/compose"
	contSrv "github.com/RA341/dockman/internal/docker/container"
	hm "github.com/RA341/dockman/internal/host/middleware"
	"github.com/RA341/dockman/pkg/fileutil"
	"github.com/RA341/dockman/pkg/listutils"

	"connectrpc.com/connect"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
	"github.com/rs/zerolog/log"
)

// ServiceProvider use a closure instead of passing a concrete Service to change hosts on demand
type ServiceProvider func(host string) (*Service, error)

type Handler struct {
	srv ServiceProvider

	// composeStateOverrides records successful state-changing actions whose
	// result is more authoritative than residual Docker labels. In particular,
	// a daemon restart can leave a one-off/orphan carrying config_files after
	// `docker compose down` has removed the actual project. The next up/start/
	// restart for that stack clears the override.
	composeStateMu sync.RWMutex
	composeStopped map[composeStackKey]int64
}

type composeStackKey struct {
	host string
	file string
}

func NewConnectHandler(srv ServiceProvider) (string, http.Handler) {
	h := &Handler{
		srv:            srv,
		composeStopped: make(map[composeStackKey]int64),
	}
	return dockerpc.NewDockerServiceHandler(h)
}

type HostGetter interface {
	GetHost() string
}

func (h *Handler) getHost(ctx context.Context) (string, *Service, error) {
	hostname, err := hm.GetHost(ctx)
	if err != nil {
		return "", nil, err
	}

	dkSrv, err := h.srv(hostname)
	if err != nil {
		return "", nil, err
	}

	return hostname, dkSrv, nil
}

////////////////////////////////////////////
// 			Compose Actions 			  //
////////////////////////////////////////////

func (h *Handler) ComposeFileStatus(ctx context.Context, c *connect.Request[v1.ComposeFileStatusRequest]) (*connect.Response[v1.ComposeFileStatusResponse], error) {
	finalResults := make(map[string]*v1.Status, len(c.Msg.Files))

	err := h.WithClient(ctx, func(dkSrv *Service) error {
		// One container listing for the whole host, aggregated per compose file
		// via the compose config-files label. This replaces one `docker compose
		// ps` subprocess per stack, so reporting the status of every stack (even
		// collapsed ones) stays cheap no matter how many there are.
		containers, err := dkSrv.Container.ContainersList(ctx)
		if err != nil {
			return err
		}

		byFile, primaryFiles := buildComposeStatusIndex(containers)

		for _, file := range c.Msg.Files {
			// The discovered index below overlays live states. Anything requested
			// but absent from Docker is a stopped stack; no per-file filesystem lookup
			// is needed. Resolving the alias to its absolute path is an in-memory
			// operation and avoids a Compose subprocess or filesystem stat per stack.
			finalResults[file] = &v1.Status{}
			absPath, pathErr := dkSrv.Compose.ComposeAbsPath(file)
			if pathErr != nil {
				log.Debug().Err(pathErr).Str("compose_file", file).
					Msg("unable to resolve requested compose file status path")
				continue
			}
			absPath = normalizeComposeConfigPath(absPath, "")
			status := byFile[absPath]
			if status == nil {
				continue
			}
			if stoppedAt, stopped := h.composeStoppedAt(dkSrv.Host, file); stopped {
				if hasComposeContainerCreatedSince(containers, absPath, stoppedAt) {
					h.setComposeStopped(dkSrv.Host, file, false)
				} else {
					continue
				}
			}
			finalResults[file] = status.toProto()
		}

		// Explicitly requested files above include stopped stacks that still
		// exist on disk. Docker labels also reveal deployed stacks at every tree
		// depth, but containers may outlive a deleted/renamed compose file. Keep
		// only paths that still exist so stale Docker metadata cannot pollute a
		// parent folder aggregate.
		for absPath := range primaryFiles {
			if file := dkSrv.Compose.DockmanPath(absPath); file != "" {
				if stoppedAt, stopped := h.composeStoppedAt(dkSrv.Host, file); stopped {
					if hasComposeContainerCreatedSince(containers, absPath, stoppedAt) {
						h.setComposeStopped(dkSrv.Host, file, false)
					} else {
						finalResults[file] = &v1.Status{}
						continue
					}
				}
				exists, statErr := dkSrv.Compose.ComposeFileExists(file)
				if statErr != nil {
					// A transient filesystem error must not make a live stack vanish.
					// Retain it for this snapshot and retry on the next normal poll.
					log.Debug().Err(statErr).Str("compose_file", file).
						Msg("unable to verify Docker-discovered compose file")
				} else if !exists {
					continue
				}
				finalResults[file] = byFile[absPath].toProto()
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&v1.ComposeFileStatusResponse{
		Status: finalResults,
	}), nil
}

// stackStatus aggregates the container states of a single compose stack.
//
// It maps onto the v1.Status fields the UI already consumes. ServicesDown is the
// number of non-running services. ServicesUnHealthy carries observable failures
// (unhealthy/dead/restarting/removing). An exited code alone cannot distinguish
// a crash from a user-requested stop — SIGTERM commonly produces 137/143 — and
// must therefore remain a neutral stopped state, consistent with Monitor.
type stackStatus struct {
	up        int32
	down      int32
	healthy   int32
	unhealthy int32
}

// buildComposeStatusIndex is deliberately linear in the container count. It
// performs no filesystem access and no compose subprocess, and resolves each
// stack path only once later in ComposeFileStatus.
func buildComposeStatusIndex(containers []container.Summary) (map[string]*stackStatus, map[string]struct{}) {
	byFile := make(map[string]*stackStatus)
	primaryFiles := make(map[string]struct{})
	for i := range containers {
		ct := containers[i]
		// `docker compose run` containers do not represent a declared service
		// state and can survive a daemon/project lifecycle. Counting one keeps a
		// stack green after its actual services have been taken down.
		if strings.EqualFold(ct.Labels[api.OneoffLabel], "true") {
			continue
		}
		cfg := ct.Labels[api.ConfigFilesLabel]
		if cfg == "" {
			continue
		}

		first := ""
		for _, path := range strings.Split(cfg, ",") {
			path = normalizeComposeConfigPath(path, ct.Labels[api.WorkingDirLabel])
			if path == "" {
				continue
			}
			if first == "" {
				first = path
			}
			status := byFile[path]
			if status == nil {
				status = &stackStatus{}
				byFile[path] = status
			}
			status.add(ct)
		}
		if first != "" {
			primaryFiles[first] = struct{}{}
		}
	}
	return byFile, primaryFiles
}

// normalizeComposeConfigPath mirrors Docker Compose's config-files label
// semantics. Recent Compose versions may retain a relative -f value and expose
// its absolute base separately through project.working_dir. Canonicalising both
// forms prevents a healthy stack from becoming "stopped" solely because
// "./compose.yml", "compose.yml" and the absolute requested path differ.
func normalizeComposeConfigPath(configFile, workingDir string) string {
	configFile = filepath.ToSlash(strings.TrimSpace(configFile))
	workingDir = filepath.ToSlash(strings.TrimSpace(workingDir))
	if configFile == "" {
		return ""
	}
	if !filepath.IsAbs(configFile) && workingDir != "" {
		configFile = filepath.Join(workingDir, configFile)
	}
	return filepath.ToSlash(filepath.Clean(configFile))
}

func (h *Handler) setComposeStopped(host, file string, stopped bool) {
	key := composeStackKey{host: host, file: file}
	h.composeStateMu.Lock()
	defer h.composeStateMu.Unlock()
	if stopped {
		h.composeStopped[key] = time.Now().Unix()
		return
	}
	delete(h.composeStopped, key)
}

func (h *Handler) composeStoppedAt(host, file string) (int64, bool) {
	h.composeStateMu.RLock()
	defer h.composeStateMu.RUnlock()
	stoppedAt, stopped := h.composeStopped[composeStackKey{host: host, file: file}]
	return stoppedAt, stopped
}

func hasComposeContainerCreatedSince(containers []container.Summary, composeFile string, since int64) bool {
	composeFile = normalizeComposeConfigPath(composeFile, "")
	for i := range containers {
		ct := containers[i]
		// Docker exposes Created with one-second precision. Require a strictly
		// newer timestamp so a fast Up -> Down in the same second cannot make the
		// just-stopped container clear its own authoritative stopped marker.
		if ct.Created <= since || strings.EqualFold(ct.Labels[api.OneoffLabel], "true") {
			continue
		}
		for _, path := range strings.Split(ct.Labels[api.ConfigFilesLabel], ",") {
			path = normalizeComposeConfigPath(path, ct.Labels[api.WorkingDirLabel])
			if path == composeFile {
				return true
			}
		}
	}
	return false
}

func (s *stackStatus) add(ct container.Summary) {
	switch string(ct.State) {
	case "running":
		s.up++
		if ct.Health != nil {
			switch ct.Health.Status {
			case container.Healthy:
				s.healthy++
			case container.Unhealthy:
				s.unhealthy++
			}
		}
	case "restarting", "dead", "removing":
		s.down++
		s.unhealthy++
	case "exited", "created", "paused":
		s.down++
	}
}

func (s *stackStatus) toProto() *v1.Status {
	return &v1.Status{
		ServicesUp:        s.up,
		ServicesDown:      s.down,
		ServicesHealthy:   s.healthy,
		ServicesUnHealthy: s.unhealthy,
	}
}

func (h *Handler) ComposeUp(ctx context.Context, req *connect.Request[v1.ComposeFile], responseStream *connect.ServerStream[v1.LogsMessage]) error {
	return h.WithClientAndStream(ctx, responseStream, func(dkSrv *Service, writer io.Writer) error {
		return withComposeActionLock(dkSrv, req.Msg.Filename, func() error {
			// From this point the real daemon state becomes authoritative again,
			// including partial state when Compose itself returns an error.
			h.setComposeStopped(dkSrv.Host, req.Msg.Filename, false)
			return dkSrv.Compose.Up(
				ctx,
				req.Msg.Filename,
				writer,
				req.Msg.SelectedServices...,
			)
		})
	})
}

func (h *Handler) ComposeStart(ctx context.Context, req *connect.Request[v1.ComposeFile], responseStream *connect.ServerStream[v1.LogsMessage]) error {
	return h.WithClientAndStream(ctx, responseStream, func(dkSrv *Service, writer io.Writer) error {
		return withComposeActionLock(dkSrv, req.Msg.Filename, func() error {
			h.setComposeStopped(dkSrv.Host, req.Msg.Filename, false)
			return dkSrv.Compose.Start(
				ctx,
				req.Msg.Filename,
				writer,
				req.Msg.SelectedServices...,
			)
		})
	})
}

func (h *Handler) ComposeStop(ctx context.Context, req *connect.Request[v1.ComposeFile], responseStream *connect.ServerStream[v1.LogsMessage]) error {
	return h.WithClientAndStream(ctx, responseStream, func(dkSrv *Service, writer io.Writer) error {
		return withComposeActionLock(dkSrv, req.Msg.Filename, func() error {
			return dkSrv.Compose.Stop(
				ctx,
				req.Msg.Filename,
				writer,
				req.Msg.SelectedServices...,
			)
		})
	})
}

func (h *Handler) ComposeDown(ctx context.Context, req *connect.Request[v1.ComposeFile], responseStream *connect.ServerStream[v1.LogsMessage]) error {
	return h.WithClientAndStream(ctx, responseStream, func(dkSrv *Service, writer io.Writer) error {
		return withComposeActionLock(dkSrv, req.Msg.Filename, func() error {
			err := dkSrv.Compose.Down(
				ctx,
				req.Msg.Filename,
				writer,
				req.Msg.SelectedServices...,
			)
			if err == nil && len(req.Msg.SelectedServices) == 0 {
				h.setComposeStopped(dkSrv.Host, req.Msg.Filename, true)
			}
			return err
		})
	})
}

func (h *Handler) ComposeRestart(ctx context.Context, req *connect.Request[v1.ComposeFile], responseStream *connect.ServerStream[v1.LogsMessage]) error {
	return h.WithClientAndStream(ctx, responseStream, func(dkSrv *Service, writer io.Writer) error {
		return withComposeActionLock(dkSrv, req.Msg.Filename, func() error {
			h.setComposeStopped(dkSrv.Host, req.Msg.Filename, false)
			return dkSrv.Compose.Restart(
				ctx,
				req.Msg.Filename,
				writer,
				req.Msg.SelectedServices...,
			)
		})
	})

}

func (h *Handler) ComposeRedeploy(ctx context.Context, req *connect.Request[v1.ComposeRedeployRequest], responseStream *connect.ServerStream[v1.LogsMessage]) error {
	return h.WithClientAndStream(ctx, responseStream, func(dkSrv *Service, writer io.Writer) error {
		file := req.Msg.GetFile()
		return withComposeActionLock(dkSrv, file.GetFilename(), func() error {
			return dkSrv.Compose.Redeploy(
				ctx,
				file.GetFilename(),
				writer,
				req.Msg.GetPull(), req.Msg.GetBuild(), req.Msg.GetRecreate(),
				file.GetSelectedServices()...,
			)
		})
	})
}

func (h *Handler) ComposeUpdate(ctx context.Context, req *connect.Request[v1.ComposeFile], responseStream *connect.ServerStream[v1.LogsMessage]) error {
	return h.WithClientAndStream(ctx, responseStream, func(dkSrv *Service, writer io.Writer) error {
		return withComposeActionLock(dkSrv, req.Msg.Filename, func() error {
			return ComposeSelectiveUpdate(ctx, dkSrv, req.Msg.Filename, writer, req.Msg.SelectedServices...)
		})
	})

	// todo
	//go sendReqToUpdater(h.addr, h.pass, "")
	//return nil
}

func withComposeActionLock(dkSrv *Service, filename string, action func() error) error {
	unlock, ok := compose.TryLockStack(dkSrv.Host, filename)
	if !ok {
		return fmt.Errorf("another action is already running for stack %s", filename)
	}
	defer unlock()
	return action()
}

func (h *Handler) DockerCommand(ctx context.Context, req *connect.Request[v1.DockerCommandRequest], responseStream *connect.ServerStream[v1.LogsMessage]) error {
	return h.WithClientAndStream(ctx, responseStream, func(dkSrv *Service, writer io.Writer) error {
		return dkSrv.Compose.RunDockerCommand(ctx, req.Msg.GetCommand(), writer)
	})
}

func (h *Handler) ComposeValidate(ctx context.Context, req *connect.Request[v1.ComposeFile]) (*connect.Response[v1.ComposeValidateResponse], error) {
	var validationResult []error

	err := h.WithClient(ctx, func(dkSrv *Service) error {
		errs := dkSrv.Compose.Validate(ctx, req.Msg.Filename)
		validationResult = errs
		return nil
	})
	if err != nil {
		return nil, err
	}

	if validationResult == nil {
		return connect.NewResponse(&v1.ComposeValidateResponse{
			Errs: []string{},
		}), nil
	}

	toMap := listutils.ToMap(validationResult, func(t error) string {
		return t.Error()
	})
	return connect.NewResponse(&v1.ComposeValidateResponse{
		Errs: toMap,
	}), nil
}

func (h *Handler) ComposeList(ctx context.Context, req *connect.Request[v1.ComposeFile]) (*connect.Response[v1.ListResponse], error) {
	var result []*v1.ContainerList
	err := h.WithClient(ctx, func(dkSrv *Service) error {
		res, err := dkSrv.Compose.List(
			ctx,
			req.Msg.Filename,
		)
		if err != nil {
			return err
		}

		result, _ = h.containersToRpc(res, dkSrv.Host, dkSrv)
		return nil
	})
	return connect.NewResponse(&v1.ListResponse{List: result}), err
}

func (h *Handler) WithClient(ctx context.Context, runner func(dkSrv *Service) error) error {
	_, dkSrv, err := h.getHost(ctx)
	if err != nil {
		return err
	}

	return runner(dkSrv)
}

func (h *Handler) WithClientAndStream(
	ctx context.Context,
	responseStream *connect.ServerStream[v1.LogsMessage],
	run func(srv *Service, writer io.Writer) error,
) error {
	stream := LogStreamWriter{responseStream: responseStream}
	err := h.WithClient(ctx, func(dkSrv *Service) error {
		return run(dkSrv, &stream)
	})
	if err != nil {
		return err
	}
	return nil
}

////////////////////////////////////////////
// 				Utils 			  		  //
////////////////////////////////////////////

// LogStreamWriter turns an action's text output into stream messages.
//
// The mutex is not decoration: since stacks update in parallel, several
// goroutines write text here while a third reports structured progress, and a
// Connect stream has no lock of its own - concurrent Send calls corrupt it.
type LogStreamWriter struct {
	mu             sync.Mutex
	responseStream *connect.ServerStream[v1.LogsMessage]
}

func (l *LogStreamWriter) Write(p []byte) (n int, err error) {
	if err = l.send(&v1.LogsMessage{Message: string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// SendProgress reports one container's update state alongside the text. A
// failure to send is dropped: progress is an accessory to the update, and
// losing a state must never abort a replacement already under way.
func (l *LogStreamWriter) SendProgress(progress *v1.UpdateProgress) {
	_ = l.send(&v1.LogsMessage{Progress: progress})
}

func (l *LogStreamWriter) send(message *v1.LogsMessage) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.responseStream.Send(message)
}

func ToRPCStat(cont contSrv.Stats) *v1.ContainerStats {
	return &v1.ContainerStats{
		Id:           cont.ID,
		Name:         strings.TrimPrefix(cont.Name, "/"),
		Image:        cont.Image,
		State:        cont.State,
		Health:       cont.Health,
		IpAddress:    cont.IPAddress,
		RestartCount: cont.RestartCount,
		CpuUsage:     cont.CPUUsage,
		MemoryUsage:  cont.MemoryUsage,
		MemoryLimit:  cont.MemoryLimit,
		NetworkRx:    cont.NetworkRx,
		NetworkTx:    cont.NetworkTx,
		BlockRead:    cont.BlockRead,
		BlockWrite:   cont.BlockWrite,
		StartedAt:    cont.StartedAt,
	}
}

func getSortFn(field v1.SORT_FIELD) func(a, b contSrv.Stats) int {
	switch field {
	case v1.SORT_FIELD_CPU:
		return func(a, b contSrv.Stats) int {
			return cmp.Compare(b.CPUUsage, a.CPUUsage)
		}
	case v1.SORT_FIELD_MEM:
		return func(a, b contSrv.Stats) int {
			return cmp.Compare(b.MemoryUsage, a.MemoryUsage)
		}
	case v1.SORT_FIELD_NETWORK_RX:
		return func(a, b contSrv.Stats) int {
			return cmp.Compare(b.NetworkRx, a.NetworkRx)
		}
	case v1.SORT_FIELD_NETWORK_TX:
		return func(a, b contSrv.Stats) int {
			return cmp.Compare(b.NetworkTx, a.NetworkTx)
		}
	case v1.SORT_FIELD_DISK_W:
		return func(a, b contSrv.Stats) int {
			return cmp.Compare(b.BlockWrite, a.BlockWrite)
		}
	case v1.SORT_FIELD_DISK_R:
		return func(a, b contSrv.Stats) int {
			return cmp.Compare(b.BlockRead, a.BlockRead)
		}
	case v1.SORT_FIELD_STARTED:
		return func(a, b contSrv.Stats) int {
			// Compare parsed times, not the raw strings: RFC3339Nano trims
			// trailing zeros, so lexicographic order is wrong across values
			// with and without fractional seconds.
			return parseStarted(b.StartedAt).Compare(parseStarted(a.StartedAt))
		}
	case v1.SORT_FIELD_NAME:
		fallthrough
	default:
		return func(a, b contSrv.Stats) int {
			return cmp.Compare(b.Name, a.Name)
		}
	}
}

// parseStarted parses a container's RFC3339 start time, returning the zero time
// (sorts first) when empty or unparseable (e.g. a never-started container).
func parseStarted(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func sendReqToUpdater(addr, key, path string) {
	log.Debug().Str("addr", addr).Msg("sending request to updating dockman")
	if key != "" && addr != "" {
		addr = strings.TrimSuffix(addr, "/")
		addr = fmt.Sprintf("%s/update", addr) // Remove key from URL path

		formData := url.Values{}
		formData.Set("composeFile", path)

		req, err := http.NewRequest("POST", addr, strings.NewReader(formData.Encode()))
		if err != nil {
			log.Warn().Err(err).Str("addr", addr).Msg("unable to create request")
			return
		}

		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Authorization", key) // Add key as header

		httpclient := &http.Client{}
		if _, err = httpclient.Do(req); err != nil {
			log.Warn().Err(err).Str("addr", addr).Msg("unable to send request to updater")
			return
		}
	}
}

func streamManager(streamFn func(val string) error) (*io.PipeWriter, *sync.WaitGroup) {
	pipeReader, pipeWriter := io.Pipe()
	wg := sync.WaitGroup{}
	// Start a goroutine that reads from the pipe, splits the data into lines,
	// and sends each line as a message on the response stream.
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer fileutil.Close(pipeReader)

		scanner := bufio.NewScanner(pipeReader)
		for scanner.Scan() {
			err := streamFn(fmt.Sprintf("%s\r\n", scanner.Text()))
			if err != nil {
				log.Warn().Err(err).Msg("Failed to send message to stream")
			}
		}
		// If the scanner stops because of an error, log it.
		if err := scanner.Err(); err != nil {
			log.Error().Err(err).Msg("Error reading from pipe for streaming")
		}
	}()

	return pipeWriter, &wg
}

func toRPCPort(p container.PortSummary) *v1.Port {
	return &v1.Port{
		Public:  int32(p.PublicPort),
		Private: int32(p.PrivatePort),
		Host:    p.IP.String(),
		Type:    p.Type,
	}
}

type ContainerLogWriter struct {
	responseStream *connect.ServerStream[v1.LogsMessage]
}

func (l *ContainerLogWriter) Write(p []byte) (n int, err error) {
	msg := &v1.LogsMessage{Message: string(p)}
	if err = l.responseStream.Send(msg); err != nil {
		return 0, err
	}
	return len(p), nil
}
