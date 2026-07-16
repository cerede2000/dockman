import {Box, Divider, Paper, Stack, Typography} from "@mui/material";
import {
    Dns as ContainerIcon,
    ImportExport as NetworkIcon,
    Memory as MemoryIcon,
    Speed as CpuIcon,
    Storage as StorageIcon
} from "@mui/icons-material";
import {formatBytes, getUsageColor} from "../../../lib/editor.ts";
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
                p: 2,
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
                spacing={4}
                divider={<Divider orientation="vertical" flexItem sx={{borderColor: t.border}}/>}
                sx={{width: '100%', overflowX: 'auto'}}
            >
                {/* Container count */}
                <StatItem
                    icon={<ContainerIcon sx={{color: t.cpuLine}}/>}
                    label="Containers"
                    value={aggregates ? aggregates.total.toString() : '–'}
                    subValue={aggregates ? `${aggregates.running} running` : ''}
                />

                {/* Total CPU with per-cycle history */}
                <ChartItem
                    icon={<CpuIcon sx={{color: getUsageColor((aggregates?.cpu ?? 0) / 10)}}/>}
                    label="Total CPU"
                    value={aggregates ? `${aggregates.cpu.toFixed(1)}%` : '–'}
                    data={aggregates?.cpuHistory ?? []}
                    color={t.cpuLine}
                />

                {/* Aggregate memory with per-cycle history */}
                <ChartItem
                    icon={<MemoryIcon sx={{color: getUsageColor(memPercent)}}/>}
                    label="Memory"
                    value={aggregates ? formatBytes(aggregates.memUsed) : '–'}
                    subValue={aggregates ? `${memPercent.toFixed(1)}% of limits` : ''}
                    data={aggregates?.memHistory ?? []}
                    color={t.memLine}
                />

                {/* Network totals */}
                <StatItem
                    icon={<NetworkIcon sx={{color: t.netUp}}/>}
                    label="Network I/O"
                    value={aggregates ? formatBytes(aggregates.netRx + aggregates.netTx) : '–'}
                    subValue={aggregates ? `↓ ${formatBytes(aggregates.netRx)}  ↑ ${formatBytes(aggregates.netTx)}` : ''}
                />

                {/* Disk totals */}
                <StatItem
                    icon={<StorageIcon sx={{color: t.diskWrite}}/>}
                    label="Block I/O"
                    value={aggregates ? formatBytes(aggregates.diskR + aggregates.diskW) : '–'}
                    subValue={aggregates ? `r ${formatBytes(aggregates.diskR)}  w ${formatBytes(aggregates.diskW)}` : ''}
                />
            </Stack>
        </Paper>
    );
}

export default AggregateStats;

function StatItem({icon, label, value, subValue}: {
    icon: ReactNode,
    label: string,
    value: string,
    subValue: string
}) {
    return (
        <Box sx={{minWidth: 140}}>
            <Stack direction="row" spacing={1} alignItems="center" sx={{mb: 0.5}}>
                {icon}
                <Typography variant="overline" sx={{fontWeight: 700, color: t.textDim, lineHeight: 1}}>
                    {label}
                </Typography>
            </Stack>
            <Typography variant="h6" sx={{fontFamily: t.mono, fontWeight: 800, lineHeight: 1.2, color: t.text}}>
                {value}
            </Typography>
            <Typography variant="caption" sx={{whiteSpace: 'nowrap', color: t.textDim, fontFamily: t.mono}}>
                {subValue}
            </Typography>
        </Box>
    );
}

function ChartItem({icon, label, value, subValue, data, color}: {
    icon: ReactNode,
    label: string,
    value: string,
    subValue?: string,
    data: number[],
    color: string,
}) {
    return (
        <Box sx={{minWidth: 200}}>
            <Stack direction="row" spacing={1} alignItems="center" sx={{mb: 0.5}}>
                {icon}
                <Typography variant="overline" sx={{fontWeight: 700, color: t.textDim, lineHeight: 1}}>
                    {label}
                </Typography>
            </Stack>
            <Stack direction="row" spacing={1.5} alignItems="flex-end">
                <Box>
                    <Typography variant="h6"
                                sx={{fontFamily: t.mono, fontWeight: 800, lineHeight: 1.2, color: t.text}}>
                        {value}
                    </Typography>
                    {subValue && (
                        <Typography variant="caption"
                                    sx={{whiteSpace: 'nowrap', color: t.textDim, fontFamily: t.mono}}>
                            {subValue}
                        </Typography>
                    )}
                </Box>
                <Box sx={{width: 140}}>
                    <Sparkline data={data} color={color} height={34}/>
                </Box>
            </Stack>
        </Box>
    );
}
