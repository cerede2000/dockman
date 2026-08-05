-- +goose Up
CREATE TABLE update_notification_channels_unified (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    host TEXT NOT NULL,
    name TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('smtp', 'webhook', 'gotify', 'ntfy', 'discord', 'apprise')),
    enabled NUMERIC NOT NULL DEFAULT 0,
    target TEXT NOT NULL DEFAULT '',
    topic TEXT NOT NULL DEFAULT '',
    priority INTEGER NOT NULL DEFAULT 0,
    tags TEXT NOT NULL DEFAULT '',
    allow_insecure_http NUMERIC NOT NULL DEFAULT 0,
    notify_updates NUMERIC NOT NULL DEFAULT 1,
    notify_errors NUMERIC NOT NULL DEFAULT 1,
    event_types TEXT NOT NULL DEFAULT '',
    secret_key TEXT NOT NULL,
    encrypted_config BLOB NOT NULL
);

INSERT INTO update_notification_channels_unified (
    id, created_at, updated_at, host, name, type, enabled, target, topic,
    priority, tags, allow_insecure_http, notify_updates, notify_errors,
    event_types, secret_key, encrypted_config
)
SELECT id, created_at, updated_at, host, name, type, enabled, target, topic,
       priority, tags, allow_insecure_http, notify_updates, notify_errors,
       CASE
           WHEN notify_updates = 1 AND notify_errors = 1 THEN 'updates.available' || char(10) || 'updates.success' || char(10) || 'updates.failure'
           WHEN notify_updates = 1 THEN 'updates.available' || char(10) || 'updates.success'
           WHEN notify_errors = 1 THEN 'updates.failure'
           ELSE ''
       END,
       secret_key, encrypted_config
FROM update_notification_channels;

DROP INDEX IF EXISTS idx_update_notification_channels_secret_key;
DROP INDEX IF EXISTS idx_update_notification_channel;
DROP TABLE update_notification_channels;
ALTER TABLE update_notification_channels_unified RENAME TO update_notification_channels;
CREATE UNIQUE INDEX idx_update_notification_channel ON update_notification_channels(host, name);
CREATE UNIQUE INDEX idx_update_notification_channels_secret_key ON update_notification_channels(secret_key);

-- +goose Down
CREATE TABLE update_notification_channels_legacy (
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
INSERT INTO update_notification_channels_legacy (
    id, created_at, updated_at, host, name, type, enabled, target, topic,
    priority, tags, allow_insecure_http, notify_updates, notify_errors,
    secret_key, encrypted_config
)
SELECT id, created_at, updated_at, host, name, type, enabled, target, topic,
       priority, tags, allow_insecure_http, notify_updates, notify_errors,
       secret_key, encrypted_config
FROM update_notification_channels WHERE type <> 'smtp';
DROP INDEX IF EXISTS idx_update_notification_channels_secret_key;
DROP INDEX IF EXISTS idx_update_notification_channel;
DROP TABLE update_notification_channels;
ALTER TABLE update_notification_channels_legacy RENAME TO update_notification_channels;
CREATE UNIQUE INDEX idx_update_notification_channel ON update_notification_channels(host, name);
CREATE UNIQUE INDEX idx_update_notification_channels_secret_key ON update_notification_channels(secret_key);
