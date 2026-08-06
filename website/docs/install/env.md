---
title: Environment-variable reference
sidebar_position: 1
---

# Runtime configuration

Dockman prints its parsed application configuration at startup. Secrets are masked. Values below describe the current integration image; database-backed settings such as SMTP servers and update policies are configured in the UI and are not environment variables.

## Core server and paths

| Variable | Default | Purpose |
|---|---:|---|
| `DOCKMAN_PORT` | `8866` | HTTP listening port |
| `DOCKMAN_MACHINE_ADDR` | auto-detected | Address used when Dockman constructs its own URL |
| `DOCKMAN_COMPOSE_ROOT` | `./compose` (`/compose` in the image) | Absolute root of managed Compose stacks |
| `DOCKMAN_CONFIG` | `./config` (`/config` in the image) | Persistent configuration and SQLite directory |
| `DOCKMAN_YAML_PATH` | `./config/dockyaml` | Custom Dockman UI configuration directory |
| `DOCKMAN_UI_PATH` | `dist` | Frontend assets; normally leave unchanged in the image |
| `DOCKMAN_PUB_CERT_PATH` | empty | Optional HTTPS public certificate path |
| `DOCKMAN_PRIV_KEY_PATH` | empty | Optional HTTPS private-key path |

`DOCKMAN_COMPOSE_ROOT`, its host bind source and its container bind target must use the same absolute path.

## HTTP security and limits

| Variable | Default | Purpose |
|---|---:|---|
| `DOCKMAN_ORIGINS` | empty | Comma-separated additional browser origins; same-origin is always allowed |
| `DOCKMAN_HTTP_MAX_BODY_MB` | `16` | Maximum ordinary request body in MiB; `0` disables the limit |
| `DOCKMAN_HTTP_MAX_UPLOAD_MB` | `1024` | Maximum file upload in MiB; uploads are streamed; `0` disables the limit |
| `DOCKMAN_HTTP_READ_HEADER_TIMEOUT` | `10` | Header-read timeout in seconds |
| `DOCKMAN_HTTP_IDLE_TIMEOUT` | `120` | HTTP keep-alive idle timeout in seconds |
| `DOCKMAN_LOGS_KEEPALIVE` | `5` | Seconds between empty log-stream keepalives; positive integers only |
| `DOCKMAN_ALLOW_SELF_EXEC` | `false` | Enables Host Shell and Exec into Dockman's own container; troubleshooting only |

Do not add an origin unless a separate trusted frontend legitimately calls Dockman. Enabling self-Exec exposes the Dockman configuration and mounted credentials to terminal users; remove the variable and recreate the container after troubleshooting.

## Authentication and OIDC

| Variable | Default | Purpose |
|---|---:|---|
| `DOCKMAN_AUTH_ENABLE` | `false` | Enable built-in authentication |
| `DOCKMAN_AUTH_USERNAME` | `admin` | Local username |
| `DOCKMAN_AUTH_PASSWORD` | `admin99988` | Local password; always replace when auth is enabled |
| `DOCKMAN_AUTH_EXPIRY` | `24h` | Session cookie lifetime, for example `30m`, `12h` |
| `DOCKMAN_AUTH_MAX_SESSIONS` | `5` | Maximum active sessions per user |
| `DOCKMAN_AUTH_OIDC_ENABLE` | `false` | Enable OIDC |
| `DOCKMAN_AUTH_OIDC_AUTO_REDIRECT` | `true` | Redirect directly to the OIDC provider |
| `DOCKMAN_AUTH_OIDC_ISSUER` | empty | OIDC issuer URL |
| `DOCKMAN_AUTH_OIDC_CLIENT_ID` | empty | OIDC client ID |
| `DOCKMAN_AUTH_OIDC_CLIENT_SECRET` | empty | OIDC client secret |
| `DOCKMAN_AUTH_OIDC_REDIRECT_URL` | empty | Exact callback URL configured at the provider |
| `DOCKMAN_AUTH_OIDC_SECURE` | `true` | Require secure OIDC cookies; disable only for deliberate HTTP testing |

Authentication does not make direct Internet exposure of a Docker-management endpoint risk-free. Prefer a VPN or an authenticated reverse proxy on a trusted network.

## Git synchronization

| Variable | Default | Purpose |
|---|---:|---|
| `DOCKMAN_GIT_SYNC` | `false` | Enable Git synchronization features |
| `DOCKMAN_GIT_MASTER_KEY_FILE` | generated under `/config` | File containing 32 raw bytes or base64-encoded 32-byte key material |
| `DOCKMAN_GIT_STORAGE_PATH` | `/config/git` hierarchy | Optional absolute dedicated root for compact repositories and backups |
| `DOCKMAN_GIT_HISTORY_RETENTION_DAYS` | `30` | Activity-history retention |
| `DOCKMAN_GIT_BACKUP_RETENTION_DAYS` | `30` | Git synchronization backup retention |
| `DOCKMAN_GIT_COMMIT_INSTANCE` | `dockman` | Stable instance name stored in commit provenance |

For production, mount `DOCKMAN_GIT_MASTER_KEY_FILE` as a secret. Back it up separately; changing it does not re-encrypt existing credentials.

`DOCKMAN_GIT_STORAGE_PATH` must be absolute and cannot be a filesystem root. Dockman stores Git objects without a permanent duplicate worktree and creates temporary worktrees only for transfers.

## Compose secrets and SOPS/age

| Variable | Default | Purpose |
|---|---:|---|
| `DOCKMAN_SOPS_BINARY` | `sops` | SOPS executable used for explicit encrypted-source operations |
| `DOCKMAN_SOPS_AGE_KEY_FILE` | empty | Mounted age identity file; back it up independently from Dockman |
| `DOCKMAN_SOPS_AGE_RECIPIENT` | empty | Public age recipient matching the configured identity |

No SOPS process, polling loop or plaintext cache exists while the feature is idle.

## Notifications and SMTP trust

| Variable | Default | Purpose |
|---|---:|---|
| `DOCKMAN_NOTIFICATION_MASTER_KEY_FILE` | generated under `/config` | 32-byte/base64 key used to encrypt notification credentials |
| `DOCKMAN_SMTP_CA_FILE` | `/etc/ssl/certs/smtp-ca.crt` if present | Additional trusted CA bundle for SMTP TLS/STARTTLS |

SMTP server, port, security mode, username, password, sender, recipients and notification choices are stored from the **Updates** UI. Supplying `DOCKMAN_SMTP_CA_FILE` explicitly makes the file mandatory: a missing or invalid file causes a clear error instead of silently weakening TLS.

## Logging

| Variable | Default | Purpose |
|---|---:|---|
| `DOCKMAN_LOG_LEVEL` | `info` | `disabled`, `debug`, `info`, `warn`, `error` or `fatal` |
| `DOCKMAN_LOG_VERBOSE` | `false` | Add verbose diagnostic context |
| `DOCKMAN_LOG_HTTP` | `false` | Log HTTP routes and requests |
| `DOCKMAN_LOG_AUTH_WARNING` | `true` | Show the startup warning when authentication is disabled |

Verbose and HTTP logging can expose paths and operational metadata. Enable them temporarily.

## Container/runtime variables

These variables are consumed by the image entrypoint, Docker client or Go runtime rather than Dockman's configuration parser.

| Variable | Default | Purpose |
|---|---:|---|
| `DOCKER_HOST` | local socket | Docker daemon or socket-proxy endpoint, for example `tcp://socketproxy:2375` |
| `DOCKER_CONFIG` | `$DOCKMAN_CONFIG/docker-cli` | Writable Docker/Buildx CLI state; useful with a read-only rootfs |
| `PUID` | `1000` | Runtime UID created by the entrypoint; use `0` with a read-only rootfs |
| `PGID` | `1000` | Runtime GID created by the entrypoint |
| `TZ` | image/system default | Timezone used for dates and cron interpretation |
| `GOMEMLIMIT` | cgroup-derived | Explicit Go memory soft limit; when absent Dockman derives a safe value from cgroups |

## Example environment file

```dotenv
DOCKMAN_COMPOSE_ROOT=/server/stacks
DOCKMAN_CONFIG=/config
DOCKER_HOST=tcp://socketproxy:2375
DOCKMAN_AUTH_ENABLE=true
DOCKMAN_AUTH_USERNAME=admin
DOCKMAN_AUTH_PASSWORD=replace-this
DOCKMAN_GIT_SYNC=true
DOCKMAN_GIT_MASTER_KEY_FILE=/run/secrets/dockman_git_key
DOCKMAN_NOTIFICATION_MASTER_KEY_FILE=/run/secrets/dockman_notification_key
DOCKMAN_SOPS_AGE_KEY_FILE=/run/secrets/dockman_sops_age_key
DOCKMAN_SOPS_AGE_RECIPIENT=age1replace-with-your-public-recipient
DOCKMAN_GIT_STORAGE_PATH=/git-data
TZ=Europe/Paris
```

After changing a runtime variable, recreate the Dockman container; a browser refresh is not sufficient.
