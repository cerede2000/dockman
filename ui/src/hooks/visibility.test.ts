import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'
import {documentIsVisible, pollWhileVisible, whenVisible} from './visibility.ts'
import {restoreDocumentVisibility, setDocumentVisibility} from '../test/visibility.ts'

const MINUTE = 60_000

describe('documentIsVisible', () => {
    afterEach(restoreDocumentVisibility)

    it('reports a visible document', () => {
        setDocumentVisibility('visible')
        expect(documentIsVisible()).toBe(true)
    })

    it('reports a hidden document', () => {
        setDocumentVisibility('hidden')
        expect(documentIsVisible()).toBe(false)
    })
})

describe('whenVisible', () => {
    afterEach(restoreDocumentVisibility)

    it('fires when the document comes back and not when it leaves', () => {
        const onVisible = vi.fn()
        const stop = whenVisible(onVisible)

        setDocumentVisibility('hidden')
        expect(onVisible).not.toHaveBeenCalled()

        setDocumentVisibility('visible')
        expect(onVisible).toHaveBeenCalledTimes(1)

        stop()
    })

    it('stops listening once unsubscribed', () => {
        const onVisible = vi.fn()
        const stop = whenVisible(onVisible)
        stop()

        setDocumentVisibility('hidden')
        setDocumentVisibility('visible')
        expect(onVisible).not.toHaveBeenCalled()
    })
})

describe('pollWhileVisible', () => {
    beforeEach(() => {
        vi.useFakeTimers()
        setDocumentVisibility('visible')
    })

    afterEach(() => {
        vi.useRealTimers()
        restoreDocumentVisibility()
    })

    it('runs at once and then on its cadence', () => {
        const run = vi.fn()
        const stop = pollWhileVisible(run, 5000)

        expect(run).toHaveBeenCalledTimes(1)
        vi.advanceTimersByTime(15_000)
        expect(run).toHaveBeenCalledTimes(4)

        stop()
    })

    // The finding this whole module exists for: a background tab keeps firing
    // its timers, roughly once a minute, and every firing was a real call to
    // the Docker daemon.
    it('does not call once in ten minutes while the tab is hidden', () => {
        const run = vi.fn()
        setDocumentVisibility('hidden')
        const stop = pollWhileVisible(run, 5000)

        vi.advanceTimersByTime(10 * MINUTE)
        expect(run).not.toHaveBeenCalled()

        stop()
    })

    it('refreshes the moment the tab comes back, and resumes its cadence', () => {
        const run = vi.fn()
        const stop = pollWhileVisible(run, 5000)
        expect(run).toHaveBeenCalledTimes(1)

        setDocumentVisibility('hidden')
        vi.advanceTimersByTime(10 * MINUTE)
        expect(run).toHaveBeenCalledTimes(1)

        // returning to the tab must show a fresh reading, not the one taken
        // ten minutes ago
        setDocumentVisibility('visible')
        expect(run).toHaveBeenCalledTimes(2)

        vi.advanceTimersByTime(5000)
        expect(run).toHaveBeenCalledTimes(3)

        stop()
    })

    it('leaves nothing armed after teardown', () => {
        const run = vi.fn()
        const stop = pollWhileVisible(run, 5000)
        stop()

        vi.advanceTimersByTime(10 * MINUTE)
        setDocumentVisibility('hidden')
        setDocumentVisibility('visible')
        expect(run).toHaveBeenCalledTimes(1)
    })
})
