---
title: Private registries
sidebar_position: 5
---

# Private registries

Images that need a login - a private GitHub or GitLab registry, a self-hosted one, a paid Docker Hub account - are reached with credentials stored in **Settings → Registries**: a registry host, a username and a password or token.

One credential per registry. It is used for every image whose reference resolves to that host, whichever spelling the compose file uses: a credential for `docker.io` serves `nginx`, `library/nginx` and `index.docker.io/me/app` alike.

## What a credential covers

| Path | Covered |
|---|---|
| `up`, `pull`, `update` and redeploys on the **local** host | yes |
| Builds whose base image is private (`FROM` a private registry) | yes |
| The image replacement the Monitor performs, with health check and rollback | yes |
| Update detection: digest comparison and version discovery | yes |
| Hosts reached over **SSH** | no: that daemon pulls with its own `docker login` |
| The Dockman self-update helper | no: it pulls the public `docker:cli` image |

## Adding one

1. **Settings → Registries → Add registry**.
2. **Registry**: the host only - `ghcr.io`, `docker.io`, `registry.example.com:5000`. A pasted repository URL keeps its host.
3. **Username** and **password or token**. For GitHub, the username is the account and the token carries the permissions (`read:packages` is enough to pull).
4. **Test** performs the registry's own authentication exchange and reports what it answered.

The credential is encrypted at rest, and written to the Docker CLI configuration Dockman owns (`$DOCKER_CONFIG/config.json`, by default `/config/docker-cli`) so the Compose and Docker commands authenticate. Removing it takes the entry out of that file as well.

## Where the key lives

Credentials are sealed with a master key in `/config/registry/master.key`, generated on first start. For production, mount your own as a Docker secret and point `REGISTRY_MASTER_KEY_FILE` at it, as with the Git and notification keys: back it up separately from the database, since the database alone cannot be read without it.

A key that changes makes the stored secrets unreadable. Dockman then starts normally, logs which credential it could not read, and the others keep working; re-enter the one it names.

## Limits

- **Update detection uses HTTPS.** A registry reachable only over plain HTTP still pulls - that part goes through the Docker daemon - but its digest and tag checks are reported as errors rather than guessed.
- **SSH hosts** keep their own credentials. Run `docker login` once on that host, or use a socket proxy and let Dockman reach its daemon.
- **One credential per registry.** Two accounts on the same host are not expressible; use the one with the rights the stacks need.

## Troubleshooting

| What you see | What it means |
|---|---|
| `registry authentication required for <host>; add a credential in Settings > Registries` | no credential is stored for that host |
| `<host> rejected the stored credentials; check them in Settings > Registries` | the stored login was presented and refused: expired token, wrong username, missing scope |
| `<host> answered without asking for authentication; the credentials were not checked` | the Test button reached an open registry, so the credential proved nothing |
| A pull works but updates report an error | the registry is HTTP-only, or it blocks the manifest endpoint |
