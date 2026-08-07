package secrets

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/stretchr/testify/require"
)

type catalogSOPSRunner struct {
	ciphertexts map[string][]byte
	next        int
}

func (r *catalogSOPSRunner) Run(_ context.Context, _ string, args []string, _ []string, input []byte) ([]byte, error) {
	if len(args) > 0 && args[0] == "encrypt" {
		values := map[string]string{}
		if err := json.Unmarshal(input, &values); err != nil {
			return nil, err
		}
		names := sortedValueNames(values)
		var output strings.Builder
		for _, name := range names {
			fmt.Fprintf(&output, "%s: ENC[AES256_GCM,data:%d]\n", name, r.next)
		}
		fmt.Fprintf(&output, "sops:\n  age: []\n  mac: ENC[AES256_GCM,data:%d]\n", r.next)
		ciphertext := output.String()
		if r.ciphertexts == nil {
			r.ciphertexts = map[string][]byte{}
		}
		r.ciphertexts[ciphertext] = append([]byte(nil), input...)
		r.next++
		return []byte(ciphertext), nil
	}
	plain, ok := r.ciphertexts[string(input)]
	if !ok {
		return nil, fmt.Errorf("unknown ciphertext")
	}
	return append([]byte(nil), plain...), nil
}

// countingReconcileFS observes the atomic rename that publishes a host
// reconciliation request.
type countingReconcileFS struct {
	filesystem.FileSystem
	requests int
}

func (f *countingReconcileFS) Rename(oldPath, newPath string) error {
	if newPath == HostRuntimeReconcileRequestFile {
		f.requests++
	}
	return f.FileSystem.Rename(oldPath, newPath)
}

// Every stack of a host writes the same request file, and the host unit
// re-materializes all of them on every trigger. One request per stack was
// therefore redundant work, and enough of it to trip the systemd start limit
// and leave the watch permanently failed.
func TestGlobalAssignmentRequestsHostReconciliationOnce(t *testing.T) {
	root := t.TempDir()
	stacks := []string{"alpha", "beta", "gamma"}
	for _, stack := range stacks {
		directory := filepath.Join(root, stack)
		require.NoError(t, os.MkdirAll(filepath.Join(directory, RuntimeDirectory), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "compose.yml"), []byte("services: {}\n"), 0o600))
	}
	counting := &countingReconcileFS{FileSystem: filesystem.NewLocal(root)}
	resolver := func(_ string, stackPath string) (filesystem.FileSystem, string, error) {
		if stackPath == "compose" {
			return counting, ".", nil
		}
		if !strings.HasPrefix(stackPath, "compose/") {
			return nil, "", ErrInvalidStackPath
		}
		return counting, strings.TrimPrefix(stackPath, "compose/"), nil
	}
	store := NewPlainFileStore(resolver)
	store.ConfigureAliases(func(string) ([]string, error) { return []string{"compose"}, nil })
	key := filepath.Join(t.TempDir(), "age-key.txt")
	require.NoError(t, os.WriteFile(key, []byte("AGE-SECRET-KEY-TEST"), 0o600))
	provider := NewSOPSProvider(store, resolver, "true", key, "age1testrecipient")
	provider.runner = &catalogSOPSRunner{}
	service := NewService(store)
	service.ConfigureSOPS(provider)

	paths := make([]string, 0, len(stacks))
	for _, stack := range stacks {
		path := "compose/" + stack
		_, err := provider.EnableInline(context.Background(), "local", path, "compose.yml")
		require.NoError(t, err)
		paths = append(paths, path)
	}
	counting.requests = 0

	assignments, err := service.AssignEncrypted(context.Background(), "local", "SHARED_TOKEN", []byte("same-value"), paths)
	require.NoError(t, err)
	require.Len(t, assignments, len(stacks))
	require.Equal(t, 1, counting.requests)
}

func TestGlobalCatalogAndEncryptedAssignmentRemainPerStack(t *testing.T) {
	root := t.TempDir()
	for _, stack := range []string{"alpha", "beta"} {
		directory := filepath.Join(root, stack)
		require.NoError(t, os.MkdirAll(filepath.Join(directory, RuntimeDirectory), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "compose.yml"), []byte("services: {}\n"), 0o600))
	}
	resolver := func(_ string, stackPath string) (filesystem.FileSystem, string, error) {
		if stackPath == "compose" {
			return filesystem.NewLocal(root), ".", nil
		}
		if !strings.HasPrefix(stackPath, "compose/") {
			return nil, "", ErrInvalidStackPath
		}
		return filesystem.NewLocal(root), strings.TrimPrefix(stackPath, "compose/"), nil
	}
	store := NewPlainFileStore(resolver)
	store.ConfigureAliases(func(string) ([]string, error) { return []string{"compose"}, nil })
	key := filepath.Join(t.TempDir(), "age-key.txt")
	require.NoError(t, os.WriteFile(key, []byte("AGE-SECRET-KEY-TEST"), 0o600))
	provider := NewSOPSProvider(store, resolver, "true", key, "age1testrecipient")
	provider.runner = &catalogSOPSRunner{}
	service := NewService(store)
	service.ConfigureSOPS(provider)

	for _, stack := range []string{"alpha", "beta"} {
		path := "compose/" + stack
		_, err := provider.EnableInline(context.Background(), "local", path, "compose.yml")
		require.NoError(t, err)
	}
	assignments, err := service.AssignEncrypted(context.Background(), "local", "SHARED_TOKEN", []byte("same-value"), []string{"compose/beta", "compose/alpha"})
	require.NoError(t, err)
	require.Len(t, assignments, 2)

	catalog, err := service.ListCatalog(context.Background(), "local")
	require.NoError(t, err)
	require.Len(t, catalog.Stacks, 2)
	var paths []string
	for _, secret := range catalog.Secrets {
		if secret.Name != "SHARED_TOKEN" {
			continue
		}
		for _, assignment := range secret.Assignments {
			paths = append(paths, assignment.StackPath)
		}
	}
	sort.Strings(paths)
	require.Equal(t, []string{"compose/alpha", "compose/beta"}, paths)

	for _, stack := range []string{"alpha", "beta"} {
		ciphertext, readErr := os.ReadFile(filepath.Join(root, stack, SOPSSourceFile))
		require.NoError(t, readErr)
		require.Contains(t, string(ciphertext), "SHARED_TOKEN:")
		require.NotContains(t, string(ciphertext), "same-value")
	}
}
