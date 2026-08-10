import {useSyncExternalStore} from 'react';

// The build the page was loaded with, and whether the server has since moved
// to a different one.
//
// Cost at rest is nil by construction: nothing polls and no timer exists. The
// only trigger is the container events stream dropping, which happens when the
// server or the daemon restarts - and a Dockman restart is exactly the event
// worth reacting to. A daemon-only restart costs one request that finds the
// same build and changes nothing.
let loadedBuild: string | null = null;
let stale = false;
const listeners = new Set<() => void>();

export interface BuildIdentity {
    version: string;
    commit: string;
    buildDate: string;
}

function identify(info: BuildIdentity): string {
    return `${info.version}|${info.commit}|${info.buildDate}`;
}

/**
 * Compares the server's build with the one this page was loaded with.
 *
 * The first call only records the reference: a page has to know what it was
 * serving before it can say the server moved. A failed call concludes nothing
 * - a server that is still restarting is not a new version.
 */
export async function checkServerBuild(read: () => Promise<BuildIdentity>): Promise<void> {
    if (stale) return;
    let build: string;
    try {
        build = identify(await read());
    } catch {
        return;
    }
    if (loadedBuild === null) {
        loadedBuild = build;
        return;
    }
    if (build === loadedBuild) return;
    stale = true;
    for (const listener of listeners) listener();
}

function subscribe(callback: () => void): () => void {
    listeners.add(callback);
    return () => {
        listeners.delete(callback);
    };
}

/** True once the server reports a build different from the loaded one. */
export function useServerBuildChanged(): boolean {
    return useSyncExternalStore(subscribe, () => stale);
}
