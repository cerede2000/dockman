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
    const {showError, showSuccess} = useSnackbar();
    const [rows, setRows] = useState<UpdateEnrollment[]>([]);
    const [loading, setLoading] = useState(true);
    const [search, setSearch] = useState('');
    const [editing, setEditing] = useState<UpdateEnrollment | null>(null);
    const [saving, setSaving] = useState(false);
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
            if (editing.policyTarget && editing.policyTargetId &&
                (editing.policyTarget !== draft.targetType || editing.policyTargetId !== target.key)) {
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
                <Button startIcon={<SpaceDashboardOutlined/>} onClick={() => navigate(`/${host}/monitor`)}>Check images</Button>
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
                <Paper variant="outlined" sx={{p: 1.75, flex: 2}}>
                    <Stack direction="row" spacing={1} sx={{alignItems: 'center'}}><ShieldOutlined color="success"/><Typography sx={{fontWeight: 600}}>Opt-in foundation</Typography></Stack>
                    <Typography variant="body2" color="text.secondary">Nothing is updated automatically in Lot 1. Execution, scheduling and rollback are added in later lots.</Typography>
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
                            <TableCell>Policy</TableCell><TableCell>Schedule</TableCell><TableCell align="right">Action</TableCell>
                        </TableRow></TableHead>
                        <TableBody>
                            {visibleRows.map(row => <TableRow key={row.containerId} hover>
                                <TableCell><Stack direction="row" spacing={1} sx={{alignItems: 'center'}}>
                                    {row.enrolled && <CheckCircleOutlined color="success" fontSize="small"/>}
                                    <Box><Typography sx={{fontWeight: 600}}>{row.containerName}</Typography><Typography variant="caption" color="text.secondary">{row.state}</Typography></Box>
                                </Stack></TableCell>
                                <TableCell>{row.stackName || <Typography color="text.secondary">Standalone</Typography>}</TableCell>
                                <TableCell sx={{fontFamily: 'monospace', maxWidth: 360, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>{row.image}</TableCell>
                                <TableCell><Tooltip title={row.reason ?? ''}><Chip size="small" label={sourceLabels[row.source]} color={row.enrolled ? 'success' : row.source === 'disabled-label' ? 'warning' : 'default'} variant="outlined"/></Tooltip></TableCell>
                                <TableCell sx={{fontFamily: 'monospace'}}>{row.schedule || '—'}</TableCell>
                                <TableCell align="right"><Button size="small" startIcon={<EditOutlined/>} disabled={row.source === 'label' || row.source === 'disabled-label' || row.source === 'protected'} onClick={() => openPolicy(row)}>Configure</Button></TableCell>
                            </TableRow>)}
                            {!loading && visibleRows.length === 0 && <TableRow><TableCell colSpan={6} align="center" sx={{py: 6, color: 'text.secondary'}}>No container matches this view.</TableCell></TableRow>}
                        </TableBody>
                    </Table>
                </TableContainer>
            </Paper>

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
                    <TextField label="Schedule (optional)" placeholder="0 4 * * *" value={draft.schedule} onChange={event => setDraft(current => ({...current, schedule: event.target.value}))} helperText="Standard five-field cron expression. Stored now; execution starts in a later lot."/>
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
