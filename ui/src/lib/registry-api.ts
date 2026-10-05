import {withProtectedAPI} from './api.ts';

export interface RegistryCredential {
    uuid: string;
    registry: string;
    username: string;
    secretHint?: string;
    hasSecret: boolean;
    updatedAt: string;
    lastTestedAt?: string;
    lastTestError?: string;
}

export interface RegistryCredentialInput {
    registry: string;
    username: string;
    secret: string;
}

export interface RegistryTestResult {
    ok: boolean;
    error?: string;
}

export class RegistryAPIError extends Error {
    constructor(public readonly status: number, message: string) {
        super(message);
    }
}

export async function registryAPI<T>(path: string, init?: RequestInit): Promise<T> {
    const headers = new Headers(init?.headers);
    if (init?.body) headers.set('Content-Type', 'application/json');
    const response = await fetch(withProtectedAPI(`/registries${path}`), {...init, headers});
    if (!response.ok) {
        const body = await response.json().catch(() => ({error: response.statusText})) as { error?: string };
        throw new RegistryAPIError(response.status, body.error || `HTTP ${response.status}`);
    }
    return response.status === 204 ? undefined as T : response.json() as Promise<T>;
}

export function registryDateLabel(value?: string) {
    return value ? new Date(value).toLocaleString() : '—';
}
