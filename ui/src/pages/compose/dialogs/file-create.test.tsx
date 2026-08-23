import {beforeEach, describe, expect, it, vi} from 'vitest'
import {act, fireEvent, render, screen, waitFor} from '@testing-library/react'

const addFile = vi.fn()
const copyFile = vi.fn()

vi.mock('../../../context/file-context.tsx', () => ({
    useFiles: () => ({addFile, copyFile}),
}))

const {default: FileCreate, useFileCreate} = await import('./file-create.tsx')

const dialogIsOpen = () => Boolean(useFileCreate.getState().rootPath)
const nameField = () => screen.getByPlaceholderText('Enter name...') as HTMLInputElement
const button = (name: string) => screen.getByRole('button', {name})

describe('FileCreate', () => {
    beforeEach(() => {
        addFile.mockReset()
        copyFile.mockReset()
        act(() => useFileCreate.getState().closeDialog())
    })

    // The defect: addFile announced the failure through the snackbar and
    // returned normally, so the dialog closed on top of it - taking the name
    // that had just been refused, and every keystroke that produced it.
    it('keeps the dialog and the typed name when creation is refused', async () => {
        addFile.mockResolvedValue({ok: false, err: 'file already exists'})

        act(() => useFileCreate.getState().open('compose'))
        render(<FileCreate/>)

        fireEvent.click(screen.getByText('New File'))
        fireEvent.change(nameField(), {target: {value: 'compose.yaml'}})
        fireEvent.click(button('Create'))

        await waitFor(() => expect(addFile).toHaveBeenCalledWith('compose/compose.yaml', false))
        expect(dialogIsOpen()).toBe(true)
        expect(nameField().value).toBe('compose.yaml')
    })

    it('closes once creation succeeds', async () => {
        addFile.mockResolvedValue({ok: true, err: ''})

        act(() => useFileCreate.getState().open('compose'))
        render(<FileCreate/>)

        fireEvent.click(screen.getByText('New File'))
        fireEvent.change(nameField(), {target: {value: 'compose.yaml'}})
        fireEvent.click(button('Create'))

        await waitFor(() => expect(dialogIsOpen()).toBe(false))
    })

    // Duplicate opens straight at the name step, so there is no earlier step:
    // the button read Back and led into a type chooser that flow never had.
    it('offers Cancel, not Back, when duplicating', () => {
        act(() => useFileCreate.getState().open('compose', true, 'compose/a.yaml', false))
        render(<FileCreate/>)

        expect(button('Cancel')).toBeTruthy()
        expect(screen.queryByRole('button', {name: 'Back'})).toBeNull()
    })

    it('still offers Back when creating, which does have an earlier step', () => {
        act(() => useFileCreate.getState().open('compose'))
        render(<FileCreate/>)

        fireEvent.click(screen.getByText('New File'))
        expect(button('Back')).toBeTruthy()
    })
})
