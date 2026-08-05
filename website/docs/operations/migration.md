---
title: Migration and release readiness
sidebar_position: 3
---

# Upgrade and migration procedure

Use this procedure when moving from upstream Dockman or an older integration image.

## Before upgrading

1. Record the current image reference and digest.
2. Back up `/config`, the stack root and any dedicated Git storage path.
3. Back up Git and notification master-key secrets separately.
4. Export the current Compose definition and environment variables.
5. Check available disk space for a second image and rollback material.
6. Pause Git auto-deploy and automatic image updates for the maintenance window.

SQLite schema migrations run automatically at startup. A database backup is therefore required before moving forward; reverting only the image may not revert a migrated database safely.

## First integration deployment

1. Deploy `ghcr.io/cerede2000/dockman:integration` without deleting the previous image.
2. Confirm health and authentication.
3. Check Files, Monitor, container details and one harmless stack action.
4. Verify socket-proxy reads and writes.
5. Verify Git repositories/folder links before resuming automation.
6. Send an SMTP test and run a read-only image scan.
7. Resume automation gradually.

## Configuration additions

Review at minimum:

- HTTP origin and request limits;
- `DOCKMAN_ALLOW_SELF_EXEC=false`;
- Git/notification external master keys;
- optional dedicated Git storage;
- SMTP CA path;
- `DOCKER_CONFIG` writable location for read-only rootfs;
- capabilities required by provisioning;
- update schedules, rollback and cleanup retention.

## Rollback

If startup or basic navigation fails:

1. stop the new container without deleting volumes;
2. preserve logs and a copy of the migrated database;
3. restore the pre-upgrade `/config` backup if the old binary requires it;
4. redeploy the exact previous image digest;
5. keep Git and notification key files unchanged.

Do not run prune until the validation period ends.

## Release checklist

- backend build, vet and owned tests pass;
- frontend dependency audit, lint and production build pass;
- reachable Go vulnerability gate passes;
- amd64/arm64 images build and scan successfully;
- multi-architecture manifest is signed;
- integration digest is recorded;
- environment reference matches configuration tags and direct runtime lookups;
- migration and rollback have been exercised on a copy of production data;
- release notes list schema, security and behavior changes.

The moving `integration` tag is a test channel. A formal stable Dockman Git Edition release and its versioning/migration promise require a separate release decision.
