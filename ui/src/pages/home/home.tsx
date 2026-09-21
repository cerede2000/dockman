import {Box, Divider, Drawer, List, ListItemButton, ListItemIcon, Tooltip,} from '@mui/material';
import {FolderDelete, Logout, Settings, SpaceDashboardOutlined, SystemUpdateAlt} from '@mui/icons-material';
import {Link as RouterLink, Outlet, useLocation, useNavigate, useParams} from 'react-router';

import HostSelectDropdown from "./host-selector.tsx";
import {useAuth} from "../../hooks/auth.ts";
import {ShortcutFormatter} from "../compose/components/shortcut-formatter.tsx";
import React, {useEffect, useMemo, useRef} from "react";
import {
    ContainerIcon,
    DockerFolderIcon,
    ImagesIcon,
    NetworkIcon,
    StatsIcon,
    VolumeIcon
} from "../compose/components/file-icon.tsx";
import {useHostStore, useLastOpened} from "../compose/state/files.ts";
import {useTabsStore} from "../../context/tab-context.tsx";
import {useTerminalTabs} from "../compose/state/terminal.tsx";
import {useNavigationPreferences} from './navigation-preferences.ts';
import {useSidebarShortcuts} from './sidebar-shortcuts.ts';
import FileDockerBuild, {DockerBuildActivityIndicator} from '../compose/dialogs/file-docker-build.tsx';

const MAIN_SIDEBAR_WIDTH = 72;

export const useHostFromUrl = () => {
    const {host} = useParams()
    const rememberedHost = useHostStore(state => state.host)
    return host || rememberedHost || "local";
}

export function RootLayout() {
    const navigate = useNavigate();
    const location = useLocation();
    const {logout} = useAuth();
    const host = useHostFromUrl()
    const setHost = useHostStore(state => state.setHost)
    const resetTabs = useTabsStore(state => state.reset)
    const clearTerminalTabs = useTerminalTabs(state => state.clearAll)
    const clearLastOpened = useLastOpened(state => state.clear)
    const previousHost = useRef(host)
    const showStats = useNavigationPreferences(state => state.showStats)
    const showContainers = useNavigationPreferences(state => state.showContainers)
    useEffect(() => {
        if (previousHost.current !== host) {
            resetTabs()
            clearTerminalTabs()
            clearLastOpened()
            previousHost.current = host
        }
        setHost(host)
    }, [clearLastOpened, clearTerminalTabs, host, resetTabs, setHost]);

    const handleLogout = () => {
        logout();
        navigate('/');
    };

    const navigationItems = useMemo(() => [
        {id: 'files', title: 'Files', path: `/${host}/files`, icon: DockerFolderIcon},
        {id: 'monitor', title: 'Monitor', path: `/${host}/monitor`, icon: () => <SpaceDashboardOutlined sx={{color: '#4db6ac'}}/>},
        ...(showStats ? [{id: 'stats', title: 'Stats', path: `/${host}/stats`, icon: StatsIcon}] : []),
        ...(showContainers ? [{id: 'containers', title: 'Containers', path: `/${host}/containers`, icon: ContainerIcon}] : []),
        {id: 'updates', title: 'Updates', path: `/${host}/updates`, icon: () => <SystemUpdateAlt sx={{color: '#ffb74d'}}/>},
        {id: 'images', title: 'Images', path: `/${host}/images`, icon: ImagesIcon},
        {id: 'volumes', title: 'Volumes', path: `/${host}/volumes`, icon: VolumeIcon},
        {id: 'networks', title: 'Networks', path: `/${host}/networks`, icon: NetworkIcon},
        {id: 'cleaner', title: 'Cleaner', path: `/${host}/cleaner`, icon: () => <FolderDelete sx={{color: 'greenyellow'}}/>},
    ], [host, showContainers, showStats]);

    useSidebarShortcuts(navigationItems, navigate, location.pathname);

    return (
        <Box sx={{display: 'flex', minHeight: '100vh'}}>
            <Drawer
                sx={{
                    width: MAIN_SIDEBAR_WIDTH,
                    flexShrink: 0,
                    '& .MuiDrawer-paper': {
                        width: MAIN_SIDEBAR_WIDTH,
                        boxSizing: 'border-box',
                        borderRight: '1px solid',
                        borderColor: 'divider',
                        display: 'flex',
                        flexDirection: 'column',
                        alignItems: 'center',
                        backgroundColor: 'background.default',
                        py: 2
                    }
                }}
                variant="permanent"
                anchor="left"
            >
                {/* 1. TOP: Logo Icon Only */}
                <Box
                    component={RouterLink}
                    to="/"
                    sx={{
                        mb: 3,
                        display: 'flex',
                        justifyContent: 'center',
                        transition: 'transform 0.2s',
                        '&:hover': {transform: 'scale(1.1)'}
                    }}
                >
                    <Box component="img" sx={{height: 36, width: 36}} alt="Logo" src="/dockman.svg"/>
                </Box>

                <Box sx={{mb: 2, position: 'relative'}}>
                    <HostSelectDropdown/>
                </Box>

                <Divider sx={{width: '60%', mb: 2}}/>

                {/* Navigation Items - Icon Only */}
                <Box sx={{flexGrow: 1, overflowY: 'auto', width: '100%'}}>
                    <List sx={{display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 1}}>
                        {navigationItems.map((item, index) => {
                            const IconComponent = item.icon as React.ComponentType;
                            const isSelected = location.pathname.startsWith(item.path);

                            return (
                                <Tooltip
                                    key={item.title}
                                    placement="right"
                                    title={<ShortcutFormatter title={item.title}
                                                              keyCombo={index < 9 ? ["ALT", `${index + 1}`] : []}/>}
                                >
                                    <ListItemButton
                                        onClick={() => navigate(item.path)}
                                        selected={isSelected}
                                        sx={{
                                            borderRadius: 2,
                                            justifyContent: 'center',
                                            width: 48,
                                            height: 48,
                                            px: 0
                                        }}
                                    >
                                        <ListItemIcon sx={{minWidth: 0, justifyContent: 'center'}}>
                                            <IconComponent/>
                                        </ListItemIcon>
                                    </ListItemButton>
                                </Tooltip>
                            );
                        })}
                    </List>
                </Box>

                {/* Bottom Items */}
                <Box sx={{
                    width: '100%',
                    mt: 'auto',
                    display: 'flex',
                    flexDirection: 'column',
                    alignItems: 'center',
                    gap: 1
                }}>
                    <Divider sx={{width: '60%', mb: 1}}/>

                    <Tooltip title="Settings" placement="right">
                        <ListItemButton
                            component={RouterLink}
                            to="/settings"
                            selected={location.pathname === '/settings'}
                            sx={{borderRadius: 2, justifyContent: 'center', width: 48, height: 48, px: 0}}
                        >
                            <ListItemIcon sx={{minWidth: 0, justifyContent: 'center'}}>
                                <Settings/>
                            </ListItemIcon>
                        </ListItemButton>
                    </Tooltip>

                    <Tooltip title="Logout" placement="right">
                        <ListItemButton
                            onClick={handleLogout}
                            sx={{borderRadius: 2, justifyContent: 'center', width: 48, height: 48, px: 0}}
                        >
                            <ListItemIcon sx={{minWidth: 0, justifyContent: 'center'}}>
                                <Logout/>
                            </ListItemIcon>
                        </ListItemButton>
                    </Tooltip>
                </Box>
            </Drawer>

            <Box
                component="main"
                sx={{
                    flexGrow: 1,
                    minWidth: 0,
                    height: '100vh',
                    overflow: 'auto'
                }}
            >
                <Outlet/>
            </Box>
            <DockerBuildActivityIndicator/>
            <FileDockerBuild/>
        </Box>
    );
}

export default RootLayout;
