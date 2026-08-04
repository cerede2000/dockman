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
		"git_credentials", "git_repositories", "git_stack_bindings", "git_binding_baselines", "git_operations", "git_deployments", "git_stack_statuses", "update_policies", "update_scan_results", "update_scan_runs", "update_smtp_configs", "update_notification_states", "update_notification_deliveries", "update_execution_runs", "update_execution_results", "update_execution_blocks",
	} {
		var count int
		require.NoError(t, db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count))
		require.Equal(t, 1, count, table)
	}
	for _, column := range []string{"sync_profile", "include_patterns", "exclude_patterns", "compose_selection_mode", "selected_compose_paths", "auto_sync_selection_mode", "auto_sync_compose_paths"} {
		var count int
		require.NoError(t, db.QueryRow("SELECT count(*) FROM pragma_table_info('git_stack_bindings') WHERE name=?", column).Scan(&count))
		require.Equal(t, 1, count, column)
	}
	for _, column := range []string{"commit_author_name", "commit_author_email"} {
		var count int
		require.NoError(t, db.QueryRow("SELECT count(*) FROM pragma_table_info('git_repositories') WHERE name=?", column).Scan(&count))
		require.Equal(t, 1, count, column)
	}
	var pauseReasonColumns int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM pragma_table_info('git_stack_statuses') WHERE name='pause_reason'").Scan(&pauseReasonColumns))
	require.Equal(t, 1, pauseReasonColumns)

	var cleanerCronColumns int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM pragma_table_info('prune_configs') WHERE name='cron_expression'").Scan(&cleanerCronColumns))
	require.Equal(t, 1, cleanerCronColumns)

	for _, column := range []string{"host", "target_type", "target_key", "target_name", "enabled", "schedule", "rollback_enabled"} {
		var count int
		require.NoError(t, db.QueryRow("SELECT count(*) FROM pragma_table_info('update_policies') WHERE name=?", column).Scan(&count))
		require.Equal(t, 1, count, column)
	}
}
