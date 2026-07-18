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
                px: 2.5,
                py: 1.5,
                mb: 2,
                borderRadius: 2,
                bgcolor: t.panel,
                borderColor: t.border,
                display: 'flex',
                alignItems: 'center'
            }}
        >
            <Stack
                direction="row"
                spacing={3}
                divider={<Divider orientation="vertical" flexItem sx={{borderColor: t.border}}/>}
                sx={{width: '100%', overflowX: 'auto', alignItems: 'stretch'}}
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

// one quiet tile: dim small-caps label with a small dim icon, a single
// non-wrapping mono value line, an optional dim detail line, and an optional
// sparkline sitting to the right of the numbers
function StatTile({icon, label, value, sub, spark, tooltip}: {
    icon: ReactNode,
    label: string,
    value: string,
    sub?: string,
    spark?: { data: number[], color: string },
    tooltip?: string,
}) {
    const body = (
        <Box sx={{minWidth: 150, flex: '1 0 auto'}}>
            <Stack direction="row" spacing={0.75} alignItems="center" sx={{mb: 0.5, color: t.textDim}}>
                <Box sx={{display: 'flex', '& svg': {fontSize: 15}}}>{icon}</Box>
                <Typography variant="overline" sx={{fontWeight: 700, lineHeight: 1, letterSpacing: '0.08em'}}>
                    {label}
                </Typography>
            </Stack>
            <Stack direction="row" spacing={1.5} alignItems="center">
                <Box sx={{minWidth: 0}}>
                    <Typography sx={{
                        fontFamily: t.mono,
                        fontWeight: 700,
                        fontSize: '1.05rem',
                        lineHeight: 1.25,
                        color: t.text,
                        whiteSpace: 'nowrap',
                    }}>
                        {value}
                    </Typography>
                    <Typography variant="caption" sx={{
                        whiteSpace: 'nowrap',
                        color: t.textDim,
                        fontFamily: t.mono,
                        display: 'block',
                        minHeight: '1.1em',
                    }}>
                        {sub ?? ''}
                    </Typography>
                </Box>
                {spark && (
                    <Box sx={{width: 120, flexShrink: 0}}>
                        <Sparkline data={spark.data} color={spark.color} height={30}/>
                    </Box>
                )}
            </Stack>
        </Box>
    );

    return tooltip ? <Tooltip title={tooltip} arrow placement="top">{body}</Tooltip> : body;
}
