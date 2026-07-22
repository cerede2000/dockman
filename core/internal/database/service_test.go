package database

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedMigrationsCreateGitSyncFoundation(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+t.Name()+"?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	require.NoError(t, migrate(db, migrationDir, migrationPath))
	for _, table := range []string{
		"git_credentials", "git_repositories", "git_stack_bindings", "git_operations", "git_deployments",
	} {
		var count int
		require.NoError(t, db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count))
		require.Equal(t, 1, count, table)
	}
}
