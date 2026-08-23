import {afterEach, describe, expect, it} from 'vitest'
import {readClipboardText} from './clipboard.ts'

// jsdom does not define isSecureContext at all, which is itself the shape a
// plain-HTTP page presents.
const secureContext = (secure: boolean) =>
    Object.defineProperty(window, 'isSecureContext', {configurable: true, get: () => secure})

afterEach(() => Reflect.deleteProperty(window, 'isSecureContext'))

describe('readClipboardText', () => {
    it('returns what the clipboard holds', async () => {
        const clipboard = {readText: async () => 'services: {}\n'} as unknown as Clipboard
        expect(await readClipboardText(clipboard)).toEqual({text: 'services: {}\n'})
    })

    // The common homelab setup: Dockman on a plain-HTTP LAN address has no
    // clipboard object at all.
    it('names the missing secure context rather than failing silently', async () => {
        secureContext(false)
        const result = await readClipboardText(undefined)
        expect(result).toHaveProperty('unavailable')
        expect((result as {unavailable: string}).unavailable).toContain('secure origin')
        expect((result as {unavailable: string}).unavailable).toContain('Shift+right-click')
    })

    // Firefox exposes readText to extensions only, on HTTPS as well.
    it('names the browser restriction when the context is already secure', async () => {
        secureContext(true)
        const result = await readClipboardText({} as Clipboard)
        expect((result as {unavailable: string}).unavailable).toContain('never lets a page read the clipboard')
    })

    it('survives a denied or dismissed permission', async () => {
        secureContext(true)
        const clipboard = {readText: async () => { throw new Error('NotAllowedError') }} as unknown as Clipboard
        const result = await readClipboardText(clipboard)
        expect((result as {unavailable: string}).unavailable).toContain('not allowed')
    })
})
