-- +goose Up
ALTER TABLE `git_stack_statuses` ADD COLUMN `pause_reason` text NOT NULL DEFAULT '';
UPDATE `git_stack_statuses`
SET `pause_reason` = CASE
  WHEN `error_message` LIKE 'Local rollback waiting%' OR `error_message` LIKE 'Backup restored locally%' THEN 'recovery'
  ELSE 'manual'
END
WHERE `automation_paused` = 1;

-- +goose Down
ALTER TABLE `git_stack_statuses` DROP COLUMN `pause_reason`;
