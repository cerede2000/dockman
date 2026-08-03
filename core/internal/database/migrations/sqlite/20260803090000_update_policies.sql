-- +goose Up
CREATE TABLE update_policies (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    host TEXT NOT NULL,
    target_type TEXT NOT NULL CHECK (target_type IN ('container', 'stack')),
    target_key TEXT NOT NULL,
    target_name TEXT NOT NULL,
    enabled NUMERIC NOT NULL DEFAULT 1,
    schedule TEXT NOT NULL DEFAULT '',
    rollback_enabled NUMERIC NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX idx_update_policy_target ON update_policies(host, target_type, target_key);

-- +goose Down
DROP INDEX IF EXISTS idx_update_policy_target;
DROP TABLE IF EXISTS update_policies;
