---
title: Protected image updates
sidebar_position: 1
---

# Image update system

The Updates view combines read-only discovery, opt-in automation, stack-aware execution, rollback and notifications. It does not require Watchtower or WUD.

## Update discovery

**Check updates** compares the local image manifest with the registry manifest without pulling the image. Public registries use their standard token challenge flow. Locally built images and references without a pullable registry tag are classified as local/skipped rather than errors.

Private-registry credentials are not implemented yet. A private image that requires authentication is reported as unsupported/error without affecting public or local images.

Starting a new check resets previous transient statuses. The Monitor status filter can show only containers with an available update.

## Manual updates

From Monitor or Updates, an operator can update one container, selected containers or a stack. Progress runs as a backend job and remains available while navigating between views.

Sensitive infrastructure—such as the socket proxy used by Dockman—uses a protected helper workflow so replacing it does not cut off the API operation mid-transaction.

## Automatic updates

Automation is opt-in by UI policy or Compose labels. Each scheduled cycle:

1. scans enrolled targets;
2. groups Compose services into stack transactions;
3. preloads pullable images;
4. recreates the intended target(s);
5. observes container state, health and restart behavior;
6. rolls back the complete transaction on failure when enabled;
7. records results and sends configured notifications;
8. performs safe old-image cleanup only after full success.

The global pause, maximum groups per run and persistent circuit breaker limit unintended mass changes. A manually triggered automation cycle uses the same protections.

## The next deployment after an update

Dockman replaces a container through the Docker API, not through Compose, so that it can check the new container's health and roll back. Compose records a fingerprint of the image on each container it creates and recreates any container whose fingerprint no longer matches its image. The replacement therefore carries the fingerprint Compose would record for the new image, and the next Deploy, Update or Git deployment of the stack leaves it running as it is.

Dockman does not assume which Compose deploys the stack: an SSH host runs its own, and the fingerprint has changed between Compose releases and depends on the image store. It reads how the fingerprint on the replaced container was computed and computes the new one the same way. When it cannot tell, it leaves the fingerprint as it was, and the next deployment recreates the container once more, without a health check, as every update did before:

- the image used to be a single-platform manifest and the new version is a multi-platform index, on the containerd image store;
- several platforms of the new image are present on the host;
- the previous image was deleted while its container still ran;
- the container was last updated by a Dockman version without this fix.

A Compose upgrade that changes the fingerprint (see [Bundled Docker Compose](../operations/compose.md)) recreates updated containers once, like every other container.

## Rollback scope

Rollback restores the previous image IDs and stack state recorded before execution. A digest that repeatedly fails is blocked until explicitly retried or superseded. A Dockman restart during execution marks the run interrupted and pauses automation for operator review.

## Scheduled checks

Schedules use standard five-field cron expressions in the configured timezone. The minimum interval is 15 minutes.

Examples:

| Schedule | Meaning |
|---|---|
| `0 4 * * *` | Daily at 04:00 |
| `0 8,20 * * *` | Every 12 hours starting at 08:00 |
| `0 3 * * 1` | Monday at 03:00 |

The scheduler runs only for enrolled targets. Loading the UI does not start additional scan loops.

## Current provider scope

Digest checks support public registry flows used by common Docker Hub and OCI images. Semantic version discovery currently supports unauthenticated registry catalog/tag APIs. Authenticated private registry support is intentionally deferred.
