---
title: Troubleshooting
sidebar_position: 2
---

# Troubleshooting

## Collect a concise runtime sample

```bash
for i in $(seq 1 30); do
  docker stats --no-stream --format '{{.Name}} CPU={{.CPUPerc}} RAM={{.MemUsage}} PIDS={{.PIDs}}' dockman socketproxy
  sleep 2
done
```

Short CPU spikes during scans, stats collection, Git operations or garbage collection are expected. Persistent idle usage is not; correlate timestamps with scheduled Git/image jobs and socket-proxy traffic.

## Buildx host entitlement denied

Update to an image containing the job-scoped builder fix. A valid host build log must show a `dockman-*` builder, daemon entitlement and build entitlement. The reserved context builder `default` must not be removed.

## BuildKit helper remains

```bash
docker ps -a --filter name=buildx_buildkit
docker buildx ls
```

If a `dockman-*` helper remains, inspect the end of the build log for a proxy denial on builder/container deletion. Ensure the proxy permits the corresponding write operation. Remove only the exact orphan after confirming no build uses it.

## SMTP certificate signed by unknown authority

Mount the issuing CA chain and set `DOCKMAN_SMTP_CA_FILE` to the in-container path. Verify the PEM contains a CA certificate and that its SAN/chain validates the SMTP hostname. Do not disable certificate verification.

## Git reports local changes but nothing is transferable

Refresh the stack status. Only files admitted by the current profile/rules can produce `Local changes waiting`. Files outside policy must not color the stack or receive a synchronized badge. If the state persists, inspect the policy tree and activity error rather than forcing an All-files scan.

## Git inventory limit or unreadable file

Use Docker Compose only or add narrow exact include rules. The result view identifies problematic stack/path entries. Excluded trees are not read. Permission failures are isolated where safe and must not block unrelated selected stacks.

## Folder-link deletion state remains blocked

Resolve each preserved deletion from the stack/folder synchronization popup: restore, archive, delete locally, delete from Git or stop synchronizing. Successful resolution triggers a fresh check; editing and saving automation settings should not be required.

## `addgroup: /etc/group: Read-only file system`

The non-root entrypoint is trying to create its runtime account. With `read_only: true`, use `PUID=0` and `PGID=0`, or provide a writable rootfs. See the hardened installation example.

## Provisioning `operation not permitted`

Test against the exact mounted stack path from inside Dockman. Create/chmod requires directory write/search rights and possibly `DAC_OVERRIDE`; changing to another UID/GID also requires UID 0 and `CHOWN`. Docker capabilities cannot override a genuinely read-only bind mount or unsupported remote filesystem semantics.

## File browser special paths

Entries such as `/dev/ptmx` and `/proc/*` can disappear or return Docker archive 404s between listing and stat. Dockman skips these volatile special paths; browse regular files or writable bind/volume mounts instead.

## Logs

Temporarily set `DOCKMAN_LOG_LEVEL=debug` or `DOCKMAN_LOG_VERBOSE=true`, reproduce once, then disable it. `DOCKMAN_LOG_HTTP=true` is more verbose and may expose operational metadata.
