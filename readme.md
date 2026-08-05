<div align="center">
  <img src="website/static/img/dockman.svg" alt="Dockman logo" width="180" height="180">
  <h1>Dockman — integration fork</h1>
  <p>Compose editor, container monitor, Git synchronization and protected image updates in one lightweight Docker UI.</p>
</div>

> This repository is the active integration fork maintained at
> [cerede2000/dockman](https://github.com/cerede2000/dockman). It is based on
> [RA341/dockman](https://github.com/RA341/dockman) and remains licensed under AGPL-3.0.

## What is included

- Compose and configuration-file editor with validation, quick YAML navigation and stack actions.
- Container and stack monitor, detailed inspect view, logs, processes, networks, mounts, security and terminal access.
- Container and volume file browsers with upload, download, permissions and read-only detection.
- Multi-host support through local Docker or SSH.
- GitHub repository synchronization for complete stack folders, with policies, previews, conflicts, backups, provisioning and protected automatic deployment.
- Image update discovery, scheduled opt-in policies, stack transactions, health validation, rollback, safe image cleanup and SMTP/Gotify/ntfy/Discord/Apprise/webhook notifications.
- Background Dockerfile builds using Buildx, persistent progress and automatic helper cleanup.
- Hardened container image, origin checks, bounded HTTP requests, encrypted credentials and Docker socket-proxy support.

## Quick start

The `integration` image is the test channel for this fork. Pin its immutable digest for a reproducible deployment when promoting it beyond testing.

```yaml
services:
  dockman:
    image: ghcr.io/cerede2000/dockman:integration
    container_name: dockman
    environment:
      DOCKMAN_COMPOSE_ROOT: /server/stacks
      DOCKMAN_CONFIG: /config
      DOCKER_HOST: tcp://socketproxy:2375
      DOCKMAN_AUTH_ENABLE: "true"
      DOCKMAN_AUTH_USERNAME: admin
      DOCKMAN_AUTH_PASSWORD: change-me
    volumes:
      - /server/stacks:/server/stacks
      - /server/appdata/dockman:/config
    ports:
      - "8866:8866"
    restart: unless-stopped
```

The stack path must be absolute and identical for `DOCKMAN_COMPOSE_ROOT`, the host mount and the container mount. Dockman must be able to write the stack directories for editing, Git import and provisioning.

For production-like installations, use a Docker socket proxy, enable authentication, keep `DOCKMAN_ALLOW_SELF_EXEC` disabled, mount the credential encryption keys as secrets and back up `/config`.

## Documentation

- [Container installation](website/docs/install/docker.mdx)
- [Environment-variable reference](website/docs/install/env.md)
- [Security model](website/docs/security.md)
- [Docker socket proxy](website/docs/docker-socket/index.md)
- [Git synchronization](website/docs/git-sync/overview.md)
- [Image updates and rollback](website/docs/updates/overview.md)
- [Buildx and background jobs](website/docs/operations/background-builds.md)
- [Migration and release readiness](website/docs/operations/migration.md)
- [Troubleshooting](website/docs/operations/troubleshooting.md)

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md). The integration pipeline builds amd64 and arm64 images, runs backend/frontend tests, dependency audits, reachable Go-vulnerability checks, image scans and image signing.

## License

GNU Affero General Public License v3.0 — see [LICENSE](LICENSE).
