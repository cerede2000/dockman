import {
    Alert, Box, Button, CircularProgress, Divider, IconButton, Popover, Stack, Tooltip, Typography,
} from '@mui/material';
import {
    CloudUploadOutlined, CompareArrowsOutlined, OpenInNew, PauseCircleOutlined, PlayCircleOutlined, RocketLaunchOutlined,
    ScheduleOutlined, Sync,
} from '@mui/icons-material';
import {useMemo, useState} from 'react';
import {useNavigate} from 'react-router-dom';
import {withProtectedAPI} from '../lib/api.ts';
import {useSnackbar} from '../hooks/snackbar.ts';
import {gitStatusSeverity, type GitStackStatus, refreshGitStackStatuses} from './git-stack-status-store.ts';

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
        local_changes: 'Local changes waiting', remote_changes: 'Git changes waiting', conflict: 'Conflict requires a decision',
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

export default function GitStackStatusIndicator({status, size = 18, interactive = true}: {
    status?: GitStackStatus;
    size?: number;
    interactive?: boolean;
}) {
    const [anchor, setAnchor] = useState<HTMLElement | null>(null);
    const [busy, setBusy] = useState(false);
    const [confirmPush, setConfirmPush] = useState(false);
    const {showError, showSuccess} = useSnackbar();
    const navigate = useNavigate();
    const severity = status ? gitStatusSeverity(status) : 'neutral';
    const color = status?.automationPaused && (severity === 'success' || severity === 'neutral') ? colors.neutral : colors[severity];
    const error = status?.deployState === 'failed' ? status.deployError : status?.error;
    const encodedComposePath = useMemo(() => status?.composePath.split('/').map(encodeURIComponent).join('/') ?? '', [status?.composePath]);
    if (!status) return null;

    const icon = <>
        <Sync sx={{fontSize: size, animation: status.state === 'checking' ? 'dockman-git-spin 1.1s linear infinite' : 'none', '@keyframes dockman-git-spin': {to: {transform: 'rotate(360deg)'}}}}/>
        {interactive && status.selected && (status.autoDeployEnabled || status.autoSyncEnabled) && <Box sx={{position: 'absolute', right: -2, bottom: -2, width: 11, height: 11, borderRadius: '50%', bgcolor: '#121212', display: 'grid', placeItems: 'center'}}>{status.autoDeployEnabled ? <RocketLaunchOutlined sx={{fontSize: 9, color}}/> : <ScheduleOutlined sx={{fontSize: 9, color}}/>}</Box>}
    </>;

    const pause = async () => {
        setBusy(true);
        try {
            await request(`/bindings/${status.bindingId}/stack-status/${encodedComposePath}`, {method: 'PUT', body: JSON.stringify({paused: !status.automationPaused})});
            await refreshGitStackStatuses(status.host);
            showSuccess(status.automationPaused ? 'Automatic Git synchronization resumed for this stack.' : 'Automatic Git synchronization paused for this stack.');
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

    const enableSynchronization = async () => {
        setBusy(true);
        try {
            await request(`/bindings/${status.bindingId}/stack-select/${encodedComposePath}`, {method: 'POST'});
            await refreshGitStackStatuses(status.host);
            showSuccess('Stack selected for Git synchronization. No file was pushed or deployed.');
            closePopover();
        } catch (reason) {
            showError((reason as Error).message);
            await refreshGitStackStatuses(status.host);
        } finally { setBusy(false); }
    };

    const openRelevantGitView = () => {
        const action = status.state === 'conflict' ? 'conflicts'
            : status.state === 'error' || status.deployState === 'failed' ? 'details'
                : status.state === 'remote_changes' || status.state === 'orphaned' ? 'preview_git' : 'preview_stack';
        navigate(`/settings?tab=2&gitBinding=${encodeURIComponent(status.bindingId)}&gitAction=${action}&gitCompose=${encodeURIComponent(status.composePath)}`);
    };

    const closePopover = () => {
        setAnchor(null);
        setConfirmPush(false);
    };

    const pushStack = async () => {
        setBusy(true);
        try {
            const result = await request<{message: string}>(`/bindings/${status.bindingId}/stack-push/${encodedComposePath}`, {method: 'POST'});
            await refreshGitStackStatuses(status.host);
            showSuccess(result.message);
            closePopover();
        } catch (reason) {
            showError((reason as Error).message);
            await refreshGitStackStatuses(status.host);
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
                    <Stack direction="row" sx={{justifyContent: 'space-between', gap: 2}}><Typography variant="body2" color="text.secondary">Automation</Typography><Typography variant="body2">{!status.autoSyncEnabled ? 'Manual' : status.automationPaused ? 'Paused for this stack' : `Every ${status.autoSyncIntervalMinutes} min`}</Typography></Stack>
                    {status.autoSyncEnabled && !status.automationPaused && <Stack direction="row" sx={{justifyContent: 'space-between', gap: 2}}><Typography variant="body2" color="text.secondary">Next check</Typography><Typography variant="body2">{dateLabel(status.nextCheckAt)}</Typography></Stack>}
                    <Stack direction="row" sx={{justifyContent: 'space-between', gap: 2}}><Typography variant="body2" color="text.secondary">Auto-deploy</Typography><Typography variant="body2">{status.autoDeployEnabled ? status.deployState.replaceAll('_', ' ') : 'Off'}</Typography></Stack>
                </>}
                {status.conflictCount > 0 && <Alert severity="error">{status.conflictCount} conflict{status.conflictCount === 1 ? '' : 's'} require a manual decision.</Alert>}
                {status.state === 'local_changes' && <Alert severity="warning">Dockman contains changes that are not on Git yet. Review them, then commit and push. Automatic Git → Dockman synchronization never pushes local changes by itself.</Alert>}
                {status.state === 'orphaned' && <Alert severity="warning">This stack disappeared completely from Git and was preserved locally. Restore it to Git here, or open the detailed view to archive or explicitly remove the local folder after backup.</Alert>}
                {confirmPush && <Alert severity="warning" action={<Stack direction="row" spacing={.5}><Button size="small" color="inherit" disabled={busy} onClick={() => setConfirmPush(false)}>Cancel</Button><Button size="small" color="warning" variant="contained" disabled={busy} onClick={() => void pushStack()}>{busy ? <CircularProgress size={14}/> : status.state === 'orphaned' ? 'Confirm restore' : 'Confirm push'}</Button></Stack>}>{status.state === 'orphaned' ? 'Restore every transferable file belonging to this stack back to Git?' : "Push every transferable local change belonging to this stack with Dockman's default commit message?"}</Alert>}
                {error && <Alert severity="error" sx={{whiteSpace: 'pre-wrap', overflowWrap: 'anywhere'}}>{error}</Alert>}
                <Divider/>
                <Stack direction="row" spacing={1} sx={{flexWrap: 'wrap'}}>
                    {!status.selected && <Button size="small" color="success" variant="contained" startIcon={busy ? <CircularProgress size={14}/> : <Sync/>} disabled={busy} onClick={() => void enableSynchronization()}>Enable synchronization</Button>}
                    {status.selected && status.autoSyncEnabled && !status.automationPaused && <Button size="small" startIcon={busy ? <CircularProgress size={14}/> : <Sync/>} disabled={busy} onClick={() => void checkNow()}>Check now</Button>}
                    {(status.state === 'local_changes' || status.state === 'orphaned') && <Button size="small" color="success" variant="contained" startIcon={<CloudUploadOutlined/>} disabled={busy || confirmPush} onClick={() => setConfirmPush(true)}>{status.state === 'orphaned' ? 'Restore to Git' : 'Push to Git'}</Button>}
                    {status.selected && <Button size="small" startIcon={<CompareArrowsOutlined/>} onClick={openRelevantGitView}>{status.state === 'conflict' ? 'Resolve conflicts' : status.state === 'error' || status.deployState === 'failed' ? 'Details' : status.state === 'local_changes' || status.state === 'orphaned' ? 'Review details' : 'Preview'}</Button>}
                    {status.autoSyncEnabled && <Button size="small" color={status.automationPaused ? 'success' : 'warning'} startIcon={status.automationPaused ? <PlayCircleOutlined/> : <PauseCircleOutlined/>} disabled={busy} onClick={() => void pause()}>{status.automationPaused ? 'Resume' : 'Pause'}</Button>}
                    <Button size="small" startIcon={<OpenInNew/>} onClick={() => navigate('/settings?tab=2')}>Git settings</Button>
                </Stack>
            </Stack>
        </Popover>
    </>;
}
