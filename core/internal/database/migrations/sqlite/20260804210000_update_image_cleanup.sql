-- +goose Up
ALTER TABLE update_policies ADD COLUMN cleanup_enabled NUMERIC NOT NULL DEFAULT 0;
ALTER TABLE update_policies ADD COLUMN cleanup_keep INTEGER NOT NULL DEFAULT 1 CHECK (cleanup_keep BETWEEN 0 AND 10);

CREATE TABLE update_image_cleanups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    host TEXT NOT NULL,
    target_key TEXT NOT NULL,
    container_name TEXT NOT NULL,
    image_id TEXT NOT NULL,
    retention INTEGER NOT NULL DEFAULT 1 CHECK (retention BETWEEN 0 AND 10),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'removed')),
    reason TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_update_image_cleanup_candidate ON update_image_cleanups(host, target_key, image_id);
CREATE INDEX idx_update_image_cleanup_host ON update_image_cleanups(host, status, created_at);

-- +goose Down
DROP INDEX IF EXISTS idx_update_image_cleanup_host;
DROP INDEX IF EXISTS idx_update_image_cleanup_candidate;
DROP TABLE IF EXISTS update_image_cleanups;
ALTER TABLE update_policies DROP COLUMN cleanup_keep;
ALTER TABLE update_policies DROP COLUMN cleanup_enabled;
