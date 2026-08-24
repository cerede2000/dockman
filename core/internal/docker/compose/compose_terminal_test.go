package compose

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"testing"

	"github.com/RA341/dockman/internal/docker/container"
	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

var termCli *Service
var ctx = context.Background()

// var rwc = TermRWC()
var cont *container.Service

func init() {
	//local := LocalRunner{}
	c, err := client.New()
	if err != nil {
		log.Fatal(err)
	}

	cont = container.New(c)
	termCli = NewComposeTerminal(
		"local",
		cont,
		func(filename string, host string) (Host, error) {
			return Host{
				Fs:      filesystem.NewLocal(working),
				Relpath: com,
			}, nil
		},
		nil,
	)

	loadEnvFile(filesystem.NewLocal(working), "/some/rooted/path/"+com)
}

func TestVersion(t *testing.T) {
	version, err := termCli.version(context.Background())
	require.NoErrorf(t, err, "version error: %v", err)
	require.Equal(t, version, composePlugin)
}

const working = "samples"
const com = "test-compose.yaml"

func TestUp(t *testing.T) {
	err := termCli.Up(ctx, com, os.Stdout, "some", "service")
	require.NoError(t, err)
}

//func TestDown(t *testing.T) {
//	err := termCli.Up(ctx, working, com, rwc, []string{})
//	require.NoError(t, err)
//	err = termCli.Down(ctx, working, com, rwc)
//	require.NoError(t, err)
//}
//
//func TestLs(t *testing.T) {
//	err := termCli.Up(ctx, working, com, rwc, []string{})
//	require.NoError(t, err)
//
//	ids, err := termCli.listIds(ctx, working, com)
//	require.NoError(t, err)
//	require.NotEqual(t, len(ids), 0)
//	t.Log(ids)
//
//	err = termCli.Down(ctx, working, com, rwc)
//	require.NoError(t, err)
//}
//
//func TestValidate(t *testing.T) {
//	out, err := termCli.Validate(ctx, working)
//	require.NoError(t, err)
//
//	t.Log(out)
//}
//
//func TestStartStop(t *testing.T) {
//	err := termCli.Up(ctx, working, com, rwc, []string{})
//	require.NoError(t, err)
//
//	time.Sleep(1 * time.Second)
//
//	err = termCli.Stop(ctx, working, com, rwc)
//	require.NoError(t, err)
//
//	time.Sleep(1 * time.Second)
//
//	err = termCli.Start(ctx, working, com, rwc, []string{})
//	require.NoError(t, err)
//}
//
//type RWC struct {
//	io.Reader
//	io.Writer
//}
//
//func TermRWC() RWC {
//	return RWC{
//		Reader: os.Stdin,
//		Writer: os.Stdout,
//	}
//}

// A cancelled context fails both binary probes before either process starts,
// leaving the captured stderr empty. Reporting "could not determine compose
// binary location" there is a lie: it sent an operator hunting a healthy
// compose installation while the real cause was a deployment cancelled
// mid-flight whose rollback ran on the same dead context.
type contextEchoRunner struct{ calls int }

func (r *contextEchoRunner) Run(ctx context.Context, _ []string, _ string, _ []string, _ io.Writer, _ io.Writer) error {
	r.calls++
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("exec: \"docker\": executable file not found in $PATH")
}

func TestComposeVersionReportsCancellationInsteadOfMissingBinary(t *testing.T) {
	runner := &contextEchoRunner{}
	service := &Service{runner: runner}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := service.version(cancelled)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	require.NotContains(t, err.Error(), "could not determine compose binary location",
		"a cancelled context must not be reported as a missing compose binary")
}

func TestComposeVersionStillReportsAGenuinelyMissingBinary(t *testing.T) {
	runner := &contextEchoRunner{}
	service := &Service{runner: runner}

	_, err := service.version(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "could not determine compose binary location")
	require.Equal(t, 2, runner.calls, "both spellings must be tried when the context is alive")
}
