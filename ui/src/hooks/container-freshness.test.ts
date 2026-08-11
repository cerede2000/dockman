import {afterEach, describe, expect, it, vi} from 'vitest'
import {FAST_POLL_MS, IDLE_POLL_MS, isSettling} from './container-freshness.ts'

const NOW = new Date('2026-08-11T12:00:00Z')
const ago = (ms: number) => new Date(NOW.getTime() - ms).toISOString()

describe('isSettling', () => {
    afterEach(() => {
        vi.useRealTimers()
    })

    const settled = () => {
        vi.useFakeTimers()
        vi.setSystemTime(NOW)
    }

    it('is true while the container is in a transitional state', () => {
        settled()
        for (const state of ['created', 'restarting', 'removing']) {
            expect(isSettling(state, 'healthy', ago(3600_000))).toBe(true)
        }
    })

    it('reads the state case-insensitively, like the daemon writes it', () => {
        settled()
        expect(isSettling('Restarting', 'healthy', ago(3600_000))).toBe(true)
        expect(isSettling('running', 'STARTING', ago(3600_000))).toBe(true)
    })

    it('is true while a health check has not settled yet', () => {
        settled()
        expect(isSettling('running', 'starting', ago(3600_000))).toBe(true)
    })

    // A container that just started must visibly count up in the uptime column
    // rather than jump a minute at a time.
    it('is true for a minute after the container was created', () => {
        settled()
        expect(isSettling('running', 'healthy', ago(59_000))).toBe(true)
        expect(isSettling('running', 'healthy', ago(61_000))).toBe(false)
    })

    it('is false once everything is stable', () => {
        settled()
        expect(isSettling('running', 'healthy', ago(3600_000))).toBe(false)
        expect(isSettling('exited', '', ago(3600_000))).toBe(false)
    })

    // The created timestamp is whatever the daemon reported. An unparseable one
    // must not pin the view to the fast cadence forever.
    it('falls back to the slow cadence when the timestamp is unusable', () => {
        settled()
        expect(isSettling('running', 'healthy', 'not a date')).toBe(false)
        expect(isSettling('running', 'healthy', '')).toBe(false)
    })

    it('keeps the fast cadence well under the idle one', () => {
        expect(FAST_POLL_MS).toBeLessThan(IDLE_POLL_MS)
    })
})
