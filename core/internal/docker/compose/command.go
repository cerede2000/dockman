package compose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync/atomic"
	"time"
)

const dockmanDockerfilePrefix = "dockman://"

var dockmanBuilderSequence atomic.Uint64

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
	args, wd, managedBuild, err := c.prepareDockerBuild(args)
	if err != nil {
		return err
	}
	if managedBuild {
		return c.runManagedDockerBuild(ctx, args, wd, stream)
	}
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

// runManagedDockerBuild uses a builder owned only by this build invocation.
// The docker-container driver is portable across direct sockets, socketproxy
// and SSH hosts; removing the builder in an independent cleanup context also
// removes its BuildKit container after success, failure or client disconnect.
// User-selected builders are never modified.
func (c *Service) runManagedDockerBuild(ctx context.Context, args []string, wd string, stream io.Writer) error {
	name := fmt.Sprintf("dockman-%x-%x", time.Now().UnixNano(), dockmanBuilderSequence.Add(1))
	args = setBuilderOption(args, name)
	if err := c.runner.Run(ctx, []string{"docker", "buildx", "create", "--name", name, "--driver", "docker-container"}, wd, nil, io.Discard); err != nil {
		return fmt.Errorf("create temporary Buildx builder: %w", err)
	}

	buildErr := c.runDockerCLI(ctx, args, wd, stream)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cleanupErr := c.removeManagedBuilder(cleanupCtx, name, wd)
	if cleanupErr != nil && stream != nil {
		_, _ = fmt.Fprintf(stream, "\nwarning: %v\n", cleanupErr)
	}
	return errors.Join(buildErr, cleanupErr)
}

func (c *Service) removeManagedBuilder(ctx context.Context, name, wd string) error {
	var output bytes.Buffer
	if err := c.runner.Run(ctx, []string{"docker", "buildx", "rm", "--force", name}, wd, nil, &output); err == nil {
		return nil
	}

	// A cancelled/failed build can leave Buildx metadata inconsistent. Remove
	// the uniquely named helper directly, then retry metadata cleanup. This
	// target can only belong to the Dockman invocation above.
	containerName := "buildx_buildkit_" + name + "0"
	output.Reset()
	removeErr := c.runner.Run(ctx, []string{"docker", "rm", "--force", containerName}, wd, nil, &output)
	output.Reset()
	metadataErr := c.runner.Run(ctx, []string{"docker", "buildx", "rm", "--force", name}, wd, nil, &output)
	if removeErr != nil && metadataErr != nil {
		return fmt.Errorf("remove temporary Buildx builder %q: container cleanup: %v; metadata cleanup: %v", name, removeErr, metadataErr)
	}
	return nil
}

// prepareDockerBuild upgrades the legacy `docker build` spelling to Buildx.
// Builds initiated by Dockman's file browser are marked as managed so they use
// a uniquely named, short-lived builder that is always cleaned afterwards.
// Explicit `docker buildx build` commands without a dockman:// path remain
// untouched so advanced users can keep their own persistent builder. The
// dockman:// marker never reaches the Docker CLI; it maps an alias-relative
// browser path to the real local or SSH host directory.
func (c *Service) prepareDockerBuild(args []string) ([]string, string, bool, error) {
	if len(args) < 2 || args[0] != "docker" {
		return args, ".", false, nil
	}
	managedBuild := args[1] == "build"
	if args[1] == "build" {
		args = append([]string{"docker", "buildx", "build"}, args[2:]...)
		if !hasBuildOutputOption(args[3:]) {
			args = append(args[:3], append([]string{"--load"}, args[3:]...)...)
		}
	}
	if len(args) < 3 || args[1] != "buildx" || args[2] != "build" {
		return args, ".", false, nil
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
		managedBuild = true
		filename := strings.TrimPrefix(value, dockmanDockerfilePrefix)
		fileParts, err := c.parser(filename, c.hostname)
		if err != nil {
			return nil, "", false, fmt.Errorf("resolve Dockerfile %q: %w", filename, err)
		}
		info, err := fileParts.Fs.Stat(fileParts.Relpath)
		if err != nil {
			return nil, "", false, fmt.Errorf("read Dockerfile %q: %w", filename, err)
		}
		if !info.Mode().IsRegular() {
			return nil, "", false, fmt.Errorf("Dockerfile %q is not a regular file", filename)
		}
		wd = path.Dir(fileParts.Fs.Join(fileParts.Fs.Root(), fileParts.Relpath))
		base := path.Base(strings.ReplaceAll(fileParts.Relpath, "\\", "/"))
		if inline {
			args[valueIndex] = "--file=" + base
		} else {
			args[valueIndex] = base
		}
	}
	return args, wd, managedBuild, nil
}

func setBuilderOption(args []string, name string) []string {
	result := make([]string, 0, len(args)+2)
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--builder" {
			if index+1 < len(args) {
				index++
			}
			continue
		}
		if strings.HasPrefix(arg, "--builder=") {
			continue
		}
		result = append(result, arg)
	}
	return append(result[:3], append([]string{"--builder", name}, result[3:]...)...)
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
