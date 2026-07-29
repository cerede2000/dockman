-- +goose Up
ALTER TABLE `git_repositories` ADD COLUMN `commit_author_name` text NOT NULL DEFAULT 'Dockman Git Sync';
ALTER TABLE `git_repositories` ADD COLUMN `commit_author_email` text NOT NULL DEFAULT 'dockman@localhost.invalid';

-- +goose Down
ALTER TABLE `git_repositories` DROP COLUMN `commit_author_email`;
ALTER TABLE `git_repositories` DROP COLUMN `commit_author_name`;
