import {useCallback, useEffect, useMemo, useState} from 'react';
import {
    Alert, Box, Button, Checkbox, Chip, CircularProgress, Dialog, DialogActions, DialogContent,
    DialogTitle, IconButton, Paper, Stack, Tab, Table, TableBody, TableCell, TableContainer,
    TableHead, TableRow, Tabs, Tooltip, Typography,
} from '@mui/material';
import {CloudDownloadOutlined, DeleteOutlined, RestoreOutlined} from '@mui/icons-material';
import {withProtectedAPI} from '../lib/api.ts';
import {formatBytes} from '../lib/editor.ts';
import {useSnackbar} from '../hooks/snackbar.ts';

export interface RecoveryBinding {
    id: string;
    stackPath: string;
    repositoryName: string;
}

interface Operation {
    id: string; type: string; state: string; trigger: string; composePath?: string; details?: string;
    commitSha?: string; backupId?: string; startedAt?: string; finishedAt?: string; error?: string;
}

interface Backup {
    id: string; kind: string; composePaths: string[]; commitSha?: string; fileCount: number;
    sizeBytes: number; restorable: boolean; expiresAt?: string; createdAt: string;
}

interface RestoreEntry {
    path: string; action: 'restore' | 'remove' | 'noop' | 'conflict'; beforeSha?: string;
    afterSha?: string; currentSha?: string; reason?: string;
}

interface RestorePreview {
    backup: Backup; entries: RestoreEntry[]; restorable: number; conflicts: number; token: string;
}

const dateLabel = (value?: string) => value ? new Date(value).toLocaleString() : '—';

async function api<T>(path: string, init?: RequestInit): Promise<T> {
    const response = await fetch(withProtectedAPI(`/git${path}`), {
        ...init,
        headers: {'Content-Type': 'application/json', ...(init?.headers || {})},
    });
    if (!response.ok) {
        const payload = await response.json().catch(() => ({}));
        throw new Error(payload.error || `Request failed (${response.status})`);
    }
    return response.status === 204 ? undefined as T : response.json() as Promise<T>;
}

function activitySummary(operation: Operation) {
    if (operation.error) return operation.error;
    if (!operation.details) return '—';
    try {
        const details = JSON.parse(operation.details) as {message?: string; action?: string; paths?: string[]};
        return details.message || [details.action, details.paths?.length ? `${details.paths.length} path(s)` : ''].filter(Boolean).join(' · ') || '—';
    } catch {
        return operation.details;
    }
}

export default function GitBindingRecovery({binding, initialTab, onClose}: {
    binding: RecoveryBinding;
    initialTab: 'activity' | 'backups';
    onClose: () => void;
}) {
    const {showError, showSuccess} = useSnackbar();
    const [tab, setTab] = useState(initialTab);
    const [operations, setOperations] = useState<Operation[]>([]);
    const [operationPage, setOperationPage] = useState(0);
    const [backups, setBackups] = useState<Backup[]>([]);
    const [loading, setLoading] = useState(true);
    const [busy, setBusy] = useState<string | null>(null);
    const [deleteBackup, setDeleteBackup] = useState<Backup | null>(null);
    const [restorePreview, setRestorePreview] = useState<RestorePreview | null>(null);
    const [selectedPaths, setSelectedPaths] = useState<Set<string>>(new Set());

    const load = useCallback(async () => {
        setLoading(true);
        try {
            const [nextOperations, nextBackups] = await Promise.all([
                api<Operation[]>(`/bindings/${binding.id}/operations?limit=100&offset=${operationPage * 100}`),
                api<Backup[]>(`/bindings/${binding.id}/backups?limit=100`),
            ]);
            setOperations(nextOperations);
            setBackups(nextBackups);
        } catch (error) {
            showError(error instanceof Error ? error.message : String(error));
        } finally {
            setLoading(false);
        }
    }, [binding.id, operationPage, showError]);

    useEffect(() => { void load(); }, [load]);

    const actionableEntries = useMemo(() => restorePreview?.entries.filter((entry) => entry.action === 'restore' || entry.action === 'remove') || [], [restorePreview]);

    const openRestore = async (backup: Backup) => {
        setBusy(`preview-${backup.id}`);
        try {
            const preview = await api<RestorePreview>(`/bindings/${binding.id}/backups/${backup.id}/restore-preview`);
            setRestorePreview(preview);
            setSelectedPaths(new Set(preview.entries.filter((entry) => entry.action === 'restore' || entry.action === 'remove').map((entry) => entry.path)));
        } catch (error) {
            showError(error instanceof Error ? error.message : String(error));
        } finally {
            setBusy(null);
        }
    };

    const restore = async () => {
        if (!restorePreview) return;
        setBusy(`restore-${restorePreview.backup.id}`);
        try {
            const result = await api<{message: string; safetyBackupId?: string}>(`/bindings/${binding.id}/backups/${restorePreview.backup.id}/restore`, {
                method: 'POST', body: JSON.stringify({previewToken: restorePreview.token, selectedPaths: [...selectedPaths]}),
            });
            showSuccess(`${result.message}${result.safetyBackupId ? ' A safety backup was created.' : ''}`);
            setRestorePreview(null);
            await load();
        } catch (error) {
            showError(error instanceof Error ? error.message : String(error));
        } finally {
            setBusy(null);
        }
    };

    const removeBackup = async () => {
        if (!deleteBackup) return;
        setBusy(`delete-${deleteBackup.id}`);
        try {
            await api<void>(`/bindings/${binding.id}/backups/${deleteBackup.id}`, {method: 'DELETE'});
            showSuccess('Backup deleted.');
            setDeleteBackup(null);
            await load();
        } catch (error) {
            showError(error instanceof Error ? error.message : String(error));
        } finally {
            setBusy(null);
        }
    };

    const download = (backup: Backup) => {
        window.location.assign(withProtectedAPI(`/git/bindings/${binding.id}/backups/${backup.id}/download`));
    };

    return <>
        <Dialog open onClose={() => busy === null && onClose()} fullWidth maxWidth="lg">
            <DialogTitle>Git recovery — {binding.stackPath}</DialogTitle>
            <DialogContent dividers sx={{p: 0}}>
                <Tabs value={tab} onChange={(_, value) => setTab(value)} sx={{px: 2, borderBottom: 1, borderColor: 'divider'}}>
                    <Tab value="activity" label="Activity"/>
                    <Tab value="backups" label={`Backups (${backups.length})`}/>
                </Tabs>
                {loading ? <Box sx={{display: 'grid', placeItems: 'center', minHeight: 260}}><CircularProgress/></Box> : tab === 'activity' ?
                    <Stack><TableContainer sx={{maxHeight: '56vh'}}><Table size="small" stickyHeader><TableHead><TableRow><TableCell>Date</TableCell><TableCell>Action</TableCell><TableCell>Origin</TableCell><TableCell>Stack</TableCell><TableCell>Status</TableCell><TableCell>Details</TableCell></TableRow></TableHead><TableBody>
                        {!operations.length && <TableRow><TableCell colSpan={6} align="center" sx={{py: 6, color: 'text.secondary'}}>No activity recorded for this folder link.</TableCell></TableRow>}
                        {operations.map((operation) => <TableRow key={operation.id} hover><TableCell sx={{whiteSpace: 'nowrap'}}>{dateLabel(operation.startedAt)}</TableCell><TableCell>{operation.type.replaceAll('_', ' ')}</TableCell><TableCell><Chip size="small" variant="outlined" label={operation.trigger || 'system'}/></TableCell><TableCell sx={{fontFamily: 'monospace'}}>{operation.composePath || '—'}</TableCell><TableCell><Chip size="small" color={operation.state === 'success' ? 'success' : operation.state === 'failed' ? 'error' : 'info'} variant="outlined" label={operation.state}/></TableCell><TableCell sx={{maxWidth: 420, overflowWrap: 'anywhere', userSelect: 'text'}}>{activitySummary(operation)}</TableCell></TableRow>)}
                    </TableBody></Table></TableContainer><Stack direction="row" spacing={1} sx={{p: 1.5, justifyContent: 'flex-end', alignItems: 'center', borderTop: 1, borderColor: 'divider'}}><Typography variant="caption" color="text.secondary">Page {operationPage + 1}</Typography><Button size="small" disabled={operationPage === 0} onClick={() => setOperationPage((page) => Math.max(0, page - 1))}>Previous</Button><Button size="small" disabled={operations.length < 100} onClick={() => setOperationPage((page) => page + 1)}>Next</Button></Stack></Stack> :
                    <Stack spacing={1.5} sx={{p: 2}}>
                        <Alert severity="warning">Backups can contain secrets. Archives use mode 0600 and expire automatically, but Dockman does not encrypt them: protect or encrypt the Git storage volume. Restoring never deploys or restarts a stack.</Alert>
                        <TableContainer component={Paper} variant="outlined" sx={{maxHeight: '55vh'}}><Table size="small" stickyHeader><TableHead><TableRow><TableCell>Date</TableCell><TableCell>Type</TableCell><TableCell>Stacks</TableCell><TableCell>Files</TableCell><TableCell>Size</TableCell><TableCell>Expires</TableCell><TableCell align="right">Actions</TableCell></TableRow></TableHead><TableBody>
                            {!backups.length && <TableRow><TableCell colSpan={7} align="center" sx={{py: 6, color: 'text.secondary'}}>No backup is currently retained.</TableCell></TableRow>}
                            {backups.map((backup) => <TableRow key={backup.id} hover><TableCell sx={{whiteSpace: 'nowrap'}}>{dateLabel(backup.createdAt)}</TableCell><TableCell><Chip size="small" variant="outlined" label={backup.kind.replaceAll('_', ' ')}/></TableCell><TableCell sx={{fontFamily: 'monospace', maxWidth: 260, overflowWrap: 'anywhere'}}>{backup.composePaths.join(', ') || 'folder link'}</TableCell><TableCell>{backup.fileCount}</TableCell><TableCell>{formatBytes(backup.sizeBytes)}</TableCell><TableCell>{dateLabel(backup.expiresAt)}</TableCell><TableCell align="right" sx={{whiteSpace: 'nowrap'}}>
                                {backup.restorable && <Tooltip title="Preview safe restoration"><span><IconButton size="small" disabled={busy !== null} onClick={() => void openRestore(backup)}>{busy === `preview-${backup.id}` ? <CircularProgress size={17}/> : <RestoreOutlined fontSize="small"/>}</IconButton></span></Tooltip>}
                                <Tooltip title="Download archive"><IconButton size="small" disabled={busy !== null} onClick={() => download(backup)}><CloudDownloadOutlined fontSize="small"/></IconButton></Tooltip>
                                <Tooltip title="Delete backup"><IconButton size="small" color="error" disabled={busy !== null} onClick={() => setDeleteBackup(backup)}><DeleteOutlined fontSize="small"/></IconButton></Tooltip>
                            </TableCell></TableRow>)}
                        </TableBody></Table></TableContainer>
                    </Stack>}
            </DialogContent>
            <DialogActions><Button onClick={() => void load()} disabled={loading || busy !== null}>Refresh</Button><Button onClick={onClose} disabled={busy !== null}>Close</Button></DialogActions>
        </Dialog>

        <Dialog open={restorePreview !== null} onClose={() => busy === null && setRestorePreview(null)} fullWidth maxWidth="md">
            <DialogTitle>Restore backup safely</DialogTitle><DialogContent dividers><Stack spacing={1.5}>
                <Alert severity="warning">Only files still matching the post-backup state can be restored. Conflicts are never overwritten. A new safety backup is created before applying the selected restoration.</Alert>
                {!!restorePreview?.conflicts && <Alert severity="error">{restorePreview.conflicts} file(s) changed after this backup and cannot be restored automatically.</Alert>}
                <TableContainer component={Paper} variant="outlined" sx={{maxHeight: 420}}><Table size="small" stickyHeader><TableHead><TableRow><TableCell padding="checkbox"/><TableCell>File</TableCell><TableCell>Action</TableCell><TableCell>Reason</TableCell></TableRow></TableHead><TableBody>
                    {restorePreview?.entries.map((entry) => { const selectable = entry.action === 'restore' || entry.action === 'remove'; return <TableRow key={entry.path}><TableCell padding="checkbox"><Checkbox size="small" disabled={!selectable || busy !== null} checked={selectedPaths.has(entry.path)} onChange={(_, checked) => setSelectedPaths((current) => { const next = new Set(current); if (checked) next.add(entry.path); else next.delete(entry.path); return next; })}/></TableCell><TableCell sx={{fontFamily: 'monospace', overflowWrap: 'anywhere'}}>{entry.path}</TableCell><TableCell><Chip size="small" color={entry.action === 'conflict' ? 'error' : entry.action === 'noop' ? 'default' : 'warning'} variant="outlined" label={entry.action}/></TableCell><TableCell>{entry.reason || '—'}</TableCell></TableRow>; })}
                </TableBody></Table></TableContainer>
                <Typography variant="caption" color="text.secondary">{selectedPaths.size} of {actionableEntries.length} safe action(s) selected. No Compose or Docker action will run.</Typography>
            </Stack></DialogContent><DialogActions><Button onClick={() => setRestorePreview(null)} disabled={busy !== null}>Cancel</Button><Button variant="contained" color="warning" startIcon={<RestoreOutlined/>} disabled={busy !== null || selectedPaths.size === 0} onClick={() => void restore()}>{busy?.startsWith('restore-') && <CircularProgress size={16} sx={{mr: 1}}/>}Create safety backup and restore</Button></DialogActions>
        </Dialog>

        <Dialog open={deleteBackup !== null} onClose={() => busy === null && setDeleteBackup(null)} maxWidth="xs" fullWidth><DialogTitle>Delete backup?</DialogTitle><DialogContent><Alert severity="warning">This archive will be permanently removed. Git history and stack files are not changed.</Alert></DialogContent><DialogActions><Button onClick={() => setDeleteBackup(null)} disabled={busy !== null}>Cancel</Button><Button color="error" variant="contained" disabled={busy !== null} onClick={() => void removeBackup()}>Delete</Button></DialogActions></Dialog>
    </>;
}
