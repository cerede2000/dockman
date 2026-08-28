package gitsync

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// deployTracer records what a controlled deployment actually did, stage by
// stage, when DEPLOY_TRACE is on.
//
// It exists because the failures worth chasing here are not wrong logic but
// missing context: a stage that failed for a reason belonging to the stage
// BEFORE it, a rollback fired by a context that was already dead, a verdict
// reached from silence. The compose output alone cannot show any of that -
// it shows what compose printed, not what Dockman concluded or why.
//
// Every line goes to two places on purpose. The structured log is greppable on
// the host; the deployment log is what the operator can already read in the UI
// and paste into a bug report without shell access.
type deployTracer struct {
	enabled bool
	stack   string
	commit  string
	out     io.Writer
	started time.Time
}

func (s *Service) newDeployTracer(stack, commit string, out io.Writer) *deployTracer {
	return &deployTracer{enabled: s.deployTrace, stack: stack, commit: commit, out: out, started: time.Now()}
}

func (t *deployTracer) On() bool { return t != nil && t.enabled }

// stage reports the outcome of one stage and how long it took. ctx is read for
// its own state: a stage that failed on an already-cancelled context failed for
// a reason that has nothing to do with the stack.
func (t *deployTracer) stage(ctx context.Context, name string, since time.Time, err error) {
	if !t.On() {
		return
	}
	verdict := "ok"
	detail := ""
	if err != nil {
		verdict = "failed"
		detail = err.Error()
	}
	ctxState := "live"
	if ctxErr := ctx.Err(); ctxErr != nil {
		ctxState = ctxErr.Error()
	}
	event := log.Info()
	if err != nil {
		event = log.Warn()
	}
	event.Str("stack", t.stack).Str("commit", shortCommit(t.commit)).Str("stage", name).
		Str("verdict", verdict).Dur("took", time.Since(since)).Str("context", ctxState).
		Str("detail", truncateTrace(detail)).Msg("deploy trace")
	t.write(fmt.Sprintf("[trace] %-11s %-6s in %-8s context=%s%s",
		name, verdict, time.Since(since).Round(time.Millisecond), ctxState, traceSuffix(detail)))
}

// note records a decision rather than a stage: why a rollback ran, what the
// retry set was, whether a lock was taken. These are the answers that were
// missing every time a report contradicted itself.
func (t *deployTracer) note(what string, args ...any) {
	if !t.On() {
		return
	}
	message := fmt.Sprintf(what, args...)
	log.Info().Str("stack", t.stack).Str("commit", shortCommit(t.commit)).
		Str("note", truncateTrace(message)).Msg("deploy trace")
	t.write("[trace] " + message)
}

func (t *deployTracer) done(state string) {
	if !t.On() {
		return
	}
	log.Info().Str("stack", t.stack).Str("commit", shortCommit(t.commit)).Str("final", state).
		Dur("total", time.Since(t.started)).Msg("deploy trace")
	t.write(fmt.Sprintf("[trace] finished as %s after %s", state, time.Since(t.started).Round(time.Millisecond)))
}

func (t *deployTracer) write(line string) {
	if t.out == nil {
		return
	}
	_, _ = fmt.Fprintln(t.out, line)
}

func traceSuffix(detail string) string {
	if detail == "" {
		return ""
	}
	return " detail=" + truncateTrace(detail)
}

// truncateTrace keeps a trace line readable. A compose failure can carry a
// whole build log, and a trace nobody can read is a trace nobody reads.
func truncateTrace(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " | "))
	const limit = 400
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "… (truncated)"
}

func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
