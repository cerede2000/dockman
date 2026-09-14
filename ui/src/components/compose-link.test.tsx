import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'
import {act, fireEvent, render, screen} from '@testing-library/react'
import {MemoryRouter, Route, Routes, useLocation} from 'react-router'

// The real files.ts drags in the whole compose state chain, down to a store
// that reads localStorage while its module loads - which Node's experimental
// localStorage breaks under vitest. None of that is under test here. The host
// store keeps its exact shape, so the component's selector is exercised as is.
vi.mock('../pages/compose/state/files.ts', async () => {
    const {create} = await import('zustand')
    return {
        useHostStore: create<{host: string; setHost: (host: string) => void}>(set => ({
            host: '',
            setHost: (host: string) => set({host}),
        })),
    }
})

const {default: ComposeLink, editorPathForStack} = await import('./compose-link.tsx')
const {useHostStore} = await import('../pages/compose/state/files.ts')

// Where the router actually ended up, as the rest of the app would see it.
function LocationProbe() {
    const location = useLocation()
    return <output data-testid="location">{location.pathname + location.search}</output>
}

function renderInsideDeployTab(servicePath: string) {
    render(
        <MemoryRouter initialEntries={['/Home-Server/files/compose/auth/compose.yaml?tab=1']}>
            <Routes>
                <Route path="*" element={<>
                    <ComposeLink stackName="adguard" servicePath={servicePath}/>
                    <LocationProbe/>
                </>}/>
            </Routes>
        </MemoryRouter>
    )
}

describe('ComposeLink', () => {
    beforeEach(() => {
        act(() => useHostStore.setState({host: 'Home-Server'}))
    })
    afterEach(() => {
        act(() => useHostStore.setState({host: ''}))
    })

    // Reported: the stack link in the Deploy tab led nowhere. It navigated to
    // /stacks/<path>, a route that no longer exists - the editor is mounted at
    // /<host>/files/<path>, which is what the Monitor's edit action uses.
    it('opens the stack file in the editor of the current host', () => {
        renderInsideDeployTab('compose/adguard/compose.yaml')

        fireEvent.click(screen.getByRole('link', {name: 'adguard'}))

        expect(screen.getByTestId('location').textContent)
            .toBe('/Home-Server/files/compose/adguard/compose.yaml?tab=0')
    })

    // The destination must match the Monitor's, character for character, so
    // the two ways of opening a stack can never drift apart again.
    it('builds the same destination as the Monitor edit action', () => {
        const host = 'Home-Server'
        const servicePath = 'compose/adguard/compose.yaml'
        expect(editorPathForStack(host, servicePath)).toBe(`/${host}/files/${servicePath}?tab=0`)
    })

    // A container whose compose file is outside every alias arrives with an
    // empty path. A link there could only land on the file index.
    it('names a stack without a file instead of linking to nowhere', () => {
        renderInsideDeployTab('')

        expect(screen.queryByRole('link', {name: 'adguard'})).toBeNull()
        fireEvent.click(screen.getByText('adguard'))
        expect(screen.getByTestId('location').textContent)
            .toBe('/Home-Server/files/compose/auth/compose.yaml?tab=1')
    })

    it('does not build a destination before the host is known', () => {
        expect(editorPathForStack('', 'compose/adguard/compose.yaml')).toBeNull()
        expect(editorPathForStack('Home-Server', '')).toBeNull()
    })
})
