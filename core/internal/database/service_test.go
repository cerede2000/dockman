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
		"git_credentials", "git_repositories", "git_stack_bindings", "git_binding_baselines", "git_operations", "git_deployments", "git_stack_statuses", "git_repository_webhooks", "git_webhook_deliveries", "update_policies", "update_scan_results", "update_scan_runs", "update_smtp_configs", "update_notification_states", "update_notification_channels", "update_notification_channel_states", "update_notification_deliveries", "update_execution_runs", "update_execution_results", "update_execution_blocks", "update_automation_controls", "update_image_cleanups",
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

	_, err = db.Exec(`INSERT INTO git_stack_bindings
		(uuid, repository_uuid, host, stack_path, sub_path, enabled)
		VALUES ('binding-root', 'repository', 'local', 'compose', '.', 1)`)
	require.NoError(t, err)
	_, err = db.Exec("UPDATE git_stack_bindings SET sync_profile='compose_only' WHERE uuid='binding-root'")
	require.NoError(t, err, "mutable Folder Link settings must remain writable")
	_, err = db.Exec("UPDATE git_stack_bindings SET sub_path='stacks/compose' WHERE uuid='binding-root'")
	require.ErrorContains(t, err, "folder link target is immutable")
	var repositorySubPath string
	require.NoError(t, db.QueryRow("SELECT sub_path FROM git_stack_bindings WHERE uuid='binding-root'").Scan(&repositorySubPath))
	require.Equal(t, ".", repositorySubPath)

	var cleanerCronColumns int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM pragma_table_info('prune_configs') WHERE name='cron_expression'").Scan(&cleanerCronColumns))
	require.Equal(t, 1, cleanerCronColumns)

	var eventTypeColumns int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM pragma_table_info('update_notification_channels') WHERE name='event_types'").Scan(&eventTypeColumns))
	require.Equal(t, 1, eventTypeColumns)

	for _, column := range []string{"host", "target_type", "target_key", "target_name", "enabled", "schedule", "rollback_enabled", "cleanup_enabled", "cleanup_keep", "version_policy", "version_prerelease"} {
		var count int
		require.NoError(t, db.QueryRow("SELECT count(*) FROM pragma_table_info('update_policies') WHERE name=?", column).Scan(&count))
		require.Equal(t, 1, count, column)
	}
	for _, column := range []string{"current_tag", "latest_tag", "version_policy", "version_available", "version_reason"} {
		var count int
		require.NoError(t, db.QueryRow("SELECT count(*) FROM pragma_table_info('update_scan_results') WHERE name=?", column).Scan(&count))
		require.Equal(t, 1, count, column)
	}
	var versionCountColumns int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM pragma_table_info('update_scan_runs') WHERE name='versions'").Scan(&versionCountColumns))
	require.Equal(t, 1, versionCountColumns)
}
