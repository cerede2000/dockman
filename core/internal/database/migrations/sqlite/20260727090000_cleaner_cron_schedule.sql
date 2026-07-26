-- +goose Up
ALTER TABLE prune_configs ADD COLUMN cron_expression text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE prune_configs DROP COLUMN cron_expression;
