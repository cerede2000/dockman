import React, {useMemo} from 'react';
import {FolderDelete, SpaceDashboardOutlined, SystemUpdateAlt} from '@mui/icons-material';
import {
    ContainerIcon,
    DockerFolderIcon,
    ImagesIcon,
    NetworkIcon,
    StatsIcon,
    VolumeIcon
} from '../compose/components/file-icon.tsx';
import {type NavViewId, useNavigationPreferences, visibleViews} from './navigation-preferences.ts';

interface NavViewDefinition {
    title: string;
    icon: React.ComponentType;
}

// What each view looks like in the sidebar and in Settings → Views. Keyed by
// NavViewId, so adding a view to NAV_VIEW_IDS without describing it here is a
// type error rather than a blank sidebar entry.
export const NAV_VIEWS: Record<NavViewId, NavViewDefinition> = {
    files: {title: 'Files', icon: DockerFolderIcon},
    monitor: {title: 'Monitor', icon: () => <SpaceDashboardOutlined sx={{color: '#4db6ac'}}/>},
    stats: {title: 'Stats', icon: StatsIcon},
    containers: {title: 'Containers', icon: ContainerIcon},
    updates: {title: 'Updates', icon: () => <SystemUpdateAlt sx={{color: '#ffb74d'}}/>},
    images: {title: 'Images', icon: ImagesIcon},
    volumes: {title: 'Volumes', icon: VolumeIcon},
    networks: {title: 'Networks', icon: NetworkIcon},
    cleaner: {title: 'Cleaner', icon: () => <FolderDelete sx={{color: 'greenyellow'}}/>},
};

export interface SidebarItem extends NavViewDefinition {
    id: NavViewId;
    path: string;
}

// sidebarItems is the sidebar as displayed for a host: `views` in their order
// (see visibleViews), each with the route it opens.
export function sidebarItems(host: string, views: NavViewId[]): SidebarItem[] {
    return views.map(id => ({id, ...NAV_VIEWS[id], path: `/${host}/${id}`}));
}

// useSidebarItems is the sidebar of a host as this browser arranged it
// (Settings → Views): the stored order, the legacy views switched off left out.
export function useSidebarItems(host: string): SidebarItem[] {
    const order = useNavigationPreferences(state => state.order);
    const showStats = useNavigationPreferences(state => state.showStats);
    const showContainers = useNavigationPreferences(state => state.showContainers);
    return useMemo(
        () => sidebarItems(host, visibleViews(order, {showStats, showContainers})),
        [host, order, showStats, showContainers],
    );
}
