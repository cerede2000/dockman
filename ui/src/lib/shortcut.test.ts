import {afterEach, describe, expect, it} from 'vitest'
import {altDigit, claimShortcut, isAltLetter, isShortcutClaimed, isTextEntry, ownsEditorShortcut} from './shortcut.ts'

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

describe('altDigit', () => {
    const digit = (init: Partial<KeyboardEvent>, target: EventTarget | null = document.body) =>
        ({...key({altKey: true, shiftKey: false, repeat: false, ...init}), target}) as unknown as KeyboardEvent

    it('reads the plain QWERTY case', () => {
        expect(altDigit(digit({code: 'Digit2', key: '2'}))).toBe(2)
    })

    // Reported upstream (RA341/dockman#229): the sidebar's Alt+1..9 read the
    // digit from event.key, and on these layouts the key never is a digit.
    it('reads AZERTY, where the digit row types symbols', () => {
        expect(altDigit(digit({code: 'Digit1', key: '&'}))).toBe(1)
        expect(altDigit(digit({code: 'Digit2', key: 'é'}))).toBe(2)
        expect(altDigit(digit({code: 'Digit9', key: 'ç'}))).toBe(9)
    })

    it('reads macOS, where Option+digit composes a sign', () => {
        expect(altDigit(digit({code: 'Digit1', key: '¡'}))).toBe(1)
        expect(altDigit(digit({code: 'Digit2', key: '™'}))).toBe(2)
    })

    // On a French Mac, Option+5 is how "{" is typed. In a text field only a
    // keystroke that yields the digit itself may count, or typing a brace in
    // the editor would leave the page.
    it('leaves composed characters to text fields', () => {
        const editor = document.createElement('textarea')
        expect(altDigit(digit({code: 'Digit5', key: '{'}, editor))).toBeNull()
        expect(altDigit(digit({code: 'Digit1', key: '&'}, editor))).toBeNull()
    })

    // ...while a keystroke that does produce the digit behaves as it always did.
    it('still reads a typed digit inside a text field', () => {
        const editor = document.createElement('textarea')
        expect(altDigit(digit({code: 'Digit3', key: '3'}, editor))).toBe(3)
    })

    // AltGr reports Ctrl+Alt on Windows: AltGr+2 types "~" on AZERTY.
    it('never fires on AltGr, Cmd, Shift or auto-repeat', () => {
        expect(altDigit(digit({ctrlKey: true, code: 'Digit2', key: '~'}))).toBeNull()
        expect(altDigit(digit({metaKey: true, code: 'Digit2', key: '2'}))).toBeNull()
        expect(altDigit(digit({shiftKey: true, code: 'Digit2', key: '2'}))).toBeNull()
        expect(altDigit(digit({repeat: true, code: 'Digit2', key: '2'}))).toBeNull()
    })

    it('ignores Alt without a digit, and Alt+0', () => {
        expect(altDigit(digit({code: 'KeyA', key: 'a'}))).toBeNull()
        expect(altDigit(digit({code: 'Digit0', key: 'à'}))).toBeNull()
        expect(altDigit(digit({altKey: false, code: 'Digit2', key: '2'}))).toBeNull()
    })
})

describe('isTextEntry', () => {
    afterEach(() => {
        document.body.innerHTML = ''
    })

    it('recognises form fields and editable content', () => {
        document.body.innerHTML = `
            <input id="i"/><textarea id="t"></textarea><select id="s"></select>
            <div contenteditable="true"><span id="inside"></span></div>
            <div contenteditable="false"><span id="locked"></span></div>
            <button id="b"></button>`
        const at = (id: string) => document.getElementById(id)
        expect(isTextEntry(at('i'))).toBe(true)
        expect(isTextEntry(at('t'))).toBe(true)
        expect(isTextEntry(at('s'))).toBe(true)
        expect(isTextEntry(at('inside'))).toBe(true)
        expect(isTextEntry(at('locked'))).toBe(false)
        expect(isTextEntry(at('b'))).toBe(false)
        expect(isTextEntry(document.body)).toBe(false)
        expect(isTextEntry(null)).toBe(false)
    })
})

describe('claimShortcut', () => {
    it('marks one event, not the key', () => {
        const first = new KeyboardEvent('keydown', {code: 'Digit1'})
        const second = new KeyboardEvent('keydown', {code: 'Digit1'})
        claimShortcut(first)
        expect(isShortcutClaimed(first)).toBe(true)
        expect(isShortcutClaimed(second)).toBe(false)
    })
})
