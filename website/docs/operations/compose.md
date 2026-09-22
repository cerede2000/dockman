---
title: Bundled Docker Compose
sidebar_position: 4
---

# Bundled Docker Compose

The image ships its own `docker-compose` binary, rebuilt from the Compose release source with the image's Go toolchain and security floors on some dependencies. Dockman runs it for every Compose action on the **local** host: deploy, update, redeploy, start, stop, restart, Git deployments and the model reads behind the selective update.

Hosts reached over **SSH** use the Compose installed on that host, not this one. Dockman's self-update helper and the host wrapper of the SOPS secrets also run their own Compose.

The bundled version is recorded in the image label `dev.dockman.build.compose`:

```bash
docker inspect -f '{{index .Config.Labels "dev.dockman.build.compose"}}' ghcr.io/cerede2000/dockman:integration
```

## Compose 5.5.1 (from 5.3.1)

### Containers recreated once

Nothing happens when the Dockman image is updated: running containers are not touched, and a stack nobody deploys keeps running as it is.

The **first deployment of a stack** afterwards may recreate its containers once, with the same image and configuration. It is the same as a redeploy with forced recreation: a few seconds of interruption for that stack's services, volumes kept, and anything written inside a container outside a volume lost.

What triggers that deployment: Deploy, an Update or Redeploy that goes through Compose, and Git deployments. Start, Stop, Restart and the Monitor's image updates do not.

Which stacks:

| Docker host | Recreated once |
|---|---|
| containerd image store (the default of fresh Docker 29 installs) | every stack |
| classic image store | only stacks with a `build:` section |

Why: Compose records a fingerprint of each container's image. From 5.4 it computes it differently on the containerd image store, so the first `up` sees a mismatch and recreates. Images built by Compose now also carry its version, so a rebuilt image differs from the one built by the previous version. Once recreated, the containers carry the new fingerprint and are left alone.

To tell which image store a host uses:

```bash
docker info --format '{{json .DriverStatus}}'
```

`io.containerd.snapshotter` in the output means the containerd image store.

To choose the moment, deploy the stacks during a maintenance window rather than letting the next Git deployment or update do it.

### Stricter `pull_policy`

A `pull_policy` value Compose does not know (a typo such as `alway`) used to be ignored silently. It is now rejected, and the stack does not load until it is fixed. Valid values: `always`, `never`, `missing`, `build`, `if_not_present`, `daily`, `weekly`, `every_<duration>` and `refresh`.

`refresh` on its own now pulls on every deployment.

### Fixed

- The selective update (Update in the editor's Deploy tab) now recognises services with an `env_file` as unchanged. Compose 5.3.1 computed their configuration hash without the file, so they always looked modified and went back to Compose instead of the verified, health-checked replacement.
- On the containerd image store, a stack with a `build:` section is no longer recreated by every deployment when nothing changed.

## How an upgrade of the bundled Compose is checked

Each image build runs a contract suite (`core/internal/docker/compose/contract_test.go`) inside the image it just built, on both the classic and the containerd image store. It drives Dockman's own Compose calls against the real binary and a real daemon. It also deploys stacks with the Compose of the currently published image and redeploys them with the new one, and records in the run summary how many containers that recreates. The multi-architecture tag is published only when the suite passes.
