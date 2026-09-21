---
title: Navigation and landing page
sidebar_position: 5
---

# Navigation

The sidebar, its keyboard shortcuts and the page a host opens on are set in **Settings → Views**. They are preferences of the browser you set them in, like the other options of that page: they change nothing on the server and nothing for other users.

## Sidebar order

Each entry can be moved up or down; **Reset order** restores the default one. A move always changes the sidebar: an entry jumps over the hidden views in its way to trade places with the next visible one. The legacy **Stats** and **Containers** views, which Monitor replaces, are hidden by default: switch **Show in sidebar** to bring either back, or open them from the same page without adding them.

A view added by a later Dockman release appears at the end of a customised sidebar.

## Keyboard shortcuts

`Alt` + `1` to `9` opens the sidebar entry at that position, as displayed: the numbers follow your order and skip the hidden views. Each entry's tooltip, and its row in Settings → Views, shows its shortcut.

The shortcuts follow the physical keys of the digit row, so they work whatever the keyboard layout: on AZERTY the key marked `1`/`&` is `Alt` + `1`, and on macOS `Option` + `1` works although it types `¡`. On Linux, Firefox binds these keys to its own tabs; Dockman takes them first.

Inside a text field, the editor or a terminal, only a keystroke that types the digit itself opens an entry: `Option` + `5` is how `{` is typed on a French Mac, and it types `{`.

In the Files view, `Alt` + `1` toggles the file bar when the first sidebar entry is Files. When another entry comes first, `Alt` + `1` opens that entry instead.

## Landing page

**Open Dockman on** chooses the view Dockman opens on, at its root address or from the logo. Switching hosts keeps the view you are in. Its **Server default** follows [`defaultView`](dockman-yaml/default-view.md) in the host's dockman.yml, then Files; a choice made here takes precedence in this browser.
