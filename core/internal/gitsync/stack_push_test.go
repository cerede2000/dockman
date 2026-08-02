package gitsync

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStackTransferPathsKeepsPushScopedToOneNestedStack(t *testing.T) {
	preview := TransferPreview{Entries: []PreviewEntry{
		{Path: "alpha/compose.yml", Status: "modify"},
		{Path: "alpha/config/app.yml", Status: "add"},
		{Path: "beta/compose.yml", Status: "modify"},
		{Path: "beta/config/app.yml", Status: "conflict"},
	}}
	compose := []string{"alpha/compose.yml", "beta/compose.yml"}

	selected, conflicts := stackTransferPaths(preview, compose, "alpha/compose.yml")
	require.Equal(t, []string{"alpha/compose.yml", "alpha/config/app.yml"}, selected)
	require.Zero(t, conflicts)

	selected, conflicts = stackTransferPaths(preview, compose, "beta/compose.yml")
	require.Equal(t, []string{"beta/compose.yml"}, selected)
	require.Equal(t, 1, conflicts)
}

func TestStackImportPathsRejectsAmbiguousChangesAndKeepsScope(t *testing.T) {
	preview := TransferPreview{Entries: []PreviewEntry{
		{Path: "alpha/compose.yml", Status: "modify"},
		{Path: "alpha/.env.example", Status: "add"},
		{Path: "beta/compose.yml", Status: "add", ConflictKind: "destination_deleted"},
		{Path: "gamma/compose.yml", Status: "deleted_on_git"},
	}}
	compose := []string{"alpha/compose.yml", "beta/compose.yml", "gamma/compose.yml"}

	selected, conflicts, preserved := stackImportPaths(preview, compose, "alpha/compose.yml")
	require.Equal(t, []string{"alpha/.env.example", "alpha/compose.yml"}, selected)
	require.Zero(t, conflicts)
	require.False(t, preserved)

	selected, conflicts, preserved = stackImportPaths(preview, compose, "beta/compose.yml")
	require.Empty(t, selected)
	require.Equal(t, 1, conflicts)
	require.False(t, preserved)

	selected, conflicts, preserved = stackImportPaths(preview, compose, "gamma/compose.yml")
	require.Empty(t, selected)
	require.Zero(t, conflicts)
	require.True(t, preserved)
}
