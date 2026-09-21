import {describe, expect, it, vi} from 'vitest'
import {render, screen} from '@testing-library/react'
import {MemoryRouter, Route, Routes} from 'react-router'
import {memoryStorage} from '../../test/memory-storage.ts'

// Settings is mounted the way App mounts it: /settings is let through the host
// guard without the host's UserConfigProvider. A tab that reads the host
// configuration throws in render, and React unmounts the whole app - the
// black screen the first release of Settings → Views produced.
vi.stubGlobal('localStorage', memoryStorage())
const {SettingsPage} = await import('./settings-page.tsx')

function openSettingsTab(tab: number) {
    render(
        <MemoryRouter initialEntries={[`/settings?tab=${tab}`]}>
            <Routes>
                <Route path="settings" element={<SettingsPage/>}/>
            </Routes>
        </MemoryRouter>
    )
}

describe('SettingsPage without a host configuration provider', () => {
    it('renders the Views tab', () => {
        openSettingsTab(5)
        expect(screen.getByRole('tab', {name: 'Views', selected: true})).toBeTruthy()
        expect(screen.getByRole('list', {name: 'Sidebar order'})).toBeTruthy()
    })
})
