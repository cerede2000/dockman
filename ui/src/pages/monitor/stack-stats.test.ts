import {describe, expect, it} from 'vitest'
import {create} from '@bufbuild/protobuf'
import {ContainerListSchema, ContainerStatsSchema} from '../../gen/docker/v1/docker_pb.ts'
import type {MonitorRow} from './monitor-table.tsx'
import {aggregateStack} from './stack-stats.ts'

const MB = 1024n * 1024n
const GB = 1024n * MB
const HOST = Number(16n * GB)

function row(name: string, memoryUsage: bigint, memoryLimit: bigint): MonitorRow {
    return {
        info: create(ContainerListSchema, {name}),
        stats: create(ContainerStatsSchema, {name, cpuUsage: 1, memoryUsage, memoryLimit}),
        stackKey: 'stacks/app/compose.yaml',
    }
}

const noHistory = new Map<string, { cpu: number[]; mem: number[] }>()

// The Monitor's stack row prints "<used> / <ceiling>" and colours the value
// by the ratio of the two.
describe('aggregateStack memory ceiling', () => {
    // The largest-limit shortcut read "1.17 GB / 512 MB" here.
    it('adds up the limits of capped containers', () => {
        const rows = ['a', 'b', 'c'].map(n => row(n, 400n * MB, 512n * MB))
        const stats = aggregateStack(rows, noHistory, HOST)!
        expect(stats.memUsed).toBe(Number(1200n * MB))
        expect(stats.memLimit).toBe(Number(1536n * MB))
    })

    // RA341/dockman#231: unlimited containers each report the host's RAM.
    it('counts the host once for unlimited containers', () => {
        const rows = ['a', 'b', 'c', 'd'].map(n => row(n, GB, 16n * GB))
        expect(aggregateStack(rows, noHistory, HOST)!.memLimit).toBe(HOST)
    })

    it('has no ceiling while the host total is unknown', () => {
        const rows = ['a', 'b'].map(n => row(n, 400n * MB, 512n * MB))
        expect(aggregateStack(rows, noHistory, 0)!.memLimit).toBe(0)
    })

    it('has no stats before any member reported', () => {
        const pending: MonitorRow = {info: create(ContainerListSchema, {name: 'a'}), stackKey: 'k'}
        expect(aggregateStack([pending], noHistory, HOST)).toBeNull()
    })
})
