import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'
import {act, renderHook} from '@testing-library/react'
import {useSaveStatus} from './status-hook.tsx'

beforeEach(() => vi.useFakeTimers())
afterEach(() => vi.useRealTimers())

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
})
