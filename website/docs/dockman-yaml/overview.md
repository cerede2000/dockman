---
title: Overview
sidebar_position: 1
---

# Dockman UI configuration

Dockman stores one host-specific UI configuration below:

```text
DOCKMAN_YAML_PATH/<host>.dockman.yml
```

The container default is `/config/dockyaml`. Override the directory with:

```yaml
environment:
  DOCKMAN_YAML_PATH: /config/custom-dockyaml
```

Open the configuration from the YAML action in the Files toolbar. It controls aliases, default view, table sorting/limits, Monitor layout and graph visibility, Compose/editor preferences and custom tools.

This configuration is separate from Git synchronization policies and must remain in the persistent `/config` backup.
