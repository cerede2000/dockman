package updater

import (
	"bytes"
	"io"
	"strings"
	"sync"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
)

// maxParallelStackUpdates caps how many stacks are updated at once. An update
// is network-bound - one image pull each - so the limit is about not
// saturating the link or the registry, not about CPU.
const maxParallelStackUpdates = 4

// stackGroup is one stack's containers, in the order they were requested.
type stackGroup struct {
	key        string
	containers []container.Summary
}

// groupContainersByStack splits a batch so that each stack becomes one group.
//
// Containers of the same stack must not be replaced at the same time: Compose
// orders a stack from its dependency graph, and a database recreated under its
// application is exactly the failure the sequential loop avoided. Different
// stacks have no such relation and can proceed together.
//
// Grouping is by Compose project. That is never finer than the lock the caller
// already holds - two containers sharing a Dockman stack file always share a
// project name - so this can only serialize more than the lock does, never
// less. A container with no project of its own is its own group.
func groupContainersByStack(containers []container.Summary) []stackGroup {
	order := make([]string, 0, len(containers))
	byKey := make(map[string]*stackGroup, len(containers))
	for _, cur := range containers {
		key := stackGroupKey(cur)
		group, seen := byKey[key]
		if !seen {
			group = &stackGroup{key: key}
			byKey[key] = group
			order = append(order, key)
		}
		group.containers = append(group.containers, cur)
	}
	groups := make([]stackGroup, 0, len(order))
	for _, key := range order {
		groups = append(groups, *byKey[key])
	}
	return groups
}

func stackGroupKey(cur container.Summary) string {
	if project := strings.TrimSpace(cur.Labels[api.ProjectLabel]); project != "" {
		return project
	}
	return cur.ID
}

// stackLogWriter serializes the output of concurrent stack updates and tags
// every line with the stack it came from.
//
// Without it two things break at once. The stream underneath is a single
// Connect response with no lock of its own, so concurrent writes race on it;
// and even if they did not, the lines of several updates would arrive
// shuffled with no way to tell which stack each belongs to. Output is
// buffered until a newline so a line is emitted whole, prefix included.
type stackLogWriter struct {
	mu     *sync.Mutex
	out    io.Writer
	prefix string
	buf    bytes.Buffer
}

func newStackLogWriter(mu *sync.Mutex, out io.Writer, stack string) *stackLogWriter {
	return &stackLogWriter{mu: mu, out: out, prefix: "[" + stack + "] "}
}

func (w *stackLogWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if err != nil {
			// No newline yet: put the partial line back and wait for the rest.
			w.buf.Reset()
			w.buf.WriteString(line)
			return len(p), nil
		}
		if err := w.emit(line); err != nil {
			return len(p), err
		}
	}
}

// Flush emits whatever is left when a stack finishes without a trailing
// newline, so its last line is not swallowed.
func (w *stackLogWriter) Flush() {
	if w.buf.Len() == 0 {
		return
	}
	line := w.buf.String()
	w.buf.Reset()
	_ = w.emit(line + "\r\n")
}

func (w *stackLogWriter) emit(line string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err := io.WriteString(w.out, w.prefix+line)
	return err
}
