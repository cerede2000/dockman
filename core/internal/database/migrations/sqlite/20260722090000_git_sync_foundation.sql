-- +goose Up
CREATE TABLE `git_credentials` (
    `id` integer PRIMARY KEY AUTOINCREMENT,
    `created_at` datetime,
    `updated_at` datetime,
    `deleted_at` datetime,
    `uuid` text NOT NULL,
    `name` text NOT NULL,
    `auth_type` text NOT NULL,
    `username` text,
    `encrypted_payload` blob,
    `secret_hint` text
);
CREATE UNIQUE INDEX `idx_git_credentials_uuid` ON `git_credentials` (`uuid`);
CREATE UNIQUE INDEX `idx_git_credentials_name` ON `git_credentials` (`name`);
CREATE INDEX `idx_git_credentials_deleted_at` ON `git_credentials` (`deleted_at`);

CREATE TABLE `git_repositories` (
    `id` integer PRIMARY KEY AUTOINCREMENT,
    `created_at` datetime,
    `updated_at` datetime,
    `deleted_at` datetime,
    `uuid` text NOT NULL,
    `name` text NOT NULL,
    `provider` text NOT NULL DEFAULT 'generic',
    `remote_url` text NOT NULL,
    `default_branch` text NOT NULL DEFAULT 'main',
    `mode` text NOT NULL DEFAULT 'managed',
    `credential_uuid` text,
    `status` text NOT NULL DEFAULT 'uninitialized',
    `last_error` text,
    `last_fetched_at` datetime
);
CREATE UNIQUE INDEX `idx_git_repositories_uuid` ON `git_repositories` (`uuid`);
CREATE UNIQUE INDEX `idx_git_repositories_name` ON `git_repositories` (`name`);
CREATE INDEX `idx_git_repositories_deleted_at` ON `git_repositories` (`deleted_at`);

CREATE TABLE `git_stack_bindings` (
    `id` integer PRIMARY KEY AUTOINCREMENT,
    `created_at` datetime,
    `updated_at` datetime,
    `deleted_at` datetime,
    `uuid` text NOT NULL,
    `repository_uuid` text NOT NULL,
    `host` text NOT NULL,
    `stack_path` text NOT NULL,
    `sub_path` text NOT NULL,
    `compose_paths` text,
    `enabled` numeric NOT NULL DEFAULT true
);
CREATE UNIQUE INDEX `idx_git_stack_bindings_uuid` ON `git_stack_bindings` (`uuid`);
CREATE UNIQUE INDEX `idx_git_stack_binding_target` ON `git_stack_bindings` (`host`, `stack_path`);
CREATE INDEX `idx_git_stack_bindings_deleted_at` ON `git_stack_bindings` (`deleted_at`);

CREATE TABLE `git_operations` (
    `id` integer PRIMARY KEY AUTOINCREMENT,
    `created_at` datetime,
    `updated_at` datetime,
    `uuid` text NOT NULL,
    `repository_uuid` text,
    `binding_uuid` text,
    `operation_type` text NOT NULL,
    `state` text NOT NULL,
    `started_at` datetime,
    `finished_at` datetime,
    `error_message` text
);
CREATE UNIQUE INDEX `idx_git_operations_uuid` ON `git_operations` (`uuid`);
CREATE INDEX `idx_git_operations_repository_uuid` ON `git_operations` (`repository_uuid`);
CREATE INDEX `idx_git_operations_state` ON `git_operations` (`state`);

CREATE TABLE `git_deployments` (
    `id` integer PRIMARY KEY AUTOINCREMENT,
    `created_at` datetime,
    `updated_at` datetime,
    `uuid` text NOT NULL,
    `repository_uuid` text NOT NULL,
    `binding_uuid` text NOT NULL,
    `commit_sha` text NOT NULL,
    `compose_hash` text,
    `state` text NOT NULL,
    `result` text,
    `logs` text
);
CREATE UNIQUE INDEX `idx_git_deployments_uuid` ON `git_deployments` (`uuid`);
CREATE INDEX `idx_git_deployments_binding_uuid` ON `git_deployments` (`binding_uuid`);

-- +goose Down
DROP TABLE IF EXISTS `git_deployments`;
DROP TABLE IF EXISTS `git_operations`;
DROP TABLE IF EXISTS `git_stack_bindings`;
DROP TABLE IF EXISTS `git_repositories`;
DROP TABLE IF EXISTS `git_credentials`;
