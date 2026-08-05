---
title: Implementation status and roadmap
sidebar_position: 4
---

# Update-system status

The implementation test plans in this repository use the following delivered sequence:

| Delivered lot | Status | Scope |
|---:|:---:|---|
| 1 | ✅ | Update inventory/view foundation and read-only checks |
| 2 | ✅ | Opt-in policies, enrollment and scheduling |
| 3 | ✅ | SMTP configuration, encrypted password, tests and event notifications |
| 4 | ✅ | Protected automatic execution, health observation, rollback and circuit breaker |
| 5 | ✅ | Complete Compose-stack transactions and protected infrastructure updates |
| 6 | ✅ | Persistent pause/resume, manual cycle, execution limits and interrupted-run recovery |
| 7 | ✅ | Safe previous-image cleanup with configurable retention and Prune integration |
| 8 | ✅ | Informative semantic-version discovery and bulk policy editing |
| 9 | ✅ | Isolated webhook, Gotify, ntfy, Discord and Apprise notification channels |

Post-lot stabilization also delivered background operations, self-update checking, public-registry challenge support, local-image classification, Buildx lifecycle cleanup, UI progress, update-reference/stat fixes, unified multi-channel event subscriptions and signed GitHub synchronization webhooks.

## Remaining roadmap

### Private registries

Private-registry credentials remain deliberately deferred. The implementation must provide host/repository-scoped encrypted credentials, challenge-safe token handling, secret redaction, rotation and compatibility with both digest checks and semantic tag discovery.

### Advanced strategies

Maintenance windows beyond per-target cron, canary batches, dependency/priority groups, concurrency rules and enforceable semantic version constraints remain future work. Current version discovery is informative and never rewrites Compose tags.

### Additional Git providers

Signed GitHub push webhooks are delivered. GitLab and Bitbucket require explicit provider adapters rather than pretending their signatures and payloads are GitHub-compatible; see [GitHub webhooks](../git-sync/webhooks.md).

### Documentation/release

This documentation refresh covers the integration channel. A formal stable release still needs version selection, migration/recovery rehearsal, release notes, compatibility statement and promotion of an immutable signed digest.
