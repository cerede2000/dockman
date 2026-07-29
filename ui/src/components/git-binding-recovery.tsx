import {useCallback, useEffect, useMemo, useState} from 'react';
import {DiffEditor} from '@monaco-editor/react';
import {
    Alert, Box, Button, Checkbox, Chip, CircularProgress, Dialog, DialogActions, DialogContent, FormControlLabel,
    DialogTitle, IconButton, Paper, Stack, Tab, Table, TableBody, TableCell, TableContainer,
    TableHead, TableRow, Tabs, Tooltip, Typography,
} from '@mui/material';
import {CompareArrowsOutlined, CloudDownloadOutlined, DeleteOutlined, RestoreOutlined, UndoOutlined} from '@mui/icons-material';
import {gitAPI as api, gitComparisonLanguage as comparisonLanguage, gitDateLabel as dateLabel} from '../lib/git-api.ts';
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

interface BindingCommit {
    sha: string; shortSha: string; message: string; authorName: string; authorEmail?: string; authoredAt: string;
}

interface RollbackEntry {
    path: string; composePath: string; action: 'restore' | 'remove' | 'noop' | 'skipped'; currentSha?: string;
    targetSha?: string; size?: number; sensitive?: boolean; reason?: string;
}

interface RollbackPreview {
    commit: BindingCommit; composePaths: string[]; entries: RollbackEntry[]; changed: number; restores: number;
    removals: number; skipped: number; missingComposePaths?: string[]; composeErrors?: Record<string, string>; token: string;
}

interface ComparisonSide { sha256: string; size: number; content?: string; }
interface FileComparison { path: string; dockman: ComparisonSide; git: ComparisonSide; comparable: boolean; reason?: string; }

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
    initialTab: 'activity' | 'backups' | 'commits';
    onClose: () => void;
}) {
    const {showError, showSuccess} = useSnackbar();
    const [tab, setTab] = useState(initialTab);
    const [operations, setOperations] = useState<Operation[]>([]);
    const [operationPage, setOperationPage] = useState(0);
    const [backups, setBackups] = useState<Backup[]>([]);
    const [commits, setCommits] = useState<BindingCommit[]>([]);
    const [commitsLoading, setCommitsLoading] = useState(false);
    const [loading, setLoading] = useState(true);
    const [busy, setBusy] = useState<string | null>(null);
    const [deleteBackup, setDeleteBackup] = useState<Backup | null>(null);
    const [restorePreview, setRestorePreview] = useState<RestorePreview | null>(null);
    const [selectedPaths, setSelectedPaths] = useState<Set<string>>(new Set());
    const [rollbackPreview, setRollbackPreview] = useState<RollbackPreview | null>(null);
    const [rollbackSelectedPaths, setRollbackSelectedPaths] = useState<Set<string>>(new Set());
    const [rollbackSelectedStacks, setRollbackSelectedStacks] = useState<Set<string>>(new Set());
    const [rollbackComparison, setRollbackComparison] = useState<FileComparison | null>(null);

    const load = useCallback(async () => {
        setLoading(true);
        await (async () => {
            const [nextOperations, nextBackups] = await Promise.all([
                api<Operation[]>(`/bindings/${binding.id}/operations?limit=100&offset=${operationPage * 100}`),
                api<Backup[]>(`/bindings/${binding.id}/backups?limit=100`),
            ]);
            setOperations(nextOperations);
            setBackups(nextBackups);
        })().catch((error) => {
            showError(error instanceof Error ? error.message : String(error));
        }).finally(() => setLoading(false));
    }, [binding.id, operationPage, showError]);

    useEffect(() => { void load(); }, [load]);

    const loadCommits = useCallback(async () => {
        setCommitsLoading(true);
        await (async () => {
            setCommits(await api<BindingCommit[]>(`/bindings/${binding.id}/commits?limit=50`));
        })().catch((error) => {
            showError(error instanceof Error ? error.message : String(error));
        }).finally(() => setCommitsLoading(false));
    }, [binding.id, showError]);

    useEffect(() => { if (tab === 'commits' && commits.length === 0) void loadCommits(); }, [commits.length, loadCommits, tab]);

    const actionableEntries = useMemo(() => restorePreview?.entries.filter((entry) => entry.action === 'restore' || entry.action === 'remove') || [], [restorePreview]);
    const rollbackActionableEntries = useMemo(() => rollbackPreview?.entries.filter((entry) => entry.action === 'restore' || entry.action === 'remove') || [], [rollbackPreview]);

    const openRestore = async (backup: Backup) => {
        setBusy(`preview-${backup.id}`);
        await (async () => {
            const preview = await api<RestorePreview>(`/bindings/${binding.id}/backups/${backup.id}/restore-preview`);
            setRestorePreview(preview);
            setSelectedPaths(new Set(preview.entries.filter((entry) => entry.action === 'restore' || entry.action === 'remove').map((entry) => entry.path)));
        })().catch((error) => {
            showError(error instanceof Error ? error.message : String(error));
        }).finally(() => setBusy(null));
    };

    const restore = async () => {
        if (!restorePreview) return;
        setBusy(`restore-${restorePreview.backup.id}`);
        await (async () => {
            const result = await api<{message: string; safetyBackupId?: string}>(`/bindings/${binding.id}/backups/${restorePreview.backup.id}/restore`, {
                method: 'POST', body: JSON.stringify({previewToken: restorePreview.token, selectedPaths: [...selectedPaths]}),
            });
            showSuccess(`${result.message}${result.safetyBackupId ? ' A safety backup was created.' : ''}`);
            setRestorePreview(null);
            await load();
        })().catch((error) => {
            showError(error instanceof Error ? error.message : String(error));
        }).finally(() => setBusy(null));
    };

    const openCommitRollback = async (commit: BindingCommit) => {
        setBusy(`rollback-preview-${commit.sha}`);
        await (async () => {
            const preview = await api<RollbackPreview>(`/bindings/${binding.id}/rollback-preview`, {
                method: 'POST', body: JSON.stringify({commitSha: commit.sha}),
            });
            setRollbackPreview(preview);
            setRollbackSelectedStacks(new Set(preview.composePaths));
            setRollbackSelectedPaths(new Set(preview.entries.filter((entry) => entry.action === 'restore' || entry.action === 'remove').map((entry) => entry.path)));
        })().catch((error) => {
            showError(error instanceof Error ? error.message : String(error));
        }).finally(() => setBusy(null));
    };

    const toggleRollbackStack = (composePath: string, checked: boolean) => {
        setRollbackSelectedStacks((current) => {
            const next = new Set(current);
            if (checked) next.add(composePath); else next.delete(composePath);
            return next;
        });
        setRollbackSelectedPaths((current) => {
            const next = new Set(current);
            for (const entry of rollbackActionableEntries.filter((candidate) => candidate.composePath === composePath)) {
                if (checked) next.add(entry.path); else next.delete(entry.path);
            }
            return next;
        });
    };

    const compareRollback = async (entry: RollbackEntry) => {
        if (!rollbackPreview) return;
        setBusy(`rollback-compare-${entry.path}`);
        await (async () => {
            const encoded = entry.path.split('/').map(encodeURIComponent).join('/');
            const comparison = await api<FileComparison>(`/bindings/${binding.id}/rollback-compare/${encoded}`, {
                method: 'POST', body: JSON.stringify({commitSha: rollbackPreview.commit.sha, composePaths: rollbackPreview.composePaths}),
            });
            setRollbackComparison(comparison);
        })().catch((error) => {
            showError(error instanceof Error ? error.message : String(error));
        }).finally(() => setBusy(null));
    };

    const applyCommitRollback = async () => {
        if (!rollbackPreview) return;
        setBusy(`rollback-${rollbackPreview.commit.sha}`);
        await (async () => {
            const result = await api<{message: string; safetyBackupId: string}>(`/bindings/${binding.id}/rollback`, {
                method: 'POST', body: JSON.stringify({
                    commitSha: rollbackPreview.commit.sha, composePaths: rollbackPreview.composePaths,
                    selectedPaths: [...rollbackSelectedPaths], previewToken: rollbackPreview.token,
                }),
            });
            showSuccess(`${result.message} A safety backup was created.`);
            setRollbackPreview(null);
            setRollbackComparison(null);
            await load();
        })().catch((error) => {
            showError(error instanceof Error ? error.message : String(error));
        }).finally(() => setBusy(null));
    };

    const removeBackup = async () => {
        if (!deleteBackup) return;
        setBusy(`delete-${deleteBackup.id}`);
        await (async () => {
            await api<void>(`/bindings/${binding.id}/backups/${deleteBackup.id}`, {method: 'DELETE'});
            showSuccess('Backup deleted.');
            setDeleteBackup(null);
            await load();
        })().catch((error) => {
            showError(error instanceof Error ? error.message : String(error));
        }).finally(() => setBusy(null));
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
                    <Tab value="commits" label={`Commits (${commits.length})`}/>
                </Tabs>
                {loading ? <Box sx={{display: 'grid', placeItems: 'center', minHeight: 260}}><CircularProgress/></Box> : tab === 'activity' ?
                    <Stack><TableContainer sx={{maxHeight: '56vh'}}><Table size="small" stickyHeader><TableHead><TableRow><TableCell>Date</TableCell><TableCell>Action</TableCell><TableCell>Origin</TableCell><TableCell>Stack</TableCell><TableCell>Status</TableCell><TableCell>Details</TableCell></TableRow></TableHead><TableBody>
                        {!operations.length && <TableRow><TableCell colSpan={6} align="center" sx={{py: 6, color: 'text.secondary'}}>No activity recorded for this folder link.</TableCell></TableRow>}
                        {operations.map((operation) => <TableRow key={operation.id} hover><TableCell sx={{whiteSpace: 'nowrap'}}>{dateLabel(operation.startedAt)}</TableCell><TableCell>{operation.type.replaceAll('_', ' ')}</TableCell><TableCell><Chip size="small" variant="outlined" label={operation.trigger || 'system'}/></TableCell><TableCell sx={{fontFamily: 'monospace'}}>{operation.composePath || '—'}</TableCell><TableCell><Chip size="small" color={operation.state === 'success' ? 'success' : operation.state === 'failed' ? 'error' : 'info'} variant="outlined" label={operation.state}/></TableCell><TableCell sx={{maxWidth: 420, overflowWrap: 'anywhere', userSelect: 'text'}}>{activitySummary(operation)}</TableCell></TableRow>)}
                    </TableBody></Table></TableContainer><Stack direction="row" spacing={1} sx={{p: 1.5, justifyContent: 'flex-end', alignItems: 'center', borderTop: 1, borderColor: 'divider'}}><Typography variant="caption" color="text.secondary">Page {operationPage + 1}</Typography><Button size="small" disabled={operationPage === 0} onClick={() => setOperationPage((page) => Math.max(0, page - 1))}>Previous</Button><Button size="small" disabled={operations.length < 100} onClick={() => setOperationPage((page) => page + 1)}>Next</Button></Stack></Stack> :
                    tab === 'backups' ? <Stack spacing={1.5} sx={{p: 2}}>
                        <Alert severity="warning">Backups can contain secrets. Archives use mode 0600 and expire automatically, but Dockman does not encrypt them: protect or encrypt the Git storage volume. Restoring never deploys or restarts a stack.</Alert>
                        <TableContainer component={Paper} variant="outlined" sx={{maxHeight: '55vh'}}><Table size="small" stickyHeader><TableHead><TableRow><TableCell>Date</TableCell><TableCell>Type</TableCell><TableCell>Stacks</TableCell><TableCell>Files</TableCell><TableCell>Size</TableCell><TableCell>Expires</TableCell><TableCell align="right">Actions</TableCell></TableRow></TableHead><TableBody>
                            {!backups.length && <TableRow><TableCell colSpan={7} align="center" sx={{py: 6, color: 'text.secondary'}}>No backup is currently retained.</TableCell></TableRow>}
                            {backups.map((backup) => <TableRow key={backup.id} hover><TableCell sx={{whiteSpace: 'nowrap'}}>{dateLabel(backup.createdAt)}</TableCell><TableCell><Chip size="small" variant="outlined" label={backup.kind.replaceAll('_', ' ')}/></TableCell><TableCell sx={{fontFamily: 'monospace', maxWidth: 260, overflowWrap: 'anywhere'}}>{backup.composePaths.join(', ') || 'folder link'}</TableCell><TableCell>{backup.fileCount}</TableCell><TableCell>{formatBytes(backup.sizeBytes)}</TableCell><TableCell>{dateLabel(backup.expiresAt)}</TableCell><TableCell align="right" sx={{whiteSpace: 'nowrap'}}>
                                {backup.restorable && <Tooltip title="Preview safe restoration"><span><IconButton size="small" disabled={busy !== null} onClick={() => void openRestore(backup)}>{busy === `preview-${backup.id}` ? <CircularProgress size={17}/> : <RestoreOutlined fontSize="small"/>}</IconButton></span></Tooltip>}
                                <Tooltip title="Download archive"><IconButton size="small" disabled={busy !== null} onClick={() => download(backup)}><CloudDownloadOutlined fontSize="small"/></IconButton></Tooltip>
                                <Tooltip title="Delete backup"><IconButton size="small" color="error" disabled={busy !== null} onClick={() => setDeleteBackup(backup)}><DeleteOutlined fontSize="small"/></IconButton></Tooltip>
                            </TableCell></TableRow>)}
                        </TableBody></Table></TableContainer>
                    </Stack> : <Stack spacing={1.5} sx={{p: 2}}>
                        <Alert severity="info">Choose an earlier commit to preview a local rollback. Dockman never rewrites Git history and never runs Compose or Docker in this step.</Alert>
                        <TableContainer component={Paper} variant="outlined" sx={{maxHeight: '55vh'}}><Table size="small" stickyHeader><TableHead><TableRow><TableCell>Date</TableCell><TableCell>Commit</TableCell><TableCell>Author</TableCell><TableCell>Message</TableCell><TableCell align="right">Action</TableCell></TableRow></TableHead><TableBody>
                            {!commits.length && <TableRow><TableCell colSpan={5} align="center" sx={{py: 6, color: 'text.secondary'}}>{commitsLoading ? <CircularProgress size={22}/> : 'No commit affecting this folder link was found.'}</TableCell></TableRow>}
                            {commits.map((commit, index) => <TableRow key={commit.sha} hover><TableCell sx={{whiteSpace: 'nowrap'}}>{dateLabel(commit.authoredAt)}</TableCell><TableCell sx={{fontFamily: 'monospace', userSelect: 'text'}}>{commit.shortSha}{index === 0 && <Chip size="small" color="success" variant="outlined" label="current" sx={{ml: 1}}/>}</TableCell><TableCell>{commit.authorName || '—'}</TableCell><TableCell sx={{maxWidth: 460, overflowWrap: 'anywhere'}}>{commit.message || '(no message)'}</TableCell><TableCell align="right"><Tooltip title={index === 0 ? 'Previewing the current commit normally produces no change' : 'Preview local rollback'}><span><Button size="small" variant="outlined" startIcon={busy === `rollback-preview-${commit.sha}` ? <CircularProgress size={15}/> : <UndoOutlined/>} disabled={busy !== null} onClick={() => void openCommitRollback(commit)}>Preview</Button></span></Tooltip></TableCell></TableRow>)}
                        </TableBody></Table></TableContainer>
                    </Stack>}
            </DialogContent>
            <DialogActions><Button onClick={() => void (tab === 'commits' ? loadCommits() : load())} disabled={loading || commitsLoading || busy !== null}>Refresh</Button><Button onClick={onClose} disabled={busy !== null}>Close</Button></DialogActions>
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

        <Dialog open={rollbackPreview !== null} onClose={() => busy === null && setRollbackPreview(null)} fullWidth maxWidth="lg">
            <DialogTitle sx={{display: 'flex', alignItems: 'center', gap: 1}}><UndoOutlined/>Local rollback — {rollbackPreview?.commit.shortSha}</DialogTitle>
            <DialogContent dividers><Stack spacing={1.5}>
                <Alert severity="warning">This restores selected local files to the chosen commit after creating a safety backup. Affected stacks are paused for automatic synchronization so Git cannot immediately overwrite the rollback. Nothing is committed, pushed, deployed, restarted or stopped.</Alert>
                <Paper variant="outlined" sx={{p: 1.5}}><Typography variant="subtitle2">{rollbackPreview?.commit.message || '(no message)'}</Typography><Typography variant="caption" color="text.secondary">{rollbackPreview?.commit.authorName} · {dateLabel(rollbackPreview?.commit.authoredAt)} · <Box component="span" sx={{fontFamily: 'monospace', userSelect: 'text'}}>{rollbackPreview?.commit.sha}</Box></Typography></Paper>
                {!!Object.keys(rollbackPreview?.composeErrors || {}).length && <Alert severity="error">The selected historical commit contains an invalid Compose file. Rollback is blocked until another commit is selected.</Alert>}
                {!!rollbackPreview?.missingComposePaths?.length && <Alert severity="warning">The target commit predates {rollbackPreview.missingComposePaths.length} selected stack(s). Keeping their removal actions selected can remove their synchronized local files, but never stops their containers or removes volumes.</Alert>}
                <Box><Typography variant="subtitle2" sx={{mb: .75}}>Stacks</Typography><Stack direction="row" useFlexGap spacing={1} sx={{flexWrap: 'wrap'}}>{rollbackPreview?.composePaths.map((composePath) => <FormControlLabel key={composePath} sx={{m: 0, px: 1, border: 1, borderColor: 'divider', borderRadius: 1}} control={<Checkbox size="small" checked={rollbackSelectedStacks.has(composePath)} disabled={busy !== null} onChange={(_, checked) => toggleRollbackStack(composePath, checked)}/>} label={<Box component="span" sx={{fontFamily: 'monospace', fontSize: 13}}>{composePath}</Box>}/>)}</Stack></Box>
                <TableContainer component={Paper} variant="outlined" sx={{maxHeight: '43vh'}}><Table size="small" stickyHeader><TableHead><TableRow><TableCell padding="checkbox"/><TableCell>Stack</TableCell><TableCell>File</TableCell><TableCell>Action</TableCell><TableCell>Size</TableCell><TableCell>Reason</TableCell><TableCell align="right">Compare</TableCell></TableRow></TableHead><TableBody>
                    {rollbackPreview?.entries.map((entry) => { const actionable = entry.action === 'restore' || entry.action === 'remove'; const stackEnabled = rollbackSelectedStacks.has(entry.composePath); return <TableRow key={entry.path} hover><TableCell padding="checkbox"><Checkbox size="small" disabled={!actionable || !stackEnabled || busy !== null} checked={rollbackSelectedPaths.has(entry.path)} onChange={(_, checked) => setRollbackSelectedPaths((current) => { const next = new Set(current); if (checked) next.add(entry.path); else next.delete(entry.path); return next; })}/></TableCell><TableCell sx={{fontFamily: 'monospace'}}>{entry.composePath}</TableCell><TableCell sx={{fontFamily: 'monospace', overflowWrap: 'anywhere'}}>{entry.path}</TableCell><TableCell><Chip size="small" variant="outlined" color={entry.action === 'remove' ? 'error' : entry.action === 'restore' ? 'warning' : 'default'} label={entry.action}/></TableCell><TableCell>{formatBytes(entry.size || 0)}</TableCell><TableCell>{entry.reason || '—'}</TableCell><TableCell align="right">{entry.action === 'restore' && entry.currentSha && entry.targetSha && <Tooltip title="Compare Dockman with this commit"><IconButton size="small" disabled={busy !== null} onClick={() => void compareRollback(entry)}><CompareArrowsOutlined fontSize="small"/></IconButton></Tooltip>}</TableCell></TableRow>; })}
                </TableBody></Table></TableContainer>
                <Typography variant="caption" color="text.secondary">{rollbackSelectedPaths.size} of {rollbackActionableEntries.length} action(s) selected · {rollbackPreview?.restores || 0} restore(s) · {rollbackPreview?.removals || 0} removal(s) · {rollbackPreview?.skipped || 0} protected/skipped.</Typography>
            </Stack></DialogContent>
            <DialogActions><Button onClick={() => setRollbackPreview(null)} disabled={busy !== null}>Cancel</Button><Button variant="contained" color="warning" startIcon={<UndoOutlined/>} disabled={busy !== null || rollbackSelectedPaths.size === 0 || !!Object.keys(rollbackPreview?.composeErrors || {}).length} onClick={() => void applyCommitRollback()}>{busy?.startsWith('rollback-') && !busy.startsWith('rollback-compare-') && <CircularProgress size={16} sx={{mr: 1}}/>}Create backup and rollback locally</Button></DialogActions>
        </Dialog>

        <Dialog open={rollbackComparison !== null} onClose={() => busy === null && setRollbackComparison(null)} fullWidth maxWidth="lg">
            <DialogTitle sx={{display: 'flex', alignItems: 'center', gap: 1}}><CompareArrowsOutlined/>Compare rollback — <Box component="span" sx={{fontFamily: 'monospace', fontSize: '.9em', overflowWrap: 'anywhere'}}>{rollbackComparison?.path}</Box></DialogTitle>
            <DialogContent dividers sx={{p: 0}}>{rollbackComparison?.comparable ? <><Stack direction="row" sx={{px: 2, py: 1, bgcolor: 'background.paper', borderBottom: 1, borderColor: 'divider'}}><Typography variant="body2" sx={{width: '50%', fontWeight: 700}}>Current Dockman · {formatBytes(rollbackComparison.dockman.size)} · {rollbackComparison.dockman.sha256.slice(0, 12)}</Typography><Typography variant="body2" sx={{width: '50%', fontWeight: 700}}>Selected Git commit · {formatBytes(rollbackComparison.git.size)} · {rollbackComparison.git.sha256.slice(0, 12)}</Typography></Stack><DiffEditor height="52vh" theme="vs-dark" original={rollbackComparison.dockman.content || ''} modified={rollbackComparison.git.content || ''} language={comparisonLanguage(rollbackComparison.path)} options={{readOnly: true, renderSideBySide: true, minimap: {enabled: false}, wordWrap: 'on', originalEditable: false, automaticLayout: true}}/></> : <Alert severity="warning" sx={{m: 2}}>{rollbackComparison?.reason || 'This file cannot be displayed as text.'}</Alert>}</DialogContent>
            <DialogActions><Button onClick={() => setRollbackComparison(null)}>Close comparison</Button></DialogActions>
        </Dialog>

        <Dialog open={deleteBackup !== null} onClose={() => busy === null && setDeleteBackup(null)} maxWidth="xs" fullWidth><DialogTitle>Delete backup?</DialogTitle><DialogContent><Alert severity="warning">This archive will be permanently removed. Git history and stack files are not changed.</Alert></DialogContent><DialogActions><Button onClick={() => setDeleteBackup(null)} disabled={busy !== null}>Cancel</Button><Button color="error" variant="contained" disabled={busy !== null} onClick={() => void removeBackup()}>Delete</Button></DialogActions></Dialog>
    </>;
}
