---
title: Build resource limits
sidebar_position: 2
---

# Build resource limits

An image build can take every core of the machine it runs on and slow down everything else the host serves. Each host can cap the builds Dockman runs on it, in **Settings → Docker Hosts → (host) → Build limits**:

- **CPU cores**: fractional values are accepted, e.g. `1.5`. At least `0.1`.
- **Memory (GiB)**: at least `0.25`.

Leave a field empty for no limit, which is the default. The limits apply to:

- Compose stacks with a `build:` section, from the Deploy tab (Up, Redeploy, Update) and from Git deployments, rollbacks included;
- Dockerfile builds from the Files view, host networking included.

The "run a docker command" tool is not capped: a command typed there runs as written.

## How it works

Options such as `docker build --cpu-quota` belonged to the legacy builder. BuildKit ignores them, and Compose passes no such limit to the build it delegates to Buildx. Dockman therefore puts the limit on the builder itself.

When a host has limits, each build runs in a dedicated `docker-container` Buildx builder named `dockman-limited`, created with `cpu-quota` / `memory` options just before the build, and removed right after it:

- no BuildKit container keeps running between builds;
- the build cache survives, in the `buildx_buildkit_dockman-limited0_state` volume;
- the limits in force are always the current settings, with no reconnect or restart;
- builds on the host run one at a time. Two builds at once would share the capped resources, which defeats the purpose.

The trade-offs, only while limits are set:

- this builder has its own cache, separate from the daemon's: the first build after enabling limits rebuilds everything;
- the image is copied into the Docker daemon at the end of each build, which adds a few seconds;
- creating the builder adds a few seconds to each build.

Removing the limits returns every build to the daemon's builder, as before. The cache volume can then be deleted with `docker volume rm buildx_buildkit_dockman-limited0_state`.

## Behind a socket proxy

The limited builder runs BuildKit in a container that Dockman creates, and Buildx copies its configuration into that container. Behind [LinuxServer's socket-proxy](../docker-socket/index.md), this needs `ALLOW_ARCHIVE=1` in addition to `CONTAINERS`, `IMAGES`, `POST`, `EXEC` and `VOLUMES`. It does not need `GRPC`. Without `ALLOW_ARCHIVE`, the build fails with "403 Forbidden", and Dockman's error names the setting.

`ALLOW_ARCHIVE` lets Dockman read and write files in containers. That is no new authority for a proxy that already allows container creation and `EXEC`, which Dockman needs anyway.
