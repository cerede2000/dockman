import {Box, Divider, Paper, Stack, Tooltip, Typography} from "@mui/material";
import {
    Dns as ContainerIcon,
    ImportExport as NetworkIcon,
    Memory as MemoryIcon,
    Pause as PauseIcon,
    PlayArrow as PlayArrowIcon,
    RestartAlt as RestartIcon,
    Speed as CpuIcon,
    Stop as StopIcon,
    Storage as StorageIcon,
    WarningAmber as WarningIcon,
} from "@mui/icons-material";
import {formatBytes} from "../../../lib/editor.ts";
import {type ReactNode} from "react";
import {type AggregateSnapshot, type HostStatsView} from "../../../hooks/docker-containers-stats.ts";
import Sparkline from "../../../components/sparkline.tsx";
import {statsTheme as t} from "./stats-theme.ts";

interface AggregateStatsProps {
    // computed once per completed refresh cycle — null until the first
    // cycle lands, so the header never renders half-updated totals
    aggregates: AggregateSnapshot | null;
    // when set (general view), CPU and memory show the real host usage
    // Dockhand-style instead of summing container numbers; stack views
    // keep the per-container aggregation
    hostStats?: HostStatsView | null;
}

// load stays default-colored while calm, then warns
const cpuValueColor = (cpu: number) =>
    cpu < 50 ? t.text : cpu < 85 ? '#ffb74d' : '#ef5350';

function AggregateStats({aggregates, hostStats}: AggregateStatsProps) {
    const memPercent = hostStats
        ? (hostStats.memTotal > 0 ? (hostStats.memUsed / hostStats.memTotal) * 100 : 0)
        : (aggregates && aggregates.memLimit > 0 ? (aggregates.memUsed / aggregates.memLimit) * 100 : 0);
    const cpu = hostStats ? hostStats.cpuPercent : (aggregates?.cpu ?? 0);
    const cpuReady = hostStats ? true : aggregates !== null;
    const memUsed = hostStats ? hostStats.memUsed : aggregates?.memUsed;
    const memCeil = hostStats ? hostStats.memTotal : aggregates?.memLimit;

    return (
        <Paper
            variant="outlined"
            sx={{
                px: 2,
                py: 1,
                mb: 1.5,
                borderRadius: 2,
                bgcolor: t.panel,
                borderColor: t.border,
                display: 'flex',
                alignItems: 'center'
            }}
        >
            <Stack
                direction="row"
                spacing={2.5}
                divider={<Divider orientation="vertical" flexItem sx={{borderColor: t.border}}/>}
                sx={{width: '100%', alignItems: 'stretch'}}
            >
                {/* per-state breakdown, wraps onto more lines when narrow */}
                <StateTile aggregates={aggregates}/>

                {/* charted tiles: value block + a chart that takes the room */}
                <ChartTile
                    icon={<CpuIcon/>}
                    label={hostStats ? "Host CPU" : "Total CPU"}
                    value={cpuReady ? `${cpu.toFixed(1)}%` : '–'}
                    valueColor={cpuValueColor(cpu)}
                    sub={hostStats && hostStats.cpus > 0 ? `${hostStats.cpus} cores` : ''}
                    data={hostStats ? hostStats.cpuHistory : aggregates?.cpuHistory ?? []}
                    color={t.cpuLine}
                />

                <ChartTile
                    icon={<MemoryIcon/>}
                    label={hostStats ? "Host Memory" : "Memory"}
                    value={memUsed !== undefined ? formatBytes(memUsed) : '–'}
                    sub={memCeil && memCeil > 0
                        ? `${memPercent.toFixed(1)}% of ${formatBytes(memCeil)}`
                        : ''}
                    data={hostStats ? hostStats.memHistory : aggregates?.memHistory ?? []}
                    color={t.memLine}
                />

                <CompactTile
                    icon={<NetworkIcon/>}
                    label="Network I/O"
                    value={aggregates ? `↓ ${formatBytes(aggregates.netRx)}` : '–'}
                    sub={aggregates ? `↑ ${formatBytes(aggregates.netTx)}` : ''}
                    tooltip={aggregates ? `Total ${formatBytes(aggregates.netRx + aggregates.netTx)}` : ''}
                />

                <CompactTile
                    icon={<StorageIcon/>}
                    label="Block I/O"
                    value={aggregates ? `R ${formatBytes(aggregates.diskR)}` : '–'}
                    sub={aggregates ? `W ${formatBytes(aggregates.diskW)}` : ''}
                    tooltip={aggregates ? `Total ${formatBytes(aggregates.diskR + aggregates.diskW)}` : ''}
                />
            </Stack>
        </Paper>
    );
}

export default AggregateStats;

// Dockhand-style status strip: one icon+count pair per container state.
// The total lives on the label line so the strip itself stays one row in
// the common case (it can still wrap when very narrow).
function StateTile({aggregates}: { aggregates: AggregateSnapshot | null }) {
    const entries: { icon: ReactNode, count: number, color: string, title: string }[] = aggregates ? [
        {icon: <PlayArrowIcon/>, count: aggregates.running, color: '#66bb6a', title: 'Running'},
        {icon: <StopIcon/>, count: aggregates.stopped, color: '#9e9e9e', title: 'Stopped'},
        {icon: <PauseIcon/>, count: aggregates.paused, color: '#ffb74d', title: 'Paused'},
        {icon: <RestartIcon/>, count: aggregates.restarting, color: '#4db6ac', title: 'Restarting'},
        {icon: <WarningIcon/>, count: aggregates.unhealthy, color: '#ef5350', title: 'Unhealthy'},
    ] : [];

    return (
        <Box sx={{flex: '0 1 auto', minWidth: 108, maxWidth: 175, alignSelf: 'center'}}>
            <TileLabel icon={<ContainerIcon/>}
                       label={aggregates ? `Containers · ${aggregates.total}` : 'Containers'}/>
            {aggregates ? (
                <Stack direction="row" spacing={1} alignItems="center" flexWrap="wrap" useFlexGap>
                    {entries.map(e => (
                        <Tooltip key={e.title} title={e.title} arrow placement="top">
                            <Stack direction="row" spacing={0.25} alignItems="center" sx={{color: e.color}}>
                                <Box sx={{display: 'flex', '& svg': {fontSize: 15}}}>{e.icon}</Box>
                                <Typography sx={{fontFamily: t.mono, fontWeight: 700, fontSize: '0.85rem', lineHeight: 1}}>
                                    {e.count}
                                </Typography>
                            </Stack>
                        </Tooltip>
                    ))}
                </Stack>
            ) : (
                <Typography sx={{fontFamily: t.mono, fontWeight: 700, fontSize: '0.95rem', color: t.text}}>
                    –
                </Typography>
            )}
        </Box>
    );
}

function TileLabel({icon, label}: { icon: ReactNode, label: string }) {
    return (
        <Stack direction="row" spacing={0.75} alignItems="center" sx={{mb: 0.25, color: t.textDim}}>
            <Box sx={{display: 'flex', '& svg': {fontSize: 14}}}>{icon}</Box>
            <Typography variant="overline" noWrap
                        sx={{fontWeight: 700, lineHeight: 1, letterSpacing: '0.08em', fontSize: '0.62rem'}}>
                {label}
            </Typography>
        </Stack>
    );
}

// label + a single value line (optional dim inline detail); used for the
// counters that need no chart, kept narrow so charted tiles get the room
function CompactTile({icon, label, value, sub, tooltip, grow = true}: {
    icon: ReactNode,
    label: string,
    value: string,
    sub?: string,
    tooltip?: string,
    grow?: boolean,
}) {
    const body = (
        <Box sx={{flex: grow ? '1 1 0' : '0 0 auto', minWidth: 0, alignSelf: 'center', width: grow ? undefined : 'auto'}}>
            <TileLabel icon={icon} label={label}/>
            <Stack direction="row" spacing={1} alignItems="baseline" sx={{minWidth: 0}}>
                <Typography noWrap sx={{
                    fontFamily: t.mono,
                    fontWeight: 700,
                    fontSize: '0.95rem',
                    lineHeight: 1.3,
                    color: t.text,
                    flexShrink: 0,
                }}>
                    {value}
                </Typography>
                {sub && (
                    <Typography variant="caption" noWrap sx={{color: t.textDim, fontFamily: t.mono, minWidth: 0}}>
                        {sub}
                    </Typography>
                )}
            </Stack>
        </Box>
    );

    return tooltip ? <Tooltip title={tooltip} arrow placement="top">{body}</Tooltip> : body;
}

// label + value (and detail below it) with a chart filling the tile's
// remaining width — CPU and memory read at a glance, same size for both.
// The value block has a fixed width so both sparklines end up the same
// length regardless of how wide the numbers are.
function ChartTile({icon, label, value, valueColor, sub, data, color}: {
    icon: ReactNode,
    label: string,
    value: string,
    valueColor?: string,
    sub: string,
    data: number[],
    color: string,
}) {
    return (
        <Box sx={{flex: '1.5 1 0', minWidth: 0}}>
            <TileLabel icon={icon} label={label}/>
            <Stack direction="row" spacing={1.5} alignItems="center" sx={{minWidth: 0}}>
                <Box sx={{flexShrink: 0, width: 134, overflow: 'hidden'}}>
                    <Typography noWrap sx={{
                        fontFamily: t.mono,
                        fontWeight: 700,
                        fontSize: '1.05rem',
                        lineHeight: 1.2,
                        color: valueColor ?? t.text,
                    }}>
                        {value}
                    </Typography>
                    <Typography variant="caption" noWrap sx={{
                        color: t.textDim,
                        fontFamily: t.mono,
                        display: 'block',
                        minHeight: '1.1em',
                    }}>
                        {sub}
                    </Typography>
                </Box>
                <Box sx={{flexGrow: 1, minWidth: 60}}>
                    <Sparkline data={data} color={color} height={32}/>
                </Box>
            </Stack>
        </Box>
    );
}
