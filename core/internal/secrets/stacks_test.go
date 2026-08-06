package secrets

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/stretchr/testify/require"
)

func TestListStacksDiscoversAliasesAndSkipsRuntimeTrees(t *testing.T) {
	roots := map[string]string{"compose": t.TempDir(), "templates": t.TempDir()}
	require.NoError(t, os.MkdirAll(filepath.Join(roots["compose"], "group", "app"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(roots["compose"], "group", "app", "compose.yml"), []byte("services: {}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(roots["compose"], "group", "app", "docker-compose.yaml"), []byte("services: {}\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(roots["compose"], ".secrets", "ignored"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(roots["compose"], ".secrets", "ignored", "compose.yml"), []byte("services: {}\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(roots["templates"], "compose.yaml"), []byte("services: {}\n"), 0o600))

	store := NewPlainFileStore(func(_ string, stackPath string) (filesystem.FileSystem, string, error) {
		return filesystem.NewLocal(roots[stackPath]), ".", nil
	})
	store.ConfigureAliases(func(host string) ([]string, error) {
		require.Equal(t, "ssh-node", host)
		return []string{"templates", "compose", "compose"}, nil
	})
	stacks, err := store.ListStacks("ssh-node")
	require.NoError(t, err)
	require.Equal(t, []StackOption{
		{Path: "compose/group/app", Alias: "compose", Manifests: []string{"compose.yml", "docker-compose.yaml"}},
		{Path: "templates", Alias: "templates", Manifests: []string{"compose.yaml"}},
	}, stacks)
}
