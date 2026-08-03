import {create} from 'zustand';
import {persist} from 'zustand/middleware';

interface NavigationPreferences {
    showStats: boolean;
    showContainers: boolean;
    setShowStats: (visible: boolean) => void;
    setShowContainers: (visible: boolean) => void;
}

// Stats and Containers remain available for compatibility, but Monitor is the
// default consolidated view. This is a browser UI preference and therefore
// intentionally does not alter the server configuration or other users.
export const useNavigationPreferences = create<NavigationPreferences>()(
    persist(
        (set) => ({
            showStats: false,
            showContainers: false,
            setShowStats: (showStats) => set({showStats}),
            setShowContainers: (showContainers) => set({showContainers}),
        }),
        {name: 'dockman-navigation-views'},
    ),
);
