package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/stretchr/testify/require"
)

func testStore(t *testing.T) (*PlainFileStore, map[string]string) {
	t.Helper()
	roots := map[string]string{"local": t.TempDir(), "remote": t.TempDir()}
	for _, root := range roots {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "apps", "demo"), 0o755))
	}
	store := NewPlainFileStore(func(host, stackPath string) (filesystem.FileSystem, string, error) {
		root, ok := roots[host]
		if !ok {
			return nil, "", errors.New("unknown host")
		}
		if stackPath != "compose/apps/demo" {
			return nil, "", ErrInvalidStackPath
		}
		return filesystem.NewLocal(root), "apps/demo", nil
	})
	return store, roots
}

func TestPlainFileStoreWritesSecureAtomicRuntimeSecret(t *testing.T) {
	store, roots := testStore(t)
	item, err := store.Write("local", "compose/apps/demo", "database_password", []byte("correct horse battery staple\n"))
	require.NoError(t, err)
	require.Equal(t, "database_password", item.Name)

	directory := filepath.Join(roots["local"], "apps", "demo", RuntimeDirectory)
	dirInfo, err := os.Stat(directory)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
	secretInfo, err := os.Stat(filepath.Join(directory, "database_password"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), secretInfo.Mode().Perm())

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Len(t, entries, 1, "atomic temporary files must be cleaned")
	value, err := store.Read("local", "compose/apps/demo", "database_password")
	require.NoError(t, err)
	require.Equal(t, "correct horse battery staple\n", string(value))
}

func TestPlainFileStoreIsolatesHostsWithIdenticalStackPaths(t *testing.T) {
	store, roots := testStore(t)
	_, err := store.Write("local", "compose/apps/demo", "token", []byte("local-value"))
	require.NoError(t, err)
	_, err = store.Write("remote", "compose/apps/demo", "token", []byte("remote-value"))
	require.NoError(t, err)

	local, err := store.Read("local", "compose/apps/demo", "token")
	require.NoError(t, err)
	remote, err := store.Read("remote", "compose/apps/demo", "token")
	require.NoError(t, err)
	require.Equal(t, "local-value", string(local))
	require.Equal(t, "remote-value", string(remote))
	require.NotEqual(t, roots["local"], roots["remote"])
}

func TestPlainFileStoreRejectsTraversalAndNonRegularSecrets(t *testing.T) {
	store, roots := testStore(t)
	for _, name := range []string{"", ".", "..", "../token", "nested/token", "/token", " token"} {
		_, err := store.Write("local", "compose/apps/demo", name, []byte("value"))
		require.ErrorIs(t, err, ErrInvalidName, name)
	}
	secretDirectory := filepath.Join(roots["local"], "apps", "demo", RuntimeDirectory)
	require.NoError(t, os.MkdirAll(filepath.Join(secretDirectory, "directory"), 0o700))
	_, err := store.Read("local", "compose/apps/demo", "directory")
	require.ErrorContains(t, err, "not a regular file")
}

func TestPlainFileStoreEnforcesSizeAndDeletesExactlyOneSecret(t *testing.T) {
	store, _ := testStore(t)
	_, err := store.Write("local", "compose/apps/demo", "oversized", make([]byte, MaxSecretBytes+1))
	require.ErrorIs(t, err, ErrSecretTooLarge)
	_, err = store.Write("local", "compose/apps/demo", "first", []byte("one"))
	require.NoError(t, err)
	_, err = store.Write("local", "compose/apps/demo", "second", []byte("two"))
	require.NoError(t, err)
	require.NoError(t, store.Delete("local", "compose/apps/demo", "first"))
	_, err = store.Read("local", "compose/apps/demo", "first")
	require.ErrorIs(t, err, os.ErrNotExist)
	second, err := store.Read("local", "compose/apps/demo", "second")
	require.NoError(t, err)
	require.Equal(t, "two", string(second))
}

func TestPlainFileStoreRequiresExplicitExistingStackDirectory(t *testing.T) {
	store, _ := testStore(t)
	_, err := store.List("local", "compose/apps/missing")
	require.ErrorIs(t, err, ErrInvalidStackPath)
}

func TestPlainFileStoreKeepsThreeSecureVersionsAndRestores(t *testing.T) {
	store, roots := testStore(t)
	for _, value := range []string{"one", "two", "three", "four", "five"} {
		_, err := store.Write("local", "compose/apps/demo", "token", []byte(value))
		require.NoError(t, err)
	}
	versions, err := store.ListHistory("local", "compose/apps/demo", "token")
	require.NoError(t, err)
	require.Len(t, versions, HistoryLimit)
	for _, version := range versions {
		info, statErr := os.Stat(filepath.Join(roots["local"], "apps", "demo", RuntimeDirectory, HistoryDirectory, "token", version.ID))
		require.NoError(t, statErr)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	_, err = store.Restore("local", "compose/apps/demo", "token", versions[len(versions)-1].ID)
	require.NoError(t, err)
	value, err := store.Read("local", "compose/apps/demo", "token")
	require.NoError(t, err)
	require.Equal(t, "two", string(value))
}

func TestPlainFileStoreListsAndRecoversDeletedSecret(t *testing.T) {
	store, _ := testStore(t)
	_, err := store.Write("local", "compose/apps/demo", "deleted_token", []byte("recover-me"))
	require.NoError(t, err)
	require.NoError(t, store.Delete("local", "compose/apps/demo", "deleted_token"))
	archived, err := store.ListArchived("local", "compose/apps/demo")
	require.NoError(t, err)
	require.Equal(t, []ArchivedSecret{{Name: "deleted_token", Versions: 1}}, archived)
	versions, err := store.ListHistory("local", "compose/apps/demo", "deleted_token")
	require.NoError(t, err)
	_, err = store.Restore("local", "compose/apps/demo", "deleted_token", versions[0].ID)
	require.NoError(t, err)
	value, err := store.Read("local", "compose/apps/demo", "deleted_token")
	require.NoError(t, err)
	require.Equal(t, "recover-me", string(value))
}

func TestPlainFileStoreAnalyzesComposeSecretReferences(t *testing.T) {
	store, roots := testStore(t)
	compose := `services:
  api:
    image: alpine
    secrets:
      - database_password
      - source: external_token
secrets:
  database_password:
    file: ./.secrets/database_password
  external_token:
    external: true
  misplaced:
    file: ../misplaced
  named_differently:
    file: ./.secrets/runtime-token.txt
`
	require.NoError(t, os.WriteFile(filepath.Join(roots["local"], "apps", "demo", "compose.yml"), []byte(compose), 0o600))
	_, err := store.Write("local", "compose/apps/demo", "database_password", []byte("value"))
	require.NoError(t, err)
	analysis, err := store.AnalyzeCompose("local", "compose/apps/demo")
	require.NoError(t, err)
	require.Equal(t, []string{"compose.yml"}, analysis.Manifests)
	require.Len(t, analysis.Secrets, 4)
	require.Equal(t, "database_password", analysis.Secrets[0].Name)
	require.True(t, analysis.Secrets[0].Managed)
	require.True(t, analysis.Secrets[0].Exists)
	require.Equal(t, []string{"api"}, analysis.Secrets[0].Services)
	require.True(t, analysis.Secrets[1].External)
	require.False(t, analysis.Secrets[2].Managed)
	require.Equal(t, "named_differently", analysis.Secrets[3].Name)
	require.True(t, analysis.Secrets[3].Managed, "Compose secret keys do not have to match their source filename")
	require.Equal(t, "runtime-token.txt", analysis.Secrets[3].RuntimeName)
}
