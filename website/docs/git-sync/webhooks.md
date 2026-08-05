---
title: Webhook gap analysis
sidebar_position: 4
---

# Git webhook gap analysis

## Current Dockman state

Dockman currently provides manual checks and per-folder-link Git-to-Dockman polling, with a minimum five-minute interval. Both paths reuse the same policy, backup, conflict, provisioning, validation, deployment and rollback pipeline. There is no public inbound Git webhook endpoint yet.

The absence of webhooks does not reduce synchronization correctness, but it adds latency and repeated fetches when repositories are idle.

## Functional comparison

| Capability | Dockman | Dockhand | Watchtower / WUD |
|---|---|---|---|
| Bidirectional stack-file synchronization | Yes, policy bounded and conflict aware | Git is primarily a deployment source | Not a Git-stack synchronizer |
| Manual and scheduled Git check | Yes | Yes | Not applicable |
| Signed inbound Git trigger | Not yet | Yes | Watchtower exposes a token-protected update API; WUD uses event triggers rather than Git folder synchronization |
| Per-stack policy inside a linked tree | Yes | Deployment-oriented mapping | Not applicable |
| Backup, provisioning and file rollback | Yes | Deployment workflow differs | Container-image lifecycle only |
| Conflict comparison and partial resolution | Yes | Git-source deployment model avoids most bidirectional conflicts | Not applicable |

Dockhand's webhook implementation proves the usability gain, but Dockman must not copy its route mechanically. In the inspected implementation, branch filtering was not consistently enforced and a compatibility GET path accepted a secret in the query string. Dockman's target must remain POST-only and fail closed.

## Target design for Dockman

The webhook lot should add provider adapters around one internal trigger—not a second synchronization engine:

1. expose a unique endpoint per repository or folder link;
2. require a separately generated webhook secret stored encrypted;
3. accept POST only and cap the request body;
4. verify GitHub `X-Hub-Signature-256` in constant time, with equivalent GitLab and Bitbucket adapters later;
5. verify event type, normalized repository identity and exact configured branch/ref;
6. use the provider delivery ID as a bounded replay cache key;
7. rate-limit and coalesce bursts for the same repository;
8. return `202 Accepted` quickly and enqueue the existing normal synchronization pipeline;
9. record accepted, ignored and rejected deliveries in the activity history without storing payloads or secrets;
10. preserve polling as a configurable safety net.

Webhook receipt must never imply deployment permission. The existing folder-link, stack-selection, auto-deploy and rollback settings remain authoritative.

## Recommended sequence

1. GitHub push webhook, exact branch filtering, replay protection and audit.
2. UI setup instructions and a test-delivery endpoint.
3. Coalescing queue plus polling fallback controls.
4. GitLab and Bitbucket signature adapters after repository-provider abstraction.

This work is deliberately separate from outbound update notifications: inbound Git webhooks expand Dockman's attack surface and require their own threat model and regression test plan.

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
