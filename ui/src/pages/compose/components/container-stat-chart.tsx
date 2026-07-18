import {Box, Divider, Paper, Stack, Tooltip, Typography} from "@mui/material";
import {
    Dns as ContainerIcon,
    ImportExport as NetworkIcon,
    Memory as MemoryIcon,
    Speed as CpuIcon,
    Storage as StorageIcon
} from "@mui/icons-material";
import {formatBytes} from "../../../lib/editor.ts";
import {type ReactNode} from "react";
import {type AggregateSnapshot} from "../../../hooks/docker-containers-stats.ts";
import Sparkline from "../../../components/sparkline.tsx";
import {statsTheme as t} from "./stats-theme.ts";

interface AggregateStatsProps {
    // computed once per completed refresh cycle — null until the first
    // cycle lands, so the header never renders half-updated totals
    aggregates: AggregateSnapshot | null;
}

// cumulative load stays default-colored while calm, then warns
const cpuValueColor = (cpu: number) =>
    cpu < 50 ? t.text : cpu < 85 ? '#ffb74d' : '#ef5350';

function AggregateStats({aggregates}: AggregateStatsProps) {
    const memPercent = aggregates && aggregates.memLimit > 0
        ? (aggregates.memUsed / aggregates.memLimit) * 100
        : 0;
    const cpu = aggregates?.cpu ?? 0;

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
                {/* compact: one line says it all */}
                <CompactTile
                    icon={<ContainerIcon/>}
                    label="Containers"
                    value={aggregates ? `${aggregates.running} running of ${aggregates.total}` : '–'}
                    grow={false}
                />

                {/* charted tiles: value block + a chart that takes the room */}
                <ChartTile
                    icon={<CpuIcon/>}
                    label="Total CPU"
                    value={aggregates ? `${aggregates.cpu.toFixed(1)}%` : '–'}
                    valueColor={cpuValueColor(cpu)}
                    sub=""
                    data={aggregates?.cpuHistory ?? []}
                    color={t.cpuLine}
                />

                <ChartTile
                    icon={<MemoryIcon/>}
                    label="Memory"
                    value={aggregates ? formatBytes(aggregates.memUsed) : '–'}
                    sub={aggregates && aggregates.memLimit > 0
                        ? `${memPercent.toFixed(1)}% of ${formatBytes(aggregates.memLimit)}`
                        : ''}
                    data={aggregates?.memHistory ?? []}
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
// remaining width — CPU and memory read at a glance, same size for both
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
                <Box sx={{flexShrink: 0}}>
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
