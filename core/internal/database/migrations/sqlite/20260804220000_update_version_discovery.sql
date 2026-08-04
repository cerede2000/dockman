-- +goose Up
ALTER TABLE update_policies ADD COLUMN version_policy TEXT NOT NULL DEFAULT 'off' CHECK (version_policy IN ('off', 'patch', 'minor', 'major'));
ALTER TABLE update_policies ADD COLUMN version_prerelease NUMERIC NOT NULL DEFAULT 0;

ALTER TABLE update_scan_results ADD COLUMN current_tag TEXT NOT NULL DEFAULT '';
ALTER TABLE update_scan_results ADD COLUMN latest_tag TEXT NOT NULL DEFAULT '';
ALTER TABLE update_scan_results ADD COLUMN version_policy TEXT NOT NULL DEFAULT '';
ALTER TABLE update_scan_results ADD COLUMN version_available NUMERIC NOT NULL DEFAULT 0;
ALTER TABLE update_scan_results ADD COLUMN version_reason TEXT NOT NULL DEFAULT '';

ALTER TABLE update_scan_runs ADD COLUMN versions INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE update_scan_runs DROP COLUMN versions;
ALTER TABLE update_scan_results DROP COLUMN version_reason;
ALTER TABLE update_scan_results DROP COLUMN version_available;
ALTER TABLE update_scan_results DROP COLUMN version_policy;
ALTER TABLE update_scan_results DROP COLUMN latest_tag;
ALTER TABLE update_scan_results DROP COLUMN current_tag;
ALTER TABLE update_policies DROP COLUMN version_prerelease;
ALTER TABLE update_policies DROP COLUMN version_policy;
