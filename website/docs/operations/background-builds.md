---
title: Background Dockerfile builds
sidebar_position: 1
---

# Dockerfile builds

The Files view can build a selected Dockerfile into the current Docker host. Builds are backend jobs: closing the dialog or changing page does not cancel them, and the global activity indicator can reopen progress.

## Build context

Dockman uses the directory containing the selected Dockerfile as build context. This fixes the common `/app/Dockerfile: no such file or directory` failure caused by running Docker CLI from Dockman's own application directory.

The result is loaded into the Docker host with `docker buildx build --load`.

## Builders

- When the daemon-backed `docker` driver is available and default networking is selected, Dockman uses it without a helper container.
- With a socket proxy exposing a `docker-container` driver, or when host networking is requested, Dockman creates a uniquely named job-scoped builder.
- The temporary builder is removed after success, failure or cancellation. An exact legacy `buildx_buildkit_default` helper from older builds is also cleaned up.

A `buildx_buildkit_dockman-*` container can therefore exist while a build is running, but must not remain afterwards.

## Host networking

Host mode grants BuildKit's `network.host` entitlement only to the temporary builder and current build. It does not globally reconfigure the Docker daemon.

Expected arguments include both:

```text
--buildkitd-flags "--allow-insecure-entitlement network.host"
--allow=network.host --network=host
```

Use host networking only when the build must reach a service bound specifically to the Docker host. It does not guarantee faster package mirrors.

## Socket-proxy requirements

Builds require Build API access and permission to create/remove the temporary BuildKit container. If cleanup is denied, the build log reports the exact cleanup failure without incorrectly marking an otherwise successful image build as failed.

## Slow package downloads

Long gaps during `apt`/`apk` downloads usually originate from DNS, mirror routing, IPv6 fallback, proxy MTU or the remote package server—not from Dockman's job tracking. Compare from the Docker host and a simple container on the same network before changing Buildx mode.

## Verification

```bash
docker ps -a --filter name=buildx_buildkit
docker image inspect IMAGE:TAG
```

After completion, the image must exist and no Dockman-scoped helper should remain.
