import {Box, Chip, Divider, Fade, Paper} from '@mui/material';
import {Delete, PlayArrow, RestartAlt, SpaceDashboardOutlined, Stop} from '@mui/icons-material';
import {useEffect, useMemo, useState} from 'react';
import {useNavigate} from 'react-router-dom';
import "@xterm/xterm/css/xterm.css";
import PageHeader, {RefreshButton} from '../../components/page-header.tsx';
import SearchBar from '../../components/search-bar.tsx';
import useSearch from '../../hooks/search.ts';
import ActionButtons from '../../components/action-buttons.tsx';
import scrollbarStyles from '../../components/scrollbar-style.tsx';
import {useDockerContainers} from '../../hooks/docker-containers.ts';
import {useDockerStats, useHostStats} from '../../hooks/docker-containers-stats.ts';
import {callRPC, useContainerExecWsUrl, useHostClient} from '../../lib/api.ts';
import {useSnackbar} from '../../hooks/snackbar.ts';
import {useHostStore} from '../compose/state/files.ts';
import {DockerService} from '../../gen/docker/v1/docker_pb.ts';
import AggregateStats from '../compose/components/container-stat-chart.tsx';
import {LogsPanel} from '../compose/components/logs-panel.tsx';
import {useContainerExec, useLogsPanel, useTerminalTabs} from '../compose/state/terminal.tsx';
import {useComposeAction} from '../compose/state/compose.tsx';
import {ContainersLoading} from '../containers/containers-loading.tsx';
import {MonitorTable, type MonitorRow, type RowAction, type StackAction, type StackGroup} from './monitor-table.tsx';
import {statsTheme as t} from '../compose/components/stats-theme.ts';

// one view to run the host from: real host usage on top, every container
// grouped by stack below it, with per-row and per-stack controls plus the
// logs/exec bottom panel — no hopping between views. The existing Stats and
// Containers pages are left untouched.
function MonitorPage() {
    const dockerService = useHostClient(DockerService);
    const {containers, loading, refreshContainers, fetchContainers} = useDockerContainers();
    const {history, containers: statContainers, aggregates} = useDockerStats("");
    const hostStats = useHostStats(true);
    const {showSuccess, showError} = useSnackbar();
    const {search, setSearch, searchInputRef} = useSearch();
    const navigate = useNavigate();
    const host = useHostStore(state => state.host);

    const [selected, setSelected] = useState<string[]>([]);
    const [expanded, setExpanded] = useState<Record<string, boolean>>({});
    const [now, setNow] = useState(() => Date.now());

    // uptime column tick; freshness of the values themselves comes from the
    // list poll
    useEffect(() => {
        const id = setInterval(() => setNow(Date.now()), 10_000);
        return () => clearInterval(id);
    }, []);

    // the bottom panel tabs reference the previous host's containers
    const clearTabs = useTerminalTabs(state => state.clearAll);
    useEffect(() => {
        clearTabs();
        setSelected([]);
    }, [host]);

    const createExecUrl = useContainerExecWsUrl();
    const execContainer = useContainerExec(state => state.execParams);
    const openLogs = useLogsPanel(state => state.openLogs);
    const runAction = useComposeAction(state => state.runAction);
    // string-valued selector: output appends leave it unchanged, so the page
    // only re-renders when a stack action actually starts or finishes
    const runningStackKeys = useComposeAction(state =>
        Object.entries(state.runs).filter(([, r]) => r.running).map(([f]) => f).sort().join('|'));

    const statsById = useMemo(() => {
        const map = new Map(statContainers.map(s => [s.id, s]));
        return map;
    }, [statContainers]);

    const groups: StackGroup[] = useMemo(() => {
        const query = search.trim().toLowerCase();
        const list = (containers?.list ?? []).filter(c =>
            !query || [c.name, c.imageName, c.stackName, c.serviceName].some(f => f.toLowerCase().includes(query))
        );

        const byStack = new Map<string, StackGroup>();
        for (const c of list) {
            const key = c.stackName;
            const group = byStack.get(key) ?? {stack: key, servicePath: '', rows: []};
            if (c.servicePath) group.servicePath = c.servicePath;
            group.rows = [...group.rows, {info: c, stats: statsById.get(c.id)}];
            byStack.set(key, group);
        }

        return [...byStack.values()]
            .map(g => ({...g, rows: [...g.rows].sort((a, b) => a.info.name.localeCompare(b.info.name))}))
            .sort((a, b) => {
                // named stacks first, loose containers at the end
                if (!a.stack) return 1;
                if (!b.stack) return -1;
                return a.stack.localeCompare(b.stack);
            });
    }, [containers, statsById, search]);

    const total = containers?.list.length ?? 0;
    const runningStacks = useMemo(() => {
        const map: Record<string, boolean> = {};
        for (const file of runningStackKeys.split('|')) {
            if (file) map[file] = true;
        }
        return map;
    }, [runningStackKeys]);

    async function bulkAction(name: string, rpcName: keyof typeof dockerService, message: string, ids: string[]) {
        // @ts-ignore dynamic rpc dispatch, same pattern as the containers view
        const {err} = await callRPC(() => dockerService[rpcName]({containerIds: ids}));
        if (err) showError(`Failed to ${name} containers: ${err}`);
        else showSuccess(`Successfully ${message} ${ids.length > 1 ? `${ids.length} containers` : 'container'}`);
        setSelected(prev => prev.filter(id => !ids.includes(id)));
        await fetchContainers();
    }

    const rowRpc: Record<RowAction, { rpc: keyof typeof dockerService, message: string }> = {
        start: {rpc: 'containerStart', message: 'started'},
        stop: {rpc: 'containerStop', message: 'stopped'},
        restart: {rpc: 'containerRestart', message: 'restarted'},
    };

    const handleRowAction = (row: MonitorRow, action: RowAction) =>
        void bulkAction(action, rowRpc[action].rpc, rowRpc[action].message, [row.info.id]);

    const stackRpc: Record<StackAction, { rpc: 'composeUp' | 'composeStop' | 'composeRestart', message: string }> = {
        up: {rpc: 'composeUp', message: 'up'},
        stop: {rpc: 'composeStop', message: 'stopped'},
        restart: {rpc: 'composeRestart', message: 'restarted'},
    };

    const handleStackAction = (group: StackGroup, action: StackAction) => {
        const {rpc, message} = stackRpc[action];
        runAction(group.servicePath, dockerService[rpc], action, [], (error) => {
            if (error) showError(`Stack ${group.stack}: ${action} failed — ${error}`);
            else showSuccess(`Stack ${group.stack} ${message}`);
            void fetchContainers();
        });
    };

    const handleRowLogs = (row: MonitorRow) =>
        openLogs(`logs:${host}/monitor#${row.info.id}`,
            row.info.stackName ? `${row.info.stackName}/${row.info.name}` : row.info.name,
            [{id: row.info.id, name: row.info.name}]);

    const handleStackLogs = (group: StackGroup) =>
        openLogs(`logs:${host}/monitor#stack:${group.stack || 'standalone'}`,
            `${group.stack || 'standalone'}: stack logs`,
            group.rows.map(r => ({id: r.info.id, name: r.info.name})));

    const handleRowExec = (row: MonitorRow) =>
        execContainer(`exec:${host}/monitor#${row.info.id}`,
            `${row.info.stackName ? `${row.info.stackName}/` : ''}${row.info.name} (exec)`,
            createExecUrl(row.info.id, '/bin/sh'),
            true);

    const handleStackEdit = (group: StackGroup) =>
        navigate(`/${host}/files/${group.servicePath}`);

    const bulkActions = [
        {
            action: 'start', buttonText: 'Start', icon: <PlayArrow/>,
            disabled: selected.length === 0,
            handler: () => bulkAction('start', 'containerStart', 'started', selected),
            tooltip: '',
        },
        {
            action: 'stop', buttonText: 'Stop', icon: <Stop/>,
            disabled: selected.length === 0,
            handler: () => bulkAction('stop', 'containerStop', 'stopped', selected),
            tooltip: '',
        },
        {
            action: 'restart', buttonText: 'Restart', icon: <RestartAlt/>,
            disabled: selected.length === 0,
            handler: () => bulkAction('restart', 'containerRestart', 'restarted', selected),
            tooltip: '',
        },
        {
            action: 'remove', buttonText: 'Remove', icon: <Delete/>,
            disabled: selected.length === 0,
            handler: () => bulkAction('remove', 'containerRemove', 'removed', selected),
            tooltip: '',
        },
    ];

    return (
        <Box sx={{
            display: 'flex',
            flexDirection: 'column',
            height: '100vh',
            overflow: 'hidden',
        }}>
            <Box sx={{
                display: 'flex',
                flexDirection: 'column',
                flexGrow: 1,
                minHeight: 0,
                p: {xs: 1, md: 3},
                pb: 0,
                overflow: 'hidden',
                ...scrollbarStyles
            }}>
                <PageHeader
                    icon={<SpaceDashboardOutlined/>}
                    title="Monitor"
                    count={total}
                    host={host}
                    right={<SearchBar search={search} setSearch={setSearch} inputRef={searchInputRef}/>}
                />

                <Box sx={{flexShrink: 0}}>
                    <AggregateStats aggregates={aggregates} hostStats={hostStats}/>
                </Box>

                <Paper
                    variant="outlined"
                    sx={{
                        px: 1.5,
                        py: 1,
                        mb: 1.5,
                        display: 'flex',
                        alignItems: 'center',
                        gap: 1.5,
                        borderRadius: 2,
                        bgcolor: t.panel,
                        borderColor: t.border,
                    }}
                >
                    <ActionButtons actions={bulkActions}/>
                    <RefreshButton onClick={refreshContainers} loading={loading}/>
                    {selected.length > 0 && (
                        <>
                            <Divider orientation="vertical" flexItem sx={{mx: 0.5, borderColor: t.border}}/>
                            <Chip
                                size="small"
                                variant="outlined"
                                color="primary"
                                label={`${selected.length} selected`}
                                sx={{fontWeight: 700}}
                            />
                        </>
                    )}
                </Paper>

                <Paper
                    variant="outlined"
                    sx={{
                        flexGrow: 1,
                        minHeight: 0,
                        borderRadius: 2,
                        overflow: 'hidden',
                        display: 'flex',
                        flexDirection: 'column',
                        bgcolor: t.panel,
                        borderColor: t.border,
                        mb: 1,
                    }}
                >
                    {loading ? (
                        <ContainersLoading/>
                    ) : (
                        <Fade in={!loading}>
                            <Box sx={{height: '100%', overflow: 'hidden'}}>
                                <MonitorTable
                                    groups={groups}
                                    history={history}
                                    selected={selected}
                                    onToggleSelect={(ids, on) => setSelected(prev =>
                                        on ? [...new Set([...prev, ...ids])] : prev.filter(id => !ids.includes(id)))}
                                    expanded={expanded}
                                    onToggleExpand={(stack) => setExpanded(prev =>
                                        ({...prev, [stack]: !(prev[stack] ?? true)}))}
                                    now={now}
                                    runningStacks={runningStacks}
                                    onRowAction={handleRowAction}
                                    onRowLogs={handleRowLogs}
                                    onRowExec={handleRowExec}
                                    onStackAction={handleStackAction}
                                    onStackLogs={handleStackLogs}
                                    onStackEdit={handleStackEdit}
                                />
                            </Box>
                        </Fade>
                    )}
                </Paper>
            </Box>

            <LogsPanel/>
        </Box>
    );
}

export default MonitorPage;
