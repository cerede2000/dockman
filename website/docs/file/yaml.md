---
title: YAML and Compose navigation
sidebar_position: 4
---

# YAML files

Press `Alt + L` to format the current YAML document.

For Compose manifests, the editor toolbar exposes a quick outline for:

- services and each service name;
- networks;
- volumes;
- secrets.

Only real YAML keys are indexed; commented examples such as `# services:` are ignored. Selecting an item reveals and scrolls its source line to the top of the editor viewport where Monaco permits it.

Formatting changes text and should be reviewed before saving. Git synchronization treats a saved formatting-only change like any other admissible local change.
