import {withProtectedAPI} from './api.ts';

export interface GitAPIErrorBody {
    error?: string;
    code?: string;
    branch?: string;
    sourceBranch?: string;
    canCreate?: boolean;
    canCreateFromDefault?: boolean;
    canCreateEmpty?: boolean;
}

export class GitAPIError extends Error {
    constructor(public readonly status: number, public readonly body: GitAPIErrorBody) {
        super(body.error || `HTTP ${status}`);
    }
}

export async function gitAPI<T>(path: string, init?: RequestInit): Promise<T> {
    const headers = new Headers(init?.headers);
    if (init?.body) headers.set('Content-Type', 'application/json');
    const response = await fetch(withProtectedAPI(`/git${path}`), {...init, headers});
    if (!response.ok) {
        const body = await response.json().catch(() => ({error: response.statusText})) as GitAPIErrorBody;
        throw new GitAPIError(response.status, body);
    }
    return response.status === 204 ? undefined as T : response.json() as Promise<T>;
}

export function gitDateLabel(value?: string) {
    return value ? new Date(value).toLocaleString() : '—';
}

export function gitComparisonLanguage(path: string) {
    const name = path.toLocaleLowerCase();
    if (name.endsWith('.json')) return 'json';
    if (name.endsWith('.xml')) return 'xml';
    if (name.endsWith('.sh') || name.endsWith('.bash')) return 'shell';
    if (name.endsWith('.sql')) return 'sql';
    if (name.endsWith('.toml') || name.endsWith('.ini') || name.endsWith('.cfg') || name.endsWith('.conf')) return 'ini';
    if (name.endsWith('.md')) return 'markdown';
    if (name.endsWith('.yml') || name.endsWith('.yaml')) return 'yaml';
    return 'plaintext';
}
