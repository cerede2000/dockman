import {
    Box,
    Checkbox,
    CircularProgress,
    IconButton,
    ListItemIcon,
    ListItemText,
    Menu,
    MenuItem,
    Stack,
    Table,
    TableBody,
    TableCell,
    TableContainer,
    TableHead,
    TableRow,
    Tooltip,
    Typography,
} from '@mui/material';
import {
    ArrowDownward,
    ArrowUpward,
    Build,
    CloudDownload,
    Delete,
    EditNote,
    ExpandLess,
    ExpandMore,
    Pause,
    PlayArrow,
    RestartAlt,
    RocketLaunch,
    Stop,
    Subject,
    Terminal,
    Upgrade,
    WarningAmber,
} from '@mui/icons-material';
import {Fragment, type MouseEvent, type ReactNode, useState} from 'react';
import type {ContainerList, ContainerStats} from '../../gen/docker/v1/docker_pb.ts';
import {statsTheme as t} from '../compose/components/stats-theme.ts';
import Sparkline from '../../components/sparkline.tsx';
import {formatBytes} from '../../lib/editor.ts';

export interface MonitorRow {
    info: ContainerList;
    stats?: ContainerStats;
}

export interface StackGroup {
    // display name; empty for containers outside any compose stack
    stack: string;
    // compose file in dockman's namespace, empty when unknown — stack-level
    // actions and the editor shortcut need it
    servicePath: string;
    rows: MonitorRow[];
}

export type RowAction = 'start' | 'stop' | 'restart' | 'pause' | 'unpause' | 'remove';
export type StackAction = 'up' | 'down' | 'start' | 'stop' | 'restart';

export interface RedeployOptions {
    pull: boolean;
    build: boolean;
    recreate: boolean;
}

interface MonitorTableProps {
    groups: StackGroup[];
    history: Map<string, { cpu: number[]; mem: number[] }>;
    // stack and container selections are mutually exclusive: picking one
    // kind clears the other, and the toolbar actions adapt to the kind
    selectedContainers: string[];
    selectedStacks: string[];
    onToggleContainers: (ids: string[], on: boolean) => void;
    onToggleStack: (stack: string, on: boolean) => void;
    onToggleAllStacks: (stacks: string[], on: boolean) => void;
    expanded: Record<string, boolean>;
    onToggleExpand: (stack: string) => void;
    // explicit clock so the memoized rows re-render on tick
    now: number;
    runningStacks: Record<string, boolean>;
    onRowAction: (row: MonitorRow, action: RowAction) => void;
    onRowLogs: (row: MonitorRow) => void;
    onRowExec: (row: MonitorRow) => void;
    onStackAction: (group: StackGroup, action: StackAction) => void;
    onStackRedeploy: (group: StackGroup, opts: RedeployOptions) => void;
    onStackLogs: (group: StackGroup) => void;
    onStackEdit: (group: StackGroup) => void;
}

const headCell = {
    bgcolor: t.header,
    color: t.textDim,
    borderColor: t.border,
    fontWeight: 700,
    fontSize: '0.68rem',
    letterSpacing: '0.08em',
    whiteSpace: 'nowrap' as const,
    py: 0.75,
};

const bodyCell = {
    borderColor: t.border,
    color: t.text,
    py: 0.5,
};

const disabledIcon = {color: 'rgba(255,255,255,0.15)'};

// per-state icon + color, shared by the state column
const stateVisual: Record<string, { icon: ReactNode, color: string }> = {
    running: {icon: <PlayArrow/>, color: '#66bb6a'},
    restarting: {icon: <RestartAlt/>, color: '#4db6ac'},
    paused: {icon: <Pause/>, color: '#ffb74d'},
    exited: {icon: <Stop/>, color: '#9e9e9e'},
    created: {icon: <Stop/>, color: '#64748b'},
    dead: {icon: <WarningAmber/>, color: '#ef5350'},
    removing: {icon: <Delete/>, color: '#ef5350'},
};

export function MonitorTable(props: MonitorTableProps) {
    const {groups, selectedStacks, onToggleAllStacks} = props;

    // header checkbox drives stack selection (containers are picked row by
    // row); standalone containers have no stack to select
    const selectableStacks = groups.filter(g => g.stack && g.servicePath).map(g => g.stack);
    const allStacksSelected = selectableStacks.length > 0 && selectableStacks.every(s => selectedStacks.includes(s));
    const someStacksSelected = selectableStacks.some(s => selectedStacks.includes(s));

    return (
        <TableContainer sx={{height: '100%', bgcolor: t.panel}}>
            <Table size="small" stickyHeader>
                <TableHead>
                    <TableRow>
                        <TableCell padding="checkbox" sx={headCell}>
                            <Tooltip title="Select all stacks" arrow>
                                <Checkbox
                                    size="small"
                                    checked={allStacksSelected}
                                    indeterminate={someStacksSelected && !allStacksSelected}
                                    onChange={e => onToggleAllStacks(selectableStacks, e.target.checked)}
                                    sx={{color: t.textDim, p: 0.5}}
                                />
                            </Tooltip>
                        </TableCell>
                        <TableCell sx={headCell}>NAME</TableCell>
                        <TableCell sx={headCell}>STATE</TableCell>
                        <TableCell sx={headCell}>UPTIME</TableCell>
                        <TableCell sx={{...headCell, minWidth: 130}}>CPU</TableCell>
                        <TableCell sx={{...headCell, minWidth: 150}}>MEMORY</TableCell>
                        <TableCell sx={headCell}>NET I/O</TableCell>
                        <TableCell sx={headCell}>PORTS</TableCell>
                        <TableCell sx={{...headCell, width: 210}} align="right">ACTIONS</TableCell>
                    </TableRow>
                </TableHead>
                <TableBody>
                    {groups.map(group => (
                        <Fragment key={group.stack || '(standalone)'}>
                            <StackRow {...props} group={group}/>
                            {(props.expanded[group.stack] ?? false) && group.rows.map(row => (
                                <ContainerRow {...props} key={row.info.id} row={row}/>
                            ))}
                        </Fragment>
                    ))}
                </TableBody>
            </Table>
        </TableContainer>
    );
}

function StackRow(props: MonitorTableProps & { group: StackGroup }) {
    const {
        group, expanded, onToggleExpand, selectedStacks, selectedContainers,
        onToggleStack, onToggleContainers, runningStacks,
        onStackAction, onStackRedeploy, onStackLogs, onStackEdit,
    } = props;

    const isExpanded = expanded[group.stack] ?? false;
    const isStack = group.stack !== '';
    const hasFile = group.servicePath !== '';
    const busy = runningStacks[group.servicePath] ?? false;
    const running = group.rows.filter(r => r.info.state === 'running').length;
    // paused/restarting containers still count as an "active" stack: down is
    // the meaningful direction, up only once everything is stopped
    const active = group.rows.some(r => ['running', 'restarting', 'paused'].includes(r.info.state));

    const ids = group.rows.map(r => r.info.id);
    const checked = isStack && hasFile
        ? selectedStacks.includes(group.stack)
        : ids.length > 0 && ids.every(id => selectedContainers.includes(id));
    const indeterminate = !(isStack && hasFile)
        && !checked && ids.some(id => selectedContainers.includes(id));

    return (
        <TableRow sx={{bgcolor: 'rgba(255,255,255,0.03)'}}>
            <TableCell padding="checkbox" sx={bodyCell}>
                <Tooltip title={isStack && hasFile ? "Select stack" : "Select containers"} arrow>
                    <Checkbox
                        size="small"
                        checked={checked}
                        indeterminate={indeterminate}
                        onChange={e => isStack && hasFile
                            ? onToggleStack(group.stack, e.target.checked)
                            : onToggleContainers(ids, e.target.checked)}
                        sx={{color: t.textDim, p: 0.5}}
                    />
                </Tooltip>
            </TableCell>
            <TableCell colSpan={7} sx={{...bodyCell, py: 0.25}}>
                <Stack direction="row" spacing={0.75} alignItems="center"
                       onClick={() => onToggleExpand(group.stack)}
                       sx={{cursor: 'pointer', userSelect: 'none'}}>
                    <IconButton size="small" sx={{color: t.textDim, p: 0.25}}>
                        {isExpanded ? <ExpandLess sx={{fontSize: 17}}/> : <ExpandMore sx={{fontSize: 17}}/>}
                    </IconButton>
                    <Typography sx={{fontWeight: 700, fontSize: '0.85rem', color: t.text}}>
                        {group.stack || 'standalone'}
                    </Typography>
                    <Typography sx={{color: t.textDim, fontFamily: t.mono, fontSize: '0.72rem'}}>
                        {running}/{group.rows.length} running
                    </Typography>
                    {busy && <CircularProgress size={12} sx={{color: t.textDim}}/>}
                </Stack>
            </TableCell>
            <TableCell align="right" sx={{...bodyCell, py: 0.25, whiteSpace: 'nowrap'}}>
                {hasFile && (
                    <>
                        <StackActionButton
                            title={active ? 'Stack down (remove containers)' : 'Stack up'}
                            disabled={busy}
                            onClick={() => onStackAction(group, active ? 'down' : 'up')}
                            icon={active ? <ArrowDownward sx={{fontSize: 15}}/> : <ArrowUpward sx={{fontSize: 15}}/>}
                        />
                        <StackActionButton
                            title={running > 0 ? 'Stack stop' : 'Stack start'}
                            disabled={busy}
                            onClick={() => onStackAction(group, running > 0 ? 'stop' : 'start')}
                            icon={running > 0 ? <Stop sx={{fontSize: 15}}/> : <PlayArrow sx={{fontSize: 15}}/>}
                        />
                        <StackActionButton
                            title="Stack restart"
                            disabled={busy}
                            onClick={() => onStackAction(group, 'restart')}
                            icon={<RestartAlt sx={{fontSize: 15}}/>}
                        />
                        <RedeployMenuButton disabled={busy} onPick={opts => onStackRedeploy(group, opts)}/>
                    </>
                )}
                <StackActionButton
                    title={isStack ? "Stack logs" : "Logs"}
                    onClick={() => onStackLogs(group)}
                    icon={<Subject sx={{fontSize: 15}}/>}
                />
                {hasFile && (
                    <StackActionButton
                        title="Open compose file"
                        onClick={() => onStackEdit(group)}
                        icon={<EditNote sx={{fontSize: 16}}/>}
                    />
                )}
            </TableCell>
        </TableRow>
    );
}

function StackActionButton({title, icon, onClick, disabled}: {
    title: string,
    icon: ReactNode,
    onClick: () => void,
    disabled?: boolean,
}) {
    return (
        <Tooltip title={title} arrow>
            <span>
                <IconButton size="small" disabled={disabled} onClick={onClick}
                            sx={{color: t.textDim, '&:hover': {color: t.text}, '&.Mui-disabled': disabledIcon}}>
                    {icon}
                </IconButton>
            </span>
        </Tooltip>
    );
}

// redeploy = compose up -d with a forced option, picked from a small menu
function RedeployMenuButton({disabled, onPick}: {
    disabled?: boolean,
    onPick: (opts: RedeployOptions) => void,
}) {
    const [anchor, setAnchor] = useState<null | HTMLElement>(null);

    const pick = (opts: RedeployOptions) => {
        setAnchor(null);
        onPick(opts);
    };

    return (
        <>
            <Tooltip title="Redeploy…" arrow>
                <span>
                    <IconButton size="small" disabled={disabled}
                                onClick={(e: MouseEvent<HTMLElement>) => setAnchor(e.currentTarget)}
                                sx={{color: t.textDim, '&:hover': {color: t.text}, '&.Mui-disabled': disabledIcon}}>
                        <RocketLaunch sx={{fontSize: 15}}/>
                    </IconButton>
                </span>
            </Tooltip>
            <Menu anchorEl={anchor} open={anchor !== null} onClose={() => setAnchor(null)}>
                <MenuItem onClick={() => pick({pull: true, build: false, recreate: false})}>
                    <ListItemIcon><CloudDownload sx={{fontSize: 17}}/></ListItemIcon>
                    <ListItemText primary="Pull images (force)" secondary="up -d --pull always"/>
                </MenuItem>
                <MenuItem onClick={() => pick({pull: false, build: true, recreate: false})}>
                    <ListItemIcon><Build sx={{fontSize: 17}}/></ListItemIcon>
                    <ListItemText primary="Build images (force)" secondary="up -d --build"/>
                </MenuItem>
                <MenuItem onClick={() => pick({pull: false, build: false, recreate: true})}>
                    <ListItemIcon><RestartAlt sx={{fontSize: 17}}/></ListItemIcon>
                    <ListItemText primary="Recreate (force)" secondary="up -d --force-recreate"/>
                </MenuItem>
            </Menu>
        </>
    );
}

function ContainerRow(props: MonitorTableProps & { row: MonitorRow }) {
    const {row, history, selectedContainers, onToggleContainers, now, onRowAction, onRowLogs, onRowExec} = props;
    const c = row.info;
    const s = row.stats;
    const hist = history.get(c.name);
    const isRunning = c.state === 'running';
    const isPaused = c.state === 'paused';
    const isActive = ['running', 'restarting', 'paused'].includes(c.state);
    const isChecked = selectedContainers.includes(c.id);

    const ports = c.ports
        .filter(p => p.public > 0)
        .map(p => `${p.public}:${p.private}`)
        .filter((v, i, arr) => arr.indexOf(v) === i);

    return (
        <TableRow hover sx={{'&:hover': {bgcolor: t.rowHover}}}>
            <TableCell padding="checkbox" sx={bodyCell}>
                <Checkbox
                    size="small"
                    checked={isChecked}
                    onChange={e => onToggleContainers([c.id], e.target.checked)}
                    sx={{color: t.textDim, p: 0.5}}
                />
            </TableCell>
            <TableCell sx={{...bodyCell, maxWidth: 240}}>
                <Stack direction="row" spacing={0.5} alignItems="center">
                    <Typography noWrap sx={{fontWeight: 600, fontSize: '0.82rem'}}>
                        {c.name}
                    </Typography>
                    {c.updateAvailable && (
                        <Tooltip title={`Update available: ${c.updateAvailable}`} arrow>
                            <Upgrade sx={{fontSize: 14, color: '#4db6ac'}}/>
                        </Tooltip>
                    )}
                </Stack>
                <Typography noWrap sx={{color: t.textDim, fontSize: '0.7rem', fontFamily: t.mono}}>
                    {c.imageName}
                </Typography>
            </TableCell>
            <StateCell state={c.state} health={c.health}/>
            <TableCell sx={{...bodyCell, whiteSpace: 'nowrap', fontFamily: t.mono, fontSize: '0.75rem', color: t.textDim}}>
                {isRunning && s ? formatUptime(s.startedAt, now) : '–'}
            </TableCell>
            <MetricCell
                value={s ? `${s.cpuUsage.toFixed(1)}%` : '–'}
                data={hist?.cpu ?? []}
                color={t.cpuLine}
            />
            <MetricCell
                value={s ? formatBytes(Number(s.memoryUsage)) : '–'}
                data={hist?.mem ?? []}
                color={t.memLine}
            />
            <TableCell sx={{...bodyCell, whiteSpace: 'nowrap', fontFamily: t.mono, fontSize: '0.72rem', color: t.textDim}}>
                {s ? <>↓ {formatBytes(Number(s.networkRx))}<br/>↑ {formatBytes(Number(s.networkTx))}</> : '–'}
            </TableCell>
            <TableCell sx={{...bodyCell, maxWidth: 150}}>
                <Tooltip title={ports.join(', ')} arrow disableHoverListener={ports.length === 0}>
                    <Typography noWrap sx={{fontFamily: t.mono, fontSize: '0.72rem', color: t.textDim}}>
                        {ports.length > 0 ? ports.join(', ') : '–'}
                    </Typography>
                </Tooltip>
            </TableCell>
            <TableCell align="right" sx={{...bodyCell, whiteSpace: 'nowrap'}}>
                <Tooltip title={isActive ? 'Stop' : 'Start'} arrow>
                    <IconButton size="small"
                                onClick={() => onRowAction(row, isActive ? 'stop' : 'start')}
                                sx={{color: isActive ? '#ef5350' : '#66bb6a'}}>
                        {isActive ? <Stop sx={{fontSize: 16}}/> : <PlayArrow sx={{fontSize: 16}}/>}
                    </IconButton>
                </Tooltip>
                <Tooltip title="Restart" arrow>
                    <IconButton size="small" onClick={() => onRowAction(row, 'restart')}
                                sx={{color: '#4db6ac'}}>
                        <RestartAlt sx={{fontSize: 16}}/>
                    </IconButton>
                </Tooltip>
                <Tooltip title={isPaused ? 'Unpause' : 'Pause'} arrow>
                    <span>
                        <IconButton size="small" disabled={!isRunning && !isPaused}
                                    onClick={() => onRowAction(row, isPaused ? 'unpause' : 'pause')}
                                    sx={{color: isPaused ? '#ffb74d' : t.textDim, '&:hover': {color: t.text}, '&.Mui-disabled': disabledIcon}}>
                            <Pause sx={{fontSize: 16}}/>
                        </IconButton>
                    </span>
                </Tooltip>
                <Tooltip title="Remove" arrow>
                    <IconButton size="small" onClick={() => onRowAction(row, 'remove')}
                                sx={{color: t.textDim, '&:hover': {color: '#ef5350'}}}>
                        <Delete sx={{fontSize: 16}}/>
                    </IconButton>
                </Tooltip>
                <Tooltip title="Logs" arrow>
                    <IconButton size="small" onClick={() => onRowLogs(row)}
                                sx={{color: t.textDim, '&:hover': {color: t.text}}}>
                        <Subject sx={{fontSize: 16}}/>
                    </IconButton>
                </Tooltip>
                <Tooltip title="Exec" arrow>
                    <span>
                        <IconButton size="small" disabled={!isRunning}
                                    onClick={() => onRowExec(row)}
                                    sx={{color: t.textDim, '&:hover': {color: t.text}, '&.Mui-disabled': disabledIcon}}>
                            <Terminal sx={{fontSize: 16}}/>
                        </IconButton>
                    </span>
                </Tooltip>
            </TableCell>
        </TableRow>
    );
}

// state as a colored icon (tooltip carries the word), health spelled out
// next to it when the container has a healthcheck
function StateCell({state, health}: { state: string, health: string }) {
    const visual = (health === 'unhealthy')
        ? {icon: <WarningAmber/>, color: '#ef5350'}
        : stateVisual[state] ?? {icon: <Stop/>, color: t.textDim};

    return (
        <TableCell sx={bodyCell}>
            <Stack direction="row" spacing={0.5} alignItems="center">
                <Tooltip title={state} arrow>
                    <Box sx={{display: 'flex', color: visual.color, '& svg': {fontSize: 17}}}>
                        {visual.icon}
                    </Box>
                </Tooltip>
                {health && (
                    <Typography sx={{
                        fontSize: '0.7rem',
                        fontFamily: t.mono,
                        color: health === 'healthy' ? '#66bb6a' : health === 'unhealthy' ? '#ef5350' : t.textDim,
                    }}>
                        {health}
                    </Typography>
                )}
            </Stack>
        </TableCell>
    );
}

function MetricCell({value, data, color}: { value: string, data: number[], color: string }) {
    return (
        <TableCell sx={bodyCell}>
            <Stack direction="row" spacing={1} alignItems="center">
                <Typography sx={{
                    fontFamily: t.mono,
                    fontWeight: 600,
                    fontSize: '0.78rem',
                    minWidth: 62,
                    flexShrink: 0,
                }}>
                    {value}
                </Typography>
                <Box sx={{width: 64}}>
                    <Sparkline data={data} color={color} height={20}/>
                </Box>
            </Stack>
        </TableCell>
    );
}

// how long the container has been up, from the stats' RFC3339 started_at:
// "3d 4h", "5h 12m", "8m", "42s"
function formatUptime(startedAt: string, now: number): string {
    if (!startedAt || startedAt.startsWith('0001')) return '–';
    const start = Date.parse(startedAt);
    if (isNaN(start)) return '–';
    let secs = Math.floor((now - start) / 1000);
    if (secs < 0) secs = 0;
    const d = Math.floor(secs / 86400);
    const h = Math.floor((secs % 86400) / 3600);
    const m = Math.floor((secs % 3600) / 60);
    if (d > 0) return `${d}d ${h}h`;
    if (h > 0) return `${h}h ${m}m`;
    if (m > 0) return `${m}m`;
    return `${secs}s`;
}
