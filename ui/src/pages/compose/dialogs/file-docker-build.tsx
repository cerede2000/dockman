import {useCallback, useEffect, useMemo, useState} from 'react';
import {Box, Button, Chip, Dialog, DialogActions, DialogContent, DialogTitle, Divider, Stack, TextField, Typography} from '@mui/material';
import {CancelOutlined, ConstructionOutlined, VisibilityOutlined} from '@mui/icons-material';
import {create} from 'zustand';
import {useHostUrl} from '../../../lib/api.ts';
import {useSnackbar} from '../../../hooks/snackbar.ts';
import {makeID, type TabTerminal, useTerminalAction, useTerminalTabs} from '../state/terminal.tsx';

type BuildJob = {
    id: string;
    filename: string;
    imageTag: string;
    status: 'queued' | 'running' | 'succeeded' | 'failed' | 'canceled';
    error?: string;
    createdAt: string;
    startedAt?: string;
    completedAt?: string;
    log?: string;
    nextOffset?: number;
    truncated?: boolean;
};

const terminalBuildStatuses = new Set<BuildJob['status']>(['succeeded', 'failed', 'canceled']);

// The dialog store deliberately lives beside the dialog, matching the other
// Files actions so a context-menu row can open the single mounted instance.
// eslint-disable-next-line react-refresh/only-export-components
export const useFileDockerBuild = create<{
    filename: string;
    open: (filename: string) => void;
    close: () => void;
}>((set) => ({
    filename: '',
    open: (filename) => set({filename}),
    close: () => set({filename: ''}),
}));

const defaultImageTag = (filename: string) => {
    const parts = filename.replaceAll('\\', '/').split('/');
    const folder = parts.length > 1 ? parts.at(-2)! : 'image';
    const repository = folder.toLowerCase().replace(/[^a-z0-9._-]+/g, '-').replace(/^[-_.]+|[-_.]+$/g, '') || 'image';
    return `${repository}:local`;
};

const buildStatusColor = (status: BuildJob['status']): 'default' | 'info' | 'success' | 'error' | 'warning' => {
    if (status === 'running') return 'info';
    if (status === 'succeeded') return 'success';
    if (status === 'failed') return 'error';
    if (status === 'canceled') return 'warning';
    return 'default';
};

export default function FileDockerBuild() {
    const filename = useFileDockerBuild((state) => state.filename);
    const close = useFileDockerBuild((state) => state.close);
    const hostUrl = useHostUrl();
    const {showError, showSuccess} = useSnackbar();
    const [imageTag, setImageTag] = useState('');
    const [starting, setStarting] = useState(false);
    const [jobs, setJobs] = useState<BuildJob[]>([]);

    useEffect(() => {
        if (filename) setImageTag(defaultImageTag(filename));
    }, [filename]);

    const context = useMemo(() => {
        const normalized = filename.replaceAll('\\', '/');
        const slash = normalized.lastIndexOf('/');
        return slash >= 0 ? normalized.slice(0, slash) : '.';
    }, [filename]);
    const validTag = imageTag.trim().length > 0 && imageTag.trim().length <= 255 && !/\s/.test(imageTag) && !imageTag.startsWith('-');

    const refreshJobs = useCallback(async () => {
        try {
            const response = await fetch(hostUrl('/docker/builds'));
            if (!response.ok) return;
            const payload = await response.json() as {jobs: BuildJob[]};
            setJobs(payload.jobs ?? []);
        } catch {
            // A host may reconnect while a background build continues. The
            // next refresh or reopening this dialog restores the server state.
        }
    }, [hostUrl]);

    useEffect(() => {
        if (!filename) return;
        void refreshJobs();
        const timer = window.setInterval(() => void refreshJobs(), 2000);
        return () => window.clearInterval(timer);
    }, [filename, refreshJobs]);

    const openJob = useCallback((job: BuildJob) => {
        useTerminalAction.getState().open();
        const tabs = useTerminalTabs.getState();
        const key = `dockerfile-build-job:${job.id}`;
        if (tabs.tabs.has(key)) {
            tabs.setActiveTab(key);
            return;
        }
        let pollingGeneration = 0;
        const tab: TabTerminal = {
            id: makeID(),
            title: `Build ${job.imageTag}`,
            interactive: false,
            onClose: () => { pollingGeneration++; },
            onTerminal: (term) => {
                const generation = ++pollingGeneration;
                let offset = 0;
                let failures = 0;
                const poll = async () => {
                    if (generation !== pollingGeneration) return;
                    try {
                        const response = await fetch(`${hostUrl(`/docker/builds/${encodeURIComponent(job.id)}`)}?after=${offset}`);
                        if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
                        const current = await response.json() as BuildJob;
                        if (current.truncated && offset === 0) term.write('\x1b[33m*** Earlier build output was truncated ***\x1b[0m\r\n');
                        if (current.log) term.write(current.log.replaceAll('\n', '\r\n'));
                        offset = current.nextOffset ?? offset;
                        failures = 0;
                        if (terminalBuildStatuses.has(current.status)) return;
                    } catch (reason) {
                        failures++;
                        if (failures >= 3) {
                            const message = reason instanceof Error ? reason.message : String(reason);
                            term.write(`\r\n\x1b[31m*** Progress connection interrupted: ${message}. Reopen the build to reconnect. ***\x1b[0m\r\n`);
                            return;
                        }
                    }
                    window.setTimeout(() => void poll(), 1000);
                };
                void poll();
            },
        };
        tabs.addTab(key, tab);
    }, [hostUrl]);

    const build = async () => {
        const tag = imageTag.trim();
        if (!filename || !validTag || starting) return;
        setStarting(true);
        try {
            const response = await fetch(hostUrl('/docker/builds'), {
                method: 'POST',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({filename, imageTag: tag}),
            });
            if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
            const job = await response.json() as BuildJob;
            setJobs(current => [job, ...current.filter(item => item.id !== job.id)]);
            openJob(job);
            close();
            showSuccess(`Background build started for ${tag}`);
        } catch (reason) {
            const message = reason instanceof Error ? reason.message : String(reason);
            showError(`Unable to start image build: ${message}`);
        } finally {
            setStarting(false);
        }
    };

    const cancelJob = async (job: BuildJob) => {
        try {
            const response = await fetch(hostUrl(`/docker/builds/${encodeURIComponent(job.id)}`), {method: 'DELETE'});
            if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
            await refreshJobs();
            showSuccess(`Cancellation requested for ${job.imageTag}`);
        } catch (reason) {
            showError(`Unable to cancel build: ${reason instanceof Error ? reason.message : String(reason)}`);
        }
    };

    return <Dialog open={Boolean(filename)} onClose={() => !starting && close()} fullWidth maxWidth="sm">
        <DialogTitle sx={{display: 'flex', alignItems: 'center', gap: 1}}>
            <ConstructionOutlined color="primary"/> Build Docker image
        </DialogTitle>
        <DialogContent>
            <Box sx={{pt: 1, display: 'grid', gap: 2}}>
                <Box>
                    <Typography variant="caption" color="text.secondary">Dockerfile</Typography>
                    <Typography variant="body2" sx={{fontFamily: 'monospace', overflowWrap: 'anywhere'}}>{filename}</Typography>
                    <Typography variant="caption" color="text.secondary">Build context: {context}</Typography>
                </Box>
                <TextField
                    autoFocus
                    fullWidth
                    label="Image name and tag"
                    value={imageTag}
                    error={imageTag.length > 0 && !validTag}
                    helperText={validTag ? 'The build continues on the server if you close this dialog or change view.' : 'Enter a valid tag without spaces, for example apple-music-rip:local.'}
                    onChange={(event) => setImageTag(event.target.value)}
                    onKeyDown={(event) => {
                        if (event.key === 'Enter' && validTag) {
                            event.preventDefault();
                            void build();
                        }
                    }}
                    slotProps={{input: {sx: {fontFamily: 'monospace'}}}}
                />
                {jobs.length > 0 && <>
                    <Divider/>
                    <Box>
                        <Typography variant="subtitle2" sx={{mb: 1}}>Recent background builds</Typography>
                        <Stack spacing={.75}>
                            {jobs.slice(0, 5).map(job => <Stack key={job.id} direction="row" spacing={1} sx={{alignItems: 'center'}}>
                                <Chip size="small" color={buildStatusColor(job.status)} label={job.status}/>
                                <Box sx={{minWidth: 0, flex: 1}}>
                                    <Typography variant="body2" noWrap>{job.imageTag}</Typography>
                                    <Typography variant="caption" color="text.secondary" noWrap>{job.filename}</Typography>
                                </Box>
                                <Button size="small" startIcon={<VisibilityOutlined/>} onClick={() => openJob(job)}>View</Button>
                                {(job.status === 'queued' || job.status === 'running') && <Button size="small" color="warning" startIcon={<CancelOutlined/>} onClick={() => void cancelJob(job)}>Cancel</Button>}
                            </Stack>)}
                        </Stack>
                    </Box>
                </>}
            </Box>
        </DialogContent>
        <DialogActions>
            <Button onClick={close} disabled={starting}>Cancel</Button>
            <Button variant="contained" startIcon={<ConstructionOutlined/>} disabled={!validTag || starting} onClick={() => void build()}>{starting ? 'Starting…' : 'Build in background'}</Button>
        </DialogActions>
    </Dialog>;
}
