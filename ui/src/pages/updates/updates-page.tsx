import {
    Alert,
    Box,
    Button,
    Checkbox,
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
	DeleteSweepOutlined,
	ErrorOutlined,
    EditOutlined,
    HistoryOutlined,
	PauseCircleOutlined,
	PlayCircleOutlined,
    Refresh,
	ReplayOutlined,
	RocketLaunchOutlined,
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
import NotificationChannels from './notification-channels.tsx';

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
	cleanupEnabled: boolean;
	cleanupKeep: number;
    versionPolicy: 'off' | 'patch' | 'minor' | 'major';
    versionPrerelease: boolean;
    policyTarget?: TargetType;
    policyTargetId?: string;
};

type PolicyDraft = {
    targetType: TargetType;
    enabled: boolean;
    schedule: string;
    rollbackEnabled: boolean;
	cleanupEnabled: boolean;
	cleanupKeep: number;
    versionPolicy: 'off' | 'patch' | 'minor' | 'major';
    versionPrerelease: boolean;
};

type ScanResult = {
    containerId: string;
	image: string;
    status: 'available' | 'current' | 'skipped' | 'error';
    reason?: string;
    currentTag?: string;
    latestTag?: string;
    versionPolicy?: string;
    versionAvailable: boolean;
    versionReason?: string;
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
    versions: number;
    error?: string;
};

type ScheduledScan = {schedule: string; nextRun: string; targets: number};

type ExecutionRun = {
	id: number; startedAt: string; schedule: string; targets: number; updated: number;
	current: number; rolledBack: number; failed: number; skipped: number; error?: string;
};

type AutomationControl = {paused: boolean; maxGroupsPerRun: number; running: boolean; updatedAt?: string};

type ImageCleanup = {
	id: number; createdAt: string; updatedAt: string; targetKey: string; containerName: string;
	imageId: string; retention: number; status: 'pending' | 'removed'; reason?: string;
};

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
    const [selected, setSelected] = useState<Set<string>>(new Set());
    const [bulkOpen, setBulkOpen] = useState(false);
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
	const [cleanups, setCleanups] = useState<ImageCleanup[]>([]);
	const [cleanupRetrying, setCleanupRetrying] = useState(false);
    const [draft, setDraft] = useState<PolicyDraft>({
        targetType: 'container', enabled: true, schedule: '', rollbackEnabled: true, cleanupEnabled: false, cleanupKeep: 1, versionPolicy: 'off', versionPrerelease: false,
    });

    const load = useCallback(async () => {
        setLoading(true);
        try {
            const [response, stateResponse] = await Promise.all([
                fetch(hostUrl('/docker/updates/inventory')),
                fetch(hostUrl('/docker/updates/state')),
            ]);
            if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
            const payload = await response.json() as {results: UpdateEnrollment[]};
            setRows(payload.results ?? []);
			setSelected(current => new Set([...current].filter(id => (payload.results ?? []).some(row => row.containerId === id && row.source !== 'label' && row.source !== 'disabled-label' && row.source !== 'protected'))));
			if (!stateResponse.ok) throw new Error((await stateResponse.text()).trim() || `HTTP ${stateResponse.status}`);
			const state = await stateResponse.json() as {results: ScanResult[]; runs: ScanRun[]; schedules: ScheduledScan[]; executionRuns: ExecutionRun[]; executionResults: ExecutionResult[]; blocks: ExecutionBlock[]; control?: AutomationControl; cleanups?: ImageCleanup[]};
			setScanResults(Object.fromEntries((state.results ?? []).map(result => [result.containerId, result])));
			setScanRuns(state.runs ?? []);
			setSchedules(state.schedules ?? []);
			setExecutionRuns(state.executionRuns ?? []);
			setExecutionResults(state.executionResults ?? []);
			setExecutionBlocks(state.blocks ?? []);
			setControl(state.control ?? {paused: false, maxGroupsPerRun: 0, running: false});
			setCleanups(state.cleanups ?? []);
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
	const newerVersionCount = rows.filter(row => scanResults[row.containerId]?.versionAvailable).length;
	const selectableVisibleRows = visibleRows.filter(row => row.source !== 'label' && row.source !== 'disabled-label' && row.source !== 'protected');
	const selectedRows = rows.filter(row => selected.has(row.containerId) && row.source !== 'label' && row.source !== 'disabled-label' && row.source !== 'protected');
	const allVisibleSelected = selectableVisibleRows.length > 0 && selectableVisibleRows.every(row => selected.has(row.containerId));
	const someVisibleSelected = selectableVisibleRows.some(row => selected.has(row.containerId));
	const toggleVisibleSelection = () => setSelected(current => {
		const next = new Set(current);
		if (allVisibleSelected) selectableVisibleRows.forEach(row => next.delete(row.containerId));
		else selectableVisibleRows.forEach(row => next.add(row.containerId));
		return next;
	});
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
				showWarning(`${payload.run.available} digest update${payload.run.available === 1 ? '' : 's'} and ${payload.run.versions} newer tag${payload.run.versions === 1 ? '' : 's'}; ${payload.run.errors} check${payload.run.errors === 1 ? '' : 's'} failed`);
			} else {
				showSuccess(payload.run.available > 0 || payload.run.versions > 0
					? `${payload.run.available} digest update${payload.run.available === 1 ? '' : 's'} and ${payload.run.versions} newer tag${payload.run.versions === 1 ? '' : 's'} found`
					: 'All enrolled images and configured version channels are up to date');
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

	const retryImageCleanup = async () => {
		setCleanupRetrying(true);
		try {
			const response = await fetch(hostUrl('/docker/updates/cleanup/retry'), {method: 'POST'});
			if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
			await load();
			showSuccess('Safe previous-image cleanup checked again');
		} catch (error) {
			await load();
			showError(`Unable to retry image cleanup — ${error instanceof Error ? error.message : String(error)}`);
		} finally {
			setCleanupRetrying(false);
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
			cleanupEnabled: row.source === 'interface' ? row.cleanupEnabled : false,
            cleanupKeep: row.source === 'interface' && row.cleanupEnabled ? row.cleanupKeep : 1,
            versionPolicy: row.source === 'interface' ? row.versionPolicy : 'off',
            versionPrerelease: row.source === 'interface' ? row.versionPrerelease : false,
        });
    };

	const openBulkPolicy = () => {
		if (selectedRows.length === 0) return;
		setDraft({targetType: selectedRows.every(row => row.stackKey) ? 'stack' : 'container', enabled: true, schedule: '', rollbackEnabled: true, cleanupEnabled: false, cleanupKeep: 1, versionPolicy: 'off', versionPrerelease: false});
		setBulkOpen(true);
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
					cleanupEnabled: draft.cleanupEnabled,
					cleanupKeep: draft.cleanupKeep,
					versionPolicy: draft.versionPolicy,
					versionPrerelease: draft.versionPrerelease,
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

	const saveBulkPolicy = async () => {
		if (selectedRows.length === 0) return;
		if (draft.targetType === 'stack' && selectedRows.some(row => !row.stackKey)) {
			showError('Standalone containers cannot receive a stack policy');
			return;
		}
		setSaving(true);
		try {
			const targets = new Map<string, {key: string; name: string}>();
			const removals = new Map<string, {targetType: TargetType; targetKey: string}>();
			for (const row of selectedRows) {
				const target = targetFor(row, draft.targetType);
				targets.set(`${draft.targetType}\0${target.key}`, target);
				if (row.policyTarget === 'container' && row.policyTargetId && (draft.targetType !== 'container' || row.policyTargetId !== target.key)) {
					removals.set(`container\0${row.policyTargetId}`, {targetType: 'container', targetKey: row.policyTargetId});
				}
			}
			const policies = [...targets.values()].map(target => ({
				targetType: draft.targetType, targetKey: target.key, targetName: target.name,
				enabled: draft.enabled, schedule: draft.schedule.trim(), rollbackEnabled: draft.rollbackEnabled,
				cleanupEnabled: draft.cleanupEnabled, cleanupKeep: draft.cleanupKeep,
				versionPolicy: draft.versionPolicy, versionPrerelease: draft.versionPrerelease,
			}));
			const response = await fetch(hostUrl('/docker/updates/policies/bulk'), {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({policies, removals: [...removals.values()]})});
			if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
			setBulkOpen(false);
			setSelected(new Set());
			await load();
			showSuccess(`${policies.length} update ${policies.length === 1 ? 'policy' : 'policies'} saved atomically`);
		} catch (error) {
			showError(`Unable to save bulk policies — ${error instanceof Error ? error.message : String(error)}`);
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
				<Button color={control.paused ? 'warning' : 'success'} startIcon={control.paused ? <PlayCircleOutlined/> : <PauseCircleOutlined/>} onClick={() => void saveControl(!control.paused)} disabled={loading || controlSaving || executing || control.running}>{control.paused ? 'Resume auto' : 'Pause auto'}</Button>
				<Button color="warning" startIcon={<RocketLaunchOutlined/>} onClick={() => setExecuteConfirmOpen(true)} disabled={loading || executing || control.running || control.paused || enrolledCount === 0}>{executing || control.running ? 'Running…' : 'Run updates now'}</Button>
				<NotificationChannels/>
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
				<Paper variant="outlined" sx={{p: 1.75, flex: 1}}>
					<Typography color="text.secondary" variant="body2">Newer version tags</Typography>
					<Typography variant="h4" color={newerVersionCount > 0 ? 'info.main' : 'inherit'}>{newerVersionCount}</Typography>
				</Paper>
                <Paper variant="outlined" sx={{p: 1.75, flex: 2}}>
                    <Stack direction="row" spacing={1} sx={{alignItems: 'center'}}><ShieldOutlined color="success"/><Typography sx={{fontWeight: 600}}>Protected automatic updates</Typography></Stack>
					<Typography variant="body2" color="text.secondary">Scheduled checks apply available images only to enrolled targets. Container rules stay isolated; stack rules preload every image, follow dependencies and roll the whole changed group back on failure.</Typography>
                </Paper>
            </Stack>

            <Alert severity="info" sx={{mb: 2}}>
                Labels have priority over graphical rules. Use <code>dockman.update=true</code>, optionally <code>dockman.update.schedule</code>, <code>dockman.update.rollback</code>, <code>dockman.update.cleanup</code>, <code>dockman.update.version</code> and <code>dockman.update.version.prerelease</code>. <code>dockman.update.disable=true</code> always blocks enrollment. Version discovery is informational and never rewrites Compose.
            </Alert>

            <Paper variant="outlined">
                <Box sx={{p: 1.5, display: 'flex', gap: 1.5, alignItems: 'center'}}>
                    <TextField size="small" label="Search containers, images or stacks" value={search} onChange={event => setSearch(event.target.value)} sx={{width: {xs: '100%', sm: 420}}}/>
                    <Typography variant="body2" color="text.secondary">{visibleRows.length} result{visibleRows.length === 1 ? '' : 's'}</Typography>
					{selected.size > 0 && <><Chip size="small" color="primary" label={`${selected.size} selected`}/><Button size="small" variant="contained" startIcon={<EditOutlined/>} onClick={openBulkPolicy}>Edit policies</Button><Button size="small" onClick={() => setSelected(new Set())}>Clear</Button></>}
                </Box>
                <TableContainer sx={{maxHeight: 'calc(100vh - 390px)', minHeight: 280}}>
                    <Table stickyHeader size="small">
                        <TableHead><TableRow>
							<TableCell padding="checkbox"><Checkbox size="small" checked={allVisibleSelected} indeterminate={!allVisibleSelected && someVisibleSelected} onChange={toggleVisibleSelection} disabled={selectableVisibleRows.length === 0}/></TableCell>
                            <TableCell>Container</TableCell><TableCell>Stack</TableCell><TableCell>Image</TableCell>
							<TableCell>Policy</TableCell><TableCell>Schedule</TableCell><TableCell>Latest scan</TableCell><TableCell align="right">Action</TableCell>
                        </TableRow></TableHead>
                        <TableBody>
                            {visibleRows.map(row => <TableRow key={row.containerId} hover>
								<TableCell padding="checkbox"><Checkbox size="small" checked={selected.has(row.containerId)} disabled={row.source === 'label' || row.source === 'disabled-label' || row.source === 'protected'} onChange={() => setSelected(current => { const next = new Set(current); if (next.has(row.containerId)) next.delete(row.containerId); else next.add(row.containerId); return next; })}/></TableCell>
                                <TableCell><Stack direction="row" spacing={1} sx={{alignItems: 'center'}}>
                                    {row.enrolled && <CheckCircleOutlined color="success" fontSize="small"/>}
                                    <Box><Typography sx={{fontWeight: 600}}>{row.containerName}</Typography><Typography variant="caption" color="text.secondary">{row.state}</Typography></Box>
                                </Stack></TableCell>
                                <TableCell>{row.stackName || <Typography color="text.secondary">Standalone</Typography>}</TableCell>
                                <TableCell sx={{fontFamily: 'monospace', maxWidth: 360, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>{row.image}</TableCell>
                                <TableCell><Tooltip title={row.scheduleError || row.reason || ''}><Chip size="small" label={row.scheduleError ? 'Invalid schedule' : sourceLabels[row.source]} color={row.scheduleError ? 'error' : row.enrolled ? 'success' : row.source === 'disabled-label' ? 'warning' : 'default'} variant="outlined"/></Tooltip></TableCell>
								<TableCell sx={{fontFamily: 'monospace'}}>{row.schedule || (row.enrolled ? '0 4 * * *' : '—')}</TableCell>
								<TableCell>{blocksByContainer[row.containerId] ? <Tooltip title={blocksByContainer[row.containerId].reason}><Chip size="small" icon={<ErrorOutlined/>} label="retry blocked" color="error" variant="outlined"/></Tooltip> : row.enrolled && scanResults[row.containerId]?.image === row.image ? <Stack spacing={.5} sx={{alignItems: 'flex-start'}}><Tooltip title={scanResults[row.containerId].reason ?? new Date(scanResults[row.containerId].checkedAt).toLocaleString()}>
					<Chip size="small" label={scanResults[row.containerId].status} color={scanResults[row.containerId].status === 'available' ? 'warning' : scanResults[row.containerId].status === 'error' ? 'error' : scanResults[row.containerId].status === 'current' ? 'success' : 'default'} variant="outlined"/>
				</Tooltip>{scanResults[row.containerId].versionAvailable && <Tooltip title={scanResults[row.containerId].versionReason ?? ''}><Chip size="small" color="info" label={`${scanResults[row.containerId].currentTag} → ${scanResults[row.containerId].latestTag}`}/></Tooltip>}</Stack> : '—'}</TableCell>
				<TableCell align="right"><Stack direction="row" spacing={.5} sx={{justifyContent: 'flex-end'}}>{blocksByContainer[row.containerId] && <Tooltip title={blocksByContainer[row.containerId].targetType === 'stack' ? 'Allow one retry of this digest for the whole stack transaction' : 'Allow one retry of this same image digest'}><Button size="small" color="warning" startIcon={<ReplayOutlined/>} onClick={() => void allowRetry(row.containerId)}>Retry</Button></Tooltip>}{host === 'local' && row.stackKey && row.source !== 'protected' && <Tooltip title="Update this sensitive Compose service through a detached helper with health verification and rollback"><Button size="small" color="warning" startIcon={<ShieldOutlined/>} onClick={() => setProtectedTarget(row)}>Protected update</Button></Tooltip>}<Button size="small" startIcon={<EditOutlined/>} disabled={row.source === 'label' || row.source === 'disabled-label' || row.source === 'protected'} onClick={() => openPolicy(row)}>Configure</Button></Stack></TableCell>
                            </TableRow>)}
							{!loading && visibleRows.length === 0 && <TableRow><TableCell colSpan={8} align="center" sx={{py: 6, color: 'text.secondary'}}>No container matches this view.</TableCell></TableRow>}
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
							<Typography variant="body2">{run.targets} checked · {run.available} digest update · {run.versions ?? 0} newer tag · {run.errors} errors</Typography>
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

			<Paper variant="outlined" sx={{mt: 2, p: 2}}>
				<Stack direction={{xs: 'column', md: 'row'}} spacing={1} sx={{alignItems: {md: 'center'}, mb: 1}}><DeleteSweepOutlined/><Typography variant="h6">Previous image cleanup</Typography><Chip size="small" variant="outlined" color={cleanups.some(item => item.status === 'pending') ? 'warning' : 'default'} label={`${cleanups.filter(item => item.status === 'pending').length} retained`}/><Box sx={{flex: 1}}/><Button size="small" startIcon={<ReplayOutlined/>} onClick={() => void retryImageCleanup()} disabled={cleanupRetrying || !cleanups.some(item => item.status === 'pending')}>{cleanupRetrying ? 'Checking…' : 'Retry retained'}</Button></Stack>
				{cleanups.length === 0 ? <Typography color="text.secondary">Cleanup is opt-in. No previous image has been registered for safe removal.</Typography> : cleanups.slice(0, 12).map(item => <Stack key={item.id} direction={{xs: 'column', md: 'row'}} spacing={1} sx={{alignItems: {md: 'center'}, py: .65, borderBottom: 1, borderColor: 'divider'}}>
					<Chip size="small" color={item.status === 'removed' ? 'success' : 'warning'} variant="outlined" label={item.status}/><Typography variant="body2" sx={{minWidth: 170}}>{item.containerName}</Typography><Typography variant="caption" sx={{fontFamily: 'monospace', flex: 1, overflowWrap: 'anywhere'}}>{item.imageId}</Typography><Tooltip title={item.reason ?? ''}><Typography variant="caption" color="text.secondary" sx={{maxWidth: 420}}>{item.reason || '—'}</Typography></Tooltip>
				</Stack>)}
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

			<Dialog open={bulkOpen} onClose={() => !saving && setBulkOpen(false)} fullWidth maxWidth="sm">
				<DialogTitle>Bulk update policy — {selectedRows.length} selected container{selectedRows.length === 1 ? '' : 's'}</DialogTitle>
				<DialogContent dividers><Stack spacing={2.25} sx={{pt: .5}}>
					<Alert severity="info">Stack targets are deduplicated. All changes and obsolete per-container overrides are committed in one transaction.</Alert>
					<FormControl fullWidth><InputLabel>Apply to</InputLabel><Select label="Apply to" value={draft.targetType} onChange={event => setDraft(current => ({...current, targetType: event.target.value as TargetType}))}><MenuItem value="container">Each selected container</MenuItem><MenuItem value="stack" disabled={selectedRows.some(row => !row.stackKey)}>Each selected complete stack</MenuItem></Select></FormControl>
					<FormControlLabel control={<Switch checked={draft.enabled} onChange={event => setDraft(current => ({...current, enabled: event.target.checked}))}/>} label="Automatically install available digest updates"/>
					<TextField label="Image check schedule (optional)" placeholder="0 4 * * *" value={draft.schedule} onChange={event => setDraft(current => ({...current, schedule: event.target.value}))} helperText="Standard five-field cron, minimum 15 minutes. Empty uses daily 04:00."/>
					<FormControl fullWidth><InputLabel>Newer version tag notifications</InputLabel><Select label="Newer version tag notifications" value={draft.versionPolicy} onChange={event => setDraft(current => ({...current, versionPolicy: event.target.value as PolicyDraft['versionPolicy']}))}><MenuItem value="off">Off — digest of current tag only</MenuItem><MenuItem value="patch">Patch channel</MenuItem><MenuItem value="minor">Minor and patch</MenuItem><MenuItem value="major">Any newer stable version</MenuItem></Select></FormControl>
					<FormControlLabel control={<Switch disabled={draft.versionPolicy === 'off'} checked={draft.versionPrerelease} onChange={event => setDraft(current => ({...current, versionPrerelease: event.target.checked}))}/>} label="Include prerelease tags"/>
					<FormControlLabel control={<Switch checked={draft.rollbackEnabled} onChange={event => setDraft(current => ({...current, rollbackEnabled: event.target.checked}))}/>} label="Verify health and roll back automatically"/>
					<FormControlLabel control={<Switch checked={draft.cleanupEnabled} onChange={event => setDraft(current => ({...current, cleanupEnabled: event.target.checked}))}/>} label="Clean previous images safely after successful updates"/>
					<TextField type="number" disabled={!draft.cleanupEnabled} label="Previous rollback images to retain" value={draft.cleanupKeep} onChange={event => setDraft(current => ({...current, cleanupKeep: Math.max(0, Math.min(10, Number(event.target.value) || 0))}))} slotProps={{htmlInput: {min: 0, max: 10}}}/>
				</Stack></DialogContent>
				<DialogActions><Button onClick={() => setBulkOpen(false)} disabled={saving}>Cancel</Button><Button variant="contained" onClick={() => void saveBulkPolicy()} disabled={saving}>{saving ? 'Saving…' : 'Apply atomically'}</Button></DialogActions>
			</Dialog>

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
					<FormControl fullWidth><InputLabel>Newer version tag notifications</InputLabel><Select label="Newer version tag notifications" value={draft.versionPolicy} onChange={event => setDraft(current => ({...current, versionPolicy: event.target.value as PolicyDraft['versionPolicy']}))}><MenuItem value="off">Off — digest of current tag only</MenuItem><MenuItem value="patch">Patch channel</MenuItem><MenuItem value="minor">Minor and patch</MenuItem><MenuItem value="major">Any newer stable version</MenuItem></Select></FormControl>
					<FormControlLabel control={<Switch disabled={draft.versionPolicy === 'off'} checked={draft.versionPrerelease} onChange={event => setDraft(current => ({...current, versionPrerelease: event.target.checked}))}/>} label="Include prerelease tags"/>
					{draft.versionPolicy !== 'off' && <Alert severity="info">Dockman only lists public registry tags and notifies you. It never changes the image reference or deploys another tag automatically.</Alert>}
					<FormControlLabel control={<Switch checked={draft.rollbackEnabled} onChange={event => setDraft(current => ({...current, rollbackEnabled: event.target.checked}))}/>} label="Verify health and roll back automatically"/>
					<FormControlLabel control={<Switch checked={draft.cleanupEnabled} onChange={event => setDraft(current => ({...current, cleanupEnabled: event.target.checked}))}/>} label="Clean previous images safely after successful updates"/>
					<TextField type="number" disabled={!draft.cleanupEnabled} label="Previous rollback images to retain" value={draft.cleanupKeep} onChange={event => setDraft(current => ({...current, cleanupKeep: Math.max(0, Math.min(10, Number(event.target.value) || 0))}))} slotProps={{htmlInput: {min: 0, max: 10}}} helperText="0 removes the previous image immediately when Docker confirms it is unused and untagged. Keeping 1 is recommended."/>
					{draft.cleanupEnabled && <Alert severity="warning">Cleanup is performed only after the complete protected operation succeeds. Dockman never uses force and retains tagged or referenced images.</Alert>}
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

        </Box>
    );
}
