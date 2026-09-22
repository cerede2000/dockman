import {describe, expect, it} from 'vitest'
import {buildLimitErrors, cpusText, memoryText, parseCpus, parseMemory} from './build-limits.ts'

const GIB = 1024n ** 3n
const MIB = 1024n ** 2n

describe('build limit fields', () => {
    it('reads empty as no limit', () => {
        expect(parseCpus('')).toBe(0)
        expect(parseMemory('')).toBe(0n)
        expect(buildLimitErrors('', '')).toEqual({})
    })

    it('stores memory in bytes at MiB precision', () => {
        expect(parseMemory('2')).toBe(2n * GIB)
        expect(parseMemory('0.75')).toBe(768n * MIB)
    })

    it('shows stored values back as typed', () => {
        expect(cpusText(1.5)).toBe('1.5')
        expect(cpusText(0)).toBe('')
        expect(memoryText(2n * GIB)).toBe('2')
        expect(memoryText(0n)).toBe('')
    })

    // The same bounds as compose.BuildLimits.Validate on the server.
    it('refuses what BuildKit cannot run under', () => {
        expect(buildLimitErrors('0.05', '').cpu).toBeDefined()
        expect(buildLimitErrors('-1', '').cpu).toBeDefined()
        expect(buildLimitErrors('', '0.1').memory).toBeDefined()
        expect(buildLimitErrors('0.1', '0.25')).toEqual({})
    })
})
