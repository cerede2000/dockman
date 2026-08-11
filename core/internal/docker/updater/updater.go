package updater

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	containerSrv "github.com/RA341/dockman/internal/docker/container"
	"github.com/RA341/dockman/pkg/fileutil"

	"github.com/google/uuid"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"
)

// dockerClient is the slice of the Docker client this package drives. It
// exists so that ContainerRecreateWithOptions — the code that stops, rebuilds
// and destroys production containers, and the only place where a mistake is
// unrecoverable — can be exercised without a daemon. *client.Client satisfies
// it as written.
type dockerClient interface {
	ContainerList(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error)
	ContainerInspect(ctx context.Context, containerID string, options client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(ctx context.Context, containerID string, options client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerStop(ctx context.Context, containerID string, options client.ContainerStopOptions) (client.ContainerStopResult, error)
	ContainerRemove(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	ContainerRename(ctx context.Context, containerID string, options client.ContainerRenameOptions) (client.ContainerRenameResult, error)
	ImageList(ctx context.Context, options client.ImageListOptions) (client.ImageListResult, error)
	ImageInspect(ctx context.Context, imageID string, inspectOpts ...client.ImageInspectOption) (client.ImageInspectResult, error)
}

type Service struct {
	srv            *containerSrv.Service
	hostname       string
	dockmanUpdater string
	Store          Store

	// Test seams. Both are nil in production, where the real Docker client and
	// the process-wide events hub are used.
	client    dockerClient
	subscribe func() (<-chan containerSrv.Event, func())
}

func New(
	srv *containerSrv.Service,
	hostname string,
	url string,
	store Store,
) *Service {
	return &Service{
		srv:            srv,
		hostname:       hostname,
		dockmanUpdater: url,
		Store:          store,
	}
}

// access to the raw docker client
func (u *Service) cli() dockerClient {
	if u.client != nil {
		return u.client
	}
	return u.srv.Client
}

// events subscribes to this host's container events through the shared hub.
func (u *Service) events() (<-chan containerSrv.Event, func()) {
	if u.subscribe != nil {
		return u.subscribe()
	}
	return u.srv.SubscribeEvents()
}

// The update engine is ForceUpdateContainer / ContainersForceUpdate over
// ContainerRecreateWithOptions. A second, older loop used to sit here -
// containersUpdateLoop and four entry points into it - with no caller anywhere
// and three behaviours this one was written to avoid: it pruned every dangling
// image on each pass, which is exactly where a retagged :latest leaves the
// rollback image the retention policy is meant to keep; it swallowed pull and
// recreate failures and returned success; and its Dockman self-update branch
// was an empty closure. It has been removed rather than left looking supported.

// ImagePuller pulls an image tag; injected so the caller decides HOW to
// pull (the compose CLI runner carries the host's registry credentials,
// unlike the bare daemon API).
type ImagePuller func(ctx context.Context, imageTag string) error

type ForceUpdateOptions struct {
	VerifyHealth   bool
	ImagePrepared  bool
	ImageReference string
	// Report receives structured per-container progress. Nil on every path
	// that has no stream to report to, which is most of them.
	Report ProgressReporter
}

type ForceUpdateResult struct {
	ContainerID   string
	ContainerName string
	Image         string
	PreviousImage string
	NewImage      string
	Updated       bool
}

// RestoreContainerImage transactionally recreates the current container with
// a previously recorded local image ID. It intentionally performs no registry
// access: stack rollback must remain possible even when the registry or the new
// tag is broken. The returned ID is the restored replacement container ID.
func (u *Service) RestoreContainerImage(ctx context.Context, containerName, previousImageID string, verifyHealth bool) (string, error) {
	containerName = strings.TrimSpace(strings.TrimPrefix(containerName, "/"))
	previousImageID = strings.TrimSpace(previousImageID)
	if containerName == "" || previousImageID == "" {
		return "", errors.New("container name and previous image id are required for rollback")
	}
	filters := client.Filters{}
	// The daemon treats the name filter as a regular expression, so a dot or
	// a plus in a container name would silently match its neighbours.
	filters.Add("name", "^/"+regexp.QuoteMeta(containerName)+"$")
	list, err := u.cli().ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return "", fmt.Errorf("locate container %s for rollback: %w", containerName, err)
	}
	if len(list.Items) != 1 {
		return "", fmt.Errorf("locate container %s for rollback: expected one container, found %d", containerName, len(list.Items))
	}
	if err := u.ContainerRecreateWithOptions(ctx, previousImageID, list.Items[0], verifyHealth); err != nil {
		return "", fmt.Errorf("restore container %s to %s: %w", containerName, previousImageID, err)
	}
	list, err = u.cli().ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil || len(list.Items) != 1 {
		if err == nil {
			err = fmt.Errorf("expected one restored container, found %d", len(list.Items))
		}
		return "", fmt.Errorf("verify restored container %s: %w", containerName, err)
	}
	return list.Items[0].ID, nil
}

func newestImage(items []image.Summary) string {
	best := ""
	var bestCreated int64
	for _, item := range items {
		if item.ID == "" {
			continue
		}
		if best == "" || item.Created > bestCreated || (item.Created == bestCreated && item.ID > best) {
			best, bestCreated = item.ID, item.Created
		}
	}
	return best
}

// shortContainerID is the display form used in logs. Slicing to twelve
// characters outright panics on anything shorter, which a malformed or
// truncated id from an intermediary would be.
func shortContainerID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// RolledBackError marks a failed replacement for which Dockman successfully
// restored the previous running container. Callers can distinguish this safe
// outcome from a failure that requires manual recovery.
type RolledBackError struct{ Err error }

func (e *RolledBackError) Error() string {
	return "update failed, rolled back to previous version: " + e.Err.Error()
}
func (e *RolledBackError) Unwrap() error { return e.Err }

func IsRolledBack(err error) bool {
	var rolledBack *RolledBackError
	return errors.As(err, &rolledBack)
}

// ContainersForceUpdate pulls each container's image tag and recreates the
// container when the pull brought down a different image. Unlike the
// metadata-driven update loop it needs no registry digest lookup, and it
// reports failures instead of skipping silently — this backs the explicit
// per-container Update action in the UI. Progress lines go to out, and
// report - when the caller has a stream for it - receives the same progress
// as structured per-container states.
func (u *Service) ContainersForceUpdate(ctx context.Context, pull ImagePuller, out io.Writer, report ProgressReporter, containerID ...string) error {
	list, err := u.srv.ContainerListByIDs(ctx, containerID...)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return fmt.Errorf("no containers found for the given ids")
	}

	groups := groupContainersByStack(list)
	// One stack keeps the original path untouched: same order, same output,
	// no prefix, no goroutine. Only a batch that actually spans several stacks
	// pays for the parallel machinery.
	// Everything accepted is announced before any work starts, so a view can
	// show the whole batch as pending instead of filling in one row at a time.
	for _, group := range groups {
		for _, cur := range group.containers {
			reportStage(report, cur, StageQueued, "")
		}
	}
	if len(groups) == 1 {
		return u.forceUpdateStack(ctx, pull, out, report, groups[0].containers)
	}

	var (
		mu      sync.Mutex
		workers errgroup.Group
	)
	workers.SetLimit(maxParallelStackUpdates)
	errs := make([]error, len(groups))
	for index, group := range groups {
		workers.Go(func() error {
			writer := newStackLogWriter(&mu, out, group.key)
			defer writer.Flush()
			errs[index] = u.forceUpdateStack(ctx, pull, writer, report, group.containers)
			// Errors are collected per stack rather than returned here: one
			// stack failing must not cancel the others, which are independent
			// and may already be half-way through a replacement.
			return nil
		})
	}
	_ = workers.Wait()
	return errors.Join(errs...)
}

// forceUpdateStack replaces one stack's containers one after another. The
// order is the caller's, which is the order Compose reported them in, and it
// matters: recreating a database under the application that depends on it is
// exactly what running a stack in parallel with itself would do.
func (u *Service) forceUpdateStack(ctx context.Context, pull ImagePuller, out io.Writer, report ProgressReporter, containers []container.Summary) error {
	var errs []error
	for _, cur := range containers {
		// This is the manual update path behind the Monitor and container
		// views. It drives the Docker API through whatever connection Dockman
		// holds - which, for a socket proxy, is the container being replaced:
		// after ContainerStop the connection is gone and not even the rollback
		// can be reached. Refusing here rather than in the interface means no
		// caller can reach that state by mistake.
		if err := guardProtectedInfrastructure(&cur); err != nil {
			reportStage(report, cur, StageFailed, err.Error())
			errs = append(errs, err)
			continue
		}
		if _, err := u.forceUpdateContainer(ctx, pull, out, cur, ForceUpdateOptions{VerifyHealth: true, Report: report}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ForceUpdateContainer updates one explicit container and returns a structured
// result suitable for persistent automatic-update history.
func (u *Service) ForceUpdateContainer(ctx context.Context, pull ImagePuller, out io.Writer, containerID string, options ForceUpdateOptions) (ForceUpdateResult, error) {
	list, err := u.srv.ContainerListByIDs(ctx, containerID)
	if err != nil {
		return ForceUpdateResult{}, err
	}
	if len(list) != 1 {
		return ForceUpdateResult{}, fmt.Errorf("container %q was not found", containerID)
	}
	return u.forceUpdateContainer(ctx, pull, out, list[0], options)
}

func (u *Service) forceUpdateContainer(ctx context.Context, pull ImagePuller, out io.Writer, cur container.Summary, options ForceUpdateOptions) (ForceUpdateResult, error) {
	name, imgTag := summaryName(cur), forceUpdateImageReference(cur, options)
	result := ForceUpdateResult{ContainerID: cur.ID, ContainerName: name, Image: imgTag, PreviousImage: cur.ImageID}
	report := func(format string, args ...any) { _, _ = fmt.Fprintf(out, format+"\r\n", args...) }
	if imgTag == "" || strings.HasPrefix(imgTag, "sha256:") {
		err := fmt.Errorf("%s: image %q has no pullable tag", name, imgTag)
		report("%s: image %q has no pullable tag (locally built?), skipping", name, imgTag)
		reportStage(options.Report, cur, StageFailed, err.Error())
		return result, err
	}
	if options.ImagePrepared {
		report("Using preloaded image %s for %s...", imgTag, name)
	} else {
		reportStage(options.Report, cur, StagePulling, imgTag)
		report("Pulling %s for %s...", imgTag, name)
		if err := pull(ctx, imgTag); err != nil {
			report("%s: pull failed: %v", name, err)
			reportStage(options.Report, cur, StageFailed, err.Error())
			return result, fmt.Errorf("%s: pull %s: %w", name, imgTag, err)
		}
	}
	localImages, err := u.cli().ImageList(ctx, client.ImageListOptions{Filters: client.Filters{}.Add("reference", imgTag)})
	if err != nil {
		reportStage(options.Report, cur, StageFailed, err.Error())
		return result, fmt.Errorf("%s: inspect %s: %w", name, imgTag, err)
	}
	// A reference filter can match several images - an untagged digest
	// reference in particular - and the daemon guarantees no ordering. Taking
	// the first item made NewImage, and therefore PreviousImage and the image
	// cleanup that follows it, depend on which row the daemon happened to
	// return first. Newest wins, with the id as a deterministic tie-break.
	if newest := newestImage(localImages.Items); newest != "" {
		result.NewImage = newest
	}
	if result.NewImage == "" || result.NewImage == cur.ImageID {
		report("%s: image already up to date, container kept as is", name)
		reportStage(options.Report, cur, StageUpToDate, "")
		return result, nil
	}
	reportStage(options.Report, cur, StageRecreating, imgTag)
	report("Recreating %s on the new image...", name)
	onVerify := func() { reportStage(options.Report, cur, StageVerifying, "") }
	if err := u.ContainerRecreateWithOptions(ctx, imgTag, cur, options.VerifyHealth, onVerify); err != nil {
		report("%s: recreate failed: %v", name, err)
		// A rollback that worked is a different outcome from a container left
		// needing manual recovery, and the view has to be able to tell them
		// apart at a glance.
		stage := StageFailed
		if IsRolledBack(err) {
			stage = StageRolledBack
		}
		reportStage(options.Report, cur, stage, err.Error())
		return result, fmt.Errorf("%s: recreate: %w", name, err)
	}
	result.Updated = true
	report("%s updated successfully", name)
	reportStage(options.Report, cur, StageUpdated, result.NewImage)
	return result, nil
}

// forceUpdateImageReference keeps the registry reference captured by the
// update scan authoritative for a preloaded transaction. Pulling `:latest`
// moves that tag to the new image, so a fresh Docker container listing may
// expose the old running image only as sha256:<id>. That digest is rollback
// material, not evidence that the Compose service is locally built.
func forceUpdateImageReference(cur container.Summary, options ForceUpdateOptions) string {
	if options.ImagePrepared {
		if prepared := strings.TrimSpace(options.ImageReference); prepared != "" {
			return prepared
		}
	}
	return strings.TrimSpace(cur.Image)
}

//////////////////////////////////////////////
// update guards and utils

// DockmanOptInUpdateLabel lets a container that exposes the Docker socket opt
// into being updated through it anyway.
const DockmanOptInUpdateLabel = "dockman.update"

const DockmanUpdateDisableLabel = "dockman.update.disable"

// hasDisableUpdateLabel treats anything Dockman cannot read as "disabled".
// An exact "true" comparison silently ignored dockman.update.disable=yes, so
// the operator who wrote it watched the container get updated anyway.
// HasDisableUpdateLabel is the exported form used by the execution path.
func HasDisableUpdateLabel(c *container.Summary) bool { return hasDisableUpdateLabel(c) }

func hasDisableUpdateLabel(c *container.Summary) bool {
	disabled, present, valid := parseBoolLabel(c.Labels, DockmanUpdateDisableLabel)
	return present && (!valid || disabled)
}

const DockmanContainerLabel = "dockman.container"

// The label is set on the Dockman image, so it normally reads "true". It can
// still be overridden in a Compose file or in a modified image, and the two
// questions asked of it want opposite treatment of a value that cannot be
// read - which is why they are two functions.
//
// MarksDockmanContainer answers "must this be kept away from an ordinary
// update?". Getting that wrong means recreating Dockman through the API it is
// itself serving, so anything unreadable counts as yes. An exact "true"
// comparison silently failed open on dockman.container=1, the opposite of how
// dockman.update.disable has always been read.
func MarksDockmanContainer(cont *container.Summary) bool {
	if cont == nil {
		return false
	}
	marked, present, valid := parseBoolLabel(cont.Labels, DockmanContainerLabel)
	return present && (!valid || marked)
}

// IdentifiesDockmanContainer answers "is this the container to restart?".
// Getting that wrong means recreating somebody else's container, so only an
// unambiguous value counts and anything unreadable is refused. The self-update
// helper labels itself false precisely so this never picks it.
func IdentifiesDockmanContainer(labels map[string]string) bool {
	marked, present, valid := parseBoolLabel(labels, DockmanContainerLabel)
	return present && valid && marked
}

func hasDockmanLabel(cont *container.Summary) bool {
	return MarksDockmanContainer(cont)
}

// SourceProtectedInfrastructure marks a container that carries the daemon
// socket. Kept distinct from Dockman's own "protected" classification: Dockman
// has a dedicated self-update action, while these are precisely the containers
// the detached protected update exists for.
const SourceProtectedInfrastructure = "protected-infra"

// dockerSocketPaths are the daemon socket locations a container must not be
// able to take away from Dockman in the middle of an update.
var dockerSocketPaths = []string{"/var/run/docker.sock", "/run/docker.sock"}

// ExposesDockerSocket reports whether the daemon socket is bound into this
// container. Recreating one of those through Dockman means severing the very
// connection Dockman is driving the update with: once ContainerStop has run,
// not even the rollback path can be reached, and the host is left with the
// socket proxy down and the old container stopped.
//
// The answer comes from the summary already in hand, so the classification
// costs no Docker call. It is deliberately placed after the explicit update
// labels, so an operator who knows what they are doing keeps the final say.
// guardProtectedInfrastructure refuses an API-driven update of a container
// that carries the daemon socket. The explicit opt-in label remains the way to
// say "I know what this is, do it anyway".
func guardProtectedInfrastructure(cont *container.Summary) error {
	// Dockman recreating itself through the Docker API cannot work: the process
	// dies on its own ContainerStop, before the replacement is created, so
	// there is no rollback and no replacement - just a stopped container and a
	// host that needs a manual `docker start`. The dedicated self-update action
	// exists precisely because this sequence has to survive the process ending,
	// and the label is how Dockman recognises itself.
	//
	// The socket check below does not cover this: it only catches a container
	// that bind-mounts the daemon socket, and a Dockman reaching its daemon
	// through a socket proxy mounts nothing.
	if hasDockmanLabel(cont) {
		return fmt.Errorf("%s is Dockman itself and cannot recreate its own container through the Docker API: use the Dockman update action, which hands the work to a detached helper", summaryName(*cont))
	}
	if !ExposesDockerSocket(cont) {
		return nil
	}
	if _, optIn := cont.Labels[DockmanOptInUpdateLabel]; optIn {
		return nil
	}
	return fmt.Errorf("%s exposes the Docker socket and cannot be updated through it: use the protected update on the Updates page, which runs from a detached helper that survives Dockman losing its Docker connection, or set %s=true to override", summaryName(*cont), DockmanOptInUpdateLabel)
}

// ProtectedFromAPIReplacement reports a container Dockman must not replace
// through its own Docker connection: itself, or infrastructure that connection
// runs on. Callers that can hand the work to Compose instead should route it
// there rather than fail, which is what the Deploy tab does.
func ProtectedFromAPIReplacement(cont *container.Summary) bool {
	return guardProtectedInfrastructure(cont) != nil
}

func ExposesDockerSocket(cont *container.Summary) bool {
	for _, mountPoint := range cont.Mounts {
		for _, socket := range dockerSocketPaths {
			if path.Clean(mountPoint.Source) == socket || path.Clean(mountPoint.Destination) == socket {
				return true
			}
		}
	}
	return false
}

func summaryName(cont container.Summary) string {
	if len(cont.Names) == 0 {
		if len(cont.ID) > 12 {
			return cont.ID[:12]
		}
		return cont.ID
	}
	return strings.TrimPrefix(cont.Names[0], "/")
}

// UpdateDockman updates a running dockman container
// by pinging the sidecar updater service
//
// containerID is the id of the current dockman container
func UpdateDockman(containerID, updaterUrl string) error {
	fullUrl := fmt.Sprintf("%s/%s", updaterUrl, containerID)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fullUrl, nil)
	if err != nil {
		return fmt.Errorf("unable to create updater request: %w", err)
	}
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("unable to send request updater: %w", err)
	}
	defer fileutil.Close(resp.Body)
	return nil
}

// ContainerRecreate swaps a container onto a (freshly pulled) image while
// keeping its whole configuration. A running container is replaced through a
// create-start-healthcheck-swap sequence with rollback to the old container
// on any failure; a stopped one is swapped in place and left stopped.
func (u *Service) ContainerRecreate(ctx context.Context, imageTag string, oldContainer container.Summary) error {
	return u.ContainerRecreateWithOptions(ctx, imageTag, oldContainer, true)
}

// onVerify is variadic so the existing call sites - stack rollback, the
// transaction path - stay exactly as they are; only the callers that report
// progress pass one.
// temporaryReplacementName is the name the replacement is built under before
// it takes the real one.
//
// It used to be the container's name plus "_updated" - the same string on
// every attempt. When a compensating removal failed (a busy daemon, a
// container stuck in Removing), the leftover stayed and every later attempt
// collided with it on creation, so the service could never be updated again.
// A suffix unique to the attempt cannot collide with anything; it also names
// Dockman in plain sight, so an operator finding one knows where it came from.
func temporaryReplacementName(containerName string) string {
	return containerName + ".dockman-update-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
}

func (u *Service) ContainerRecreateWithOptions(ctx context.Context, imageTag string, oldContainer container.Summary, verifyHealth bool, onVerify ...func()) error {
	containerName := "untagged"
	if len(oldContainer.Names) > 0 {
		containerName = strings.TrimPrefix(oldContainer.Names[0], "/")
	}
	replacementName := temporaryReplacementName(containerName)
	log.Debug().Msgf("Recreating container %s (%s) on image %s", containerName, shortContainerID(oldContainer.ID), imageTag)

	inspected, err := u.cli().ContainerInspect(ctx, oldContainer.ID, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("failed to inspect container %s: %w", containerName, err)
	}
	inspectedData := inspected.Container

	wasRunning := inspectedData.State != nil && inspectedData.State.Running

	// A stopped container still gets a create-before-remove swap. Creating the
	// replacement under a temporary name validates the image, networks and full
	// configuration while the original container remains recoverable.
	if !wasRunning {
		newContainer, createErr := u.containerCreate(ctx, imageTag, replacementName, inspectedData)
		if createErr != nil {
			return fmt.Errorf("failed to create replacement for stopped container %s; original container was preserved: %w", containerName, createErr)
		}
		if _, err := u.cli().ContainerRemove(ctx, oldContainer.ID, client.ContainerRemoveOptions{}); err != nil {
			cleanupCtx, cancel := rollbackContext(ctx)
			defer cancel()
			if _, cleanupErr := u.cli().ContainerRemove(cleanupCtx, newContainer.ID, client.ContainerRemoveOptions{Force: true}); cleanupErr != nil {
				log.Warn().Err(cleanupErr).Str("container", replacementName).Msg("failed to clean up replacement after preserving stopped container")
				return fmt.Errorf("failed to remove old container %s, and the replacement container %s could not be removed and is still on the host: %w", containerName, replacementName, err)
			}
			return fmt.Errorf("failed to remove old container %s: %w", containerName, err)
		}
		renameCtx, cancel := rollbackContext(ctx)
		defer cancel()
		if _, err := u.cli().ContainerRename(renameCtx, newContainer.ID, client.ContainerRenameOptions{NewName: containerName}); err != nil {
			return fmt.Errorf("replacement container %s was created safely but could not take its final name (currently %s): %w", containerName, replacementName, err)
		}
		return nil
	}

	if _, err := u.cli().ContainerStop(ctx, oldContainer.ID, client.ContainerStopOptions{}); err != nil {
		// The only step of the sequence that used to give up without trying to
		// put the service back. A stop that reports an error may still have
		// taken effect - a deadline reached while the daemon was already
		// killing the container, for instance - and the service then stayed
		// down until somebody noticed. Starting a container that is still
		// running is a no-op, so this is safe either way.
		restoreCtx, cancel := rollbackContext(ctx)
		defer cancel()
		if _, startErr := u.cli().ContainerStart(restoreCtx, oldContainer.ID, client.ContainerStartOptions{}); startErr != nil {
			return fmt.Errorf("failed to stop container %s and could not bring it back up: %v (stop error: %w)", containerName, startErr, err)
		}
		return fmt.Errorf("failed to stop container %s; it was left running: %w", containerName, err)
	}

	newContainer, err := u.containerCreate(ctx, imageTag, replacementName, inspectedData)
	if err != nil {
		return u.containerRollbackToOldContainer(ctx, oldContainer.ID, containerName, err)
	}

	if _, err = u.cli().ContainerStart(ctx, newContainer.ID, client.ContainerStartOptions{}); err != nil {
		// Compensation runs on rollbackContext, not on ctx: the usual reason to
		// be here is that ctx itself expired or was cancelled, and cleaning up
		// through a dead context leaves the replacement container behind next
		// to the old one.
		cleanupCtx, cancel := rollbackContext(ctx)
		defer cancel()
		if _, rmErr := u.cli().ContainerRemove(cleanupCtx, newContainer.ID, client.ContainerRemoveOptions{Force: true}); rmErr != nil {
			// The replacement is still on the host. Its name is unique to this
			// attempt so it blocks nothing, but the operator has to be told
			// which container to delete rather than find it by accident.
			log.Warn().Err(rmErr).Str("container", replacementName).Msg("failed to clean up the replacement container")
			err = fmt.Errorf("%w; the replacement container %s could not be removed and is still on the host", err, replacementName)
		}
		return u.containerRollbackToOldContainer(ctx, oldContainer.ID, containerName, err)
	}

	if verifyHealth {
		// The replacement exists and is started; what follows is the wait for
		// it to hold or turn healthy, which is the long part. Callers that
		// report progress announce it here rather than guessing at a duration.
		for _, announce := range onVerify {
			announce()
		}
		err = u.ContainerHealthCheck(ctx, newContainer.ID, &inspectedData)
	}
	if err != nil {
		// A healthcheck that timed out cancels ctx, which is precisely when this
		// cleanup matters most.
		cleanupCtx, cancel := rollbackContext(ctx)
		defer cancel()
		if _, rmErr := u.cli().ContainerRemove(cleanupCtx, newContainer.ID, client.ContainerRemoveOptions{Force: true}); rmErr != nil {
			// The replacement is still on the host. Its name is unique to this
			// attempt so it blocks nothing, but the operator has to be told
			// which container to delete rather than find it by accident.
			log.Warn().Err(rmErr).Str("container", replacementName).Msg("failed to clean up the replacement container")
			err = fmt.Errorf("%w; the replacement container %s could not be removed and is still on the host", err, replacementName)
		}
		return u.containerRollbackToOldContainer(ctx, oldContainer.ID, containerName, err)
	}

	// healthy: drop the old container and take over its name
	if _, err := u.cli().ContainerRemove(ctx, oldContainer.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
		cleanupCtx, cancel := rollbackContext(ctx)
		defer cancel()
		_, _ = u.cli().ContainerRemove(cleanupCtx, newContainer.ID, client.ContainerRemoveOptions{Force: true})
		_, restartErr := u.cli().ContainerStart(cleanupCtx, oldContainer.ID, client.ContainerStartOptions{})
		if restartErr != nil {
			return fmt.Errorf("failed to remove old container %s and rollback restart failed: %v (remove error: %w)", containerName, restartErr, err)
		}
		return fmt.Errorf("failed to remove old container %s; replacement removed and old container restarted: %w", containerName, err)
	}
	renameCtx, cancel := rollbackContext(ctx)
	defer cancel()
	if _, err := u.cli().ContainerRename(renameCtx, newContainer.ID, client.ContainerRenameOptions{NewName: containerName}); err != nil {
		return fmt.Errorf("container update completed but replacement %s could not take final name %s; manual recovery is required: %w", newContainer.ID, containerName, err)
	}

	log.Info().Msgf("Successfully updated container %s", containerName)
	return nil
}

func (u *Service) containerRollbackToOldContainer(ctx context.Context, oldContainerID, containerName string, originalErr error) error {
	log.Warn().Msgf("Rolling back to old container %s", containerName)

	rollbackCtx, cancel := rollbackContext(ctx)
	defer cancel()
	_, err := u.cli().ContainerStart(rollbackCtx, oldContainerID, client.ContainerStartOptions{})
	if err != nil {
		return fmt.Errorf("rollback failed - cannot restart old container: %w (original error: %v)", err, originalErr)
	}

	log.Info().Msgf("Successfully rolled back to old container %s", containerName)
	return &RolledBackError{Err: originalErr}
}

func rollbackContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
}

// containerCreate creates a new container carrying the old one's whole
// configuration (config, host config, network endpoints) on a new image.
func (u *Service) containerCreate(
	ctx context.Context,
	imageTag, containerName string,
	inspectedData container.InspectResponse,
) (client.ContainerCreateResult, error) {
	cfg := inspectedData.Config
	if cfg == nil {
		cfg = &container.Config{}
	}
	// the inspected config still names the old image
	newCfg := *cfg
	newCfg.Image = imageTag

	var netConfig *network.NetworkingConfig
	if inspectedData.NetworkSettings != nil {
		netConfig = &network.NetworkingConfig{
			EndpointsConfig: inspectedData.NetworkSettings.Networks,
		}
	}

	created, err := u.cli().ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:             containerName,
		Config:           &newCfg,
		HostConfig:       inspectedData.HostConfig,
		NetworkingConfig: netConfig,
	})
	if err != nil {
		return client.ContainerCreateResult{}, fmt.Errorf("failed to create new container for %s: %w", containerName, err)
	}
	return created, nil
}

func (u *Service) ContainerHealthCheck(ctx context.Context, containerID string, c *container.InspectResponse) error {
	log.Info().Msg("Starting healthcheck for container")

	eg, healthCtx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		err := u.containerHealthCheckRuntime(healthCtx, containerID)
		if err != nil {
			return fmt.Errorf("runtime healthcheck failed\n%w", err)
		}
		return nil
	})

	eg.Go(func() error {
		err := u.containerHealthCheckUptime(healthCtx, containerID, c)
		if err != nil {
			return fmt.Errorf("uptime healthcheck failed\n%w", err)
		}
		return nil
	})

	eg.Go(func() error {
		err := u.containerHealthCheckPing(healthCtx, c)
		if err != nil {
			return fmt.Errorf("endpoint ping healthcheck failed\n%w", err)
		}
		return nil
	})

	if err := eg.Wait(); err != nil {
		return err
	}

	return nil
}

const (
	// An image without a HEALTHCHECK offers no evidence of readiness beyond
	// staying up, so require it to stay up for a while. Same window as the
	// protected update helper's wait_ready.
	updateStabilityWindow = 10 * time.Second
	// Floor for reaching `healthy`. Raised per container from the image's own
	// start period, because that is exactly how long the image declares it
	// needs before its check means anything.
	updateHealthFloor = 2 * time.Minute
	updateHealthCap   = 10 * time.Minute
)

// containerHealthCheckRuntime is the check that always runs. The two label
// driven checks below are opt-in and return nil when their label is absent,
// which left the overwhelming majority of updates verifying nothing at all:
// ContainerStart returning success was taken as proof of health, the previous
// container was force removed, and its image was queued for cleanup. An image
// that crashed on boot passed as a success and the rollback never fired.
//
// The verdict comes from the daemon's own event stream rather than a poll
// loop. The events hub already multiplexes one daemon subscription per host
// and is shared with the UI, so this costs one inspect, one subscription and
// one timer for the duration of a single update, and nothing at all at rest.
func (u *Service) containerHealthCheckRuntime(ctx context.Context, containerID string) error {
	// Subscribed before inspecting, so nothing that happens after the state is
	// read can slip between the two.
	events, unsubscribe := u.events()
	defer unsubscribe()

	inspect, err := u.cli().ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("inspect replacement container: %w", err)
	}
	state := inspect.Container.State
	if state == nil {
		return errors.New("replacement container reports no state")
	}
	if !state.Running {
		return fmt.Errorf("replacement container is %s instead of running", state.Status)
	}
	if state.Health == nil || state.Health.Status == container.NoHealthcheck {
		return u.waitForContainerStability(ctx, events, containerID)
	}
	switch state.Health.Status {
	case container.Healthy:
		return nil
	case container.Unhealthy:
		return errors.New("replacement container reported unhealthy")
	}
	return u.waitForContainerHealthy(ctx, events, containerID, healthDeadline(inspect.Container.Config))
}

// healthDeadline derives the wait from the image's declared start period, so a
// slow booting container is not failed for being slow in the way it said it
// would be.
func healthDeadline(config *container.Config) time.Duration {
	deadline := updateHealthFloor
	if config != nil && config.Healthcheck != nil && config.Healthcheck.StartPeriod > 0 {
		if candidate := config.Healthcheck.StartPeriod + time.Minute; candidate > deadline {
			deadline = candidate
		}
	}
	return min(deadline, updateHealthCap)
}

// sameContainer matches the hub's short ids against a full container id.
func sameContainer(eventID, containerID string) bool {
	if eventID == "" || len(containerID) < len(eventID) {
		return false
	}
	return containerID[:len(eventID)] == eventID
}

// waitForContainerStability accepts a container that simply survives the
// window. Only the process actually going away is a failure; a stop or kill
// is followed by its own die event.
func (u *Service) waitForContainerStability(ctx context.Context, events <-chan containerSrv.Event, containerID string) error {
	timer := time.NewTimer(updateStabilityWindow)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		case event, open := <-events:
			if !open {
				return errors.New("container event stream closed before the stability window elapsed")
			}
			if !sameContainer(event.ID, containerID) {
				continue
			}
			if event.Action == "die" || event.Action == "destroy" {
				return fmt.Errorf("replacement container %s within %s of starting", event.Action, updateStabilityWindow)
			}
		}
	}
}

func (u *Service) waitForContainerHealthy(ctx context.Context, events <-chan containerSrv.Event, containerID string, deadline time.Duration) error {
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			// The hub drops events for a slow consumer rather than blocking the
			// other listeners, so confirm against the daemon before condemning
			// a container that may well have reported healthy.
			return u.confirmHealthyOnDeadline(ctx, containerID, deadline)
		case event, open := <-events:
			if !open {
				return errors.New("container event stream closed before the container became healthy")
			}
			if !sameContainer(event.ID, containerID) {
				continue
			}
			switch event.Action {
			case "health_status":
				switch container.HealthStatus(event.Status) {
				case container.Healthy:
					return nil
				case container.Unhealthy:
					return errors.New("replacement container reported unhealthy")
				}
			case "die", "destroy":
				return fmt.Errorf("replacement container %s before reporting healthy", event.Action)
			}
		}
	}
}

func (u *Service) confirmHealthyOnDeadline(ctx context.Context, containerID string, deadline time.Duration) error {
	inspect, err := u.cli().ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("replacement container did not report healthy within %s: %w", deadline, err)
	}
	state := inspect.Container.State
	if state != nil && state.Running && state.Health != nil && state.Health.Status == container.Healthy {
		return nil
	}
	return fmt.Errorf("replacement container did not report healthy within %s", deadline)
}

const DockmanHealthCheckUptimeLabel = "dockman.update.healthcheck.uptime"

func (u *Service) containerHealthCheckUptime(ctx context.Context, containerID string, c *container.InspectResponse) error {
	if c == nil || c.Config == nil {
		return nil
	}
	lab := strings.TrimSpace(c.Config.Labels[DockmanHealthCheckUptimeLabel])
	if lab == "" {
		return nil
	}
	expectedUptime, err := time.ParseDuration(lab)
	if err != nil {
		log.Warn().Str("value", lab).Msg("invalid configured uptime healthcheck duration; skipping check")
		return nil
	}

	// wait for 1.5 times the expected time, to skip any container shenanigans
	// on 1x the container uptime was not matching expectedUptime for some reason
	if expectedUptime <= 0 || expectedUptime > 10*time.Minute {
		return fmt.Errorf("expected uptime must be greater than zero and at most 10m")
	}
	wait := time.Duration(float64(expectedUptime) * 1.5)
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}

	inspect, err := u.cli().ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}

	state := inspect.Container.State
	if !state.Running {
		return fmt.Errorf("container is not running")
	}

	startedAt, err := time.Parse(time.RFC3339Nano, state.StartedAt)
	if err != nil {
		return fmt.Errorf("failed to parse started time: %w", err)
	}

	uptime := time.Since(startedAt)
	if uptime < expectedUptime {
		return fmt.Errorf("container did not reach expected uptime of %s, container uptime: %s",
			expectedUptime.String(), uptime.String())
	}

	return nil
}

const DockmanHealthCheckPingLabel = "dockman.update.healthcheck.ping"
const DockmanHealthCheckPingTimeLabel = "dockman.update.healthcheck.time"

func (u *Service) containerHealthCheckPing(ctx context.Context, c *container.InspectResponse) error {
	if c == nil || c.Config == nil {
		return nil
	}
	endpoint := c.Config.Labels[DockmanHealthCheckPingLabel]
	if endpoint == "" {
		return nil
	}

	val := strings.TrimSpace(c.Config.Labels[DockmanHealthCheckPingTimeLabel])
	pingAfter := time.Duration(0)
	if val != "" {
		var err error
		pingAfter, err = time.ParseDuration(val)
		if err != nil {
			log.Warn().Str("value", val).Msg("invalid configured ping healthcheck delay; skipping check")
			return nil
		}
	}

	if pingAfter < 0 || pingAfter > 10*time.Minute {
		return fmt.Errorf("ping delay must be between zero and 10m")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return fmt.Errorf("healthcheck endpoint must be an absolute HTTP or HTTPS URL")
	}
	if err := validateHealthcheckHost(parsed.Hostname(), c); err != nil {
		return err
	}
	timer := time.NewTimer(pingAfter)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create healthcheck request: %w", err)
	}
	pingClient := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if err := validateHealthcheckHost(req.URL.Hostname(), c); err != nil {
			return err
		}
		if len(via) >= 3 {
			return errors.New("too many healthcheck redirects")
		}
		return nil
	}}
	resp, err := pingClient.Do(request)
	if err != nil {
		return fmt.Errorf("failed to ping %s: %w", endpoint, err)
	}
	defer fileutil.Close(resp.Body)

	// Check for a successful status code (in the 2xx range)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("invalid http status code %d: %s, statusCode must be within 200 <= code < 300", resp.StatusCode, resp.Status)
	}

	return nil
}

func validateHealthcheckHost(host string, c *container.InspectResponse) error {
	allowed := map[string]struct{}{"localhost": {}}
	for _, address := range []string{"127.0.0.1", "::1"} {
		allowed[address] = struct{}{}
	}
	if c != nil && c.NetworkSettings != nil {
		for _, endpoint := range c.NetworkSettings.Networks {
			if endpoint != nil && endpoint.IPAddress.IsValid() {
				allowed[endpoint.IPAddress.String()] = struct{}{}
			}
		}
	}
	if _, ok := allowed[strings.ToLower(host)]; ok {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("healthcheck endpoint host %q is refused; use loopback or one of the container IP addresses", host)
}

func (u *Service) ImageUpdateAvailable(ctx context.Context, imageName string) (bool, string, error) {
	// Get local image info
	localImages, err := u.cli().ImageList(ctx, client.ImageListOptions{
		Filters: client.Filters{}.Add("reference", imageName),
	})
	if err != nil {
		return false, "", err
	}

	var localDigest string
	for _, img := range localImages.Items {
		localDigest = img.ID
	}

	// Query the public registry directly. This avoids requiring the Docker
	// socket proxy's /distribution endpoint for a read-only metadata check.
	remoteDigest, err := RegistryManifestDigest(ctx, imageName)
	if err != nil {
		return false, "", err
	}

	localDigest = strings.TrimPrefix(localDigest, "sha256:")
	remoteDigest = strings.TrimPrefix(remoteDigest, "sha256:")

	return localDigest != remoteDigest, remoteDigest, nil
}
