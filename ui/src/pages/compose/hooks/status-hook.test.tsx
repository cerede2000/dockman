import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'
import {act, renderHook} from '@testing-library/react'
import {restoreDocumentVisibility, setDocumentVisibility} from '../../../test/visibility.ts'
import {useSaveStatus} from './status-hook.tsx'

beforeEach(() => vi.useFakeTimers())
afterEach(() => {
    vi.useRealTimers()
    restoreDocumentVisibility()
})

const flush = async (ms = 0) => {
    await act(async () => {
        await vi.advanceTimersByTimeAsync(ms)
    })
}

describe('useSaveStatus', () => {
    it('saves once the typing settles', async () => {
        const onSave = vi.fn(async () => 'success' as const)
        const {result} = renderHook(() => useSaveStatus(500, 'compose.yml'))

        act(() => result.current.handleContentChange('services: {}', onSave))
        await flush(499)
        expect(onSave).not.toHaveBeenCalled()

        await flush(1)
        expect(onSave).toHaveBeenCalledExactlyOnceWith('services: {}')
    })

    // The editor has no manual save. Whatever the debounce is still holding is
    // the only copy of the last keystrokes.
    it('does not lose a pending save when the file changes', async () => {
        const onSave = vi.fn(async () => 'success' as const)
        const {result, rerender} = renderHook(
            ({name}: { name: string }) => useSaveStatus(500, name),
            {initialProps: {name: 'compose.yml'}},
        )

        act(() => result.current.handleContentChange('the last thing I typed', onSave))
        rerender({name: 'other.yml'})
        await flush(2000)

        expect(onSave).toHaveBeenCalledExactlyOnceWith('the last thing I typed')
    })

    it('does not lose a pending save when the editor closes', async () => {
        const onSave = vi.fn(async () => 'success' as const)
        const {result, unmount} = renderHook(() => useSaveStatus(500, 'compose.yml'))

        act(() => result.current.handleContentChange('typed then closed', onSave))
        unmount()
        await flush(2000)

        expect(onSave).toHaveBeenCalledExactlyOnceWith('typed then closed')
    })

    // React's cleanup does not run when the page itself goes away.
    it('saves when the tab is merely hidden', async () => {
        const onSave = vi.fn(async () => 'success' as const)
        const {result, unmount} = renderHook(() => useSaveStatus(500, 'compose.yml'))

        act(() => result.current.handleContentChange('typed then switched away', onSave))
        act(() => setDocumentVisibility('hidden'))

        expect(onSave).toHaveBeenCalledExactlyOnceWith('typed then switched away')

        // and the debounce that was still armed must not save it a second time
        await flush(2000)
        expect(onSave).toHaveBeenCalledTimes(1)
        unmount()
    })

    // Closing or reloading cannot be saved properly: the write would arrive
    // without its If-Match header and overwrite blind. Asking is the honest
    // answer, and only while something really is pending.
    it('asks before leaving with a save still pending, and never otherwise', async () => {
        const onSave = vi.fn(async () => 'success' as const)
        const {result, unmount} = renderHook(() => useSaveStatus(500, 'compose.yml'))

        const quiet = new Event('beforeunload', {cancelable: true})
        act(() => void window.dispatchEvent(quiet))
        expect(quiet.defaultPrevented).toBe(false)

        act(() => result.current.handleContentChange('typed then reloaded', onSave))
        const pending = new Event('beforeunload', {cancelable: true})
        act(() => void window.dispatchEvent(pending))
        expect(pending.defaultPrevented).toBe(true)

        await flush(600)
        const settled = new Event('beforeunload', {cancelable: true})
        act(() => void window.dispatchEvent(settled))
        expect(settled.defaultPrevented).toBe(false)
        unmount()
    })
})
