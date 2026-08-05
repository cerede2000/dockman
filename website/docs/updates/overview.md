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
