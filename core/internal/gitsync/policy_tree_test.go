package gitsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/RA341/dockman/internal/host/filesystem"
	"github.com/stretchr/testify/require"
)

func TestBindingPolicyTreePreviewsRulesWithoutPersistingThem(t *testing.T) {
	service, stackRoot, binding := prepareMultiStackBinding(t)
	alpha := filepath.Join(stackRoot, "alpha")
	require.NoError(t, os.WriteFile(filepath.Join(alpha, "application.json"), []byte("{}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(alpha, "debug.log"), []byte("debug\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(alpha, ".env"), []byte("TOKEN=secret\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(alpha, ".env.example"), []byte("TOKEN=example\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(alpha, "data", "deep"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(alpha, "data", "deep", "large.bin"), []byte("not inspected\n"), 0o644))
	repository, err := service.store.GetRepository(binding.RepositoryID)
	require.NoError(t, err)
	remoteChange(t, repository.RemoteURL, "stacks/alpha/git-only.conf", "from Git\n")
	_, err = service.PullRepository(context.Background(), repository.UUID)
	require.NoError(t, err)

	// Make an accidental eager descent into data deterministic. Listing alpha
	// must succeed because the selector only reads one directory at a time.
	service.ConfigureStackAccess(func(host, stackPath string) (filesystem.FileSystem, string, error) {
		return denyReadDirFS{FileSystem: filesystem.NewLocal(stackRoot), baseName: "data"}, ".", nil
	}, func() []string { return []string{"local"} }, filepath.Join(t.TempDir(), "backups"))

	root, err := service.BindingPolicyTree(binding.ID, BindingPolicyTreeInput{Profile: syncProfileComposeConfig, IncludePatterns: []string{}, ExcludePatterns: []string{}})
	require.NoError(t, err)
	require.Equal(t, []string{"alpha", "beta"}, policyTreePaths(root.Entries))
	require.Equal(t, "mixed", root.Entries[0].State)

	view, err := service.BindingPolicyTree(binding.ID, BindingPolicyTreeInput{Directory: "alpha", Profile: syncProfileComposeConfig, IncludePatterns: []string{}, ExcludePatterns: []string{}})
	require.NoError(t, err)
	entries := policyTreeEntryMap(view.Entries)
	require.Equal(t, "included", entries["alpha/application.json"].State)
	require.Equal(t, "excluded", entries["alpha/debug.log"].State)
	require.Equal(t, "protected", entries["alpha/.env"].State)
	require.False(t, entries["alpha/.env"].Selectable)
	require.Equal(t, "included", entries["alpha/.env.example"].State)
	require.Equal(t, "git", entries["alpha/git-only.conf"].Origin)
	require.Equal(t, "included", entries["alpha/git-only.conf"].State)
	require.Equal(t, "included", entries["alpha/compose.yml"].State)
	require.False(t, entries["alpha/compose.yml"].Selectable)
	require.Equal(t, "mixed", entries["alpha/data"].State)

	custom, err := service.BindingPolicyTree(binding.ID, BindingPolicyTreeInput{
		Directory: "alpha", Profile: syncProfileComposeConfig,
		IncludePatterns: []string{"/alpha/debug.log"}, ExcludePatterns: []string{"/alpha/application.json"},
	})
	require.NoError(t, err)
	customEntries := policyTreeEntryMap(custom.Entries)
	require.Equal(t, "included", customEntries["alpha/debug.log"].State)
	require.True(t, customEntries["alpha/debug.log"].ExplicitlyIncluded)
	require.Equal(t, "excluded", customEntries["alpha/application.json"].State)
	require.True(t, customEntries["alpha/application.json"].ExplicitlyExcluded)

	persisted, err := service.store.GetBinding(binding.ID)
	require.NoError(t, err)
	require.Empty(t, persisted.IncludePatterns, "previewing the tree must not persist generated rules")
	require.Empty(t, persisted.ExcludePatterns, "previewing the tree must not persist generated rules")

	data, err := service.BindingPolicyTree(binding.ID, BindingPolicyTreeInput{Directory: "alpha/data", Profile: syncProfileComposeConfig, IncludePatterns: []string{}, ExcludePatterns: []string{}})
	require.NoError(t, err)
	require.Empty(t, data.Entries)
	require.NotEmpty(t, data.Warnings)
}

func TestBindingPolicyTreeRejectsTraversal(t *testing.T) {
	service, _, binding := prepareMultiStackBinding(t)
	_, err := service.BindingPolicyTree(binding.ID, BindingPolicyTreeInput{Directory: "../outside"})
	require.ErrorContains(t, err, "path traversal")
}

func policyTreeEntryMap(entries []BindingPolicyTreeEntry) map[string]BindingPolicyTreeEntry {
	result := make(map[string]BindingPolicyTreeEntry, len(entries))
	for _, entry := range entries {
		result[entry.Path] = entry
	}
	return result
}

func policyTreePaths(entries []BindingPolicyTreeEntry) []string {
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry.Path)
	}
	return result
}
