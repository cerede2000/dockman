package secrets

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func watchLines(t *testing.T, options HostInstallOptions) []string {
	t.Helper()
	directives := reconcileWatchDirectives(options)
	if directives == "" {
		return nil
	}
	return strings.Split(directives, "\n")
}

// A nested alias is the case that never reconciled. Dockman writes its request
// through the filesystem of the alias the stack belongs to, and that filesystem
// cannot reach above its own root - so with only the stack root watched, the
// request for /server/stacks/media landed where nothing was looking and the
// user waited on a reconciliation that never came.
func TestReconcileWatchCoversNestedAliasRoots(t *testing.T) {
	lines := watchLines(t, HostInstallOptions{
		Config:     HostRuntimeConfig{StackRoot: "/server/stacks"},
		WatchRoots: []string{"/server/stacks/media", "/opt/other"},
	})

	require.Equal(t, []string{
		"PathChanged=/server/stacks/.dockman-secrets-reconcile",
		"PathChanged=/server/stacks/media/.dockman-secrets-reconcile",
		"PathChanged=/opt/other/.dockman-secrets-reconcile",
	}, lines, "the stack root comes first, then each alias root")
}

func TestReconcileWatchKeepsTheStackRootWhenNoAliasIsGiven(t *testing.T) {
	// The single-alias host, which is the common setup: behaviour unchanged.
	lines := watchLines(t, HostInstallOptions{Config: HostRuntimeConfig{StackRoot: "/server/stacks"}})
	require.Equal(t, []string{"PathChanged=/server/stacks/.dockman-secrets-reconcile"}, lines)
}

func TestReconcileWatchDropsDuplicatesAndUnusableRoots(t *testing.T) {
	// systemd rejects the whole unit on one malformed directive, which would
	// take the stack root's own watch down with it. Anything unusable is
	// dropped rather than emitted.
	lines := watchLines(t, HostInstallOptions{
		Config: HostRuntimeConfig{StackRoot: "/server/stacks"},
		WatchRoots: []string{
			"/server/stacks",        // the stack root again
			"/server/stacks/media/", // same as the cleaned form below
			"/server/stacks/media",
			"relative/path",
			"",
			"/",
			"/tmp/bad\nPathChanged=/etc/shadow",
		},
	})

	require.Equal(t, []string{
		"PathChanged=/server/stacks/.dockman-secrets-reconcile",
		"PathChanged=/server/stacks/media/.dockman-secrets-reconcile",
	}, lines)
}

// systemd does not unquote PathChanged=. Writing it with %q made it read the
// leading double quote as the first character of the path, decide the path was
// not absolute, drop the directive, and then refuse the whole unit for having
// no path at all:
//
//	PathChanged= path is not absolute, ignoring: "/server/stacks/...
//	dockman-secrets-reconcile.path: Path unit lacks path setting. Refusing.
//
// The watch was dead from the day it was first installed, so nothing ever
// reconciled automatically. This is the assertion that would have caught it.
func TestReconcileWatchEmitsAnUnquotedAbsolutePath(t *testing.T) {
	lines := watchLines(t, HostInstallOptions{Config: HostRuntimeConfig{StackRoot: "/server/stacks"}})
	require.Len(t, lines, 1)
	value := strings.TrimPrefix(lines[0], "PathChanged=")
	require.NotEqual(t, lines[0], value, "the directive must be PathChanged=")
	require.True(t, strings.HasPrefix(value, "/"),
		"systemd reads this verbatim: anything before the slash makes the path non-absolute, got %q", value)
	require.NotContains(t, value, `"`, "quotes are part of the path as far as systemd is concerned")
}
