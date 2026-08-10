package gitsync

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// SaveBinding checks immutability with Unscoped() but writes in the default
// scope. A folder link unlinked without "forget" is only soft-deleted, so the
// two halves disagree about which rows exist. These tests pin what actually
// happens in that window rather than what the code appears to intend.
func seedBinding(t *testing.T, store *Store) StackBinding {
	t.Helper()
	row := StackBinding{
		UUID:           "binding-uuid",
		RepositoryUUID: "repo-uuid",
		Host:           "nas",
		StackPath:      "media",
		SubPath:        "media",
		AutoSyncState:  "idle",
	}
	require.NoError(t, store.SaveBinding(&row))
	return row
}

// A worker holding a binding in memory while the user unlinks it will call
// SaveBinding afterwards to record its run. The immutability read sees the
// soft-deleted row and lets it through; the write cannot reach that row.
func TestSaveBindingOnASoftDeletedLink(t *testing.T) {
	_, db := testService(t, false)
	store := NewStore(db)
	row := seedBinding(t, store)

	require.NoError(t, store.DeleteBinding(row.UUID, false))

	row.AutoSyncState = "running"
	err := store.SaveBinding(&row)

	// The caller has to be told, not silently ignored: it believes it has
	// persisted a run that no longer has anywhere to go.
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	var revived StackBinding
	require.NoError(t, db.Unscoped().Where("uuid = ?", row.UUID).Take(&revived).Error)
	require.True(t, revived.DeletedAt.Valid,
		"saving state on an unlinked binding must not resurrect it")
	require.NotEqual(t, "running", revived.AutoSyncState,
		"an unlinked binding must not be re-enrolled in automation")
	live, err := store.ListBindings()
	require.NoError(t, err)
	require.Empty(t, live, "the link must stay out of the live listing")
}

// The immutability guard reads soft-deleted rows, so a brand new link that
// happens to reuse a retired UUID is judged against the retired endpoints.
func TestSaveBindingRejectsANewLinkReusingARetiredUUID(t *testing.T) {
	_, db := testService(t, false)
	store := NewStore(db)
	row := seedBinding(t, store)
	require.NoError(t, store.DeleteBinding(row.UUID, false))

	fresh := StackBinding{
		UUID:           row.UUID,
		RepositoryUUID: "other-repo",
		Host:           "nas",
		StackPath:      "downloads",
		SubPath:        "downloads",
	}
	err := store.SaveBinding(&fresh)
	t.Logf("reusing a retired uuid with new endpoints: %v", err)
	require.Error(t, err, "a retired row still guards its endpoints")
}

// Baseline: the ordinary path must keep working untouched.
func TestSaveBindingUpdatesMutableStateOnALiveLink(t *testing.T) {
	_, db := testService(t, false)
	store := NewStore(db)
	row := seedBinding(t, store)

	row.AutoSyncState = "running"
	require.NoError(t, store.SaveBinding(&row))

	stored, err := store.GetBinding(row.UUID)
	require.NoError(t, err)
	require.Equal(t, "running", stored.AutoSyncState)
}

func TestSaveBindingStillRefusesToMoveALiveLink(t *testing.T) {
	_, db := testService(t, false)
	store := NewStore(db)
	row := seedBinding(t, store)

	row.StackPath = "somewhere-else"
	require.ErrorContains(t, store.SaveBinding(&row), "immutable")

	stored, err := store.GetBinding(row.UUID)
	require.NoError(t, err)
	require.Equal(t, "media", stored.StackPath)
}
