-- +goose Up
ALTER TABLE `git_operations` ADD COLUMN `compose_path` text;
ALTER TABLE `git_operations` ADD COLUMN `trigger` text NOT NULL DEFAULT 'system';
ALTER TABLE `git_operations` ADD COLUMN `details` text;
ALTER TABLE `git_operations` ADD COLUMN `commit_sha` text;
ALTER TABLE `git_operations` ADD COLUMN `backup_uuid` text;
CREATE INDEX `idx_git_operations_binding_uuid` ON `git_operations` (`binding_uuid`);
CREATE INDEX `idx_git_operations_created_at` ON `git_operations` (`created_at`);
CREATE TABLE `git_backups` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `created_at` datetime,
  `updated_at` datetime,
  `uuid` text NOT NULL,
  `repository_uuid` text NOT NULL,
  `binding_uuid` text NOT NULL,
  `kind` text NOT NULL,
  `compose_paths` text,
  `archive_path` text NOT NULL,
  `commit_sha` text,
  `file_count` integer NOT NULL DEFAULT 0,
  `size_bytes` integer NOT NULL DEFAULT 0,
  `restorable` numeric NOT NULL DEFAULT 0,
  `expires_at` datetime
);
CREATE UNIQUE INDEX `idx_git_backups_uuid` ON `git_backups` (`uuid`);
CREATE UNIQUE INDEX `idx_git_backups_archive_path` ON `git_backups` (`archive_path`);
CREATE INDEX `idx_git_backups_repository_uuid` ON `git_backups` (`repository_uuid`);
CREATE INDEX `idx_git_backups_binding_uuid` ON `git_backups` (`binding_uuid`);
CREATE INDEX `idx_git_backups_expires_at` ON `git_backups` (`expires_at`);

-- +goose Down
DROP TABLE IF EXISTS `git_backups`;
DROP INDEX IF EXISTS `idx_git_operations_created_at`;
DROP INDEX IF EXISTS `idx_git_operations_binding_uuid`;
ALTER TABLE `git_operations` DROP COLUMN `backup_uuid`;
ALTER TABLE `git_operations` DROP COLUMN `commit_sha`;
ALTER TABLE `git_operations` DROP COLUMN `details`;
ALTER TABLE `git_operations` DROP COLUMN `trigger`;
ALTER TABLE `git_operations` DROP COLUMN `compose_path`;
