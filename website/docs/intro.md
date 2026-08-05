---
sidebar_position: 0
title: Overview
---

# Dockman integration fork

Dockman keeps Docker Compose files visible and editable while adding the operational tools needed to run them safely.

The integration fork combines:

- a file-first Compose editor;
- stack and container monitoring;
- complete container and volume inspection;
- Git-backed stack synchronization;
- protected image updates and rollback;
- a hardened, multi-architecture container image.

## Design principles

1. **Files remain the source of truth.** Dockman does not hide Compose configuration in a proprietary database.
2. **Destructive changes are explicit.** Git deletions, orphan cleanup and resets require a short `CONFIRM` acknowledgement.
3. **Automation is opt-in.** Git deployment and container updates are enabled per stack or container.
4. **Failures are isolated.** One invalid stack must not prevent independent stacks from being synchronized.
5. **No permanent background churn.** Schedulers wake only for configured work and histories are bounded.
6. **Rollback material remains available.** Backups and previous images are retained according to explicit policies.

## Start here

1. [Install Dockman](install/docker.mdx).
2. Review the [security model](security.md) and [socket-proxy permissions](docker-socket/index.md).
3. Enable [Git synchronization](git-sync/overview.md) only after mounting its encryption key.
4. Configure [protected image updates](updates/overview.md) and notifications.
5. Read [migration](operations/migration.md) before promoting a new image.

This documentation describes `ghcr.io/cerede2000/dockman:integration`. The original project documentation remains available from the upstream repository.
