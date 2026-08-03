-- +goose Up
CREATE TABLE update_smtp_configs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    host TEXT NOT NULL,
    enabled NUMERIC NOT NULL DEFAULT 0,
    server TEXT NOT NULL,
    port INTEGER NOT NULL,
    security TEXT NOT NULL CHECK (security IN ('starttls', 'tls', 'none')),
    username TEXT NOT NULL DEFAULT '',
    encrypted_password BLOB,
    from_address TEXT NOT NULL,
    recipients TEXT NOT NULL,
    notify_updates NUMERIC NOT NULL DEFAULT 1,
    notify_errors NUMERIC NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX idx_update_smtp_configs_host ON update_smtp_configs(host);

CREATE TABLE update_notification_states (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    updated_at DATETIME NOT NULL,
    host TEXT NOT NULL,
    schedule TEXT NOT NULL,
    last_available_fingerprint TEXT NOT NULL DEFAULT '',
    last_error_fingerprint TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_update_notification_state ON update_notification_states(host, schedule);

CREATE TABLE update_notification_deliveries (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at DATETIME NOT NULL,
    host TEXT NOT NULL,
    kind TEXT NOT NULL,
    subject TEXT NOT NULL,
    success NUMERIC NOT NULL,
    error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_update_notification_deliveries_host ON update_notification_deliveries(host);

-- +goose Down
DROP INDEX IF EXISTS idx_update_notification_deliveries_host;
DROP TABLE IF EXISTS update_notification_deliveries;
DROP INDEX IF EXISTS idx_update_notification_state;
DROP TABLE IF EXISTS update_notification_states;
DROP INDEX IF EXISTS idx_update_smtp_configs_host;
DROP TABLE IF EXISTS update_smtp_configs;
