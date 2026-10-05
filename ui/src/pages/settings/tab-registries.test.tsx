import {beforeEach, describe, expect, it, vi} from 'vitest'
import {fireEvent, render, screen, waitFor, within} from '@testing-library/react'

// The snackbar is the only thing stubbed: it reaches for a provider this tab
// is not mounted under, exactly as the Views tab learned the hard way.
const notices: string[] = []
vi.mock('../../hooks/snackbar.ts', () => ({
    useSnackbar: () => ({
        showSuccess: (message: string) => notices.push(`ok:${message}`),
        showError: (message: string) => notices.push(`error:${message}`),
        showWarning: (message: string) => notices.push(`warn:${message}`),
    }),
}))

const {default: TabRegistries} = await import('./tab-registries.tsx')

interface Call { url: string; method: string; body?: string }

const calls: Call[] = []
let credentials: unknown[] = []

function mockFetch(responses: Record<string, unknown> = {}) {
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
        const method = init?.method ?? 'GET'
        calls.push({url, method, body: init?.body as string | undefined})
        const key = `${method} ${url.replace(/^.*\/registries/, '')}`
        const payload = key in responses ? responses[key] : credentials
        return {
            ok: true,
            status: 200,
            json: async () => payload,
        } as unknown as Response
    }))
}

beforeEach(() => {
    calls.length = 0
    notices.length = 0
    credentials = []
})

const stored = {
    uuid: 'u-1',
    registry: 'ghcr.io',
    username: 'robot',
    secretHint: 'ghp_…oken',
    hasSecret: true,
    updatedAt: '2026-10-05T08:00:00Z',
}

describe('Settings → Registries', () => {
    it('lists a stored login by its hint, never by its secret', async () => {
        credentials = [stored]
        mockFetch()
        render(<TabRegistries/>)

        const row = await screen.findByText('ghcr.io')
        expect(row).toBeTruthy()
        expect(screen.getByText('robot')).toBeTruthy()
        expect(screen.getByText('ghp_…oken')).toBeTruthy()
        expect(document.body.textContent).not.toContain('ghp_averysecrettoken')
    })

    it('sends the registry, the username and the secret when one is added', async () => {
        mockFetch({'POST /credentials': {uuid: 'u-2'}})
        render(<TabRegistries/>)
        await screen.findByText(/No registry credential yet/)

        fireEvent.click(screen.getByRole('button', {name: 'Add registry'}))
        fireEvent.change(screen.getByLabelText('Registry'), {target: {value: 'ghcr.io'}})
        fireEvent.change(screen.getByLabelText('Username'), {target: {value: 'robot'}})
        fireEvent.change(screen.getByLabelText('Password or token'), {target: {value: 'ghp_averysecrettoken'}})
        fireEvent.click(screen.getByRole('button', {name: 'Save'}))

        await waitFor(() => expect(calls.some(call => call.method === 'POST')).toBe(true))
        const created = calls.find(call => call.method === 'POST')!
        expect(created.url).toContain('/registries/credentials')
        expect(JSON.parse(created.body!)).toEqual({
            registry: 'ghcr.io', username: 'robot', secret: 'ghp_averysecrettoken',
        })
    })

    it('does not let an empty secret create a credential, and does on an edit', async () => {
        credentials = [stored]
        mockFetch()
        render(<TabRegistries/>)
        await screen.findByText('ghcr.io')

        fireEvent.click(screen.getByRole('button', {name: 'Add registry'}))
        fireEvent.change(screen.getByLabelText('Registry'), {target: {value: 'ghcr.io'}})
        fireEvent.change(screen.getByLabelText('Username'), {target: {value: 'robot'}})
        expect(screen.getByRole('button', {name: 'Save'}).hasAttribute('disabled')).toBe(true)
        fireEvent.click(screen.getByRole('button', {name: 'Cancel'}))
        // the dialog hides the table behind it while it closes
        await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())

        fireEvent.click(screen.getByRole('button', {name: 'Edit ghcr.io'}))
        // the stored secret is never put back in the field
        expect((screen.getByLabelText('New password or token') as HTMLInputElement).value).toBe('')
        expect(screen.getByRole('button', {name: 'Save'}).hasAttribute('disabled')).toBe(false)
    })

    it('reports a refused login as the registry worded it', async () => {
        credentials = [stored]
        mockFetch({'POST /credentials/u-1/test': {ok: false, error: 'ghcr.io rejected these credentials'}})
        render(<TabRegistries/>)
        await screen.findByText('ghcr.io')

        const row = screen.getByText('ghcr.io').closest('tr')!
        fireEvent.click(within(row).getByRole('button', {name: 'Test'}))

        await waitFor(() => expect(notices).toContain('warn:ghcr.io rejected these credentials'))
    })

    it('removes a credential only after the confirmation', async () => {
        credentials = [stored]
        mockFetch({'DELETE /credentials/u-1': {status: 'deleted'}})
        render(<TabRegistries/>)
        await screen.findByText('ghcr.io')

        fireEvent.click(screen.getByRole('button', {name: 'Remove ghcr.io'}))
        expect(calls.some(call => call.method === 'DELETE')).toBe(false)
        fireEvent.click(screen.getByRole('button', {name: 'Remove'}))
        await waitFor(() => expect(calls.some(call => call.method === 'DELETE')).toBe(true))
    })
})
