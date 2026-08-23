// Keyboard-shortcut matching that survives the two ways a layout can lie.

// isAltLetter matches an Alt+<letter> shortcut whatever the keyboard layout.
//
// Matching on event.key alone is wrong on macOS: Option+R yields "®",
// Option+S "ß", Option+A "å" - never the letter itself, so a
// key-based shortcut simply never fires there.
//
// Matching on event.code alone is wrong on every non-US layout: code names the
// physical key by its US label, so on AZERTY the key printed A reports "KeyQ".
//
// Accepting either one fires on the key the user actually pressed in both
// cases. It can only widen the match, never narrow it - except for the guard
// below, which is the point of having one.
//
// AltGr sets ctrlKey AND altKey on Windows, so on a French layout AltGr+E
// (typing "€") carries code "KeyE". Without the guard, code matching would
// turn every AltGr composition into a fired shortcut. Ctrl+Alt+<letter> and
// Cmd+Alt+<letter> therefore no longer trigger anything; nothing advertises
// them, and the browser owns several of them already.
export function isAltLetter(event: KeyboardEvent, letter: string): boolean {
    if (!event.altKey || event.ctrlKey || event.metaKey) return false
    return event.code === `Key${letter.toUpperCase()}`
        || event.key.toLowerCase() === letter.toLowerCase()
}

// ownsEditorShortcut reports whether the pane numbered `track` is the one a
// global editor shortcut should act on.
//
// Both panes of a split view listen on window, so one keypress reached both.
// Each rebuilt its URL from the SAME search string captured at the last render,
// and the second navigate() silently reverted what the first had just changed:
// pressing Alt+Z in split view moved the split pane and put the main pane's tab
// straight back. Only the pane holding focus acts now.
//
// With focus outside both panes - the file tree, a dialog, the document body -
// the main pane acts, which is exactly the behaviour of a non-split window.
export function ownsEditorShortcut(track: number): boolean {
    const focused = document.activeElement?.closest('[data-editor-track]')
    const owner = focused?.getAttribute('data-editor-track')
    return (owner === null || owner === undefined ? 0 : Number(owner)) === track
}
