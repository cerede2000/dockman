---
title: Compose secrets
sidebar_position: 1
---

# Compose secrets

Dockman manages portable, file-backed Docker Compose secrets. The runtime contract does not depend on Dockman: after the files exist, ordinary `docker compose up -d` continues to work if Dockman is stopped or unavailable.

## Runtime layout

For a stack stored in `compose/myapp`, Dockman writes:

```text
myapp/
├── compose.yml
└── .secrets/             mode 0700
    ├── database_password mode 0600
    └── api_token         mode 0600
```

Reference those files using standard Compose syntax:

```yaml
services:
  app:
    image: example/app:latest
    secrets:
      - database_password

secrets:
  database_password:
    file: ./.secrets/database_password
```

The container receives the value at `/run/secrets/database_password`. Prefer images supporting the conventional `*_FILE` variables rather than copying secret contents into environment variables.

## Managing values

Open **Settings → Secrets**, verify the active Docker host, then enter an alias-qualified stack directory such as `compose/myapp`.

- listing returns names, sizes and dates but never values;
- revealing a value is a separate explicit operation;
- writes use a same-directory temporary file followed by an atomic rename;
- replacing a value never writes it to Dockman's SQLite database;
- deleting a secret does not restart or recreate a container automatically.

Recreate affected services after changing a mounted secret:

```console
docker compose up -d --force-recreate app
```

## Git boundary

`.secrets/` is unconditionally excluded by Dockman's Git synchronization, including the **all files** profile and one-time sensitive-file transfers. Add the same rule to repositories that may also be handled outside Dockman:

```gitignore
.secrets/
```

Encrypted `secrets.sops.yaml` sources will be supported separately. They must never be stored inside `.secrets/`.

## Multiple hosts

Every operation is scoped by both Docker host and alias-qualified stack path. `local:compose/myapp` and `remote:compose/myapp` are distinct stores. Remote SSH hosts use their existing SFTP filesystem; secret values are not copied into Dockman's local configuration directory.

There is no secret polling, background scheduler or resident plaintext cache. CPU overhead is zero while the feature is idle, and memory usage is bounded to one value with a 1 MiB maximum during an explicit operation.

## Backup responsibility

Plain-file mode protects permissions, not storage encryption. Back up `.secrets/` through an encrypted backup system and restrict access to the Compose root. The later SOPS/age mode will allow encrypted sources in Git and deterministic recovery using an independently backed-up age identity.
