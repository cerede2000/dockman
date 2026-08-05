---
title: Notifications
sidebar_position: 3
---

# Notifications

Notification configuration is per Docker host. SMTP is an ordinary named channel, just like the HTTP providers: several SMTP relays, recipients or mixed providers can coexist. Passwords, endpoint URLs, tokens and API keys are encrypted in Dockman's database and are never returned by the API.

## Supported channels

- SMTP;
- generic JSON webhook;
- Gotify;
- ntfy;
- Discord webhooks;
- Apprise API.

Every destination is isolated. A failing scan destination is recorded and remains eligible at the next scheduled scan without preventing delivery to other configured channels or failing the update cycle itself. Execution delivery failures remain visible in history and can be tested explicitly; Dockman does not create a hidden retry loop.

The HTTP transports use POST, bounded payloads/responses and a ten-second timeout. Redirects are refused. HTTPS is required by default; private-network or plain-HTTP endpoints require an explicit opt-in. Loopback, link-local and metadata destinations remain blocked.

## SMTP

Supported transport modes:

- STARTTLS, normally port 587;
- implicit TLS, normally port 465;
- unencrypted SMTP for explicitly trusted test networks.

TLS verification remains enabled. Private relay CAs can be mounted with `DOCKMAN_SMTP_CA_FILE`.

## Event subscriptions

Every channel independently subscribes to any combination of:

- image updates available, successful updates, failures and rollbacks;
- Docker cleaner completion or failure;
- background image-build completion or failure;
- Git synchronization success/failure, conflicts and newly discovered stacks;
- Git auto-deployment success/failure and rollback;
- unexpected container restart, OOM termination and unhealthy transition.

Container lifecycle events are observed by Dockman's server through the already shared Docker event stream. They remain active when no browser is open and do not add a polling loop. Explicit operator stops and ordinary starts are not reported as incidents.

Unchanged scan results are fingerprinted independently for every channel to avoid repeated identical messages. A failed channel remains eligible for retry while a successful channel is not duplicated. Delivery history is bounded and visible in the Updates view.

## Deliverability

Dockman emits standards-compliant messages, but final inbox placement also depends on the relay and recipient reputation systems. Use an authenticated relay with aligned SPF, DKIM and DMARC. A valid private certificate alone does not improve sender reputation; it only secures the Dockman-to-relay connection.

## Test procedure

1. Open **Updates → Notifications**, add an SMTP or HTTP channel and choose its subscribed events.
2. Send a test message from that channel.
3. Verify sender, recipient, transport security and inbox placement.
4. Trigger an enrolled update check and a protected execution separately.
5. Confirm both delivery-history entries and expected messages.

### Gotify example

1. In Gotify, create an application and copy its application token.
2. In **Updates → Channels**, choose **Gotify**.
3. Enter the server base URL, without `/message`, and the token.
4. For an HTTPS service on a public address, leave private/HTTP access disabled. Enable it only for a trusted LAN address or HTTP-only installation.
5. Save, then use **Send test**.

### Generic webhook envelope

```json
{
  "kind": "build.success",
  "host": "local",
  "title": "Dockman updates on local - 2 updated - 0 failed",
  "message": "Dockman automatic image update summary…",
  "severity": "success",
  "timestamp": "2026-08-05T12:00:00Z"
}
```

The generic webhook supports an optional bearer token or HTTP Basic credentials. Apprise expects its persistent API URL and configuration key; provider-specific destination URLs remain managed by Apprise itself. Producers enqueue into one bounded worker, so a slow notification endpoint never blocks the Docker, build, cleaner or Git action and cannot grow memory without a fixed ceiling.
