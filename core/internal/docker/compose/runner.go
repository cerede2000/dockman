package compose

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/RA341/dockman/pkg/fileutil"
	"golang.org/x/crypto/ssh"
)

type CmdRunner interface {
	Run(
		ctx context.Context,
		cmd []string,
		wd string,
		env []string,
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
	env []string,
	out io.Writer,
	errWriter io.Writer,
) error {
	if len(cmd) < 1 {
		return fmt.Errorf("invalid command")
	}

	ins := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	ins.Dir = wd
	if len(env) > 0 {
		ins.Env = mergeEnvironment(os.Environ(), env)
	}
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
	env []string,
	out io.Writer,
	errWriter io.Writer,
) error {
	session, err := r.cli.NewSession()
	if err != nil {
		return fmt.Errorf("unable to create ssh session: %w", err)
	}
	defer fileutil.Close(session)

	fullCmd := fmt.Sprintf("cd %s && %s", shellQuote(wd), quoteRemoteCommand(cmd))
	if len(env) > 0 {
		// Send secret values through the encrypted SSH channel on stdin. They are
		// never written to the remote filesystem and, unlike `env KEY=value ...`,
		// never appear in the remote process command line.
		var script strings.Builder
		script.WriteString("set -eu\ncd ")
		script.WriteString(shellQuote(wd))
		script.WriteByte('\n')
		for _, entry := range env {
			name, value, found := strings.Cut(entry, "=")
			if !found || !environmentNamePattern.MatchString(name) || strings.IndexByte(value, 0) >= 0 {
				return fmt.Errorf("invalid inline secret environment entry")
			}
			script.WriteString("export ")
			script.WriteString(name)
			script.WriteByte('=')
			script.WriteString(shellQuote(value))
			script.WriteByte('\n')
		}
		script.WriteString("exec ")
		script.WriteString(quoteRemoteCommand(cmd))
		script.WriteByte('\n')
		session.Stdin = strings.NewReader(script.String())
		fullCmd = "sh -s"
	}

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

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func mergeEnvironment(base, overrides []string) []string {
	keys := make(map[string]struct{}, len(overrides))
	for _, entry := range overrides {
		name, _, found := strings.Cut(entry, "=")
		if found {
			keys[name] = struct{}{}
		}
	}
	merged := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		name, _, found := strings.Cut(entry, "=")
		if _, replaced := keys[name]; found && replaced {
			continue
		}
		merged = append(merged, entry)
	}
	return append(merged, overrides...)
}

func quoteRemoteCommand(cmd []string) string {
	quoted := make([]string, len(cmd))
	for index, arg := range cmd {
		quoted[index] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
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
