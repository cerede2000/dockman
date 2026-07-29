-- +goose Up
-- Existing folder links retain their historical behavior: all stacks selected
-- for Git synchronization remain automatic targets until the operator chooses
-- an explicit per-stack policy.
ALTER TABLE git_stack_bindings ADD COLUMN auto_sync_selection_mode text NOT NULL DEFAULT 'all';
ALTER TABLE git_stack_bindings ADD COLUMN auto_sync_compose_paths text;

-- +goose Down
ALTER TABLE git_stack_bindings DROP COLUMN auto_sync_compose_paths;
ALTER TABLE git_stack_bindings DROP COLUMN auto_sync_selection_mode;
