---
title: Files and editor
sidebar_position: 0
---

# Files and editor

The Files view manages Compose stacks and adjacent configuration without hiding them behind an application-specific model.

## Navigation and operations

- nested folders with horizontal scrolling;
- create, upload, download, rename, permissions and deletion;
- file aliases for mounted paths outside the Compose root;
- sortable/searchable lists and compact context menus;
- editor tabs closed or refreshed correctly when changing Docker host;
- external-change protection for unsaved editors;
- SQLite and YAML tooling.

Folder and stack bullets aggregate actual Compose/container state even while collapsed. Running, healthy, unhealthy, stopped and fully down stacks remain distinct. Nested Compose stacks are indexed without adding continuous polling.

## Compose editor

Compose files add:

- validation errors;
- deploy/start/stop/restart/update actions;
- quick navigation to `services`, individual services, `networks`, `volumes` and `secrets` while ignoring comments;
- Git synchronization status/actions when the stack belongs to a folder link;
- direct Dockerfile build from the file context menu.

Quick YAML navigation places the selected declaration at the top of the editor viewport when possible.

## Git badges

- No badge means the file/folder is outside every folder link or outside policy.
- A stack synchronization icon opens stack/folder actions.
- A small cloud badge on a normal file means that exact file is included by policy; it is informative and does not open stack actions.
- Excluded and sensitive files must not receive a badge or affect stack synchronization status.

Manual folder links still expose **Sync now** from Files so Git changes can be fetched without opening Settings.

## Destructive actions

Deleting a linked stack/folder first checks Git coherence and offers local-only, Git-aware or link-removal choices. Git deletion and orphan cleanup use backup where required and the typed confirmation `CONFIRM`.
