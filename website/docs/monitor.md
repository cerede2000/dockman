---
title: Monitor and container details
sidebar_position: 4
---

# Monitor

Monitor replaces the former separate Stats and Containers navigation entries. Those legacy views remain reachable from Settings → Views when needed.

## Layouts and filters

- stack layout or flat container list;
- optional CPU/RAM graphs while retaining numeric values;
- sortable/searchable name column;
- clickable status counters;
- Ctrl-click multi-status filtering;
- update-available filtering;
- multi-select container and stack actions.

Stack/container border color follows real state instead of a fixed green accent. Nested stacks remain visible and parent status aggregates only the current discovered children.

## Container Details

The information button opens a bordered overlay with vertical tabs:

- Overview and live CPU/RAM/network/disk/process statistics;
- Logs with fixed controls;
- Process list;
- Networks, ports and endpoints;
- Mounts, environment, labels, security, resources and health checks;
- formatted/colorized Inspect with Copy;
- terminal Exec with shell/user selection, connect/disconnect, font size, clear and copy;
- container filesystem browser.

Exec detects available shells. If none exists, Dockman explains that no terminal can be opened. Exec into Dockman's own container and Host Shell remain disabled unless `DOCKMAN_ALLOW_SELF_EXEC=true`.

Stack actions temporarily block member-container actions to prevent contradictory operations.
