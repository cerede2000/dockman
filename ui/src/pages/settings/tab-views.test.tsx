import {act} from 'react'
import {beforeEach, describe, expect, it, vi} from 'vitest'
import {fireEvent, render, screen, within} from '@testing-library/react'
import {MemoryRouter} from 'react-router'
import {memoryStorage} from '../../test/memory-storage.ts'

const h = vi.hoisted(() => ({dockYaml: null as null | { defaultView: string }}))
vi.mock('../../hooks/config.ts', () => ({useConfig: () => ({dockYaml: h.dockYaml})}))
// The real module drags in the compose state chain, down to a store reading
// localStorage at load time; the tab only needs the host name.
vi.mock('../compose/state/files.ts', () => ({
    useHostStore: (select: (s: { host: string }) => unknown) => select({host: 'nas'}),
}))

vi.stubGlobal('localStorage', memoryStorage())
const {default: TabViews} = await import('./tab-views.tsx')
const {NAV_VIEW_IDS, useNavigationPreferences} = await import('../home/navigation-preferences.ts')

function renderTab() {
    render(<MemoryRouter><TabViews/></MemoryRouter>)
}

// The sidebar entries as listed, each with its shortcut chip.
function listed(): string[] {
    const list = screen.getByRole('list', {name: 'Sidebar order'})
    return within(list).getAllByRole('listitem').map(item => {
        const name = item.getAttribute('aria-label')
        const chip = within(item).queryByText(/^Alt\+\d$|^Hidden$/)?.textContent
        return `${name}:${chip ?? '-'}`
    })
}

beforeEach(() => {
    h.dockYaml = null
    act(() => useNavigationPreferences.setState({
        order: [...NAV_VIEW_IDS], showStats: false, showContainers: false, defaultView: '',
    }))
})

describe('Settings → Views: sidebar order', () => {
    it('lists every view in the sidebar order, with the shortcut each one answers to', () => {
        renderTab()
        expect(listed()).toEqual([
            'Files:Alt+1', 'Monitor:Alt+2', 'Stats:Hidden', 'Containers:Hidden', 'Updates:Alt+3',
            'Images:Alt+4', 'Volumes:Alt+5', 'Networks:Alt+6', 'Cleaner:Alt+7',
        ])
    })

    // RA341/dockman#229: reorder the sidebar icons. One click is one step in
    // the sidebar, hidden Stats and Containers in between or not.
    it('moves an entry, and its shortcut follows its new position', () => {
        renderTab()
        fireEvent.click(screen.getByRole('button', {name: 'Move Updates up'}))

        expect(useNavigationPreferences.getState().order.slice(0, 3)).toEqual(['files', 'updates', 'monitor'])
        expect(listed().slice(0, 3)).toEqual(['Files:Alt+1', 'Updates:Alt+2', 'Monitor:Alt+3'])
    })

    const disabled = (name: string) =>
        (screen.getByRole('button', {name}) as HTMLButtonElement).disabled

    it('cannot move past either end', () => {
        renderTab()
        expect(disabled('Move Files up')).toBe(true)
        expect(disabled('Move Cleaner down')).toBe(true)
        expect(disabled('Move Monitor up')).toBe(false)
    })

    // Only hidden views before it: moving up would change nothing on screen.
    it('offers no move that would change nothing in the sidebar', () => {
        act(() => useNavigationPreferences.setState({
            order: ['stats', 'files', 'monitor', 'containers', 'updates', 'images', 'volumes', 'networks', 'cleaner'],
        }))
        renderTab()
        expect(disabled('Move Files up')).toBe(true)
        expect(disabled('Move Stats down')).toBe(false)
    })

    it('shows a legacy view again and gives it a shortcut', () => {
        renderTab()
        const stats = screen.getByRole('listitem', {name: 'Stats'})
        fireEvent.click(within(stats).getByRole('switch'))

        expect(useNavigationPreferences.getState().showStats).toBe(true)
        expect(listed().slice(0, 4)).toEqual(['Files:Alt+1', 'Monitor:Alt+2', 'Stats:Alt+3', 'Containers:Hidden'])
    })

    it('resets the order', () => {
        renderTab()
        const reset = screen.getByRole('button', {name: 'Reset order'}) as HTMLButtonElement
        expect(reset.disabled).toBe(true)

        fireEvent.click(screen.getByRole('button', {name: 'Move Cleaner up'}))
        expect(reset.disabled).toBe(false)
        fireEvent.click(reset)
        expect(useNavigationPreferences.getState().order).toEqual([...NAV_VIEW_IDS])
    })
})

describe('Settings → Views: landing page', () => {
    function choose(option: RegExp | string) {
        fireEvent.mouseDown(screen.getByRole('combobox', {name: /Open Dockman on/}))
        fireEvent.click(screen.getByRole('option', {name: option}))
    }

    // RA341/dockman#229: open Dockman on Stats or Containers.
    it('stores the chosen landing page', () => {
        renderTab()
        choose('Containers')
        expect(useNavigationPreferences.getState().defaultView).toBe('containers')
    })

    it('names what the server default currently is, and can return to it', () => {
        h.dockYaml = {defaultView: 'monitor'}
        act(() => useNavigationPreferences.getState().setDefaultView('images'))
        renderTab()
        choose('Server default (Monitor)')
        expect(useNavigationPreferences.getState().defaultView).toBe('')
    })

    // An empty value is a choice, not a blank field: the server default is
    // named in the field itself.
    it('shows the server default in the field when nothing is chosen', () => {
        h.dockYaml = {defaultView: 'updates'}
        renderTab()
        expect(screen.getByRole('combobox', {name: /Open Dockman on/}).textContent).toBe('Server default (Updates)')
    })
})
