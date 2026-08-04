import {useCallback, useEffect, useMemo, useState} from 'react';
import {Badge, Box, Button, Chip, Dialog, DialogActions, DialogContent, DialogTitle, Divider, Fab, Stack, TextField, Tooltip, Typography} from '@mui/material';
import {CancelOutlined, ConstructionOutlined, VisibilityOutlined} from '@mui/icons-material';
import {create} from 'zustand';
import {getBaseUrl} from '../../../lib/api.ts';
import {useSnackbar} from '../../../hooks/snackbar.ts';
import {useHostStore} from '../state/files.ts';

export type BuildJob = {
    id: string;
    filename: string;
    imageTag: string;
    status: 'queued' | 'running' | 'succeeded' | 'failed' | 'canceled';
    error?: string;
    createdAt: string;
    startedAt?: string;
    completedAt?: string;
    lastOutputAt?: string;
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
    historyOpen: boolean;
    open: (filename: string) => void;
    openHistory: () => void;
    close: () => void;
}>((set) => ({
    filename: '',
    historyOpen: false,
    open: (filename) => set({filename, historyOpen: false}),
    openHistory: () => set({filename: '', historyOpen: true}),
    close: () => set({filename: '', historyOpen: false}),
}));

// eslint-disable-next-line react-refresh/only-export-components
export const useDockerBuildJobs = create<{
    jobs: BuildJob[];
    setJobs: (jobs: BuildJob[]) => void;
}>((set) => ({jobs: [], setJobs: (jobs) => set({jobs})}));

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

const describeBuildPhase = (log: string) => {
    if (/\b(Get:\d+|apt(?:-get)?\b|Packages \[)/i.test(log)) return 'Downloading operating-system packages';
    if (/exporting (?:to image|layers)|naming to/i.test(log)) return 'Exporting the image to Docker';
    if (/load(?:ing)? build context|transferring context/i.test(log)) return 'Transferring the build context';
    if (/pulling|resolve image config|load metadata/i.test(log)) return 'Downloading a base image';
    if (/running|RUN /i.test(log)) return 'Running a Dockerfile instruction';
    return 'BuildKit is processing the Dockerfile';
};

export default function FileDockerBuild() {
    const filename = useFileDockerBuild((state) => state.filename);
    const historyOpen = useFileDockerBuild((state) => state.historyOpen);
    const close = useFileDockerBuild((state) => state.close);
    const openHistory = useFileDockerBuild((state) => state.openHistory);
    const activeHost = useHostStore(state => state.host) || 'local';
    const hostUrl = useCallback((url: string) => `${getBaseUrl('host', activeHost)}${url}`, [activeHost]);
    const {showError, showSuccess} = useSnackbar();
    const [imageTag, setImageTag] = useState('');
    const [starting, setStarting] = useState(false);
    const jobs = useDockerBuildJobs((state) => state.jobs);
    const setJobs = useDockerBuildJobs((state) => state.setJobs);
    const [selectedJobId, setSelectedJobId] = useState('');
    const [selectedJob, setSelectedJob] = useState<BuildJob | null>(null);
    const [selectedLog, setSelectedLog] = useState('');
    const activeBuildCount = jobs.filter(job => job.status === 'queued' || job.status === 'running').length;

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
    }, [hostUrl, setJobs]);

    useEffect(() => {
        setJobs([]);
        void refreshJobs();
    }, [refreshJobs, setJobs]);

    useEffect(() => {
        const timer = window.setInterval(() => void refreshJobs(), activeBuildCount > 0 ? 2000 : 10000);
        return () => window.clearInterval(timer);
    }, [activeBuildCount, refreshJobs]);

    const openJob = useCallback((job: BuildJob) => {
        openHistory();
        setSelectedJobId(job.id);
    }, [openHistory]);

    useEffect(() => {
        if (!selectedJobId || !historyOpen) return;
        let active = true;
        let offset = 0;
        setSelectedLog('');
        setSelectedJob(null);
        const poll = async () => {
            try {
                const response = await fetch(`${hostUrl(`/docker/builds/${encodeURIComponent(selectedJobId)}`)}?after=${offset}`);
                if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
                const current = await response.json() as BuildJob;
                if (!active) return;
                setSelectedJob(current);
                if (current.log) setSelectedLog(previous => previous + current.log);
                offset = current.nextOffset ?? offset;
                if (terminalBuildStatuses.has(current.status)) {
                    void refreshJobs();
                    return;
                }
            } catch (reason) {
                if (active) showError(`Unable to read build progress: ${reason instanceof Error ? reason.message : String(reason)}`);
                return;
            }
            if (active) window.setTimeout(() => void poll(), 1000);
        };
        void poll();
        return () => { active = false; };
    }, [historyOpen, hostUrl, refreshJobs, selectedJobId, showError]);

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
            setJobs([job, ...jobs.filter(item => item.id !== job.id)]);
            openJob(job);
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

    const detail = selectedJob ?? jobs.find(job => job.id === selectedJobId) ?? null;
    const lastActivitySeconds = detail?.lastOutputAt
        ? Math.max(0, Math.floor((Date.now() - new Date(detail.lastOutputAt).getTime()) / 1000))
        : null;
    const quietBuild = detail?.status === 'running' && lastActivitySeconds !== null && lastActivitySeconds >= 60;
    const elapsedSeconds = detail?.startedAt
        ? Math.max(0, Math.floor(((detail.completedAt ? new Date(detail.completedAt).getTime() : Date.now()) - new Date(detail.startedAt).getTime()) / 1000))
        : 0;
    const currentPhase = selectedLog.split('\n').map(line => line.trim()).filter(Boolean).at(-1) ?? '';

    return <Dialog open={Boolean(filename) || historyOpen} onClose={() => !starting && close()} fullWidth maxWidth="md">
        <DialogTitle sx={{display: 'flex', alignItems: 'center', gap: 1}}>
            <ConstructionOutlined color="primary"/> {filename ? 'Build Docker image' : 'Background builds'}
        </DialogTitle>
        <DialogContent>
            <Box sx={{pt: 1, display: 'grid', gap: 2}}>
                {filename && <><Box>
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
                /></>}
                {jobs.length > 0 && <>
                    {filename && <Divider/>}
                    <Box>
                        <Typography variant="subtitle2" sx={{mb: 1}}>Recent background builds</Typography>
                        <Stack spacing={.75}>
                            {jobs.slice(0, 5).map(job => <Stack key={job.id} direction="row" spacing={1} sx={{alignItems: 'center'}}>
                                <Chip size="small" color={buildStatusColor(job.status)} label={job.status}/>
                                <Box sx={{minWidth: 0, flex: 1}}>
                                    <Typography variant="body2" noWrap>{job.imageTag}</Typography>
                                    <Typography variant="caption" color="text.secondary" noWrap>{job.filename}</Typography>
                                </Box>
                                <Button size="small" variant={selectedJobId === job.id ? 'outlined' : 'text'} startIcon={<VisibilityOutlined/>} onClick={() => openJob(job)}>View</Button>
                                {(job.status === 'queued' || job.status === 'running') && <Button size="small" color="warning" startIcon={<CancelOutlined/>} onClick={() => void cancelJob(job)}>Cancel</Button>}
                            </Stack>)}
                        </Stack>
                    </Box>
                </>}
                {!filename && jobs.length === 0 && <Typography color="text.secondary">No recent Docker image build is available for this host.</Typography>}
                {detail && <>
                    <Divider/>
                    <Stack direction={{xs: 'column', sm: 'row'}} spacing={1} sx={{alignItems: {sm: 'center'}}}>
                        <Typography variant="subtitle2" sx={{flex: 1}}>Progress · {detail.imageTag}</Typography>
                        <Chip size="small" color={buildStatusColor(detail.status)} label={detail.status}/>
                        {lastActivitySeconds !== null && <Typography variant="caption" color={quietBuild ? 'warning.main' : 'text.secondary'}>
                            Last output {lastActivitySeconds < 2 ? 'just now' : `${lastActivitySeconds}s ago`}
                        </Typography>}
                        {detail.startedAt && <Typography variant="caption" color="text.secondary">Elapsed {Math.floor(elapsedSeconds / 60)}m {elapsedSeconds % 60}s</Typography>}
                    </Stack>
                    {currentPhase && <Typography variant="caption" color="text.secondary" noWrap title={currentPhase}>Current output: {currentPhase}</Typography>}
                    {detail.status === 'running' && <Typography variant="body2" color="primary.main">Phase: {describeBuildPhase(selectedLog)}</Typography>}
                    {quietBuild && <Typography variant="body2" color="warning.main">
                        No new output for more than one minute. The current Dockerfile command may be downloading slowly or waiting on the network; the job is still running and can be canceled safely.
                    </Typography>}
                    <Box component="pre" sx={{m: 0, p: 1.5, minHeight: 180, maxHeight: '42vh', overflow: 'auto', bgcolor: '#030303', border: '1px solid', borderColor: 'divider', borderRadius: 1, color: '#e5e7eb', fontFamily: 'monospace', fontSize: 12, lineHeight: 1.45, whiteSpace: 'pre-wrap', userSelect: 'text'}}>
                        {selectedLog || 'Waiting for build output…'}
                    </Box>
                    {detail.error && <Typography variant="body2" color="error.main" sx={{overflowWrap: 'anywhere'}}>{detail.error}</Typography>}
                </>}
            </Box>
        </DialogContent>
        <DialogActions>
            <Button onClick={close} disabled={starting}>Close</Button>
            {filename && <Button variant="contained" startIcon={<ConstructionOutlined/>} disabled={!validTag || starting} onClick={() => void build()}>{starting ? 'Starting…' : 'Build in background'}</Button>}
        </DialogActions>
    </Dialog>;
}

export function DockerBuildActivityIndicator() {
    const jobs = useDockerBuildJobs((state) => state.jobs);
    const openHistory = useFileDockerBuild((state) => state.openHistory);
    const active = jobs.filter(job => job.status === 'queued' || job.status === 'running');
    if (active.length === 0) return null;
    return <Tooltip title={`${active.length} Docker image build${active.length > 1 ? 's' : ''} in progress`} placement="left">
        <Fab size="small" color="primary" onClick={openHistory} sx={{position: 'fixed', right: 20, bottom: 20, zIndex: 1400}}>
            <Badge badgeContent={active.length} color="warning"><ConstructionOutlined/></Badge>
        </Fab>
    </Tooltip>;
}
