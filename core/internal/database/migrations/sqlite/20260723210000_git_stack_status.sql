-- +goose Up
CREATE TABLE `git_stack_statuses` (
  `id` integer PRIMARY KEY AUTOINCREMENT,
  `created_at` datetime,
  `updated_at` datetime,
  `binding_uuid` text NOT NULL,
  `compose_path` text NOT NULL,
  `state` text NOT NULL DEFAULT 'pending',
  `error_message` text,
  `conflict_count` integer NOT NULL DEFAULT 0,
  `automation_paused` numeric NOT NULL DEFAULT 0,
  `last_checked_at` datetime,
  `last_success_at` datetime,
  `last_commit` text,
  `deploy_state` text NOT NULL DEFAULT 'disabled',
  `deploy_error` text,
  `last_deploy_at` datetime
);
CREATE UNIQUE INDEX `idx_git_stack_status_target` ON `git_stack_statuses` (`binding_uuid`, `compose_path`);
CREATE INDEX `idx_git_stack_statuses_state` ON `git_stack_statuses` (`state`);

-- +goose Down
DROP TABLE IF EXISTS `git_stack_statuses`;
