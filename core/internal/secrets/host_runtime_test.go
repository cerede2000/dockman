package secrets

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiscoverEncryptedStacksIsExplicitBoundedAndSorted(t *testing.T) {
	root := t.TempDir()
	for _, stack := range []string{"z-last", "apps/first"} {
		directory := filepath.Join(root, stack)
		require.NoError(t, os.MkdirAll(directory, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(directory, SOPSInlineMarkerFile), []byte("version=1\n"), 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(directory, SOPSSourceFile), []byte("ciphertext"), 0o600))
	}
	ignored := filepath.Join(root, ".git", "ignored")
	require.NoError(t, os.MkdirAll(ignored, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(ignored, SOPSInlineMarkerFile), []byte("version=1\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(ignored, SOPSSourceFile), []byte("ciphertext"), 0o600))

	discovery, err := discoverEncryptedStacks(root)
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(root, "apps/first"), filepath.Join(root, "z-last")}, discovery.Ready)
}

// An unsupported marker version still disqualifies the stack from being
// materialized. It is reported as that stack's problem rather than as a failure
// of the whole pass - see TestDiscoverEncryptedStacksKeepsHealthyStacksWhenOneIsBroken.
func TestDiscoverEncryptedStacksRejectsInvalidMarker(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "app")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, SOPSInlineMarkerFile), []byte("version=99\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, SOPSSourceFile), []byte("ciphertext"), 0o600))

	discovery, err := discoverEncryptedStacks(root)
	require.NoError(t, err)
	require.Empty(t, discovery.Ready, "an unsupported marker version is never materialized")
	require.ErrorContains(t, errors.Join(discovery.Problems...), "invalid encrypted runtime marker")
}

func TestLoadHostRuntimeConfigAppliesSafeDefaults(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(root, "age-key.txt")
	sops := filepath.Join(root, "sops")
	value, err := json.Marshal(HostRuntimeConfig{StackRoot: filepath.Join(root, "stacks"), AgeKeyFile: key, SOPSBinary: sops})
	require.NoError(t, err)
	path := filepath.Join(root, "runtime.json")
	require.NoError(t, os.WriteFile(path, value, 0o600))
	config, err := LoadHostRuntimeConfig(path)
	require.NoError(t, err)
	require.Equal(t, 16, config.TmpfsSizeMiB)
	require.Equal(t, uint32(0o444), config.FileMode)
}

func TestHostRuntimeConfigRejectsPersistentRootOrUnsafeMode(t *testing.T) {
	config := HostRuntimeConfig{StackRoot: "/", AgeKeyFile: "/key", SOPSBinary: "/sops"}
	require.ErrorContains(t, config.normalize(), "non-root")
	config = HostRuntimeConfig{StackRoot: "/stacks", AgeKeyFile: "/key", SOPSBinary: "/sops", FileMode: 0o666}
	require.ErrorContains(t, config.normalize(), "0400")
}

func TestInstallHostRuntimeWritesIndependentSystemdKit(t *testing.T) {
	root := t.TempDir()
	source := t.TempDir()
	helper := filepath.Join(source, "helper")
	sops := filepath.Join(source, "sops")
	require.NoError(t, os.WriteFile(helper, []byte("helper"), 0o755))
	require.NoError(t, os.WriteFile(sops, []byte("sops"), 0o755))
	config := HostRuntimeConfig{
		StackRoot: "/server/stacks", AgeKeyFile: "/etc/dockman-secrets/age-key.txt",
		TmpfsSizeMiB: 16, FileMode: 0o444,
	}
	require.NoError(t, InstallHostRuntime(HostInstallOptions{
		Config: config, HelperFrom: helper, SOPSFrom: sops, SystemRoot: root, Activate: false,
	}))

	for _, path := range []string{
		HostRuntimeBinaryPath, HostRuntimeSOPSPath, HostRuntimeConfigPath,
		"/etc/systemd/system/" + HostRuntimeUnitName,
		"/etc/systemd/system/" + HostReconcileUnitName,
		"/etc/systemd/system/" + HostReconcilePathName,
		"/etc/systemd/system/docker.service.d/20-dockman-secrets.conf",
	} {
		_, err := os.Stat(rooted(root, path))
		require.NoError(t, err, path)
	}
	unit, err := os.ReadFile(rooted(root, "/etc/systemd/system/"+HostRuntimeUnitName))
	require.NoError(t, err)
	// Ordering before docker.socket closes a systemd cycle through
	// sockets.target and basic.target, and systemd resolves it by dropping the
	// Docker start job: the host boots with no daemon at all.
	require.Contains(t, string(unit), "Before=docker.service\n")
	require.NotContains(t, string(unit), "docker.socket")
	// ExecStop stays: a deliberate stop must take the plaintext out of memory.
	// The reinstall hazard is handled by never activating with restart.
	require.Contains(t, string(unit), "ExecStop=")
	dropIn, err := os.ReadFile(rooted(root, "/etc/systemd/system/docker.service.d/20-dockman-secrets.conf"))
	require.NoError(t, err)
	// A failed materialization must never keep the Docker daemon down.
	require.Contains(t, string(dropIn), "Wants=dockman-secrets-host.service")
	require.NotContains(t, string(dropIn), "Requires=")
	require.Contains(t, string(dropIn), "After=dockman-secrets-host.service")
	_, err = os.Stat(rooted(root, "/etc/systemd/system/docker.socket.d/20-dockman-secrets.conf"))
	require.ErrorIs(t, err, os.ErrNotExist)
	pathUnit, err := os.ReadFile(rooted(root, "/etc/systemd/system/"+HostReconcilePathName))
	require.NoError(t, err)
	// Unquoted: systemd reads this setting verbatim, so a leading double
	// quote makes the path non-absolute and takes the whole unit down with it.
	require.Contains(t, string(pathUnit), "PathChanged=/server/stacks/.dockman-secrets-reconcile\n")
	require.Contains(t, string(pathUnit), "Unit="+HostReconcileUnitName)
	info, err := os.Stat(rooted(root, HostRuntimeConfigPath))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// A host provisioned by an earlier revision keeps the cycle-inducing socket
// drop-in until an install removes it; the unit file alone no longer mentions
// docker.socket, so nothing else would repair such a host.
func TestInstallHostRuntimeRemovesObsoleteSocketDropIn(t *testing.T) {
	root := t.TempDir()
	source := t.TempDir()
	helper := filepath.Join(source, "helper")
	sops := filepath.Join(source, "sops")
	require.NoError(t, os.WriteFile(helper, []byte("helper"), 0o755))
	require.NoError(t, os.WriteFile(sops, []byte("sops"), 0o755))

	stale := rooted(root, "/etc/systemd/system/docker.socket.d/20-dockman-secrets.conf")
	require.NoError(t, os.MkdirAll(filepath.Dir(stale), 0o755))
	require.NoError(t, os.WriteFile(stale, []byte("[Unit]\nRequires=dockman-secrets-host.service\n"), 0o644))

	require.NoError(t, InstallHostRuntime(HostInstallOptions{
		Config: HostRuntimeConfig{
			StackRoot: "/server/stacks", AgeKeyFile: "/etc/dockman-secrets/age-key.txt",
			TmpfsSizeMiB: 16, FileMode: 0o444,
		},
		HelperFrom: helper, SOPSFrom: sops, SystemRoot: root, Activate: false,
	}))

	_, err := os.Stat(stale)
	require.ErrorIs(t, err, os.ErrNotExist)
}
