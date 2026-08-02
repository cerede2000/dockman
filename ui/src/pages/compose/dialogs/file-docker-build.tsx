import {useEffect, useMemo, useState} from 'react';
import {Box, Button, Dialog, DialogActions, DialogContent, DialogTitle, TextField, Typography} from '@mui/material';
import {ConstructionOutlined} from '@mui/icons-material';
import {create} from 'zustand';
import {useHostClient} from '../../../lib/api.ts';
import {DockerService} from '../../../gen/docker/v1/docker_pb.ts';
import {makeID, type TabTerminal, useTerminalAction, useTerminalTabs} from '../state/terminal.tsx';

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

const commandArgument = (value: string) => `'${value.replaceAll("'", "'\\''")}'`;

const defaultImageTag = (filename: string) => {
    const parts = filename.replaceAll('\\', '/').split('/');
    const folder = parts.length > 1 ? parts.at(-2)! : 'image';
    const repository = folder.toLowerCase().replace(/[^a-z0-9._-]+/g, '-').replace(/^[-_.]+|[-_.]+$/g, '') || 'image';
    return `${repository}:local`;
};

export default function FileDockerBuild() {
    const filename = useFileDockerBuild((state) => state.filename);
    const close = useFileDockerBuild((state) => state.close);
    const dockerService = useHostClient(DockerService);
    const [imageTag, setImageTag] = useState('');

    useEffect(() => {
        if (filename) setImageTag(defaultImageTag(filename));
    }, [filename]);

    const context = useMemo(() => {
        const normalized = filename.replaceAll('\\', '/');
        const slash = normalized.lastIndexOf('/');
        return slash >= 0 ? normalized.slice(0, slash) : '.';
    }, [filename]);
    const validTag = imageTag.trim().length > 0 && imageTag.trim().length <= 255 && !/\s/.test(imageTag) && !imageTag.startsWith('-');

    const build = () => {
        const tag = imageTag.trim();
        if (!filename || !validTag) return;
        const command = [
            'docker buildx build',
            '--load',
            '--progress=plain',
            '--tag', commandArgument(tag),
            '--file', commandArgument(`dockman://${filename}`),
            '.',
        ].join(' ');
        const stream = dockerService.dockerCommand({command});
        useTerminalAction.getState().open();
        const tabs = useTerminalTabs.getState();
        const key = `dockerfile-build:${filename}:${makeID(6)}`;
        const tab: TabTerminal = {
            id: makeID(),
            title: `Build ${tag}`,
            interactive: false,
            onClose: () => {},
            onTerminal: (term) => {
                void (async () => {
                    try {
                        for await (const item of stream) term.write(item.message);
                        term.write('\r\n\x1b[32m*** image build completed ***\x1b[0m\r\n');
                    } catch (reason) {
                        const message = reason instanceof Error ? reason.message : String(reason);
                        term.write(`\r\n\x1b[31m${message}\x1b[0m\r\n`);
                    }
                })();
            },
        };
        tabs.addTab(key, tab);
        close();
    };

    return <Dialog open={Boolean(filename)} onClose={close} fullWidth maxWidth="sm">
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
                    helperText={validTag ? 'The image will be loaded into the selected Docker host.' : 'Enter a valid tag without spaces, for example apple-music-rip:local.'}
                    onChange={(event) => setImageTag(event.target.value)}
                    onKeyDown={(event) => {
                        if (event.key === 'Enter' && validTag) {
                            event.preventDefault();
                            build();
                        }
                    }}
                    slotProps={{input: {sx: {fontFamily: 'monospace'}}}}
                />
            </Box>
        </DialogContent>
        <DialogActions>
            <Button onClick={close}>Cancel</Button>
            <Button variant="contained" startIcon={<ConstructionOutlined/>} disabled={!validTag} onClick={build}>Build</Button>
        </DialogActions>
    </Dialog>;
}
