package compose

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"

	"github.com/RA341/dockman/internal/docker/container"
	"github.com/RA341/dockman/internal/host/filesystem"

	"github.com/fatih/color"
	container2 "github.com/moby/moby/api/types/container"
	"golang.org/x/crypto/ssh"
)

// installed alone
const composeStandalone = "docker-compose"

// installed via docker compose
const composePlugin = "docker compose"

type Host struct {
	Fs      filesystem.FileSystem
	Relpath string
}

type FilenameParser func(filename string, host string) (Host, error)

// EnvironmentProvider returns short-lived KEY=value entries for one Compose
// invocation. Implementations must not persist plaintext or retain a cache.
type EnvironmentProvider func(context.Context, string, filesystem.FileSystem, string) ([]string, error)

type Service struct {
	TTY      bool
	cont     *container.Service
	parser   FilenameParser
	runner   CmdRunner
	hostname string
	// reverse of parser: absolute compose path -> dockman filename;
	// injected by the host service which owns the alias table
	pathResolver PathResolver
	environment  EnvironmentProvider
	// caps the builds this host runs; zero value = no limit (see BuildLimits)
	buildLimits BuildLimits
}

func (c *Service) SetEnvironmentProvider(provider EnvironmentProvider) { c.environment = provider }

func NewComposeTerminal(
	hostname string,
	cont *container.Service,
	getFs FilenameParser,
	cli *ssh.Client,
	// TTY bool,
) *Service {
	var runner CmdRunner
	if cli == nil {
		runner = NewLocalRunner()
	} else {
		runner = NewRemoteRunner(cli)
	}

	return &Service{
		TTY:      true,
		cont:     cont,
		parser:   getFs,
		runner:   runner,
		hostname: hostname,
	}
}

// checks if tty is enabled or not and sets the appropriate --progress flag
// todo load TTY from rpc instead of struct wide
func (c *Service) progressOut() string {
	if c.TTY {
		return "--progress=tty"
	}
	return "--progress=plain"
}

// mayBuild reports whether an up of this stack can build an image: true when
// a service has a build section, and true when the model cannot be read -
// the answer that keeps the action working either way (see actionProgress).
func (c *Service) mayBuild(ctx context.Context, filename string) bool {
	shapes, err := c.serviceShapes(ctx, filename)
	if err != nil {
		return true
	}
	for _, shape := range shapes {
		if shape.Buildable {
			return true
		}
	}
	return false
}

// actionProgress is the progress mode of an action that may build an image.
//
// Dockman runs Compose without a terminal: the output is streamed to the UI,
// not written to a console. Compose's own tty display copes with that;
// BuildKit's does not. Compose hands builds to Buildx bake, and under
// --progress=tty bake stops at "failed to get console: provided file is not a
// console" before building a single layer. Every stack with a build section
// failed that way from the Deploy tab - and from Git deployments without
// automatic rollback, which run the same Up - while stacks of pulled images
// never noticed anything.
//
// So an action that may build runs with plain progress, and keeps the tty
// display otherwise. When the model cannot be read, plain is the safe answer:
// it works with or without a build, and the action itself then reports why
// the file does not load.
func (c *Service) actionProgress(builds bool) string {
	if builds {
		return "--progress=plain"
	}
	return c.progressOut()
}

// explainBuildDenial names the likely cause when the Docker API refuses a
// build with a bare "403 Forbidden" page, which says nothing about why.
// Both causes were reproduced with LinuxServer's socket-proxy:
//   - Compose builds through BuildKit's /grpc API (or the /session
//     fallback), which the proxy denies unless GRPC=1 (or SESSION=1);
//   - a host with build limits builds in a docker-container builder, and
//     Buildx copies its configuration into that container when it creates
//     it: a PUT on containers/{id}/archive, denied unless ALLOW_ARCHIVE=1.
//     That builder does not use /grpc at all.
func (c *Service) explainBuildDenial(err error, builds bool) error {
	if err == nil || !builds || !strings.Contains(err.Error(), "403 Forbidden") {
		return err
	}
	if c.buildLimits.Active() {
		return fmt.Errorf("%w\n\nThe Docker API refused the limited builder. This host caps its builds, so they run "+
			"in a BuildKit container Dockman creates and copies its configuration into. Behind a socket proxy, "+
			"allow container archive writes (ALLOW_ARCHIVE=1 on LinuxServer's socket-proxy) besides CONTAINERS, "+
			"POST, EXEC and VOLUMES. See the Docker socket proxy documentation", err)
	}
	return fmt.Errorf("%w\n\nThe Docker API refused the build. If Dockman reaches Docker through a socket proxy, "+
		"Compose builds need BuildKit's API: allow GRPC=1 on the proxy (SESSION=1 also works). "+
		"See the Docker socket proxy documentation", err)
}

func (c *Service) version(ctx context.Context) ([]string, error) {
	errWriter := bytes.Buffer{}

	split := strings.Split(composePlugin, " ")
	err := c.runner.Run(
		ctx,
		[]string{split[0], split[1], "version"},
		"",
		nil,
		nil,
		&errWriter,
	)
	if err == nil {
		return []string{split[0], split[1]}, nil
	}

	err = c.runner.Run(
		ctx,
		[]string{composeStandalone, "version"},
		"",
		nil,
		nil,
		&errWriter,
	)
	if err == nil {
		return []string{composeStandalone}, nil
	}

	// A cancelled context fails both probes instantly, before either process
	// starts, and errWriter stays empty. Reporting "compose binary not found"
	// there is a lie that sends the operator hunting a healthy installation:
	// it is what the caller saw when a deployment was cancelled mid-flight and
	// its rollback ran on the same dead context.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("compose was not run: %w", ctxErr)
	}
	return nil, fmt.Errorf(
		"could not determine compose binary location tried %s and %s\nerr:%s",
		composeStandalone,
		composePlugin,
		errWriter.String(),
	)
}

type WithCmd func(curCmds []string) []string

func (c *Service) withCmd(
	ctx context.Context,
	filename string,
	stream io.Writer,
	addCmd WithCmd,
	services []string,
) error {
	return c.withCmdProgress(ctx, filename, stream, c.progressOut(), addCmd, services)
}

func (c *Service) withCmdProgress(
	ctx context.Context,
	filename string,
	stream io.Writer,
	progress string,
	addCmd WithCmd,
	services []string,
) error {
	return c.runCompose(ctx, filename, stream, progress, true, nil, addCmd, services)
}

// runBuildingAction runs a Compose action that may build. When the host has
// build limits and the stack builds something, it runs with Compose's builds
// routed to the limited builder; otherwise exactly as before.
func (c *Service) runBuildingAction(
	ctx context.Context,
	filename string,
	stream io.Writer,
	progress string,
	builds bool,
	addCmd WithCmd,
	services []string,
) error {
	if !builds || !c.buildLimits.Active() {
		return c.runCompose(ctx, filename, stream, progress, true, nil, addCmd, services)
	}
	fileParts, err := c.parser(filename, c.hostname)
	if err != nil {
		return err
	}
	return c.withLimitedBuilder(ctx, fileParts.Fs.Root(), stream, false, func(string) error {
		return c.runCompose(ctx, filename, stream, progress, true, limitedBuilderEnv(), addCmd, services)
	})
}

// captureCmd runs a Compose command and returns its standard output. Unlike
// withCmd it does not echo the command line into the stream, so callers that
// parse the result do not have to strip that first line back out - which
// Status and listIds both have to do, and which silently corrupts any output
// format that is not line-oriented.
func (c *Service) captureCmd(
	ctx context.Context,
	filename string,
	addCmd WithCmd,
	services []string,
) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := c.runCompose(ctx, filename, buf, "--progress=plain", false, nil, addCmd, services); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (c *Service) runCompose(
	ctx context.Context,
	filename string,
	stream io.Writer,
	progress string,
	echoCommand bool,
	// extraEnv is added to the command's environment, e.g. the builder
	// selection of a limited build; never secret values
	extraEnv []string,
	addCmd WithCmd,
	services []string,
) error {
	fileParts, err := c.parser(filename, c.hostname)
	if err != nil {
		return err
	}

	binary, err := c.version(ctx)
	if err != nil {
		return err
	}

	envs := loadEnvFile(fileParts.Fs, fileParts.Relpath)
	var secretEnvironment []string
	if c.environment != nil {
		secretEnvironment, err = c.environment(ctx, c.hostname, fileParts.Fs, fileParts.Relpath)
		if err != nil {
			return fmt.Errorf("load inline SOPS environment: %w", err)
		}
		// This drops the references so the values become collectable as soon as
		// the command returns, instead of living as long as the slice does. It
		// is not a wipe and cannot be one: Go strings are immutable, so the
		// bytes stay in the heap until the collector reuses that memory. The
		// scrubbing that does happen is on the decrypted buffers the secrets
		// package clears; what survives here are copies it had to make to build
		// KEY=value pairs for exec.
		defer func() {
			for index := range secretEnvironment {
				secretEnvironment[index] = ""
			}
		}()
	}

	// docker compose --envfile=... -f some/file/path/compose.yml --progress=<val>
	fullCmd := append(
		append(binary, envs...),
		progress,
		"-f", fileParts.Relpath,
	)

	fullCmd = addCmd(fullCmd)
	fullCmd = append(fullCmd, services...)

	var cleanCmd = make([]string, 0, len(fullCmd))
	var sb strings.Builder
	for _, cmd := range fullCmd {
		cl := strings.TrimSpace(cmd)
		if cl == "" {
			continue
		}

		cleanCmd = append(cleanCmd, cl)
		sb.WriteString(cl + " ")
	}

	if stream != nil && echoCommand {
		_, err = stream.Write([]byte(green(sb.String())))
		if err != nil {
			return fmt.Errorf("could not write to stream: %w", err)
		}
	}

	environment := secretEnvironment
	if len(extraEnv) > 0 {
		environment = append(slices.Clone(extraEnv), secretEnvironment...)
	}
	errWriter := new(bytes.Buffer)
	err = c.runner.Run(ctx, cleanCmd, fileParts.Fs.Root(), environment, stream, errWriter)
	if err != nil {
		message := strings.TrimSpace(errWriter.String())
		if message != "" {
			return fmt.Errorf("%s", message)
		}
		return fmt.Errorf("compose command failed: %w", err)
	}
	return nil
}

const envFileName = ".env"

func loadEnvFile(fs filesystem.FileSystem, filename string) []string {
	// remove leading '/' if left it will break filepath.dir
	filename = strings.TrimPrefix(filename, "/")
	var envPaths []string

	// some/relative/path/compose.yml
	start := filename
	for start != "." { // "." will return for empty
		// some/relative
		start = filepath.Dir(start) // some/relative/path
		// some/relative/path/.env
		envPath := fs.Join(start, envFileName)
		_, err := fs.Stat(envPath)
		if err == nil {
			absEnvPath := fs.Join(fs.Root(), envPath)
			envPaths = append(envPaths, "--env-file="+absEnvPath)
		}
	}

	// envs (lower) outer -> (higher) inner
	slices.Reverse(envPaths)

	return envPaths
}

var green = color.New(color.BgGreen).SprintlnFunc()

func (c *Service) Up(
	ctx context.Context,
	filename string,
	io io.Writer,
	services ...string,
) error {
	builds := c.mayBuild(ctx, filename)
	return c.explainBuildDenial(c.runBuildingAction(
		ctx, filename, io, c.actionProgress(builds), builds,
		func(cmdList []string) []string {
			return append(cmdList,
				"up", "-d", "-y",
				"--build", "--remove-orphans",
			)
		},
		services,
	), builds)
}

// DryRunUp validates the complete execution plan without changing containers,
// networks or images. It uses Compose's global --dry-run mode with the same
// options as Up so automatic Git deployment cannot skip the real plan check.
func (c *Service) DryRunUp(ctx context.Context, filename string, out io.Writer) error {
	return c.withCmdProgress(ctx, filename, out, "--progress=plain", func(cmdList []string) []string {
		return append(cmdList, "--dry-run", "up", "-d", "-y", "--build", "--remove-orphans")
	}, nil)
}

// UpWait applies the same controlled deployment as Up and asks Compose to wait
// until services are running or healthy. It is reserved for Git deployments
// with automatic rollback enabled; regular interactive actions keep their
// existing non-blocking behaviour.
func (c *Service) UpWait(ctx context.Context, filename string, out io.Writer) error {
	// already plain; the model is only read when limits make it matter
	builds := c.buildLimits.Active() && c.mayBuild(ctx, filename)
	err := c.runBuildingAction(ctx, filename, out, "--progress=plain", builds, func(cmdList []string) []string {
		return append(cmdList, "up", "-d", "-y", "--build", "--remove-orphans", "--wait", "--wait-timeout", "60")
	}, nil)
	// already plain, so without limits the model is only read to explain a
	// refused build
	if err != nil && strings.Contains(err.Error(), "403 Forbidden") {
		if !builds {
			builds = c.mayBuild(ctx, filename)
		}
		return c.explainBuildDenial(err, builds)
	}
	return err
}

// Redeploy runs `up -d` with explicit force flags so a stack can be
// re-rolled in one action: pull images, rebuild, or recreate containers
// even when nothing changed.
func (c *Service) Redeploy(
	ctx context.Context,
	filename string,
	out io.Writer,
	pull, build, recreate bool,
	services ...string,
) error {
	// build or not, up builds any buildable service whose image is missing
	builds := c.mayBuild(ctx, filename)
	return c.explainBuildDenial(c.runBuildingAction(
		ctx, filename, out, c.actionProgress(builds), builds,
		func(cmdList []string) []string {
			cmdList = append(cmdList, "up", "-d", "-y", "--remove-orphans")
			if pull {
				cmdList = append(cmdList, "--pull", "always")
			}
			if build {
				cmdList = append(cmdList, "--build")
			}
			if recreate {
				cmdList = append(cmdList, "--force-recreate")
			}
			return cmdList
		},
		services,
	), builds)
}

func (c *Service) Down(
	ctx context.Context,
	filename string,
	io io.Writer,
	services ...string,
) error {
	return c.withCmd(ctx, filename, io,
		func(cmdList []string) []string {
			return append(cmdList,
				"down", "--remove-orphans",
			)
		},
		services,
	)
}

// DownPlain is used by background recovery where terminal cursor sequences
// would otherwise be persisted verbatim in the deployment log.
func (c *Service) DownPlain(ctx context.Context, filename string, out io.Writer) error {
	return c.withCmdProgress(ctx, filename, out, "--progress=plain", func(cmdList []string) []string {
		return append(cmdList, "down", "--remove-orphans")
	}, nil)
}

func (c *Service) Start(
	ctx context.Context,
	filename string,
	io io.Writer,
	services ...string,
) error {
	return c.withCmd(
		ctx, filename, io,
		func(cmdList []string) []string {
			return append(cmdList, "start", "--wait")
		},
		services,
	)
}

func (c *Service) Stop(
	ctx context.Context,
	filename string,
	io io.Writer,
	services ...string,
) error {
	return c.withCmd(ctx, filename, io,
		func(cmdList []string) []string {
			return append(cmdList, "stop")
		},
		services,
	)
}

func (c *Service) Pull(
	ctx context.Context,
	filename string,
	io io.Writer,
	services ...string,
) error {
	return c.withCmd(
		ctx, filename, io,
		func(cmdList []string) []string {
			return append(
				cmdList,
				"pull",
				"--ignore-buildable", "--include-deps", "--ignore-pull-failures",
				"--policy", "always",
			)
		},
		services,
	)
}

func (c *Service) Restart(
	ctx context.Context,
	filename string,
	io io.Writer,
	services ...string,
) error {
	return c.withCmd(ctx, filename, io,
		func(cmdList []string) []string {
			return append(
				cmdList, "restart",
			)
		},
		services,
	)
}

func (c *Service) Update(
	ctx context.Context,
	filename string,
	io io.Writer,
	services ...string,
) error {
	err := c.Pull(ctx, filename, io, services...)
	if err != nil {
		return err
	}
	return c.Up(ctx, filename, io, services...)
}

func (c *Service) List(ctx context.Context, filename string) ([]container2.Summary, error) {
	lines, err := c.listIds(ctx, filename)
	if err != nil {
		return nil, err
	}
	return c.cont.ContainerListByIDs(ctx, lines...)
}

func (c *Service) Stats(ctx context.Context, filename string) ([]container.Stats, error) {
	// Match the stack's containers by the compose config-files label: one
	// ContainerList against the daemon instead of spawning a `docker compose
	// ps` subprocess plus a second listing on every stats poll.
	absPath, err := c.ComposeAbsPath(filename)
	if err != nil {
		return nil, err
	}
	ds, err := c.cont.ContainerListByComposeFile(ctx, absPath)
	if err != nil {
		return nil, err
	}

	return c.cont.ContainerGetStatsFromList(ctx, ds), nil
}

func (c *Service) listIds(ctx context.Context, filename string) ([]string, error) {
	sb := new(bytes.Buffer)
	err := c.withCmd(ctx, filename, sb,
		func(cmdList []string) []string {
			return append(
				cmdList,
				"ps", "-a", "--format", "{{.ID}}",
			)
		},
		[]string{},
	)
	if err != nil {
		return nil, err
	}

	output := sb.String()
	lines := strings.Split(output, "\n")
	return lines, err
}

func (c *Service) Status(ctx context.Context, filename string) (*StackState, error) {
	sb := new(bytes.Buffer)
	err := c.withCmd(ctx, filename, sb,
		func(cmdList []string) []string {
			return append(
				cmdList,
				"ps", "-a", "--format", "{{.State}} {{.Health}}",
			)
		},
		[]string{},
	)
	if err != nil {
		return nil, err
	}

	output := sb.String()
	lines := strings.Split(output, "\n")

	var stackState StackState

	// first element is the command for some dumbass reason
	// ["docker compose ps -a ...", "{status}", "", ...]
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}

		// Index 0: State (running, exited, etc)
		// Index 1: Health (healthy, unhealthy, starting) - might be missing
		state := parts[0]

		if state == "running" {
			stackState.UpCount++
		} else {
			stackState.DownCount++
		}

		if len(parts) > 1 {
			health := parts[1]
			if health == "healthy" {
				stackState.HealthyCount++
			} else if health == "unhealthy" {
				stackState.UnhealthyCount++
			}
		}
	}

	return &stackState, err
}

type StackState struct {
	UpCount        uint
	DownCount      uint
	HealthyCount   uint
	UnhealthyCount uint
}

// ComposeAbsPath returns the absolute path of the compose file as Docker Compose
// records it in the com.docker.compose.project.config_files label. It reuses the
// exact resolution the compose commands use (working dir = Fs.Root(), -f Relpath),
// so running containers can be matched back to their compose file from a single
// container listing — no per-stack `docker compose ps` process.
func (c *Service) ComposeAbsPath(filename string) (string, error) {
	parts, err := c.parser(filename, c.hostname)
	if err != nil {
		return "", err
	}
	return parts.Fs.Join(parts.Fs.Root(), parts.Relpath), nil
}

func (c *Service) Validate(ctx context.Context, filename string) []error {
	buf := new(bytes.Buffer)
	err := c.withCmd(ctx, filename, buf,
		func(cmdList []string) []string {
			return append(
				cmdList, "config", "--quiet",
			)
		},
		[]string{},
	)
	if err == nil {
		return []error{}
	}

	fileErr := fmt.Errorf("failed to validate compose file: %w", err)
	// todo more validations

	return []error{fileErr}
}

// todo validate ports
//	var errs []error
//
//	project, err := s.LoadProject(ctx, shortName)
//	if err != nil {
//		return append(errs, err)
//	}
//
//	runningContainers, err := s.cont.ContainersList(ctx)
//	if err != nil {
//		return append(errs, err)
//	}
//
//	for svcName, svc := range project.Services {
//		for _, portConfig := range svc.Ports {
//			published, err := strconv.Atoi(portConfig.Published)
//			if err != nil {
//				errs = append(errs, fmt.Errorf("invalid port %q in service %s: %w", portConfig.Published, svcName, err))
//				continue
//			}
//
//			// check running Containers using this port
//			conflicts := s.findConflictingContainers(runningContainers, svcName, uint16(published))
//			for _, c := range conflicts {
//				errs = append(errs, fmt.Errorf(
//					"service %q wants port %d, but container %q (id=%s) is already using it",
//					svcName, published, c.Names[0], c.ID[:12],
//				))
//			}
//		}
//	}
//
//	return errs
//}
//
//// findConflictingContainers returns containers using the given port but not matching the service name
//func (s *Service) findConflictingContainers(containers []container.Summary, serviceName string, port uint16) []container.Summary {
//	var matches []container.Summary
//	for _, c := range containers {
//		for _, p := range c.Ports {
//			if p.PublicPort == port {
//				// container names have leading "/" -> strip when comparing
//				containerName := c.Names[0]
//				if len(containerName) > 0 && containerName[0] == '/' {
//					containerName = containerName[1:]
//				}
//
//				serviceLabel := c.Labels[api.ServiceLabel]
//				if serviceLabel != serviceName {
//					matches = append(matches, c)
//				}
//			}
//		}
//	}
//
//	return matches
//}
//}

//func (s *Service) LoadProject(ctx context.Context, resourcePath string) (*types.Project, error) {
//	// fsCli is a file system
//	fsCli, relpath, err := s.getFs(resourcePath)
//	if err != nil {
//		return nil, err
//	}
//	// will be the parent dir of the compose file else equal to compose root
//	workingDir := filepath.Dir(relpath)
//
//	var finalEnv []string
//	for _, file := range []string{
//		// Global .env
//		// todo
//		//filepath.Join("s.ComposeRoot", ".env"),
//		// Subdirectory .env (will override global)
//		filepath.Join(filename, ".env"),
//	} {
//		if fileutil.FileExists(file) {
//			finalEnv = append(finalEnv, file)
//		}
//	}
//
//	fsLoader := FSResourceLoader{
//		Fs: fsCli,
//	}
//
//	options, err := cli.NewProjectOptions(
//		[]string{relpath},
//		cli.WithLoadOptions(
//			func(options *loader.Options) {
//				options.ResourceLoaders = []loader.ResourceLoader{&fsLoader}
//			}),
//		// important maintain this order to load .env properly
//		// highest 										lowest
//		// working-dir .env <- compose root .env <- os envs
//		cli.WithEnvFiles(finalEnv...),
//		cli.WithDotEnv,
//		cli.WithOsEnv,
//		// compose operations will take place in working dir
//		cli.WithWorkingDirectory(workingDir),
//		// other shit
//		cli.WithDefaultProfiles(),
//		cli.WithResolvedPaths(true),
//	)
//	if err != nil {
//		return nil, fmt.Errorf("failed to create new project: %w", err)
//	}
//
//	project, err := options.LoadProject(ctx)
//	if err != nil {
//		return nil, fmt.Errorf("failed to load project: %w", err)
//	}
//
//	addServiceLabels(project)
//	// Ensure service environment variables
//	project, err = project.WithServicesEnvironmentResolved(true)
//	if err != nil {
//		return nil, fmt.Errorf("failed to resolve services environment: %w", err)
//	}
//
//	return project.WithoutUnnecessaryResources(), nil
//}
