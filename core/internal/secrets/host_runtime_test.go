package secrets

import (
	"encoding/json"
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

	stacks, err := discoverEncryptedStacks(root)
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(root, "apps/first"), filepath.Join(root, "z-last")}, stacks)
}

func TestDiscoverEncryptedStacksRejectsInvalidMarker(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "app")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, SOPSInlineMarkerFile), []byte("version=99\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, SOPSSourceFile), []byte("ciphertext"), 0o600))
	_, err := discoverEncryptedStacks(root)
	require.ErrorContains(t, err, "invalid encrypted runtime marker")
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
		"/etc/systemd/system/docker.socket.d/20-dockman-secrets.conf",
	} {
		_, err := os.Stat(rooted(root, path))
		require.NoError(t, err, path)
	}
	unit, err := os.ReadFile(rooted(root, "/etc/systemd/system/"+HostRuntimeUnitName))
	require.NoError(t, err)
	require.Contains(t, string(unit), "Before=docker.service docker.socket")
	require.Contains(t, string(unit), "ExecStop=")
	pathUnit, err := os.ReadFile(rooted(root, "/etc/systemd/system/"+HostReconcilePathName))
	require.NoError(t, err)
	require.Contains(t, string(pathUnit), `PathChanged="/server/stacks/.dockman-secrets-reconcile"`)
	require.Contains(t, string(pathUnit), "Unit="+HostReconcileUnitName)
	info, err := os.Stat(rooted(root, HostRuntimeConfigPath))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
