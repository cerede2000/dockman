package compose

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"sync/atomic"
	"time"
)

const dockmanDockerfilePrefix = "dockman://"

// Keep Buildx's builder selection isolated from Dockman's persistent Docker
// CLI configuration. A user-selected docker-container builder may otherwise
// be named "default" and start a permanent buildx_buildkit_default helper.
// With an empty Buildx state directory, Buildx automatically exposes the
// current Docker context through the daemon-integrated `docker` driver.
const dockmanNativeBuildxConfig = "/tmp/dockman-buildx-native"

var dockmanBuildxSequence atomic.Uint64

// RunDockerCommand executes a user-provided docker CLI command line on this
// host through the same runner compose uses (local exec or ssh), streaming
// its combined output. Only the docker binary is allowed.
func (c *Service) RunDockerCommand(ctx context.Context, rawCommand string, stream io.Writer) error {
	args, err := splitCommandLine(rawCommand)
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] != "docker" {
		return fmt.Errorf("only docker commands are allowed, e.g. docker run --rm nginx:alpine")
	}
	wd := "."
	args, wd, err = c.prepareDockerBuild(args)
	if err != nil {
		return err
	}
	return c.runDockerCLI(ctx, args, wd, stream)
}

// RunDockerfileBuild executes the safe, non-interactive build used by the
// Files browser. The daemon-backed default Buildx builder is preferred when
// available. Otherwise Dockman creates a job-scoped builder (also required
// for host networking), then removes it after the build. The context is
// resolved from the selected Dockerfile and loaded into the selected host.
func (c *Service) RunDockerfileBuild(ctx context.Context, filename, imageTag, networkMode string, stream io.Writer) error {
	args := []string{
		"docker", "buildx", "build", "--load", "--progress=plain",
	}
	if networkMode == "host" {
		args = append(args, "--network=host")
	} else {
		networkMode = "default"
	}
	args = append(args, "--tag", imageTag, "--file", dockmanDockerfilePrefix+filename, ".")
	args, wd, err := c.prepareDockerBuild(args)
	if err != nil {
		return err
	}
	driver, currentBuilder := c.dockmanBuildxBuilder(ctx, wd)
	if stream != nil {
		_, _ = fmt.Fprintf(stream, "*** Buildx driver: %s ***\n", driver)
		_, _ = fmt.Fprintf(stream, "*** Build network: %s ***\n", networkMode)
	}

	// Host networking is an explicitly privileged BuildKit entitlement. The
	// daemon-backed driver cannot be configured per build, while a named
	// docker-container builder can be granted the entitlement narrowly and
	// removed as soon as this job ends. A named builder also avoids hijacking
	// the reserved Docker-context builder named "default".
	builderName := ""
	if driver != "docker" || networkMode == "host" {
		builderName = newDockmanBuildxBuilderName()
		if err := c.createDockmanBuildxBuilder(ctx, wd, builderName, networkMode == "host"); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = c.cleanupDockmanBuildxHelper(cleanupCtx, wd, builderName)
			return err
		}
		args = append(args[:3], append([]string{"--builder", builderName}, args[3:]...)...)
		if networkMode == "host" {
			args = append(args[:5], append([]string{"--allow=network.host"}, args[5:]...)...)
		}
	} else if currentBuilder != "" {
		// Name the daemon-integrated builder rather than letting Buildx resolve
		// it implicitly from its state directory.
		args = append(args[:3], append([]string{"--builder", currentBuilder}, args[3:]...)...)
	}
	args = dockmanNativeBuildxArgs(args)
	buildErr := c.runDockerCLI(ctx, args, wd, stream)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cleanupErr := c.cleanupDockmanBuildxHelper(cleanupCtx, wd, builderName)
	if cleanupErr != nil && stream != nil {
		_, _ = fmt.Fprintf(stream, "\n*** Buildx helper cleanup failed: %v ***\n", cleanupErr)
	}
	// Helper cleanup is best-effort housekeeping. It must remain visible in
	// the build log, but it must not turn a successfully built image into a
	// failed job when a restrictive socket proxy refuses container deletion.
	return buildErr
}

func newDockmanBuildxBuilderName() string {
	return fmt.Sprintf("dockman-%d-%d", time.Now().UnixNano(), dockmanBuildxSequence.Add(1))
}

func (c *Service) createDockmanBuildxBuilder(ctx context.Context, wd, builderName string, allowHostNetwork bool) error {
	args := []string{"docker", "buildx", "create", "--name", builderName, "--driver", "docker-container"}
	if allowHostNetwork {
		args = append(args, "--buildkitd-flags", "--allow-insecure-entitlement network.host")
	}
	var output bytes.Buffer
	if err := c.runner.Run(ctx, dockmanNativeBuildxArgs(args), wd, nil, io.Discard, &output); err != nil {
		message := strings.TrimSpace(output.String())
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("create isolated Buildx builder: %s", message)
	}
	return nil
}

func dockmanNativeBuildxArgs(args []string) []string {
	return append([]string{"env", "BUILDX_CONFIG=" + dockmanNativeBuildxConfig, "BUILDX_BUILDER="}, args...)
}

// dockmanBuildxBuilder reports the builder selected from Dockman's isolated
// Buildx state, by driver and by name. The first builder row is the current
// Docker context builder; node rows use the context endpoint rather than a
// driver name.
//
// The name matters as much as the driver: naming the builder on the command
// line means Buildx never has to pick the current instance out of its state
// directory, which lives at a predictable path and could therefore be
// pre-created by another local user to point the implicit builder elsewhere.
func (c *Service) dockmanBuildxBuilder(ctx context.Context, wd string) (string, string) {
	var output bytes.Buffer
	args := dockmanNativeBuildxArgs([]string{"docker", "buildx", "ls", "--format", "{{.Name}}|{{.DriverEndpoint}}"})
	if err := c.runner.Run(ctx, args, wd, nil, &output, io.Discard); err != nil {
		return "unknown", ""
	}
	for _, line := range strings.Split(output.String(), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "|", 2)
		if len(parts) != 2 {
			continue
		}
		driver := strings.TrimSpace(parts[1])
		if driver == "docker" || driver == "docker-container" || driver == "kubernetes" || driver == "remote" {
			return driver, strings.TrimSpace(parts[0])
		}
	}
	return "unknown", ""
}

// cleanupDockmanBuildxHelper removes the ephemeral builder this job created,
// and nothing else.
//
// It used to also run `docker rm --force buildx_buildkit_default` on every
// build. Dockman cannot create that container: its Buildx state is isolated,
// BUILDX_BUILDER is empty and the builder is now always named explicitly, so
// a container by that name belongs to the user's own docker-container builder
// called `default`. Force-removing it destroyed their builder, and did so once
// per build. A leftover from a Dockman old enough to have created one has to be
// removed by hand — once — rather than by guessing at a name forever.
func (c *Service) cleanupDockmanBuildxHelper(ctx context.Context, wd, builderName string) error {
	if builderName == "" {
		return nil
	}
	var output bytes.Buffer
	args := dockmanNativeBuildxArgs([]string{"docker", "buildx", "rm", "--force", builderName})
	if err := c.runner.Run(ctx, args, wd, nil, nil, &output); err != nil {
		return fmt.Errorf("remove isolated builder %s: %s", builderName, strings.TrimSpace(output.String()))
	}
	return nil
}

func (c *Service) runDockerCLI(ctx context.Context, args []string, wd string, stream io.Writer) error {
	if stream != nil {
		if _, err := stream.Write([]byte(green(strings.Join(args, " ")))); err != nil {
			return fmt.Errorf("could not write to stream: %w", err)
		}
	}

	errWriter := new(bytes.Buffer)
	if err := c.runner.Run(ctx, args, wd, nil, stream, errWriter); err != nil {
		if errWriter.Len() > 0 {
			return fmt.Errorf("%s", errWriter.String())
		}
		return err
	}
	return nil
}

// prepareDockerBuild upgrades the legacy `docker build` spelling to Buildx.
// It uses Buildx's daemon-backed default builder, avoiding the standalone
// BuildKit container created by the docker-container driver. Explicit
// `docker buildx build` commands remain untouched so advanced users can keep
// their own builder. The
// dockman:// marker never reaches the Docker CLI; it maps an alias-relative
// browser path to the real local or SSH host directory.
func (c *Service) prepareDockerBuild(args []string) ([]string, string, error) {
	if len(args) < 2 || args[0] != "docker" {
		return args, ".", nil
	}
	if args[1] == "build" {
		args = append([]string{"docker", "buildx", "build"}, args[2:]...)
		args = append(args[:3], append([]string{"--builder", "default"}, args[3:]...)...)
		if !hasBuildOutputOption(args[3:]) {
			args = append(args[:3], append([]string{"--load"}, args[3:]...)...)
		}
	}
	if len(args) < 3 || args[1] != "buildx" || args[2] != "build" {
		return args, ".", nil
	}

	wd := "."
	for index := 3; index < len(args); index++ {
		valueIndex := -1
		switch {
		case (args[index] == "-f" || args[index] == "--file") && index+1 < len(args):
			valueIndex = index + 1
		case strings.HasPrefix(args[index], "--file="):
			valueIndex = index
		}
		if valueIndex < 0 {
			continue
		}
		value := args[valueIndex]
		inline := strings.HasPrefix(value, "--file=")
		if inline {
			value = strings.TrimPrefix(value, "--file=")
		}
		if !strings.HasPrefix(value, dockmanDockerfilePrefix) {
			continue
		}
		filename := strings.TrimPrefix(value, dockmanDockerfilePrefix)
		fileParts, err := c.parser(filename, c.hostname)
		if err != nil {
			return nil, "", fmt.Errorf("resolve Dockerfile %q: %w", filename, err)
		}
		info, err := fileParts.Fs.Stat(fileParts.Relpath)
		if err != nil {
			return nil, "", fmt.Errorf("read Dockerfile %q: %w", filename, err)
		}
		if !info.Mode().IsRegular() {
			return nil, "", fmt.Errorf("Dockerfile %q is not a regular file", filename)
		}
		wd = path.Dir(fileParts.Fs.Join(fileParts.Fs.Root(), fileParts.Relpath))
		base := path.Base(strings.ReplaceAll(fileParts.Relpath, "\\", "/"))
		if inline {
			args[valueIndex] = "--file=" + base
		} else {
			args[valueIndex] = base
		}
	}
	return args, wd, nil
}

func hasBuildOutputOption(args []string) bool {
	for _, arg := range args {
		if arg == "--load" || arg == "--push" || arg == "-o" || arg == "--output" || strings.HasPrefix(arg, "-o=") || strings.HasPrefix(arg, "--output=") {
			return true
		}
	}
	return false
}

// splitCommandLine splits a command line into arguments, honoring single
// quotes, double quotes and backslash escapes (outside single quotes)
func splitCommandLine(input string) ([]string, error) {
	var args []string
	var current strings.Builder
	inArg := false

	const (
		modePlain = iota
		modeSingle
		modeDouble
	)
	mode := modePlain

	flush := func() {
		if inArg {
			args = append(args, current.String())
			current.Reset()
			inArg = false
		}
	}

	runes := []rune(input)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		switch mode {
		case modeSingle:
			if ch == '\'' {
				mode = modePlain
			} else {
				current.WriteRune(ch)
			}
		case modeDouble:
			if ch == '"' {
				mode = modePlain
			} else if ch == '\\' && i+1 < len(runes) && (runes[i+1] == '"' || runes[i+1] == '\\') {
				i++
				current.WriteRune(runes[i])
			} else {
				current.WriteRune(ch)
			}
		default:
			switch {
			case ch == ' ' || ch == '\t' || ch == '\n':
				flush()
			case ch == '\'':
				mode = modeSingle
				inArg = true
			case ch == '"':
				mode = modeDouble
				inArg = true
			case ch == '\\' && i+1 < len(runes):
				i++
				current.WriteRune(runes[i])
				inArg = true
			default:
				current.WriteRune(ch)
				inArg = true
			}
		}
	}
	if mode != modePlain {
		return nil, fmt.Errorf("unbalanced quote in command")
	}
	flush()
	return args, nil
}

// PullImage pulls an image through the host's docker CLI in the compose
// runner context, so registry credentials (docker login, credential
// helpers) apply exactly as they do for compose — a bare daemon API pull
// is unauthenticated and fails on private registries.
func (c *Service) PullImage(ctx context.Context, imageTag string, out io.Writer) error {
	errWriter := new(bytes.Buffer)
	if err := c.runner.Run(ctx, []string{"docker", "pull", imageTag}, ".", nil, out, errWriter); err != nil {
		if errWriter.Len() > 0 {
			return fmt.Errorf("%s", strings.TrimSpace(errWriter.String()))
		}
		return err
	}
	return nil
}
