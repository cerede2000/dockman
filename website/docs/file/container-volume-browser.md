---
title: Container and volume browsers
sidebar_position: 5
---

# Container filesystem browser

Container Details includes a Files tab with breadcrumbs, sorting, hidden-file control, upload/download, create, rename, permissions and deletion.

Dockman does not assume that `ls`, `find`, `tar` or even a shell exists in the target image. It prefers Docker archive APIs and can inject a bounded architecture-specific helper when required. Read-only/special images fall back to available container tools only when safe.

The browser detects a read-only root filesystem and writable bind/volume mount boundaries. Write actions are disabled globally on read-only areas and re-enabled when navigation enters a writable mount.

Special volatile paths such as `/proc`, `/sys` and `/dev` may disappear between list and stat and are skipped rather than failing the complete directory.

# Volume filesystem browser

Volume Details uses the same UI and operations. A short-lived helper mounts the selected volume; it is constrained and removed after the operation. Volume inspection is shown in the same bordered overlay style as Container Details rather than a full-screen replacement.

Changing owner requires sufficient Dockman capabilities. Recursive chmod/chown and archive download should be used carefully on large trees.
