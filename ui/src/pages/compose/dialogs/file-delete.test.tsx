import {beforeEach, describe, expect, it, vi} from 'vitest'
import {act, fireEvent, render, screen, waitFor} from '@testing-library/react'
import {TYPED_CONFIRMATION} from '../../../components/typed-confirmation-field.tsx'

const deleteFile = vi.fn()
const listFiles = vi.fn()
let trackedFile: Record<string, unknown> | undefined

vi.mock('../../../context/file-context.tsx', () => ({
    useFiles: () => ({deleteFile, listFiles}),
}))
vi.mock('../state/files.ts', () => ({
    useHostStore: (selector: (s: {host: string}) => unknown) => selector({host: 'local'}),
}))
vi.mock('../../../components/git-stack-status-store.ts', () => ({
    useGitTrackedFileInfo: () => trackedFile,
    reconcileDeletedGitFile: vi.fn(async () => {}),
    refreshGitStackStatuses: vi.fn(async () => {}),
    refreshGitTrackedFile: vi.fn(async () => {}),
}))
vi.mock('../../../hooks/snackbar.ts', () => ({
    useSnackbar: () => ({showError: vi.fn(), showSuccess: vi.fn()}),
}))

const {default: FileDelete, useFileDelete} = await import('./file-delete.tsx')

const typeConfirmation = () =>
    fireEvent.change(screen.getByRole('textbox'), {target: {value: TYPED_CONFIRMATION}})

describe('FileDelete', () => {
    beforeEach(() => {
        deleteFile.mockReset().mockResolvedValue(true)
        listFiles.mockReset()
        trackedFile = undefined
        vi.unstubAllGlobals()
        act(() => useFileDelete.getState().close())
    })

    // The server has always required the typed word for delete_git. Sending a
    // hard-coded constant answered its own question, so the gate never fired
    // here - while the stack synchronization popup asked for it on the very
    // same operation.
    it('sends the word the user typed, not a constant', async () => {
        const fetchMock = vi.fn<typeof fetch>(async () => new Response(
            JSON.stringify({message: 'deleted'}), {status: 200, headers: {'Content-Type': 'application/json'}}))
        vi.stubGlobal('fetch', fetchMock)
        trackedFile = {
            tracked: true, mutable: true, bindingId: 'b1',
            composePath: 'stack/compose.yaml', relativePath: 'stack/.env', path: 'stack/.env',
        }

        act(() => useFileDelete.getState().open('stack/.env'))
        render(<FileDelete/>)

        const gitButton = screen.getByRole('button', {name: 'Delete locally and from Git'})
        expect((gitButton as HTMLButtonElement).disabled).toBe(true)

        typeConfirmation()
        expect((gitButton as HTMLButtonElement).disabled).toBe(false)
        fireEvent.click(gitButton)

        await waitFor(() => expect(fetchMock).toHaveBeenCalled())
        const body = JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))
        expect(body).toMatchObject({action: 'delete_git', confirmation: TYPED_CONFIRMATION})
    })

    // Cancelling while the Folder Link verification was still in flight left
    // `busy` true for good: every button disabled, and onDelete returning at
    // its own guard. The delete dialog was dead until a page reload.
    it('recovers when a Folder Link check is abandoned mid-flight', async () => {
        let release: (() => void) | undefined
        vi.stubGlobal('fetch', vi.fn<typeof fetch>(() => new Promise<Response>((resolve) => {
            release = () => resolve(new Response('{}', {status: 200, headers: {'Content-Type': 'application/json'}}))
        })))
        trackedFile = {folderLinkRoot: true, bindingId: 'b1', tracked: true, mutable: true}

        act(() => useFileDelete.getState().open('stack', true))
        const {rerender} = render(<FileDelete/>)

        // Abandon the dialog while the verification has not answered.
        trackedFile = undefined
        act(() => useFileDelete.getState().close())
        rerender(<FileDelete/>)
        act(() => release?.())

        // Reopen on an ordinary file: it must be operable again.
        act(() => useFileDelete.getState().open('stack/compose.yaml'))
        rerender(<FileDelete/>)

        const button = screen.getByRole('button', {name: 'Delete'}) as HTMLButtonElement
        expect(button.disabled).toBe(false)
        fireEvent.click(button)
        await waitFor(() => expect(deleteFile).toHaveBeenCalledWith('stack/compose.yaml'))
    })
})
