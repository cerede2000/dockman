package secrets

import (
	"context"
	"os"
	"path/filepath"
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
