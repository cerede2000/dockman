-- +goose Up
ALTER TABLE `git_stack_bindings` ADD COLUMN `auto_sync_enabled` numeric NOT NULL DEFAULT 0;
ALTER TABLE `git_stack_bindings` ADD COLUMN `auto_sync_interval_minutes` integer NOT NULL DEFAULT 15;
ALTER TABLE `git_stack_bindings` ADD COLUMN `auto_sync_state` text NOT NULL DEFAULT 'disabled';
ALTER TABLE `git_stack_bindings` ADD COLUMN `auto_sync_error` text;
ALTER TABLE `git_stack_bindings` ADD COLUMN `last_auto_sync_at` datetime;
ALTER TABLE `git_stack_bindings` ADD COLUMN `last_auto_sync_success_at` datetime;
ALTER TABLE `git_stack_bindings` ADD COLUMN `last_auto_sync_commit` text;

-- +goose Down
ALTER TABLE `git_stack_bindings` DROP COLUMN `last_auto_sync_commit`;
ALTER TABLE `git_stack_bindings` DROP COLUMN `last_auto_sync_success_at`;
ALTER TABLE `git_stack_bindings` DROP COLUMN `last_auto_sync_at`;
ALTER TABLE `git_stack_bindings` DROP COLUMN `auto_sync_error`;
ALTER TABLE `git_stack_bindings` DROP COLUMN `auto_sync_state`;
ALTER TABLE `git_stack_bindings` DROP COLUMN `auto_sync_interval_minutes`;
ALTER TABLE `git_stack_bindings` DROP COLUMN `auto_sync_enabled`;
