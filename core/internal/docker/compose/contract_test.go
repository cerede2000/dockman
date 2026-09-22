//go:build composecontract

// The Compose contract: Dockman's own Compose calls, run against the real
// docker-compose binary and a real Docker daemon.
//
// Every other test of this package drives a fake runner, so it proves which
// command Dockman builds, never what Compose does with it. What Dockman then
// relies on - the config hash matching the label Compose stamps, the JSON
// model it decodes, the ps templates it parses, a build running without a
// console, --wait failing on an unhealthy service, the builder selection a
// limited build passes through the environment - lives in the binary, and a
// Compose upgrade can change any of it silently.
//
// CI runs this suite inside the image it just built (fork-integration-build),
// so it exercises the exact Compose, Buildx and Docker CLI Dockman ships:
//
//	go test -c -tags composecontract -o contract.test ./internal/docker/compose
//	docker run --entrypoint /contract.test ... <image> -test.run '^TestContract'
//
// Lines starting with "OBSERVE:" record behaviour Dockman does not depend on
// but that an upgrade review wants to compare between versions.
package compose_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/RA341/dockman/internal/docker/compose"
	"github.com/RA341/dockman/internal/docker/container"
	"github.com/RA341/dockman/internal/docker/updater"
	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/docker/compose/v5/pkg/api"
	containertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

const (
	contractBusybox = "busybox:1.37"
	contractAlpine  = "alpine:3.20"
	actionTimeout   = 4 * time.Minute
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

type contractStack struct {
	t       *testing.T
	root    string // Dockman's compose root: the runner's working directory
	name    string // stack directory, and so the Compose project name
	file    string // Dockman filename: <name>/compose.yaml, relative to root
	svc     *compose.Service
	cont    *container.Service
	cli     *client.Client
	cleanup []func()
}

func contractClient(t *testing.T) *client.Client {
	t.Helper()
	cli, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// newStack writes files into a fresh stack directory and wires a Compose
// service exactly as the host service does for a local host.
func newStack(t *testing.T, files map[string]string) *contractStack {
	t.Helper()
	base := os.Getenv("CONTRACT_WORKDIR")
	if base == "" {
		base = t.TempDir()
	}
	root, err := os.MkdirTemp(base, "root-")
	require.NoError(t, err)

	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	s := &contractStack{t: t, root: root, name: "dmc-" + hex.EncodeToString(suffix)}
	s.file = s.name + "/compose.yaml"
	for path, content := range files {
		s.write(path, content)
	}

	s.cli = contractClient(t)
	s.cont = container.New(s.cli)
	s.svc = compose.NewComposeTerminal("contract", s.cont, func(filename, _ string) (compose.Host, error) {
		return compose.Host{Fs: filesystem.NewLocal(root), Relpath: filename}, nil
	}, nil)

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = s.svc.Down(ctx, s.file, nil)
		for _, fn := range s.cleanup {
			fn()
		}
	})
	return s
}

// write puts a file in the stack directory; a path starting with "/" is
// relative to the compose root instead.
func (s *contractStack) write(path, content string) {
	s.t.Helper()
	full := filepath.Join(s.root, s.name, path)
	if strings.HasPrefix(path, "/") {
		full = filepath.Join(s.root, path)
	}
	require.NoError(s.t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(s.t, os.WriteFile(full, []byte(content), 0o644))
}

func (s *contractStack) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
	s.t.Cleanup(cancel)
	return ctx
}

// containers lists the project's containers, stopped ones included.
func (s *contractStack) containers() []containertypes.Summary {
	s.t.Helper()
	list, err := s.cli.ContainerList(context.Background(), client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", api.ProjectLabel+"="+s.name),
	})
	require.NoError(s.t, err)
	return list.Items
}

// only returns the single container of a service, waiting out the moment a
// recreated container is listed next to the one it replaces.
func (s *contractStack) only(service string) containertypes.Summary {
	s.t.Helper()
	var found []containertypes.Summary
	eventually(s.t, 10*time.Second, "a single container for service "+service, func() bool {
		found = found[:0]
		for _, c := range s.containers() {
			if c.Labels[api.ServiceLabel] == service {
				found = append(found, c)
			}
		}
		return len(found) == 1
	})
	return found[0]
}

func (s *contractStack) env(containerID string) []string {
	s.t.Helper()
	inspected, err := s.cli.ContainerInspect(context.Background(), containerID, client.ContainerInspectOptions{})
	require.NoError(s.t, err)
	return inspected.Container.Config.Env
}

func (s *contractStack) up(out *bytes.Buffer) {
	s.t.Helper()
	require.NoError(s.t, s.svc.Up(s.ctx(), s.file, out), "up output:\n%s", out)
}

func (s *contractStack) imageID(ref string) string {
	s.t.Helper()
	inspected, err := s.cli.ImageInspect(context.Background(), ref)
	require.NoError(s.t, err, "image %s", ref)
	return inspected.ID
}

func (s *contractStack) removeImageLater(ref string) {
	s.cleanup = append(s.cleanup, func() {
		_, _ = s.cli.ImageRemove(context.Background(), ref, client.ImageRemoveOptions{Force: true})
	})
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, within time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", within, what)
}

// running waits until every container of the stack is listed as running:
// the daemon's list trails a state change by a few milliseconds.
func (s *contractStack) running() {
	s.t.Helper()
	eventually(s.t, 10*time.Second, "every container running", func() bool {
		list := s.containers()
		for _, c := range list {
			if c.State != containertypes.StateRunning {
				return false
			}
		}
		return len(list) > 0
	})
}

func pullOnce(t *testing.T, refs ...string) {
	t.Helper()
	for _, ref := range refs {
		out, err := exec.Command("docker", "pull", "-q", ref).CombinedOutput()
		require.NoError(t, err, "pull %s: %s", ref, out)
	}
}

func sleeper(image string) string {
	return fmt.Sprintf("    image: %s\n    command: [\"sleep\", \"3600\"]\n", image)
}

// --- the binary --------------------------------------------------------------

// Dockman probes `docker compose` first and falls back to `docker-compose`.
// The image ships only the standalone binary it builds itself; a Compose
// plugin appearing in the image would silently take its place.
func TestContractDockmanRunsTheComposeShippedInTheImage(t *testing.T) {
	s := newStack(t, map[string]string{"compose.yaml": "services:\n  app:\n" + sleeper(contractBusybox)})
	pullOnce(t, contractBusybox)

	out := new(bytes.Buffer)
	s.up(out)
	firstLine := strings.TrimSpace(ansi.ReplaceAllString(strings.SplitN(out.String(), "\n", 2)[0], ""))
	require.True(t, strings.HasPrefix(firstLine, "docker-compose "), "Dockman ran: %q", firstLine)

	version, err := exec.Command("docker-compose", "version", "--short").Output()
	require.NoError(t, err)
	got := strings.TrimPrefix(strings.TrimSpace(string(version)), "v")
	t.Logf("OBSERVE: docker-compose version %s", got)
	if want := strings.TrimPrefix(os.Getenv("CONTRACT_COMPOSE_VERSION"), "v"); want != "" {
		require.Equal(t, want, got, "the image label and the binary disagree")
	}
}

// --- what the selective update reads ---------------------------------------

// The selective update calls a service unchanged when the hash `config
// --hash` reports equals the label on its container. If the two ever diverge,
// every service goes back through Compose and is recreated.
func TestContractTheConfigHashMatchesTheLabelOnTheContainer(t *testing.T) {
	s := newStack(t, map[string]string{
		"compose.yaml": "services:\n" +
			"  plain:\n" + sleeper(contractBusybox) +
			"  withenvfile:\n" + sleeper(contractBusybox) + "    env_file: [app.env]\n" +
			"  interpolated:\n" + sleeper(contractBusybox) + "    environment:\n      V: ${DMC_VALUE:-fallback}\n" +
			"  replicated:\n" + sleeper(contractBusybox) + "    deploy:\n      replicas: 2\n",
		"app.env": "X=1\n",
		".env":    "DMC_VALUE=from-dotenv\n",
	})
	pullOnce(t, contractBusybox)
	s.up(new(bytes.Buffer))

	plan, err := s.svc.ProjectPlan(s.ctx(), s.file)
	require.NoError(t, err)
	require.Len(t, plan, 4)
	for _, c := range s.containers() {
		service := c.Labels[api.ServiceLabel]
		if plan[service].ConfigHash == c.Labels[api.ConfigHashLabel] {
			continue
		}
		// Before 5.5, `config --hash` hashed env_file services without
		// merging the file into the environment, which `up` does before
		// stamping the label (docker/compose "resolve service environment
		// when computing --hash", 2026-08-11). The selective update then saw
		// such services as changed forever and always sent them back to
		// Compose. Recorded for older binaries, required from 5.5 on.
		if service == "withenvfile" && composeOlderThan(t, 5, 5) {
			t.Logf("OBSERVE: env_file services never match their label with this Compose: the selective update always hands them to Compose")
			continue
		}
		t.Errorf("service %s: config --hash %s, container label %s", service, plan[service].ConfigHash, c.Labels[api.ConfigHashLabel])
	}
}

func composeOlderThan(t *testing.T, major, minor int) bool {
	t.Helper()
	out, err := exec.Command("docker-compose", "version", "--short").Output()
	require.NoError(t, err)
	var gotMajor, gotMinor int
	_, err = fmt.Sscanf(strings.TrimPrefix(strings.TrimSpace(string(out)), "v"), "%d.%d", &gotMajor, &gotMinor)
	require.NoError(t, err)
	return gotMajor < major || (gotMajor == major && gotMinor < minor)
}

// --- upgrading Compose ---------------------------------------------------------

// What the first deployment after a Compose upgrade does to stacks the
// previous Compose deployed. CONTRACT_PREVIOUS_COMPOSE is the binary of the
// image this one replaces: the stacks are deployed with it, then deployed
// again through Dockman.
//
// Whether Compose recreates them is Compose's decision and is recorded, not
// asserted: from 5.4 the image label holds the platform manifest digest on
// the containerd image store, so every container is recreated once there,
// and an image built by a new Compose carries its version label, so a built
// service is recreated once on any store. What Dockman needs is asserted:
// every container runs afterwards and matches its manifest, so the selective
// update does not see a change that is not there.
func TestContractStacksDeployedByThePreviousComposeAfterAnUpgrade(t *testing.T) {
	previous := os.Getenv("CONTRACT_PREVIOUS_COMPOSE")
	if previous == "" {
		t.Skip("CONTRACT_PREVIOUS_COMPOSE is not set")
	}
	out, err := exec.Command(previous, "version", "--short").Output()
	require.NoError(t, err)
	store, err := exec.Command("docker", "info", "--format", "{{json .DriverStatus}}").Output()
	require.NoError(t, err)
	t.Logf("OBSERVE: upgrading from Compose %s, image store %s", strings.TrimSpace(string(out)), strings.TrimSpace(string(store)))
	pullOnce(t, contractBusybox)

	stacks := map[string]map[string]string{
		"plain":        {"compose.yaml": "services:\n  app:\n" + sleeper(contractBusybox) + "    restart: unless-stopped\n"},
		"envfile":      {"compose.yaml": "services:\n  app:\n" + sleeper(contractBusybox) + "    env_file: [app.env]\n", "app.env": "X=1\n"},
		"interpolated": {"compose.yaml": "services:\n  app:\n" + sleeper(contractBusybox) + "    environment:\n      V: ${DMC_V}\n", ".env": "DMC_V=1\n"},
		"replicas":     {"compose.yaml": "services:\n  app:\n" + sleeper(contractBusybox) + "    deploy:\n      replicas: 2\n"},
		"health": {"compose.yaml": "services:\n  app:\n" + sleeper(contractBusybox) +
			"    healthcheck:\n      test: [\"CMD\", \"true\"]\n      interval: 1s\n"},
		"network": {"compose.yaml": "services:\n  a:\n" + sleeper(contractBusybox) + "    networks: [back]\n" +
			"  b:\n" + sleeper(contractBusybox) + "    networks:\n      back:\n        aliases: [bee]\n    depends_on: [a]\n" +
			"networks:\n  back: {}\n"},
		"volume": {"compose.yaml": "services:\n  app:\n" + sleeper(contractBusybox) + "    volumes: [data:/data]\n" +
			"    tmpfs: [/run]\nvolumes:\n  data: {}\n"},
		"configs": {"compose.yaml": "services:\n  app:\n" + sleeper(contractBusybox) + "    configs: [conf]\n" +
			"    labels:\n      dockman.test: \"1\"\n    logging:\n      options: {max-size: 1m}\n" +
			"configs:\n  conf:\n    content: hello\n"},
		"build": {"compose.yaml": "services:\n  app:\n    build: .\n    command: [\"sleep\", \"3600\"]\n",
			"Dockerfile": "FROM busybox:1.37\nRUN echo upgrade\n"},
	}
	for name, files := range stacks {
		t.Run(name, func(t *testing.T) {
			s := newStack(t, files)
			s.removeImageLater(s.name + "-app")
			envFiles := []string{}
			if _, ok := files[".env"]; ok {
				envFiles = append(envFiles, "--env-file="+filepath.Join(s.root, s.name, ".env"))
			}
			args := append(envFiles, "--progress=plain", "-f", s.file, "up", "-d", "-y", "--build", "--remove-orphans")
			cmd := exec.Command(previous, args...)
			cmd.Dir = s.root
			deployed, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", deployed)

			before := map[string]string{}
			for _, c := range s.containers() {
				before[c.Names[0]] = c.ID
			}
			require.NotEmpty(t, before)

			s.up(new(bytes.Buffer))
			s.running()
			plan, err := s.svc.ProjectPlan(s.ctx(), s.file)
			require.NoError(t, err)
			recreated := 0
			for _, c := range s.containers() {
				// the env_file gap of Compose before 5.5, see
				// TestContractTheConfigHashMatchesTheLabelOnTheContainer
				if name != "envfile" || !composeOlderThan(t, 5, 5) {
					require.Equal(t, plan[c.Labels[api.ServiceLabel]].ConfigHash, c.Labels[api.ConfigHashLabel], "%s", c.Names[0])
				}
				if before[c.Names[0]] != c.ID {
					recreated++
				}
			}
			t.Logf("OBSERVE: upgrade, stack %q: %d of %d container(s) recreated by the first up", name, recreated, len(before))
		})
	}
}

// The plan's comments rely on Compose leaving the build section and the
// replica count out of the hash: a rescale or a build change must not look
// like a changed manifest to the selective update.
func TestContractTheConfigHashIgnoresBuildAndReplicas(t *testing.T) {
	s := newStack(t, map[string]string{
		"compose.yaml": "services:\n  app:\n    image: dmc/hash:dev\n    build:\n      context: .\n      args: {V: \"1\"}\n",
		"Dockerfile":   "FROM busybox:1.37\n",
	})
	before, err := s.svc.ProjectPlan(s.ctx(), s.file)
	require.NoError(t, err)

	s.write("compose.yaml", "services:\n  app:\n    image: dmc/hash:dev\n    scale: 3\n    build:\n      context: .\n      args: {V: \"2\"}\n")
	after, err := s.svc.ProjectPlan(s.ctx(), s.file)
	require.NoError(t, err)

	require.Equal(t, before["app"].ConfigHash, after["app"].ConfigHash)
	require.True(t, after["app"].Buildable)
	require.Equal(t, 3, after["app"].Replicas)
}

func TestContractServiceShapesComeFromTheUninterpolatedModel(t *testing.T) {
	s := newStack(t, map[string]string{
		"compose.yaml": "services:\n" +
			"  built:\n    build: .\n" +
			"  scaled:\n" + sleeper(contractBusybox) + "    scale: 2\n" +
			"  deployed:\n" + sleeper(contractBusybox) + "    deploy:\n      replicas: 3\n" +
			"  variable:\n" + sleeper(contractBusybox) + "    deploy:\n      replicas: ${DMC_REPLICAS}\n" +
			"    environment:\n      TOKEN: ${DMC_SECRET}\n",
		"Dockerfile": "FROM busybox:1.37\n",
		".env":       "DMC_REPLICAS=2\nDMC_SECRET=do-not-leak-4242\n",
	})
	plan, err := s.svc.ProjectPlan(s.ctx(), s.file)
	require.NoError(t, err)
	require.True(t, plan["built"].Buildable)
	require.False(t, plan["scaled"].Buildable)
	require.Equal(t, 2, plan["scaled"].Replicas)
	require.Equal(t, 3, plan["deployed"].Replicas)
	require.Equal(t, 0, plan["variable"].Replicas, "an unresolved count must read as unknown")

	// The flags Dockman passes are what keeps secret values out of the model
	// it decodes; this is Compose honouring them.
	cmd := exec.Command("docker-compose", "--progress=plain",
		"--env-file="+filepath.Join(s.root, s.name, ".env"), "-f", s.file,
		"config", "--format", "json", "--no-interpolate", "--no-env-resolution")
	cmd.Dir = s.root
	model, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", model)
	require.NotContains(t, string(model), "do-not-leak-4242")
	require.Contains(t, string(model), "${DMC_SECRET}")
}

// --- status and listing ------------------------------------------------------

func TestContractStatusCountsStatesAndHealth(t *testing.T) {
	s := newStack(t, map[string]string{
		"compose.yaml": "services:\n" +
			"  healthy:\n" + sleeper(contractBusybox) +
			"    healthcheck:\n      test: [\"CMD\", \"true\"]\n      interval: 1s\n      retries: 3\n" +
			"  exits:\n    image: " + contractBusybox + "\n    command: [\"true\"]\n    restart: \"no\"\n",
	})
	pullOnce(t, contractBusybox)
	s.up(new(bytes.Buffer))

	var state *compose.StackState
	eventually(t, 60*time.Second, "one healthy and one exited container", func() bool {
		var err error
		state, err = s.svc.Status(s.ctx(), s.file)
		require.NoError(t, err)
		return state.UpCount == 1 && state.DownCount == 1 && state.HealthyCount == 1
	})
	require.Zero(t, state.UnhealthyCount)
}

func TestContractListReturnsEveryContainerOfTheStack(t *testing.T) {
	s := newStack(t, map[string]string{
		"compose.yaml": "services:\n  a:\n" + sleeper(contractBusybox) + "  b:\n" + sleeper(contractBusybox),
	})
	pullOnce(t, contractBusybox)
	s.up(new(bytes.Buffer))
	require.NoError(t, s.svc.Stop(s.ctx(), s.file, new(bytes.Buffer), "b"))

	listed, err := s.svc.List(s.ctx(), s.file)
	require.NoError(t, err)
	require.Len(t, listed, 2, "a stopped container is still part of the stack")
}

// Stats and the monitor match containers to their stack by the config-files
// label, which must hold the path ComposeAbsPath computes.
func TestContractComposeLabelsAreWhatDockmanMatchesOn(t *testing.T) {
	s := newStack(t, map[string]string{"compose.yaml": "services:\n  app:\n" + sleeper(contractBusybox)})
	pullOnce(t, contractBusybox)
	s.up(new(bytes.Buffer))

	c := s.only("app")
	abs, err := s.svc.ComposeAbsPath(s.file)
	require.NoError(t, err)
	require.Equal(t, s.name, c.Labels[api.ProjectLabel])
	require.Equal(t, abs, c.Labels[api.ConfigFilesLabel])
	require.Equal(t, filepath.Join(s.root, s.name), c.Labels[api.WorkingDirLabel])
	require.Equal(t, "False", c.Labels[api.OneoffLabel])
	t.Logf("OBSERVE: image label %s=%s", api.ImageDigestLabel, c.Labels[api.ImageDigestLabel])

	stats, err := s.svc.Stats(s.ctx(), s.file)
	require.NoError(t, err)
	require.Len(t, stats, 1)
}

// --- validation --------------------------------------------------------------

func TestContractValidateReportsBrokenFiles(t *testing.T) {
	s := newStack(t, map[string]string{"compose.yaml": "services:\n  app:\n" + sleeper(contractBusybox)})
	require.Empty(t, s.svc.Validate(s.ctx(), s.file))

	s.write("compose.yaml", "services:\n  app:\n    image: busybox\n    notakey: 1\n")
	errs := s.svc.Validate(s.ctx(), s.file)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0].Error(), "notakey")

	s.write("compose.yaml", "services:\n  app:\n    image: busybox:${DMC_TAG:?DMC_TAG is required}\n")
	errs = s.svc.Validate(s.ctx(), s.file)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0].Error(), "DMC_TAG is required")
}

// --- deploying ---------------------------------------------------------------

// Dockman has no console. A stack that builds must build anyway (plain
// progress), and one that does not must deploy under the tty display.
func TestContractAStackWithABuildSectionBuildsWithoutAConsole(t *testing.T) {
	s := newStack(t, map[string]string{
		"compose.yaml": "services:\n  app:\n    build: .\n    command: [\"sleep\", \"3600\"]\n",
		"Dockerfile":   "FROM busybox:1.37\nRUN echo DMC-BUILD-STEP\n",
	})
	s.removeImageLater(s.name + "-app")
	out := new(bytes.Buffer)
	s.up(out)
	require.Contains(t, out.String(), "DMC-BUILD-STEP")
	s.running()
}

func TestContractAPulledStackDeploysUnderTheTtyDisplay(t *testing.T) {
	s := newStack(t, map[string]string{"compose.yaml": "services:\n  app:\n" + sleeper(contractBusybox)})
	pullOnce(t, contractBusybox)
	out := new(bytes.Buffer)
	s.up(out)
	require.Contains(t, ansi.ReplaceAllString(out.String(), ""), "--progress=tty")
	s.running()
}

func TestContractDryRunUpTouchesNothing(t *testing.T) {
	s := newStack(t, map[string]string{"compose.yaml": "services:\n  app:\n" + sleeper(contractBusybox)})
	pullOnce(t, contractBusybox)
	out := new(bytes.Buffer)
	require.NoError(t, s.svc.DryRunUp(s.ctx(), s.file, out), "%s", out)
	require.Empty(t, s.containers())
}

// Git deployments with automatic rollback depend on --wait failing on an
// unhealthy service, and succeeding on a healthy one.
func TestContractUpWaitFollowsHealth(t *testing.T) {
	healthy := newStack(t, map[string]string{"compose.yaml": "services:\n  app:\n" + sleeper(contractBusybox) +
		"    healthcheck:\n      test: [\"CMD\", \"true\"]\n      interval: 1s\n      retries: 3\n"})
	pullOnce(t, contractBusybox)
	out := new(bytes.Buffer)
	require.NoError(t, healthy.svc.UpWait(healthy.ctx(), healthy.file, out), "%s", out)

	sick := newStack(t, map[string]string{"compose.yaml": "services:\n  app:\n" + sleeper(contractBusybox) +
		"    healthcheck:\n      test: [\"CMD\", \"false\"]\n      interval: 1s\n      retries: 1\n"})
	out.Reset()
	start := time.Now()
	err := sick.svc.UpWait(sick.ctx(), sick.file, out)
	require.Error(t, err, "an unhealthy service must fail the wait:\n%s", out)
	lines := strings.Split(strings.TrimSpace(ansi.ReplaceAllString(err.Error(), "")), "\n")
	t.Logf("OBSERVE: an unhealthy wait failed after %s, last line: %s", time.Since(start).Round(time.Second), lines[len(lines)-1])
}

func TestContractAnUnchangedStackIsNotRecreated(t *testing.T) {
	s := newStack(t, map[string]string{"compose.yaml": "services:\n  app:\n" + sleeper(contractBusybox)})
	pullOnce(t, contractBusybox)
	s.up(new(bytes.Buffer))
	first := s.only("app").ID
	s.up(new(bytes.Buffer))
	require.Equal(t, first, s.only("app").ID)

	built := newStack(t, map[string]string{
		"compose.yaml": "services:\n  app:\n    build: .\n    command: [\"sleep\", \"3600\"]\n",
		"Dockerfile":   "FROM busybox:1.37\nRUN echo same\n",
	})
	built.removeImageLater(built.name + "-app")
	built.up(new(bytes.Buffer))
	firstBuilt := built.only("app").ID
	built.up(new(bytes.Buffer))
	t.Logf("OBSERVE: an unchanged build stack is recreated by a second up: %v", firstBuilt != built.only("app").ID)
}

func TestContractRedeployForceRecreates(t *testing.T) {
	s := newStack(t, map[string]string{"compose.yaml": "services:\n  app:\n" + sleeper(contractBusybox)})
	pullOnce(t, contractBusybox)
	s.up(new(bytes.Buffer))
	first := s.only("app").ID

	out := new(bytes.Buffer)
	require.NoError(t, s.svc.Redeploy(s.ctx(), s.file, out, true, false, true), "%s", out)
	require.NotEqual(t, first, s.only("app").ID)
}

func TestContractPullSkipsBuildsAndUnreachableImages(t *testing.T) {
	s := newStack(t, map[string]string{
		"compose.yaml": "services:\n" +
			"  pulled:\n" + sleeper(contractBusybox) +
			"  built:\n    build: .\n" +
			"  gone:\n" + sleeper("dmc-registry.invalid/nothing:1"),
		"Dockerfile": "FROM busybox:1.37\n",
	})
	out := new(bytes.Buffer)
	require.NoError(t, s.svc.Pull(s.ctx(), s.file, out), "%s", out)
}

func TestContractLifecycle(t *testing.T) {
	s := newStack(t, map[string]string{"compose.yaml": "services:\n  app:\n" + sleeper(contractBusybox)})
	pullOnce(t, contractBusybox)
	s.up(new(bytes.Buffer))
	out := new(bytes.Buffer)

	// The daemon's container list trails a state change by a few
	// milliseconds: a container Compose has just stopped can still be listed
	// as running. Wait for the state rather than read it once.
	stateBecomes := func(want containertypes.ContainerState) {
		t.Helper()
		eventually(t, 10*time.Second, "state "+string(want), func() bool { return s.only("app").State == want })
	}

	require.NoError(t, s.svc.Stop(s.ctx(), s.file, out), "%s", out)
	stateBecomes(containertypes.StateExited)

	require.NoError(t, s.svc.Start(s.ctx(), s.file, out), "%s", out)
	stateBecomes(containertypes.StateRunning)

	require.NoError(t, s.svc.Restart(s.ctx(), s.file, out), "%s", out)
	stateBecomes(containertypes.StateRunning)

	require.NoError(t, s.svc.Update(s.ctx(), s.file, out), "%s", out)
	stateBecomes(containertypes.StateRunning)

	require.NoError(t, s.svc.DownPlain(s.ctx(), s.file, out), "%s", out)
	eventually(t, 10*time.Second, "no container left", func() bool { return len(s.containers()) == 0 })
}

func TestContractUpRemovesOrphans(t *testing.T) {
	s := newStack(t, map[string]string{"compose.yaml": "services:\n  a:\n" + sleeper(contractBusybox) + "  b:\n" + sleeper(contractBusybox)})
	pullOnce(t, contractBusybox)
	s.up(new(bytes.Buffer))
	require.Len(t, s.containers(), 2)

	s.write("compose.yaml", "services:\n  a:\n"+sleeper(contractBusybox))
	s.up(new(bytes.Buffer))
	require.Len(t, s.containers(), 1)
}

// --- environment -------------------------------------------------------------

// Dockman passes every .env from the compose root down to the stack, outer
// first, so the stack's own file wins.
func TestContractEnvFilesLayerInnerOverOuter(t *testing.T) {
	s := newStack(t, map[string]string{
		"compose.yaml": "services:\n  app:\n" + sleeper(contractBusybox) +
			"    environment:\n      LAYERED: ${DMC_A}\n      OUTER_ONLY: ${DMC_B}\n",
		".env":  "DMC_A=inner\n",
		"/.env": "DMC_A=outer\nDMC_B=outer\n",
	})
	pullOnce(t, contractBusybox)
	s.up(new(bytes.Buffer))
	env := s.env(s.only("app").ID)
	require.Contains(t, env, "LAYERED=inner")
	require.Contains(t, env, "OUTER_ONLY=outer")
}

// Inline SOPS values reach Compose through the process environment only.
func TestContractInlineEnvironmentReachesInterpolation(t *testing.T) {
	s := newStack(t, map[string]string{"compose.yaml": "services:\n  app:\n" + sleeper(contractBusybox) +
		"    environment:\n      TOKEN: ${DMC_INLINE_TOKEN}\n"})
	s.svc.SetEnvironmentProvider(func(context.Context, string, filesystem.FileSystem, string) ([]string, error) {
		return []string{"DMC_INLINE_TOKEN=inline-value"}, nil
	})
	pullOnce(t, contractBusybox)
	s.up(new(bytes.Buffer))
	require.Contains(t, s.env(s.only("app").ID), "TOKEN=inline-value")
}

// --- builds with limits --------------------------------------------------------

// A limited build reaches Compose as BUILDX_CONFIG and BUILDX_BUILDER; Compose
// hands both to bake. The builder must be gone afterwards.
func TestContractLimitedBuildsRunInTheirOwnBuilder(t *testing.T) {
	s := newStack(t, map[string]string{
		"compose.yaml": "services:\n  app:\n    build: .\n    command: [\"sleep\", \"3600\"]\n",
		"Dockerfile":   "FROM busybox:1.37\nRUN echo DMC-LIMITED-STEP\n",
	})
	s.removeImageLater(s.name + "-app")
	s.svc.SetBuildLimits(compose.BuildLimits{CPUs: 1, MemoryBytes: 1 << 30})
	out := new(bytes.Buffer)
	s.up(out)
	require.Contains(t, out.String(), "*** Build limits: 1 CPU, 1 GiB ***")
	require.Contains(t, out.String(), "DMC-LIMITED-STEP")
	s.running()

	list, err := s.cli.ContainerList(context.Background(), client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("name", "buildx_buildkit_dockman-limited0"),
	})
	require.NoError(t, err)
	require.Empty(t, list.Items, "the limited builder must not outlive the build")
}

// --- images that move -------------------------------------------------------------

// A tag that moves to another image must end up running, whether Compose
// reconciles it alone or after Dockman replaced the container through the
// Docker API (the selective update and the Monitor do). What Compose does
// with the container Dockman replaced is recorded, not asserted.
func TestContractAMovedTagEndsUpRunning(t *testing.T) {
	pullOnce(t, contractBusybox, contractAlpine)
	for _, viaDockman := range []bool{false, true} {
		t.Run(fmt.Sprintf("dockman-replaced-first=%v", viaDockman), func(t *testing.T) {
			s := newStack(t, nil)
			tag := "dmc-moving/" + s.name + ":current"
			s.write("compose.yaml", "services:\n  app:\n    image: "+tag+"\n    pull_policy: never\n    command: [\"sleep\", \"3600\"]\n")
			s.removeImageLater(tag)
			_, err := s.cli.ImageTag(context.Background(), client.ImageTagOptions{Source: contractBusybox, Target: tag})
			require.NoError(t, err)
			s.up(new(bytes.Buffer))
			before := s.only("app")

			_, err = s.cli.ImageTag(context.Background(), client.ImageTagOptions{Source: contractAlpine, Target: tag})
			require.NoError(t, err)
			moved := s.imageID(tag)

			if viaDockman {
				engine := updater.New(s.cont, "contract", "", updater.NewNoopStore())
				result, err := engine.ForceUpdateContainer(s.ctx(),
					func(context.Context, string) error { return nil },
					new(bytes.Buffer), before.ID,
					updater.ForceUpdateOptions{ImagePrepared: true, ImageReference: tag})
				require.NoError(t, err)
				require.Equal(t, moved, result.NewImage)
				before = s.only("app")
				require.Equal(t, moved, before.ImageID)
				plan, err := s.svc.ProjectPlan(s.ctx(), s.file)
				require.NoError(t, err)
				require.Equal(t, plan["app"].ConfigHash, before.Labels[api.ConfigHashLabel],
					"a replaced container must still match its manifest")
			}

			s.up(new(bytes.Buffer))
			s.running()
			after := s.only("app")
			require.Equal(t, moved, after.ImageID)
			t.Logf("OBSERVE: dockman-replaced-first=%v, the next up recreated the container: %v",
				viaDockman, before.ID != after.ID)
		})
	}
}
