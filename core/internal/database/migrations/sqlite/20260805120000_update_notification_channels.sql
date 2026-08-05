-- +goose Up
CREATE TABLE update_notification_channels (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    host TEXT NOT NULL,
    name TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('webhook', 'gotify', 'ntfy', 'discord', 'apprise')),
    enabled NUMERIC NOT NULL DEFAULT 0,
    target TEXT NOT NULL DEFAULT '',
    topic TEXT NOT NULL DEFAULT '',
    priority INTEGER NOT NULL DEFAULT 0,
    tags TEXT NOT NULL DEFAULT '',
    allow_insecure_http NUMERIC NOT NULL DEFAULT 0,
    notify_updates NUMERIC NOT NULL DEFAULT 1,
    notify_errors NUMERIC NOT NULL DEFAULT 1,
    secret_key TEXT NOT NULL,
    encrypted_config BLOB NOT NULL
);
CREATE UNIQUE INDEX idx_update_notification_channel ON update_notification_channels(host, name);
CREATE UNIQUE INDEX idx_update_notification_channels_secret_key ON update_notification_channels(secret_key);

CREATE TABLE update_notification_channel_states (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    updated_at DATETIME NOT NULL,
    host TEXT NOT NULL,
    schedule TEXT NOT NULL,
    channel_key TEXT NOT NULL,
    last_available_fingerprint TEXT NOT NULL DEFAULT '',
    last_error_fingerprint TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_update_notification_channel_state ON update_notification_channel_states(host, schedule, channel_key);

INSERT INTO update_notification_channel_states (
    updated_at, host, schedule, channel_key, last_available_fingerprint, last_error_fingerprint
)
SELECT updated_at, host, schedule, 'smtp', last_available_fingerprint, last_error_fingerprint
FROM update_notification_states;

ALTER TABLE update_notification_deliveries ADD COLUMN channel_type TEXT NOT NULL DEFAULT 'smtp';
ALTER TABLE update_notification_deliveries ADD COLUMN channel_name TEXT NOT NULL DEFAULT 'SMTP';

-- +goose Down
ALTER TABLE update_notification_deliveries DROP COLUMN channel_name;
ALTER TABLE update_notification_deliveries DROP COLUMN channel_type;
DROP INDEX IF EXISTS idx_update_notification_channel_state;
DROP TABLE IF EXISTS update_notification_channel_states;
DROP INDEX IF EXISTS idx_update_notification_channels_secret_key;
DROP INDEX IF EXISTS idx_update_notification_channel;
DROP TABLE IF EXISTS update_notification_channels;
