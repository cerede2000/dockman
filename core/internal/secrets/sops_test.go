package secrets

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
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

func TestSOPSInlineModeKeepsOnlyCiphertextAndInjectsComposeEnvironment(t *testing.T) {
	provider, runtime, roots, _ := testSOPSProvider(t)
	_, err := runtime.Write("local", "compose/apps/demo", "API_TOKEN", []byte("inline-only-value"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(roots["local"], "apps", "demo", "compose.yml"), []byte("services: {}\n"), 0o600))

	result, err := provider.EnableInline(context.Background(), "local", "compose/apps/demo", "compose.yml")
	require.NoError(t, err)
	require.Equal(t, []string{"API_TOKEN"}, result.Names)
	stackRoot := filepath.Join(roots["local"], "apps", "demo")
	_, err = os.Stat(filepath.Join(stackRoot, RuntimeDirectory))
	require.ErrorIs(t, err, os.ErrNotExist, "plaintext runtime values and history must be removed")
	ciphertext, err := os.ReadFile(filepath.Join(stackRoot, SOPSSourceFile))
	require.NoError(t, err)
	require.NotContains(t, string(ciphertext), "inline-only-value")
	marker, err := os.Stat(filepath.Join(stackRoot, SOPSInlineMarkerFile))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), marker.Mode().Perm())
	script, err := os.Stat(filepath.Join(stackRoot, SOPSRecoveryScriptFile))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), script.Mode().Perm())
	require.NoError(t, exec.Command("sh", "-n", filepath.Join(stackRoot, SOPSRecoveryScriptFile)).Run())

	environment, err := provider.ComposeEnvironment(context.Background(), "local", filesystem.NewLocal(roots["local"]), "apps/demo/compose.yml")
	require.NoError(t, err)
	require.Equal(t, []string{"API_TOKEN=inline-only-value"}, environment)
}

func TestSOPSInlineEditsRewriteCiphertextWithoutMaterializing(t *testing.T) {
	provider, runtime, roots, _ := testSOPSProvider(t)
	_, err := runtime.Write("local", "compose/apps/demo", "API_TOKEN", []byte("first"))
	require.NoError(t, err)
	_, err = provider.EnableInline(context.Background(), "local", "compose/apps/demo", "compose.yml")
	require.NoError(t, err)

	_, err = provider.WriteInline(context.Background(), "local", "compose/apps/demo", "API_TOKEN", []byte("second"))
	require.NoError(t, err)
	value, err := provider.ReadInline(context.Background(), "local", "compose/apps/demo", "API_TOKEN")
	require.NoError(t, err)
	require.Equal(t, "second", string(value))
	require.NoError(t, provider.DeleteInline(context.Background(), "local", "compose/apps/demo", "API_TOKEN"))
	_, err = provider.ReadInline(context.Background(), "local", "compose/apps/demo", "API_TOKEN")
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(roots["local"], "apps", "demo", RuntimeDirectory))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestSOPSInlineRejectsNonEnvironmentSecretNames(t *testing.T) {
	provider, runtime, _, _ := testSOPSProvider(t)
	_, err := runtime.Write("local", "compose/apps/demo", "cloudflare-token", []byte("value"))
	require.NoError(t, err)
	_, err = provider.EnableInline(context.Background(), "local", "compose/apps/demo", "compose.yml")
	require.ErrorContains(t, err, "cannot be used inline")
}

func TestSOPSInlineRefusesToRemoveFilesStillReferencedByCompose(t *testing.T) {
	provider, runtime, roots, _ := testSOPSProvider(t)
	_, err := runtime.Write("local", "compose/apps/demo", "API_TOKEN", []byte("value"))
	require.NoError(t, err)
	compose := `services:
  app:
    image: example/app
    secrets: [api_token]
secrets:
  api_token:
    file: ./.secrets/API_TOKEN
`
	require.NoError(t, os.WriteFile(filepath.Join(roots["local"], "apps", "demo", "compose.yml"), []byte(compose), 0o600))
	_, err = provider.EnableInline(context.Background(), "local", "compose/apps/demo", "compose.yml")
	require.ErrorContains(t, err, "still uses")
	value, readErr := runtime.Read("local", "compose/apps/demo", "API_TOKEN")
	require.NoError(t, readErr)
	require.Equal(t, "value", string(value), "failed conversion must preserve plaintext runtime source")
}

func TestSOPSInlineSupportsEnvironmentBackedComposeFileSecrets(t *testing.T) {
	provider, runtime, roots, _ := testSOPSProvider(t)
	_, err := runtime.Write("local", "compose/apps/demo", "API_TOKEN", []byte("file-secret-value"))
	require.NoError(t, err)
	_, err = runtime.Write("local", "compose/apps/demo", "DIRECT_TOKEN", []byte("direct-value"))
	require.NoError(t, err)
	compose := `services:
  app:
    image: example/app
    environment:
      DIRECT_TOKEN: ${DIRECT_TOKEN}
    secrets:
      - source: api_token
        target: api_token
        mode: 0440
secrets:
  api_token:
    environment: API_TOKEN
`
	require.NoError(t, os.WriteFile(filepath.Join(roots["local"], "apps", "demo", "compose.yml"), []byte(compose), 0o600))

	_, err = provider.EnableInline(context.Background(), "local", "compose/apps/demo", "compose.yml")
	require.NoError(t, err)
	environment, err := provider.ComposeEnvironment(context.Background(), "local", filesystem.NewLocal(roots["local"]), "apps/demo/compose.yml")
	require.NoError(t, err)
	require.Equal(t, []string{"API_TOKEN=file-secret-value", "DIRECT_TOKEN=direct-value"}, environment)
}

func TestSOPSInlineRequiresEnvironmentBackedComposeSecretValue(t *testing.T) {
	provider, runtime, roots, _ := testSOPSProvider(t)
	_, err := runtime.Write("local", "compose/apps/demo", "OTHER_TOKEN", []byte("preserved"))
	require.NoError(t, err)
	compose := `services:
  app:
    image: example/app
    secrets: [api_token]
secrets:
  api_token:
    environment: API_TOKEN
`
	require.NoError(t, os.WriteFile(filepath.Join(roots["local"], "apps", "demo", "compose.yml"), []byte(compose), 0o600))

	_, err = provider.EnableInline(context.Background(), "local", "compose/apps/demo", "compose.yml")
	require.ErrorContains(t, err, `requires encrypted value "API_TOKEN"`)
	value, readErr := runtime.Read("local", "compose/apps/demo", "OTHER_TOKEN")
	require.NoError(t, readErr)
	require.Equal(t, "preserved", string(value), "failed conversion must preserve plaintext runtime source")
}

func TestSOPSInlineProtectsEnvironmentBackedComposeSecretFromDeletion(t *testing.T) {
	provider, runtime, roots, _ := testSOPSProvider(t)
	_, err := runtime.Write("local", "compose/apps/demo", "API_TOKEN", []byte("preserved"))
	require.NoError(t, err)
	compose := `services:
  app:
    image: example/app
    secrets: [api_token]
secrets:
  api_token:
    environment: API_TOKEN
`
	require.NoError(t, os.WriteFile(filepath.Join(roots["local"], "apps", "demo", "compose.yml"), []byte(compose), 0o600))
	_, err = provider.EnableInline(context.Background(), "local", "compose/apps/demo", "compose.yml")
	require.NoError(t, err)

	err = provider.DeleteInline(context.Background(), "local", "compose/apps/demo", "API_TOKEN")
	require.ErrorContains(t, err, `supplies Compose file secret "api_token"`)
	value, readErr := provider.ReadInline(context.Background(), "local", "compose/apps/demo", "API_TOKEN")
	require.NoError(t, readErr)
	require.Equal(t, "preserved", string(value))
}
