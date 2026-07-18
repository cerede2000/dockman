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

function AggregateStats({aggregates}: AggregateStatsProps) {
    const memPercent = aggregates && aggregates.memLimit > 0
        ? (aggregates.memUsed / aggregates.memLimit) * 100
        : 0;

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
                <StatTile
                    icon={<ContainerIcon/>}
                    label="Containers"
                    value={aggregates ? aggregates.total.toString() : '–'}
                    sub={aggregates ? `${aggregates.running} running` : ''}
                />

                <StatTile
                    icon={<CpuIcon/>}
                    label="Total CPU"
                    value={aggregates ? `${aggregates.cpu.toFixed(1)}%` : '–'}
                    spark={{data: aggregates?.cpuHistory ?? [], color: t.cpuLine}}
                />

                <StatTile
                    icon={<MemoryIcon/>}
                    label="Memory"
                    value={aggregates ? formatBytes(aggregates.memUsed) : '–'}
                    sub={aggregates && aggregates.memLimit > 0
                        ? `${memPercent.toFixed(1)}% of ${formatBytes(aggregates.memLimit)}`
                        : ''}
                    spark={{data: aggregates?.memHistory ?? [], color: t.memLine}}
                />

                <StatTile
                    icon={<NetworkIcon/>}
                    label="Network I/O"
                    value={aggregates ? `↓ ${formatBytes(aggregates.netRx)}` : '–'}
                    sub={aggregates ? `↑ ${formatBytes(aggregates.netTx)}` : ''}
                    tooltip={aggregates ? `Total ${formatBytes(aggregates.netRx + aggregates.netTx)}` : ''}
                />

                <StatTile
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

// one quiet two-line tile: dim small-caps label, then a single row carrying
// the mono value, its dim inline detail and (when present) a small sparkline
// pinned to the right — the whole band stays ~2 text lines tall
function StatTile({icon, label, value, sub, spark, tooltip}: {
    icon: ReactNode,
    label: string,
    value: string,
    sub?: string,
    spark?: { data: number[], color: string },
    tooltip?: string,
}) {
    const body = (
        <Box sx={{flex: '1 1 0', minWidth: 0}}>
            <Stack direction="row" spacing={0.75} alignItems="center" sx={{mb: 0.25, color: t.textDim}}>
                <Box sx={{display: 'flex', '& svg': {fontSize: 14}}}>{icon}</Box>
                <Typography variant="overline" noWrap
                            sx={{fontWeight: 700, lineHeight: 1, letterSpacing: '0.08em', fontSize: '0.62rem'}}>
                    {label}
                </Typography>
            </Stack>
            <Stack direction="row" spacing={1} alignItems="center" sx={{minWidth: 0}}>
                <Typography noWrap sx={{
                    fontFamily: t.mono,
                    fontWeight: 700,
                    fontSize: '1rem',
                    lineHeight: 1.3,
                    color: t.text,
                    flexShrink: 0,
                }}>
                    {value}
                </Typography>
                {sub && (
                    <Typography variant="caption" noWrap sx={{
                        color: t.textDim,
                        fontFamily: t.mono,
                        minWidth: 0,
                    }}>
                        {sub}
                    </Typography>
                )}
                {spark && (
                    <>
                        <Box sx={{flexGrow: 1}}/>
                        <Box sx={{width: 90, flexShrink: 0}}>
                            <Sparkline data={spark.data} color={spark.color} height={20}/>
                        </Box>
                    </>
                )}
            </Stack>
        </Box>
    );

    return tooltip ? <Tooltip title={tooltip} arrow placement="top">{body}</Tooltip> : body;
}
