---
title: Compose secrets
sidebar_position: 1
---

# Compose secrets

Dockman manages standard Docker Compose secrets with SOPS/age as the encrypted
source of truth. The runtime contract does not depend on Dockman: a host
one-shot service materializes file values into tmpfs before Docker starts, and
the repository-local helper handles inline values without calling Dockman.

## Runtime layout

For a stack stored in `compose/myapp`, the persistent and volatile layout is:

```text
myapp/
├── compose.yml
├── secrets.sops.yaml      encrypted, safe for Git
├── .dockman-sops-inline   non-secret runtime marker
├── compose-sops.sh        autonomous recovery helper
└── .secrets/              tmpfs, recreated at boot
    ├── database_password  plaintext only in RAM
    └── api_token          plaintext only in RAM
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

The container receives only the declared value at
`/run/secrets/database_password`. Prefer images supporting conventional
`*_FILE` variables. Inline `${NAME}` remains available for applications that
cannot consume files, but Docker records such environment values in container
metadata visible to principals with Docker API access.

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

In the initial migration mode, before replacing or deleting a value Dockman saves the previous value under
`.secrets/.history/`. Only the three latest versions per secret are retained.
History directories use mode 0700 and version files use mode 0600. A deleted
secret can be recovered from **Recover deleted secrets**. Restoring a version
first preserves the current value when one exists.

This history is a short migration safety net, not a backup strategy. Enabling
encrypted runtime removes `.secrets` and this plaintext history only after the
ciphertext has been decrypt-verified.

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

The Secrets page distinguishes a temporary migration state from the target
encrypted runtime:

- **migration mode** keeps legacy plaintext under `.secrets/` only long enough
  to encrypt and verify it;
- **encrypted runtime** makes `secrets.sops.yaml` the sole persistent source.
  The same stack can deliver some values as standard Compose file secrets from
  volatile tmpfs and other values inline through `${NAME}`.

The materialized policy provides two explicit actions:

- **Encrypt runtime** reads the current `.secrets` values into bounded memory,
  encrypts them through SOPS, decrypt-verifies the result with the configured
  identity, and atomically writes standard `secrets.sops.yaml` ciphertext;
- **Materialize source** decrypts that source in memory and writes matching
  runtime files under `.secrets`. Runtime files absent from the source are
  preserved, never deleted implicitly.

### Encrypted runtime, including local-only stacks

Encrypted runtime does not require Git. It works for a stack that exists only on the
local Compose filesystem:

1. create secrets using valid environment names such as `API_TOKEN`;
2. reference them directly with `${API_TOKEN}`, through
   `file: ./.secrets/API_TOKEN`, or both;
3. install the host boot runtime from **Host boot wizard**;
4. select **Enable encrypted runtime** and type `CONFIRM`;
5. run `sudo systemctl restart dockman-secrets-host` once after activating a
   new stack, then recreate its services.

Dockman encrypts and decrypt-verifies every current value before deleting
`.secrets/` and its bounded plaintext history. It writes only:

- `secrets.sops.yaml`, the encrypted source of truth;
- `.dockman-sops-inline`, a non-secret portable policy marker;
- `compose-sops.sh`, a non-secret recovery helper with mode `0700`.

Creating, editing or deleting a value in the Secrets page then decrypts the
bounded source in memory, atomically re-encrypts it and verifies the result.
There is no plaintext intermediate file, periodic export, scheduler or idle
process. Every Dockman Compose operation automatically receives inline values
for the lifetime of that process only. File values exist only in the stack's
tmpfs-backed `.secrets` mount.

```yaml
services:
  app:
    image: example/app:latest
    environment:
      API_TOKEN: ${API_TOKEN}
```

The resulting container configuration contains `API_TOKEN` in plaintext, as
with every Docker environment variable.

For applications that read `/run/secrets/...` or support the `*_FILE`
convention, keep the portable file declaration:

```yaml
secrets:
  database_password:
    file: ./.secrets/DATABASE_PASSWORD

services:
  database:
    image: postgres:latest
    environment:
      POSTGRES_PASSWORD_FILE: /run/secrets/database_password
    secrets:
      - source: database_password
        target: database_password
        mode: 0440
```

Here, `DATABASE_PASSWORD` exists persistently only inside encrypted
`secrets.sops.yaml`. The host service recreates `.secrets/DATABASE_PASSWORD`
inside tmpfs before Docker starts; Compose mounts only that declared file
read-only into the selected service. The same stack may simultaneously use
other values through `${API_TOKEN}`. File delivery is recommended whenever the
application supports it because the value does not appear in the container
environment or `docker inspect` output.

Used file sources outside `.secrets` remain rejected during conversion because
Dockman cannot guarantee that they are encrypted at rest. Environment-backed
Compose secrets remain supported for writable-rootfs services, but a standard
`.secrets` file is more portable and also works with `read_only: true`.

:::warning Read-only root filesystems

Docker Compose currently rejects `secrets.<name>.environment` for a service
with `read_only: true`; its implementation can only bind a `file:` source in
that case. Dockman detects this before activation or deployment.

For an application such as cloudflared that accepts the value directly, keep
the read-only root filesystem and use `TUNNEL_TOKEN: ${CLOUDFLARE_TUNNEL_TOKEN}`.
The host remains ciphertext-only, but Docker stores the value in the container
configuration where users with Docker API access can inspect it. Otherwise use
`file: ./.secrets/CLOUDFLARE_TUNNEL_TOKEN`. The autonomous boot runtime is the
decryptor that makes file delivery, read-only rootfs, ciphertext-only disk and
unattended host restart compatible.

:::

### Host boot wizard and recovery without Dockman

Open **Settings → Secrets → Host boot wizard** and run the generated command
once on each Docker host. It copies the pinned helper and SOPS binary from the
image, copies the configured age identity without printing it, and installs:

- `/usr/local/libexec/dockman-secrets-host`;
- `/usr/local/libexec/dockman-sops`;
- `/etc/dockman-secrets-host.json` with mode `0600`;
- `dockman-secrets-host.service`;
- Docker service and socket drop-ins requiring that service.

The unit runs after local filesystems and before Docker. It scans only explicit
`.dockman-sops-inline` markers, validates each bounded ciphertext, mounts an
empty tmpfs over that stack's `.secrets`, decrypts in bounded memory and writes
the values atomically. It exits immediately and retains no process, polling
loop or cache. A non-empty persistent `.secrets` directory is never covered:
the helper stops instead of hiding unexpected plaintext.

The supported automatic boot path targets Linux hosts using systemd and the
standard `mount`, `umount` and `findmnt` tools (normally provided by
util-linux). Other init systems can invoke
`dockman-secrets-host materialize --config /etc/dockman-secrets-host.json`
before starting Docker and call `cleanup` after Docker stops.

The first installation must be run as root on the host. A container with only
the Docker socket cannot safely install host systemd units. Mount the stack
root into Dockman with one-way `rslave` propagation, for example
`/server/stacks:/server/stacks:rslave`, then recreate Dockman once. New host
tmpfs mounts become visible in Dockman, while mounts created in the container
cannot propagate back to the host.

For manual recovery, restore the independently backed-up age identity, run:

```console
sudo systemctl restart dockman-secrets-host.service
```

Then run from the stack directory:

```console
export SOPS_AGE_KEY_FILE=/secure/recovery/dockman-sops-age-key.txt
./compose-sops.sh up
```

Supported recovery actions are `up`, `down`, `start`, `stop`, `restart`,
`pull`, `config`, `ps` and `shell`. The script calls standard `sops exec-env`
and verifies that a required file-secret tmpfs is mounted; it does not call
Dockman or its API. A plain `docker compose up` works for file-only stacks after
the host unit has run. Inline `${NAME}` values require
`./compose-sops.sh up`. Direct interpolation and top-level
`secrets.<name>.environment: NAME` declarations both work through this same
recovery path.

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
the encrypted source and tmpfs files stay on that remote filesystem. For an
inline Compose action over SSH, Dockman sends the environment through the
encrypted SSH stdin channel; values are neither placed in the remote command
line nor on persistent storage. Independent recovery requires installing the
host runtime and securely provisioning the matching age identity on that host.
The private identity is never copied into a stack, Git repository, API
response, log or SQLite database.

## Multiple hosts

Every operation is scoped by both Docker host and alias-qualified stack path.
`local:compose/myapp` and `remote:compose/myapp` are distinct stores. Install
the wizard output independently on every host. A single Dockman instance
currently uses one configured recipient, so those hosts must receive the
matching identity; use separate Dockman instances and recipients when strict
cryptographic separation between hosts is required.

There is no secret polling, background scheduler or resident plaintext cache. CPU overhead is zero while the feature is idle. A single value is limited to 1 MiB and an encrypted source to 4 MiB during an explicit operation. Compose analysis reads at most four 4 MiB manifests and runs only when the user loads or refreshes a stack.

## Backup responsibility

Migration mode protects permissions, not storage encryption and should not be
kept as the final state. In encrypted runtime, back up `secrets.sops.yaml`, the
portable marker and recovery script normally through Git, and back up the age
identity separately. Loss of that identity makes the ciphertext unrecoverable.
