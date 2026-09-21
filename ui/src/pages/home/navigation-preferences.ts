import {create} from 'zustand';
import {persist} from 'zustand/middleware';

// Every view the sidebar can hold, in its default order. This list is the one
// source of truth for "which views exist": the sidebar, the landing-page
// choices and the Settings → Views list all derive from it.
export const NAV_VIEW_IDS = [
    'files',
    'monitor',
    'stats',
    'containers',
    'updates',
    'images',
    'volumes',
    'networks',
    'cleaner',
] as const;

export type NavViewId = typeof NAV_VIEW_IDS[number];

export function isNavViewId(value: unknown): value is NavViewId {
    return typeof value === 'string' && (NAV_VIEW_IDS as readonly string[]).includes(value);
}

interface NavigationPreferences {
    showStats: boolean;
    showContainers: boolean;
    // sidebar order, always a permutation of NAV_VIEW_IDS (see normalizeOrder)
    order: NavViewId[];
    // landing view of a host; '' follows the server's dockman.yml defaultView
    defaultView: NavViewId | '';
    setShowStats: (visible: boolean) => void;
    setShowContainers: (visible: boolean) => void;
    moveView: (id: NavViewId, offset: -1 | 1) => void;
    resetOrder: () => void;
    setDefaultView: (view: NavViewId | '') => void;
}

// normalizeOrder turns whatever was stored into a complete order: unknown or
// repeated ids are dropped, and views the stored order does not know yet - one
// added by a later Dockman release - are appended in their default order, so
// a new view never goes missing from a customised sidebar.
export function normalizeOrder(stored: unknown): NavViewId[] {
    const order: NavViewId[] = [];
    if (Array.isArray(stored)) {
        for (const id of stored) {
            if (isNavViewId(id) && !order.includes(id)) order.push(id);
        }
    }
    for (const id of NAV_VIEW_IDS) {
        if (!order.includes(id)) order.push(id);
    }
    return order;
}

// moveOrder moves a view one step, as the sidebar sees it: a shown view jumps
// over the hidden views in its way to trade places with its next shown
// neighbour, so every click changes the sidebar - moving past a hidden Stats
// would otherwise change nothing anyone can see. A hidden view simply swaps
// with its neighbour. A move with nowhere to go returns the same order.
export function moveOrder(
    order: NavViewId[],
    id: NavViewId,
    offset: -1 | 1,
    isShown: (id: NavViewId) => boolean = () => true,
): NavViewId[] {
    const from = order.indexOf(id);
    if (from < 0) return order;
    let to = from + offset;
    if (isShown(id)) {
        while (to >= 0 && to < order.length && !isShown(order[to])) to += offset;
    }
    if (to < 0 || to >= order.length) return order;
    const next = order.filter(v => v !== id);
    next.splice(to, 0, id);
    return next;
}

// visibleViews is the sidebar as displayed: the order, minus the legacy views
// switched off. Positions in this list are the Alt+1..9 shortcuts.
export function visibleViews(
    order: NavViewId[],
    visibility: { showStats: boolean; showContainers: boolean },
): NavViewId[] {
    return order.filter(isShownIn(visibility));
}

// isShownIn tells which views the sidebar shows under the given switches
export function isShownIn(visibility: { showStats: boolean; showContainers: boolean }): (id: NavViewId) => boolean {
    return id => (id !== 'stats' || visibility.showStats) && (id !== 'containers' || visibility.showContainers);
}

// resolveLandingView picks the view a host opens on: the choice stored in this
// browser (Settings → Views) first, then the host's dockman.yml defaultView,
// then Files. An unknown name in dockman.yml falls through to Files.
export function resolveLandingView(preferred: NavViewId | '', configured: string | undefined): NavViewId {
    if (preferred) return preferred;
    const view = (configured ?? '').trim().toLowerCase();
    return isNavViewId(view) ? view : 'files';
}

// Persisted state comes from older releases too: the first version stored only
// the two visibility switches. Merging normalises what it finds instead of
// bumping the store version - a version bump without a migration would make
// zustand drop the stored state, and those switches with it.
export function mergeStoredPreferences<T extends NavigationPreferences>(stored: unknown, current: T): T {
    const s = (stored ?? {}) as Partial<Record<keyof NavigationPreferences, unknown>>;
    return {
        ...current,
        showStats: typeof s.showStats === 'boolean' ? s.showStats : current.showStats,
        showContainers: typeof s.showContainers === 'boolean' ? s.showContainers : current.showContainers,
        order: normalizeOrder(s.order),
        defaultView: isNavViewId(s.defaultView) ? s.defaultView : '',
    };
}

// Stats and Containers remain available for compatibility, but Monitor is the
// default consolidated view. These are browser UI preferences, like the rest
// of this store: they never alter the server configuration or other users.
export const useNavigationPreferences = create<NavigationPreferences>()(
    persist(
        (set) => ({
            showStats: false,
            showContainers: false,
            order: [...NAV_VIEW_IDS],
            defaultView: '',
            setShowStats: (showStats) => set({showStats}),
            setShowContainers: (showContainers) => set({showContainers}),
            moveView: (id, offset) => set(state => ({
                order: moveOrder(state.order, id, offset, isShownIn(state)),
            })),
            resetOrder: () => set({order: [...NAV_VIEW_IDS]}),
            setDefaultView: (defaultView) => set({defaultView}),
        }),
        {
            name: 'dockman-navigation-views',
            partialize: (state) => ({
                showStats: state.showStats,
                showContainers: state.showContainers,
                order: state.order,
                defaultView: state.defaultView,
            }),
            merge: (stored, current) => mergeStoredPreferences(stored, current),
        },
    ),
);
