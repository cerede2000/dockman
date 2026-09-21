import {act} from 'react'
import {afterEach, describe, expect, it, vi} from 'vitest'
import {renderHook} from '@testing-library/react'
import {memoryStorage} from '../../test/memory-storage.ts'
import routes from '../../App.tsx?raw'

vi.stubGlobal('localStorage', memoryStorage())
const {NAV_VIEW_IDS, useNavigationPreferences} = await import('./navigation-preferences.ts')
const {NAV_VIEWS, sidebarItems, useSidebarItems} = await import('./navigation-views.tsx')

describe('sidebarItems', () => {
    it('keeps the order it is given and builds each route for the host', () => {
        expect(sidebarItems('nas', ['updates', 'files']).map(i => [i.id, i.title, i.path])).toEqual([
            ['updates', 'Updates', '/nas/updates'],
            ['files', 'Files', '/nas/files'],
        ])
    })
})

// Every view the sidebar, the landing page and Settings → Views can name must
// be a route of the host - the old list of landing views had drifted (Updates
// was missing), which is what a single list is meant to prevent.
describe('NAV_VIEW_IDS', () => {
    it.each([...NAV_VIEW_IDS])('%s is a host route', id => {
        expect(routes).toContain(`<Route path="${id}"`)
    })

    it('describes every view', () => {
        expect(Object.keys(NAV_VIEWS).sort()).toEqual([...NAV_VIEW_IDS].sort())
    })
})

// What RootLayout renders: the sidebar follows what Settings → Views stored.
describe('useSidebarItems', () => {
    afterEach(() => {
        act(() => useNavigationPreferences.setState({order: [...NAV_VIEW_IDS], showStats: false, showContainers: false}))
    })

    const ids = (host = 'nas') => renderHook(() => useSidebarItems(host)).result.current.map(i => i.id)

    it('shows the default sidebar, legacy views hidden', () => {
        expect(ids()).toEqual(['files', 'monitor', 'updates', 'images', 'volumes', 'networks', 'cleaner'])
    })

    it('follows the stored order and visibility', () => {
        act(() => useNavigationPreferences.setState({
            order: ['stats', 'cleaner', 'files', 'monitor', 'containers', 'updates', 'images', 'volumes', 'networks'],
            showStats: true,
        }))
        expect(ids()).toEqual(['stats', 'cleaner', 'files', 'monitor', 'updates', 'images', 'volumes', 'networks'])
    })

    it('updates when the order changes', () => {
        const {result} = renderHook(() => useSidebarItems('nas'))
        act(() => useNavigationPreferences.getState().moveView('monitor', -1))
        expect(result.current.slice(0, 2).map(i => i.path)).toEqual(['/nas/monitor', '/nas/files'])
    })
})
