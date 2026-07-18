import {Box, Button, Chip, Divider, Fade, Paper} from '@mui/material';
import {
    ArrowDownward,
    ArrowUpward,
    Delete,
    Pause,
    PlayArrow,
    RestartAlt,
    SpaceDashboardOutlined,
    Stop,
    UnfoldLess,
    UnfoldMore,
} from '@mui/icons-material';
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
import {
    MonitorTable,
    type MonitorRow,
    type RedeployOptions,
    type RowAction,
    type StackAction,
    type StackGroup,
    type StackStats,
} from './monitor-table.tsx';
import {statsTheme as t} from '../compose/components/stats-theme.ts';

// sums the member containers' live metrics and their history windows;
// sparklines scale to the window's shape, so a summed series keeps the
// aggregate's evolution readable
function aggregateStack(rows: MonitorRow[], history: Map<string, { cpu: number[]; mem: number[] }>): StackStats | null {
    let cpu = 0, memUsed = 0, memLimit = 0, seen = 0;
    for (const r of rows) {
        const s = r.stats;
        if (!s) continue;
        seen++;
        cpu += Math.max(s.cpuUsage, 0);
        memUsed += Number(s.memoryUsage);
        // same host-ceiling logic as the aggregate band: unlimited containers
        // report the host total, summing would count it once per container
        memLimit = Math.max(memLimit, Number(s.memoryLimit));
    }
    if (seen === 0) return null;

    const cpuSeries: number[][] = [];
    const memSeries: number[][] = [];
    for (const r of rows) {
        const h = history.get(r.info.name);
        if (!h) continue;
        cpuSeries.push(h.cpu);
        memSeries.push(h.mem);
    }

    return {cpu, memUsed, memLimit, cpuHist: sumSeries(cpuSeries), memHist: sumSeries(memSeries)};
}

// element-wise sum of series aligned on their most recent points
function sumSeries(series: number[][]): number[] {
    const len = Math.max(0, ...series.map(s => s.length));
    const out: number[] = [];
    for (let k = len; k >= 1; k--) {
        let sum = 0;
        for (const s of series) {
            const v = s[s.length - k];
            if (v !== undefined) sum += v;
        }
        out.push(sum);
    }
    return out;
}

// one view to run the host from: real host usage on top, every container
// grouped by stack below it, with per-row, per-stack and bulk controls plus
// the logs/exec bottom panel — no hopping between views. The existing Stats
// and Containers pages are left untouched.
function MonitorPage() {
    const dockerService = useHostClient(DockerService);
    const {containers, loading, refreshContainers, fetchContainers} = useDockerContainers();
    const {history, containers: statContainers, aggregates} = useDockerStats("");
    const hostStats = useHostStats(true);
    const {showSuccess, showError} = useSnackbar();
    const {search, setSearch, searchInputRef} = useSearch();
    const navigate = useNavigate();
    const host = useHostStore(state => state.host);

    // stack and container selections are mutually exclusive; the toolbar
    // switches to whichever kind is active
    const [selectedContainers, setSelectedContainers] = useState<string[]>([]);
    const [selectedStacks, setSelectedStacks] = useState<string[]>([]);
    const [expanded, setExpanded] = useState<Record<string, boolean>>({});
    const [now, setNow] = useState(() => Date.now());

    // uptime column tick; freshness of the values themselves comes from the
    // stats stream
    useEffect(() => {
        const id = setInterval(() => setNow(Date.now()), 10_000);
        return () => clearInterval(id);
    }, []);

    // the bottom panel tabs reference the previous host's containers
    const clearTabs = useTerminalTabs(state => state.clearAll);
    useEffect(() => {
        clearTabs();
        setSelectedContainers([]);
        setSelectedStacks([]);
        setExpanded({});
    }, [host]);

    const createExecUrl = useContainerExecWsUrl();
    const execContainer = useContainerExec(state => state.execParams);
    const openLogs = useLogsPanel(state => state.openLogs);
    const runAction = useComposeAction(state => state.runAction);
    // string-valued selector: output appends leave it unchanged, so the page
    // only re-renders when a stack action actually starts or finishes
    const runningStackKeys = useComposeAction(state =>
        Object.entries(state.runs).filter(([, r]) => r.running).map(([f]) => f).sort().join('|'));

    // the stats stream and the container list agree on names (leading slash
    // trimmed on both sides), while their id fields differ — join by name
    const statsByName = useMemo(() =>
        new Map(statContainers.map(s => [s.name, s])), [statContainers]);

    const groups: StackGroup[] = useMemo(() => {
        const query = search.trim().toLowerCase();
        const list = (containers?.list ?? []).filter(c =>
            !query || [c.name, c.imageName, c.stackName, c.serviceName].some(f => f.toLowerCase().includes(query))
        );

        const byStack = new Map<string, StackGroup>();
        for (const c of list) {
            const key = c.stackName;
            const group = byStack.get(key) ?? {stack: key, servicePath: '', rows: [], stats: null};
            if (c.servicePath) group.servicePath = c.servicePath;
            group.rows = [...group.rows, {info: c, stats: statsByName.get(c.name)}];
            byStack.set(key, group);
        }

        return [...byStack.values()]
            .map(g => ({
                ...g,
                rows: [...g.rows].sort((a, b) => a.info.name.localeCompare(b.info.name)),
                stats: aggregateStack(g.rows, history),
            }))
            .sort((a, b) => {
                // loose containers first (#standalone), then stacks A→Z
                if (!a.stack) return -1;
                if (!b.stack) return 1;
                return a.stack.localeCompare(b.stack);
            });
    }, [containers, statsByName, history, search]);

    const total = containers?.list.length ?? 0;

    // authoritative state counts from the (event-refreshed) container list —
    // the band updates within seconds of a start/stop instead of waiting on
    // a full stats cycle
    const stateCounts = useMemo(() => {
        const list = containers?.list ?? [];
        const counts = {total: list.length, running: 0, stopped: 0, paused: 0, restarting: 0, unhealthy: 0};
        for (const c of list) {
            switch (c.state) {
                case 'running':
                    counts.running++;
                    break;
                case 'paused':
                    counts.paused++;
                    break;
                case 'restarting':
                    counts.restarting++;
                    break;
                default:
                    counts.stopped++;
                    break;
            }
            if (c.health === 'unhealthy') counts.unhealthy++;
        }
        return counts;
    }, [containers]);
    const runningStacks = useMemo(() => {
        const map: Record<string, boolean> = {};
        for (const file of runningStackKeys.split('|')) {
            if (file) map[file] = true;
        }
        return map;
    }, [runningStackKeys]);

    const allExpanded = groups.length > 0 && groups.every(g => expanded[g.stack] ?? false);

    // ---- container actions -------------------------------------------------

    async function containerAction(name: string, rpcName: keyof typeof dockerService, message: string, ids: string[]) {
        // @ts-ignore dynamic rpc dispatch, same pattern as the containers view
        const {err} = await callRPC(() => dockerService[rpcName]({containerIds: ids}));
        if (err) showError(`Failed to ${name} containers: ${err}`);
        else showSuccess(`Successfully ${message} ${ids.length > 1 ? `${ids.length} containers` : 'container'}`);
        setSelectedContainers(prev => prev.filter(id => !ids.includes(id)));
        await fetchContainers();
    }

    const rowRpc: Record<RowAction, { rpc: keyof typeof dockerService, message: string }> = {
        start: {rpc: 'containerStart', message: 'started'},
        stop: {rpc: 'containerStop', message: 'stopped'},
        restart: {rpc: 'containerRestart', message: 'restarted'},
        pause: {rpc: 'containerPause', message: 'paused'},
        unpause: {rpc: 'containerUnpause', message: 'unpaused'},
        remove: {rpc: 'containerRemove', message: 'removed'},
    };

    const handleRowAction = (row: MonitorRow, action: RowAction) =>
        void containerAction(action, rowRpc[action].rpc, rowRpc[action].message, [row.info.id]);

    // ---- stack actions -----------------------------------------------------

    const stackRpc: Record<StackAction, { rpc: 'composeUp' | 'composeDown' | 'composeStart' | 'composeStop' | 'composeRestart', message: string }> = {
        up: {rpc: 'composeUp', message: 'up'},
        down: {rpc: 'composeDown', message: 'down'},
        start: {rpc: 'composeStart', message: 'started'},
        stop: {rpc: 'composeStop', message: 'stopped'},
        restart: {rpc: 'composeRestart', message: 'restarted'},
    };

    const runStack = (stackName: string, servicePath: string, action: StackAction) => {
        const {rpc, message} = stackRpc[action];
        runAction(servicePath, dockerService[rpc], action, [], (error) => {
            if (error) showError(`Stack ${stackName}: ${action} failed — ${error}`);
            else showSuccess(`Stack ${stackName} ${message}`);
            void fetchContainers();
        });
    };

    const handleStackAction = (group: StackGroup, action: StackAction) =>
        runStack(group.stack, group.servicePath, action);

    const handleStackRedeploy = (group: StackGroup, opts: RedeployOptions) => {
        runAction(
            group.servicePath,
            (req, callOpts) => dockerService.composeRedeploy({
                file: {filename: req.filename, selectedServices: req.selectedServices},
                pull: opts.pull,
                build: opts.build,
                recreate: opts.recreate,
            }, callOpts),
            'redeploy',
            [],
            (error) => {
                if (error) showError(`Stack ${group.stack}: redeploy failed — ${error}`);
                else showSuccess(`Stack ${group.stack} redeployed`);
                void fetchContainers();
            },
        );
    };

    // async to satisfy the shared ActionButtons handler contract; the stack
    // runs themselves are fire-and-forget background actions
    const bulkStackAction = async (action: StackAction) => {
        for (const stackName of selectedStacks) {
            const group = groups.find(g => g.stack === stackName);
            if (group?.servicePath) runStack(group.stack, group.servicePath, action);
        }
        setSelectedStacks([]);
    };

    // ---- panel openers -----------------------------------------------------

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

    // ---- selection + toolbar ----------------------------------------------

    const toggleContainers = (ids: string[], on: boolean) => {
        setSelectedStacks([]);
        setSelectedContainers(prev =>
            on ? [...new Set([...prev, ...ids])] : prev.filter(id => !ids.includes(id)));
    };

    const toggleStack = (stack: string, on: boolean) => {
        setSelectedContainers([]);
        setSelectedStacks(prev =>
            on ? [...new Set([...prev, stack])] : prev.filter(s => s !== stack));
    };

    const toggleAllStacks = (stacks: string[], on: boolean) => {
        setSelectedContainers([]);
        setSelectedStacks(on ? stacks : []);
    };

    const stacksMode = selectedStacks.length > 0;

    const containerBulkActions = [
        {
            action: 'start', buttonText: 'Start', icon: <PlayArrow/>,
            disabled: selectedContainers.length === 0,
            handler: () => containerAction('start', 'containerStart', 'started', selectedContainers),
            tooltip: '',
        },
        {
            action: 'stop', buttonText: 'Stop', icon: <Stop/>,
            disabled: selectedContainers.length === 0,
            handler: () => containerAction('stop', 'containerStop', 'stopped', selectedContainers),
            tooltip: '',
        },
        {
            action: 'restart', buttonText: 'Restart', icon: <RestartAlt/>,
            disabled: selectedContainers.length === 0,
            handler: () => containerAction('restart', 'containerRestart', 'restarted', selectedContainers),
            tooltip: '',
        },
        {
            action: 'pause', buttonText: 'Pause', icon: <Pause/>,
            disabled: selectedContainers.length === 0,
            handler: () => containerAction('pause', 'containerPause', 'paused', selectedContainers),
            tooltip: '',
        },
        {
            action: 'unpause', buttonText: 'Unpause', icon: <PlayArrow/>,
            disabled: selectedContainers.length === 0,
            handler: () => containerAction('unpause', 'containerUnpause', 'unpaused', selectedContainers),
            tooltip: '',
        },
        {
            action: 'remove', buttonText: 'Remove', icon: <Delete/>,
            disabled: selectedContainers.length === 0,
            handler: () => containerAction('remove', 'containerRemove', 'removed', selectedContainers),
            tooltip: '',
        },
    ];

    const stackBulkActions = [
        {
            action: 'up', buttonText: 'Up', icon: <ArrowUpward/>,
            disabled: false, handler: () => bulkStackAction('up'), tooltip: '',
        },
        {
            action: 'down', buttonText: 'Down', icon: <ArrowDownward/>,
            disabled: false, handler: () => bulkStackAction('down'), tooltip: '',
        },
        {
            action: 'start', buttonText: 'Start', icon: <PlayArrow/>,
            disabled: false, handler: () => bulkStackAction('start'), tooltip: '',
        },
        {
            action: 'stop', buttonText: 'Stop', icon: <Stop/>,
            disabled: false, handler: () => bulkStackAction('stop'), tooltip: '',
        },
        {
            action: 'restart', buttonText: 'Restart', icon: <RestartAlt/>,
            disabled: false, handler: () => bulkStackAction('restart'), tooltip: '',
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
                    <AggregateStats aggregates={aggregates} hostStats={hostStats}
                                    states={containers ? stateCounts : null}/>
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
                    <ActionButtons actions={stacksMode ? stackBulkActions : containerBulkActions}/>
                    <RefreshButton onClick={refreshContainers} loading={loading}/>
                    <Button
                        variant="outlined"
                        size="small"
                        onClick={() => setExpanded(allExpanded
                            ? {}
                            : Object.fromEntries(groups.map(g => [g.stack, true])))}
                        startIcon={allExpanded ? <UnfoldLess sx={{fontSize: 17}}/> : <UnfoldMore sx={{fontSize: 17}}/>}
                        sx={{
                            textTransform: 'none',
                            fontWeight: 600,
                            px: 1.5,
                            borderColor: 'divider',
                            color: 'text.secondary',
                            whiteSpace: 'nowrap',
                            '&:hover': {borderColor: 'primary.main', color: 'primary.main', bgcolor: 'action.hover'},
                        }}
                    >
                        {allExpanded ? 'Collapse all' : 'Expand all'}
                    </Button>
                    {(stacksMode || selectedContainers.length > 0) && (
                        <>
                            <Divider orientation="vertical" flexItem sx={{mx: 0.5, borderColor: t.border}}/>
                            <Chip
                                size="small"
                                variant="outlined"
                                color="primary"
                                label={stacksMode
                                    ? `${selectedStacks.length} stack${selectedStacks.length > 1 ? 's' : ''} selected`
                                    : `${selectedContainers.length} container${selectedContainers.length > 1 ? 's' : ''} selected`}
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
                                    selectedContainers={selectedContainers}
                                    selectedStacks={selectedStacks}
                                    onToggleContainers={toggleContainers}
                                    onToggleStack={toggleStack}
                                    onToggleAllStacks={toggleAllStacks}
                                    expanded={expanded}
                                    onToggleExpand={(stack) => setExpanded(prev =>
                                        ({...prev, [stack]: !(prev[stack] ?? false)}))}
                                    now={now}
                                    runningStacks={runningStacks}
                                    onRowAction={handleRowAction}
                                    onRowLogs={handleRowLogs}
                                    onRowExec={handleRowExec}
                                    onStackAction={handleStackAction}
                                    onStackRedeploy={handleStackRedeploy}
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
