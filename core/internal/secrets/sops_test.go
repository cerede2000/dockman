package secrets

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/stretchr/testify/require"
)

type memorySOPSRunner struct{ plain []byte }

func (r *memorySOPSRunner) Run(_ context.Context, _ string, args []string, _ []string, input []byte) ([]byte, error) {
	if len(args) > 0 && args[0] == "encrypt" {
		r.plain = append(r.plain[:0], input...)
		return []byte("token: ENC[AES256_GCM,data:test]\nsops:\n  age: []\n"), nil
	}
	return append([]byte(nil), r.plain...), nil
}

func testSOPSProvider(t *testing.T) (*SOPSProvider, *PlainFileStore, map[string]string, *memorySOPSRunner) {
	t.Helper()
	runtime, roots := testStore(t)
	key := filepath.Join(t.TempDir(), "age-key.txt")
	require.NoError(t, os.WriteFile(key, []byte("AGE-SECRET-KEY-TEST"), 0o600))
	resolver := func(host, stackPath string) (filesystem.FileSystem, string, error) {
		root := roots[host]
		if root == "" || stackPath != "compose/apps/demo" {
			return nil, "", ErrInvalidStackPath
		}
		return filesystem.NewLocal(root), "apps/demo", nil
	}
	runner := &memorySOPSRunner{}
	provider := NewSOPSProvider(runtime, resolver, "true", key, "age1testrecipient")
	provider.runner = runner
	return provider, runtime, roots, runner
}

func TestSOPSExportEncryptsVerifiesAndWritesOnlyCiphertext(t *testing.T) {
	provider, runtime, roots, _ := testSOPSProvider(t)
	_, err := runtime.Write("local", "compose/apps/demo", "database_password", []byte("plaintext-must-not-land-in-source"))
	require.NoError(t, err)

	result, err := provider.Export(context.Background(), "local", "compose/apps/demo")
	require.NoError(t, err)
	require.Equal(t, []string{"database_password"}, result.Names)
	source := filepath.Join(roots["local"], "apps", "demo", SOPSSourceFile)
	content, err := os.ReadFile(source)
	require.NoError(t, err)
	require.NotContains(t, string(content), "plaintext-must-not-land-in-source")
	info, err := os.Stat(source)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestSOPSMaterializePreservesRuntimeValuesAbsentFromSource(t *testing.T) {
	provider, runtime, roots, runner := testSOPSProvider(t)
	_, err := runtime.Write("local", "compose/apps/demo", "preserved", []byte("keep-me"))
	require.NoError(t, err)
	runner.plain, err = json.Marshal(map[string]string{"token": "materialized-value"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(roots["local"], "apps", "demo", SOPSSourceFile), []byte("encrypted"), 0o644))

	result, err := provider.Materialize(context.Background(), "local", "compose/apps/demo")
	require.NoError(t, err)
	require.Equal(t, []string{"token"}, result.Names)
	value, err := runtime.Read("local", "compose/apps/demo", "token")
	require.NoError(t, err)
	require.Equal(t, "materialized-value", string(value))
	preserved, err := runtime.Read("local", "compose/apps/demo", "preserved")
	require.NoError(t, err)
	require.Equal(t, "keep-me", string(preserved))
}

func TestSOPSMaterializeRejectsNestedOrNonStringDocument(t *testing.T) {
	provider, _, roots, runner := testSOPSProvider(t)
	runner.plain = []byte(`{"nested":{"value":"secret"}}`)
	require.NoError(t, os.WriteFile(filepath.Join(roots["local"], "apps", "demo", SOPSSourceFile), []byte("encrypted"), 0o644))
	_, err := provider.Materialize(context.Background(), "local", "compose/apps/demo")
	require.ErrorContains(t, err, "flat string map")
}
