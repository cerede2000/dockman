package secrets

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/stretchr/testify/require"
)

// hostRuntimeFS stands in for the host side of the volatile runtime. A
// reconciliation request is honoured the way dockman-secrets-host would honour
// it: a stack that no longer carries its encrypted marker has its tmpfs
// unmounted, which here means the runtime marker file goes away.
type hostRuntimeFS struct {
	filesystem.FileSystem
	stackDirectory string
	hostResponds   bool
}

func (f *hostRuntimeFS) Rename(oldPath, newPath string) error {
	err := f.FileSystem.Rename(oldPath, newPath)
	if err != nil || newPath != HostRuntimeReconcileRequestFile || !f.hostResponds {
		return err
	}
	if _, statErr := os.Stat(filepath.Join(f.stackDirectory, SOPSInlineMarkerFile)); os.IsNotExist(statErr) {
		_ = os.Remove(filepath.Join(f.stackDirectory, RuntimeDirectory, HostRuntimeMarkerFile))
	}
	return nil
}

// encryptedStackWithMountedRuntime builds a stack that is encrypted at rest and
// whose secrets are currently materialized in a tmpfs.
func encryptedStackWithMountedRuntime(t *testing.T, hostResponds bool) (*SOPSProvider, *hostRuntimeFS, string) {
	t.Helper()
	root := t.TempDir()
	stack := filepath.Join(root, "app")
	require.NoError(t, os.MkdirAll(filepath.Join(stack, RuntimeDirectory), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(stack, "compose.yml"), []byte("services: {}\n"), 0o600))

	hostFS := &hostRuntimeFS{FileSystem: filesystem.NewLocal(root), stackDirectory: stack, hostResponds: hostResponds}
	resolver := func(_ string, stackPath string) (filesystem.FileSystem, string, error) {
		if stackPath == "compose" {
			return hostFS, ".", nil
		}
		return hostFS, "app", nil
	}
	store := NewPlainFileStore(resolver)
	store.ConfigureAliases(func(string) ([]string, error) { return []string{"compose"}, nil })
	key := filepath.Join(t.TempDir(), "age-key.txt")
	require.NoError(t, os.WriteFile(key, []byte("AGE-SECRET-KEY-TEST"), 0o600))
	provider := NewSOPSProvider(store, resolver, "true", key, "age1testrecipient")
	provider.runner = &catalogSOPSRunner{}

	// Write a secret, then encrypt the stack: the plaintext runtime directory is
	// removed and the values live only in secrets.sops.yaml from here on.
	_, err := store.Write("local", "compose/app", "API_TOKEN", []byte("s3cret"))
	require.NoError(t, err)
	_, err = provider.EnableInline(context.Background(), "local", "compose/app", "compose.yml")
	require.NoError(t, err)

	// The host has since materialized the secrets into a tmpfs.
	require.NoError(t, os.MkdirAll(filepath.Join(stack, RuntimeDirectory), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(stack, RuntimeDirectory, HostRuntimeMarkerFile), []byte("version=1\n"), 0o600))
	provider.ConfigureRuntimeMountVerifier(func(context.Context, string, string) (bool, error) { return true, nil })
	return provider, hostFS, stack
}

// Materializing before releasing the mount writes the plaintext into the very
// tmpfs the marker removal is about to discard, so the user asks to decrypt and
// loses every secret instead.
func TestDisableInlineReleasesTheMountBeforeWritingPlaintext(t *testing.T) {
	provider, _, stack := encryptedStackWithMountedRuntime(t, true)

	result, err := provider.DisableInline(context.Background(), "local", "compose/app")
	require.NoError(t, err)
	require.Equal(t, []string{"API_TOKEN"}, result.Names)

	// The plaintext landed on a directory the host is no longer going to unmount.
	value, err := os.ReadFile(filepath.Join(stack, RuntimeDirectory, "API_TOKEN"))
	require.NoError(t, err)
	require.Equal(t, "s3cret", string(value))
	require.NoFileExists(t, filepath.Join(stack, RuntimeDirectory, HostRuntimeMarkerFile), "the tmpfs must be released")
	require.NoFileExists(t, filepath.Join(stack, SOPSInlineMarkerFile))
	require.NoFileExists(t, filepath.Join(stack, SOPSRecoveryScriptFile))
	require.FileExists(t, filepath.Join(stack, SOPSSourceFile), "the ciphertext is kept, decrypting is not deleting")
}

// When the host does not release the mount, decrypting has to abort rather than
// write into memory that is about to be discarded. The stack must be left
// exactly as it was found.
func TestDisableInlineAbortsWhenTheMountSurvives(t *testing.T) {
	previous := volatileReleaseTimeout
	volatileReleaseTimeout = 300 * time.Millisecond
	t.Cleanup(func() { volatileReleaseTimeout = previous })

	provider, _, stack := encryptedStackWithMountedRuntime(t, false)

	_, err := provider.DisableInline(context.Background(), "local", "compose/app")
	require.ErrorContains(t, err, "still mounted")
	require.ErrorContains(t, err, "Nothing was changed")

	require.NoFileExists(t, filepath.Join(stack, RuntimeDirectory, "API_TOKEN"), "no plaintext may be written into a live tmpfs")
	require.FileExists(t, filepath.Join(stack, SOPSInlineMarkerFile), "the stack must stay marked as encrypted")
	require.FileExists(t, filepath.Join(stack, SOPSSourceFile))
}

// A stack with no volatile runtime keeps the original, simpler path.
func TestDisableInlineWithoutVolatileRuntimeIsUnchanged(t *testing.T) {
	provider, _, stack := encryptedStackWithMountedRuntime(t, true)
	require.NoError(t, os.Remove(filepath.Join(stack, RuntimeDirectory, HostRuntimeMarkerFile)))

	result, err := provider.DisableInline(context.Background(), "local", "compose/app")
	require.NoError(t, err)
	require.Equal(t, []string{"API_TOKEN"}, result.Names)
	value, err := os.ReadFile(filepath.Join(stack, RuntimeDirectory, "API_TOKEN"))
	require.NoError(t, err)
	require.Equal(t, "s3cret", string(value))
	require.NoFileExists(t, filepath.Join(stack, SOPSInlineMarkerFile))
}

// countingWriteFS counts the atomic renames that publish a materialized
// secret, which is what creates a new inode for that path.
type countingWriteFS struct {
	filesystem.FileSystem
	writes int
}

func (f *countingWriteFS) Rename(oldPath, newPath string) error {
	if strings.Contains(newPath, RuntimeDirectory) {
		f.writes++
	}
	return f.FileSystem.Rename(oldPath, newPath)
}

// syncVolatileRuntime runs on every Compose action, read-only ones included:
// ps, status and config all pass through the same environment provider. The
// rewrite was unconditional, so each of them spent a create-write-rename per
// secret for nothing - and since the rename replaces the inode, a container
// bind-mounting .secrets/<name> kept the inode it started with and never saw
// an update while Dockman churned the file underneath it.
func TestSyncVolatileRuntimeOnlyWritesWhatChanged(t *testing.T) {
	root := t.TempDir()
	runtimeDirectory := filepath.Join(root, "app", RuntimeDirectory)
	require.NoError(t, os.MkdirAll(runtimeDirectory, 0o700))
	counting := &countingWriteFS{FileSystem: filesystem.NewLocal(root)}
	values := map[string]string{"API_TOKEN": "s3cret", "DB_PASSWORD": "hunter2"}

	// First materialization writes both.
	require.NoError(t, syncVolatileRuntime(counting, "app", values))
	require.Equal(t, 2, counting.writes)

	// Replaying the same values writes nothing at all.
	counting.writes = 0
	require.NoError(t, syncVolatileRuntime(counting, "app", values))
	require.Equal(t, 0, counting.writes, "an unchanged secret must keep its inode")

	// A real change is still applied, and only that one.
	values["DB_PASSWORD"] = "hunter3"
	require.NoError(t, syncVolatileRuntime(counting, "app", values))
	require.Equal(t, 1, counting.writes)
	updated, err := os.ReadFile(filepath.Join(runtimeDirectory, "DB_PASSWORD"))
	require.NoError(t, err)
	require.Equal(t, "hunter3", string(updated))
	kept, err := os.ReadFile(filepath.Join(runtimeDirectory, "API_TOKEN"))
	require.NoError(t, err)
	require.Equal(t, "s3cret", string(kept))
}

// The recovery script is the whole independence guarantee: a host carrying
// only Docker and SOPS has to be able to bring the stack up from the stack
// directory alone. It used to stop on a stack with file secrets and tell the
// reader to start a Dockman systemd unit, which is precisely the dependency it
// exists to remove.
func TestRecoveryScriptNeverDependsOnDockman(t *testing.T) {
	for _, requiresFiles := range []bool{false, true} {
		script := recoveryScript("compose.yml", requiresFiles)
		require.NotContains(t, script, "systemctl", "recovery must not require a Dockman unit")
		require.NotContains(t, script, "dockman-secrets-host")
		require.NotContains(t, script, "dockman-secrets-reconcile")
		require.Contains(t, script, "sops exec-env")
	}
	script := recoveryScript("compose.yml", true)
	require.Contains(t, script, "sops -d --extract")
	require.Contains(t, script, "mount -t tmpfs")
	require.Contains(t, script, "secrets-clean")
}

// The script is only useful if a POSIX shell accepts it, and if its key
// extraction actually finds every secret SOPS wrote.
func TestRecoveryScriptIsValidShellAndFindsEveryKey(t *testing.T) {
	script := recoveryScript("compose.yml", true)
	require.NoError(t, exec.Command("sh", "-n", "-c", script).Run(), "generated script must parse as POSIX sh")

	directory := t.TempDir()
	ciphertext := "API_TOKEN: ENC[AES256_GCM,data:aa]\n" +
		"db.password: ENC[AES256_GCM,data:bb]\n" +
		"tls.key: ENC[AES256_GCM,data:cc]\n" +
		"sops:\n    age: []\n    mac: ENC[AES256_GCM,data:dd]\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, SOPSSourceFile), []byte(ciphertext), 0o600))

	// The ciphertext keeps its top-level keys in clear, so the list is readable
	// without the age identity - this is what lets the script enumerate them.
	extract := `sed -n 's/^\([A-Za-z0-9][A-Za-z0-9._-]*\):.*/\1/p' ` + SOPSSourceFile + ` | grep -v '^sops$'`
	command := exec.Command("sh", "-c", extract)
	command.Dir = directory
	output, err := command.Output()
	require.NoError(t, err)
	require.Equal(t, []string{"API_TOKEN", "db.password", "tls.key"}, strings.Fields(string(output)),
		"every secret must be discoverable from the ciphertext alone, sops metadata excluded")
}

// A stack encrypted by an older Dockman keeps that Dockman's script. When the
// template is the thing that was wrong - it used to refuse to start without a
// systemd unit - the stack has no way of learning about the fix on its own.
func TestRecoveryScriptIsRefreshedWhenItDrifts(t *testing.T) {
	root := t.TempDir()
	stack := filepath.Join(root, "app")
	require.NoError(t, os.MkdirAll(stack, 0o700))
	stackFS := filesystem.NewLocal(root)
	stale := "#!/bin/sh\n# Generated by Dockman.\necho 'run: sudo systemctl start dockman-secrets-reconcile.service'\n"
	require.NoError(t, os.WriteFile(filepath.Join(stack, SOPSRecoveryScriptFile), []byte(stale), 0o700))

	refreshRecoveryScript(stackFS, "app", "compose.yml", true)

	refreshed, err := os.ReadFile(filepath.Join(stack, SOPSRecoveryScriptFile))
	require.NoError(t, err)
	require.NotContains(t, string(refreshed), "systemctl")
	require.Contains(t, string(refreshed), "sops -d --extract")
	info, err := os.Stat(filepath.Join(stack, SOPSRecoveryScriptFile))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	// Replaying it changes nothing: no write, no new inode.
	before := info.ModTime()
	refreshRecoveryScript(stackFS, "app", "compose.yml", true)
	after, err := os.Stat(filepath.Join(stack, SOPSRecoveryScriptFile))
	require.NoError(t, err)
	require.Equal(t, before, after.ModTime(), "an up-to-date script must not be rewritten")
}

// Someone who replaced the script with their own procedure must keep it.
func TestRecoveryScriptRefreshLeavesAHandWrittenScriptAlone(t *testing.T) {
	root := t.TempDir()
	stack := filepath.Join(root, "app")
	require.NoError(t, os.MkdirAll(stack, 0o700))
	mine := "#!/bin/sh\n# my own recovery procedure\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(stack, SOPSRecoveryScriptFile), []byte(mine), 0o700))

	refreshRecoveryScript(filesystem.NewLocal(root), "app", "compose.yml", true)

	kept, err := os.ReadFile(filepath.Join(stack, SOPSRecoveryScriptFile))
	require.NoError(t, err)
	require.Equal(t, mine, string(kept))
}

// An absent script is left to EnableInline: nothing here can tell a stack that
// never had one from a file somebody deliberately removed.
func TestRecoveryScriptRefreshDoesNotCreateAMissingScript(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "app"), 0o700))
	refreshRecoveryScript(filesystem.NewLocal(root), "app", "compose.yml", true)
	require.NoFileExists(t, filepath.Join(root, "app", SOPSRecoveryScriptFile))
}
