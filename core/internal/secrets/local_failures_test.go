package secrets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/stretchr/testify/require"
)

func writeEncryptedStack(t *testing.T, root, relative, marker string) string {
	t.Helper()
	directory := filepath.Join(root, relative)
	require.NoError(t, os.MkdirAll(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, SOPSInlineMarkerFile), []byte(marker), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, SOPSSourceFile), []byte("ciphertext"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "compose.yml"), []byte("services: {}\n"), 0o600))
	return directory
}

// This walk runs at boot, before Docker. Aborting it on the first unusable
// stack means a single truncated secrets.sops.yaml - an interrupted rsync, a
// full disk - leaves every other stack on the host with no secrets at all,
// which is the exact outcome the per-stack error handling downstream was
// written to prevent.
func TestDiscoverEncryptedStacksKeepsHealthyStacksWhenOneIsBroken(t *testing.T) {
	root := t.TempDir()
	writeEncryptedStack(t, root, "apps/healthy", "version=1\n")
	writeEncryptedStack(t, root, "apps/other", "version=1\n")
	broken := writeEncryptedStack(t, root, "apps/broken", "version=99\n")

	discovery, err := discoverEncryptedStacks(root)
	require.NoError(t, err, "a malformed stack is not a fatal condition for the host")

	require.Equal(t, []string{
		filepath.Join(root, "apps/healthy"),
		filepath.Join(root, "apps/other"),
	}, discovery.Ready, "the healthy stacks still have to be materialized")

	require.Len(t, discovery.Problems, 1, "and the broken one is still reported")
	require.ErrorContains(t, errors.Join(discovery.Problems...), "invalid encrypted runtime marker")
	require.NotContains(t, discovery.Ready, broken)
}

// A stack that carries a marker is claimed even when this pass cannot validate
// it. Dropping it from the desired set would unmount the tmpfs of a stack the
// user marked encrypted, taking the secrets away from containers that are
// running right now because of a problem that may well be transient.
func TestDiscoverEncryptedStacksClaimsBrokenStacksSoTheirMountSurvives(t *testing.T) {
	root := t.TempDir()
	writeEncryptedStack(t, root, "apps/healthy", "version=1\n")
	writeEncryptedStack(t, root, "apps/broken", "version=99\n")
	// A marker that is valid but whose ciphertext has gone missing.
	sourceless := writeEncryptedStack(t, root, "apps/sourceless", "version=1\n")
	require.NoError(t, os.Remove(filepath.Join(sourceless, SOPSSourceFile)))

	discovery, err := discoverEncryptedStacks(root)
	require.NoError(t, err)

	require.Equal(t, []string{
		filepath.Join(root, "apps/broken"),
		filepath.Join(root, "apps/healthy"),
		filepath.Join(root, "apps/sourceless"),
	}, discovery.Claimed, "every marked stack keeps its runtime directory")
	require.Equal(t, []string{filepath.Join(root, "apps/healthy")}, discovery.Ready)
	require.Len(t, discovery.Problems, 2)
}

// A planted symlink marker is refused, but only for its own stack: it must not
// become a way to take every other stack down with it.
func TestDiscoverEncryptedStacksKeepsASymlinkedMarkerLocal(t *testing.T) {
	root := t.TempDir()
	symlinked := filepath.Join(root, "app")
	require.NoError(t, os.MkdirAll(symlinked, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(symlinked, SOPSSourceFile), []byte("ciphertext"), 0o600))
	require.NoError(t, os.Symlink(SOPSSourceFile, filepath.Join(symlinked, SOPSInlineMarkerFile)))

	discovery, err := discoverEncryptedStacks(root)
	require.NoError(t, err)
	require.Empty(t, discovery.Ready)
	require.ErrorContains(t, errors.Join(discovery.Problems...), "symlink")
}

// One mount that refuses to go - a container still holding a bind into it -
// must not stop the others from being released.
func TestReleaseObsoleteMountsContinuesAfterOneFailure(t *testing.T) {
	released := []string{}
	err := releaseObsoleteMounts(
		[]string{"/stacks/c/.secrets", "/stacks/b/.secrets", "/stacks/a/.secrets"},
		map[string]struct{}{"/stacks/b/.secrets": {}},
		func(mount string) error {
			if mount == "/stacks/c/.secrets" {
				return errors.New("target is busy")
			}
			released = append(released, mount)
			return nil
		},
	)

	require.ErrorContains(t, err, "target is busy")
	require.Equal(t, []string{"/stacks/a/.secrets"}, released,
		"b is still wanted and c refused, but a must be released either way")
}

// multiStackFS stands in for the host daemon across several stacks at once: a
// reconciliation request unmounts every stack that no longer carries its
// encrypted marker.
type multiStackFS struct {
	filesystem.FileSystem
	root string
}

func (f *multiStackFS) Rename(oldPath, newPath string) error {
	err := f.FileSystem.Rename(oldPath, newPath)
	if err != nil || newPath != HostRuntimeReconcileRequestFile {
		return err
	}
	return filepath.WalkDir(f.root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || !entry.IsDir() || entry.Name() != RuntimeDirectory {
			return walkErr
		}
		stack := filepath.Dir(current)
		if _, statErr := os.Stat(filepath.Join(stack, SOPSInlineMarkerFile)); os.IsNotExist(statErr) {
			_ = os.Remove(filepath.Join(current, HostRuntimeMarkerFile))
		}
		return nil
	})
}

func serviceWithEncryptedStacks(t *testing.T, relatives ...string) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	for _, relative := range relatives {
		directory := writeEncryptedStack(t, root, relative, "version=1\n")
		runtime := filepath.Join(directory, RuntimeDirectory)
		require.NoError(t, os.MkdirAll(runtime, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(runtime, HostRuntimeMarkerFile), []byte("version=1\n"), 0o600))
	}

	hostFS := &multiStackFS{FileSystem: filesystem.NewLocal(root), root: root}
	resolver := func(_ string, stackPath string) (filesystem.FileSystem, string, error) {
		relative := strings.TrimPrefix(strings.TrimPrefix(stackPath, "compose"), "/")
		if relative == "" {
			relative = "."
		}
		return hostFS, relative, nil
	}
	store := NewPlainFileStore(resolver)
	store.ConfigureAliases(func(string) ([]string, error) { return []string{"compose"}, nil })
	key := filepath.Join(t.TempDir(), "age-key.txt")
	require.NoError(t, os.WriteFile(key, []byte("AGE-SECRET-KEY-TEST"), 0o600))
	provider := NewSOPSProvider(store, resolver, "true", key, "age1testrecipient")
	provider.runner = &catalogSOPSRunner{}
	provider.ConfigureRuntimeMountVerifier(func(_ context.Context, _ string, absolute string) (bool, error) {
		_, err := os.Stat(filepath.Join(absolute, HostRuntimeMarkerFile))
		return err == nil, nil
	})
	service := NewService(store)
	service.ConfigureSOPS(provider)
	return service, root
}

// Deleting a folder holding more than one encrypted stack released only the
// first one, so RemoveAll wiped that stack, walked on, and hit EBUSY on the
// second stack's live mount: the half-removed directory the guard exists to
// prevent, reached through the guard itself.
func TestGuardFileDeletionReleasesEveryEncryptedStackUnderTheFolder(t *testing.T) {
	service, root := serviceWithEncryptedStacks(t, "media/plex", "media/sonarr")

	require.NoError(t, service.GuardFileDeletion("local", "compose/media"))

	for _, stack := range []string{"media/plex", "media/sonarr"} {
		require.NoFileExists(t,
			filepath.Join(root, stack, RuntimeDirectory, HostRuntimeMarkerFile),
			"%s still holds a live tmpfs, so RemoveAll would stop half way", stack)
		require.NoFileExists(t, filepath.Join(root, stack, SOPSInlineMarkerFile))
	}
}

// If any one of them cannot be released, nothing is deleted: a partial release
// is the same half-removed directory by another route.
func TestGuardFileDeletionRefusesWhenOneStackCannotBeReleased(t *testing.T) {
	service, root := serviceWithEncryptedStacks(t, "media/plex", "media/sonarr")
	// The host stops answering for sonarr: its marker file is write-protected,
	// so the release cannot take it out of the encrypted set.
	stuck := filepath.Join(root, "media/sonarr")
	require.NoError(t, os.Chmod(stuck, 0o500))
	t.Cleanup(func() { _ = os.Chmod(stuck, 0o700) })

	err := service.GuardFileDeletion("local", "compose/media")
	require.Error(t, err)
	require.ErrorContains(t, err, "refusing to delete")
}
