---
title: Docker socket proxy
---

# Docker socket proxy

Dockman honors `DOCKER_HOST`. A proxy limits the Docker API routes exposed to the application and avoids mounting the raw socket into Dockman.

```yaml
services:
  dockman:
    image: ghcr.io/cerede2000/dockman:integration
    environment:
      DOCKER_HOST: tcp://socketproxy:2375
      DOCKMAN_COMPOSE_ROOT: /server/stacks
    depends_on:
      socketproxy:
        condition: service_healthy

  socketproxy:
    image: lscr.io/linuxserver/socket-proxy:latest
    environment:
      LOG_LEVEL: info
      PING: 1
      VERSION: 1
      INFO: 1
      EVENTS: 1
      CONTAINERS: 1
      IMAGES: 1
      NETWORKS: 1
      VOLUMES: 1
      EXEC: 1
      POST: 1
      SYSTEM: 1
      ALLOW_START: 1
      ALLOW_STOP: 1
      ALLOW_RESTARTS: 1
      BUILD: 1
      AUTH: 0
      COMMIT: 0
      CONFIGS: 0
      DISTRIBUTION: 0
      NODES: 0
      PLUGINS: 0
      SERVICES: 0
      SESSION: 0
      SWARM: 0
      TASKS: 0
      SECRETS: 0
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    read_only: true
    tmpfs:
      - /run
      - /tmp
      - /var/lib/haproxy
    healthcheck:
      test: ["CMD", "wget", "--quiet", "--spider", "http://127.0.0.1:2375/version"]
      interval: 30s
      timeout: 5s
      retries: 3
    restart: unless-stopped
```

Exact variable names depend on the proxy image. The example targets LinuxServer socket-proxy.

## Permission matrix

| Feature | Required API groups |
|---|---|
| Monitor, details, stats | `CONTAINERS`, `INFO`, `EVENTS` |
| Image inventory/check/update | `IMAGES`, `POST`; registry digest checks do not require `DISTRIBUTION` |
| Networks and volumes | `NETWORKS`, `VOLUMES` |
| Start/stop/restart/Compose | `POST`, lifecycle allowances, containers/networks/volumes/images |
| Container terminal | `EXEC`, `POST` |
| Container file browser | containers/archive; `EXEC` for compatibility fallbacks |
| Prune | `SYSTEM`, affected resource group and `POST` |
| Dockerfile/Buildx build | `BUILD`, containers, images and permission to create/delete the temporary BuildKit helper |
| Protected socket-proxy update | containers/images and lifecycle write access |

`POST=1` is essential for mutations. A read-only proxy filesystem does not make the Docker API read-only; API policy comes from the proxy variables.

## Reduce privileges by feature

- Set `BUILD=0` if Dockerfile builds are not used.
- Set `EXEC=0` if neither terminal nor compatibility file browsing is needed.
- Keep Swarm, plugins, nodes, secrets, configs and distribution disabled unless a verified Dockman feature requires them.
- Do not expose port 2375 outside the private Docker network.

## Troubleshooting

### Reads work but actions fail

Verify `POST`, lifecycle allowances and the specific resource group. Inspect proxy logs for the denied method/path.

### Build succeeds but helper remains

Builder removal also uses Docker write APIs. The build log reports cleanup failure separately. Grant the narrow required delete operation and remove only confirmed orphan `buildx_buildkit_dockman-*` containers.

### Update check reports a private image error

Current registry checks support public authentication challenges but do not store private-registry credentials yet. This is independent of Docker API proxy permissions.

## Security boundary

A proxy is defense in depth, not a complete sandbox. Allowing container creation, arbitrary bind mounts and Exec can still provide host-level authority. Restrict who can access Dockman and who can reach the proxy network.

Resources:

- [Docker Engine API](https://docs.docker.com/reference/api/engine/)
- [LinuxServer socket-proxy](https://github.com/linuxserver/docker-socket-proxy)
