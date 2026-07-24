import {
    Alert, Box, Button, CircularProgress, Divider, IconButton, Popover, Stack, TextField, Tooltip, Typography,
} from '@mui/material';
import {
    CloudDownloadOutlined, CloudUploadOutlined, CompareArrowsOutlined, DeleteOutlined, LinkOffOutlined, OpenInNew,
    HistoryOutlined, PauseCircleOutlined, PlayCircleOutlined, RestoreOutlined, RocketLaunchOutlined, ScheduleOutlined, Sync,
} from '@mui/icons-material';
import {useMemo, useState} from 'react';
import {useNavigate} from 'react-router-dom';
import {withProtectedAPI} from '../lib/api.ts';
import {useSnackbar} from '../hooks/snackbar.ts';
import {gitStatusSeverity, type GitStackStatus, refreshGitStackStatuses} from './git-stack-status-store.ts';
import GitBindingRecovery from './git-binding-recovery.tsx';

async function request<T>(path: string, init?: RequestInit): Promise<T> {
    const headers = new Headers(init?.headers);
    if (init?.body) headers.set('Content-Type', 'application/json');
    const response = await fetch(withProtectedAPI(`/git${path}`), {...init, headers});
    if (!response.ok) {
        const body = await response.json().catch(() => ({error: response.statusText})) as {error?: string};
        throw new Error(body.error || `HTTP ${response.status}`);
    }
    return response.json() as Promise<T>;
}


const colors = {neutral: '#9e9e9e', info: '#64b5f6', warning: '#ffb74d', error: '#ef5350', success: '#66bb6a'};

function stateLabel(status: GitStackStatus) {
    if (status.deployState === 'failed') return 'Automatic deployment failed';
    const label = ({
        unselected: 'Not selected for Git synchronization', pending: 'Synchronization not checked yet', up_to_date: 'Synchronized', checking: 'Synchronization in progress',
        local_changes: 'Local changes waiting', locally_deleted: 'Deleted locally · still present on Git', remote_changes: 'Git changes waiting', conflict: 'Conflict requires a decision',
        orphaned: 'Deleted on Git · preserved locally', error: 'Synchronization failed',
    } as Record<string, string>)[status.state] ?? status.state;
    if (!status.automationPaused) return label;
    return status.state === 'up_to_date' || status.state === 'pending'
        ? 'Automatic synchronization paused'
        : `Automatic synchronization paused · ${label}`;
}

function dateLabel(value?: string) {
    return value ? new Date(value).toLocaleString() : '—';
}

export default function GitStackStatusIndicator({status, size = 18, interactive = true, aggregateStatuses}: {
    status?: GitStackStatus;
    size?: number;
    interactive?: boolean;
    aggregateStatuses?: GitStackStatus[];
}) {
    const [anchor, setAnchor] = useState<HTMLElement | null>(null);
    const [busy, setBusy] = useState(false);
    const [confirmPush, setConfirmPush] = useState(false);
    const [deleteGitTarget, setDeleteGitTarget] = useState<GitStackStatus | null>(null);
    const [deleteGitConfirmation, setDeleteGitConfirmation] = useState('');
    const [recoveryTab, setRecoveryTab] = useState<'activity' | 'backups' | null>(null);
    const {showError, showSuccess} = useSnackbar();
    const navigate = useNavigate();
    const severity = status ? gitStatusSeverity(status) : 'neutral';
    const color = status?.automationPaused && (severity === 'success' || severity === 'neutral') ? colors.neutral : colors[severity];
    const error = status?.deployState === 'failed' ? status.deployError : status?.error;
    const encodedComposePath = useMemo(() => status?.composePath.split('/').map(encodeURIComponent).join('/') ?? '', [status?.composePath]);
    if (!status) return null;

    const icon = <>
        <Sync sx={{fontSize: size, animation: status.state === 'checking' ? 'dockman-git-spin 1.1s linear infinite' : 'none', '@keyframes dockman-git-spin': {to: {transform: 'rotate(360deg)'}}}}/>
        {interactive && !aggregateStatuses && status.selected && (status.autoDeployEnabled || status.autoSyncEnabled) && <Box sx={{position: 'absolute', right: -2, bottom: -2, width: 11, height: 11, borderRadius: '50%', bgcolor: '#121212', display: 'grid', placeItems: 'center'}}>{status.autoDeployEnabled ? <RocketLaunchOutlined sx={{fontSize: 9, color}}/> : <ScheduleOutlined sx={{fontSize: 9, color}}/>}</Box>}
    </>;

    const pause = async () => {
        setBusy(true);
        try {
            await request(`/bindings/${status.bindingId}/stack-status/${encodedComposePath}`, {method: 'PUT', body: JSON.stringify({paused: !status.automationPaused})});
            await refreshGitStackStatuses(status.host);
            showSuccess(status.automationPaused && (status.state === 'local_changes' || status.state === 'orphaned')
                ? 'Local changes pushed and automatic Git synchronization resumed.'
                : status.automationPaused ? 'Automatic Git synchronization resumed for this stack.' : 'Automatic Git synchronization paused for this stack.');
        } catch (reason) {
            showError((reason as Error).message);
        } finally { setBusy(false); }
    };

    const checkNow = async () => {
        setBusy(true);
        try {
            await request(`/bindings/${status.bindingId}/automation/run`, {method: 'POST'});
            await refreshGitStackStatuses(status.host);
            showSuccess('Git synchronization check completed.');
        } catch (reason) {
            showError((reason as Error).message);
            await refreshGitStackStatuses(status.host);
        } finally { setBusy(false); }
    };

    const enableSynchronization = async (target = status, push = false) => {
        setBusy(true);
        try {
            const encoded = target.composePath.split('/').map(encodeURIComponent).join('/');
            await request(`/bindings/${target.bindingId}/stack-select/${encoded}`, {method: 'POST'});
            if (push) {
                await request(`/bindings/${target.bindingId}/stack-push/${encoded}`, {method: 'POST'});
            }
            await refreshGitStackStatuses(target.host);
            showSuccess(push ? 'Stack selected and pushed to Git.' : 'Stack selected for Git synchronization. No file was pushed or deployed.');
            closePopover();
        } catch (reason) {
            showError((reason as Error).message);
            await refreshGitStackStatuses(target.host);
        } finally { setBusy(false); }
    };

    const resolveLocalDeletion = async (target: GitStackStatus, action: 'restore' | 'delete_git' | 'deselect') => {
        setBusy(true);
        try {
            const encoded = target.composePath.split('/').map(encodeURIComponent).join('/');
            const result = await request<{message: string}>(`/bindings/${target.bindingId}/local-deletion/${encoded}`, {
                method: 'POST', body: JSON.stringify({action, confirmation: action === 'delete_git' ? deleteGitConfirmation : ''}),
            });
            await refreshGitStackStatuses(target.host);
            showSuccess(result.message);
            setDeleteGitTarget(null);
            setDeleteGitConfirmation('');
            if (!aggregateStatuses) closePopover();
        } catch (reason) {
            showError((reason as Error).message);
            await refreshGitStackStatuses(target.host);
        } finally { setBusy(false); }
    };

    const openRelevantGitView = (target = status) => {
        const action = target.state === 'conflict' ? 'conflicts'
            : target.state === 'error' || target.deployState === 'failed' ? 'details'
                : target.state === 'remote_changes' || target.state === 'orphaned' ? 'preview_git' : 'preview_stack';
        navigate(`/settings?tab=2&gitBinding=${encodeURIComponent(target.bindingId)}&gitAction=${action}&gitCompose=${encodeURIComponent(target.composePath)}`);
    };

    const closePopover = () => {
        setAnchor(null);
        setConfirmPush(false);
        setDeleteGitTarget(null);
        setDeleteGitConfirmation('');
    };

    const pushStack = async (target = status) => {
        setBusy(true);
        try {
            const encoded = target.composePath.split('/').map(encodeURIComponent).join('/');
            const result = await request<{message: string}>(`/bindings/${target.bindingId}/stack-push/${encoded}`, {method: 'POST'});
            await refreshGitStackStatuses(target.host);
            showSuccess(result.message);
            closePopover();
        } catch (reason) {
            showError((reason as Error).message);
            await refreshGitStackStatuses(target.host);
        } finally {
            setBusy(false);
            setConfirmPush(false);
        }
    };

    return <>
        <Tooltip title={interactive ? `${stateLabel(status)} · ${status.repositoryName}` : `${stateLabel(status)} · Open this folder to inspect its stacks`} arrow>
            {interactive
                ? <IconButton size="small" onClick={(event) => { event.preventDefault(); event.stopPropagation(); setAnchor(event.currentTarget); }} sx={{position: 'relative', color, p: 0.25}} aria-label={`Git status: ${stateLabel(status)}`}>{icon}</IconButton>
                : <Box component="span" role="img" aria-label={`Aggregated Git status: ${stateLabel(status)}`} sx={{position: 'relative', color, p: 0.25, display: 'inline-flex', alignItems: 'center'}}>{icon}</Box>}
        </Tooltip>
        <Popover
            open={interactive && Boolean(anchor)}
            anchorEl={anchor}
            onClose={closePopover}
            // Popovers render in a portal but React events still bubble through
            // the file row that owns this component. Stop every interaction at
            // the portal boundary so clicking its content/backdrop cannot open
            // the Compose file or reset its selected editor tab.
            onClick={(event) => event.stopPropagation()}
            onMouseDown={(event) => event.stopPropagation()}
            onPointerDown={(event) => event.stopPropagation()}
            onKeyDown={(event) => event.stopPropagation()}
            anchorOrigin={{vertical: 'bottom', horizontal: 'left'}}
            transformOrigin={{vertical: 'top', horizontal: 'left'}}
            slotProps={{paper: {sx: {width: 390, maxWidth: 'calc(100vw - 24px)', bgcolor: '#17191c', border: '1px solid rgba(255,255,255,.14)', borderRadius: 2}}}}
        >
            {aggregateStatuses && aggregateStatuses.length > 0 ? <Stack spacing={1.25} sx={{p: 1.75, userSelect: 'text'}}>
                <Stack direction="row" spacing={1} sx={{alignItems: 'center'}}><Sync sx={{color}}/><Box><Typography sx={{fontWeight: 700}}>Linked folder synchronization</Typography><Typography variant="caption" color="text.secondary">{aggregateStatuses.length} stack{aggregateStatuses.length === 1 ? '' : 's'} in this folder</Typography></Box></Stack>
                <Alert severity={severity === 'error' ? 'error' : severity === 'warning' ? 'warning' : 'info'}>This is an aggregate indicator. Resolve each affected stack below; no action is applied blindly to the whole folder.</Alert>
                <Divider/>
                <Stack spacing={1} sx={{maxHeight: 420, overflowY: 'auto', pr: .5}}>
                    {aggregateStatuses.map((target) => <Box key={`${target.bindingId}:${target.composePath}`} sx={{p: 1.1, border: '1px solid rgba(255,255,255,.1)', borderRadius: 1.5, bgcolor: 'rgba(255,255,255,.025)'}}>
                        <Stack direction="row" sx={{alignItems: 'flex-start', justifyContent: 'space-between', gap: 1}}><Box sx={{minWidth: 0}}><Typography variant="body2" sx={{fontFamily: 'monospace', overflowWrap: 'anywhere'}}>{target.composePath}</Typography><Typography variant="caption" sx={{color: colors[gitStatusSeverity(target)]}}>{stateLabel(target)}</Typography></Box></Stack>
                        <Stack direction="row" spacing={.75} sx={{mt: .8, flexWrap: 'wrap'}}>
                            {target.state === 'locally_deleted' && <>
                                <Button size="small" color="success" startIcon={<CloudDownloadOutlined/>} disabled={busy} onClick={() => void resolveLocalDeletion(target, 'restore')}>Restore from Git</Button>
                                <Button size="small" startIcon={<LinkOffOutlined/>} disabled={busy} onClick={() => void resolveLocalDeletion(target, 'deselect')}>Stop synchronizing</Button>
                                <Button size="small" color="error" startIcon={<DeleteOutlined/>} disabled={busy} onClick={() => { setDeleteGitTarget(target); setDeleteGitConfirmation(''); }}>Delete from Git</Button>
                            </>}
                            {!target.selected && <><Button size="small" color="success" startIcon={<Sync/>} disabled={busy} onClick={() => void enableSynchronization(target, false)}>Enable</Button><Button size="small" color="success" variant="contained" startIcon={<CloudUploadOutlined/>} disabled={busy} onClick={() => void enableSynchronization(target, true)}>Enable & push</Button></>}
                            {target.state === 'local_changes' && <Button size="small" color="success" startIcon={<CloudUploadOutlined/>} disabled={busy} onClick={() => void pushStack(target)}>Push to Git</Button>}
                            {target.selected && target.state !== 'up_to_date' && target.state !== 'locally_deleted' && target.state !== 'local_changes' && <Button size="small" startIcon={<CompareArrowsOutlined/>} onClick={() => openRelevantGitView(target)}>Open details</Button>}
                        </Stack>
                        {deleteGitTarget?.bindingId === target.bindingId && deleteGitTarget.composePath === target.composePath && <Alert severity="error" sx={{mt: 1}}><Stack spacing={1}><Typography variant="caption">This commits the stack deletion to Git. Type <strong>DELETE STACK FROM GIT</strong> to confirm.</Typography><TextField size="small" value={deleteGitConfirmation} onChange={(event) => setDeleteGitConfirmation(event.target.value)} slotProps={{htmlInput: {autoComplete: 'off'}}}/><Stack direction="row" spacing={1}><Button size="small" onClick={() => { setDeleteGitTarget(null); setDeleteGitConfirmation(''); }}>Cancel</Button><Button size="small" color="error" variant="contained" disabled={busy || deleteGitConfirmation !== 'DELETE STACK FROM GIT'} onClick={() => void resolveLocalDeletion(target, 'delete_git')}>Confirm deletion</Button></Stack></Stack></Alert>}
                    </Box>)}
                </Stack>
            </Stack> :
            <Stack spacing={1.25} sx={{p: 1.75, userSelect: 'text'}}>
                <Stack direction="row" spacing={1} sx={{alignItems: 'center'}}><Sync sx={{color}}/><Box sx={{minWidth: 0}}><Typography noWrap sx={{fontWeight: 700}}>{status.fullComposePath}</Typography><Typography variant="caption" color="text.secondary">{status.repositoryName} · {status.repositoryBranch}</Typography></Box></Stack>
                <Divider/>
                <Stack direction="row" sx={{justifyContent: 'space-between', gap: 2}}><Typography variant="body2" color="text.secondary">State</Typography><Typography variant="body2" sx={{color, fontWeight: 650, textAlign: 'right'}}>{stateLabel(status)}</Typography></Stack>
                <Stack direction="row" sx={{justifyContent: 'space-between', gap: 2}}><Typography variant="body2" color="text.secondary">Git path</Typography><Typography variant="body2" sx={{fontFamily: 'monospace', overflowWrap: 'anywhere', textAlign: 'right'}}>{status.repositorySubPath}</Typography></Stack>
                {!status.selected && <Alert severity="info">This stack is inside a linked folder but is not synchronized yet. Enabling it only adds it to the folder-link selection; it does not push, deploy, or restart anything.</Alert>}
                {status.selected && <>
                    <Stack direction="row" sx={{justifyContent: 'space-between', gap: 2}}><Typography variant="body2" color="text.secondary">Last check</Typography><Typography variant="body2">{dateLabel(status.lastCheckedAt)}</Typography></Stack>
                    <Stack direction="row" sx={{justifyContent: 'space-between', gap: 2}}><Typography variant="body2" color="text.secondary">Last success</Typography><Typography variant="body2">{dateLabel(status.lastSuccessAt)}</Typography></Stack>
                    {status.lastCommit && <Stack direction="row" sx={{justifyContent: 'space-between', gap: 2}}><Typography variant="body2" color="text.secondary">Commit</Typography><Typography variant="body2" sx={{fontFamily: 'monospace'}}>{status.lastCommit.slice(0, 12)}</Typography></Stack>}
                    <Stack direction="row" sx={{justifyContent: 'space-between', gap: 2}}><Typography variant="body2" color="text.secondary">Automation</Typography><Typography variant="body2">{!status.autoSyncEnabled ? 'Manual' : status.automationPaused ? status.pauseReason === 'recovery' ? 'Paused after recovery' : 'Paused manually' : `Every ${status.autoSyncIntervalMinutes} min`}</Typography></Stack>
                    {status.autoSyncEnabled && !status.automationPaused && <Stack direction="row" sx={{justifyContent: 'space-between', gap: 2}}><Typography variant="body2" color="text.secondary">Next check</Typography><Typography variant="body2">{dateLabel(status.nextCheckAt)}</Typography></Stack>}
                    <Stack direction="row" sx={{justifyContent: 'space-between', gap: 2}}><Typography variant="body2" color="text.secondary">Auto-deploy</Typography><Typography variant="body2">{status.autoDeployEnabled ? status.deployState.replaceAll('_', ' ') : 'Off'}</Typography></Stack>
                </>}
                {status.conflictCount > 0 && <Alert severity="error">{status.conflictCount} conflict{status.conflictCount === 1 ? '' : 's'} require a manual decision.</Alert>}
                {status.state === 'local_changes' && <Alert severity="warning">Dockman contains changes that are not on Git yet. Review them, then commit and push. Automatic Git → Dockman synchronization never pushes local changes by itself.</Alert>}
                {status.state === 'locally_deleted' && <Alert severity="warning">This synchronized stack was deleted locally but still exists on Git. Automatic synchronization is blocked until you restore it, delete it explicitly from Git, or stop synchronizing it.</Alert>}
                {status.state === 'orphaned' && <Alert severity="warning">This stack disappeared completely from Git and was preserved locally. Restore it to Git here, or open the detailed view to archive or explicitly remove the local folder after backup.</Alert>}
                {deleteGitTarget && <Alert severity="error"><Stack spacing={1}><Typography variant="caption">Type <strong>DELETE STACK FROM GIT</strong> to confirm the committed Git deletion.</Typography><TextField size="small" value={deleteGitConfirmation} onChange={(event) => setDeleteGitConfirmation(event.target.value)} slotProps={{htmlInput: {autoComplete: 'off'}}}/><Stack direction="row" spacing={1}><Button size="small" onClick={() => { setDeleteGitTarget(null); setDeleteGitConfirmation(''); }}>Cancel</Button><Button size="small" color="error" variant="contained" disabled={busy || deleteGitConfirmation !== 'DELETE STACK FROM GIT'} onClick={() => void resolveLocalDeletion(status, 'delete_git')}>Confirm deletion</Button></Stack></Stack></Alert>}
                {confirmPush && <Alert severity="warning" action={<Stack direction="row" spacing={.5}><Button size="small" color="inherit" disabled={busy} onClick={() => setConfirmPush(false)}>Cancel</Button><Button size="small" color="warning" variant="contained" disabled={busy} onClick={() => void pushStack()}>{busy ? <CircularProgress size={14}/> : status.state === 'orphaned' ? 'Confirm restore' : 'Confirm push'}</Button></Stack>}>{status.state === 'orphaned' ? 'Restore every transferable file belonging to this stack back to Git?' : "Push every transferable local change belonging to this stack with Dockman's default commit message?"}</Alert>}
                {error && <Alert severity="error" sx={{whiteSpace: 'pre-wrap', overflowWrap: 'anywhere'}}>{error}</Alert>}
                <Divider/>
                <Stack direction="row" spacing={1} sx={{flexWrap: 'wrap'}}>
                    {!status.selected && <><Button size="small" color="success" startIcon={busy ? <CircularProgress size={14}/> : <Sync/>} disabled={busy} onClick={() => void enableSynchronization(status, false)}>Enable only</Button><Button size="small" color="success" variant="contained" startIcon={<CloudUploadOutlined/>} disabled={busy} onClick={() => void enableSynchronization(status, true)}>Enable & push</Button></>}
                    {status.state === 'locally_deleted' && <><Button size="small" color="success" startIcon={<CloudDownloadOutlined/>} disabled={busy} onClick={() => void resolveLocalDeletion(status, 'restore')}>Restore from Git</Button><Button size="small" startIcon={<LinkOffOutlined/>} disabled={busy} onClick={() => void resolveLocalDeletion(status, 'deselect')}>Stop synchronizing</Button><Button size="small" color="error" startIcon={<DeleteOutlined/>} disabled={busy || Boolean(deleteGitTarget)} onClick={() => setDeleteGitTarget(status)}>Delete from Git</Button></>}
                    {status.selected && status.autoSyncEnabled && !status.automationPaused && <Button size="small" startIcon={busy ? <CircularProgress size={14}/> : <Sync/>} disabled={busy} onClick={() => void checkNow()}>Check now</Button>}
                    {(status.state === 'local_changes' || status.state === 'orphaned') && <Button size="small" color="success" variant="contained" startIcon={<CloudUploadOutlined/>} disabled={busy || confirmPush} onClick={() => setConfirmPush(true)}>{status.state === 'orphaned' ? (status.pauseReason === 'recovery' ? 'Restore & resume' : 'Restore to Git') : (status.pauseReason === 'recovery' ? 'Push & resume' : 'Push to Git')}</Button>}
                    {status.selected && status.state !== 'locally_deleted' && <Button size="small" startIcon={<CompareArrowsOutlined/>} onClick={() => openRelevantGitView()}>{status.state === 'conflict' ? 'Resolve conflicts' : status.state === 'error' || status.deployState === 'failed' ? 'Details' : status.state === 'local_changes' || status.state === 'orphaned' ? 'Review details' : 'Preview'}</Button>}
                    {status.autoSyncEnabled && <Button size="small" color={status.automationPaused ? 'success' : 'warning'} startIcon={status.automationPaused ? <PlayCircleOutlined/> : <PauseCircleOutlined/>} disabled={busy} onClick={() => void pause()}>{status.automationPaused && (status.state === 'local_changes' || status.state === 'orphaned') ? 'Push & resume' : status.automationPaused ? 'Resume' : 'Pause'}</Button>}
                    <Button size="small" startIcon={<HistoryOutlined/>} disabled={busy} onClick={() => { setAnchor(null); setRecoveryTab('activity'); }}>Activity</Button>
                    <Button size="small" startIcon={<RestoreOutlined/>} disabled={busy} onClick={() => { setAnchor(null); setRecoveryTab('backups'); }}>Backups</Button>
                    <Button size="small" startIcon={<OpenInNew/>} onClick={() => navigate('/settings?tab=2')}>Git settings</Button>
                </Stack>
            </Stack>}
        </Popover>
        {recoveryTab && <GitBindingRecovery binding={{id: status.bindingId, stackPath: status.stackPath, repositoryName: status.repositoryName}} initialTab={recoveryTab} onClose={() => setRecoveryTab(null)}/>}
    </>;
}
