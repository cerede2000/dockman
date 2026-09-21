---
sidebar_position: 6
---

# Default View

`defaultView` sets the view Dockman opens on - at its root address or from the logo - for every user of this host.

```yaml title=".dockman.yml"
defaultView: monitor
```

Accepted values: `files`, `monitor`, `stats`, `containers`, `updates`, `images`, `volumes`, `networks`, `cleaner`. Any other value, or no value, opens **Files**.

A landing page chosen in **Settings → Views** takes precedence in the browser it was chosen in; **Host default** there returns to this value. See [Navigation](../navigation.md).
