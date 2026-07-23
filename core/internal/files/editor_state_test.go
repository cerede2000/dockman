package files

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEditorLeaseIsHostScopedAndReleasedByOwner(t *testing.T) {
	state := newEditorState()
	state.setLease("local", "compose/app/compose.yml", "one")
	require.Equal(t, []string{"compose/app/compose.yml"}, state.dirtyPaths("local"))
	require.Empty(t, state.dirtyPaths("remote"))

	state.releaseLease("local", "compose/app/compose.yml", "another-session")
	require.Len(t, state.dirtyPaths("local"), 1)
	state.releaseLease("local", "compose/app/compose.yml", "one")
	require.Empty(t, state.dirtyPaths("local"))
}

func TestExpiredEditorLeaseIsPruned(t *testing.T) {
	state := newEditorState()
	key := editorKey("local", "compose/app/compose.yml")
	state.leases[key] = editorLease{session: "one", expires: time.Now().Add(-time.Second)}
	require.Empty(t, state.dirtyPaths("local"))
}

func TestFileRevisionIsStableAndContentSensitive(t *testing.T) {
	require.Equal(t, fileRevision([]byte("same")), fileRevision([]byte("same")))
	require.NotEqual(t, fileRevision([]byte("same")), fileRevision([]byte("changed")))
}
