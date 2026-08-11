// jsdom always reports a visible document and gives no way to hide it, so the
// tests shadow the prototype getter with an own property and fire the event the
// browser would fire. Everything under test reads `document.visibilityState`
// and listens for `visibilitychange`, which is exactly what this drives.
//
// Deliberately free of React's `act`: this is also used by plain-function
// suites. Hook tests wrap the call themselves.
export function setDocumentVisibility(state: 'visible' | 'hidden') {
    Object.defineProperty(document, 'visibilityState', {
        configurable: true,
        get: () => state,
    })
    document.dispatchEvent(new Event('visibilitychange'))
}

// Restores jsdom's own getter. Call it from an afterEach in any suite that
// hides the document: the property is defined on the shared `document`.
export function restoreDocumentVisibility() {
    // Reflect rather than `delete`: the own property shadows a readonly one.
    Reflect.deleteProperty(document, 'visibilityState')
}
