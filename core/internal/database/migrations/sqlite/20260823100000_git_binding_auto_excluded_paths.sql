-- +goose Up
-- Paths the host's ACLs keep Dockman from reading. Recorded per Folder Link so
-- the next cycle leaves them out of both inventories instead of reporting an
-- error nobody can act on from Dockman. Re-checked cheaply on every cycle: a
-- path that becomes readable again is dropped and synchronizes normally.
ALTER TABLE git_stack_bindings ADD COLUMN auto_excluded_paths TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE git_stack_bindings DROP COLUMN auto_excluded_paths;
