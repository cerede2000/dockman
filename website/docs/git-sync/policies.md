---
title: File policies and conflicts
sidebar_position: 2
---

# File policies

Policies decide which files belong to synchronization. They are evaluated before opening file contents, so excluded `data`, `secret`, cache or application trees do not create unnecessary I/O or permission failures.

## Profiles

### Docker Compose only

Automatically includes only catalogued stack manifests:

- `compose.yml`, `compose.yaml`;
- `docker-compose.yml`, `docker-compose.yaml`;
- `.env.example`, `.env.sample`, `.env.template`, `.env.dist` and conventional variants located with a selected stack.

It does not include arbitrary YAML files or real `.env` files. Additional exact files such as `config/app.conf` can be selected manually.

### Compose and configuration

Includes stack manifests plus common configuration, template, script, documentation and public-certificate formats. Sensitive-file protection remains active.

### All files

Includes every admissible regular file in the selected stack folders. It never bypasses exclusions, sensitive-file protection, symlink/special-file rejection, size limits or inventory limits.

## Rules and tree selector

The policy dialog supports:

- broad include/exclude globs such as `*.log`;
- an expandable file tree for exact choices;
- per-stack policy overrides within a complete folder link;
- repository-global exclusions;
- `.dockmanignore`;
- search, filtering, pagination and persistent multi-selection.

An exact rule created by the file context menu is removed when that exact synchronized file is explicitly deleted. Generic rules such as `*.conf` or complete-folder rules are retained.

## Sensitive files

Real `.env` files, private keys and credential-like files are excluded by default. A one-time transfer can include them only after the operator types `CONFIRM`. This exception is not remembered by automatic synchronization.

## Hard protections

No rule can opt in:

- `.git` metadata;
- unsafe symlinks or paths outside the link root;
- sockets, devices and other special files;
- files over hard size limits;
- inventories over absolute safety limits without narrowing the policy.

Unreadable files inside an otherwise admissible profile are reported with their stack/path and skipped independently where safe; they must not block unrelated stacks.

## Three-way conflict detection

Dockman compares local content, Git content and the last accepted baseline. A conflict is created only when both sides changed relative to that baseline.

The resolution dialog can compare text blocks and resolve one file at a time. Unresolved files remain pending. After a decision, Dockman runs a fresh check automatically so stale red/orange status does not remain cached.

## Deletions

Source-side deletion is preserved by default. The operator chooses among:

- restore from the other side;
- archive and remove locally;
- back up and delete locally;
- commit deletion to Git;
- stop synchronizing an exact path or stack.

Destructive dialogs use the single typed confirmation `CONFIRM`. Successful deletion resolution refreshes the baseline and status in the same workflow; a second no-op sync should not be necessary.
