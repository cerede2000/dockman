import {
    Alert, Box, Button, CircularProgress, Divider, IconButton, Popover, Stack, Tooltip, Typography,
} from '@mui/material';
import {
    CompareArrowsOutlined, OpenInNew, PauseCircleOutlined, PlayCircleOutlined, RocketLaunchOutlined,
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
        pending: 'Synchronization not checked yet', up_to_date: 'Synchronized', checking: 'Synchronization in progress',
        local_changes: 'Local changes waiting', remote_changes: 'Git changes waiting', conflict: 'Conflict requires a decision',
        error: 'Synchronization failed',
    } as Record<string, string>)[status.state] ?? status.state;
    if (!status.automationPaused) return label;
    return status.state === 'up_to_date' || status.state === 'pending'
        ? 'Automatic synchronization paused'
        : `Automatic synchronization paused · ${label}`;
}

function dateLabel(value?: string) {
    return value ? new Date(value).toLocaleString() : '—';
}

export default function GitStackStatusIndicator({status, size = 18}: {status?: GitStackStatus; size?: number}) {
    const [anchor, setAnchor] = useState<HTMLElement | null>(null);
    const [busy, setBusy] = useState(false);
    const {showError, showSuccess} = useSnackbar();
    const navigate = useNavigate();
    const severity = status ? gitStatusSeverity(status) : 'neutral';
    const color = status?.automationPaused && (severity === 'success' || severity === 'neutral') ? colors.neutral : colors[severity];
    const error = status?.deployState === 'failed' ? status.deployError : status?.error;
    const encodedComposePath = useMemo(() => status?.composePath.split('/').map(encodeURIComponent).join('/') ?? '', [status?.composePath]);
    if (!status) return null;

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

    const openRelevantGitView = () => {
        const action = status.state === 'conflict' ? 'conflicts'
            : status.state === 'error' || status.deployState === 'failed' ? 'details'
                : status.state === 'remote_changes' ? 'preview_git' : 'preview_stack';
        navigate(`/settings?tab=2&gitBinding=${encodeURIComponent(status.bindingId)}&gitAction=${action}&gitCompose=${encodeURIComponent(status.composePath)}`);
    };

    return <>
        <Tooltip title={`${stateLabel(status)} · ${status.repositoryName}`} arrow>
            <IconButton size="small" onClick={(event) => { event.preventDefault(); event.stopPropagation(); setAnchor(event.currentTarget); }} sx={{position: 'relative', color, p: 0.25}} aria-label={`Git status: ${stateLabel(status)}`}>
                <Sync sx={{fontSize: size, animation: status.state === 'checking' ? 'dockman-git-spin 1.1s linear infinite' : 'none', '@keyframes dockman-git-spin': {to: {transform: 'rotate(360deg)'}}}}/>
                {(status.autoDeployEnabled || status.autoSyncEnabled) && <Box sx={{position: 'absolute', right: -2, bottom: -2, width: 11, height: 11, borderRadius: '50%', bgcolor: '#121212', display: 'grid', placeItems: 'center'}}>{status.autoDeployEnabled ? <RocketLaunchOutlined sx={{fontSize: 9, color}}/> : <ScheduleOutlined sx={{fontSize: 9, color}}/>}</Box>}
            </IconButton>
        </Tooltip>
        <Popover open={Boolean(anchor)} anchorEl={anchor} onClose={() => setAnchor(null)} anchorOrigin={{vertical: 'bottom', horizontal: 'left'}} transformOrigin={{vertical: 'top', horizontal: 'left'}} slotProps={{paper: {sx: {width: 390, maxWidth: 'calc(100vw - 24px)', bgcolor: '#17191c', border: '1px solid rgba(255,255,255,.14)', borderRadius: 2}}}}>
            <Stack spacing={1.25} sx={{p: 1.75, userSelect: 'text'}}>
                <Stack direction="row" spacing={1} sx={{alignItems: 'center'}}><Sync sx={{color}}/><Box sx={{minWidth: 0}}><Typography noWrap sx={{fontWeight: 700}}>{status.fullComposePath}</Typography><Typography variant="caption" color="text.secondary">{status.repositoryName} · {status.repositoryBranch}</Typography></Box></Stack>
                <Divider/>
                <Stack direction="row" sx={{justifyContent: 'space-between', gap: 2}}><Typography variant="body2" color="text.secondary">State</Typography><Typography variant="body2" sx={{color, fontWeight: 650, textAlign: 'right'}}>{stateLabel(status)}</Typography></Stack>
                <Stack direction="row" sx={{justifyContent: 'space-between', gap: 2}}><Typography variant="body2" color="text.secondary">Git path</Typography><Typography variant="body2" sx={{fontFamily: 'monospace', overflowWrap: 'anywhere', textAlign: 'right'}}>{status.repositorySubPath}</Typography></Stack>
                <Stack direction="row" sx={{justifyContent: 'space-between', gap: 2}}><Typography variant="body2" color="text.secondary">Last check</Typography><Typography variant="body2">{dateLabel(status.lastCheckedAt)}</Typography></Stack>
                <Stack direction="row" sx={{justifyContent: 'space-between', gap: 2}}><Typography variant="body2" color="text.secondary">Last success</Typography><Typography variant="body2">{dateLabel(status.lastSuccessAt)}</Typography></Stack>
                {status.lastCommit && <Stack direction="row" sx={{justifyContent: 'space-between', gap: 2}}><Typography variant="body2" color="text.secondary">Commit</Typography><Typography variant="body2" sx={{fontFamily: 'monospace'}}>{status.lastCommit.slice(0, 12)}</Typography></Stack>}
                <Stack direction="row" sx={{justifyContent: 'space-between', gap: 2}}><Typography variant="body2" color="text.secondary">Automation</Typography><Typography variant="body2">{!status.autoSyncEnabled ? 'Manual' : status.automationPaused ? 'Paused for this stack' : `Every ${status.autoSyncIntervalMinutes} min`}</Typography></Stack>
                {status.autoSyncEnabled && !status.automationPaused && <Stack direction="row" sx={{justifyContent: 'space-between', gap: 2}}><Typography variant="body2" color="text.secondary">Next check</Typography><Typography variant="body2">{dateLabel(status.nextCheckAt)}</Typography></Stack>}
                <Stack direction="row" sx={{justifyContent: 'space-between', gap: 2}}><Typography variant="body2" color="text.secondary">Auto-deploy</Typography><Typography variant="body2">{status.autoDeployEnabled ? status.deployState.replaceAll('_', ' ') : 'Off'}</Typography></Stack>
                {status.conflictCount > 0 && <Alert severity="error">{status.conflictCount} conflict{status.conflictCount === 1 ? '' : 's'} require a manual decision.</Alert>}
                {error && <Alert severity="error" sx={{whiteSpace: 'pre-wrap', overflowWrap: 'anywhere'}}>{error}</Alert>}
                <Divider/>
                <Stack direction="row" spacing={1} sx={{flexWrap: 'wrap'}}>
                    {status.autoSyncEnabled && !status.automationPaused && <Button size="small" startIcon={busy ? <CircularProgress size={14}/> : <Sync/>} disabled={busy} onClick={() => void checkNow()}>Check now</Button>}
                    <Button size="small" startIcon={<CompareArrowsOutlined/>} onClick={openRelevantGitView}>{status.state === 'conflict' ? 'Resolve conflicts' : status.state === 'error' || status.deployState === 'failed' ? 'Details' : 'Preview'}</Button>
                    {status.autoSyncEnabled && <Button size="small" color={status.automationPaused ? 'success' : 'warning'} startIcon={status.automationPaused ? <PlayCircleOutlined/> : <PauseCircleOutlined/>} disabled={busy} onClick={() => void pause()}>{status.automationPaused ? 'Resume' : 'Pause'}</Button>}
                    <Button size="small" startIcon={<OpenInNew/>} onClick={() => navigate('/settings?tab=2')}>Git settings</Button>
                </Stack>
            </Stack>
        </Popover>
    </>;
}
