import {afterEach, describe, expect, it, vi} from 'vitest'
import {renderHook} from '@testing-library/react'
import {useSidebarShortcuts, type SidebarShortcutItem} from './sidebar-shortcuts.ts'
import {isShortcutClaimed} from '../../lib/shortcut.ts'

const items: SidebarShortcutItem[] = [
    {id: 'files', path: '/local/files'},
    {id: 'monitor', path: '/local/monitor'},
    {id: 'updates', path: '/local/updates'},
]

// A real keydown, dispatched where the user's focus is.
function press(init: KeyboardEventInit, target: EventTarget = document.body): KeyboardEvent {
    const event = new KeyboardEvent('keydown', {bubbles: true, cancelable: true, altKey: true, ...init})
    target.dispatchEvent(event)
    return event
}

afterEach(() => {
    document.body.innerHTML = ''
})

describe('useSidebarShortcuts', () => {
    // RA341/dockman#229 on AZERTY: Alt+2 carries key "é".
    it('opens the entry at that position whatever the layout', () => {
        const navigate = vi.fn()
        renderHook(() => useSidebarShortcuts(items, navigate, '/local/images'))

        press({code: 'Digit2', key: 'é'})
        press({code: 'Digit3', key: '™'})
        expect(navigate.mock.calls).toEqual([['/local/monitor'], ['/local/updates']])
    })

    // On Linux, Firefox binds Alt+1..9 to its own tabs; they are not reserved
    // keys, so a prevented event keeps the page in charge.
    it('prevents the browser from acting on the keystroke', () => {
        renderHook(() => useSidebarShortcuts(items, vi.fn(), '/local/images'))
        expect(press({code: 'Digit2', key: 'é'}).defaultPrevented).toBe(true)
    })

    it('leaves a brace typed in the editor alone', () => {
        const navigate = vi.fn()
        renderHook(() => useSidebarShortcuts(items, navigate, '/local/files/app/compose.yaml'))
        const editor = document.body.appendChild(document.createElement('textarea'))

        const brace = press({code: 'Digit5', key: '{'}, editor)
        expect(navigate).not.toHaveBeenCalled()
        expect(brace.defaultPrevented).toBe(false)
    })

    it('does nothing for a position without an entry', () => {
        const navigate = vi.fn()
        renderHook(() => useSidebarShortcuts(items, navigate, '/local/images'))
        press({code: 'Digit7', key: '7'})
        expect(navigate).not.toHaveBeenCalled()
    })

    // The Files view binds Alt+1 to its file bar. The Files entry would only
    // redirect back to the open file, so the file bar must get the keystroke.
    it('lets the Files view keep Alt+1 while inside Files', () => {
        const navigate = vi.fn()
        const fileBar = vi.fn()
        const onWindow = (e: KeyboardEvent) => {
            if (!isShortcutClaimed(e)) fileBar()
        }
        window.addEventListener('keydown', onWindow)
        renderHook(() => useSidebarShortcuts(items, navigate, '/local/files/app/compose.yaml'))

        press({code: 'Digit1', key: '&'})
        window.removeEventListener('keydown', onWindow)

        expect(navigate).not.toHaveBeenCalled()
        expect(fileBar).toHaveBeenCalledTimes(1)
    })

    // Moved elsewhere in the sidebar order, Alt+1 leaves Files: the file bar,
    // listening on window, must see the keystroke as spent.
    it('claims the keystroke when it takes the page elsewhere', () => {
        const fileBar = vi.fn()
        const onWindow = (e: KeyboardEvent) => {
            if (!isShortcutClaimed(e)) fileBar()
        }
        window.addEventListener('keydown', onWindow)
        const reordered = [items[1], items[0], items[2]]
        const navigate = vi.fn()
        renderHook(() => useSidebarShortcuts(reordered, navigate, '/local/files/app/compose.yaml'))

        press({code: 'Digit1', key: '&'})
        window.removeEventListener('keydown', onWindow)

        expect(navigate).toHaveBeenCalledWith('/local/monitor')
        expect(fileBar).not.toHaveBeenCalled()
    })

    it('stops listening once unmounted', () => {
        const navigate = vi.fn()
        const {unmount} = renderHook(() => useSidebarShortcuts(items, navigate, '/local/images'))
        unmount()
        press({code: 'Digit2', key: '2'})
        expect(navigate).not.toHaveBeenCalled()
    })
})
