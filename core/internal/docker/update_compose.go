package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/RA341/dockman/internal/docker/compose"
	"github.com/RA341/dockman/internal/docker/updater"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
)

// composeUpdateTarget is a service whose only pending change could be a newer
// image, together with the image reference captured before anything is pulled.
type composeUpdateTarget struct {
	service   string
	container container.Summary
	// image is the reference the running container records. Pulling a moving
	// tag such as :latest re-points it at the new image, after which a fresh
	// listing may expose the old one only as sha256:<id> - so this has to be
	// read before the pull, not after.
	image string
}

// composeUpdatePlan splits a stack into what Compose has to reconcile and what
// Dockman can replace itself.
type composeUpdatePlan struct {
	composeServices []string
	imageTargets    []composeUpdateTarget
	orphans         []string
	// outOfScope lists services the user's selection excluded, so the report
	// says what was deliberately left alone.
	outOfScope []string
}

func (p composeUpdatePlan) nothingToDo() bool {
	return len(p.composeServices) == 0 && len(p.imageTargets) == 0 && len(p.orphans) == 0
}

// ComposeSelectiveUpdate backs the Update button of the editor's Deploy tab.
//
// It used to be `compose pull` followed by `compose up -d` across the whole
// stack. That is correct but blunt: it re-pulls services nothing asked about,
// and when a new image does come down it recreates the container with no
// health check and no rollback - the one thing the Monitor's Update button
// does provide.
//
// The split follows what each tool can actually express. Compose stays
// authoritative for everything structural: a changed manifest, a new or
// removed service, a service built from a local context, a container that is
// not running, a service scaled beyond one replica, and anything Dockman must
// not replace through its own Docker connection. What is left is the plain
// case - a service that matches its manifest and whose only change is a newer
// image - and that goes through the same verified replacement as the Monitor:
// pull, recreate, wait for health, roll back on failure.
//
// Services that need nothing are not touched at all.
func ComposeSelectiveUpdate(ctx context.Context, dkSrv *Service, filename string, out io.Writer, selected ...string) error {
	report := func(format string, args ...any) { _, _ = fmt.Fprintf(out, format+"\r\n", args...) }

	plan, err := planComposeUpdate(ctx, dkSrv, filename, selected)
	if err != nil {
		// Planning is advisory, never a gate. An older Compose without
		// --hash, a model that will not decode, a listing that fails: none of
		// those is a reason to refuse an update the user asked for. Fall back
		// to the previous unconditional pull and up, which is still correct,
		// only less selective.
		report("Could not work out what changed (%v); updating the whole stack.", err)
		return dkSrv.Compose.Update(ctx, filename, out, selected...)
	}

	if plan.nothingToDo() {
		report("Nothing to do: every service matches the compose file and runs a current container.")
		return nil
	}
	describeComposeUpdatePlan(plan, report)

	var errs []error
	if len(plan.composeServices) > 0 || len(plan.orphans) > 0 {
		// The service list stays empty when only orphans have to go, so that
		// Compose sees the whole project and can remove them.
		if err := dkSrv.Compose.Up(ctx, filename, out, plan.composeServices...); err != nil {
			errs = append(errs, err)
		}
	}
	if err := runComposeImageUpdates(ctx, dkSrv, filename, out, plan.imageTargets, report); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func describeComposeUpdatePlan(plan composeUpdatePlan, report func(string, ...any)) {
	if len(plan.composeServices) > 0 {
		report("Compose will reconcile: %s", strings.Join(plan.composeServices, ", "))
	}
	if len(plan.orphans) > 0 {
		report("Removing orphan container(s) left by: %s", strings.Join(plan.orphans, ", "))
	}
	if len(plan.imageTargets) > 0 {
		names := make([]string, 0, len(plan.imageTargets))
		for _, target := range plan.imageTargets {
			names = append(names, target.service)
		}
		report("Checking for a newer image on: %s", strings.Join(names, ", "))
	}
	if len(plan.outOfScope) > 0 {
		report("Outside the selection, left alone: %s", strings.Join(plan.outOfScope, ", "))
	}
}

// runComposeImageUpdates pulls the candidate services in one go, then replaces
// only those that actually came down on a new image.
func runComposeImageUpdates(
	ctx context.Context,
	dkSrv *Service,
	filename string,
	out io.Writer,
	targets []composeUpdateTarget,
	report func(string, ...any),
) error {
	if len(targets) == 0 {
		return nil
	}
	services := make([]string, 0, len(targets))
	for _, target := range targets {
		services = append(services, target.service)
	}
	// One pull for the whole group: Compose resolves the host's registry
	// credentials once, and --ignore-pull-failures keeps a single unreachable
	// registry from cancelling the services that are reachable.
	if err := dkSrv.Compose.Pull(ctx, filename, out, services...); err != nil {
		return fmt.Errorf("pull images for %s: %w", strings.Join(services, ", "), err)
	}

	pull := func(pullCtx context.Context, imageTag string) error {
		return dkSrv.Compose.PullImage(pullCtx, imageTag, out)
	}
	var errs []error
	var replaced, current int
	for _, target := range targets {
		result, err := dkSrv.Updater.ForceUpdateContainer(ctx, pull, out, target.container.ID, updater.ForceUpdateOptions{
			VerifyHealth: true,
			// The group pull above already ran; this only tells the updater
			// which reference to resolve the new image from.
			ImagePrepared:  true,
			ImageReference: target.image,
		})
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", target.service, err))
		case result.Updated:
			replaced++
		default:
			current++
		}
	}
	report("Images: %d service(s) replaced and verified, %d already current.", replaced, current)
	return errors.Join(errs...)
}

// planComposeUpdate reads the manifest's expectations and the daemon's reality,
// then hands both to classifyComposeUpdate.
func planComposeUpdate(ctx context.Context, dkSrv *Service, filename string, selected []string) (composeUpdatePlan, error) {
	var plan composeUpdatePlan
	if dkSrv.Compose == nil || dkSrv.Updater == nil {
		return plan, fmt.Errorf("compose or updater service is unavailable")
	}
	expected, err := dkSrv.Compose.ProjectPlan(ctx, filename)
	if err != nil {
		return plan, err
	}
	running, err := dkSrv.Compose.List(ctx, filename)
	if err != nil {
		return plan, fmt.Errorf("list stack containers: %w", err)
	}
	return classifyComposeUpdate(expected, running, selected), nil
}

// classifyComposeUpdate decides, for each service, whether Compose reconciles
// it or Dockman replaces its container. It is deliberately free of any daemon
// or subprocess call: this is where a misclassification would stop or recreate
// the wrong container, so it has to be exercisable on its own.
func classifyComposeUpdate(
	expected map[string]compose.ServicePlan,
	running []container.Summary,
	selected []string,
) composeUpdatePlan {
	var plan composeUpdatePlan

	wanted := make(map[string]bool, len(selected))
	for _, name := range selected {
		if name = strings.TrimSpace(name); name != "" {
			wanted[name] = true
		}
	}
	inScope := func(service string) bool { return len(wanted) == 0 || wanted[service] }

	byService := make(map[string][]container.Summary, len(expected))
	for _, row := range running {
		service := strings.TrimSpace(row.Labels[api.ServiceLabel])
		if service == "" {
			continue
		}
		if _, declared := expected[service]; !declared {
			// A container whose service is gone from the manifest. Compose
			// removes it, but only when it is told to look at the project.
			if !slices.Contains(plan.orphans, service) {
				plan.orphans = append(plan.orphans, service)
			}
			continue
		}
		byService[service] = append(byService[service], row)
	}

	for service, expectation := range expected {
		if !inScope(service) {
			plan.outOfScope = append(plan.outOfScope, service)
			continue
		}
		rows := byService[service]
		if len(rows) != 1 || expectation.Replicas != 1 {
			// No container yet, a replica set, or a manifest asking for a
			// count Dockman could not read: all of them are Compose's to
			// reconcile as a whole, not a container to replace one by one.
			plan.composeServices = append(plan.composeServices, service)
			continue
		}
		row := rows[0]
		switch {
		case expectation.Buildable,
			row.State != container.StateRunning,
			row.Labels[api.ConfigHashLabel] != expectation.ConfigHash,
			!pullableImageReference(row.Image),
			updater.ProtectedFromAPIReplacement(&row):
			plan.composeServices = append(plan.composeServices, service)
		default:
			plan.imageTargets = append(plan.imageTargets, composeUpdateTarget{
				service:   service,
				container: row,
				image:     strings.TrimSpace(row.Image),
			})
		}
	}

	// Stable output so the reported plan does not reshuffle between runs;
	// Compose orders its own work from the dependency graph regardless.
	slices.Sort(plan.composeServices)
	slices.Sort(plan.orphans)
	slices.Sort(plan.outOfScope)
	slices.SortFunc(plan.imageTargets, func(a, b composeUpdateTarget) int {
		return strings.Compare(a.service, b.service)
	})
	return plan
}

// pullableImageReference rejects what the updater cannot act on: an empty
// reference, or a bare digest left behind when a moving tag was re-pointed.
// Such a service goes back to Compose, which resolves it from the manifest.
func pullableImageReference(reference string) bool {
	reference = strings.TrimSpace(reference)
	return reference != "" && !strings.HasPrefix(reference, "sha256:")
}
