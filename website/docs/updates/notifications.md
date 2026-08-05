---
title: Notifications
sidebar_position: 3
---

# SMTP notifications

SMTP is the currently implemented notification channel. Configuration is per Docker host and stored encrypted in Dockman's database.

Supported transport modes:

- STARTTLS, normally port 587;
- implicit TLS, normally port 465;
- unencrypted SMTP for explicitly trusted test networks.

TLS verification remains enabled. Private relay CAs can be mounted with `DOCKMAN_SMTP_CA_FILE`.

## Events

Separate choices control notifications for:

- updates/new versions detected;
- successful automatic executions;
- failed executions and rollbacks;
- scan or delivery errors.

Unchanged scan results are fingerprinted to avoid repeated identical messages. Delivery history is bounded and visible in the Updates view.

## Deliverability

Dockman emits standards-compliant messages, but final inbox placement also depends on the relay and recipient reputation systems. Use an authenticated relay with aligned SPF, DKIM and DMARC. A valid private certificate alone does not improve sender reputation; it only secures the Dockman-to-relay connection.

## Test procedure

1. Save the SMTP configuration.
2. Send a test message.
3. Verify sender, recipient, transport security and inbox placement.
4. Trigger an enrolled update check and a protected execution separately.
5. Confirm both delivery-history entries and expected messages.

## Planned channels

Webhook, Gotify, ntfy, Discord and Apprise are not implemented yet. They belong to the next notification expansion and must reuse the encrypted credential vault, bounded delivery history, deduplication and per-event routing rather than introduce separate background loops.
