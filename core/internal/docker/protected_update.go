package docker

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/rs/zerolog/log"
)

const protectedUpdateContainerName = "dockman-protected-update"

type protectedUpdateTarget struct {
	containerID string
	name        string
	service     string
	project     string
	workingDir  string
	files       []string
}

// ProtectedContainerUpdate launches a detached helper that updates one
// Compose service without depending on Dockman's normal Docker API path while
// the target is being replaced. This is intended for infrastructure on which
// Dockman itself depends, such as a Docker socket proxy.
func ProtectedContainerUpdate(ctx context.Context, dkSrv *Service, containerID string) error {
	// Every other Compose action serializes on the stack lock; this one did
	// not, so a `compose down` running at the same moment left orphans behind.
	//
	// The lock covers the launch, not the helper's own run: the helper is
	// deliberately detached because Dockman may lose its daemon connection
	// while the socket proxy is being replaced, and an in-process lock cannot
	// reach into another container. It closes the window Dockman controls.
	return withContainerUpdateLocks(ctx, dkSrv, []string{containerID}, func() error {
		return launchProtectedContainerUpdate(ctx, dkSrv, containerID)
	})
}

func launchProtectedContainerUpdate(ctx context.Context, dkSrv *Service, containerID string) error {
	cli := dkSrv.Container.Cli()
	target, err := resolveProtectedUpdateTarget(ctx, cli, containerID)
	if err != nil {
		return err
	}

	if err = ensureProtectedUpdateHelperAvailable(ctx, cli); err != nil {
		return err
	}
	// Pull before starting the helper: once the socket proxy is replaced,
	// Dockman's own daemon connection may briefly be unavailable.
	if err = pullImage(ctx, cli, selfUpdateHelperImage); err != nil {
		return fmt.Errorf("pull protected-update helper image %s: %w", selfUpdateHelperImage, err)
	}

	mounts, err := protectedUpdateMounts(target)
	if err != nil {
		return err
	}

	const script = `set -u
sleep 2
cd "$DK_WORKING_DIR"
export COMPOSE_FILE="$DK_COMPOSE_FILES"
export COMPOSE_PATH_SEPARATOR=":"
[ -n "$DK_PROJECT" ] && export COMPOSE_PROJECT_NAME="$DK_PROJECT"

compose() {
  if [ -f ../.env ]; then
    docker compose --env-file ../.env "$@"
  else
    docker compose "$@"
  fi
}

wait_ready() {
  candidate="$1"
  stable=0
  elapsed=0
  while [ "$elapsed" -lt 90 ]; do
    state="$(docker inspect --format '{{.State.Status}}' "$candidate" 2>/dev/null || true)"
    health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$candidate" 2>/dev/null || true)"
    case "$state:$health" in
      running:healthy) return 0 ;;
      running:unhealthy|exited:*|dead:*) return 1 ;;
      running:)
        stable=$((stable + 1))
        [ "$stable" -ge 10 ] && return 0
        ;;
      *) stable=0 ;;
    esac
    sleep 1
    elapsed=$((elapsed + 1))
  done
  return 1
}

old_image_id="$(docker inspect --format '{{.Image}}' "$DK_CONTAINER_ID")"
old_image_ref="$(docker inspect --format '{{.Config.Image}}' "$DK_CONTAINER_ID")"
echo "Protected update: $DK_NAME ($DK_SERVICE)"
echo "Previous image: $old_image_ref ($old_image_id)"

if ! compose pull --ignore-buildable "$DK_SERVICE"; then
  echo "Image pull failed; the existing service was not modified."
  exit 20
fi
if ! compose up -d --no-deps --force-recreate "$DK_SERVICE"; then
  update_error="Compose could not recreate the service"
else
  new_id="$(compose ps -q "$DK_SERVICE" 2>/dev/null || true)"
  if [ -n "$new_id" ] && wait_ready "$new_id"; then
    echo "Protected update completed and verified."
    docker rm -f "$DK_SELF" >/dev/null 2>&1 || true
    exit 0
  fi
  update_error="The replacement did not reach a stable running state"
fi

echo "$update_error; restoring the previous image..."
if [ -z "$old_image_id" ] || [ -z "$old_image_ref" ]; then
  echo "Rollback unavailable: previous image identity is incomplete."
  exit 30
fi
if ! docker image tag "$old_image_id" "$old_image_ref"; then
  echo "Rollback failed: could not restore tag $old_image_ref."
  exit 31
fi
if ! compose up -d --pull never --no-build --no-deps --force-recreate "$DK_SERVICE"; then
  echo "Rollback failed while recreating the previous service."
  exit 32
fi
rollback_id="$(compose ps -q "$DK_SERVICE" 2>/dev/null || true)"
if [ -z "$rollback_id" ] || ! wait_ready "$rollback_id"; then
  echo "Rollback container was recreated but did not become ready."
  exit 33
fi
echo "Update failed; previous image restored and verified."
exit 34`

	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: protectedUpdateContainerName,
		Config: &container.Config{
			Image:      selfUpdateHelperImage,
			Entrypoint: []string{"sh"},
			Cmd:        []string{"-c", script},
			Env: []string{
				"DK_CONTAINER_ID=" + target.containerID,
				"DK_NAME=" + target.name,
				"DK_SERVICE=" + target.service,
				"DK_PROJECT=" + target.project,
				"DK_WORKING_DIR=" + target.workingDir,
				"DK_COMPOSE_FILES=" + strings.Join(target.files, ":"),
				"DK_SELF=" + protectedUpdateContainerName,
			},
			Labels: map[string]string{
				dockmanContainerLabel:             "false",
				"dockman.protected-update.helper": "true",
				"dockman.protected-update.target": target.containerID,
			},
		},
		HostConfig: &container.HostConfig{Mounts: mounts},
	})
	if err != nil {
		return fmt.Errorf("create protected-update helper: %w", err)
	}
	if _, err = cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		_, _ = cli.ContainerRemove(context.WithoutCancel(ctx), created.ID, client.ContainerRemoveOptions{Force: true})
		return fmt.Errorf("start protected-update helper: %w", err)
	}

	log.Info().Str("container", target.name).Str("service", target.service).
		Msg("protected infrastructure update helper launched")
	return nil
}

func resolveProtectedUpdateTarget(ctx context.Context, cli *client.Client, containerID string) (protectedUpdateTarget, error) {
	containerID = strings.TrimSpace(containerID)
	if containerID == "" || len(containerID) > 128 || strings.ContainsAny(containerID, "/\\\x00\r\n") {
		return protectedUpdateTarget{}, fmt.Errorf("invalid container id")
	}
	response, err := cli.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return protectedUpdateTarget{}, fmt.Errorf("inspect protected-update target: %w", err)
	}
	item := response.Container
	if item.Config == nil || item.State == nil {
		return protectedUpdateTarget{}, fmt.Errorf("target container has incomplete Docker metadata")
	}
	if !item.State.Running {
		return protectedUpdateTarget{}, fmt.Errorf("protected update requires a running container")
	}
	labels := item.Config.Labels
	if labels[dockmanContainerLabel] == "true" {
		return protectedUpdateTarget{}, fmt.Errorf("use Dockman's dedicated self-update action for the Dockman container")
	}
	service := strings.TrimSpace(labels[api.ServiceLabel])
	project := strings.TrimSpace(labels[api.ProjectLabel])
	workingDir := strings.TrimSpace(labels[api.WorkingDirLabel])
	files, err := normalizeProtectedComposeFiles(labels[api.ConfigFilesLabel], workingDir)
	if err != nil {
		return protectedUpdateTarget{}, err
	}
	if service == "" || project == "" || workingDir == "" {
		return protectedUpdateTarget{}, fmt.Errorf("protected update is only available for Docker Compose services with complete project labels")
	}
	if strings.ContainsAny(service+project+workingDir, "\x00\r\n") {
		return protectedUpdateTarget{}, fmt.Errorf("invalid Docker Compose metadata on target container")
	}
	if strings.Contains(workingDir, ":") {
		return protectedUpdateTarget{}, fmt.Errorf("compose working directory containing ':' is not supported by protected update")
	}
	name := strings.TrimPrefix(item.Name, "/")
	return protectedUpdateTarget{containerID: item.ID, name: name, service: service, project: project, workingDir: filepath.Clean(workingDir), files: files}, nil
}

func normalizeProtectedComposeFiles(value, workingDir string) ([]string, error) {
	workingDir = strings.TrimSpace(workingDir)
	if value == "" || workingDir == "" {
		return nil, fmt.Errorf("protected update is unavailable: Compose file or working-directory label is missing")
	}
	parts := strings.Split(value, ",")
	files := make([]string, 0, len(parts))
	for _, part := range parts {
		file := strings.TrimSpace(part)
		if file == "" || strings.ContainsAny(file, ":\x00\r\n") {
			return nil, fmt.Errorf("invalid Compose file metadata on target container")
		}
		if !filepath.IsAbs(file) {
			file = filepath.Join(workingDir, file)
		}
		files = append(files, filepath.Clean(file))
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no Compose file found for protected update")
	}
	return files, nil
}

func protectedUpdateMounts(target protectedUpdateTarget) ([]mount.Mount, error) {
	paths := []string{target.workingDir}
	parent := filepath.Dir(target.workingDir)
	if parent != "/" && parent != "." {
		paths = append(paths, parent)
	}
	for _, file := range target.files {
		paths = append(paths, filepath.Dir(file))
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	result := []mount.Mount{{Type: mount.TypeBind, Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"}}
	for _, path := range paths {
		path = filepath.Clean(path)
		if !filepath.IsAbs(path) || path == "/" {
			return nil, fmt.Errorf("refusing unsafe Compose bind path %q", path)
		}
		// If an ancestor is already mounted, the nested path is already visible.
		covered := false
		for _, existing := range result[1:] {
			if path == existing.Source || strings.HasPrefix(path, existing.Source+string(filepath.Separator)) {
				covered = true
				break
			}
		}
		if !covered {
			result = append(result, mount.Mount{Type: mount.TypeBind, Source: path, Target: path, ReadOnly: true})
		}
	}
	return result, nil
}

func ensureProtectedUpdateHelperAvailable(ctx context.Context, cli *client.Client) error {
	response, err := cli.ContainerInspect(ctx, protectedUpdateContainerName, client.ContainerInspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("inspect protected-update helper: %w", err)
	}
	if response.Container.State != nil && response.Container.State.Running {
		return fmt.Errorf("another protected update is already in progress; inspect %s for details", protectedUpdateContainerName)
	}
	if _, err = cli.ContainerRemove(ctx, protectedUpdateContainerName, client.ContainerRemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("remove previous protected-update helper: %w", err)
	}
	return nil
}
