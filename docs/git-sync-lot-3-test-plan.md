# Git sync lot 3 — stack links and manual transfers

This lot links a complete Dockman stacks folder to either a folder in a shared Git repository or the root of a dedicated repository. Every stack subfolder below that root is preserved automatically. Unit-level links remain supported. The lot adds previews and explicit transfers in both directions. It does not poll Git, deploy a stack, restart containers, delete files missing from the source, or resolve Git conflicts automatically.

## Prerequisites

- Start the dedicated lot 3 image with the same persistent `/config` and Git master-key secret used for lots 0–2.
- Keep `DOCKMAN_GIT_SYNC=true` and `DOCKMAN_GIT_MASTER_KEY_FILE=/run/secrets/dockman_git_key`.
- Use the disposable GitHub repository created for the previous tests.
- Have one harmless compose stack available. Do not use a production stack for the first import test.

## 1. Stack discovery and link creation

1. Open **Settings → Git**.
2. Confirm that **Stack links** appears between repositories and credentials.
3. Click **Link stack**.
4. Select **All stacks — host / compose** to associate the complete stacks root in one operation.
5. Select either a folder in a shared repository, such as `stacks`, or the root of a dedicated repository.
6. Create the link.

Expected:

- local and connected SSH hosts are supported;
- all nested compose stacks are detected and summarized on the link row;
- their relative folder structure is preserved in Git;
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

Large folders are inventoried file by file. Dockman hashes and transfers content through a reusable 64 KiB buffer instead of retaining all file contents in memory. The safety ceilings are now 20,000 files and 2 GiB of selected content per linked folder. Files over 100 MiB are reported and skipped individually instead of aborting the complete preview; these bounds limit accidental work, not Dockman's RAM usage.

## 3. Synchronization policy and exclusions

1. Open the synchronization policy with the tuning icon on the folder link.
2. Keep **Compose configuration** selected and preview a folder containing YAML/JSON configuration, an image, a log, and a file over 100 MiB.
3. Add `scripts/**` to the includes, then add `**/data/**` and `*.log` to the exclusions.
4. Save and preview again.
5. Optionally create a `.dockmanignore` at the linked folder root with another disposable folder pattern.
6. In the preview, use the exclusion button on an included file, then exclude the parent folder of another entry.

Expected:

- the default profile selects configuration and deployment files, not arbitrary generated/binary content;
- non-selected types appear as `skipped type`, large files as `skipped oversized`, and explicit rules as `skipped excluded`, with path and size visible;
- the server log records the exact path, size, and limit for every oversized file;
- custom includes add matching files and exclusions take priority;
- direct file/folder exclusions are saved permanently and the preview refreshes immediately;
- exact exclusions remain exact for names containing glob characters such as `[` or `*`;
- `.dockmanignore` exclusions apply without changing the saved policy;
- Compose files remain protected even if a broad exclusion matches them;
- skipped files are never read into memory, copied, committed, or restored;
- switching to **All regular files** includes ordinary files while retaining the special-file, sensitive-file and 100 MiB protections.

## 4. Secret filtering

1. Add a `.env` file with a fake value to the test stack.
2. Reopen the stack → Git preview.

Expected:

- `.env`, private-key formats, and secret/credential-named files are skipped by default;
- their contents are never displayed;
- enabling the one-shot sensitive mode requires typing exactly `INCLUDE SENSITIVE FILES`;
- canceling the dialog forgets that opt-in.

Keep the sensitive mode disabled for the remaining test unless the disposable repository is private and contains fake data only.

## 5. Manual export

1. Reopen the stack → Git preview.
2. Enter a one-line commit message and confirm that typing remains immediate even with a large preview.
3. Click **Commit and push**.
4. Inspect the disposable repository on GitHub.

Expected:

- changed non-sensitive files are copied into the configured repository folder;
- Dockman creates one commit and pushes it;
- files already present only in Git are preserved;
- the `.env` file is absent unless it was explicitly included;
- no stack or container action occurs;
- a second preview reports no changes.

## 6. Manual import and backup

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

## 7. Repository-state guards

Create each state below in the disposable repository and retry a transfer:

- uncommitted local Git workspace change;
- remote commit not yet pulled;
- diverged local and remote history.

Expected: transfer is refused with a clear instruction to pull or resolve the repository first. Existing stack files remain unchanged.

## 8. SSH host smoke test

Link a non-production stack on a connected SSH host, preview in both directions, export one harmless file, then import one harmless change.

Expected: behavior matches the local host, paths remain within the configured alias, and no unrelated remote file changes.

## 9. Cleanup

Delete the link from Dockman.

Expected: only the relationship is removed. The stack, repository, Git history, and backup files remain intact. Repository deletion becomes available once no link references it.
