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

export interface GitTrackedFileInfo {
    path: string;
    bindingId?: string;
    composePath?: string;
    relativePath?: string;
    linked: boolean;
    tracked: boolean;
    mutable: boolean;
    folderLinkRoot?: boolean;
    reason?: string;
}

interface GitStatusStore {
    byHost: Record<string, Record<string, GitStackStatus>>;
    aggregateByHost: Record<string, Record<string, GitStackStatus>>;
    folderStatusesByHost: Record<string, Record<string, GitStackStatus[]>>;
    trackedFilesByHost: Record<string, Record<string, GitTrackedFileInfo>>;
    setHost: (host: string, rows: GitStackStatus[]) => void;
    setTrackedFiles: (host: string, checkedPaths: string[], trackedPaths: string[], files?: GitTrackedFileInfo[]) => void;
    setTrackedFile: (host: string, file: GitTrackedFileInfo) => void;
}

const normalizePath = (value: string) => value.replaceAll('\\', '/').replace(/^\/+|\/+$/g, '');
const statusFingerprint = (row: GitStackStatus) => JSON.stringify(row);
// Zustand uses React's useSyncExternalStore. Returning a freshly allocated
// fallback from a selector makes the snapshot look different on every read and
// React correctly stops the resulting render loop with error #185. Keep the
// empty snapshot referentially stable until the first response for this host.
const EMPTY_GIT_STACK_STATUSES: Record<string, GitStackStatus> = Object.freeze({});
const EMPTY_GIT_STATUS_LIST: GitStackStatus[] = [];

// A compose file can legitimately appear more than once in the API projection:
// for example, a broad "all stacks" Folder Link catalogues it as unselected
// while a more specific Folder Link actually synchronizes it. The Files and
// Monitor views expose one badge per compose path, so the selected association
// must always win regardless of database/API ordering. Two equally selected
// rows are not a valid overlapping-link configuration; keeping the first one is
// deterministic and avoids making the displayed action target oscillate.
const preferStatusForPath = (current: GitStackStatus | undefined, candidate: GitStackStatus) => {
    if (!current) return candidate;
    if (current.selected !== candidate.selected) return candidate.selected ? candidate : current;
    return current;
};

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
        const projected: Record<string, GitStackStatus> = {};
        for (const rawRow of rows) {
            const row = rawRow;
            const path = normalizePath(row.fullComposePath);
            projected[path] = preferStatusForPath(projected[path], row);
        }
        const next = Object.fromEntries(Object.entries(projected).map(([path, row]) => {
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
    setTrackedFiles: (host, checkedPaths, trackedPaths, files = []) => set((state) => {
        const current = state.trackedFilesByHost[host] ?? {};
        const next = {...current};
        const tracked = new Set(trackedPaths.map(normalizePath));
        const details = new Map(files.map((file) => [normalizePath(file.path), file]));
        for (const path of checkedPaths) {
            const normalized = normalizePath(path);
            next[normalized] = details.get(normalized) ?? {path: normalized, linked: tracked.has(normalized), tracked: tracked.has(normalized), mutable: false};
        }
        return {trackedFilesByHost: {...state.trackedFilesByHost, [host]: next}};
    }),
    setTrackedFile: (host, file) => set((state) => ({trackedFilesByHost: {
        ...state.trackedFilesByHost,
        [host]: {...(state.trackedFilesByHost[host] ?? {}), [normalizePath(file.path)]: file},
    }})),
}));

const watchers = new Map<string, {references: number; timer?: ReturnType<typeof setInterval>; onVisible?: () => void}>();
const statusRefreshes = new Map<string, Promise<void>>();
const mutationRefreshes = new Map<string, ReturnType<typeof setTimeout>>();
const trackedFileRequests = new Map<string, {paths: Set<string>; timer?: ReturnType<typeof setTimeout>}>();
const trackedFileGenerations = new Map<string, number>();

const trackedFileKey = (host: string, path: string) => `${host}\0${normalizePath(path)}`;
const trackedFileGeneration = (host: string, path: string) => trackedFileGenerations.get(trackedFileKey(host, path)) ?? 0;
const bumpTrackedFileGeneration = (host: string, path: string) => {
    const key = trackedFileKey(host, path);
    const next = (trackedFileGenerations.get(key) ?? 0) + 1;
    trackedFileGenerations.set(key, next);
    return next;
};

async function flushTrackedFileRequests(host: string) {
    const pending = trackedFileRequests.get(host);
    if (!pending) return;
    trackedFileRequests.delete(host);
    const paths = Array.from(pending.paths);
    for (let offset = 0; offset < paths.length; offset += 500) {
        const chunk = paths.slice(offset, offset + 500);
        const generations = new Map(chunk.map((path) => [normalizePath(path), trackedFileGeneration(host, path)]));
        try {
            const response = await fetch(withProtectedAPI('/git/tracked-files'), {
                method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({host, paths: chunk}),
            });
            if (!response.ok) continue;
            const result = await response.json() as {trackedPaths: string[]; files?: GitTrackedFileInfo[]};
            // A policy mutation may have completed while this batched read was
            // in flight. Never let that older answer resurrect a cloud badge.
            const current = chunk.filter((path) => trackedFileGeneration(host, path) === generations.get(normalizePath(path)));
            const currentSet = new Set(current.map(normalizePath));
            useGitStatusStore.getState().setTrackedFiles(
                host,
                current,
                (result.trackedPaths ?? []).filter((path) => currentSet.has(normalizePath(path))),
                (result.files ?? []).filter((file) => currentSet.has(normalizePath(file.path))),
            );
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

export async function refreshGitStackStatuses(host: string, authoritative = true) {
    if (!host) return;
    if (!authoritative && document.visibilityState === 'hidden') return;

    const current = statusRefreshes.get(host);
    if (current && !authoritative) return current;

    // A mutation must be observed after every older request has completed.
    // Chaining instead of dropping the refresh prevents a pre-mutation read
    // from leaving an orange badge until the next poll or full page reload.
    const previous = current?.catch(() => undefined) ?? Promise.resolve();
    const request = previous.then(async () => {
        try {
            const response = await fetch(withProtectedAPI(`/git/stack-statuses?host=${encodeURIComponent(host)}`));
            if (!response.ok) return;
            useGitStatusStore.getState().setHost(host, await response.json() as GitStackStatus[]);
        } catch {
            // Git is optional and hosts can be offline. Keep the last projection;
            // the single bounded watcher will retry on its normal interval.
        }
    });
    statusRefreshes.set(host, request);
    await request.finally(() => {
        if (statusRefreshes.get(host) === request) statusRefreshes.delete(host);
    });
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
        void refreshGitStackStatuses(host, false);
    }, 100));
}

export function useGitStatusWatcher(host: string) {
    useEffect(() => {
        if (!host) return;
        const watcher = watchers.get(host) ?? {references: 0};
        watcher.references++;
        watchers.set(host, watcher);
        if (watcher.references === 1) {
            void refreshGitStackStatuses(host, false);
            watcher.timer = setInterval(() => void refreshGitStackStatuses(host, false), 30_000);
            watcher.onVisible = () => document.visibilityState === 'visible' && void refreshGitStackStatuses(host, false);
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
    const file = useGitStatusStore((state) => state.trackedFilesByHost[host]?.[normalized]);
    useEffect(() => {
        if (file === undefined) requestTrackedFile(host, normalized);
    }, [file, host, normalized]);
    return file?.tracked === true;
}

export function useGitTrackedFileInfo(host: string, path: string) {
    const normalized = normalizePath(path);
    const file = useGitStatusStore((state) => state.trackedFilesByHost[host]?.[normalized]);
    useEffect(() => {
        if (file === undefined) requestTrackedFile(host, normalized);
    }, [file, host, normalized]);
    return file;
}

export async function setGitFileTracking(host: string, file: GitTrackedFileInfo, tracked: boolean) {
    if (!file.bindingId) throw new Error('This file is not attached to a selected Git stack');
    bumpTrackedFileGeneration(host, file.path);
    const response = await fetch(withProtectedAPI('/git/file-tracking'), {
        method: 'PUT', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({host, path: file.path, bindingId: file.bindingId, tracked}),
    });
    if (!response.ok) {
        let message = `Git file policy failed (${response.status})`;
        try {
            const body = await response.json() as {error?: string; message?: string};
            message = body.error || body.message || message;
        } catch { /* keep the bounded fallback */ }
        throw new Error(message);
    }
    const updated = await response.json() as GitTrackedFileInfo;
    useGitStatusStore.getState().setTrackedFile(host, updated);
    await refreshGitStackStatuses(host, true);
    return updated;
}

export async function refreshGitTrackedFile(host: string, path: string) {
    const normalized = normalizePath(path);
    const generation = bumpTrackedFileGeneration(host, normalized);
    const response = await fetch(withProtectedAPI('/git/tracked-files'), {
        method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({host, paths: [normalized]}),
    });
    if (!response.ok) throw new Error(`Git file badge refresh failed (${response.status})`);
    const result = await response.json() as {trackedPaths: string[]; files?: GitTrackedFileInfo[]};
    if (trackedFileGeneration(host, normalized) !== generation) return useGitStatusStore.getState().trackedFilesByHost[host]?.[normalized];
    useGitStatusStore.getState().setTrackedFiles(host, [normalized], result.trackedPaths ?? [], result.files ?? []);
    return useGitStatusStore.getState().trackedFilesByHost[host]?.[normalized];
}

export async function reconcileDeletedGitFile(host: string, file: GitTrackedFileInfo) {
    if (!file.bindingId) return;
    const response = await fetch(withProtectedAPI('/git/file-tracking'), {
        method: 'PUT', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({host, path: file.path, bindingId: file.bindingId, tracked: false, deleted: true}),
    });
    if (!response.ok) {
        let message = `Git deletion reconciliation failed (${response.status})`;
        try {
            const body = await response.json() as {error?: string; message?: string};
            message = body.error || body.message || message;
        } catch { /* keep the bounded fallback */ }
        throw new Error(message);
    }
    await response.json() as GitTrackedFileInfo;
    // Re-read the effective policy instead of assuming that deletion always
    // untracks the path: an exact one-file rule disappears, while a broad rule
    // such as *.conf must continue to apply if the same name is recreated.
    await refreshGitTrackedFile(host, file.path);
    await refreshGitStackStatuses(host, true);
}

export function useGitFolderStatus(host: string, folderPath: string) {
    const normalized = normalizePath(folderPath);
    return useGitStatusStore((state) => state.aggregateByHost[host]?.[normalized]);
}

export function useGitFolderStatuses(host: string, folderPath: string) {
    const normalized = normalizePath(folderPath);
    return useGitStatusStore((state) => state.folderStatusesByHost[host]?.[normalized] ?? EMPTY_GIT_STATUS_LIST);
}
