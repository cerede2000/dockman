package gitsync

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func seedHostBinding(t *testing.T, store *Store, uuid, host, stackPath string) {
	t.Helper()
	row := StackBinding{
		UUID:           uuid,
		RepositoryUUID: "repo-uuid",
		Host:           host,
		StackPath:      stackPath,
		SubPath:        stackPath,
	}
	require.NoError(t, store.SaveBinding(&row))
}

// Renaming a host used to orphan its folder links: the host name is their key
// and nothing propagated. Because SaveBinding refuses to move an endpoint, the
// only way back was to unlink and relink, rebuilding the baseline.
func TestRenameBindingHostRepointsOnlyThatHost(t *testing.T) {
	_, db := testService(t, false)
	store := NewStore(db)
	seedHostBinding(t, store, "a", "nas", "media")
	seedHostBinding(t, store, "b", "nas", "downloads")
	seedHostBinding(t, store, "c", "other", "infra")

	rewritten, err := store.RenameBindingHost("nas", "nas-01")
	require.NoError(t, err)
	require.Equal(t, 2, rewritten)

	moved, err := store.ListBindingsForHost("nas-01")
	require.NoError(t, err)
	require.Len(t, moved, 2)

	untouched, err := store.ListBindingsForHost("other")
	require.NoError(t, err)
	require.Len(t, untouched, 1, "another host's links must not move")

	stale, err := store.ListBindingsForHost("nas")
	require.NoError(t, err)
	require.Empty(t, stale)
}

// The rewrite is the one thing allowed past the immutability guard, so its
// own inputs have to be tight: it must never be usable to blank a host out.
func TestRenameBindingHostRefusesAnEmptyName(t *testing.T) {
	_, db := testService(t, false)
	store := NewStore(db)
	seedHostBinding(t, store, "a", "nas", "media")

	_, err := store.RenameBindingHost("nas", "   ")
	require.Error(t, err)
	_, err = store.RenameBindingHost("", "nas-01")
	require.Error(t, err)

	kept, err := store.ListBindingsForHost("nas")
	require.NoError(t, err)
	require.Len(t, kept, 1)
}

func TestRenameBindingHostIsANoOpOnTheSameName(t *testing.T) {
	_, db := testService(t, false)
	store := NewStore(db)
	seedHostBinding(t, store, "a", "nas", "media")

	rewritten, err := store.RenameBindingHost("nas", "nas")
	require.NoError(t, err)
	require.Zero(t, rewritten)
}

// SaveBinding must keep refusing an endpoint change: the rename path is a
// deliberate exception, not a door left open.
func TestSaveBindingStillRefusesAHostChange(t *testing.T) {
	_, db := testService(t, false)
	store := NewStore(db)
	seedHostBinding(t, store, "a", "nas", "media")

	row, err := store.GetBinding("a")
	require.NoError(t, err)
	row.Host = "nas-01"
	require.ErrorContains(t, store.SaveBinding(&row), "immutable")
}
