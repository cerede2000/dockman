-- +goose Up
ALTER TABLE `git_stack_bindings` ADD COLUMN `auto_deploy_new_stacks` numeric NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE `git_stack_bindings` DROP COLUMN `auto_deploy_new_stacks`;
