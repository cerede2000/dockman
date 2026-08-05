---
title: GitHub webhooks
sidebar_position: 4
---

# GitHub inbound webhooks

## Current Dockman state

Dockman provides manual checks, bounded polling and signed GitHub push webhooks. All three triggers reuse the same folder-link policy, backup, conflict, provisioning, validation, deployment and rollback pipeline. The webhook reduces normal synchronization latency while polling remains the safety net.

The absence of webhooks does not reduce synchronization correctness, but it adds latency and repeated fetches when repositories are idle.

## Functional comparison

| Capability | Dockman | Dockhand | Watchtower / WUD |
|---|---|---|---|
| Bidirectional stack-file synchronization | Yes, policy bounded and conflict aware | Git is primarily a deployment source | Not a Git-stack synchronizer |
| Manual and scheduled Git check | Yes | Yes | Not applicable |
| Signed inbound Git trigger | GitHub push, exact repository and branch | Yes | Watchtower exposes a token-protected update API; WUD uses event triggers rather than Git folder synchronization |
| Per-stack policy inside a linked tree | Yes | Deployment-oriented mapping | Not applicable |
| Backup, provisioning and file rollback | Yes | Deployment workflow differs | Container-image lifecycle only |
| Conflict comparison and partial resolution | Yes | Git-source deployment model avoids most bidirectional conflicts | Not applicable |

Dockhand's webhook implementation proves the usability gain, but Dockman must not copy its route mechanically. In the inspected implementation, branch filtering was not consistently enforced and a compatibility GET path accepted a secret in the query string. Dockman's target must remain POST-only and fail closed.

## Configure a GitHub webhook

1. Open **Settings → Git**.
2. On the GitHub repository row, open **Configure signed GitHub webhook**.
3. Enable the webhook and save.
4. Copy both the generated payload URL and the one-time secret before closing the dialog.
5. In GitHub, open **Repository settings → Webhooks → Add webhook**.
6. Use `application/json`, paste the secret, select only push events and keep SSL verification enabled.
7. Send GitHub's ping/test delivery, then push to the exact branch configured in Dockman.

The secret is returned only at creation or rotation. Losing it is harmless: rotate it in Dockman and replace it in GitHub.

## Security and execution model

- one unguessable endpoint and independent encrypted secret per repository;
- POST and `application/json` only, with a 1 MiB request ceiling;
- constant-time validation of `X-Hub-Signature-256`;
- mandatory, bounded `X-GitHub-Delivery` replay protection;
- normalized repository identity and exact configured branch/ref validation;
- deleted branches and unrelated GitHub events are ignored;
- a bounded queue coalesces bursts by repository and returns quickly;
- webhook receipt never overrides folder-link, per-stack, auto-deploy or rollback policy;
- no request payload or secret is retained; only bounded delivery identifiers and the last result are stored.

Webhook receipt must never imply deployment permission. The existing folder-link, stack-selection, auto-deploy and rollback settings remain authoritative.

GitLab and Bitbucket signature adapters remain future provider-specific work. Do not point those services at the GitHub endpoint: their signature and event semantics differ.

## Other remaining Git gaps

The audit also keeps the following items explicit rather than implying that “Git sync” covers every Git workflow:

- GitLab and Bitbucket repository-creation/provider APIs;
- GitHub App or OAuth installation credentials as an alternative to PAT/SSH credentials;
- pull-request/protected-branch publication mode instead of direct push;
- optional commit-signature verification and signed Dockman commits;
- Git LFS and submodule policy (currently neither is a synchronized stack-content mechanism);
- role-based approval/audit identity for shared multi-user installations;
- backup/export rehearsal for both Git and notification encryption master keys.

Automatic Dockman-to-Git publication is intentionally **not** treated as a missing feature. Local-to-Git remains operator initiated so an editor, generated file or compromised container cannot silently publish repository changes. A future PR workflow could safely reduce that manual effort without turning the local filesystem into an uncontrolled writer to the protected branch.
