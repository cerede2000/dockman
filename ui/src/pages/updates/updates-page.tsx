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
	ErrorOutlined,
    EditOutlined,
    EmailOutlined,
    HistoryOutlined,
	PauseCircleOutlined,
	PlayCircleOutlined,
    Refresh,
	ReplayOutlined,
	RocketLaunchOutlined,
    SendOutlined,
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

type ExecutionRun = {
	id: number; startedAt: string; schedule: string; targets: number; updated: number;
	current: number; rolledBack: number; failed: number; skipped: number; error?: string;
};

type AutomationControl = {paused: boolean; maxGroupsPerRun: number; running: boolean; updatedAt?: string};

type ExecutionResult = {
	id: number; runId: number; createdAt: string; containerId: string; containerName: string;
	image: string; remoteDigest: string; rollbackEnabled: boolean;
	targetType: TargetType; stackName?: string; stackKey?: string;
	state: 'updated' | 'current' | 'rolled_back' | 'failed' | 'skipped'; message?: string; logs?: string;
};

type ExecutionBlock = {
	id: number; containerId: string; containerName: string; image: string;
	remoteDigest: string; targetType: TargetType; stackName?: string; stackKey?: string;
	reason: string; updatedAt: string;
};

type SMTPConfig = {
    enabled: boolean;
    server: string;
    port: number;
    security: 'starttls' | 'tls' | 'none';
    username: string;
    password: string;
    fromAddress: string;
    recipients: string;
    notifyUpdates: boolean;
    notifyErrors: boolean;
    hasPassword: boolean;
    configured: boolean;
    updatedAt?: string;
};

type NotificationDelivery = {
    id: number;
    createdAt: string;
    kind: string;
    subject: string;
    success: boolean;
    error?: string;
};

const defaultSMTPConfig: SMTPConfig = {
    enabled: false, server: '', port: 587, security: 'starttls', username: '', password: '',
    fromAddress: '', recipients: '', notifyUpdates: true, notifyErrors: true,
    hasPassword: false, configured: false,
};

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
	const [protectedTarget, setProtectedTarget] = useState<UpdateEnrollment | null>(null);
	const [protectedStarting, setProtectedStarting] = useState(false);
	const [scanning, setScanning] = useState(false);
	const [scanResults, setScanResults] = useState<Record<string, ScanResult>>({});
	const [scanRuns, setScanRuns] = useState<ScanRun[]>([]);
	const [schedules, setSchedules] = useState<ScheduledScan[]>([]);
	const [executionRuns, setExecutionRuns] = useState<ExecutionRun[]>([]);
	const [executionResults, setExecutionResults] = useState<ExecutionResult[]>([]);
	const [executionBlocks, setExecutionBlocks] = useState<ExecutionBlock[]>([]);
	const [executionDetails, setExecutionDetails] = useState<ExecutionResult | null>(null);
	const [control, setControl] = useState<AutomationControl>({paused: false, maxGroupsPerRun: 0, running: false});
	const [controlOpen, setControlOpen] = useState(false);
	const [controlSaving, setControlSaving] = useState(false);
	const [controlDraft, setControlDraft] = useState(0);
	const [executeConfirmOpen, setExecuteConfirmOpen] = useState(false);
	const [executing, setExecuting] = useState(false);
    const [smtpOpen, setSMTPOpen] = useState(false);
    const [smtpSaving, setSMTPSaving] = useState(false);
    const [smtpTesting, setSMTPTesting] = useState(false);
    const [smtpConfig, setSMTPConfig] = useState<SMTPConfig>(defaultSMTPConfig);
    const [smtpDraft, setSMTPDraft] = useState<SMTPConfig>(defaultSMTPConfig);
    const [deliveries, setDeliveries] = useState<NotificationDelivery[]>([]);
    const [draft, setDraft] = useState<PolicyDraft>({
        targetType: 'container', enabled: true, schedule: '', rollbackEnabled: true,
    });

    const load = useCallback(async () => {
        setLoading(true);
        try {
            const [response, stateResponse, smtpResponse] = await Promise.all([
                fetch(hostUrl('/docker/updates/inventory')),
                fetch(hostUrl('/docker/updates/state')),
                fetch(hostUrl('/docker/updates/notifications/smtp')),
            ]);
            if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
            const payload = await response.json() as {results: UpdateEnrollment[]};
            setRows(payload.results ?? []);
			if (!stateResponse.ok) throw new Error((await stateResponse.text()).trim() || `HTTP ${stateResponse.status}`);
			const state = await stateResponse.json() as {results: ScanResult[]; runs: ScanRun[]; schedules: ScheduledScan[]; executionRuns: ExecutionRun[]; executionResults: ExecutionResult[]; blocks: ExecutionBlock[]; control?: AutomationControl};
			setScanResults(Object.fromEntries((state.results ?? []).map(result => [result.containerId, result])));
			setScanRuns(state.runs ?? []);
			setSchedules(state.schedules ?? []);
			setExecutionRuns(state.executionRuns ?? []);
			setExecutionResults(state.executionResults ?? []);
			setExecutionBlocks(state.blocks ?? []);
			setControl(state.control ?? {paused: false, maxGroupsPerRun: 0, running: false});
            if (!smtpResponse.ok) throw new Error((await smtpResponse.text()).trim() || `HTTP ${smtpResponse.status}`);
            const smtp = await smtpResponse.json() as {config: SMTPConfig; deliveries: NotificationDelivery[]};
            const nextSMTP = {...defaultSMTPConfig, ...(smtp.config ?? {}), password: ''};
            setSMTPConfig(nextSMTP);
            setDeliveries(smtp.deliveries ?? []);
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
	const blocksByContainer = useMemo(() => Object.fromEntries(executionBlocks.map(block => [block.containerId, block])), [executionBlocks]);

	const allowRetry = async (containerId: string) => {
		try {
			const query = new URLSearchParams({containerId});
			const response = await fetch(hostUrl(`/docker/updates/execution-block?${query}`), {method: 'DELETE'});
			if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
			await load();
			showSuccess('The next scheduled scan may retry this digest');
		} catch (error) {
			showError(`Unable to allow retry — ${error instanceof Error ? error.message : String(error)}`);
		}
	};

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

	const saveControl = async (paused: boolean, maxGroupsPerRun = control.maxGroupsPerRun) => {
		setControlSaving(true);
		try {
			const response = await fetch(hostUrl('/docker/updates/control'), {
				method: 'PUT', headers: {'Content-Type': 'application/json'},
				body: JSON.stringify({paused, maxGroupsPerRun}),
			});
			if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
			const saved = await response.json() as AutomationControl;
			setControl(saved);
			setControlOpen(false);
			showSuccess(paused ? 'Automatic update execution paused' : 'Automatic update execution enabled');
		} catch (error) {
			showError(`Unable to save automatic update controls — ${error instanceof Error ? error.message : String(error)}`);
		} finally {
			setControlSaving(false);
		}
	};

	const runAutomaticNow = async () => {
		setExecuteConfirmOpen(false);
		setExecuting(true);
		try {
			const response = await fetch(hostUrl('/docker/updates/run'), {method: 'POST'});
			if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
			const payload = await response.json() as {run: ScanRun};
			await load();
			showSuccess(payload.run.available > 0 ? 'Automatic update cycle completed' : 'No enrolled image needed an update');
		} catch (error) {
			await load();
			showError(`Automatic update cycle failed — ${error instanceof Error ? error.message : String(error)}`);
		} finally {
			setExecuting(false);
		}
	};

	const startProtectedUpdate = async () => {
		if (!protectedTarget) return;
		setProtectedStarting(true);
		try {
			const response = await fetch(hostUrl(`/docker/updates/protected/${encodeURIComponent(protectedTarget.containerId)}`), {method: 'POST'});
			if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
			setProtectedTarget(null);
			showSuccess(`Protected update started for ${protectedTarget.containerName}. Docker access may reconnect briefly.`);
		} catch (error) {
			showError(`Unable to start protected update — ${error instanceof Error ? error.message : String(error)}`);
		} finally {
			setProtectedStarting(false);
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

    const openSMTP = () => {
        setSMTPDraft({...smtpConfig, password: ''});
        setSMTPOpen(true);
    };

    const saveSMTP = async () => {
        setSMTPSaving(true);
        try {
            const response = await fetch(hostUrl('/docker/updates/notifications/smtp'), {
                method: 'PUT', headers: {'Content-Type': 'application/json'},
                body: JSON.stringify(smtpDraft),
            });
            if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
            const saved = await response.json() as SMTPConfig;
            const next = {...defaultSMTPConfig, ...saved, password: ''};
            setSMTPConfig(next);
            setSMTPDraft(next);
            showSuccess('SMTP notification configuration saved securely');
        } catch (error) {
            showError(`Unable to save SMTP configuration — ${error instanceof Error ? error.message : String(error)}`);
        } finally {
            setSMTPSaving(false);
        }
    };

    const testSMTP = async () => {
        setSMTPTesting(true);
        try {
            const response = await fetch(hostUrl('/docker/updates/notifications/smtp/test'), {method: 'POST'});
            if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
            showSuccess('SMTP test message sent');
            await load();
        } catch (error) {
            showError(`SMTP test failed — ${error instanceof Error ? error.message : String(error)}`);
        } finally {
            setSMTPTesting(false);
        }
    };

    return (
        <Box sx={{p: {xs: 1.5, md: 2.5}, maxWidth: 1500, mx: 'auto'}}>
            <PageHeader title="Updates" icon={<SystemUpdateAlt/>} right={<Stack direction="row" spacing={1}>
                <Button startIcon={<Refresh/>} onClick={() => void load()} disabled={loading}>Refresh</Button>
				<Button variant="contained" startIcon={<SystemUpdateAlt/>} onClick={() => void scanNow()} disabled={loading || scanning || enrolledCount === 0}>{scanning ? 'Scanning…' : 'Scan enrolled'}</Button>
				<Button color={control.paused ? 'warning' : 'success'} startIcon={control.paused ? <PlayCircleOutlined/> : <PauseCircleOutlined/>} onClick={() => void saveControl(!control.paused)} disabled={loading || controlSaving || executing || control.running}>{control.paused ? 'Resume auto' : 'Pause auto'}</Button>
				<Button color="warning" startIcon={<RocketLaunchOutlined/>} onClick={() => setExecuteConfirmOpen(true)} disabled={loading || executing || control.running || control.paused || enrolledCount === 0}>{executing || control.running ? 'Running…' : 'Run updates now'}</Button>
				<Button startIcon={<EmailOutlined/>} color={smtpConfig.enabled ? 'success' : 'inherit'} onClick={openSMTP}>SMTP</Button>
				<Button startIcon={<SpaceDashboardOutlined/>} onClick={() => navigate(`/${host}/monitor`)}>Monitor</Button>
            </Stack>}/>

			{control.paused && <Alert severity="warning" sx={{mb: 2}}>Automatic image checks remain active, but installation is paused for this host. Manual protected infrastructure updates remain independent.</Alert>}

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
                    <Stack direction="row" spacing={1} sx={{alignItems: 'center'}}><ShieldOutlined color="success"/><Typography sx={{fontWeight: 600}}>Protected automatic updates</Typography></Stack>
					<Typography variant="body2" color="text.secondary">Scheduled checks apply available images only to enrolled targets. Container rules stay isolated; stack rules preload every image, follow dependencies and roll the whole changed group back on failure.</Typography>
                </Paper>
            </Stack>

            <Alert severity="info" sx={{mb: 2}}>
                Labels have priority over graphical rules. Use <code>dockman.update=true</code>, optionally <code>dockman.update.schedule</code> and <code>dockman.update.rollback</code>. <code>dockman.update.disable=true</code> always blocks enrollment. “Scan enrolled” is manual and read-only.
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
								<TableCell>{blocksByContainer[row.containerId] ? <Tooltip title={blocksByContainer[row.containerId].reason}><Chip size="small" icon={<ErrorOutlined/>} label="retry blocked" color="error" variant="outlined"/></Tooltip> : row.enrolled && scanResults[row.containerId]?.image === row.image ? <Tooltip title={scanResults[row.containerId].reason ?? new Date(scanResults[row.containerId].checkedAt).toLocaleString()}>
									<Chip size="small" label={scanResults[row.containerId].status} color={scanResults[row.containerId].status === 'available' ? 'warning' : scanResults[row.containerId].status === 'error' ? 'error' : scanResults[row.containerId].status === 'current' ? 'success' : 'default'} variant="outlined"/>
								</Tooltip> : '—'}</TableCell>
				<TableCell align="right"><Stack direction="row" spacing={.5} sx={{justifyContent: 'flex-end'}}>{blocksByContainer[row.containerId] && <Tooltip title={blocksByContainer[row.containerId].targetType === 'stack' ? 'Allow one retry of this digest for the whole stack transaction' : 'Allow one retry of this same image digest'}><Button size="small" color="warning" startIcon={<ReplayOutlined/>} onClick={() => void allowRetry(row.containerId)}>Retry</Button></Tooltip>}{host === 'local' && row.stackKey && row.source !== 'protected' && <Tooltip title="Update this sensitive Compose service through a detached helper with health verification and rollback"><Button size="small" color="warning" startIcon={<ShieldOutlined/>} onClick={() => setProtectedTarget(row)}>Protected update</Button></Tooltip>}<Button size="small" startIcon={<EditOutlined/>} disabled={row.source === 'label' || row.source === 'disabled-label' || row.source === 'protected'} onClick={() => openPolicy(row)}>Configure</Button></Stack></TableCell>
                            </TableRow>)}
							{!loading && visibleRows.length === 0 && <TableRow><TableCell colSpan={7} align="center" sx={{py: 6, color: 'text.secondary'}}>No container matches this view.</TableCell></TableRow>}
                        </TableBody>
                    </Table>
                </TableContainer>
            </Paper>

			<Stack direction={{xs: 'column', lg: 'row'}} spacing={2} sx={{mt: 2}}>
				<Paper variant="outlined" sx={{p: 2, flex: 1}}>
					<Stack direction="row" sx={{alignItems: 'center', justifyContent: 'space-between', mb: 1}}><Typography variant="h6">Scheduled checks</Typography><Button size="small" onClick={() => {setControlDraft(control.maxGroupsPerRun); setControlOpen(true);}}>Limits</Button></Stack>
					<Typography variant="caption" color="text.secondary">{control.maxGroupsPerRun > 0 ? `At most ${control.maxGroupsPerRun} independent update group(s) per cycle. A Compose stack is never split.` : 'No group limit. Compose stacks are still executed atomically.'}</Typography>
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

			<Paper variant="outlined" sx={{mt: 2, p: 2}}>
				<Stack direction="row" spacing={1} sx={{alignItems: 'center', mb: 1}}><ShieldOutlined/><Typography variant="h6">Automatic update history</Typography>{executionBlocks.length > 0 && <Chip size="small" color="error" variant="outlined" label={`${executionBlocks.length} retry blocked`}/>}</Stack>
				{executionRuns.length === 0 ? <Typography color="text.secondary">No automatic update has been applied yet. Read-only scans never create an execution.</Typography> : executionRuns.slice(0, 10).map(run => <Box key={run.id} sx={{py: .8, borderBottom: 1, borderColor: 'divider'}}>
					<Stack direction={{xs: 'column', md: 'row'}} spacing={1} sx={{alignItems: {md: 'center'}}}>
						<Typography variant="body2" sx={{minWidth: 165}}>{new Date(run.startedAt).toLocaleString()}</Typography>
						<Chip size="small" variant="outlined" label={run.schedule}/>
						<Typography variant="body2">{run.updated} updated · {run.current} current · {run.rolledBack} rolled back · {run.failed} failed</Typography>
					</Stack>
					{run.error && <Alert severity="warning" sx={{mt: .75}}>{run.error}</Alert>}
					<Stack direction="row" spacing={.75} sx={{mt: .6, flexWrap: 'wrap'}}>{executionResults.filter(result => result.runId === run.id).map(result => <Chip key={result.id} clickable onClick={() => setExecutionDetails(result)} size="small" variant="outlined" icon={result.targetType === 'stack' ? <AccountTreeOutlined/> : undefined} label={`${result.stackName ? `${result.stackName} / ` : ''}${result.containerName}: ${result.state}`} color={result.state === 'updated' || result.state === 'current' ? 'success' : result.state === 'failed' || result.state === 'rolled_back' ? 'error' : 'default'}/>)}</Stack>
				</Box>)}
			</Paper>

			<Dialog open={executeConfirmOpen} onClose={() => !executing && setExecuteConfirmOpen(false)} fullWidth maxWidth="sm">
				<DialogTitle>Run the protected automatic update cycle now?</DialogTitle>
				<DialogContent dividers><Stack spacing={1.5}>
					<Typography>Dockman will check every enrolled target, then install available images using the same health verification, stack transaction and rollback rules as the scheduler.</Typography>
					<Alert severity="warning">This is an execution, not the read-only “Scan enrolled” action.</Alert>
					<Typography variant="body2" color="text.secondary">{control.maxGroupsPerRun > 0 ? `This cycle is limited to ${control.maxGroupsPerRun} independent update group(s). Remaining updates stay available for a later cycle.` : 'No update-group limit is configured.'}</Typography>
				</Stack></DialogContent>
				<DialogActions><Button onClick={() => setExecuteConfirmOpen(false)}>Cancel</Button><Button variant="contained" color="warning" startIcon={<RocketLaunchOutlined/>} onClick={() => void runAutomaticNow()}>Run protected cycle</Button></DialogActions>
			</Dialog>

			<Dialog open={controlOpen} onClose={() => !controlSaving && setControlOpen(false)} fullWidth maxWidth="sm">
				<DialogTitle>Automatic update execution limits</DialogTitle>
				<DialogContent dividers><Stack spacing={2}>
					<TextField type="number" label="Maximum independent groups per cycle" value={controlDraft} onChange={event => setControlDraft(Math.max(0, Math.min(1000, Number(event.target.value) || 0)))} slotProps={{htmlInput: {min: 0, max: 1000}}} helperText="0 means unlimited. One standalone container is one group; one complete Compose stack is one group, regardless of its container count."/>
					<Alert severity="info">The limit only reduces the blast radius of each cycle. It never splits a stack transaction, and pending images remain eligible for the next cycle.</Alert>
				</Stack></DialogContent>
				<DialogActions><Button onClick={() => setControlOpen(false)} disabled={controlSaving}>Cancel</Button><Button variant="contained" onClick={() => void saveControl(control.paused, controlDraft)} disabled={controlSaving}>Save limit</Button></DialogActions>
			</Dialog>

            {(smtpConfig.configured || deliveries.length > 0) && <Paper variant="outlined" sx={{mt: 2, p: 2}}>
                <Stack direction={{xs: 'column', md: 'row'}} spacing={2} sx={{alignItems: {md: 'center'}}}>
                    <Box sx={{flex: 1}}><Stack direction="row" spacing={1} sx={{alignItems: 'center'}}><EmailOutlined color={smtpConfig.enabled ? 'success' : 'disabled'}/><Typography variant="h6">SMTP notifications</Typography><Chip size="small" variant="outlined" color={smtpConfig.enabled ? 'success' : 'default'} label={smtpConfig.enabled ? 'Enabled' : 'Disabled'}/></Stack><Typography variant="body2" color="text.secondary">{smtpConfig.configured ? `${smtpConfig.server}:${smtpConfig.port} · ${smtpConfig.recipients}` : 'Not configured'}</Typography></Box>
                    {deliveries[0] && <Tooltip title={deliveries[0].error || deliveries[0].subject}><Chip size="small" variant="outlined" color={deliveries[0].success ? 'success' : 'error'} label={`Last delivery: ${deliveries[0].success ? 'sent' : 'failed'} · ${new Date(deliveries[0].createdAt).toLocaleString()}`}/></Tooltip>}
                    <Button startIcon={<EditOutlined/>} onClick={openSMTP}>Configure</Button>
                </Stack>
            </Paper>}

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
					<FormControlLabel control={<Switch checked={draft.enabled} onChange={event => setDraft(current => ({...current, enabled: event.target.checked}))}/>} label="Automatically install available image updates"/>
					<TextField label="Image check schedule (optional)" placeholder="0 4 * * *" value={draft.schedule} onChange={event => setDraft(current => ({...current, schedule: event.target.value}))} helperText="Standard five-field cron, minimum 15 minutes. Empty uses the daily 04:00 default."/>
					<FormControlLabel control={<Switch checked={draft.rollbackEnabled} onChange={event => setDraft(current => ({...current, rollbackEnabled: event.target.checked}))}/>} label="Verify health and roll back automatically"/>
                </Stack></DialogContent>
                <DialogActions>
                    {editing?.source === 'interface' && <Button color="error" onClick={() => void deletePolicy()} disabled={saving}>Remove policy</Button>}
                    <Box sx={{flex: 1}}/>
                    <Button onClick={() => setEditing(null)} disabled={saving}>Cancel</Button>
                    <Button variant="contained" onClick={() => void savePolicy()} disabled={saving}>Save policy</Button>
                </DialogActions>
            </Dialog>

			<Dialog open={protectedTarget !== null} onClose={() => !protectedStarting && setProtectedTarget(null)} fullWidth maxWidth="sm">
				<DialogTitle><Stack direction="row" spacing={1} sx={{alignItems: 'center'}}><ShieldOutlined color="warning"/><span>Protected infrastructure update</span></Stack></DialogTitle>
				<DialogContent dividers><Stack spacing={2}>
					<Typography>Update <strong>{protectedTarget?.containerName}</strong> independently through a detached Docker helper?</Typography>
					<Alert severity="warning">Use this mode for sensitive local infrastructure such as the Docker socket proxy. Dockman's Docker access may disconnect briefly, but the helper continues through the raw local socket.</Alert>
					<Alert severity="info">Only this Compose service is recreated. Its previous image is retained, the replacement is verified for up to 90 seconds, and an unhealthy or stopped replacement is rolled back automatically.</Alert>
					<Box><Typography variant="caption" color="text.secondary">Image</Typography><Typography sx={{fontFamily: 'monospace', overflowWrap: 'anywhere'}}>{protectedTarget?.image}</Typography></Box>
					<Typography variant="body2" color="text.secondary">If the helper cannot complete successfully, it remains available as <code>dockman-protected-update</code> so its logs can be inspected.</Typography>
				</Stack></DialogContent>
				<DialogActions><Button onClick={() => setProtectedTarget(null)} disabled={protectedStarting}>Cancel</Button><Button variant="contained" color="warning" startIcon={<ShieldOutlined/>} onClick={() => void startProtectedUpdate()} disabled={protectedStarting}>{protectedStarting ? 'Starting…' : 'Start protected update'}</Button></DialogActions>
			</Dialog>

			<Dialog open={executionDetails !== null} onClose={() => setExecutionDetails(null)} fullWidth maxWidth="md">
				<DialogTitle>Automatic update — {executionDetails?.containerName}</DialogTitle>
				<DialogContent dividers><Stack spacing={1.5}>
					<Stack direction="row" spacing={1}><Chip size="small" color={executionDetails?.state === 'updated' || executionDetails?.state === 'current' ? 'success' : 'error'} label={executionDetails?.state ?? ''}/>{executionDetails?.targetType === 'stack' && <Chip size="small" variant="outlined" icon={<AccountTreeOutlined/>} label={`Stack transaction · ${executionDetails.stackName || 'Compose'}`}/>}<Typography variant="body2" color="text.secondary">{executionDetails?.rollbackEnabled ? 'Health verification and rollback enabled' : 'Transactional replacement only'}</Typography></Stack>
					<Typography sx={{fontFamily: 'monospace', overflowWrap: 'anywhere'}}>{executionDetails?.image}</Typography>
					{executionDetails?.message && <Alert severity={executionDetails.state === 'updated' || executionDetails.state === 'current' ? 'success' : 'error'}>{executionDetails.message}</Alert>}
					<Box component="pre" sx={{m: 0, p: 1.5, bgcolor: '#050607', color: '#d7ffd0', borderRadius: 1, maxHeight: 360, overflow: 'auto', whiteSpace: 'pre-wrap', userSelect: 'text'}}>{executionDetails?.logs || 'No command output was recorded.'}</Box>
				</Stack></DialogContent>
				<DialogActions><Button onClick={() => setExecutionDetails(null)}>Close</Button></DialogActions>
			</Dialog>

            <Dialog open={smtpOpen} onClose={() => !smtpSaving && !smtpTesting && setSMTPOpen(false)} fullWidth maxWidth="md">
                <DialogTitle><Stack direction="row" spacing={1} sx={{alignItems: 'center'}}><EmailOutlined/><span>SMTP notifications — {host}</span></Stack></DialogTitle>
				<DialogContent dividers><Stack spacing={2} sx={{pt: .5}}>
					<Alert severity="info">Dockman sends grouped scheduled scan errors and automatic update outcomes. Manual scans never send mail. The SMTP password is encrypted at rest and never returned by the API.</Alert>
					{smtpConfig.configured && !smtpConfig.enabled && <Alert severity="warning">Automatic notifications are disabled. A test message can still succeed, but scheduled scans and automatic updates will not send mail until this option is enabled and saved.</Alert>}
					{smtpConfig.configured && smtpConfig.enabled && !smtpConfig.notifyUpdates && <Alert severity="warning">Successful update notifications are disabled. Errors may still be sent, but successful automatic updates will not generate mail.</Alert>}
					<FormControlLabel control={<Switch checked={smtpDraft.enabled} onChange={event => setSMTPDraft(current => ({...current, enabled: event.target.checked}))}/>} label="Enable automatic notifications"/>
                    <Stack direction={{xs: 'column', sm: 'row'}} spacing={1.5}>
                        <TextField label="SMTP server" value={smtpDraft.server} onChange={event => setSMTPDraft(current => ({...current, server: event.target.value}))} fullWidth placeholder="smtp.example.com"/>
                        <TextField label="Port" type="number" value={smtpDraft.port} onChange={event => setSMTPDraft(current => ({...current, port: Number(event.target.value)}))} sx={{width: {xs: '100%', sm: 130}}}/>
                        <FormControl sx={{width: {xs: '100%', sm: 190}}}><InputLabel>Security</InputLabel><Select label="Security" value={smtpDraft.security} onChange={event => setSMTPDraft(current => { const security = event.target.value as SMTPConfig['security']; return {...current, security, ...(security === 'none' ? {username: '', password: ''} : {})}; })}><MenuItem value="starttls">STARTTLS</MenuItem><MenuItem value="tls">TLS / SMTPS</MenuItem><MenuItem value="none">None (no auth)</MenuItem></Select></FormControl>
                    </Stack>
                    <Stack direction={{xs: 'column', sm: 'row'}} spacing={1.5}>
                        <TextField label="Username" value={smtpDraft.username} disabled={smtpDraft.security === 'none'} onChange={event => setSMTPDraft(current => ({...current, username: event.target.value}))} fullWidth autoComplete="username"/>
                        <TextField label={smtpDraft.hasPassword ? 'New password (leave blank to keep current)' : 'Password'} type="password" value={smtpDraft.password} disabled={smtpDraft.security === 'none'} onChange={event => setSMTPDraft(current => ({...current, password: event.target.value}))} fullWidth autoComplete="new-password"/>
                    </Stack>
					<TextField label="From address" value={smtpDraft.fromAddress} onChange={event => setSMTPDraft(current => ({...current, fromAddress: event.target.value}))} fullWidth placeholder="Dockman <dockman@example.com>" helperText="For reliable delivery, use an address aligned with the authenticated SMTP domain and configure SPF, DKIM and DMARC for that domain."/>
                    <TextField label="Recipients" value={smtpDraft.recipients} onChange={event => setSMTPDraft(current => ({...current, recipients: event.target.value}))} fullWidth helperText="Comma, semicolon or line separated; maximum 25 recipients."/>
                    <Stack direction={{xs: 'column', sm: 'row'}} spacing={2}>
						<FormControlLabel control={<Switch checked={smtpDraft.notifyUpdates} onChange={event => setSMTPDraft(current => ({...current, notifyUpdates: event.target.checked}))}/>} label="Notify successful updates"/>
						<FormControlLabel control={<Switch checked={smtpDraft.notifyErrors} onChange={event => setSMTPDraft(current => ({...current, notifyErrors: event.target.checked}))}/>} label="Notify errors and rollbacks"/>
                    </Stack>
					<Box><Typography variant="subtitle2" sx={{mb: .75}}>Recent deliveries</Typography>{deliveries.length === 0 ? <Typography variant="body2" color="text.secondary">No delivery attempt recorded. Automatic events are only attempted when notifications and the matching success/error category are enabled.</Typography> : <TableContainer sx={{maxHeight: 180}}><Table size="small" stickyHeader><TableHead><TableRow><TableCell>Date</TableCell><TableCell>Type</TableCell><TableCell>Subject</TableCell><TableCell>Status</TableCell></TableRow></TableHead><TableBody>{deliveries.slice(0, 10).map(delivery => <TableRow key={delivery.id}><TableCell sx={{whiteSpace: 'nowrap'}}>{new Date(delivery.createdAt).toLocaleString()}</TableCell><TableCell>{delivery.kind}</TableCell><TableCell>{delivery.subject}</TableCell><TableCell><Tooltip title={delivery.error || ''}><Chip size="small" color={delivery.success ? 'success' : 'error'} variant="outlined" label={delivery.success ? 'sent' : 'failed'}/></Tooltip></TableCell></TableRow>)}</TableBody></Table></TableContainer>}</Box>
                </Stack></DialogContent>
                <DialogActions>
                    <Button onClick={() => setSMTPOpen(false)} disabled={smtpSaving || smtpTesting}>Close</Button><Box sx={{flex: 1}}/>
                    <Button startIcon={<SendOutlined/>} onClick={() => void testSMTP()} disabled={smtpSaving || smtpTesting || !smtpConfig.configured}>{smtpTesting ? 'Sending…' : 'Send test'}</Button>
                    <Button variant="contained" onClick={() => void saveSMTP()} disabled={smtpSaving || smtpTesting}>{smtpSaving ? 'Saving…' : 'Save'}</Button>
                </DialogActions>
            </Dialog>
        </Box>
    );
}
