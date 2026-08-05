-- +goose Up
CREATE TABLE git_repository_webhooks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at DATETIME,
    updated_at DATETIME,
    deleted_at DATETIME,
    uuid TEXT NOT NULL,
    repository_uuid TEXT NOT NULL,
    enabled NUMERIC NOT NULL DEFAULT 0,
    encrypted_secret BLOB NOT NULL,
    last_delivery_id TEXT,
    last_event TEXT,
    last_status TEXT,
    last_error TEXT,
    last_received_at DATETIME
);
CREATE UNIQUE INDEX idx_git_repository_webhooks_uuid ON git_repository_webhooks(uuid);
CREATE UNIQUE INDEX idx_git_repository_webhooks_repository_uuid ON git_repository_webhooks(repository_uuid);
CREATE INDEX idx_git_repository_webhooks_deleted_at ON git_repository_webhooks(deleted_at);

CREATE TABLE git_webhook_deliveries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at DATETIME,
    updated_at DATETIME,
    deleted_at DATETIME,
    webhook_uuid TEXT NOT NULL,
    delivery_id TEXT NOT NULL,
    event TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_git_webhook_delivery ON git_webhook_deliveries(webhook_uuid, delivery_id);
CREATE INDEX idx_git_webhook_deliveries_deleted_at ON git_webhook_deliveries(deleted_at);

-- +goose Down
DROP INDEX IF EXISTS idx_git_webhook_deliveries_deleted_at;
DROP INDEX IF EXISTS idx_git_webhook_delivery;
DROP TABLE IF EXISTS git_webhook_deliveries;
DROP INDEX IF EXISTS idx_git_repository_webhooks_deleted_at;
DROP INDEX IF EXISTS idx_git_repository_webhooks_repository_uuid;
DROP INDEX IF EXISTS idx_git_repository_webhooks_uuid;
DROP TABLE IF EXISTS git_repository_webhooks;
