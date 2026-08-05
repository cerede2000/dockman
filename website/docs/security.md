---
sidebar_position: 7
title: Security model
---

# Security model

Dockman controls Docker, reads and writes Compose files, can execute commands in managed containers and may hold Git/SMTP credentials. Treat its authenticated UI as an administrative interface.

## Recommended deployment boundary

- Keep Dockman on a trusted LAN or behind a VPN such as Tailscale, WireGuard or NetBird.
- Enable built-in authentication or OIDC.
- Terminate HTTPS at a trusted reverse proxy.
- Use a Docker socket proxy and expose only the API groups you need.
- Never publish the unauthenticated port directly to the Internet.

## Container hardening

The integration image supports:

- read-only root filesystem with a writable `/tmp` tmpfs;
- all Linux capabilities dropped, then only `DAC_OVERRIDE` and optionally `CHOWN` restored;
- `no-new-privileges`;
- persistent Docker CLI state under `/config/docker-cli`;
- disabled self-Exec and Host Shell by default;
- Docker API access through `DOCKER_HOST` instead of a direct socket mount.

With a read-only rootfs, use `PUID=0`/`PGID=0`; the non-root entrypoint path needs to modify `/etc/passwd` and `/etc/group`. Running as UID 0 inside the container does not remove the need for capabilities: after `cap_drop: ALL`, only explicitly restored capabilities are effective.

## Docker authority

A socket proxy reduces accidental API exposure, but any client allowed to create privileged containers, mount arbitrary host paths or execute in containers can still reach host-equivalent authority. Restrict network access to the proxy so only Dockman can contact it.

Feature permissions are additive:

- read-only inventory needs containers, images, networks, volumes, events and info;
- lifecycle actions need POST/start/stop/restart permissions;
- terminal and compatibility file browsing need Exec;
- Buildx needs Build and temporary container create/delete;
- update and prune need image pull/delete and system prune endpoints.

See the [socket-proxy matrix](docker-socket/index.md).

## Credentials at rest

Git and notification credentials are encrypted with independent AES-GCM vaults. Their master keys should be supplied with:

- `DOCKMAN_GIT_MASTER_KEY_FILE`;
- `DOCKMAN_NOTIFICATION_MASTER_KEY_FILE`.

The key files must contain 32 raw bytes or base64-encoded 32-byte material. Store them outside `/config`, mount them read-only and back them up separately. API responses never return decrypted secrets.

Git synchronization backups can contain explicitly authorized sensitive files. They inherit filesystem protection from the Git storage volume and are not independently archive-encrypted. Protect and back up that volume accordingly.

## Filesystem confinement

File operations are resolved beneath a configured alias, stack root, container root or volume root. The implementation rejects traversal, unsafe special files and symlink escapes. Container browsing prefers bounded Docker archive operations and falls back to container tools only when required.

Git policies do not read excluded trees. Sensitive files, special files, Git metadata, symlinks, size limits and inventory limits remain protected even in **All files** mode.

## Browser and HTTP controls

- Same-origin requests are allowed; extra origins must be listed explicitly in `DOCKMAN_ORIGINS`.
- Ordinary requests and uploads have separate size limits.
- Header and idle timeouts protect slow or abandoned connections.
- Authentication middleware protects administrative APIs.
- The sole unauthenticated GitHub webhook route accepts POST JSON only and is protected by a per-repository HMAC-SHA256 secret, exact repository/branch checks, bounded replay storage, payload limits and a coalescing queue.
- Typed destructive confirmations use the single word `CONFIRM` while retaining action-specific warnings.

## Automation safeguards

Git auto-deploy and image auto-update are opt-in. Their protections include validation, dry-run where applicable, per-stack isolation, health observation, circuit breakers, rollback, bounded histories and a persistent global pause.

Automatic Dockman-to-Git pushes are intentionally not performed. Local changes remain pending until an operator previews and pushes them.

## Reporting vulnerabilities

Follow [the repository security policy](https://github.com/cerede2000/dockman/blob/integration/SECURITY.md). Do not disclose credentials, private repository content, Compose secrets or host details in a public issue.
