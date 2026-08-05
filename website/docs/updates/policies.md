---
title: Policies, labels and cleanup
sidebar_position: 2
---

# Update policies

Policies can target a standalone container or a complete Compose stack. Bulk editing can apply the same configuration to multiple containers or stacks atomically.

## UI policy fields

- enabled/disabled enrollment;
- cron schedule;
- rollback protection;
- safe previous-image cleanup;
- number of previous images to retain (`0` to `10`);
- semantic version discovery (`off`, `patch`, `minor`, `major`);
- prerelease inclusion.

Policies supplied by labels are visible but not overwritten by the UI.

## Compose labels

```yaml
services:
  app:
    image: example/app:3.1.1
    labels:
      dockman.update: "true"
      dockman.update.schedule: "0 4 * * *"
      dockman.update.rollback: "true"
      dockman.update.cleanup: "true"
      dockman.update.cleanup.keep: "1"
      dockman.update.version: "minor"
      dockman.update.version.prerelease: "false"
```

| Label | Values | Purpose |
|---|---|---|
| `dockman.update` | boolean | Enroll target |
| `dockman.update.disable` | boolean | Explicitly disable/protect a target |
| `dockman.update.schedule` | five-field cron | Per-target schedule |
| `dockman.update.rollback` | boolean | Enable protected rollback |
| `dockman.update.cleanup` | boolean | Enable safe previous-image cleanup |
| `dockman.update.cleanup.keep` | `0`–`10` | Number of prior images retained |
| `dockman.update.version` | `off`, `patch`, `minor`, `major` | Informative higher-tag discovery |
| `dockman.update.version.prerelease` | boolean | Permit prerelease suggestions |

For a Compose project, stack-level execution keeps services in one transaction even if several members are selected.

## Version discovery

Version discovery reports that a higher semantic tag exists while leaving the Compose tag untouched. Digest freshness and higher-version availability are independent states. Results are cached and registry requests are shared for containers using the same repository.

Current behavior is informative: selecting `major` does not rewrite `image:` in a Compose file and does not pull the proposed tag automatically.

## Safe image cleanup

Cleanup starts only after a protected update transaction succeeds completely. Dockman records the exact prior image ID and never uses forced deletion.

An image is retained when it is:

- still referenced by a running or stopped container;
- tagged elsewhere;
- required by a descendant image;
- within the configured previous-image retention count;
- part of a failed or rolled-back transaction.

Retained candidates can be retried explicitly. The normal Images/Prune view calculates reclaimable space from genuinely unused images and does not count every used image as reclaimable.
