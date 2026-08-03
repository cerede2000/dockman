-- +goose Up
CREATE TABLE update_scan_results (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    host TEXT NOT NULL,
    container_id TEXT NOT NULL,
    container_name TEXT NOT NULL,
    image TEXT NOT NULL,
    status TEXT NOT NULL,
    current_digest TEXT NOT NULL DEFAULT '',
    remote_digest TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    checked_at DATETIME NOT NULL
);
CREATE UNIQUE INDEX idx_update_scan_result ON update_scan_results(host, container_id);

CREATE TABLE update_scan_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at DATETIME NOT NULL,
    started_at DATETIME NOT NULL,
    completed_at DATETIME,
    host TEXT NOT NULL,
    trigger TEXT NOT NULL,
    schedule TEXT NOT NULL DEFAULT '',
    targets INTEGER NOT NULL DEFAULT 0,
    available INTEGER NOT NULL DEFAULT 0,
    current INTEGER NOT NULL DEFAULT 0,
    skipped INTEGER NOT NULL DEFAULT 0,
    errors INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_update_scan_runs_host ON update_scan_runs(host);

-- +goose Down
DROP INDEX IF EXISTS idx_update_scan_runs_host;
DROP TABLE IF EXISTS update_scan_runs;
DROP INDEX IF EXISTS idx_update_scan_result;
DROP TABLE IF EXISTS update_scan_results;
