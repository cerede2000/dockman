import {
    Alert, Box, Button, Chip, CircularProgress, Dialog, DialogActions, DialogContent,
    DialogTitle, Divider, IconButton, Paper, Stack, Tab, Table, TableBody, TableCell,
    TableContainer, TableHead, TableRow, Tabs, Tooltip, Typography,
} from '@mui/material';
import {
    Close, Code, Delete, Dns, FavoriteBorder, InfoOutlined, LabelOutlined, Lan,
    Memory, Pause, PlayArrow, Refresh, RestartAlt, Security as SecurityIcon, Stop, Storage,
    Subject, Terminal, Tune, Update, Visibility, VisibilityOff,
} from '@mui/icons-material';
import {type ReactElement, type ReactNode, useCallback, useEffect, useMemo, useState} from 'react';
import {DockerService, type Network} from '../../gen/docker/v1/docker_pb.ts';
import {callRPC, useHostClient} from '../../lib/api.ts';
import {useSnackbar} from '../../hooks/snackbar.ts';
import LogsViewer from '../../components/log-viewer/logs-viewer.tsx';
import Sparkline from '../../components/sparkline.tsx';
import {formatBytes} from '../../lib/editor.ts';
import scrollbarStyles from '../../components/scrollbar-style.tsx';
import {statsTheme as t} from '../compose/components/stats-theme.ts';
import type {MonitorRow, RowAction} from './monitor-table.tsx';

type JsonObject = Record<string, unknown>;
type TabID = 'overview' | 'logs' | 'processes' | 'networks' | 'mounts' | 'environment'
    | 'labels' | 'security' | 'resources' | 'health' | 'inspect';

interface Props {
    open: boolean;
    row: MonitorRow | null;
    history?: {cpu: number[]; mem: number[]};
    busy?: RowAction;
    stackBusy: boolean;
    updateRun?: 'running' | 'failed' | 'done';
    onClose: () => void;
    onAction: (row: MonitorRow, action: RowAction) => void;
}

const tabs: {id: TabID, label: string, icon: ReactElement}[] = [
    {id: 'overview', label: 'Overview', icon: <InfoOutlined/>}, {id: 'logs', label: 'Logs', icon: <Subject/>},
    {id: 'processes', label: 'Processes', icon: <Terminal/>}, {id: 'networks', label: 'Networks', icon: <Lan/>},
    {id: 'mounts', label: 'Mounts', icon: <Storage/>}, {id: 'environment', label: 'Environment', icon: <Tune/>},
    {id: 'labels', label: 'Labels', icon: <LabelOutlined/>}, {id: 'security', label: 'Security', icon: <SecurityIcon/>},
    {id: 'resources', label: 'Resources', icon: <Memory/>}, {id: 'health', label: 'Health', icon: <FavoriteBorder/>},
    {id: 'inspect', label: 'Inspect JSON', icon: <Code/>},
];

const asObject = (value: unknown): JsonObject => value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as JsonObject : {};
const asArray = (value: unknown): unknown[] => Array.isArray(value) ? value : [];
const text = (value: unknown, fallback = '–'): string => {
    if (value === null || value === undefined || value === '') return fallback;
    if (typeof value === 'boolean') return value ? 'Yes' : 'No';
    if (typeof value === 'object') return JSON.stringify(value);
    return String(value);
};
const field = (object: unknown, key: string): unknown => asObject(object)[key];
const list = (value: unknown): string[] => asArray(value).map(v => text(v, ''));
const fmtDate = (value: unknown) => {
    const raw = text(value, '');
    if (!raw || raw.startsWith('0001-')) return '–';
    const date = new Date(raw);
    return Number.isNaN(date.getTime()) ? raw : date.toLocaleString();
};
const fmtLimit = (value: unknown) => {
    const n = Number(value ?? 0);
    return n > 0 ? formatBytes(n) : 'Unlimited / default';
};
const fmtDuration = (value: unknown) => {
    const ns = Number(value ?? 0);
    if (!Number.isFinite(ns) || ns <= 0) return 'Default';
    if (ns >= 1e9) return `${ns / 1e9}s`;
    if (ns >= 1e6) return `${ns / 1e6}ms`;
    if (ns >= 1e3) return `${ns / 1e3}µs`;
    return `${ns}ns`;
};

function Section({title, children}: {title: string, children: ReactNode}) {
    return <Paper variant="outlined" sx={{p: 1.35, borderColor: t.border, bgcolor: t.panel, borderRadius: 1.5}}>
        <Typography sx={{fontWeight: 800, mb: 0.85, fontSize: '0.82rem', letterSpacing: '0.02em'}}>{title}</Typography>{children}
    </Paper>;
}

function Details({rows}: {rows: [string, unknown][]}) {
    return <Box sx={{display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 285px), 1fr))', columnGap: 2, rowGap: 0.25}}>
        {rows.map(([label, value]) => <Box key={label} sx={{display: 'grid', gridTemplateColumns: 'minmax(96px, 0.42fr) minmax(0, 1fr)', gap: 1, minWidth: 0, py: 0.35}}>
            <Typography sx={{color: t.textDim, fontSize: '0.72rem'}}>{label}</Typography>
            <Typography sx={{fontFamily: t.mono, fontSize: '0.73rem', overflowWrap: 'anywhere', userSelect: 'text', cursor: 'text'}}>{text(value)}</Typography>
        </Box>)}
    </Box>;
}

function StringChips({values, empty = 'None'}: {values: string[], empty?: string}) {
    if (values.length === 0) return <Typography color="text.secondary">{empty}</Typography>;
    return <Stack direction="row" useFlexGap spacing={0.75} sx={{flexWrap: 'wrap'}}>
        {values.map((v, i) => <Chip key={`${v}-${i}`} label={v} size="small" sx={{fontFamily: t.mono}}/>)}
    </Stack>;
}

function KeyValueTable({value, maskSecrets = false}: {value: unknown, maskSecrets?: boolean}) {
    const [revealed, setRevealed] = useState(false);
    const entries = useMemo(() => {
        if (Array.isArray(value)) return value.map(v => {
            const raw = text(v, ''); const idx = raw.indexOf('=');
            return idx < 0 ? [raw, ''] : [raw.slice(0, idx), raw.slice(idx + 1)];
        });
        return Object.entries(asObject(value)).sort(([a], [b]) => a.localeCompare(b));
    }, [value]);
    const sensitive = (key: string) => /pass|token|secret|credential|private|api.?key|cookie|auth/i.test(key);
    return <Stack spacing={1}>
        {maskSecrets && <Box><Button size="small" startIcon={revealed ? <VisibilityOff/> : <Visibility/>}
            onClick={() => setRevealed(v => !v)}>{revealed ? 'Mask sensitive values' : 'Reveal sensitive values'}</Button></Box>}
        <TableContainer><Table size="small" sx={{'& .MuiTableCell-root': {py: 0.55, px: 1, fontSize: '0.73rem'}}}><TableHead><TableRow><TableCell>Name</TableCell><TableCell>Value</TableCell></TableRow></TableHead>
            <TableBody>{entries.map(([key, val]) => <TableRow key={String(key)} hover>
                <TableCell sx={{fontFamily: t.mono, width: '32%', overflowWrap: 'anywhere'}}>{String(key)}</TableCell>
                <TableCell sx={{fontFamily: t.mono, overflowWrap: 'anywhere'}}>
                    {maskSecrets && sensitive(String(key)) && !revealed ? '••••••••' : text(val, '')}
                </TableCell>
            </TableRow>)}</TableBody></Table></TableContainer>
        {entries.length === 0 && <Typography color="text.secondary">None</Typography>}
    </Stack>;
}

function MetricCard({label, value, sub, data, color}: {label: string, value: string, sub?: string, data?: number[], color: string}) {
    return <Box sx={{display: 'grid', gridTemplateColumns: data ? '92px minmax(80px, 1fr)' : '1fr', alignItems: 'center', gap: 1.25, minWidth: data ? 210 : 120, flex: data ? '1 1 240px' : '0 1 auto'}}>
        <Box><Typography sx={{color: t.textDim, fontSize: '0.68rem'}}>{label}</Typography>
            <Typography sx={{fontFamily: t.mono, fontWeight: 800, fontSize: '0.82rem'}}>{value}</Typography>
            {sub && <Typography sx={{color: t.textDim, fontSize: '0.64rem'}}>{sub}</Typography>}</Box>
        {data && <Sparkline data={data} color={color} height={34}/>}
    </Box>;
}

function Overview({row, inspect, history, processCount, onUpdate, updateRun}: {
    row: MonitorRow, inspect: JsonObject, history?: {cpu: number[]; mem: number[]}, processCount: number | null,
    onUpdate: () => void, updateRun?: 'running' | 'failed' | 'done',
}) {
    const state = asObject(inspect.State); const config = asObject(inspect.Config);
    const host = asObject(inspect.HostConfig); const network = asObject(inspect.NetworkSettings);
    const endpoints = asObject(network.Networks); const stats = row.stats;
    const restartPolicy = asObject(host.RestartPolicy);
    const labels = asObject(config.Labels);
    const depends = text(field(labels, 'com.docker.compose.depends_on'), '');
    const portBindings = asObject(host.PortBindings);
    const dns = new Set(row.info.IPAddress.filter(v => /[a-z]/i.test(v) && v.includes('.') && !v.includes(':')));
    for (const [key, value] of Object.entries(labels)) {
        if (!key.startsWith('traefik.http.routers.') || !key.endsWith('.rule')) continue;
        for (const match of text(value, '').matchAll(/Host(?:Regexp)?\((.*?)\)/g)) {
            for (const domain of match[1].matchAll(/[`"']([^`"',\s]+)[`"']+/g)) dns.add(domain[1]);
        }
    }
    return <Stack spacing={1.25}>
        <Box sx={{display: 'grid', gridTemplateColumns: {xs: '1fr', lg: '1fr 1fr'}, gap: 1.25}}>
            <Section title="Container">
                <Details rows={[
                    ['State', state.Status ?? row.info.state], ['Running / paused / restarting', `${text(state.Running)} / ${text(state.Paused)} / ${text(state.Restarting)}`],
                    ['Health', field(state.Health, 'Status') ?? row.info.health], ['OOM killed', state.OOMKilled], ['State error', state.Error],
                    ['Stack', row.info.stackName || 'Standalone'], ['Restart policy', `${text(restartPolicy.Name, 'no')} (${text(restartPolicy.MaximumRetryCount, '0')})`],
                    ['ID', inspect.Id ?? row.info.id], ['Created', fmtDate(inspect.Created)], ['Started', fmtDate(state.StartedAt)],
                    ['Platform', inspect.Platform], ['Exit code', state.ExitCode], ['Restart count', inspect.RestartCount ?? stats?.restartCount],
                ]}/>
            </Section>
            <Section title="Runtime">
                <Details rows={[
                    ['Image', config.Image ?? row.info.imageName], ['Command', list(config.Cmd).join(' ')],
                    ['Entrypoint', list(config.Entrypoint).join(' ')], ['Path / args', `${text(inspect.Path, '')} ${list(inspect.Args).join(' ')}`.trim()],
                    ['Depends on', depends], ['Working directory', config.WorkingDir], ['Processes', processCount],
                ]}/>
                <Stack direction="row" spacing={1} sx={{mt: 0.8, alignItems: 'center'}}>
                    <Chip size="small" color={row.info.updateAvailable ? 'warning' : 'success'}
                        label={row.info.updateAvailable ? `Update available: ${row.info.updateAvailable}` : 'Image up to date'}/>
                    <Button size="small" variant="outlined" startIcon={updateRun === 'running' ? <CircularProgress size={14}/> : <Update/>}
                        disabled={updateRun === 'running'} onClick={onUpdate}>Update</Button>
                </Stack>
            </Section>
        </Box>
        <Paper variant="outlined" sx={{px: 1.5, py: 1, borderColor: t.border, bgcolor: t.panel, borderRadius: 1.5}}>
            <Stack direction="row" useFlexGap spacing={1.5} divider={<Divider orientation="vertical" flexItem sx={{borderColor: t.border}}/>}
                sx={{alignItems: 'stretch', flexWrap: 'wrap'}}>
                <MetricCard label="CPU" value={stats ? `${stats.cpuUsage.toFixed(2)}%` : '–'} data={history?.cpu} color={t.cpuLine}/>
                <MetricCard label="RAM" value={stats ? formatBytes(Number(stats.memoryUsage)) : '–'}
                    sub={stats ? `/ ${formatBytes(Number(stats.memoryLimit))}` : undefined} data={history?.mem} color={t.memLine}/>
                <MetricCard label="Network I/O" value={stats ? `↓ ${formatBytes(Number(stats.networkRx))}` : '–'}
                    sub={stats ? `↑ ${formatBytes(Number(stats.networkTx))}` : undefined} color="#64b5f6"/>
                <MetricCard label="Disk I/O" value={stats ? `R ${formatBytes(Number(stats.blockRead))}` : '–'}
                    sub={stats ? `W ${formatBytes(Number(stats.blockWrite))}` : undefined} color="#ba68c8"/>
                <MetricCard label="Processes" value={processCount === null ? '–' : String(processCount)} color="#81c784"/>
            </Stack>
        </Paper>
        <Box sx={{display: 'grid', gridTemplateColumns: {xs: '1fr', lg: '1fr 1fr'}, gap: 1.25}}>
            <Section title="Addresses & endpoints">
                {dns.size > 0 && <Stack direction="row" useFlexGap spacing={0.65} sx={{flexWrap: 'wrap', mb: 0.8}}>
                    {[...dns].map(domain => <Chip key={domain} size="small" icon={<Dns/>} label={domain}
                        sx={{fontFamily: t.mono, userSelect: 'text'}}/>)}
                </Stack>}
                <Details rows={Object.entries(endpoints).flatMap(([name, raw]) => {
                    const ep = asObject(raw); return [
                        [`${name} IP`, ep.IPAddress], [`${name} endpoint`, ep.EndpointID], [`${name} gateway`, ep.Gateway],
                    ] as [string, unknown][];
                }).concat([["DNS endpoints", [...dns].join(', ')]])}/>
            </Section>
            <Section title="Ports"><KeyValueTable value={Object.keys(portBindings).length ? portBindings : network.Ports}/></Section>
        </Box>
    </Stack>;
}

function Processes({active, containerID, onCount}: {active: boolean, containerID: string, onCount: (n: number | null) => void}) {
    const client = useHostClient(DockerService); const [loading, setLoading] = useState(false);
    const [error, setError] = useState(''); const [titles, setTitles] = useState<string[]>([]); const [rows, setRows] = useState<string[][]>([]);
    const refresh = useCallback(async () => {
        setLoading(true); const {val, err} = await callRPC(() => client.containerTop({containerId: containerID}));
        setLoading(false); setError(err ?? '');
        const top = val?.top; const next = top?.proc.map(p => p.Processes) ?? [];
        setTitles(top?.Titles ?? []); setRows(next); onCount(err ? null : next.length);
    }, [client, containerID, onCount]);
    useEffect(() => { if (!active) return; void refresh(); const id = setInterval(refresh, 5000); return () => clearInterval(id); }, [active, refresh]);
    return <Section title="Running processes">
        <Box sx={{display: 'flex', justifyContent: 'space-between', mb: 1}}><Chip size="small" label={`${rows.length} active`}/>
            <Button size="small" startIcon={loading ? <CircularProgress size={14}/> : <Refresh/>} onClick={refresh} disabled={loading}>Refresh</Button></Box>
        {error && <Alert severity="error">{error}</Alert>}
        <TableContainer><Table size="small" stickyHeader sx={{'& .MuiTableCell-root': {py: 0.55, px: 1, fontSize: '0.72rem'}}}><TableHead><TableRow>{titles.map(v => <TableCell key={v}>{v}</TableCell>)}</TableRow></TableHead>
            <TableBody>{rows.map((r, i) => <TableRow key={i} hover>{r.map((v, j) => <TableCell key={j} sx={{fontFamily: t.mono, whiteSpace: 'nowrap'}}>{v}</TableCell>)}</TableRow>)}</TableBody>
        </Table></TableContainer>
    </Section>;
}

function Networks({containerID, inspect, onChanged}: {containerID: string, inspect: JsonObject, onChanged: () => void}) {
    const client = useHostClient(DockerService); const {showError, showSuccess} = useSnackbar();
    const [networks, setNetworks] = useState<Network[]>([]); const [busy, setBusy] = useState('');
    const [confirm, setConfirm] = useState<{network: Network, action: 'connect' | 'disconnect'} | null>(null);
    const load = useCallback(async () => { const {val, err} = await callRPC(() => client.networkList({})); if (err) showError(err); else setNetworks(val?.networks ?? []); }, [client, showError]);
    useEffect(() => { void load(); }, [load]);
    const settings = asObject(inspect.NetworkSettings); const mounted = asObject(settings.Networks); const host = asObject(inspect.HostConfig);
    const run = async () => {
        if (!confirm) return; const key = `${confirm.action}:${confirm.network.id}`; setBusy(key);
        const request = {networkId: confirm.network.id, containerId: containerID};
        const result = confirm.action === 'connect'
            ? await callRPC(() => client.networkConnectContainer(request))
            : await callRPC(() => client.networkDisconnectContainer(request));
        const {err} = result;
        setBusy(''); setConfirm(null);
        if (err) showError(`Network ${confirm.action} failed: ${err}`); else {showSuccess(`Network ${confirm.action}ed`); await load(); onChanged();}
    };
    return <Stack spacing={1.25}>
        <Section title="Network configuration"><Details rows={[
            ['Mode', host.NetworkMode], ['DNS', list(host.Dns).join(', ')], ['DNS search', list(host.DnsSearch).join(', ')],
            ['DNS options', list(host.DnsOptions).join(', ')], ['Sandbox ID', settings.SandboxID], ['Sandbox key', settings.SandboxKey],
        ]}/></Section>
        <Section title="Mounted networks"><Stack spacing={0.75}>
            {Object.entries(mounted).map(([name, raw]) => { const ep = asObject(raw); const network = networks.find(n => n.name === name);
                return <Paper key={name} variant="outlined" sx={{px: 1.1, py: 0.8}}><Stack direction={{xs: 'column', md: 'row'}} sx={{justifyContent: 'space-between', gap: 1}}>
                    <Box><Typography sx={{fontWeight: 800}}>{name}</Typography><Typography sx={{fontFamily: t.mono, fontSize: '0.75rem', color: t.textDim}}>
                        IP {text(ep.IPAddress)} · gateway {text(ep.Gateway)} · MAC {text(ep.MacAddress)}<br/>
                        endpoint {text(ep.EndpointID)} · aliases {list(ep.Aliases).join(', ') || '–'}</Typography></Box>
                    {network && <Button size="small" color="error" disabled={!!busy} onClick={() => setConfirm({network, action: 'disconnect'})}>Disconnect</Button>}
                </Stack></Paper>;
            })}{Object.keys(mounted).length === 0 && <Typography color="text.secondary">No mounted network</Typography>}
        </Stack></Section>
        <Section title="Available networks"><Stack direction="row" useFlexGap spacing={1} sx={{flexWrap: 'wrap'}}>
            {networks.filter(n => !(n.name in mounted)).map(n => <Button key={n.id} variant="outlined" size="small" disabled={!!busy || n.name === 'host' || n.name === 'none'}
                onClick={() => setConfirm({network: n, action: 'connect'})}>{n.name} · {n.driver}</Button>)}
        </Stack></Section>
        <Section title="Mapped & exposed ports"><KeyValueTable value={settings.Ports ?? host.PortBindings}/></Section>
        <Dialog open={confirm !== null} onClose={() => setConfirm(null)}><DialogTitle>{confirm?.action === 'connect' ? 'Connect network?' : 'Disconnect network?'}</DialogTitle>
            <DialogContent><Typography>{confirm?.network.name} · {confirm?.network.driver}</Typography></DialogContent>
            <DialogActions><Button onClick={() => setConfirm(null)}>Cancel</Button><Button variant="contained" color={confirm?.action === 'disconnect' ? 'error' : 'primary'} onClick={run}>Confirm</Button></DialogActions>
        </Dialog>
    </Stack>;
}

function Mounts({inspect}: {inspect: JsonObject}) {
    const mounts = asArray(inspect.Mounts).map(asObject);
    return <Section title="Bind mounts, volumes & tmpfs"><TableContainer><Table size="small" sx={{'& .MuiTableCell-root': {py: 0.55, px: 0.8, fontSize: '0.7rem'}}}><TableHead><TableRow>
        {['Type', 'Name', 'Source', 'Destination', 'Driver', 'Mode', 'RW', 'Propagation'].map(v => <TableCell key={v}>{v}</TableCell>)}
    </TableRow></TableHead><TableBody>{mounts.map((m, i) => <TableRow key={i} hover>
        {[m.Type, m.Name, m.Source, m.Destination, m.Driver, m.Mode, m.RW, m.Propagation].map((v, j) => <TableCell key={j} sx={{fontFamily: t.mono, overflowWrap: 'anywhere'}}>{text(v)}</TableCell>)}
    </TableRow>)}</TableBody></Table></TableContainer>{mounts.length === 0 && <Typography color="text.secondary">None</Typography>}</Section>;
}

function Security({inspect}: {inspect: JsonObject}) {
    const config = asObject(inspect.Config); const host = asObject(inspect.HostConfig);
    return <Stack spacing={1.25}><Section title="Isolation"><Details rows={[
        ['Privileged', host.Privileged], ['Read-only root filesystem', host.ReadonlyRootfs], ['User', config.User || 'root (default)'],
        ['PID namespace', host.PidMode], ['IPC namespace', host.IpcMode], ['UTS namespace', host.UTSMode],
        ['User namespace', host.UsernsMode], ['Cgroup namespace', host.CgroupnsMode], ['Runtime', host.Runtime],
        ['AppArmor profile', inspect.AppArmorProfile], ['Process label', inspect.ProcessLabel], ['Mount label', inspect.MountLabel],
        ['No-new-privileges', list(host.SecurityOpt).some(v => v.includes('no-new-privileges'))],
    ]}/></Section>
        <Section title="Security options"><StringChips values={list(host.SecurityOpt)}/></Section>
        <Box sx={{display: 'grid', gridTemplateColumns: {xs: '1fr', md: '1fr 1fr'}, gap: 1.25}}>
            <Section title="Capabilities added"><StringChips values={list(host.CapAdd)}/></Section>
            <Section title="Capabilities dropped"><StringChips values={list(host.CapDrop)}/></Section>
        </Box>
        <Section title="Filesystem confinement"><Details rows={[
            ['Masked paths', list(host.MaskedPaths).join(', ')], ['Read-only paths', list(host.ReadonlyPaths).join(', ')],
            ['Devices', host.Devices], ['Device cgroup rules', list(host.DeviceCgroupRules).join(', ')], ['Sysctls', host.Sysctls],
        ]}/></Section>
    </Stack>;
}

function Resources({inspect}: {inspect: JsonObject}) {
    const h = asObject(inspect.HostConfig); const nano = Number(h.NanoCpus ?? 0);
    return <Box sx={{display: 'grid', gridTemplateColumns: {xs: '1fr', xl: 'repeat(3, minmax(0, 1fr))'}, gap: 1.25}}><Section title="CPU"><Details rows={[
        ['Nano CPUs', nano > 0 ? `${nano / 1e9} CPU` : 'Unlimited / default'], ['CPU shares', h.CpuShares],
        ['CPU quota / period', `${text(h.CpuQuota)} / ${text(h.CpuPeriod)}`], ['CPU set', h.CpusetCpus], ['NUMA memory nodes', h.CpusetMems],
    ]}/></Section><Section title="Memory & OOM"><Details rows={[
        ['Memory limit', fmtLimit(h.Memory)], ['Reservation', fmtLimit(h.MemoryReservation)], ['Memory + swap', fmtLimit(h.MemorySwap)],
        ['Swappiness', h.MemorySwappiness], ['OOM killer disabled', h.OomKillDisable], ['OOM score adjustment', h.OomScoreAdj],
        ['PIDs limit', Number(h.PidsLimit ?? 0) > 0 ? h.PidsLimit : 'Unlimited / default'],
    ]}/></Section><Section title="Cgroups & I/O"><Details rows={[
        ['Cgroup parent', h.CgroupParent], ['Cgroup', h.Cgroup], ['Cgroup namespace', h.CgroupnsMode],
        ['Block I/O weight', h.BlkioWeight], ['Shared memory', fmtLimit(h.ShmSize)], ['Ulimits', h.Ulimits],
    ]}/></Section></Box>;
}

function Health({inspect}: {inspect: JsonObject}) {
    const config = asObject(inspect.Config); const healthConfig = asObject(config.Healthcheck);
    const health = asObject(field(inspect.State, 'Health')); const logs = asArray(health.Log).map(asObject);
    return <Stack spacing={1.25}><Section title="Healthcheck configuration"><Details rows={[
        ['Command', list(healthConfig.Test).join(' ')], ['Interval', fmtDuration(healthConfig.Interval)],
        ['Timeout', fmtDuration(healthConfig.Timeout)], ['Start period', fmtDuration(healthConfig.StartPeriod)],
        ['Start interval', fmtDuration(healthConfig.StartInterval)], ['Retries', healthConfig.Retries],
        ['Status', health.Status], ['Failing streak', health.FailingStreak],
    ]}/></Section><Section title="Healthcheck log"><Stack spacing={1}>{logs.map((log, i) => <Paper key={i} variant="outlined" sx={{p: 1.25}}>
        <Details rows={[[`Run ${i + 1}`, `${fmtDate(log.Start)} → ${fmtDate(log.End)}`], ['Exit code', log.ExitCode], ['Output', log.Output]]}/>
    </Paper>)}{logs.length === 0 && <Typography color="text.secondary">No healthcheck log</Typography>}</Stack></Section></Stack>;
}

export default function ContainerDetailsDialog({open, row, history, busy, stackBusy, updateRun, onClose, onAction}: Props) {
    const client = useHostClient(DockerService); const [tab, setTab] = useState<TabID>('overview');
    const [raw, setRaw] = useState(''); const [inspect, setInspect] = useState<JsonObject>({});
    const [loading, setLoading] = useState(false); const [error, setError] = useState(''); const [processCount, setProcessCount] = useState<number | null>(null);
    const [removeConfirm, setRemoveConfirm] = useState(false);
    const containerID = row?.info.id ?? '';
    const containerState = row?.info.state ?? '';
    const load = useCallback(async () => {
        if (!containerID) return; setLoading(true); const {val, err} = await callRPC(() => client.containerInspect({containerID})); setLoading(false);
        if (err || !val) {setError(err || 'Empty inspect response'); return;} setError(''); setRaw(val.rawJson);
        try {setInspect(asObject(JSON.parse(val.rawJson)));} catch {setError('The daemon returned invalid inspect JSON');}
        if (['running', 'restarting', 'paused'].includes(containerState)) {
            const top = await callRPC(() => client.containerTop({containerId: containerID}));
            setProcessCount(top.err ? null : top.val?.top?.proc.length ?? 0);
        } else {
            setProcessCount(0);
        }
    }, [client, containerID, containerState]);
    useEffect(() => { if (open) {setTab('overview'); setProcessCount(null); void load();} }, [open, load]);
    useEffect(() => { if (open && !row) onClose(); }, [open, row, onClose]);
    if (!row) return null;
    const state = row.info.state; const active = ['running', 'restarting', 'paused'].includes(state); const paused = state === 'paused';
    const processAvailable = state === 'running' || state === 'paused';
    const locked = !!busy || stackBusy;
    return <Dialog open={open} onClose={onClose} maxWidth={false} fullWidth
        slotProps={{
            backdrop: {sx: {bgcolor: 'rgba(0,0,0,0.68)', backdropFilter: 'blur(4px)'}},
            paper: {sx: {
                width: 'min(88vw, 1320px)', height: 'min(86vh, 860px)', maxWidth: 'none', maxHeight: 'none', m: 2,
                bgcolor: '#17191c', border: `1px solid ${t.border}`, borderRadius: 2.5, overflow: 'hidden',
                boxShadow: '0 28px 90px rgba(0,0,0,0.65)', userSelect: 'text', WebkitUserSelect: 'text',
                '& .MuiTypography-root, & .MuiTableCell-root, & .MuiChip-label, & pre': {userSelect: 'text', WebkitUserSelect: 'text'},
            }},
        }}>
        <Box sx={{display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0}}>
        <Box sx={{flexShrink: 0, bgcolor: t.header, borderBottom: `1px solid ${t.border}`}}>
            <Stack direction="row" sx={{px: 1.75, py: 1, alignItems: 'center', justifyContent: 'space-between', gap: 1.5}}>
                <Box sx={{minWidth: 0}}><Typography variant="h6" noWrap sx={{fontWeight: 900}}>{row.info.name}</Typography>
                    <Typography noWrap sx={{fontFamily: t.mono, color: t.textDim, fontSize: '0.68rem', userSelect: 'text', cursor: 'text'}}>{row.info.id}</Typography></Box>
                <Stack direction="row" spacing={0.5} sx={{alignItems: 'center'}}>
                    <Tooltip title={active ? 'Stop' : 'Start'}><span><IconButton disabled={locked} onClick={() => onAction(row, active ? 'stop' : 'start')}>
                        {busy === 'start' || busy === 'stop' ? <CircularProgress size={18}/> : active ? <Stop/> : <PlayArrow/>}</IconButton></span></Tooltip>
                    <Tooltip title="Restart"><span><IconButton disabled={locked} onClick={() => onAction(row, 'restart')}>{busy === 'restart' ? <CircularProgress size={18}/> : <RestartAlt/>}</IconButton></span></Tooltip>
                    <Tooltip title={paused ? 'Unpause' : 'Pause'}><span><IconButton disabled={locked || (!active && !paused)} onClick={() => onAction(row, paused ? 'unpause' : 'pause')}><Pause/></IconButton></span></Tooltip>
                    <Tooltip title="Update"><span><IconButton disabled={locked || updateRun === 'running'} onClick={() => onAction(row, 'update')}><Update/></IconButton></span></Tooltip>
                    <Tooltip title="Remove"><span><IconButton color="error" disabled={locked} onClick={() => setRemoveConfirm(true)}><Delete/></IconButton></span></Tooltip>
                    <Tooltip title="Refresh details"><span><IconButton disabled={loading} onClick={load}>{loading ? <CircularProgress size={18}/> : <Refresh/>}</IconButton></span></Tooltip>
                    <Button size="small" variant={tab === 'inspect' ? 'contained' : 'outlined'} startIcon={<InfoOutlined/>}
                        onClick={() => setTab('inspect')} sx={{display: {xs: 'none', md: 'inline-flex'}}}>Inspect</Button>
                    <Divider orientation="vertical" flexItem/><Tooltip title="Close"><IconButton onClick={onClose}><Close/></IconButton></Tooltip>
                </Stack>
            </Stack>
            {stackBusy && <Alert severity="info" sx={{borderRadius: 0, py: 0}}>Container actions are locked while its stack action is running.</Alert>}
        </Box>
        <Box sx={{display: 'flex', flex: 1, minHeight: 0}}>
            <Box component="nav" aria-label="Container details" sx={{width: 184, flexShrink: 0, overflowY: 'auto', borderRight: `1px solid ${t.border}`, bgcolor: '#1b1d20', ...scrollbarStyles}}>
                <Tabs orientation="vertical" value={tab} onChange={(_, value: TabID) => setTab(value)}
                    variant="scrollable" sx={{py: 0.8, minHeight: '100%', '& .MuiTabs-indicator': {left: 0, right: 'auto', width: 3}}}>
                    {tabs.map(v => <Tab key={v.id} value={v.id} label={v.label} icon={v.icon} iconPosition="start"
                        sx={{minHeight: 42, justifyContent: 'flex-start', alignItems: 'center', textTransform: 'none', fontWeight: 650,
                            fontSize: '0.78rem', px: 1.5, gap: 1, '& svg': {fontSize: 18}}}/>) }
                </Tabs>
            </Box>
            <Box sx={{flex: 1, minWidth: 0, minHeight: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden'}}>
                {error && <Alert severity="error" sx={{m: 1, mb: 0, flexShrink: 0}}>{error}</Alert>}
                <Box sx={{flex: 1, minHeight: 0, overflow: tab === 'logs' ? 'hidden' : 'auto', p: tab === 'logs' ? 0 : 1.4,
                    userSelect: 'text', cursor: tab === 'logs' ? 'default' : 'text', ...scrollbarStyles}}>
                {loading && !raw ? <Box sx={{display: 'grid', placeItems: 'center', height: '100%'}}><CircularProgress/></Box> : <>
                    <Box hidden={tab !== 'overview'}><Overview row={row} inspect={inspect} history={history} processCount={processCount} onUpdate={() => onAction(row, 'update')} updateRun={updateRun}/></Box>
                    <Box hidden={tab !== 'logs'} sx={{height: '100%', minHeight: 0, overflow: 'hidden'}}><LogsViewer containers={[{id: row.info.id, name: row.info.name}]} isActive={tab === 'logs'}/></Box>
                    <Box hidden={tab !== 'processes'}>{processAvailable
                        ? <Processes active={tab === 'processes'} containerID={row.info.id} onCount={setProcessCount}/>
                        : <Alert severity="info">Start the container to inspect its running processes.</Alert>}</Box>
                    <Box hidden={tab !== 'networks'}><Networks containerID={row.info.id} inspect={inspect} onChanged={load}/></Box>
                    <Box hidden={tab !== 'mounts'}><Mounts inspect={inspect}/></Box>
                    <Box hidden={tab !== 'environment'}><Section title="Environment variables"><KeyValueTable value={field(inspect.Config, 'Env')} maskSecrets/></Section></Box>
                    <Box hidden={tab !== 'labels'}><Section title="Labels"><KeyValueTable value={field(inspect.Config, 'Labels')}/></Section></Box>
                    <Box hidden={tab !== 'security'}><Security inspect={inspect}/></Box>
                    <Box hidden={tab !== 'resources'}><Resources inspect={inspect}/></Box>
                    <Box hidden={tab !== 'health'}><Health inspect={inspect}/></Box>
                    <Box hidden={tab !== 'inspect'} sx={{height: '100%'}}><Paper variant="outlined" component="pre" sx={{m: 0, p: 1.5, height: '100%', boxSizing: 'border-box', overflow: 'auto', fontFamily: t.mono, fontSize: '0.72rem', whiteSpace: 'pre', bgcolor: '#09090b', userSelect: 'text', cursor: 'text', ...scrollbarStyles}}>{raw}</Paper></Box>
                </>}
                </Box>
            </Box>
        </Box>
        </Box>
        <Dialog open={removeConfirm} onClose={() => setRemoveConfirm(false)}>
            <DialogTitle>Remove container?</DialogTitle>
            <DialogContent><Typography>This permanently removes <b>{row.info.name}</b>.</Typography></DialogContent>
            <DialogActions><Button onClick={() => setRemoveConfirm(false)}>Cancel</Button>
                <Button variant="contained" color="error" onClick={() => {setRemoveConfirm(false); onAction(row, 'remove');}}>Remove</Button>
            </DialogActions>
        </Dialog>
    </Dialog>;
}
