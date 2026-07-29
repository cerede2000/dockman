package compose

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/RA341/dockman/pkg/fileutil"
	"golang.org/x/crypto/ssh"
)

type CmdRunner interface {
	Run(
		ctx context.Context,
		cmd []string,
		wd string,
		stdIn io.Writer,
		stdErr io.Writer,
	) error
}

type LocalRunner struct{}

func NewLocalRunner() *LocalRunner {
	return &LocalRunner{}
}

func (l *LocalRunner) Run(
	ctx context.Context,
	cmd []string,
	wd string,
	out io.Writer,
	errWriter io.Writer,
) error {
	if len(cmd) < 1 {
		return fmt.Errorf("invalid command")
	}

	ins := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	ins.Dir = wd
	ins.Stdout = out
	// Keep stderr visible in the live action stream while also retaining it for
	// the API error. Previously local Compose failures were displayed to the
	// terminal but returned as an empty error message to Git automation.
	ins.Stderr = combineWriters(out, errWriter)
	ins.Stdin = nil

	err := ins.Run()
	return err
}

type RemoteRunner struct {
	cli *ssh.Client
}

func NewRemoteRunner(cli *ssh.Client) *RemoteRunner {
	return &RemoteRunner{
		cli: cli,
	}
}

func (r *RemoteRunner) Run(
	ctx context.Context,
	cmd []string,
	wd string,
	out io.Writer,
	errWriter io.Writer,
) error {
	session, err := r.cli.NewSession()
	if err != nil {
		return fmt.Errorf("unable to create ssh session: %w", err)
	}
	defer fileutil.Close(session)

	fullCmd := fmt.Sprintf(
		"cd %s && %s",
		wd,
		strings.Join(cmd, " "),
	)

	session.Stdout = out
	session.Stderr = combineWriters(out, errWriter)
	session.Stdin = nil

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// Force-close the session if the context is canceled
			fileutil.Close(session)
		case <-done:
		}
	}()
	defer close(done)

	return session.Run(fullCmd)
}

func combineWriters(writers ...io.Writer) io.Writer {
	valid := make([]io.Writer, 0, len(writers))
	for _, writer := range writers {
		if writer != nil {
			valid = append(valid, writer)
		}
	}
	if len(valid) == 0 {
		return io.Discard
	}
	if len(valid) == 1 {
		return valid[0]
	}
	return io.MultiWriter(valid...)
}
