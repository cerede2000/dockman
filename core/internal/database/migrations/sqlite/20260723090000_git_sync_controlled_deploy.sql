-- +goose Up
ALTER TABLE `git_stack_bindings` ADD COLUMN `auto_deploy_enabled` numeric NOT NULL DEFAULT 0;
ALTER TABLE `git_stack_bindings` ADD COLUMN `auto_deploy_compose_paths` text;
ALTER TABLE `git_stack_bindings` ADD COLUMN `auto_deploy_state` text NOT NULL DEFAULT 'disabled';
ALTER TABLE `git_stack_bindings` ADD COLUMN `auto_deploy_error` text;
ALTER TABLE `git_stack_bindings` ADD COLUMN `last_auto_deploy_at` datetime;

-- +goose Down
ALTER TABLE `git_stack_bindings` DROP COLUMN `last_auto_deploy_at`;
ALTER TABLE `git_stack_bindings` DROP COLUMN `auto_deploy_error`;
ALTER TABLE `git_stack_bindings` DROP COLUMN `auto_deploy_state`;
ALTER TABLE `git_stack_bindings` DROP COLUMN `auto_deploy_compose_paths`;
ALTER TABLE `git_stack_bindings` DROP COLUMN `auto_deploy_enabled`;
