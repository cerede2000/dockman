import {beforeEach, describe, expect, it, vi} from 'vitest'
import {act, fireEvent, render, screen} from '@testing-library/react'

const navigate = vi.fn()

vi.mock('react-router', () => ({useNavigate: () => navigate}))
vi.mock('../state/terminal.tsx', () => ({
    useFileComponents: () => ({alias: 'compose', host: 'local'}),
}))
vi.mock('../../../lib/api.ts', () => ({
    getBaseUrl: () => '/api/host/local',
    getWSUrl: (url: string) => `ws://test${url}`,
}))
vi.mock('../../../lib/editor.ts', () => ({
    useEditorUrl: () => (filename: string) => `/local/files/${filename}`,
}))

class FakeSocket {
    static last: FakeSocket | undefined
    onopen: (() => void) | null = null
    onmessage: ((ev: {data: string}) => void) | null = null
    onerror: (() => void) | null = null
    readyState = 1
    sent: string[] = []
    constructor() { FakeSocket.last = this }
    send(data: string) { this.sent.push(data) }
    close() { this.readyState = 3 }
}

const {default: FileSearch, useFileSearch} = await import('./file-search.tsx')

const openDialog = () => {
    act(() => useFileSearch.getState().open())
    render(<FileSearch/>)
}
const field = () => screen.getByPlaceholderText('Search files by name...')

// React does not rethrow synchronously out of an event handler, so a plain
// expect(...).not.toThrow() around fireEvent passes whether the handler blew
// up or not. jsdom reports the uncaught error on window instead; that is what
// has to be watched.
const errorsWhile = (action: () => void): unknown[] => {
    const seen: unknown[] = []
    const onError = (event: ErrorEvent) => {
        seen.push(event.error ?? event.message)
        event.preventDefault()
    }
    window.addEventListener('error', onError)
    try {
        action()
    } finally {
        window.removeEventListener('error', onError)
    }
    return seen
}

describe('FileSearch', () => {
    beforeEach(() => {
        navigate.mockReset()
        FakeSocket.last = undefined
        vi.stubGlobal('WebSocket', FakeSocket as unknown as typeof WebSocket)
        act(() => useFileSearch.getState().close())
    })

    // activeIndex is set to 0 the moment the debounced query settles, whether
    // or not anything matched, so Enter read .Value off undefined and took the
    // dialog down. Typing a name that does not exist was enough.
    it('does nothing on Enter when nothing matched', () => {
        openDialog()
        act(() => FakeSocket.last?.onopen?.())
        act(() => FakeSocket.last?.onmessage?.({data: JSON.stringify({results: []})}))

        expect(errorsWhile(() => fireEvent.keyDown(field(), {key: 'Enter'}))).toEqual([])
        expect(navigate).not.toHaveBeenCalled()
        expect(screen.getByText('Start typing to find files...')).toBeTruthy()
    })

    it('still opens the highlighted result on Enter', () => {
        openDialog()
        act(() => FakeSocket.last?.onopen?.())
        act(() => FakeSocket.last?.onmessage?.({
            data: JSON.stringify({results: [{Value: 'adguard/compose.yaml', Indexes: [0, 1]}]}),
        }))

        fireEvent.keyDown(field(), {key: 'Enter'})
        expect(navigate).toHaveBeenCalledWith('/local/files/compose/adguard/compose.yaml')
    })

    it('reports an unreadable frame instead of throwing out of the socket', () => {
        openDialog()
        act(() => FakeSocket.last?.onopen?.())
        expect(errorsWhile(() => act(() => FakeSocket.last?.onmessage?.({data: 'not json'})))).toEqual([])
        expect(screen.getByText(/unreadable response/)).toBeTruthy()
    })
})
