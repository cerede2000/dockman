-- +goose Up
-- Caps on the image builds Dockman runs on each host: CPU cores (fractional)
-- and memory in bytes. 0 keeps builds unlimited, which is every existing host.
ALTER TABLE host_config ADD COLUMN build_cpu_limit REAL NOT NULL DEFAULT 0;
ALTER TABLE host_config ADD COLUMN build_memory_limit INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE host_config DROP COLUMN build_memory_limit;
ALTER TABLE host_config DROP COLUMN build_cpu_limit;
