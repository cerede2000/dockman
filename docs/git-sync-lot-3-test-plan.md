# Git sync lot 3 — stack links and manual transfers

This lot links a Dockman stack folder to a managed Git repository folder. It adds previews and explicit transfers in both directions. It does not poll Git, deploy a stack, restart containers, delete files missing from the source, or resolve Git conflicts automatically.

## Prerequisites

- Start the dedicated lot 3 image with the same persistent `/config` and Git master-key secret used for lots 0–2.
- Keep `DOCKMAN_GIT_SYNC=true` and `DOCKMAN_GIT_MASTER_KEY_FILE=/run/secrets/dockman_git_key`.
- Use the disposable GitHub repository created for the previous tests.
- Have one harmless compose stack available. Do not use a production stack for the first import test.

## 1. Stack discovery and link creation

1. Open **Settings → Git**.
2. Confirm that **Stack links** appears between repositories and credentials.
3. Click **Link stack**.
4. Select the test repository and a detected stack.
5. Enter a repository folder such as `stacks/dockman-test`, then create the link.

Expected:

- local and connected SSH hosts are supported;
- detected compose files are shown on the link row;
- creating the link copies nothing;
- the same host/stack cannot be linked twice;
- absolute paths, `..`, and `.git` paths are refused.

Also test a repository-first case by entering a valid stack path that does not exist yet. The link may be created so a later import can create it.

## 2. Preview stack → Git

1. Add a harmless text file to the test stack folder.
2. Click the upload icon on its link.
3. Inspect the preview without confirming the transfer.

Expected:

- new and modified files are listed without displaying their contents;
- unchanged files are counted but omitted from the list;
- the dialog states that deletions are not propagated;
- closing the dialog changes neither Git nor the stack.

## 3. Secret filtering

1. Add a `.env` file with a fake value to the test stack.
2. Reopen the stack → Git preview.

Expected:

- `.env`, private-key formats, and secret/credential-named files are skipped by default;
- their contents are never displayed;
- enabling the one-shot sensitive mode requires typing exactly `INCLUDE SENSITIVE FILES`;
- canceling the dialog forgets that opt-in.

Keep the sensitive mode disabled for the remaining test unless the disposable repository is private and contains fake data only.

## 4. Manual export

1. Reopen the stack → Git preview.
2. Optionally enter a one-line commit message.
3. Click **Commit and push**.
4. Inspect the disposable repository on GitHub.

Expected:

- changed non-sensitive files are copied into the configured repository folder;
- Dockman creates one commit and pushes it;
- files already present only in Git are preserved;
- the `.env` file is absent unless it was explicitly included;
- no stack or container action occurs;
- a second preview reports no changes.

## 5. Manual import and backup

1. Change `compose.yaml` in GitHub, keeping it valid YAML, and add a harmless text file.
2. In Dockman, fetch then pull the repository.
3. Click the download icon on the stack link.
4. Review the preview and click **Backup and import**.

Expected:

- invalid compose YAML is rejected before any stack file is written;
- a backup identifier is reported;
- overwritten files are stored under `/config/git/backups/<binding-id>/` in a mode-0600 tar.gz;
- the archive includes a manifest identifying files created by the import;
- changed and new files appear in the stack folder;
- stack-only files are preserved;
- Dockman does not recompose, deploy, or restart anything.

Then open the stack in Dockman's Files view and confirm that normal editing still works.

## 6. Repository-state guards

Create each state below in the disposable repository and retry a transfer:

- uncommitted local Git workspace change;
- remote commit not yet pulled;
- diverged local and remote history.

Expected: transfer is refused with a clear instruction to pull or resolve the repository first. Existing stack files remain unchanged.

## 7. SSH host smoke test

Link a non-production stack on a connected SSH host, preview in both directions, export one harmless file, then import one harmless change.

Expected: behavior matches the local host, paths remain within the configured alias, and no unrelated remote file changes.

## 8. Cleanup

Delete the link from Dockman.

Expected: only the relationship is removed. The stack, repository, Git history, and backup files remain intact. Repository deletion becomes available once no link references it.
