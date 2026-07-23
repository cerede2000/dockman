-- +goose Up
ALTER TABLE `git_repositories` ADD COLUMN `exclude_patterns` text;
ALTER TABLE `git_stack_bindings` ADD COLUMN `initial_sync_state` text NOT NULL DEFAULT 'pending';
ALTER TABLE `git_stack_bindings` ADD COLUMN `initial_sync_error` text;
ALTER TABLE `git_stack_bindings` ADD COLUMN `initial_sync_at` datetime;
ALTER TABLE `git_stack_bindings` ADD COLUMN `auto_reconcile_enabled` numeric NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE `git_stack_bindings` DROP COLUMN `auto_reconcile_enabled`;
ALTER TABLE `git_stack_bindings` DROP COLUMN `initial_sync_at`;
ALTER TABLE `git_stack_bindings` DROP COLUMN `initial_sync_error`;
ALTER TABLE `git_stack_bindings` DROP COLUMN `initial_sync_state`;
ALTER TABLE `git_repositories` DROP COLUMN `exclude_patterns`;
