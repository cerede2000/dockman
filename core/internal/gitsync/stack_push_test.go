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
