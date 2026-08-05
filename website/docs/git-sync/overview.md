---
title: Overview and setup
sidebar_position: 1
---

# Git synchronization

Git synchronization associates a Dockman stack folder with a folder or root in a GitHub repository. A single installation can use multiple repositories, branches and folder links.

## Enable the feature

```yaml
environment:
  DOCKMAN_GIT_SYNC: "true"
  DOCKMAN_GIT_MASTER_KEY_FILE: /run/secrets/dockman_git_key
  DOCKMAN_GIT_STORAGE_PATH: /git-data # optional
```

Mount the master key and optional storage path persistently. When no explicit storage path is provided, repositories and backups live below `/config/git`.

## Storage model

Dockman stores compact Git objects without maintaining a permanent second checkout of every stack. A temporary worktree is created only while preparing a transfer or commit and is removed afterwards. This limits disk duplication and avoids continuously scanning a cloned tree.

## Add a repository

Supported repository forms include:

- `owner/repository`;
- `https://github.com/owner/repository`, with or without `.git`;
- supported GitHub SSH URLs.

Public repositories require no credential. Private GitHub repositories can use an HTTPS token or SSH key stored in Dockman's encrypted credential vault.

At repository creation, Dockman verifies the requested branch. A missing branch can be created after confirmation either from the repository default branch or as an empty independent branch.

The same normalized repository and branch cannot be registered twice.

## Create a folder link

A folder link defines:

- Docker host;
- local folder below `DOCKMAN_COMPOSE_ROOT`;
- repository and branch;
- destination Git folder;
- synchronization profile and rules;
- selected Compose stacks;
- manual or automatic behavior.

The local and Git scopes must not overlap another link ambiguously. Separate sibling folders may use separate links, including different branches of the same repository.

At link creation you can:

- create the link without transfer;
- push Dockman's current state to Git;
- import Git into Dockman;
- reconcile automatically when both admissible trees are already identical.

Stacks may also be imported directly from Git. Importing to the stack root preserves the Git tree without adding an artificial branch-name parent. It creates a link scoped only to the imported stack/folder, not to every stack at root.

## Synchronization directions

### Git to Dockman

Can be manual or automatic. Dockman fetches, compares against the last accepted baseline, backs up affected files, transfers only allowed paths and optionally validates/deploys selected stacks.

### Dockman to Git

Always operator initiated. Dockman previews changes, validates that the preview is still current, commits the selected content and pushes it. Local edits are never silently published.

## Statuses

| State | Meaning |
|---|---|
| Synchronized | Local, Git and baseline agree |
| Local changes waiting | Admissible local changes can be previewed and pushed; not a conflict |
| Git changes waiting | Remote changes can be imported |
| Conflict | Both sides changed since the baseline and require a decision |
| Deleted locally | Git still contains an item; restore, delete from Git or stop synchronizing it |
| Orphaned | Git deleted an item that Dockman preserved locally |
| Partial / deployment failed | File transfer completed partly or deployment/rollback requires review |
| Error | Repository, filesystem, policy or deployment operation failed |

The Files and Monitor views expose these states without starting extra polling. Parent-folder indicators aggregate children but are not themselves stack actions.

## Editor coherence

When Git changes a file:

- a clean open editor is refreshed;
- an unsaved editor is never overwritten and the affected synchronization is blocked;
- clicking the Git status control does not trigger file-navigation events.

## Provider scope

Repository management and URL normalization currently target GitHub. Generic Git transport concepts are reusable, but GitLab and Bitbucket provider APIs are not yet supported for repository creation or provider-specific automation.
