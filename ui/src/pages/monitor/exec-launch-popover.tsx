import {Alert, Box, Button, CircularProgress, MenuItem, Popover, Stack, TextField, Typography} from '@mui/material';
import {PersonOutlined, Terminal as TerminalIcon} from '@mui/icons-material';
import {useEffect, useState} from 'react';
import {useContainerExecOptionsUrl} from '../../lib/api.ts';
import type {MonitorRow} from './monitor-table.tsx';

export interface ExecLaunch {
    anchor: HTMLElement;
    row: MonitorRow;
}

export default function ExecLaunchPopover({launch, onClose, onConnect}: {
    launch: ExecLaunch | null;
    onClose: () => void;
    onConnect: (row: MonitorRow, shell: string, user: string) => void;
}) {
    const optionsUrl = useContainerExecOptionsUrl();
    const [shells, setShells] = useState<string[] | null>(null);
    const [shell, setShell] = useState('');
    const [userChoice, setUserChoice] = useState('context');
    const [otherUser, setOtherUser] = useState('');
    const [error, setError] = useState('');
    useEffect(() => {
        if (!launch) return;
        const controller = new AbortController();
        setShells(null); setShell(''); setError(''); setUserChoice('context'); setOtherUser('');
        fetch(optionsUrl(launch.row.info.id), {signal: controller.signal})
            .then(async response => {
                if (!response.ok) throw new Error(await response.text() || `HTTP ${response.status}`);
                return response.json() as Promise<{shells?: string[]}>;
            })
            .then(result => {
                const values = result.shells ?? [];
                setShells(values); setShell(values[0] ?? '');
            })
            .catch(err => {
                if (err instanceof Error && err.name !== 'AbortError') {setError(err.message); setShells([]);}
            });
        return () => controller.abort();
    }, [launch, optionsUrl]);
    const user = userChoice === 'context' ? '' : userChoice === 'other' ? otherUser.trim() : userChoice;
    const fieldSx = {'& .MuiInputBase-root': {height: 44, bgcolor: 'rgba(255,255,255,0.035)'}};
    return <Popover open={launch !== null} anchorEl={launch?.anchor ?? null} onClose={onClose}
        anchorOrigin={{vertical: 'top', horizontal: 'center'}} transformOrigin={{vertical: 'bottom', horizontal: 'center'}}
        slotProps={{paper: {sx: {width: 340, borderRadius: 2, border: '1px solid rgba(82,155,255,.24)', bgcolor: '#10243d', backgroundImage: 'none', boxShadow: '0 18px 45px rgba(0,0,0,.55)'}}}}>
        {launch && <>
            <Stack direction="row" spacing={1.2} sx={{px: 2, py: 1.35, alignItems: 'center', borderBottom: '1px solid rgba(82,155,255,.18)'}}>
                <TerminalIcon sx={{color: '#9fb3ca'}}/><Typography sx={{fontWeight: 800}}>{launch.row.info.name}</Typography>
            </Stack>
            {shells === null ? <Box sx={{height: 150, display: 'grid', placeItems: 'center'}}><CircularProgress size={26}/></Box>
                : shells.length === 0 ? <Stack spacing={1} sx={{px: 3, py: 3, alignItems: 'center', textAlign: 'center'}}>
                    <Typography sx={{fontSize: 34, lineHeight: 1, color: '#ffd54f'}}>!</Typography>
                    <Typography sx={{fontWeight: 800, color: '#ffd54f'}}>No shell available</Typography>
                    <Typography color="text.secondary">This container has no supported shell installed.</Typography>
                    {error && <Alert severity="error" sx={{width: '100%'}}>{error}</Alert>}
                </Stack> : <Stack spacing={1.5} sx={{p: 2}}>
                    <Typography sx={{fontWeight: 700}}>Shell</Typography>
                    <TextField select value={shell} onChange={event => setShell(event.target.value)} sx={fieldSx}>
                        {shells.map(value => <MenuItem key={value} value={value}>{value}</MenuItem>)}
                    </TextField>
                    <Typography sx={{fontWeight: 700}}>User</Typography>
                    <TextField select value={userChoice} onChange={event => setUserChoice(event.target.value)} sx={fieldSx}
                        slotProps={{input: {startAdornment: <PersonOutlined sx={{mr: 1, color: '#9fb3ca'}}/>}}}>
                        <MenuItem value="context">Container context</MenuItem><MenuItem value="root">root</MenuItem>
                        <MenuItem value="nobody">nobody</MenuItem><MenuItem value="other">Other…</MenuItem>
                    </TextField>
                    {userChoice === 'other' && <TextField value={otherUser} onChange={event => setOtherUser(event.target.value)} placeholder="UID or user" sx={fieldSx}/>}
                    <Button variant="contained" size="large" startIcon={<TerminalIcon/>}
                        disabled={!shell || (userChoice === 'other' && !otherUser.trim())}
                        onClick={() => onConnect(launch.row, shell, user)} sx={{textTransform: 'none', fontWeight: 800}}>Connect</Button>
                </Stack>}
        </>}
    </Popover>;
}
