import {describe, expect, it} from 'vitest'
import {memoryCeiling} from './memory-ceiling.ts'

const MB = 1024 * 1024
const GB = 1024 * MB
const HOST = 16 * GB

// A container without an explicit limit reports the host's RAM as its limit.
const unlimited = HOST

describe('memoryCeiling', () => {
    // Reported upstream (RA341/dockman#231): 15 unconstrained containers on a
    // 16 GB host summed to ~240 GB and read "3.2 % of total limits".
    it('counts the host once when no container has a limit', () => {
        const limits = Array(15).fill(unlimited)
        expect(memoryCeiling(limits.reduce((a, b) => a + b, 0), HOST)).toBe(HOST)
    })

    // The case the largest-limit shortcut got wrong: three containers capped
    // at 512 MB share 1.5 GB, not 512 MB ("234 %" for 1.2 GB used).
    it('adds up explicit limits', () => {
        expect(memoryCeiling(3 * 512 * MB, HOST)).toBe(1536 * MB)
    })

    // One unlimited container can use the whole host whatever the others are
    // capped at, so the host is the ceiling.
    it('is the host as soon as one container is unlimited', () => {
        expect(memoryCeiling(512 * MB + unlimited, HOST)).toBe(HOST)
    })

    it('never exceeds the host even when the caps promise more', () => {
        expect(memoryCeiling(2 * 12 * GB, HOST)).toBe(HOST)
    })

    // Without the host total the two cases above are indistinguishable:
    // 0 means "unknown", never a guess.
    it('is unknown while the host total is unknown', () => {
        expect(memoryCeiling(3 * 512 * MB, 0)).toBe(0)
        expect(memoryCeiling(3 * 512 * MB, NaN)).toBe(0)
    })

    it('is unknown when no limit was reported', () => {
        expect(memoryCeiling(0, HOST)).toBe(0)
    })
})
