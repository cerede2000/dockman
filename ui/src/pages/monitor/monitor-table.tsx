import {
    Box,
    Checkbox,
    Chip,
    CircularProgress,
    IconButton,
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
    ArrowUpward,
    EditNote,
    ExpandLess,
    ExpandMore,
    PlayArrow,
    RestartAlt,
    Stop,
    Subject,
    Terminal,
    Upgrade,
} from '@mui/icons-material';
import {Fragment, type ReactNode} from 'react';
import type {ContainerList, ContainerStats} from '../../gen/docker/v1/docker_pb.ts';
import {statsTheme as t, stateBadges} from '../compose/components/stats-theme.ts';
import Sparkline from '../../components/sparkline.tsx';
import {formatBytes} from '../../lib/editor.ts';
import {formatTimeAgo} from '../../lib/table.ts';

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

export type RowAction = 'start' | 'stop' | 'restart';
export type StackAction = 'up' | 'stop' | 'restart';

interface MonitorTableProps {
    groups: StackGroup[];
    history: Map<string, { cpu: number[]; mem: number[] }>;
    selected: string[];
    onToggleSelect: (ids: string[], on: boolean) => void;
    expanded: Record<string, boolean>;
    onToggleExpand: (stack: string) => void;
    // explicit clock so the memoized rows re-render on tick
    now: number;
    runningStacks: Record<string, boolean>;
    onRowAction: (row: MonitorRow, action: RowAction) => void;
    onRowLogs: (row: MonitorRow) => void;
    onRowExec: (row: MonitorRow) => void;
    onStackAction: (group: StackGroup, action: StackAction) => void;
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

export function MonitorTable(props: MonitorTableProps) {
    const {groups, selected, onToggleSelect} = props;

    const allIds = groups.flatMap(g => g.rows.map(r => r.info.id));
    const allSelected = allIds.length > 0 && allIds.every(id => selected.includes(id));
    const someSelected = allIds.some(id => selected.includes(id));

    return (
        <TableContainer sx={{height: '100%', bgcolor: t.panel}}>
            <Table size="small" stickyHeader>
                <TableHead>
                    <TableRow>
                        <TableCell padding="checkbox" sx={headCell}>
                            <Checkbox
                                size="small"
                                checked={allSelected}
                                indeterminate={someSelected && !allSelected}
                                onChange={e => onToggleSelect(allIds, e.target.checked)}
                                sx={{color: t.textDim, p: 0.5}}
                            />
                        </TableCell>
                        <TableCell sx={headCell}>NAME</TableCell>
                        <TableCell sx={headCell}>STATE</TableCell>
                        <TableCell sx={headCell}>UPTIME</TableCell>
                        <TableCell sx={{...headCell, minWidth: 130}}>CPU</TableCell>
                        <TableCell sx={{...headCell, minWidth: 150}}>MEMORY</TableCell>
                        <TableCell sx={headCell}>NET I/O</TableCell>
                        <TableCell sx={headCell}>PORTS</TableCell>
                        <TableCell sx={{...headCell, width: 170}} align="right">ACTIONS</TableCell>
                    </TableRow>
                </TableHead>
                <TableBody>
                    {groups.map(group => (
                        <Fragment key={group.stack || '(standalone)'}>
                            <StackRow {...props} group={group}/>
                            {(props.expanded[group.stack] ?? true) && group.rows.map(row => (
                                <ContainerRow {...props} key={row.info.id} row={row}/>
                            ))}
                        </Fragment>
                    ))}
                </TableBody>
            </Table>
        </TableContainer>
    );
}

function StackRow({group, expanded, onToggleExpand, selected, onToggleSelect, runningStacks, onStackAction, onStackLogs, onStackEdit}:
                      MonitorTableProps & { group: StackGroup }) {
    const isExpanded = expanded[group.stack] ?? true;
    const ids = group.rows.map(r => r.info.id);
    const allChecked = ids.length > 0 && ids.every(id => selected.includes(id));
    const someChecked = ids.some(id => selected.includes(id));
    const running = group.rows.filter(r => r.info.state === 'running').length;
    const hasFile = group.servicePath !== '';
    const busy = runningStacks[group.servicePath] ?? false;

    const stackActions: { action: StackAction, title: string, icon: ReactNode }[] = [
        {action: 'up', title: 'Up', icon: <ArrowUpward sx={{fontSize: 15}}/>},
        {action: 'stop', title: 'Stop', icon: <Stop sx={{fontSize: 15}}/>},
        {action: 'restart', title: 'Restart', icon: <RestartAlt sx={{fontSize: 15}}/>},
    ];

    return (
        <TableRow sx={{bgcolor: 'rgba(255,255,255,0.03)'}}>
            <TableCell padding="checkbox" sx={bodyCell}>
                <Checkbox
                    size="small"
                    checked={allChecked}
                    indeterminate={someChecked && !allChecked}
                    onChange={e => onToggleSelect(ids, e.target.checked)}
                    sx={{color: t.textDim, p: 0.5}}
                />
            </TableCell>
            <TableCell colSpan={7} sx={{...bodyCell, py: 0.25}}>
                <Stack direction="row" spacing={0.75} alignItems="center">
                    <IconButton size="small" onClick={() => onToggleExpand(group.stack)}
                                sx={{color: t.textDim, p: 0.25}}>
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
                {hasFile && stackActions.map(a => (
                    <Tooltip key={a.action} title={`Stack ${a.title.toLowerCase()}`} arrow>
                        <span>
                            <IconButton size="small" disabled={busy}
                                        onClick={() => onStackAction(group, a.action)}
                                        sx={{color: t.textDim, '&:hover': {color: t.text}}}>
                                {a.icon}
                            </IconButton>
                        </span>
                    </Tooltip>
                ))}
                <Tooltip title="Stack logs" arrow>
                    <IconButton size="small" onClick={() => onStackLogs(group)}
                                sx={{color: t.textDim, '&:hover': {color: t.text}}}>
                        <Subject sx={{fontSize: 15}}/>
                    </IconButton>
                </Tooltip>
                {hasFile && (
                    <Tooltip title="Open compose file" arrow>
                        <IconButton size="small" onClick={() => onStackEdit(group)}
                                    sx={{color: t.textDim, '&:hover': {color: t.text}}}>
                            <EditNote sx={{fontSize: 16}}/>
                        </IconButton>
                    </Tooltip>
                )}
            </TableCell>
        </TableRow>
    );
}

function ContainerRow({row, history, selected, onToggleSelect, now, onRowAction, onRowLogs, onRowExec}:
                          MonitorTableProps & { row: MonitorRow }) {
    const c = row.info;
    const s = row.stats;
    const hist = history.get(c.name);
    const badge = stateBadges[c.state] ?? {bg: 'rgba(71,85,105,0.55)', fg: '#cbd5e1', label: c.state};
    const isRunning = c.state === 'running';
    const isChecked = selected.includes(c.id);

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
                    onChange={e => onToggleSelect([c.id], e.target.checked)}
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
            <TableCell sx={bodyCell}>
                <Stack direction="row" spacing={0.5} alignItems="center">
                    <Chip label={badge.label} size="small"
                          sx={{bgcolor: badge.bg, color: badge.fg, fontWeight: 700, fontSize: '0.65rem', height: 19}}/>
                    {c.health && (
                        <Typography sx={{
                            fontSize: '0.68rem',
                            fontFamily: t.mono,
                            color: c.health === 'healthy' ? '#66bb6a' : c.health === 'unhealthy' ? '#ef5350' : t.textDim,
                        }}>
                            {c.health}
                        </Typography>
                    )}
                </Stack>
            </TableCell>
            <TableCell sx={{...bodyCell, whiteSpace: 'nowrap', fontFamily: t.mono, fontSize: '0.75rem', color: t.textDim}}>
                {isRunning && c.created ? formatTimeAgo(new Date(c.created), now) : '–'}
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
                <Tooltip title="Start" arrow>
                    <span>
                        <IconButton size="small" disabled={isRunning}
                                    onClick={() => onRowAction(row, 'start')}
                                    sx={{color: '#66bb6a', '&.Mui-disabled': {color: 'rgba(255,255,255,0.15)'}}}>
                            <PlayArrow sx={{fontSize: 16}}/>
                        </IconButton>
                    </span>
                </Tooltip>
                <Tooltip title="Stop" arrow>
                    <span>
                        <IconButton size="small" disabled={!isRunning}
                                    onClick={() => onRowAction(row, 'stop')}
                                    sx={{color: '#ef5350', '&.Mui-disabled': {color: 'rgba(255,255,255,0.15)'}}}>
                            <Stop sx={{fontSize: 16}}/>
                        </IconButton>
                    </span>
                </Tooltip>
                <Tooltip title="Restart" arrow>
                    <IconButton size="small" onClick={() => onRowAction(row, 'restart')}
                                sx={{color: '#4db6ac'}}>
                        <RestartAlt sx={{fontSize: 16}}/>
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
                                    sx={{color: t.textDim, '&:hover': {color: t.text}, '&.Mui-disabled': {color: 'rgba(255,255,255,0.15)'}}}>
                            <Terminal sx={{fontSize: 16}}/>
                        </IconButton>
                    </span>
                </Tooltip>
            </TableCell>
        </TableRow>
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
