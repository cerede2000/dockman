// Monaco shows its own right-click menu by calling preventDefault on the
// contextmenu event. Its Paste entry cannot work where the page is not allowed
// to read the clipboard - a plain-HTTP LAN address, the ordinary homelab setup
// - while the BROWSER's own menu pastes there without any permission, because
// the browser performs the paste itself.
//
// Keeping both is a matter of who sees the event. Held with Shift, it is
// stopped before it ever reaches Monaco: Monaco never calls preventDefault, and
// the browser shows its native menu. Plain right-click is untouched, so every
// Monaco action stays exactly where it was.
export function browserMenuRequested(event: {shiftKey: boolean}): boolean {
    return event.shiftKey
}

export function letBrowserMenuThrough(event: MouseEvent): void {
    if (!browserMenuRequested(event)) return
    // stopPropagation, never preventDefault: the point is that nobody handles
    // this event, so the browser falls back to its own menu.
    event.stopPropagation()
}
