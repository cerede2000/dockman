-- +goose Up
ALTER TABLE `git_stack_bindings` ADD COLUMN `sync_profile` text NOT NULL DEFAULT 'compose_config';
ALTER TABLE `git_stack_bindings` ADD COLUMN `include_patterns` text;
ALTER TABLE `git_stack_bindings` ADD COLUMN `exclude_patterns` text;

-- +goose Down
ALTER TABLE `git_stack_bindings` DROP COLUMN `exclude_patterns`;
ALTER TABLE `git_stack_bindings` DROP COLUMN `include_patterns`;
ALTER TABLE `git_stack_bindings` DROP COLUMN `sync_profile`;
