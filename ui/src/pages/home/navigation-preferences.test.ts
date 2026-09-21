import {afterEach, describe, expect, it, vi} from 'vitest'
import {memoryStorage} from '../../test/memory-storage.ts'
import {
    mergeStoredPreferences,
    moveOrder,
    NAV_VIEW_IDS,
    type NavViewId,
    normalizeOrder,
    resolveLandingView,
    visibleViews,
} from './navigation-preferences.ts'

const STORE_KEY = 'dockman-navigation-views'

describe('normalizeOrder', () => {
    it('starts from the default order when nothing was stored', () => {
        expect(normalizeOrder(undefined)).toEqual([...NAV_VIEW_IDS])
        expect(normalizeOrder('files')).toEqual([...NAV_VIEW_IDS])
    })

    it('keeps a stored order', () => {
        const stored = [...NAV_VIEW_IDS].reverse()
        expect(normalizeOrder(stored)).toEqual(stored)
    })

    // A view added by a later release must appear in a customised sidebar,
    // and one removed must not leave a dead entry.
    it('appends views the stored order does not know and drops unknown ones', () => {
        expect(normalizeOrder(['monitor', 'gone', 'files', 'monitor'])).toEqual(
            ['monitor', 'files', ...NAV_VIEW_IDS.filter(id => id !== 'monitor' && id !== 'files')])
    })
})

describe('moveOrder', () => {
    const order: NavViewId[] = ['files', 'monitor', 'updates']

    it('swaps a view with its neighbour', () => {
        expect(moveOrder(order, 'monitor', -1)).toEqual(['monitor', 'files', 'updates'])
        expect(moveOrder(order, 'monitor', 1)).toEqual(['files', 'updates', 'monitor'])
    })

    it('does nothing past either end', () => {
        expect(moveOrder(order, 'files', -1)).toBe(order)
        expect(moveOrder(order, 'updates', 1)).toBe(order)
    })

    it('never mutates the order it was given', () => {
        const before = [...order]
        moveOrder(order, 'monitor', 1)
        expect(order).toEqual(before)
    })

    // Moving Updates past a hidden Stats and Containers used to take three
    // clicks, two of which changed nothing in the sidebar.
    describe('with hidden views', () => {
        const withHidden: NavViewId[] = ['files', 'monitor', 'stats', 'containers', 'updates', 'images']
        const shown = (id: NavViewId) => id !== 'stats' && id !== 'containers'

        it('jumps a shown view over hidden ones to its next shown neighbour', () => {
            expect(moveOrder(withHidden, 'updates', -1, shown))
                .toEqual(['files', 'updates', 'monitor', 'stats', 'containers', 'images'])
            expect(moveOrder(withHidden, 'monitor', 1, shown))
                .toEqual(['files', 'stats', 'containers', 'updates', 'monitor', 'images'])
        })

        it('changes the sidebar with every move', () => {
            const sidebar = (o: NavViewId[]) => o.filter(shown)
            for (const id of withHidden.filter(shown)) {
                for (const offset of [-1, 1] as const) {
                    const moved = moveOrder(withHidden, id, offset, shown)
                    if (moved !== withHidden) expect(sidebar(moved)).not.toEqual(sidebar(withHidden))
                }
            }
        })

        it('has nowhere to go when only hidden views lie ahead', () => {
            const hiddenFirst: NavViewId[] = ['stats', 'files', 'monitor']
            expect(moveOrder(hiddenFirst, 'files', -1, shown)).toBe(hiddenFirst)
        })

        it('moves a hidden view one row at a time', () => {
            expect(moveOrder(withHidden, 'stats', -1, shown))
                .toEqual(['files', 'stats', 'monitor', 'containers', 'updates', 'images'])
        })
    })
})

describe('visibleViews', () => {
    it('drops the legacy views that are switched off, and only those', () => {
        const order: NavViewId[] = ['stats', 'files', 'containers', 'monitor']
        expect(visibleViews(order, {showStats: false, showContainers: false})).toEqual(['files', 'monitor'])
        expect(visibleViews(order, {showStats: true, showContainers: false})).toEqual(['stats', 'files', 'monitor'])
    })
})

describe('resolveLandingView', () => {
    it('prefers the choice made in this browser', () => {
        expect(resolveLandingView('monitor', 'images')).toBe('monitor')
    })

    it('falls back to dockman.yml, then to Files', () => {
        expect(resolveLandingView('', ' Images ')).toBe('images')
        expect(resolveLandingView('', undefined)).toBe('files')
        expect(resolveLandingView('', 'nowhere')).toBe('files')
    })

    // Updates was missing from the list of accepted landing views.
    it('accepts every sidebar view, Updates included', () => {
        for (const id of NAV_VIEW_IDS) expect(resolveLandingView('', id)).toBe(id)
    })
})

describe('mergeStoredPreferences', () => {
    const current = {
        showStats: false, showContainers: false, order: [...NAV_VIEW_IDS], defaultView: '' as const,
        setShowStats: () => {}, setShowContainers: () => {}, moveView: () => {}, resetOrder: () => {},
        setDefaultView: () => {},
    }

    it('rejects a landing view that no longer exists', () => {
        expect(mergeStoredPreferences({defaultView: 'gone'}, current).defaultView).toBe('')
        expect(mergeStoredPreferences({defaultView: 'updates'}, current).defaultView).toBe('updates')
    })
})

// End to end through zustand's persist middleware, from what an older release
// left in the browser.
describe('useNavigationPreferences persistence', () => {
    afterEach(() => {
        vi.unstubAllGlobals()
        vi.resetModules()
    })

    async function loadStore(storage: Storage) {
        vi.stubGlobal('localStorage', storage)
        vi.resetModules()
        return (await import('./navigation-preferences.ts')).useNavigationPreferences
    }

    // The previous release stored only the two switches: they must survive.
    it('keeps the switches an older release stored', async () => {
        const store = await loadStore(memoryStorage({
            [STORE_KEY]: JSON.stringify({state: {showStats: true, showContainers: false}, version: 0}),
        }))
        const state = store.getState()
        expect(state.showStats).toBe(true)
        expect(state.showContainers).toBe(false)
        expect(state.order).toEqual([...NAV_VIEW_IDS])
        expect(state.defaultView).toBe('')
    })

    it('stores the order and the landing page, and reads them back', async () => {
        const storage = memoryStorage()
        const first = await loadStore(storage)
        first.getState().moveView('updates', -1)
        first.getState().setDefaultView('monitor')

        const saved = JSON.parse(storage.getItem(STORE_KEY)!)
        expect(saved.state.order.slice(0, 5)).toEqual(['files', 'updates', 'monitor', 'stats', 'containers'])
        expect(saved.state.defaultView).toBe('monitor')

        const reloaded = await loadStore(storage)
        expect(reloaded.getState().order).toEqual(saved.state.order)
        expect(reloaded.getState().defaultView).toBe('monitor')
    })

    it('resets the order without touching the rest', async () => {
        const store = await loadStore(memoryStorage())
        store.getState().setShowStats(true)
        store.getState().moveView('cleaner', -1)
        store.getState().resetOrder()
        expect(store.getState().order).toEqual([...NAV_VIEW_IDS])
        expect(store.getState().showStats).toBe(true)
    })
})
