import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'
import {act, renderHook} from '@testing-library/react'
import {restoreDocumentVisibility, setDocumentVisibility} from '../test/visibility.ts'

const h = vi.hoisted(() => ({calls: 0}))

vi.mock('../lib/api.ts', () => ({withProtectedAPI: (path: string) => 'http://test' + path}))

const {useGitStatusWatcher} = await import('./git-stack-status-store.ts')

const flush = async (ms = 0) => {
    await act(async () => {
        await vi.advanceTimersByTimeAsync(ms)
    })
}

beforeEach(() => {
    vi.useFakeTimers()
    setDocumentVisibility('visible')
    h.calls = 0
    vi.stubGlobal('fetch', async () => {
        h.calls++
        return {ok: true, json: async () => []} as unknown as Response
    })
})

afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
    restoreDocumentVisibility()
})

describe('useGitStatusWatcher', () => {
    it('reads the badges once on mount and then on its cadence', async () => {
        const {unmount} = renderHook(() => useGitStatusWatcher('local'))
        await flush()
        expect(h.calls).toBe(1)

        await flush(30_000)
        expect(h.calls).toBe(2)
        unmount()
    })

    // Nobody reads the badges while the tab is hidden, and every firing is a
    // real status read on the server.
    it('reads nothing while the tab is hidden and refreshes on return', async () => {
        const {unmount} = renderHook(() => useGitStatusWatcher('local'))
        await flush()
        expect(h.calls).toBe(1)

        act(() => setDocumentVisibility('hidden'))
        await flush(10 * 60_000)
        expect(h.calls).toBe(1)

        setDocumentVisibility('visible')
        await flush(0)
        expect(h.calls).toBe(2)
        unmount()
    })

    it('runs a single watcher however many views mount it', async () => {
        const a = renderHook(() => useGitStatusWatcher('local'))
        const b = renderHook(() => useGitStatusWatcher('local'))
        await flush()
        expect(h.calls).toBe(1)

        a.unmount()
        b.unmount()
        await flush(10 * 60_000)
        expect(h.calls).toBe(1)
    })
})
