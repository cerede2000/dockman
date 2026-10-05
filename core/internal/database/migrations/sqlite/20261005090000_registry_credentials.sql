-- +goose Up
-- Logins for private container registries. The secret lives in
-- encrypted_payload, sealed by the registry vault under a scope bound to
-- uuid; secret_hint holds the few characters that let an operator recognise
-- which token is stored, never enough to use it.
CREATE TABLE registry_credentials (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at DATETIME,
    updated_at DATETIME,
    deleted_at DATETIME,
    uuid TEXT NOT NULL,
    registry TEXT NOT NULL,
    username TEXT NOT NULL,
    encrypted_payload BLOB,
    secret_hint TEXT NOT NULL DEFAULT '',
    last_tested_at DATETIME,
    last_test_error TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_registry_credentials_uuid ON registry_credentials(uuid);
CREATE UNIQUE INDEX idx_registry_credentials_registry ON registry_credentials(registry);
CREATE INDEX idx_registry_credentials_deleted_at ON registry_credentials(deleted_at);

-- +goose Down
DROP TABLE registry_credentials;
