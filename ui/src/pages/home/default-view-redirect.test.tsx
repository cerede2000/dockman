import {act} from 'react'
import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'
import {render, screen} from '@testing-library/react'
import {MemoryRouter, Route, Routes, useLocation} from 'react-router'
import {memoryStorage} from '../../test/memory-storage.ts'

const h = vi.hoisted(() => ({dockYaml: null as null | { defaultView: string }}))
vi.mock('../../hooks/config.ts', () => ({useConfig: () => ({dockYaml: h.dockYaml})}))

vi.stubGlobal('localStorage', memoryStorage())
const {default: HostDefaultViewRedirect} = await import('./default-view-redirect.tsx')
const {useNavigationPreferences} = await import('./navigation-preferences.ts')

function Where() {
    const location = useLocation()
    return <output data-testid="where">{location.pathname}</output>
}

function openHost() {
    render(
        <MemoryRouter initialEntries={['/nas']}>
            <Routes>
                <Route path=":host">
                    <Route index element={<HostDefaultViewRedirect/>}/>
                    <Route path="*" element={<Where/>}/>
                </Route>
            </Routes>
        </MemoryRouter>
    )
}

const where = () => screen.queryByTestId('where')?.textContent ?? null

beforeEach(() => {
    vi.useFakeTimers()
    h.dockYaml = null
    act(() => useNavigationPreferences.getState().setDefaultView(''))
})

afterEach(() => {
    vi.useRealTimers()
})

describe('HostDefaultViewRedirect', () => {
    // RA341/dockman#229: open Dockman straight on Stats or Containers.
    it('opens the view chosen in this browser at once, without waiting for dockman.yml', () => {
        act(() => useNavigationPreferences.getState().setDefaultView('containers'))
        openHost()
        expect(where()).toBe('/nas/containers')
    })

    it('prefers the browser choice over dockman.yml', () => {
        h.dockYaml = {defaultView: 'images'}
        act(() => useNavigationPreferences.getState().setDefaultView('monitor'))
        openHost()
        expect(where()).toBe('/nas/monitor')
    })

    // Updates was not an accepted landing view before.
    it('follows dockman.yml without a browser choice, Updates included', () => {
        h.dockYaml = {defaultView: 'updates'}
        openHost()
        expect(where()).toBe('/nas/updates')
    })

    it('waits for dockman.yml, then falls back to Files', async () => {
        openHost()
        expect(where()).toBeNull()
        await act(async () => {
            await vi.advanceTimersByTimeAsync(1500)
        })
        expect(where()).toBe('/nas/files')
    })
})
