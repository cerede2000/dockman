# Git sync lot 4 — synchronization baseline and conflict decisions

This lot records only a SHA-256 baseline for files confirmed by a successful transfer. It never stores another copy of stack contents. A later preview detects when the destination changed since that baseline and refuses to overwrite it until the user explicitly chooses a direction.

## 1. Establish a baseline

1. Start `ghcr.io/cerede2000/dockman:git-sync-lot-4` with the existing `/config` and Git key.
2. Open a linked folder that currently matches Git.
3. Run a preview and confirm one export or import, even if it reports no changed files.

Expected: the operation succeeds and establishes the first baseline without creating a commit or copying unchanged files.

## 2. Safe one-sided changes

1. Edit one tracked file only in Dockman.
2. Preview stack → Git.

Expected: the file is `modify`, not `conflict`, and export works normally.

Repeat after the new baseline by editing only Git, fetching/pulling, and previewing Git → stack. Import must work normally and create its usual backup.

## 3. Wrong-direction protection

1. After a successful synchronization, edit a tracked file only in Git and pull it into Dockman's managed repository.
2. Open stack → Git instead of Git → stack.

Expected: the file is marked `conflict`; commit/push is disabled because that direction would overwrite the newer Git version.

To keep Git, close the dialog and import Git → stack. To intentionally keep Dockman, tick the explicit overwrite confirmation and export.

## 4. Both sides changed

1. Synchronize a file to establish a fresh baseline.
2. Give it different contents in Dockman and Git, then fetch/pull the repository.
3. Preview in either direction.

Expected: both directions report a conflict. Choose stack → Git to keep Dockman, or Git → stack to keep Git. Import creates a backup before overwriting.

## 5. Persistence and cleanup

1. Restart Dockman before resolving a conflict and reopen the preview.
2. Delete a disposable folder link after the test.

Expected: conflict detection survives restart. Removing the link also removes its SHA baseline rows, but never removes stack or Git files.

## Storage check

The baseline database contains only binding ID, relative path and SHA-256. The managed repository remains one shared working tree per repository; linked stacks do not create additional Git clones.
