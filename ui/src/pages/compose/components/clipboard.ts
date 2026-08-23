export type ClipboardRead =
    | { text: string }
    | { unavailable: string }

// Reading the clipboard is far more restricted than writing it, and both
// restrictions are invisible until you try:
//
//   - navigator.clipboard only exists in a SECURE context. Dockman reached over
//     a plain-HTTP LAN address - the common homelab setup - has no clipboard
//     object at all.
//   - readText is not exposed to page scripts in Firefox, only to extensions.
//
// Neither is something Dockman can work around, so the only useful thing to do
// is say which one happened and point at the shortcut, which always works
// because the browser performs that paste itself.
export async function readClipboardText(clipboard?: Clipboard): Promise<ClipboardRead> {
    if (!clipboard || typeof clipboard.readText !== 'function') {
        if (typeof window !== 'undefined' && !window.isSecureContext) {
            return {unavailable: 'Ctrl+V / Cmd+V works, and so does Shift+right-click for the browser\u2019s own menu. This entry needs a secure origin: serve Dockman over HTTPS.'}
        }
        return {unavailable: 'Ctrl+V / Cmd+V works, and so does Shift+right-click for the browser\u2019s own menu. This browser never lets a page read the clipboard by itself.'}
    }
    try {
        return {text: await clipboard.readText()}
    } catch {
        // Denied, dismissed, or the document was not focused when the menu
        // action ran. The shortcut is unaffected by any of them.
        return {unavailable: 'Ctrl+V / Cmd+V works, and so does Shift+right-click for the browser\u2019s own menu. Dockman was not allowed to read the clipboard this time.'}
    }
}

// canReadClipboard reports whether offering a Paste entry can lead anywhere.
//
// Both blocking conditions are structural and known before the click: outside a
// secure context navigator.clipboard does not exist at all, and Firefox never
// exposes readText to page scripts. Offering an entry that can only ever answer
// with an explanation is worse than not offering one - which is exactly what
// Monaco does by default, and what this editor did before the entry existed.
//
// A permission that is granted-then-denied, or a document that is not focused,
// is NOT covered here: those cannot be known in advance, and readClipboardText
// reports them when they happen.
export function canReadClipboard(clipboard?: Clipboard): boolean {
    return Boolean(clipboard && typeof clipboard.readText === 'function')
}
