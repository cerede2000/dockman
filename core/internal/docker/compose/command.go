package compose

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
)

const dockmanDockerfilePrefix = "dockman://"

// Keep Buildx's builder selection isolated from Dockman's persistent Docker
// CLI configuration. A user-selected docker-container builder may otherwise
// be named "default" and start a permanent buildx_buildkit_default helper.
// With an empty Buildx state directory, Buildx automatically exposes the
// current Docker context through the daemon-integrated `docker` driver.
const dockmanNativeBuildxConfig = "/tmp/dockman-buildx-native"

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
// Files browser. The daemon-backed default Buildx builder does not create a
// standalone BuildKit container. The context is resolved from the selected
// Dockerfile, and the result is loaded into the selected Docker host.
func (c *Service) RunDockerfileBuild(ctx context.Context, filename, imageTag string, stream io.Writer) error {
	args := []string{
		"docker", "buildx", "build", "--builder", "default", "--load", "--progress=plain",
		"--tag", imageTag, "--file", dockmanDockerfilePrefix + filename, ".",
	}
	args, wd, err := c.prepareDockerBuild(args)
	if err != nil {
		return err
	}
	args = append([]string{"env", "BUILDX_CONFIG=" + dockmanNativeBuildxConfig}, args...)
	return c.runDockerCLI(ctx, args, wd, stream)
}

func (c *Service) runDockerCLI(ctx context.Context, args []string, wd string, stream io.Writer) error {
	if stream != nil {
		if _, err := stream.Write([]byte(green(strings.Join(args, " ")))); err != nil {
			return fmt.Errorf("could not write to stream: %w", err)
		}
	}

	errWriter := new(bytes.Buffer)
	if err := c.runner.Run(ctx, args, wd, stream, errWriter); err != nil {
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
	if err := c.runner.Run(ctx, []string{"docker", "pull", imageTag}, ".", out, errWriter); err != nil {
		if errWriter.Len() > 0 {
			return fmt.Errorf("%s", strings.TrimSpace(errWriter.String()))
		}
		return err
	}
	return nil
}
