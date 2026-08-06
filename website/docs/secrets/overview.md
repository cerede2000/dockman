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

## Encrypted Git source with SOPS and age

Configure an independently backed-up age identity and its public recipient:

```yaml
services:
  dockman:
    environment:
      DOCKMAN_SOPS_AGE_KEY_FILE: /config/secrets/dockman-sops-age-key.txt
      DOCKMAN_SOPS_AGE_RECIPIENT: age1example...
```

The Dockman image includes the pinned `age-keygen` CLI. Generate the identity
inside the running container directly in its persistent `/config` mount:

```console
docker exec dockman dockman-age-keygen
```

The helper creates `/config/secrets/dockman-sops-age-key.txt` with directory
mode `0700`, file mode `0600`, and ownership matching `PUID:PGID`. It refuses
to overwrite an existing identity and prints its public `age1...` recipient.
The underlying `age-keygen` binary is also available for manual operation.
Point
`DOCKMAN_SOPS_AGE_KEY_FILE` to
`/config/secrets/dockman-sops-age-key.txt`, put that recipient in
`DOCKMAN_SOPS_AGE_RECIPIENT`, then recreate Dockman. `/config` must be
persistent.

Back up the private identity outside the host before relying on encrypted
recovery. Never commit it; only the `age1...` recipient is public. Generate one
identity per separate Dockman instance. A single multi-host Dockman instance
can use the same identity for its managed hosts unless operational separation
requires independent instances.

The Secrets page provides two storage policies:

- **materialized files** keeps runtime values under `.secrets/` and optionally
  maintains `secrets.sops.yaml` as an encrypted recovery source;
- **encrypted inline** makes `secrets.sops.yaml` the active store. No
  `.secrets/` directory or plaintext history remains. Dockman decrypts values
  only for an explicit reveal or a Compose command and injects them into that
  child process environment.

The materialized policy provides two explicit actions:

- **Encrypt runtime** reads the current `.secrets` values into bounded memory,
  encrypts them through SOPS, decrypt-verifies the result with the configured
  identity, and atomically writes standard `secrets.sops.yaml` ciphertext;
- **Materialize source** decrypts that source in memory and writes matching
  runtime files under `.secrets`. Runtime files absent from the source are
  preserved, never deleted implicitly.

### Encrypted inline mode, including local-only stacks

Inline mode does not require Git. It works for a stack that exists only on the
local Compose filesystem:

1. create secrets using valid environment names such as `API_TOKEN`;
2. reference them from Compose with `${API_TOKEN}`;
3. select **Enable inline** and type `CONFIRM`.

Dockman encrypts and decrypt-verifies every current value before deleting
`.secrets/` and its bounded plaintext history. It writes only:

- `secrets.sops.yaml`, the encrypted source of truth;
- `.dockman-sops-inline`, a non-secret portable policy marker;
- `compose-sops.sh`, a non-secret recovery helper with mode `0700`.

Creating, editing or deleting a value in the Secrets page then decrypts the
bounded source in memory, atomically re-encrypts it and verifies the result.
There is no plaintext intermediate file, periodic export, scheduler or idle
process. Every Dockman Compose operation automatically receives the decrypted
values for the lifetime of that process only.

```yaml
services:
  app:
    image: example/app:latest
    environment:
      API_TOKEN: ${API_TOKEN}
```

The resulting container configuration contains `API_TOKEN` in plaintext, as
with every Docker environment variable. Prefer file-backed Compose secrets for
applications supporting `*_FILE` if protection from Docker inspect is needed.

### Recovery without Dockman

Install Docker Compose and SOPS on the host, restore the independently backed
up age identity, then run from the stack directory:

```console
export SOPS_AGE_KEY_FILE=/secure/recovery/dockman-sops-age-key.txt
./compose-sops.sh up
```

Supported recovery actions are `up`, `down`, `start`, `stop`, `restart`,
`pull`, `config`, `ps` and `shell`. The script calls the standard
`sops exec-env` mechanism; it does not call Dockman or its API. Consequently a
plain `docker compose up` is insufficient for an inline stack, but the stack
remains operational independently through its repository-local recovery
script.

`secrets.sops.yaml` is included by the Docker Compose-only Git profile when it
is adjacent to a catalogued Compose manifest. The `.secrets/` directory remains
an unconditional Git boundary. Existing safe runtime permissions such as
`0444` or `0440` are preserved when a value is replaced, which supports
non-root images using file-based secrets.

The source is a normal SOPS document, so recovery is not tied to Dockman:

```console
SOPS_AGE_KEY_FILE=./dockman-sops-age-key.txt \
  sops decrypt --input-type yaml --output-type json secrets.sops.yaml
```

SOPS/age configuration is instance-wide but every read and write remains
scoped to the selected Dockman host and alias-qualified stack. For an SSH host,
the encrypted source and materialized files stay on that remote filesystem;
only the bounded operation transits Dockman's memory. For an inline Compose
action over SSH, Dockman sends the environment through the encrypted SSH stdin
channel; values are neither written remotely nor placed in the remote command
line. Independent recovery on that host still requires SOPS and a securely
restored identity. The private identity is
never copied into a stack, remote host, Git repository, API response, log, or
SQLite database.

## Multiple hosts

Every operation is scoped by both Docker host and alias-qualified stack path. `local:compose/myapp` and `remote:compose/myapp` are distinct stores. Remote SSH hosts use their existing SFTP filesystem; secret values are not copied into Dockman's local configuration directory.

There is no secret polling, background scheduler or resident plaintext cache. CPU overhead is zero while the feature is idle. A single value is limited to 1 MiB and an encrypted source to 4 MiB during an explicit operation. Compose analysis reads at most four 4 MiB manifests and runs only when the user loads or refreshes a stack.

## Backup responsibility

Plain-file mode protects permissions, not storage encryption. Back up `.secrets/` through an encrypted backup system and restrict access to the Compose root. SOPS/age works with or without Git and provides an encrypted recovery source, but loss of the independently backed-up age identity makes that source unrecoverable.
