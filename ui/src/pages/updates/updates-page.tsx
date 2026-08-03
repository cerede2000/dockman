import {
    Alert,
    Box,
    Button,
    Chip,
    Dialog,
    DialogActions,
    DialogContent,
    DialogTitle,
    FormControl,
    FormControlLabel,
    InputLabel,
    MenuItem,
    Paper,
    Select,
    Stack,
    Switch,
    Table,
    TableBody,
    TableCell,
    TableContainer,
    TableHead,
    TableRow,
    TextField,
    Tooltip,
    Typography,
} from '@mui/material';
import {
    AccountTreeOutlined,
    CheckCircleOutlined,
    EditOutlined,
    HistoryOutlined,
    Refresh,
    ShieldOutlined,
    SpaceDashboardOutlined,
    SystemUpdateAlt,
} from '@mui/icons-material';
import {useCallback, useEffect, useMemo, useState} from 'react';
import {useNavigate} from 'react-router';
import PageHeader from '../../components/page-header.tsx';
import {useHostUrl} from '../../lib/api.ts';
import {useSnackbar} from '../../hooks/snackbar.ts';
import {useHostFromUrl} from '../home/home.tsx';

type TargetType = 'container' | 'stack';

type UpdateEnrollment = {
    containerId: string;
    containerName: string;
    image: string;
    state: string;
    stackName?: string;
    stackKey?: string;
    enrolled: boolean;
    source: 'none' | 'interface' | 'label' | 'disabled-label' | 'protected';
    reason?: string;
    schedule?: string;
	scheduleError?: string;
    rollback: boolean;
    policyTarget?: TargetType;
    policyTargetId?: string;
};

type PolicyDraft = {
    targetType: TargetType;
    enabled: boolean;
    schedule: string;
    rollbackEnabled: boolean;
};

type ScanResult = {
    containerId: string;
	image: string;
    status: 'available' | 'current' | 'skipped' | 'error';
    reason?: string;
    checkedAt: string;
};

type ScanRun = {
    id: number;
    startedAt: string;
    trigger: string;
    schedule?: string;
    targets: number;
    available: number;
    current: number;
    skipped: number;
    errors: number;
    error?: string;
};

type ScheduledScan = {schedule: string; nextRun: string; targets: number};

const sourceLabels: Record<UpdateEnrollment['source'], string> = {
    none: 'Not enrolled',
    interface: 'Dockman policy',
    label: 'Compose label',
    'disabled-label': 'Disabled by label',
    protected: 'Protected',
};

function targetFor(row: UpdateEnrollment, type: TargetType): {key: string; name: string} {
    if (type === 'stack' && row.stackKey && row.stackName) return {key: row.stackKey, name: row.stackName};
    return {key: row.containerName, name: row.containerName};
}

export default function UpdatesPage() {
    const host = useHostFromUrl();
    const hostUrl = useHostUrl();
    const navigate = useNavigate();
	const {showError, showSuccess, showWarning} = useSnackbar();
    const [rows, setRows] = useState<UpdateEnrollment[]>([]);
    const [loading, setLoading] = useState(true);
    const [search, setSearch] = useState('');
    const [editing, setEditing] = useState<UpdateEnrollment | null>(null);
    const [saving, setSaving] = useState(false);
	const [scanning, setScanning] = useState(false);
	const [scanResults, setScanResults] = useState<Record<string, ScanResult>>({});
	const [scanRuns, setScanRuns] = useState<ScanRun[]>([]);
	const [schedules, setSchedules] = useState<ScheduledScan[]>([]);
    const [draft, setDraft] = useState<PolicyDraft>({
        targetType: 'container', enabled: true, schedule: '', rollbackEnabled: true,
    });

    const load = useCallback(async () => {
        setLoading(true);
        try {
            const response = await fetch(hostUrl('/docker/updates/inventory'));
            if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
            const payload = await response.json() as {results: UpdateEnrollment[]};
            setRows(payload.results ?? []);
			const stateResponse = await fetch(hostUrl('/docker/updates/state'));
			if (!stateResponse.ok) throw new Error((await stateResponse.text()).trim() || `HTTP ${stateResponse.status}`);
			const state = await stateResponse.json() as {results: ScanResult[]; runs: ScanRun[]; schedules: ScheduledScan[]};
			setScanResults(Object.fromEntries((state.results ?? []).map(result => [result.containerId, result])));
			setScanRuns(state.runs ?? []);
			setSchedules(state.schedules ?? []);
        } catch (error) {
            showError(`Unable to load update policies — ${error instanceof Error ? error.message : String(error)}`);
        } finally {
            setLoading(false);
        }
    }, [hostUrl, showError]);

    useEffect(() => { void load(); }, [load]);

    const visibleRows = useMemo(() => {
        const query = search.trim().toLowerCase();
        if (!query) return rows;
        return rows.filter(row => `${row.containerName} ${row.image} ${row.stackName ?? ''}`.toLowerCase().includes(query));
    }, [rows, search]);

    const enrolledCount = rows.filter(row => row.enrolled).length;
    const labelCount = rows.filter(row => row.source === 'label' || row.source === 'disabled-label').length;
	const availableCount = rows.filter(row => scanResults[row.containerId]?.status === 'available').length;

	const scanNow = async () => {
		setScanning(true);
		try {
			const response = await fetch(hostUrl('/docker/updates/scan'), {method: 'POST'});
			if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
			const payload = await response.json() as {run: ScanRun};
			await load();
			if (payload.run.errors > 0) {
				showWarning(`${payload.run.available} update${payload.run.available === 1 ? '' : 's'} available; ${payload.run.errors} check${payload.run.errors === 1 ? '' : 's'} failed`);
			} else {
				showSuccess(payload.run.available > 0
					? `${payload.run.available} update${payload.run.available === 1 ? '' : 's'} available`
					: 'All enrolled images are up to date');
			}
		} catch (error) {
			await load();
			showError(`Image scan failed — ${error instanceof Error ? error.message : String(error)}`);
		} finally {
			setScanning(false);
		}
	};

    const openPolicy = (row: UpdateEnrollment) => {
        const targetType = row.policyTarget ?? (row.stackKey ? 'stack' : 'container');
        setEditing(row);
        setDraft({
            targetType,
            enabled: row.source === 'interface' ? row.enrolled : true,
            schedule: row.source === 'interface' ? row.schedule ?? '' : '',
            rollbackEnabled: row.source === 'interface' ? row.rollback : true,
        });
    };

    const removeExistingPolicy = async (row: UpdateEnrollment) => {
        if (!row.policyTarget || !row.policyTargetId) return;
        const query = new URLSearchParams({targetType: row.policyTarget, targetKey: row.policyTargetId});
        const response = await fetch(hostUrl(`/docker/updates/policies?${query}`), {method: 'DELETE'});
        if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
    };

    const savePolicy = async () => {
        if (!editing) return;
        setSaving(true);
        try {
            const target = targetFor(editing, draft.targetType);
            // Turning one member of an enrolled stack into a container rule is
            // an intentional per-container override: keep the stack policy.
            // In the opposite direction the old container override must be
            // removed, otherwise it would continue to shadow the new stack rule.
            if (editing.policyTarget === 'container' && editing.policyTargetId &&
                (draft.targetType !== 'container' || editing.policyTargetId !== target.key)) {
                await removeExistingPolicy(editing);
            }
            const response = await fetch(hostUrl('/docker/updates/policies'), {
                method: 'PUT',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({
                    targetType: draft.targetType,
                    targetKey: target.key,
                    targetName: target.name,
                    enabled: draft.enabled,
                    schedule: draft.schedule.trim(),
                    rollbackEnabled: draft.rollbackEnabled,
                }),
            });
            if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
            setEditing(null);
            await load();
            showSuccess(`${target.name} update policy saved`);
        } catch (error) {
            showError(`Unable to save policy — ${error instanceof Error ? error.message : String(error)}`);
        } finally {
            setSaving(false);
        }
    };

    const deletePolicy = async () => {
        if (!editing) return;
        setSaving(true);
        try {
            await removeExistingPolicy(editing);
            setEditing(null);
            await load();
            showSuccess('Update policy removed');
        } catch (error) {
            showError(`Unable to remove policy — ${error instanceof Error ? error.message : String(error)}`);
        } finally {
            setSaving(false);
        }
    };

    return (
        <Box sx={{p: {xs: 1.5, md: 2.5}, maxWidth: 1500, mx: 'auto'}}>
            <PageHeader title="Updates" icon={<SystemUpdateAlt/>} right={<Stack direction="row" spacing={1}>
                <Button startIcon={<Refresh/>} onClick={() => void load()} disabled={loading}>Refresh</Button>
				<Button variant="contained" startIcon={<SystemUpdateAlt/>} onClick={() => void scanNow()} disabled={loading || scanning || enrolledCount === 0}>{scanning ? 'Scanning…' : 'Scan enrolled'}</Button>
				<Button startIcon={<SpaceDashboardOutlined/>} onClick={() => navigate(`/${host}/monitor`)}>Monitor</Button>
            </Stack>}/>

            <Stack direction={{xs: 'column', md: 'row'}} spacing={1.5} sx={{mb: 2}}>
                <Paper variant="outlined" sx={{p: 1.75, flex: 1}}>
                    <Typography color="text.secondary" variant="body2">Containers enrolled</Typography>
                    <Typography variant="h4">{enrolledCount} <Typography component="span" color="text.secondary">/ {rows.length}</Typography></Typography>
                </Paper>
                <Paper variant="outlined" sx={{p: 1.75, flex: 1}}>
                    <Typography color="text.secondary" variant="body2">Compose-controlled policies</Typography>
                    <Typography variant="h4">{labelCount}</Typography>
                </Paper>
				<Paper variant="outlined" sx={{p: 1.75, flex: 1}}>
					<Typography color="text.secondary" variant="body2">Updates available</Typography>
					<Typography variant="h4" color={availableCount > 0 ? 'warning.main' : 'inherit'}>{availableCount}</Typography>
				</Paper>
                <Paper variant="outlined" sx={{p: 1.75, flex: 2}}>
                    <Stack direction="row" spacing={1} sx={{alignItems: 'center'}}><ShieldOutlined color="success"/><Typography sx={{fontWeight: 600}}>Opt-in foundation</Typography></Stack>
					<Typography variant="body2" color="text.secondary">Image checks are scheduled automatically. Updates remain read-only: no container is recreated in this lot.</Typography>
                </Paper>
            </Stack>

            <Alert severity="info" sx={{mb: 2}}>
                Labels have priority over graphical rules. Use <code>dockman.update=true</code>, optionally <code>dockman.update.schedule</code> and <code>dockman.update.rollback</code>. <code>dockman.update.disable=true</code> always blocks enrollment.
            </Alert>

            <Paper variant="outlined">
                <Box sx={{p: 1.5, display: 'flex', gap: 1.5, alignItems: 'center'}}>
                    <TextField size="small" label="Search containers, images or stacks" value={search} onChange={event => setSearch(event.target.value)} sx={{width: {xs: '100%', sm: 420}}}/>
                    <Typography variant="body2" color="text.secondary">{visibleRows.length} result{visibleRows.length === 1 ? '' : 's'}</Typography>
                </Box>
                <TableContainer sx={{maxHeight: 'calc(100vh - 390px)', minHeight: 280}}>
                    <Table stickyHeader size="small">
                        <TableHead><TableRow>
                            <TableCell>Container</TableCell><TableCell>Stack</TableCell><TableCell>Image</TableCell>
							<TableCell>Policy</TableCell><TableCell>Schedule</TableCell><TableCell>Latest scan</TableCell><TableCell align="right">Action</TableCell>
                        </TableRow></TableHead>
                        <TableBody>
                            {visibleRows.map(row => <TableRow key={row.containerId} hover>
                                <TableCell><Stack direction="row" spacing={1} sx={{alignItems: 'center'}}>
                                    {row.enrolled && <CheckCircleOutlined color="success" fontSize="small"/>}
                                    <Box><Typography sx={{fontWeight: 600}}>{row.containerName}</Typography><Typography variant="caption" color="text.secondary">{row.state}</Typography></Box>
                                </Stack></TableCell>
                                <TableCell>{row.stackName || <Typography color="text.secondary">Standalone</Typography>}</TableCell>
                                <TableCell sx={{fontFamily: 'monospace', maxWidth: 360, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>{row.image}</TableCell>
                                <TableCell><Tooltip title={row.scheduleError || row.reason || ''}><Chip size="small" label={row.scheduleError ? 'Invalid schedule' : sourceLabels[row.source]} color={row.scheduleError ? 'error' : row.enrolled ? 'success' : row.source === 'disabled-label' ? 'warning' : 'default'} variant="outlined"/></Tooltip></TableCell>
								<TableCell sx={{fontFamily: 'monospace'}}>{row.schedule || (row.enrolled ? '0 4 * * *' : '—')}</TableCell>
								<TableCell>{row.enrolled && scanResults[row.containerId]?.image === row.image ? <Tooltip title={scanResults[row.containerId].reason ?? new Date(scanResults[row.containerId].checkedAt).toLocaleString()}>
									<Chip size="small" label={scanResults[row.containerId].status} color={scanResults[row.containerId].status === 'available' ? 'warning' : scanResults[row.containerId].status === 'error' ? 'error' : scanResults[row.containerId].status === 'current' ? 'success' : 'default'} variant="outlined"/>
								</Tooltip> : '—'}</TableCell>
                                <TableCell align="right"><Button size="small" startIcon={<EditOutlined/>} disabled={row.source === 'label' || row.source === 'disabled-label' || row.source === 'protected'} onClick={() => openPolicy(row)}>Configure</Button></TableCell>
                            </TableRow>)}
							{!loading && visibleRows.length === 0 && <TableRow><TableCell colSpan={7} align="center" sx={{py: 6, color: 'text.secondary'}}>No container matches this view.</TableCell></TableRow>}
                        </TableBody>
                    </Table>
                </TableContainer>
            </Paper>

			<Stack direction={{xs: 'column', lg: 'row'}} spacing={2} sx={{mt: 2}}>
				<Paper variant="outlined" sx={{p: 2, flex: 1}}>
					<Typography variant="h6" sx={{mb: 1}}>Scheduled checks</Typography>
					{schedules.length === 0 ? <Typography color="text.secondary">No enrolled target is currently scheduled.</Typography> : schedules.map(schedule =>
						<Stack key={schedule.schedule} direction="row" sx={{justifyContent: 'space-between', py: .75, borderBottom: 1, borderColor: 'divider'}}>
							<Box><Typography sx={{fontFamily: 'monospace'}}>{schedule.schedule}</Typography><Typography variant="caption" color="text.secondary">{schedule.targets} target{schedule.targets === 1 ? '' : 's'}</Typography></Box>
							<Typography variant="body2">{new Date(schedule.nextRun).toLocaleString()}</Typography>
						</Stack>)}
				</Paper>
				<Paper variant="outlined" sx={{p: 2, flex: 1.3}}>
					<Stack direction="row" spacing={1} sx={{alignItems: 'center', mb: 1}}><HistoryOutlined/><Typography variant="h6">Recent scans</Typography></Stack>
					{scanRuns.length === 0 ? <Typography color="text.secondary">No scan has run yet.</Typography> : scanRuns.slice(0, 8).map(run =>
						<Stack key={run.id} direction="row" spacing={1} sx={{alignItems: 'center', py: .65, borderBottom: 1, borderColor: 'divider'}}>
							<Chip size="small" label={run.trigger}/><Typography variant="body2" sx={{minWidth: 150}}>{new Date(run.startedAt).toLocaleString()}</Typography>
							<Typography variant="body2">{run.targets} checked · {run.available} available · {run.errors} errors</Typography>
						</Stack>)}
				</Paper>
			</Stack>

            <Dialog open={editing !== null} onClose={() => !saving && setEditing(null)} fullWidth maxWidth="sm">
                <DialogTitle><Stack direction="row" spacing={1} sx={{alignItems: 'center'}}><AccountTreeOutlined/><span>Update policy — {editing?.containerName}</span></Stack></DialogTitle>
                <DialogContent dividers><Stack spacing={2.25} sx={{pt: .5}}>
                    <FormControl fullWidth>
                        <InputLabel>Apply to</InputLabel>
                        <Select label="Apply to" value={draft.targetType} onChange={event => setDraft(current => ({...current, targetType: event.target.value as TargetType}))}>
                            <MenuItem value="container">This container only</MenuItem>
                            <MenuItem value="stack" disabled={!editing?.stackKey}>Entire stack{editing?.stackName ? ` — ${editing.stackName}` : ''}</MenuItem>
                        </Select>
                    </FormControl>
                    <FormControlLabel control={<Switch checked={draft.enabled} onChange={event => setDraft(current => ({...current, enabled: event.target.checked}))}/>} label="Enroll this target in automatic updates"/>
					<TextField label="Image check schedule (optional)" placeholder="0 4 * * *" value={draft.schedule} onChange={event => setDraft(current => ({...current, schedule: event.target.value}))} helperText="Standard five-field cron, minimum 15 minutes. Empty uses the daily 04:00 default."/>
                    <FormControlLabel control={<Switch checked={draft.rollbackEnabled} onChange={event => setDraft(current => ({...current, rollbackEnabled: event.target.checked}))}/>} label="Protect with automatic rollback"/>
                </Stack></DialogContent>
                <DialogActions>
                    {editing?.source === 'interface' && <Button color="error" onClick={() => void deletePolicy()} disabled={saving}>Remove policy</Button>}
                    <Box sx={{flex: 1}}/>
                    <Button onClick={() => setEditing(null)} disabled={saving}>Cancel</Button>
                    <Button variant="contained" onClick={() => void savePolicy()} disabled={saving}>Save policy</Button>
                </DialogActions>
            </Dialog>
        </Box>
    );
}
