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
		"git_credentials", "git_repositories", "git_stack_bindings", "git_binding_baselines", "git_operations", "git_deployments", "git_stack_statuses",
	} {
		var count int
		require.NoError(t, db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count))
		require.Equal(t, 1, count, table)
	}
	for _, column := range []string{"sync_profile", "include_patterns", "exclude_patterns", "compose_selection_mode", "selected_compose_paths"} {
		var count int
		require.NoError(t, db.QueryRow("SELECT count(*) FROM pragma_table_info('git_stack_bindings') WHERE name=?", column).Scan(&count))
		require.Equal(t, 1, count, column)
	}
	var pauseReasonColumns int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM pragma_table_info('git_stack_statuses') WHERE name='pause_reason'").Scan(&pauseReasonColumns))
	require.Equal(t, 1, pauseReasonColumns)

	var cleanerCronColumns int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM pragma_table_info('prune_configs') WHERE name='cron_expression'").Scan(&cleanerCronColumns))
	require.Equal(t, 1, cleanerCronColumns)
}
