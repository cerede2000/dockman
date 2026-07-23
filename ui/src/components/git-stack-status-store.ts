import {useEffect} from 'react';
import {create} from 'zustand';
import {withProtectedAPI} from '../lib/api.ts';

export interface GitStackStatus {
    bindingId: string;
    host: string;
    stackPath: string;
    composePath: string;
    fullComposePath: string;
    repositoryId: string;
    repositoryName: string;
    repositoryBranch: string;
    repositorySubPath: string;
    state: 'pending' | 'up_to_date' | 'checking' | 'local_changes' | 'remote_changes' | 'conflict' | 'error';
    error?: string;
    conflictCount: number;
    autoSyncEnabled: boolean;
    automationPaused: boolean;
    autoDeployEnabled: boolean;
    autoSyncIntervalMinutes: number;
    lastCheckedAt?: string;
    lastSuccessAt?: string;
    nextCheckAt?: string;
    lastCommit?: string;
    deployState: string;
    deployError?: string;
    lastDeployAt?: string;
}

interface GitStatusStore {
    byHost: Record<string, Record<string, GitStackStatus>>;
    issuesByHost: Record<string, Record<string, GitStackStatus>>;
    setHost: (host: string, rows: GitStackStatus[]) => void;
}

const normalizePath = (value: string) => value.replaceAll('\\', '/').replace(/^\/+|\/+$/g, '');
const statusFingerprint = (row: GitStackStatus) => JSON.stringify(row);
// Zustand uses React's useSyncExternalStore. Returning a freshly allocated
// fallback from a selector makes the snapshot look different on every read and
// React correctly stops the resulting render loop with error #185. Keep the
// empty snapshot referentially stable until the first response for this host.
const EMPTY_GIT_STACK_STATUSES: Record<string, GitStackStatus> = Object.freeze({});

export function gitStatusSeverity(status: GitStackStatus): 'neutral' | 'info' | 'warning' | 'error' | 'success' {
    if (status.deployState === 'failed' || status.state === 'conflict' || status.state === 'error') return 'error';
    if (status.state === 'checking') return 'info';
    if (status.state === 'local_changes' || status.state === 'remote_changes' || status.deployState === 'pending') return 'warning';
    if (status.state === 'up_to_date') return 'success';
    return 'neutral';
}

export function worstGitStatus(statuses: GitStackStatus[]): GitStackStatus | undefined {
    const rank = {error: 5, conflict: 5, local_changes: 3, remote_changes: 3, checking: 2, pending: 1, up_to_date: 0};
    return statuses.reduce<GitStackStatus | undefined>((worst, current) => {
        const currentRank = current.deployState === 'failed' ? 6 : rank[current.state] ?? 0;
        const worstRank = worst ? (worst.deployState === 'failed' ? 6 : rank[worst.state] ?? 0) : -1;
        return currentRank > worstRank ? current : worst;
    }, undefined);
}

function issueFolders(rows: Record<string, GitStackStatus>) {
    const issues: Record<string, GitStackStatus> = {};
    for (const status of Object.values(rows)) {
        if (!['warning', 'error'].includes(gitStatusSeverity(status))) continue;
        let folder = normalizePath(status.fullComposePath).split('/').slice(0, -1);
        while (folder.length > 0) {
            const path = folder.join('/');
            issues[path] = worstGitStatus([issues[path], status].filter(Boolean) as GitStackStatus[])!;
            folder = folder.slice(0, -1);
        }
    }
    return issues;
}

const useGitStatusStore = create<GitStatusStore>((set) => ({
    byHost: {},
    issuesByHost: {},
    setHost: (host, rows) => set((state) => {
        const previous = state.byHost[host] ?? {};
        const next = Object.fromEntries(rows.map((row) => {
            const path = normalizePath(row.fullComposePath);
            const current = previous[path];
            return [path, current && statusFingerprint(current) === statusFingerprint(row) ? current : row];
        }));
        const currentKeys = Object.keys(previous);
        const nextKeys = Object.keys(next);
        if (currentKeys.length === nextKeys.length && nextKeys.every((key) => previous[key] === next[key])) return state;
        return {
            byHost: {...state.byHost, [host]: next},
            issuesByHost: {...state.issuesByHost, [host]: issueFolders(next)},
        };
    }),
}));

const watchers = new Map<string, {references: number; timer?: ReturnType<typeof setInterval>; running: boolean; onVisible?: () => void}>();

export async function refreshGitStackStatuses(host: string) {
    if (!host) return;
    const watcher = watchers.get(host) ?? {references: 0, running: false};
    watchers.set(host, watcher);
    if (watcher.running || document.visibilityState === 'hidden') return;
    watcher.running = true;
    try {
        const response = await fetch(withProtectedAPI(`/git/stack-statuses?host=${encodeURIComponent(host)}`));
        if (!response.ok) return;
        useGitStatusStore.getState().setHost(host, await response.json() as GitStackStatus[]);
    } catch {
        // Git is optional and hosts can be offline. Keep the last projection;
        // the single bounded watcher will retry on its normal interval.
    } finally {
        watcher.running = false;
    }
}

export function markGitStackLocal(host: string, changedPath: string) {
    const rows = Object.values(useGitStatusStore.getState().byHost[host] ?? {});
    const path = normalizePath(changedPath);
    const candidates = rows.map((row) => {
        const compose = normalizePath(row.fullComposePath);
        const separator = compose.lastIndexOf('/');
        const root = separator < 0 ? '' : compose.slice(0, separator);
        return {row, root};
    }).filter(({root}) => root === '' || path === root || path.startsWith(`${root}/`));
    const deepest = Math.max(-1, ...candidates.map(({root}) => root.length));
    if (deepest < 0) return;
    const targets = new Set(candidates.filter(({root}) => root.length === deepest).map(({row}) => row));
    const now = new Date().toISOString();
    useGitStatusStore.getState().setHost(host, rows.map((row) => targets.has(row) ? {
        ...row, state: 'local_changes', error: undefined, conflictCount: 0, lastCheckedAt: now,
    } : row));
}

export function useGitStatusWatcher(host: string) {
    useEffect(() => {
        if (!host) return;
        const watcher = watchers.get(host) ?? {references: 0, running: false};
        watcher.references++;
        watchers.set(host, watcher);
        if (watcher.references === 1) {
            void refreshGitStackStatuses(host);
            watcher.timer = setInterval(() => void refreshGitStackStatuses(host), 30_000);
            watcher.onVisible = () => document.visibilityState === 'visible' && void refreshGitStackStatuses(host);
            document.addEventListener('visibilitychange', watcher.onVisible);
        }
        return () => {
            const current = watchers.get(host);
            if (!current) return;
            current.references = Math.max(0, current.references - 1);
            if (current.references === 0) {
                if (current.timer) clearInterval(current.timer);
                if (current.onVisible) document.removeEventListener('visibilitychange', current.onVisible);
                watchers.delete(host);
            }
        };
    }, [host]);
}

export function useGitStackStatuses(host: string) {
    useGitStatusWatcher(host);
    return useGitStatusStore((state) => state.byHost[host] ?? EMPTY_GIT_STACK_STATUSES);
}

export function useGitStackStatus(host: string, composePath: string) {
    const normalized = normalizePath(composePath);
    return useGitStatusStore((state) => state.byHost[host]?.[normalized]);
}

export function useGitFolderIssue(host: string, folderPath: string) {
    const normalized = normalizePath(folderPath);
    return useGitStatusStore((state) => state.issuesByHost[host]?.[normalized]);
}
