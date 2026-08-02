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
    state: 'unselected' | 'pending' | 'up_to_date' | 'checking' | 'local_changes' | 'locally_deleted' | 'remote_changes' | 'orphaned' | 'conflict' | 'error';
    selected: boolean;
    error?: string;
    conflictCount: number;
    autoSyncEnabled: boolean;
    stackAutoSyncEnabled: boolean;
    bindingAutomationPaused: boolean;
    bindingSyncState: string;
    bindingSyncError?: string;
    automationPaused: boolean;
    pauseReason?: 'manual' | 'recovery';
    autoDeployEnabled: boolean;
    autoDeployRollbackEnabled: boolean;
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
    aggregateByHost: Record<string, Record<string, GitStackStatus>>;
    folderStatusesByHost: Record<string, Record<string, GitStackStatus[]>>;
    trackedFilesByHost: Record<string, Record<string, boolean>>;
    setHost: (host: string, rows: GitStackStatus[]) => void;
    setTrackedFiles: (host: string, checkedPaths: string[], trackedPaths: string[]) => void;
}

const normalizePath = (value: string) => value.replaceAll('\\', '/').replace(/^\/+|\/+$/g, '');
const statusFingerprint = (row: GitStackStatus) => JSON.stringify(row);
// Zustand uses React's useSyncExternalStore. Returning a freshly allocated
// fallback from a selector makes the snapshot look different on every read and
// React correctly stops the resulting render loop with error #185. Keep the
// empty snapshot referentially stable until the first response for this host.
const EMPTY_GIT_STACK_STATUSES: Record<string, GitStackStatus> = Object.freeze({});
const EMPTY_GIT_STATUS_LIST: GitStackStatus[] = [];

export function gitStatusSeverity(status: GitStackStatus): 'neutral' | 'info' | 'warning' | 'error' | 'success' {
    if (status.deployState === 'failed' || status.deployState === 'rollback_failed' || status.state === 'conflict' || status.state === 'error') return 'error';
    if (status.state === 'checking') return 'info';
    if (status.state === 'local_changes' || status.state === 'locally_deleted' || status.state === 'remote_changes' || status.state === 'orphaned' || status.deployState === 'pending' || status.deployState === 'rolled_back') return 'warning';
    if (status.state === 'up_to_date') return 'success';
    return 'neutral';
}

export function worstGitStatus(statuses: GitStackStatus[]): GitStackStatus | undefined {
    const rank = (status: GitStackStatus) => {
        if (status.deployState === 'failed' || status.deployState === 'rollback_failed') return 7;
        if (status.state === 'error' || status.state === 'conflict') return 6;
        if (status.state === 'orphaned' || status.state === 'locally_deleted') return 5;
        if (status.state === 'local_changes' || status.state === 'remote_changes' || status.deployState === 'pending' || status.deployState === 'rolled_back') return 4;
        if (status.state === 'checking') return 3;
        if (!status.selected || status.bindingAutomationPaused || status.automationPaused || status.state === 'pending') return 2;
        return 1;
    };
    return statuses.reduce<GitStackStatus | undefined>((worst, current) => {
        return !worst || rank(current) > rank(worst) ? current : worst;
    }, undefined);
}

function projectFolders(rows: Record<string, GitStackStatus>) {
    const aggregates: Record<string, GitStackStatus> = {};
    const lists: Record<string, GitStackStatus[]> = {};
    for (const status of Object.values(rows)) {
        let folder = normalizePath(status.fullComposePath).split('/').slice(0, -1);
        while (folder.length > 0) {
            const path = folder.join('/');
            aggregates[path] = worstGitStatus([aggregates[path], status].filter(Boolean) as GitStackStatus[])!;
            (lists[path] ??= []).push(status);
            folder = folder.slice(0, -1);
        }
    }
    return {aggregates, lists};
}

const useGitStatusStore = create<GitStatusStore>((set) => ({
    byHost: {},
    aggregateByHost: {},
    folderStatusesByHost: {},
    trackedFilesByHost: {},
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
        const folders = projectFolders(next);
        return {
            byHost: {...state.byHost, [host]: next},
            aggregateByHost: {...state.aggregateByHost, [host]: folders.aggregates},
            folderStatusesByHost: {...state.folderStatusesByHost, [host]: folders.lists},
        };
    }),
    setTrackedFiles: (host, checkedPaths, trackedPaths) => set((state) => {
        const current = state.trackedFilesByHost[host] ?? {};
        const next = {...current};
        const tracked = new Set(trackedPaths.map(normalizePath));
        for (const path of checkedPaths) next[normalizePath(path)] = tracked.has(normalizePath(path));
        return {trackedFilesByHost: {...state.trackedFilesByHost, [host]: next}};
    }),
}));

const watchers = new Map<string, {references: number; timer?: ReturnType<typeof setInterval>; running: boolean; onVisible?: () => void}>();
const mutationRefreshes = new Map<string, ReturnType<typeof setTimeout>>();
const trackedFileRequests = new Map<string, {paths: Set<string>; timer?: ReturnType<typeof setTimeout>}>();

async function flushTrackedFileRequests(host: string) {
    const pending = trackedFileRequests.get(host);
    if (!pending) return;
    trackedFileRequests.delete(host);
    const paths = Array.from(pending.paths);
    for (let offset = 0; offset < paths.length; offset += 500) {
        const chunk = paths.slice(offset, offset + 500);
        try {
            const response = await fetch(withProtectedAPI('/git/tracked-files'), {
                method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({host, paths: chunk}),
            });
            if (!response.ok) continue;
            const result = await response.json() as {trackedPaths: string[]};
            useGitStatusStore.getState().setTrackedFiles(host, chunk, result.trackedPaths ?? []);
        } catch {
            // Git is optional. Leave unknown entries unbadged; a remount can
            // retry without creating a persistent worker or noisy error.
        }
    }
}

function requestTrackedFile(host: string, path: string) {
    if (!host || !path) return;
    const pending = trackedFileRequests.get(host) ?? {paths: new Set<string>()};
    pending.paths.add(normalizePath(path));
    if (!pending.timer) pending.timer = setTimeout(() => void flushTrackedFileRequests(host), 20);
    trackedFileRequests.set(host, pending);
}

export async function refreshGitStackStatuses(host: string) {
    if (!host) return;
    // One-shot refreshes (for example after a file mutation) must not create a
    // permanent zero-reference watcher. Only useGitStatusWatcher owns entries
    // in this map and their timers/listeners.
    const watcher = watchers.get(host) ?? {references: 0, running: false};
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
    // The server has already processed the successful file mutation before
    // this callback runs. Coalesce bursts (upload/copy/rename) into one compact
    // authoritative status read. The browser deliberately does not infer a
    // local Git change: only the server knows the link profile, explicit rules,
    // repository exclusions and .dockmanignore policy.
    void changedPath;
    const pendingRefresh = mutationRefreshes.get(host);
    if (pendingRefresh) clearTimeout(pendingRefresh);
    mutationRefreshes.set(host, setTimeout(() => {
        mutationRefreshes.delete(host);
        void refreshGitStackStatuses(host);
    }, 100));
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

export function useGitTrackedFile(host: string, path: string) {
    const normalized = normalizePath(path);
    const tracked = useGitStatusStore((state) => state.trackedFilesByHost[host]?.[normalized]);
    useEffect(() => {
        if (tracked === undefined) requestTrackedFile(host, normalized);
    }, [host, normalized, tracked]);
    return tracked === true;
}

export function useGitFolderStatus(host: string, folderPath: string) {
    const normalized = normalizePath(folderPath);
    return useGitStatusStore((state) => state.aggregateByHost[host]?.[normalized]);
}

export function useGitFolderStatuses(host: string, folderPath: string) {
    const normalized = normalizePath(folderPath);
    return useGitStatusStore((state) => state.folderStatusesByHost[host]?.[normalized] ?? EMPTY_GIT_STATUS_LIST);
}
