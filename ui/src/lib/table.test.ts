import {describe, expect, it} from 'vitest'
import {formatTimeAgo, sortTable, type TableInfo} from './table.ts'

interface Row {
    name: string
    size: bigint
    cpu: number
    born: Date
}

const unused = () => {
    throw new Error('sortTable must only ever call getValue')
}

const columns: TableInfo<Row> = {
    name: {header: unused, cell: unused, getValue: r => r.name},
    size: {header: unused, cell: unused, getValue: r => r.size},
    cpu: {header: unused, cell: unused, getValue: r => r.cpu},
    born: {header: unused, cell: unused, getValue: r => r.born},
}

const rows: Row[] = [
    {name: 'traefik', size: 30n, cpu: 2.5, born: new Date('2026-01-03T00:00:00Z')},
    {name: 'authelia', size: 200n, cpu: 0.5, born: new Date('2026-01-01T00:00:00Z')},
    {name: 'postgres', size: 100n, cpu: 11, born: new Date('2026-01-02T00:00:00Z')},
]

const names = (list: Row[]) => list.map(r => r.name)

describe('sortTable', () => {
    it('leaves the input array alone', () => {
        const before = names(rows)
        sortTable(rows, 'name', columns, 'desc')
        expect(names(rows)).toEqual(before)
    })

    it('sorts strings both ways', () => {
        expect(names(sortTable(rows, 'name', columns, 'asc'))).toEqual(['authelia', 'postgres', 'traefik'])
        expect(names(sortTable(rows, 'name', columns, 'desc'))).toEqual(['traefik', 'postgres', 'authelia'])
    })

    // Sizes are uint64 on the wire and arrive as bigint; comparing them by
    // subtraction throws, and comparing them as strings puts 30 after 200.
    it('sorts bigints by value, not as text', () => {
        expect(names(sortTable(rows, 'size', columns, 'asc'))).toEqual(['traefik', 'postgres', 'authelia'])
    })

    it('sorts numbers by value', () => {
        expect(names(sortTable(rows, 'cpu', columns, 'asc'))).toEqual(['authelia', 'traefik', 'postgres'])
    })

    it('sorts dates chronologically', () => {
        expect(names(sortTable(rows, 'born', columns, 'asc'))).toEqual(['authelia', 'postgres', 'traefik'])
    })

    // Sort fields are remembered per view and come back from config, so a
    // column that no longer exists must not throw or scramble the table.
    it('falls back to a real column when the field is unknown', () => {
        expect(() => sortTable(rows, 'gone', columns, 'asc')).not.toThrow()
        expect(names(sortTable(rows, 'gone', columns, 'asc'))).toHaveLength(3)
    })
})

describe('formatTimeAgo', () => {
    const now = new Date('2026-08-11T12:00:00Z').getTime()
    const ago = (ms: number) => new Date(now - ms)

    // `now` is an explicit input so a memoized caller can tick a re-render and
    // have the label actually recompute.
    it('reads the clock the caller passes', () => {
        const stamp = ago(90_000)
        expect(formatTimeAgo(stamp, now)).toBe('2 minutes ago')
        expect(formatTimeAgo(stamp, now + 3600_000)).toBe('1 hour ago')
    })

    it('picks the unit that fits the distance', () => {
        expect(formatTimeAgo(ago(5_000), now)).toBe('5 seconds ago')
        expect(formatTimeAgo(ago(120_000), now)).toBe('2 minutes ago')
        expect(formatTimeAgo(ago(3 * 3600_000), now)).toBe('3 hours ago')
        expect(formatTimeAgo(ago(2 * 86400_000), now)).toBe('2 days ago')
        expect(formatTimeAgo(ago(60 * 86400_000), now)).toBe('2 months ago')
        expect(formatTimeAgo(ago(400 * 86400_000), now)).toBe('1 year ago')
    })
})
