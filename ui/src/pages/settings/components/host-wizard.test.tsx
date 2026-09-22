import {beforeEach, describe, expect, it, vi} from 'vitest'
import {fireEvent, render, screen, waitFor} from '@testing-library/react'
import {memoryStorage} from '../../../test/memory-storage.ts'

const GIB = 1024n ** 3n

const h = vi.hoisted(() => ({
    editHost: vi.fn<(req: unknown) => Promise<object>>(async () => ({})),
    createHost: vi.fn<(req: unknown) => Promise<object>>(async () => ({})),
}))

vi.mock('../../../lib/api.ts', () => ({
    useClient: () => ({editHost: h.editHost, createHost: h.createHost}),
    callRPC: async (exec: () => Promise<unknown>) => ({val: await exec(), err: ''}),
}))
vi.mock('../../../hooks/snackbar.ts', () => ({useSnackbar: () => ({showSuccess: vi.fn(), showError: vi.fn()})}))
vi.mock('../../../lib/git-api.ts', () => ({gitAPI: async () => []}))
vi.mock('./alias-manager.tsx', () => ({default: () => null}))

vi.stubGlobal('localStorage', memoryStorage())
const {default: HostWizardDialog} = await import('./host-wizard.tsx')
const {ClientType} = await import('../../../gen/host/v1/host_pb.ts')

const existing = {
    id: 3, name: 'nas', hostAddr: '', kind: ClientType.LOCAL, enable: true, dockerSocket: '',
    folderAliasesCount: 1, buildCpuLimit: 1.5, buildMemoryLimit: 2n * GIB,
}

function openHost(host = existing) {
    render(<HostWizardDialog open onClose={() => {}} onSuccess={() => {}} host={host as never}/>)
}

const cpuField = () => screen.getByRole('textbox', {name: 'Build CPU cores'}) as HTMLInputElement
const memField = () => screen.getByRole('textbox', {name: 'Build memory GiB'}) as HTMLInputElement
const save = () => screen.getByRole('button', {name: 'Save Changes'}) as HTMLButtonElement

// what the last editHost call sent
function sentHost() {
    const req = h.editHost.mock.calls.at(-1)?.[0] as { host: { buildCpuLimit: number; buildMemoryLimit: bigint } }
    return req.host
}

beforeEach(() => {
    h.editHost.mockClear()
    h.createHost.mockClear()
})

describe('HostWizardDialog build limits', () => {
    it('shows the host limits', () => {
        openHost()
        expect(cpuField().value).toBe('1.5')
        expect(memField().value).toBe('2')
    })

    // Deriving the field from the parsed number turned "0." back into an empty
    // field: "0.5" could not be typed.
    it('keeps a decimal being typed', () => {
        openHost()
        fireEvent.change(cpuField(), {target: {value: '0'}})
        fireEvent.change(cpuField(), {target: {value: '0.'}})
        expect(cpuField().value).toBe('0.')
        fireEvent.change(cpuField(), {target: {value: '0.5'}})
        expect(cpuField().value).toBe('0.5')
    })

    it('refuses text that is not a number', () => {
        openHost()
        fireEvent.change(memField(), {target: {value: 'two'}})
        expect(save().disabled).toBe(true)
        expect(screen.getByText(/At least 0.25 GiB/)).toBeTruthy()
    })

    it('saves the limits the API expects', async () => {
        openHost()
        fireEvent.change(cpuField(), {target: {value: '0.5'}})
        fireEvent.change(memField(), {target: {value: '0.75'}})
        fireEvent.click(save())

        await waitFor(() => expect(h.editHost).toHaveBeenCalled())
        expect(sentHost().buildCpuLimit).toBe(0.5)
        expect(sentHost().buildMemoryLimit).toBe(768n * 1024n ** 2n)
    })

    it('saves emptied fields as no limit', async () => {
        openHost()
        fireEvent.change(cpuField(), {target: {value: ''}})
        fireEvent.change(memField(), {target: {value: ''}})
        fireEvent.click(save())

        await waitFor(() => expect(h.editHost).toHaveBeenCalled())
        expect(sentHost().buildCpuLimit).toBe(0)
        expect(sentHost().buildMemoryLimit).toBe(0n)
    })

    it('refuses a limit BuildKit cannot run under, and says why', () => {
        openHost()
        fireEvent.change(cpuField(), {target: {value: '0.05'}})
        expect(save().disabled).toBe(true)
        expect(screen.getByText(/At least 0.1 core/)).toBeTruthy()
    })

    it('starts a new host without limits', () => {
        render(<HostWizardDialog open onClose={() => {}} onSuccess={() => {}}/>)
        expect(cpuField().value).toBe('')
        expect(memField().value).toBe('')
    })
})
