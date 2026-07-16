import {Box, Divider, Paper, Stack, Typography} from "@mui/material";
import {
    Dns as ContainerIcon,
    ImportExport as NetworkIcon,
    Memory as MemoryIcon,
    Speed as CpuIcon,
    Storage as StorageIcon
} from "@mui/icons-material";
import {type ContainerStats} from "../../../gen/docker/v1/docker_pb";
import {formatBytes, getUsageColor} from "../../../lib/editor.ts";
import {type ReactNode, useEffect, useRef} from "react";
import Sparkline from "../../../components/sparkline.tsx";
import {statsTheme as t} from "./stats-theme.ts";

interface AggregateStatsProps {
    containers: ContainerStats[];
    loading?: boolean;
}

const HISTORY_CAP = 40;

function AggregateStats({containers}: AggregateStatsProps) {
    const totals = containers.reduce((acc, curr) => {
        acc.cpu += curr.cpuUsage;
        acc.memUsed += Number(curr.memoryUsage);
        acc.memLimit += Number(curr.memoryLimit);
        acc.netRx += Number(curr.networkRx);
        acc.netTx += Number(curr.networkTx);
        acc.diskR += Number(curr.blockRead);
        acc.diskW += Number(curr.blockWrite);
        if (curr.state === 'running') acc.running++;
        return acc;
    }, {
        cpu: 0, memUsed: 0, memLimit: 0,
        netRx: 0, netTx: 0, diskR: 0, diskW: 0,
        running: 0,
    });

    const memPercent = totals.memLimit > 0 ? (totals.memUsed / totals.memLimit) * 100 : 0;

    // rolling host-level history for the aggregate charts, appended once per
    // poll tick (the containers array identity changes on every poll)
    const cpuHist = useRef<number[]>([]);
    const memHist = useRef<number[]>([]);
    useEffect(() => {
        if (containers.length === 0) return;
        cpuHist.current.push(totals.cpu);
        memHist.current.push(memPercent);
        if (cpuHist.current.length > HISTORY_CAP) cpuHist.current.shift();
        if (memHist.current.length > HISTORY_CAP) memHist.current.shift();
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [containers]);

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
                    value={containers.length.toString()}
                    subValue={`${totals.running} running`}
                />

                {/* Total CPU with history */}
                <ChartItem
                    icon={<CpuIcon sx={{color: getUsageColor(totals.cpu / 10)}}/>}
                    label="Total CPU"
                    value={`${totals.cpu.toFixed(1)}%`}
                    data={cpuHist.current}
                    color={t.cpuLine}
                />

                {/* Aggregate memory with history */}
                <ChartItem
                    icon={<MemoryIcon sx={{color: getUsageColor(memPercent)}}/>}
                    label="Memory"
                    value={formatBytes(totals.memUsed)}
                    subValue={`${memPercent.toFixed(1)}% of limits`}
                    data={memHist.current}
                    color={t.memLine}
                    max={100}
                />

                {/* Network totals */}
                <StatItem
                    icon={<NetworkIcon sx={{color: t.netUp}}/>}
                    label="Network I/O"
                    value={formatBytes(totals.netRx + totals.netTx)}
                    subValue={`↓ ${formatBytes(totals.netRx)}  ↑ ${formatBytes(totals.netTx)}`}
                />

                {/* Disk totals */}
                <StatItem
                    icon={<StorageIcon sx={{color: t.diskWrite}}/>}
                    label="Block I/O"
                    value={formatBytes(totals.diskR + totals.diskW)}
                    subValue={`r ${formatBytes(totals.diskR)}  w ${formatBytes(totals.diskW)}`}
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

function ChartItem({icon, label, value, subValue, data, color, max}: {
    icon: ReactNode,
    label: string,
    value: string,
    subValue?: string,
    data: number[],
    color: string,
    max?: number,
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
                <Sparkline data={data} color={color} width={120} height={34} max={max}/>
            </Stack>
        </Box>
    );
}
