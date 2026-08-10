package updater

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/require"
)

func inProject(id, project string) container.Summary {
	return container.Summary{ID: id, Labels: map[string]string{api.ProjectLabel: project}}
}

// Containers of one stack have to stay in a single group: Compose orders a
// stack from its dependency graph, and replacing a database at the same moment
// as the application above it is exactly what a flat parallel loop would do.
func TestGroupContainersByStackKeepsAStackTogetherAndInOrder(t *testing.T) {
	groups := groupContainersByStack([]container.Summary{
		inProject("web", "shop"),
		inProject("cache", "blog"),
		inProject("db", "shop"),
	})

	require.Len(t, groups, 2)
	require.Equal(t, "shop", groups[0].key)
	require.Equal(t, []string{"web", "db"}, ids(groups[0].containers),
		"the requested order inside a stack must be preserved")
	require.Equal(t, "blog", groups[1].key)
	require.Equal(t, []string{"cache"}, ids(groups[1].containers))
}

func TestGroupContainersByStackIsolatesContainersWithoutAProject(t *testing.T) {
	// A container deployed outside Compose has no stack to be ordered against,
	// so it is its own group and runs alongside the rest.
	groups := groupContainersByStack([]container.Summary{
		{ID: "loose-one"},
		{ID: "loose-two"},
		inProject("web", "shop"),
	})

	require.Len(t, groups, 3)
	require.Equal(t, []string{"loose-one", "loose-two", "shop"}, keys(groups))
}

// The stream underneath is one Connect response with no lock of its own. Two
// stacks writing at once would race on it, and their lines would arrive
// shuffled with nothing saying which stack each belongs to.
func TestStackLogWriterEmitsWholePrefixedLinesUnderConcurrency(t *testing.T) {
	var (
		mu  sync.Mutex
		out bytes.Buffer
		run sync.WaitGroup
	)
	stacks := []string{"shop", "blog", "wiki"}
	for _, stack := range stacks {
		run.Add(1)
		go func() {
			defer run.Done()
			writer := newStackLogWriter(&mu, &out, stack)
			defer writer.Flush()
			for step := range 20 {
				// Split mid-line on purpose: a compose pull writes arbitrary
				// chunks, not neat lines.
				_, _ = writer.Write([]byte("pulling "))
				_, _ = writer.Write([]byte(string(rune('a'+step)) + "\n"))
			}
		}()
	}
	run.Wait()

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Len(t, lines, len(stacks)*20)
	for _, line := range lines {
		stack, message, found := strings.Cut(line, "] ")
		require.True(t, found, "every line carries its stack: %q", line)
		require.Contains(t, stacks, strings.TrimPrefix(stack, "["))
		require.True(t, strings.HasPrefix(message, "pulling "),
			"a line must never be cut by another stack's output: %q", line)
	}
}

func TestStackLogWriterFlushesATrailingPartialLine(t *testing.T) {
	var (
		mu  sync.Mutex
		out bytes.Buffer
	)
	writer := newStackLogWriter(&mu, &out, "shop")
	_, _ = writer.Write([]byte("done, no newline"))
	require.Empty(t, out.String(), "an unterminated line waits for its rest")

	writer.Flush()
	require.Equal(t, "[shop] done, no newline\r\n", out.String())
}

func ids(containers []container.Summary) []string {
	out := make([]string, 0, len(containers))
	for _, cur := range containers {
		out = append(out, cur.ID)
	}
	return out
}

func keys(groups []stackGroup) []string {
	out := make([]string, 0, len(groups))
	for _, group := range groups {
		out = append(out, group.key)
	}
	return out
}
