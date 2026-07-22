-- +goose Up
CREATE TABLE `git_binding_baselines` (
    `id` integer PRIMARY KEY AUTOINCREMENT,
    `created_at` datetime,
    `updated_at` datetime,
    `binding_uuid` text NOT NULL,
    `path` text NOT NULL,
    `sha256` text NOT NULL
);
CREATE UNIQUE INDEX `idx_git_binding_baseline_path` ON `git_binding_baselines` (`binding_uuid`, `path`);

-- +goose Down
DROP TABLE IF EXISTS `git_binding_baselines`;
