package compose

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/stretchr/testify/require"
)

type commandCaptureRunner struct {
	args      []string
	wd        string
	calls     [][]string
	failBuild bool
	driver    string
}

func (r *commandCaptureRunner) Run(_ context.Context, args []string, wd string, out, _ io.Writer) error {
	r.args = append([]string(nil), args...)
	r.wd = wd
	r.calls = append(r.calls, append([]string(nil), args...))
	joined := strings.Join(args, " ")
	if r.driver != "" && strings.Contains(joined, "docker buildx ls --format") {
		if out != nil {
			_, _ = io.WriteString(out, "default|"+r.driver+"\ndefault|default\n")
		}
	}
	if r.failBuild && strings.Contains(joined, "docker buildx build") {
		return errors.New("build failed")
	}
	return nil
}

func TestSplitCommandLine(t *testing.T) {
	args, err := splitCommandLine(`docker run --rm -p 8080:80 nginx:alpine`)
	require.NoError(t, err)
	require.Equal(t, []string{"docker", "run", "--rm", "-p", "8080:80", "nginx:alpine"}, args)

	// double quotes keep spaces, backslash escapes work inside them
	args, err = splitCommandLine(`docker run -e "GREETING=hello world" -e NAME=\"quoted\" img`)
	require.NoError(t, err)
	require.Equal(t, []string{"docker", "run", "-e", "GREETING=hello world", "-e", `NAME="quoted"`, "img"}, args)

	// single quotes are literal
	args, err = splitCommandLine(`docker run -e 'A=$HOME and "stuff"' img`)
	require.NoError(t, err)
	require.Equal(t, []string{"docker", "run", "-e", `A=$HOME and "stuff"`, "img"}, args)

	// collapsed whitespace, tabs, empty quoted args
	args, err = splitCommandLine("docker   ps\t-a ''")
	require.NoError(t, err)
	require.Equal(t, []string{"docker", "ps", "-a", ""}, args)

	// escaped space outside quotes
	args, err = splitCommandLine(`docker run -v /my\ path:/data img`)
	require.NoError(t, err)
	require.Equal(t, []string{"docker", "run", "-v", "/my path:/data", "img"}, args)

	// unbalanced quotes are rejected
	_, err = splitCommandLine(`docker run "unterminated`)
	require.Error(t, err)

	// empty input
	args, err = splitCommandLine("   ")
	require.NoError(t, err)
	require.Empty(t, args)
}

func TestDockerBuildUsesBuildxAndLoadsTheImage(t *testing.T) {
	runner := &commandCaptureRunner{driver: "docker"}
	service := &Service{runner: runner}
	require.NoError(t, service.RunDockerCommand(context.Background(), "docker build -t demo:local .", io.Discard))
	require.Equal(t, [][]string{{"docker", "buildx", "build", "--load", "--builder", "default", "-t", "demo:local", "."}}, runner.calls)
	require.Equal(t, ".", runner.wd)
}

func TestDockmanDockerfileBuildUsesItsRealDirectory(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "apple music")
	require.NoError(t, os.MkdirAll(directory, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "Dockerfile"), []byte("FROM scratch\n"), 0o600))
	runner := &commandCaptureRunner{driver: "docker"}
	service := &Service{
		hostname: "local",
		runner:   runner,
		parser: func(filename, _ string) (Host, error) {
			return Host{Fs: filesystem.NewLocal(root), Relpath: strings.TrimPrefix(filename, "compose/")}, nil
		},
	}
	var output bytes.Buffer
	err := service.RunDockerfileBuild(context.Background(), "compose/apple music/Dockerfile", "apple-music-rip:local", "default", &output)
	require.NoError(t, err)
	require.Equal(t, directory, runner.wd)
	require.Equal(t, [][]string{
		{"env", "BUILDX_CONFIG=/tmp/dockman-buildx-native", "BUILDX_BUILDER=", "docker", "buildx", "ls", "--format", "{{.Name}}|{{.DriverEndpoint}}"},
		{"env", "BUILDX_CONFIG=/tmp/dockman-buildx-native", "BUILDX_BUILDER=", "docker", "buildx", "build", "--load", "--progress=plain", "--tag", "apple-music-rip:local", "--file", "Dockerfile", "."},
		{"docker", "rm", "--force", "buildx_buildkit_default"},
	}, runner.calls)
	require.NotContains(t, output.String(), dockmanDockerfilePrefix, "internal browser paths must not be exposed to the Docker CLI or logs")
	require.Contains(t, output.String(), "Buildx driver: docker")
}

func TestDockmanDockerfileBuildCanUseHostNetworkExplicitly(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\n"), 0o600))
	runner := &commandCaptureRunner{driver: "docker"}
	service := &Service{
		hostname: "local",
		runner:   runner,
		parser: func(filename, _ string) (Host, error) {
			return Host{Fs: filesystem.NewLocal(root), Relpath: filename}, nil
		},
	}

	require.NoError(t, service.RunDockerfileBuild(context.Background(), "Dockerfile", "demo:host", "host", io.Discard))
	require.Equal(t, []string{
		"env", "BUILDX_CONFIG=/tmp/dockman-buildx-native", "BUILDX_BUILDER=", "docker", "buildx", "build", "--load", "--progress=plain", "--network=host", "--tag", "demo:host", "--file", "Dockerfile", ".",
	}, runner.calls[1])
}

func TestDockerContainerDriverIsRemovedAfterBuild(t *testing.T) {
	runner := &commandCaptureRunner{}
	service := &Service{runner: runner}
	require.NoError(t, service.cleanupDockmanBuildxHelper(context.Background(), ".", "docker-container"))
	require.Equal(t, [][]string{
		{"env", "BUILDX_CONFIG=/tmp/dockman-buildx-native", "BUILDX_BUILDER=", "docker", "buildx", "rm", "--force", "default"},
		{"docker", "rm", "--force", "buildx_buildkit_default"},
	}, runner.calls)
}

func TestDockerfileBuildCleansHelperAfterFailure(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\n"), 0o600))
	runner := &commandCaptureRunner{driver: "docker-container", failBuild: true}
	service := &Service{
		hostname: "local",
		runner:   runner,
		parser: func(filename, _ string) (Host, error) {
			return Host{Fs: filesystem.NewLocal(root), Relpath: filename}, nil
		},
	}

	err := service.RunDockerfileBuild(context.Background(), "Dockerfile", "demo:broken", "default", io.Discard)
	require.ErrorContains(t, err, "build failed")
	require.Equal(t, [][]string{
		{"env", "BUILDX_CONFIG=/tmp/dockman-buildx-native", "BUILDX_BUILDER=", "docker", "buildx", "ls", "--format", "{{.Name}}|{{.DriverEndpoint}}"},
		{"env", "BUILDX_CONFIG=/tmp/dockman-buildx-native", "BUILDX_BUILDER=", "docker", "buildx", "build", "--load", "--progress=plain", "--tag", "demo:broken", "--file", "Dockerfile", "."},
		{"env", "BUILDX_CONFIG=/tmp/dockman-buildx-native", "BUILDX_BUILDER=", "docker", "buildx", "rm", "--force", "default"},
		{"docker", "rm", "--force", "buildx_buildkit_default"},
	}, runner.calls)
}

func TestDockerBuildPreservesExplicitPushOutput(t *testing.T) {
	runner := &commandCaptureRunner{}
	service := &Service{runner: runner}
	require.NoError(t, service.RunDockerCommand(context.Background(), "docker build --push -t registry.example/demo:latest .", io.Discard))
	require.Equal(t, [][]string{{"docker", "buildx", "build", "--builder", "default", "--push", "-t", "registry.example/demo:latest", "."}}, runner.calls)
}

func TestExplicitBuildxCommandKeepsUserBuilderUntouched(t *testing.T) {
	runner := &commandCaptureRunner{}
	service := &Service{runner: runner}
	require.NoError(t, service.RunDockerCommand(context.Background(), "docker buildx build --builder team-builder --push -t registry.example/demo:latest .", io.Discard))
	require.Equal(t, [][]string{{"docker", "buildx", "build", "--builder", "team-builder", "--push", "-t", "registry.example/demo:latest", "."}}, runner.calls)
}

func TestDefaultBuilderBuildFailureDoesNotCreateHelperContainer(t *testing.T) {
	runner := &commandCaptureRunner{failBuild: true}
	service := &Service{runner: runner}
	require.Error(t, service.RunDockerCommand(context.Background(), "docker build -t demo:broken .", io.Discard))
	require.Equal(t, [][]string{{"docker", "buildx", "build", "--load", "--builder", "default", "-t", "demo:broken", "."}}, runner.calls)
}
