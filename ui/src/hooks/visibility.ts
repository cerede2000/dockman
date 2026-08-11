/**
 * Polling that stops while nobody is looking.
 *
 * A background tab still runs its timers - browsers only throttle them to
 * roughly one firing a minute - and every one of those firings is a real
 * request that reaches the Docker daemon on the other side. On a host with
 * forty containers the stats cycle alone is forty daemon calls, and the tab
 * showing them may have been in the background for hours.
 *
 * The rule these helpers enforce is the same everywhere: do nothing while the
 * document is hidden, and refresh at once when it comes back, so returning to
 * a tab never shows a stale reading.
 */

export function documentIsVisible(): boolean {
    return typeof document === 'undefined' || document.visibilityState !== 'hidden';
}

/**
 * Calls `onVisible` each time the document becomes visible again. Returns the
 * unsubscribe function; call it from the effect's cleanup.
 */
export function whenVisible(onVisible: () => void): () => void {
    if (typeof document === 'undefined') return () => {
    };
    const handler = () => {
        if (document.visibilityState === 'visible') onVisible();
    };
    document.addEventListener('visibilitychange', handler);
    return () => document.removeEventListener('visibilitychange', handler);
}

/**
 * A `setInterval` that does not fire while the document is hidden, and that
 * catches up as soon as it is visible again.
 *
 * `run` is called immediately if the document is visible, then every
 * `intervalMs`. Returns the teardown function.
 */
export function pollWhileVisible(run: () => void, intervalMs: number): () => void {
    const guarded = () => {
        if (documentIsVisible()) run();
    };
    guarded();
    const timer = setInterval(guarded, intervalMs);
    // Coming back to the tab must show fresh data rather than whatever was
    // last read before it was hidden.
    const stopWatching = whenVisible(run);
    return () => {
        clearInterval(timer);
        stopWatching();
    };
}
