import {describe, expect, it, vi} from 'vitest'
import {render, screen} from '@testing-library/react'
import {MemoryRouter, Route, Routes} from 'react-router'
import {memoryStorage} from '../../test/memory-storage.ts'

// Settings is mounted the way App mounts it: /settings is let through the host
// guard without the host's UserConfigProvider. A tab that reads the host
// configuration throws in render, and React unmounts the whole app - the
// black screen the first release of Settings → Views produced.
vi.stubGlobal('localStorage', memoryStorage())
const {SettingsPage, settingsTabLabels} = await import('./settings-page.tsx')
const {SnackbarProvider} = await import('../../context/snackbar-context.tsx')

// The tab is opened by its name, not by the index it happens to have: a tab
// added before it would otherwise move this test onto another one, quietly,
// and the guard would be watching the wrong screen.
// App wraps everything in the snackbar provider, so the tabs may use it; the
// host configuration provider is the one /settings goes without.
function openSettingsTab(name: string) {
    const index = settingsTabLabels.indexOf(name)
    expect(index, `no settings tab named ${name}`).toBeGreaterThanOrEqual(0)
    render(
        <SnackbarProvider>
            <MemoryRouter initialEntries={[`/settings?tab=${index}`]}>
                <Routes>
                    <Route path="settings" element={<SettingsPage/>}/>
                </Routes>
            </MemoryRouter>
        </SnackbarProvider>
    )
}

describe('SettingsPage without a host configuration provider', () => {
    it('renders the Views tab', () => {
        openSettingsTab('Views')
        expect(screen.getByRole('tab', {name: 'Views', selected: true})).toBeTruthy()
        expect(screen.getByRole('list', {name: 'Sidebar order'})).toBeTruthy()
    })

    it('renders the Registries tab', () => {
        vi.stubGlobal('fetch', vi.fn(async () => ({ok: true, status: 200, json: async () => []}) as unknown as Response))
        openSettingsTab('Registries')
        expect(screen.getByRole('tab', {name: 'Registries', selected: true})).toBeTruthy()
        expect(screen.getByRole('button', {name: 'Add registry'})).toBeTruthy()
    })
})
