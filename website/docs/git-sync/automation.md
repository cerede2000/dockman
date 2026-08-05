---
title: Automation, deployment and recovery
sidebar_position: 3
---

# Git automation

Automatic Git synchronization is opt-in per folder link and per stack. The scheduler uses the configured interval and performs an immediate full check when automation is resumed.

## Safe execution order

For Git-to-Dockman automatic changes:

1. fetch repository state;
2. build the policy-bounded inventory;
3. detect conflicts and unsaved editors;
4. create a verified backup;
5. transfer allowed files;
6. run declarative provisioning, if present;
7. validate Compose and perform a dry-run;
8. deploy selected stacks independently;
9. observe health/restart behavior;
10. roll back files and redeploy the previous state on failure;
11. update baselines, status and activity history.

An invalid stack does not prevent an independent new or changed stack from being processed.

## Automatic deployment

Deployment is a separate opt-in from synchronization. Newly discovered Git stacks can be authorized and deployed automatically, with a per-run discovery limit. Existing stack targets can be selected in bulk.

Dockman uses the same Compose execution path and `.env` loading hierarchy as manual stack operations.

## Provisioning

A selected stack can contain `provision.yml` or `provision.yaml`. Provisioning is declarative and does not execute arbitrary shell scripts. It runs after files arrive and before Compose validation/deployment.

Supported operations cover bounded directory/file creation, copy, chmod, chown and protected removal. Removal requires a backup. All paths remain confined to the stack folder, and provisioning changes participate in rollback.

Changing ownership requires Dockman to run with sufficient filesystem authority—typically `PUID=0` plus the explicit `CHOWN` capability. Ordinary create/chmod operations need write access and may require `DAC_OVERRIDE` depending on host modes.

## Backups and history

Backups include metadata describing binding, stack, operation and source state. They can be inspected, restored or deleted from Dockman. Retention is controlled by `DOCKMAN_GIT_BACKUP_RETENTION_DAYS`; activity retention uses `DOCKMAN_GIT_HISTORY_RETENTION_DAYS`.

Removing a folder link or repository also removes its associated backup/history state according to the deletion workflow. Master keys and stack files are never silently removed.

## Pause, resume and recovery

- Pause stops automatic work but keeps manual previews and checks available.
- Resume performs an immediate normal check.
- Restore/rollback may pause automation to prevent the remote state from immediately overwriting the recovered state.
- A push or resume action can complete the paired transition when the state is safe, avoiding two redundant operator steps.

When an automatic deployment fails and rollback succeeds, the status records the failure until a later full check confirms that the corrected content is deployed successfully. Saving settings alone must never erase an active failure state.
