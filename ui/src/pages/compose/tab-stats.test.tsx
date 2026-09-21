import {beforeEach, describe, expect, it, vi} from 'vitest'
import {render, screen} from '@testing-library/react'

const MB = 1024 * 1024
const GB = 1024 * MB

const h = vi.hoisted(() => ({
    memTotalRequests: [] as boolean[],
    hostStatsRequests: [] as boolean[],
}))

// The data hooks are replaced by fixed readings: what is under test is which
// reading each view asks for and hands to the aggregate band.
vi.mock('../../hooks/docker-containers-stats.ts', () => ({
    useDockerStats: () => ({
        containers: [],
        history: new Map(),
        // three containers capped at 512 MB, 400 MB used each
        aggregates: {
            total: 3, running: 3, stopped: 0, paused: 0, restarting: 0, unhealthy: 0,
            cpu: 1, memUsed: 1200 * MB, memLimitSum: 3 * 512 * MB,
            netRx: 0, netTx: 0, diskR: 0, diskW: 0, cpuHistory: [], memHistory: [],
        },
        loading: false,
        handleSortChange: () => {},
        sortOrder: 0,
        sortField: 0,
    }),
    useHostStats: (enabled: boolean) => {
        h.hostStatsRequests.push(enabled)
        return null
    },
    useHostMemTotal: (enabled: boolean) => {
        h.memTotalRequests.push(enabled)
        return enabled ? 16 * GB : 0
    },
}))
vi.mock('./components/container-stat-table.tsx', () => ({ContainerStatTable: () => null}))
vi.mock('./state/files.ts', () => ({
    useHostStore: (select: (s: { host: string }) => unknown) => select({host: 'local'}),
    useAliasStore: () => null,
}))

const {TabStat} = await import('./tab-stats.tsx')

beforeEach(() => {
    h.memTotalRequests = []
    h.hostStatsRequests = []
})

describe('TabStat memory ceiling', () => {
    // The stack tab of the editor: the band must cap the stack's summed limits
    // with the host's total - not show "234.4% of 512 MB".
    it('gives a stack view the host total to cap its summed limits', () => {
        render(<TabStat selectedPage="stacks/app/compose.yaml"/>)
        expect(h.memTotalRequests.every(Boolean)).toBe(true)
        expect(h.hostStatsRequests.some(Boolean)).toBe(false)
        expect(screen.getByText('78.1% of 1.5 GB')).toBeTruthy()
    })

    // The host view reads the real host usage instead, and must not pay for a
    // second host reading it does not use.
    it('does not read the host total separately in the host view', () => {
        render(<TabStat selectedPage=""/>)
        expect(h.memTotalRequests.some(Boolean)).toBe(false)
        expect(h.hostStatsRequests.every(Boolean)).toBe(true)
    })
})
