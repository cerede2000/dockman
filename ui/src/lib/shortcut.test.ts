import {afterEach, describe, expect, it} from 'vitest'
import {isAltLetter, ownsEditorShortcut} from './shortcut.ts'

const key = (init: Partial<KeyboardEvent>) =>
    ({altKey: false, ctrlKey: false, metaKey: false, code: '', key: '', ...init}) as KeyboardEvent

describe('isAltLetter', () => {
    it('matches the plain Windows/Linux QWERTY case', () => {
        expect(isAltLetter(key({altKey: true, code: 'KeyR', key: 'r'}), 'r')).toBe(true)
    })

    // The reason this helper exists. macOS composes Option+letter into a
    // symbol, so every `event.key === 'r'` shortcut was dead on this Mac while
    // its tooltip still advertised ALT + R.
    it('matches on macOS, where Option+R types the registered sign', () => {
        expect(isAltLetter(key({altKey: true, code: 'KeyR', key: '®'}), 'r')).toBe(true)
        expect(isAltLetter(key({altKey: true, code: 'KeyS', key: 'ß'}), 's')).toBe(true)
        expect(isAltLetter(key({altKey: true, code: 'KeyA', key: 'å'}), 'a')).toBe(true)
    })

    // The mirror failure: code names the physical key by its US label, so the
    // key printed A on an AZERTY board reports KeyQ.
    it('matches on AZERTY, where the A key reports KeyQ', () => {
        expect(isAltLetter(key({altKey: true, code: 'KeyQ', key: 'a'}), 'a')).toBe(true)
    })

    it('ignores a different letter', () => {
        expect(isAltLetter(key({altKey: true, code: 'KeyT', key: 't'}), 'r')).toBe(false)
    })

    it('ignores the letter without Alt', () => {
        expect(isAltLetter(key({code: 'KeyR', key: 'r'}), 'r')).toBe(false)
    })

    // AltGr reports ctrlKey+altKey on Windows. Matching on code alone would
    // fire "Edit dockman.yaml" every time a French layout types a euro sign.
    it('ignores AltGr compositions', () => {
        expect(isAltLetter(key({altKey: true, ctrlKey: true, code: 'KeyE', key: '€'}), 'e')).toBe(false)
    })

    it('ignores Cmd+Alt, which belongs to the browser', () => {
        expect(isAltLetter(key({altKey: true, metaKey: true, code: 'KeyR', key: 'r'}), 'r')).toBe(false)
    })
})

describe('ownsEditorShortcut', () => {
    afterEach(() => {
        document.body.innerHTML = ''
    })

    it('gives the shortcut to the main pane when nothing is focused', () => {
        expect(ownsEditorShortcut(0)).toBe(true)
        expect(ownsEditorShortcut(1)).toBe(false)
    })

    // In split view both panes listened on window and both acted: the second
    // navigate() rebuilt the URL from the same stale search string and put the
    // first pane's tab back where it was.
    it('gives the shortcut to the focused pane alone', () => {
        document.body.innerHTML = `
            <div data-editor-track="0"><textarea id="main"></textarea></div>
            <div data-editor-track="1"><textarea id="split"></textarea></div>`

        document.querySelector<HTMLTextAreaElement>('#split')!.focus()
        expect(ownsEditorShortcut(1)).toBe(true)
        expect(ownsEditorShortcut(0)).toBe(false)

        document.querySelector<HTMLTextAreaElement>('#main')!.focus()
        expect(ownsEditorShortcut(0)).toBe(true)
        expect(ownsEditorShortcut(1)).toBe(false)
    })

    it('falls back to the main pane when focus sits outside both', () => {
        document.body.innerHTML = `
            <input id="tree"/>
            <div data-editor-track="0"></div>
            <div data-editor-track="1"></div>`

        document.querySelector<HTMLInputElement>('#tree')!.focus()
        expect(ownsEditorShortcut(0)).toBe(true)
        expect(ownsEditorShortcut(1)).toBe(false)
    })
})
