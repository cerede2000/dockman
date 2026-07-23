package gitsync

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExcludeDirtyEditorStacksOnlyBlocksAffectedNestedStack(t *testing.T) {
	service := &Service{dirtyEditorPaths: func(string) []string {
		return []string{"compose/substacks/alpha/config/app.yml"}
	}}
	binding := StackBinding{Host: "local", StackPath: "compose/substacks", ComposeSelectionMode: composeSelectionSelected, SelectedComposePaths: "alpha/compose.yml\nbeta/compose.yml"}
	selected := map[string]transferFile{
		"alpha/compose.yml": {}, "alpha/config/app.yml": {},
		"beta/compose.yml": {}, "beta/config/app.yml": {},
	}

	filtered, blocked := service.excludeDirtyEditorStacks(binding, selected)
	require.Equal(t, []string{"alpha/compose.yml"}, blocked)
	require.NotContains(t, filtered, "alpha/compose.yml")
	require.NotContains(t, filtered, "alpha/config/app.yml")
	require.Contains(t, filtered, "beta/compose.yml")
	require.Contains(t, filtered, "beta/config/app.yml")
}
