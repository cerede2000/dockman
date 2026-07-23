package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGitStorageRootsKeepLegacyDefault(t *testing.T) {
	conf := AppConfig{ConfigDir: "/config"}
	repositories, backups, err := conf.GetGitStorageRoots()
	require.NoError(t, err)
	require.Equal(t, filepath.Join("/config", "git", "repositories"), repositories)
	require.Equal(t, filepath.Join("/config", "git", "backups"), backups)
}

func TestGitStorageRootsUseDedicatedAbsolutePath(t *testing.T) {
	conf := AppConfig{ConfigDir: "/config", GitStoragePath: "/var/lib/dockman-git"}
	repositories, backups, err := conf.GetGitStorageRoots()
	require.NoError(t, err)
	require.Equal(t, "/var/lib/dockman-git/repositories", repositories)
	require.Equal(t, "/var/lib/dockman-git/backups", backups)

	conf.GitStoragePath = "relative/path"
	_, _, err = conf.GetGitStorageRoots()
	require.ErrorContains(t, err, "must be absolute")

	conf.GitStoragePath = string(filepath.Separator)
	_, _, err = conf.GetGitStorageRoots()
	require.ErrorContains(t, err, "cannot be a filesystem root")
}
