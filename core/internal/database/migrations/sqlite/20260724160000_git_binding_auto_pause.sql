-- +goose Up
ALTER TABLE git_stack_bindings ADD COLUMN auto_sync_paused numeric NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE git_stack_bindings DROP COLUMN auto_sync_paused;
