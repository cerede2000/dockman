import {describe, expect, it, vi} from 'vitest'
import {render, screen} from '@testing-library/react'
import type {AggregateSnapshot, HostStatsView} from '../../../hooks/docker-containers-stats.ts'

// The component takes only types from the stats hook and only formatBytes from
// lib/editor.ts, but both modules load the compose state chain, down to a store
// reading localStorage at load time - broken under Node's experimental
// localStorage. Nothing of that chain runs here.
vi.mock('../../../hooks/docker-containers-stats.ts', () => ({}))
vi.mock('../state/files.ts', () => ({useAliasStore: () => null, useHostStore: () => null}))

const {default: AggregateStats} = await import('./container-stat-chart.tsx')

const MB = 1024 * 1024
const GB = 1024 * MB

function snapshot(over: Partial<AggregateSnapshot>): AggregateSnapshot {
    return {
        total: 3, running: 3, stopped: 0, paused: 0, restarting: 0, unhealthy: 0,
        cpu: 1, memUsed: 0, memLimitSum: 0,
        netRx: 0, netTx: 0, diskR: 0, diskW: 0,
        cpuHistory: [], memHistory: [],
        ...over,
    }
}

// The memory tile's second line: "<percent>% of <ceiling>", or nothing.
const memoryLine = () => screen.queryByText(/% of /)?.textContent ?? null

describe('AggregateStats memory tile, per-container aggregation (stack views)', () => {
    // The residual defect of the largest-limit fix: three containers capped at
    // 512 MB using 400 MB each read "234.4% of 512 MB".
    it('shares the summed limits of capped containers', () => {
        render(<AggregateStats hostMemTotal={16 * GB}
                               aggregates={snapshot({memUsed: 1200 * MB, memLimitSum: 3 * 512 * MB})}/>)
        expect(memoryLine()).toBe('78.1% of 1.5 GB')
    })

    // RA341/dockman#231: unlimited containers each report the host's RAM.
    it('counts the host once for unlimited containers', () => {
        render(<AggregateStats hostMemTotal={16 * GB}
                               aggregates={snapshot({memUsed: 8 * GB, memLimitSum: 15 * 16 * GB})}/>)
        expect(memoryLine()).toBe('50.0% of 16 GB')
    })

    it('shows the memory used without a percentage while the host total is unknown', () => {
        render(<AggregateStats hostMemTotal={0}
                               aggregates={snapshot({memUsed: 1200 * MB, memLimitSum: 3 * 512 * MB})}/>)
        expect(memoryLine()).toBeNull()
        expect(screen.getByText('1.17 GB')).toBeTruthy()
    })
})

describe('AggregateStats memory tile, host view', () => {
    it('reads the real host usage and ignores the container limits', () => {
        const hostStats: HostStatsView = {
            cpuPercent: 10, memUsed: 4 * GB, memTotal: 16 * GB, cpus: 8, cpuHistory: [], memHistory: [],
        }
        render(<AggregateStats hostStats={hostStats}
                               aggregates={snapshot({memUsed: 1 * GB, memLimitSum: 15 * 16 * GB})}/>)
        expect(memoryLine()).toBe('25.0% of 16 GB')
    })
})
