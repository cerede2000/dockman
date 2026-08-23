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
            return {unavailable: 'Ctrl+V / Cmd+V works as usual. The menu entry needs a secure origin: serve Dockman over HTTPS, or mark this address secure in your browser settings.'}
        }
        return {unavailable: 'Ctrl+V / Cmd+V works as usual. This browser never lets a page read the clipboard by itself.'}
    }
    try {
        return {text: await clipboard.readText()}
    } catch {
        // Denied, dismissed, or the document was not focused when the menu
        // action ran. The shortcut is unaffected by any of them.
        return {unavailable: 'Ctrl+V / Cmd+V works as usual. Dockman was not allowed to read the clipboard this time.'}
    }
}
