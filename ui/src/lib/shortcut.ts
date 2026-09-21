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

// altDigit returns N for an Alt+<digit N> shortcut (1 to 9), or null.
//
// Reading the digit from event.key - the character the keystroke produces -
// only works on layouts where the digit row types digits. On AZERTY the key
// labelled 1 types "&", on macOS Option+1 types "¡": the sidebar's Alt+1..9
// never fired on either, while every tooltip advertised them. event.code names
// the physical key ("Digit1" on every layout), so it fires everywhere.
//
// Except where a keystroke may be typing. On a French Mac, Option+5 is how "{"
// is typed, and it carries code "Digit5": matching the physical key inside the
// editor would navigate away mid-word. So in a text field only a keystroke
// that yields the digit itself counts - exactly the old behaviour there, which
// is why nobody typing loses a character.
//
// Ctrl and Meta stay excluded like in isAltLetter (AltGr reports Ctrl+Alt on
// Windows: AltGr+2 types "~" on AZERTY), and Shift and auto-repeat as before.
export function altDigit(event: KeyboardEvent): number | null {
    if (!event.altKey || event.ctrlKey || event.metaKey || event.shiftKey || event.repeat) return null
    const typed = /^[1-9]$/.test(event.key) ? Number(event.key) : null
    if (typed !== null || isTextEntry(event.target)) return typed
    const physical = /^Digit([1-9])$/.exec(event.code)
    return physical ? Number(physical[1]) : null
}

// isTextEntry reports whether a key event lands where keystrokes type text:
// form fields, and contenteditable hosts. Monaco and xterm both type through a
// hidden textarea, so they count too.
export function isTextEntry(target: EventTarget | null): boolean {
    if (!(target instanceof Element)) return false
    return target.closest('input, textarea, select, [contenteditable]:not([contenteditable="false"])') !== null
}

// A shortcut handled by a page-wide listener can also match a narrower one
// registered by the view underneath: Alt+1 is both "open the first sidebar
// entry" and the Files view's own "toggle the file bar". The wider listener
// runs first (it sits on document, the view's on window) and claims the event
// when it acts on it, so the view knows the keystroke is already spent - the
// page is about to change under it.
const claimedShortcuts = new WeakSet<Event>()

export function claimShortcut(event: Event): void {
    claimedShortcuts.add(event)
}

export function isShortcutClaimed(event: Event): boolean {
    return claimedShortcuts.has(event)
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
