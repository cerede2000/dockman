-- +goose Up
ALTER TABLE update_execution_runs ADD COLUMN error TEXT NOT NULL DEFAULT '';

CREATE TABLE update_automation_controls (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    updated_at DATETIME NOT NULL,
    host TEXT NOT NULL,
    paused NUMERIC NOT NULL DEFAULT 0,
    max_groups_per_run INTEGER NOT NULL DEFAULT 0 CHECK (max_groups_per_run BETWEEN 0 AND 1000)
);
CREATE UNIQUE INDEX idx_update_automation_controls_host ON update_automation_controls(host);

-- +goose Down
DROP INDEX IF EXISTS idx_update_automation_controls_host;
DROP TABLE IF EXISTS update_automation_controls;
ALTER TABLE update_execution_runs DROP COLUMN error;
