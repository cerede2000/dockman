-- +goose Up
ALTER TABLE `git_stack_bindings` ADD COLUMN `compose_selection_mode` text NOT NULL DEFAULT 'all';
ALTER TABLE `git_stack_bindings` ADD COLUMN `selected_compose_paths` text;

-- +goose Down
ALTER TABLE `git_stack_bindings` DROP COLUMN `selected_compose_paths`;
ALTER TABLE `git_stack_bindings` DROP COLUMN `compose_selection_mode`;
