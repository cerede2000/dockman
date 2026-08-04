-- +goose Up
CREATE TABLE update_execution_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at DATETIME,
    started_at DATETIME NOT NULL,
    completed_at DATETIME,
    host TEXT NOT NULL,
    schedule TEXT NOT NULL,
    scan_run_id INTEGER NOT NULL,
    targets INTEGER NOT NULL,
    updated INTEGER NOT NULL,
    current INTEGER NOT NULL,
    rolled_back INTEGER NOT NULL,
    failed INTEGER NOT NULL,
    skipped INTEGER NOT NULL
);
CREATE INDEX idx_update_execution_runs_host ON update_execution_runs(host);
CREATE INDEX idx_update_execution_runs_scan_run_id ON update_execution_runs(scan_run_id);

CREATE TABLE update_execution_results (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at DATETIME,
    run_id INTEGER NOT NULL,
    host TEXT NOT NULL,
    container_id TEXT NOT NULL,
    container_name TEXT NOT NULL,
    image TEXT NOT NULL,
    remote_digest TEXT NOT NULL,
    rollback_enabled NUMERIC NOT NULL,
    state TEXT NOT NULL,
    message TEXT NOT NULL DEFAULT '',
    logs TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_update_execution_results_run_id ON update_execution_results(run_id);
CREATE INDEX idx_update_execution_results_host ON update_execution_results(host);
CREATE INDEX idx_update_execution_results_container_id ON update_execution_results(container_id);

CREATE TABLE update_execution_blocks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at DATETIME,
    updated_at DATETIME,
    host TEXT NOT NULL,
    container_id TEXT NOT NULL,
    container_name TEXT NOT NULL,
    image TEXT NOT NULL,
    remote_digest TEXT NOT NULL,
    reason TEXT NOT NULL
);
CREATE UNIQUE INDEX idx_update_execution_block ON update_execution_blocks(host, container_id);

-- +goose Down
DROP TABLE IF EXISTS update_execution_blocks;
DROP TABLE IF EXISTS update_execution_results;
DROP TABLE IF EXISTS update_execution_runs;
