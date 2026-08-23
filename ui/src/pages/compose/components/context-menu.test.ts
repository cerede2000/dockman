import {describe, expect, it, vi} from 'vitest'
import {browserMenuRequested, letBrowserMenuThrough} from './context-menu.ts'

describe('the browser context-menu escape hatch', () => {
    it('is requested by holding Shift', () => {
        expect(browserMenuRequested({shiftKey: true})).toBe(true)
        expect(browserMenuRequested({shiftKey: false})).toBe(false)
    })

    // Monaco must never see the event, or it calls preventDefault and shows its
    // own menu instead of the browser's.
    it('stops the event before Monaco can see it', () => {
        const event = {shiftKey: true, stopPropagation: vi.fn(), preventDefault: vi.fn()}
        letBrowserMenuThrough(event as unknown as MouseEvent)
        expect(event.stopPropagation).toHaveBeenCalledOnce()
        // preventDefault would suppress the very menu we are trying to show
        expect(event.preventDefault).not.toHaveBeenCalled()
    })

    it('leaves a plain right-click entirely alone', () => {
        const event = {shiftKey: false, stopPropagation: vi.fn(), preventDefault: vi.fn()}
        letBrowserMenuThrough(event as unknown as MouseEvent)
        expect(event.stopPropagation).not.toHaveBeenCalled()
        expect(event.preventDefault).not.toHaveBeenCalled()
    })
})
