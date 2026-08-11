import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'
import {act, renderHook} from '@testing-library/react'

interface Event {
    action: string
}

// A stream the test drives by hand: `containerEvents` hands one out per
// subscription, and the test pushes events into it or drops it the way a
// daemon restart would.
function pushable() {
    const queue: Event[] = []
    let wake: (() => void) | null = null
    let ended = false
    return {
        push(ev: Event) {
            queue.push(ev)
            wake?.()
            wake = null
        },
        drop() {
            ended = true
            wake?.()
            wake = null
        },
        async* [Symbol.asyncIterator]() {
            for (; ;) {
                while (queue.length) yield queue.shift()!
                if (ended) return
                await new Promise<void>(resolve => {
                    wake = resolve
                })
            }
        },
    }
}

const h = vi.hoisted(() => {
    const state = {
        host: 'alpha',
        streams: [] as { req: { host?: string }; signal?: AbortSignal; source: ReturnType<typeof pushableRef.make> }[],
        checkServerBuild: vi.fn(async () => {
        }),
    }
    const pushableRef = {make: null as unknown as () => ReturnType<typeof pushable>}
    const dockerClient = {
        containerEvents(req: { host?: string }, options?: { signal?: AbortSignal }) {
            const source = pushableRef.make()
            state.streams.push({req, signal: options?.signal, source})
            return source
        },
    }
    const infoClient = {getAppInfo: async () => ({version: 'test', commit: 'test'})}
    return {state, pushableRef, dockerClient, infoClient}
})
h.pushableRef.make = pushable

vi.mock('../lib/api.ts', () => ({
    useHostClient: () => h.dockerClient,
    useClient: () => h.infoClient,
}))
vi.mock('../pages/compose/state/files.ts', () => ({
    useHostStore: (select: (s: { host: string }) => unknown) => select({host: h.state.host}),
}))
vi.mock('./app-build.ts', () => ({checkServerBuild: h.state.checkServerBuild}))

const {refreshDockerStateNow, useDockerEvents} = await import('./docker-events.ts')

const flush = async (ms = 0) => {
    await act(async () => {
        await vi.advanceTimersByTimeAsync(ms)
    })
}

const latest = () => h.state.streams[h.state.streams.length - 1]

beforeEach(() => {
    vi.useFakeTimers()
    h.state.streams = []
    h.state.host = 'alpha'
    h.state.checkServerBuild.mockClear()
})

afterEach(() => {
    vi.useRealTimers()
})

describe('useDockerEvents', () => {
    // The server already multiplexes one daemon subscription per host. Opening
    // a stream per mounted view would multiply that all the way down.
    it('runs a single stream however many views listen', async () => {
        const a = renderHook(() => useDockerEvents())
        const b = renderHook(() => useDockerEvents())
        const c = renderHook(() => useDockerEvents())
        await flush()

        expect(h.state.streams).toHaveLength(1)

        a.unmount()
        b.unmount()
        c.unmount()
    })

    it('wakes every listener on one event', async () => {
        const a = renderHook(() => useDockerEvents())
        const b = renderHook(() => useDockerEvents())
        await flush()
        const before = [a.result.current, b.result.current]

        latest().source.push({action: 'start'})
        await flush(300)

        expect(a.result.current).toBe(before[0] + 1)
        expect(b.result.current).toBe(before[1] + 1)

        a.unmount()
        b.unmount()
    })

    // A compose up emits one event per container, and every one of them used
    // to be a refresh.
    it('coalesces a burst into one refresh', async () => {
        const {result, unmount} = renderHook(() => useDockerEvents())
        await flush()
        const before = result.current

        for (const action of ['create', 'start', 'health_status', 'start', 'start']) {
            latest().source.push({action})
        }
        await flush(300)

        expect(result.current).toBe(before + 1)
        unmount()
    })

    // Throttle from the FIRST event, not debounce from the last: debouncing
    // made every container in a compose operation postpone the refresh again,
    // so a stack with several services could hold stale bullets for seconds.
    it('refreshes 300 ms after the first event even while more keep arriving', async () => {
        const {result, unmount} = renderHook(() => useDockerEvents())
        await flush()
        const before = result.current

        latest().source.push({action: 'start'})
        await flush(150)
        latest().source.push({action: 'start'})
        await flush(100)
        latest().source.push({action: 'start'})
        // 300 ms after the FIRST event, not after the last one
        await flush(50)

        expect(result.current).toBe(before + 1)
        unmount()
    })

    it('ignores keepalive frames', async () => {
        const {result, unmount} = renderHook(() => useDockerEvents())
        await flush()
        const before = result.current

        latest().source.push({action: ''})
        await flush(1000)

        expect(result.current).toBe(before)
        unmount()
    })

    // A Dockman action knows exactly when its RPC settled; it should not wait
    // out the burst window, and the window's pending tick must not then fire a
    // second, redundant refresh.
    it('refreshes at once on demand and cancels the pending burst', async () => {
        const {result, unmount} = renderHook(() => useDockerEvents())
        await flush()
        const before = result.current

        latest().source.push({action: 'start'})
        await flush(50)
        act(() => refreshDockerStateNow())
        expect(result.current).toBe(before + 1)

        await flush(1000)
        expect(result.current).toBe(before + 1)
        unmount()
    })

    // A daemon or socket-proxy restart may not emit a final lifecycle event:
    // the drop itself is the signal that everything on screen may be stale.
    it('invalidates and resubscribes when the stream drops', async () => {
        const {result, unmount} = renderHook(() => useDockerEvents())
        await flush()
        const before = result.current

        latest().source.drop()
        await flush(300)
        expect(result.current).toBe(before + 1)

        // the drop is also the cheapest honest place to notice the server is
        // now serving a newer bundle than this page was loaded with
        expect(h.state.checkServerBuild).toHaveBeenCalled()

        expect(h.state.streams).toHaveLength(1)
        await flush(1000)
        expect(h.state.streams).toHaveLength(2)

        unmount()
    })

    it('stops the stream once the last listener leaves', async () => {
        const first = renderHook(() => useDockerEvents())
        const second = renderHook(() => useDockerEvents())
        await flush()
        const signal = latest().signal

        first.unmount()
        expect(signal?.aborted).toBe(false)

        second.unmount()
        expect(signal?.aborted).toBe(true)

        // and a later view opens a new one rather than listening to nothing
        const later = renderHook(() => useDockerEvents())
        await flush()
        expect(h.state.streams).toHaveLength(2)
        later.unmount()
    })

    it('follows the selected host', async () => {
        const {rerender, unmount} = renderHook(() => useDockerEvents())
        await flush()
        expect(latest().req.host).toBe('alpha')

        h.state.host = 'beta'
        rerender()
        await flush()

        expect(h.state.streams).toHaveLength(2)
        expect(latest().req.host).toBe('beta')
        unmount()
    })
})
