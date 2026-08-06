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

The eye button reveals the value only inside the explicit edit dialog. Multiline
values remain masked until that button is used.

## Compose checks and bounded history

When a stack is loaded, Dockman reads only conventional manifests located at
that exact stack root (`compose.yml`, `compose.yaml`, `docker-compose.yml` or
`docker-compose.yaml`). It reports:

- which services consume each declared secret;
- whether the source is external or file-backed;
- whether a file-backed source follows `./.secrets/<secret-name>`;
- whether the expected runtime file exists.

The Compose key and source filename may differ. For example,
`database_password: {file: ./.secrets/db-password.txt}` is managed correctly
and creates or checks `db-password.txt`. A file outside `.secrets` remains a
valid Compose source; Dockman labels it **not managed** instead of treating it
as an invalid Compose declaration.

The settings page discovers conventional Compose manifests across every alias
of the active host and offers their stack directories in a grouped selector.
Selection triggers loading directly. Manual entry remains available for an
unusual or temporarily undiscoverable path. Discovery is bounded to 1000
directories, 500 stacks and eight levels per alias, and runs only when the
Secrets tab opens or its refresh button is pressed.

The check is request-driven: it does not recursively scan stacks and does not
add a background watcher.

Before replacing or deleting a value, Dockman saves the previous value under
`.secrets/.history/`. Only the three latest versions per secret are retained.
History directories use mode 0700 and version files use mode 0600. A deleted
secret can be recovered from **Recover deleted secrets**. Restoring a version
first preserves the current value when one exists.

This history is a short operational safety net, not a backup strategy. It is
stored on the same host and storage as the active secret.

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

There is no secret polling, background scheduler or resident plaintext cache. CPU overhead is zero while the feature is idle, and memory usage is bounded to one value with a 1 MiB maximum during an explicit operation. Compose analysis reads at most four 4 MiB manifests and runs only when the user loads or refreshes a stack.

## Backup responsibility

Plain-file mode protects permissions, not storage encryption. Back up `.secrets/` through an encrypted backup system and restrict access to the Compose root. The later SOPS/age mode will allow encrypted sources in Git and deterministic recovery using an independently backed-up age identity.
