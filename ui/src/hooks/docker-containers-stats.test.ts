import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'
import {act, renderHook} from '@testing-library/react'
import {create, type MessageInitShape} from '@bufbuild/protobuf'
import {type ContainerStats, ContainerStatsSchema} from '../gen/docker/v1/docker_pb.ts'
import {restoreDocumentVisibility, setDocumentVisibility} from '../test/visibility.ts'

// A cycle is one pass of the stats stream. `useDockerStats` keeps its effect
// alive across renders only if the client and the error callback keep their
// identity, so both live in this stable fixture rather than being rebuilt per
// render — a fresh object per render would restart the effect forever and
// nothing under test would be observable.
const h = vi.hoisted(() => {
    const state = {
        streamRequests: [] as { host: string; file?: { filename: string } }[],
        // Each entry is one cycle's worth of streamed rows. The last entry is
        // reused once the queue runs dry, so a test that only cares about the
        // cadence does not have to enqueue a cycle per tick.
        cycles: [] as unknown[][],
        hostStatsCalls: 0,
        hostStatsValue: null as unknown,
        host: 'alpha',
        showError: vi.fn(),
    }

    const service = {
        containerStatsStream(req: { host: string; file?: { filename: string } }) {
            state.streamRequests.push(req)
            const batch = state.cycles.length > 1
                ? state.cycles.shift()!
                : (state.cycles[0] ?? [])
            return (async function* () {
                for (const stat of batch) yield stat
            })()
        },
        containerList: async () => ({list: []}),
        hostStats: async () => {
            state.hostStatsCalls++
            return state.hostStatsValue
        },
    }

    return {state, service}
})

vi.mock('../lib/api.ts', () => ({
    useHostClient: () => h.service,
    callRPC: async (exec: () => Promise<unknown>) => {
        try {
            return {val: await exec(), err: ''}
        } catch (e) {
            return {val: null, err: String(e)}
        }
    },
}))
vi.mock('./snackbar.ts', () => ({useSnackbar: () => ({showError: h.state.showError})}))
vi.mock('./config.ts', () => ({useConfig: () => ({dockYaml: null})}))
vi.mock('../pages/compose/state/files.ts', () => ({
    useHostStore: (select: (s: { host: string }) => unknown) => select({host: h.state.host}),
}))

const {useDockerStats, useHostStats} = await import('./docker-containers-stats.ts')

const GB = 1024n * 1024n * 1024n

function stat(over: MessageInitShape<typeof ContainerStatsSchema>): ContainerStats {
    return create(ContainerStatsSchema, {
        id: 'id-' + over.name,
        name: 'container',
        cpuUsage: 1,
        memoryUsage: GB,
        memoryLimit: 32n * GB,
        state: 'running',
        health: 'healthy',
        ...over,
    })
}

// One full cycle plus the 200 ms progressive-paint window.
const settle = async (ms = 250) => {
    await act(async () => {
        await vi.advanceTimersByTimeAsync(ms)
    })
}

// Module-level history is keyed by host and cleared when the host changes, so
// giving every test its own host name is what isolates them.
let hostSeq = 0
const freshHost = () => {
    hostSeq++
    h.state.host = `host-${hostSeq}`
    return h.state.host
}

beforeEach(() => {
    vi.useFakeTimers()
    setDocumentVisibility('visible')
    h.state.streamRequests = []
    h.state.cycles = []
    h.state.hostStatsCalls = 0
    h.state.hostStatsValue = null
    h.state.showError.mockClear()
    freshHost()
})

afterEach(() => {
    vi.useRealTimers()
    restoreDocumentVisibility()
})

describe('useDockerStats: the stats cycle and the tab', () => {
    it('runs a cycle on its cadence while the tab is visible', async () => {
        h.state.cycles = [[stat({name: 'web'})]]
        renderHook(() => useDockerStats())
        await settle()
        expect(h.state.streamRequests).toHaveLength(1)

        await settle(5000)
        expect(h.state.streamRequests).toHaveLength(2)
    })

    // One cycle is one ContainerStats call per container against the daemon.
    // A hidden tab still fires its timers, so before the guard a tab left in
    // the background kept hitting the daemon for hours.
    it('stops entirely while the tab is hidden', async () => {
        h.state.cycles = [[stat({name: 'web'})]]
        renderHook(() => useDockerStats())
        await settle()
        expect(h.state.streamRequests).toHaveLength(1)

        act(() => setDocumentVisibility('hidden'))
        await settle(10 * 60_000)
        expect(h.state.streamRequests).toHaveLength(1)
    })

    // The loop stops completely rather than staying armed, so coming back has
    // to restart it — otherwise the tab would show the reading taken before it
    // was hidden until the user reloaded.
    it('starts a fresh cycle the moment the tab comes back', async () => {
        h.state.cycles = [[stat({name: 'web'})]]
        renderHook(() => useDockerStats())
        await settle()

        act(() => setDocumentVisibility('hidden'))
        await settle(10 * 60_000)
        expect(h.state.streamRequests).toHaveLength(1)

        setDocumentVisibility('visible')
        await settle(0)
        expect(h.state.streamRequests).toHaveLength(2)

        // and the cadence picks up again from there
        await settle(5000)
        expect(h.state.streamRequests).toHaveLength(3)
    })

    it('arms nothing after unmount', async () => {
        h.state.cycles = [[stat({name: 'web'})]]
        const {unmount} = renderHook(() => useDockerStats())
        await settle()
        unmount()

        await settle(10 * 60_000)
        setDocumentVisibility('visible')
        await settle(0)
        expect(h.state.streamRequests).toHaveLength(1)
    })
})

describe('useDockerStats: header aggregates', () => {
    // Containers without an explicit memory limit report the host's total RAM
    // as their limit. Summing counts the host once per container: four
    // containers on a 32 GB host would read "128 GB".
    it('takes the memory ceiling as the largest limit, not the sum', async () => {
        h.state.cycles = [[
            stat({name: 'web', memoryUsage: GB, memoryLimit: 32n * GB}),
            stat({name: 'db', memoryUsage: 2n * GB, memoryLimit: 32n * GB}),
        ]]
        const {result} = renderHook(() => useDockerStats())
        await settle()

        expect(result.current.aggregates?.memLimit).toBe(Number(32n * GB))
        expect(result.current.aggregates?.memUsed).toBe(Number(3n * GB))
    })

    // Healthchecks only run on running containers: a stopped one keeps the
    // daemon's last health value, and counting it would book the same
    // container as both stopped and unhealthy.
    it('does not count a stopped container as unhealthy', async () => {
        h.state.cycles = [[
            stat({name: 'gone', state: 'exited', health: 'unhealthy'}),
            stat({name: 'sick', state: 'running', health: 'unhealthy'}),
        ]]
        const {result} = renderHook(() => useDockerStats())
        await settle()

        expect(result.current.aggregates?.stopped).toBe(1)
        expect(result.current.aggregates?.unhealthy).toBe(1)
        expect(result.current.aggregates?.running).toBe(1)
    })

    it('ignores a negative cpu reading instead of subtracting it', async () => {
        h.state.cycles = [[
            stat({name: 'web', cpuUsage: 12}),
            // identity-only row: streamed ahead of its metrics so the view
            // paints fast, and never a data point
            stat({name: 'pending', cpuUsage: -1}),
        ]]
        const {result} = renderHook(() => useDockerStats())
        await settle()

        expect(result.current.aggregates?.cpu).toBe(12)
        expect(result.current.aggregates?.total).toBe(1)
    })
})

describe('useDockerStats: history', () => {
    it('keys history by name so a recreate does not restart the chart', async () => {
        h.state.cycles = [
            [stat({id: 'aaaa', name: 'web', cpuUsage: 10})],
            // same container after a compose recreate: new id, same name
            [stat({id: 'bbbb', name: 'web', cpuUsage: 20})],
        ]
        const {result} = renderHook(() => useDockerStats())
        await settle()
        await settle(5000)

        expect(result.current.history.get('web')?.cpu).toEqual([10, 20])
    })

    it('drops the history of a container that vanished from a host-wide cycle', async () => {
        h.state.cycles = [
            [stat({name: 'web'}), stat({name: 'db'})],
            [stat({name: 'web'})],
        ]
        const {result} = renderHook(() => useDockerStats())
        await settle()
        expect([...result.current.history.keys()].sort()).toEqual(['db', 'web'])

        await settle(5000)
        expect([...result.current.history.keys()]).toEqual(['web'])
    })

    // A stack tab only ever sees its own containers. Pruning from there would
    // evict every other stack's history on each of its cycles.
    it('keeps other containers when the cycle is scoped to one stack', async () => {
        h.state.cycles = [[stat({name: 'web'}), stat({name: 'db'})]]
        const {result, rerender} = renderHook(
            ({page}: { page?: string }) => useDockerStats(page),
            {initialProps: {page: undefined as string | undefined}},
        )
        await settle()
        expect([...result.current.history.keys()].sort()).toEqual(['db', 'web'])

        h.state.cycles = [[stat({name: 'web'})]]
        rerender({page: 'web/compose.yaml'})
        await settle()

        expect([...result.current.history.keys()].sort()).toEqual(['db', 'web'])
    })

    // The React Compiler memoizes lookups like history.get(name) by reference.
    // Served the same Map object, the sparklines would keep the geometry they
    // cached on their first render however many points arrive after it.
    it('publishes a new map identity on every cycle', async () => {
        h.state.cycles = [[stat({name: 'web'})]]
        const {result} = renderHook(() => useDockerStats())
        await settle()
        const first = result.current.history

        await settle(5000)
        expect(result.current.history).not.toBe(first)
    })
})

describe('useDockerStats: resetContainerStats', () => {
    // After an action on a container its old samples describe a process that
    // no longer exists. They must not stay on screen, and the header totals
    // computed from them must not be shown as current either.
    it('drops the rows, their history and the stale header in one go', async () => {
        h.state.cycles = [[stat({name: 'web'}), stat({name: 'db'})]]
        const {result} = renderHook(() => useDockerStats())
        await settle()
        expect(result.current.containers).toHaveLength(2)
        expect(result.current.aggregates).not.toBeNull()

        act(() => result.current.resetContainerStats(['web']))

        expect(result.current.containers.map(c => c.name)).toEqual(['db'])
        expect(result.current.history.has('web')).toBe(false)
        expect(result.current.aggregates).toBeNull()
    })

    it('does nothing when asked to reset nothing', async () => {
        h.state.cycles = [[stat({name: 'web'})]]
        const {result} = renderHook(() => useDockerStats())
        await settle()
        const before = result.current.aggregates

        act(() => result.current.resetContainerStats([]))
        expect(result.current.aggregates).toBe(before)
        expect(result.current.containers).toHaveLength(1)
    })
})

describe('useHostStats', () => {
    const hostSample = {cpuPercent: 40, memUsed: 4n * GB, memTotal: 16n * GB, cpus: 8}

    it('reads nothing while disabled', async () => {
        h.state.hostStatsValue = hostSample
        renderHook(() => useHostStats(false))
        await settle(60_000)
        expect(h.state.hostStatsCalls).toBe(0)
    })

    it('polls while the tab is visible', async () => {
        h.state.hostStatsValue = hostSample
        const {result} = renderHook(() => useHostStats(true))
        await settle()
        expect(h.state.hostStatsCalls).toBe(1)
        expect(result.current?.memTotal).toBe(Number(16n * GB))

        await settle(5000)
        expect(h.state.hostStatsCalls).toBe(2)
    })

    it('stays silent while the tab is hidden and refreshes on return', async () => {
        h.state.hostStatsValue = hostSample
        renderHook(() => useHostStats(true))
        await settle()
        expect(h.state.hostStatsCalls).toBe(1)

        act(() => setDocumentVisibility('hidden'))
        await settle(10 * 60_000)
        expect(h.state.hostStatsCalls).toBe(1)

        setDocumentVisibility('visible')
        await settle(0)
        expect(h.state.hostStatsCalls).toBe(2)
    })

    // memTotal of 0 means the reading failed; publishing it would render a
    // divide-by-zero percentage in the header gauge.
    it('ignores a reading with no total memory', async () => {
        h.state.hostStatsValue = {...hostSample, memTotal: 0n}
        const {result} = renderHook(() => useHostStats(true))
        await settle()
        expect(result.current).toBeNull()
    })
})
