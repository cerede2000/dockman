import {Box, IconButton, Stack, TextField, Tooltip, Typography} from '@mui/material';
import {Check, ContentCopy, DeleteSweep, Terminal as TerminalIcon} from '@mui/icons-material';
import {FitAddon} from '@xterm/addon-fit';
import type {Terminal} from '@xterm/xterm';
import {useMemo, useRef, useState} from 'react';
import AppTerminal from './logs-terminal.tsx';
import type {TabTerminal} from '../state/terminal.tsx';

const sizes = [10, 12, 14, 16];

export default function ExecTerminalPanel({tab, isActive}: {tab: TabTerminal; isActive: boolean}) {
    const fit = useRef(new FitAddon());
    const xterm = useRef<Terminal | null>(null);
    const [fontSize, setFontSize] = useState(() => Number(localStorage.getItem('dockman-exec-fontsize')) || 12);
    const [copied, setCopied] = useState(false);
    const session = tab.execSession!;
    const controlled = useMemo(() => ({
        ...tab,
        onTerminal: (term: Terminal) => { xterm.current = term; tab.onTerminal(term); },
        onClose: () => { xterm.current = null; tab.onClose(); },
    }), [tab]);
    const copy = async () => {
        const term = xterm.current;
        if (!term) return;
        const selected = term.getSelection();
        const lines: string[] = [];
        if (!selected) {
            const buffer = term.buffer.active;
            for (let i = 0; i < buffer.length; i++) lines.push(buffer.getLine(i)?.translateToString(true) ?? '');
        }
        await navigator.clipboard.writeText(selected || lines.join('\n').replace(/\n+$/, ''));
        setCopied(true);
        setTimeout(() => setCopied(false), 1200);
    };
    const changeFont = (value: number) => {
        localStorage.setItem('dockman-exec-fontsize', String(value));
        setFontSize(value);
    };
    const fieldSx = {'& .MuiInputBase-root': {height: 28, fontSize: '0.68rem'}, '& .Mui-disabled': {WebkitTextFillColor: 'rgba(255,255,255,0.72)'}};
    return <Box sx={{height: '100%', minHeight: 0, display: 'flex', flexDirection: 'column', bgcolor: '#09090b'}}>
        <Stack direction="row" spacing={0.65} sx={{px: 1, py: 0.6, alignItems: 'center', flexShrink: 0, borderBottom: '1px solid rgba(255,255,255,0.14)', bgcolor: '#111318'}}>
            <TerminalIcon sx={{fontSize: 17, color: '#7dd3fc'}}/>
            <Typography sx={{fontSize: '0.73rem', fontWeight: 800}}>{session.containerID.slice(0, 12)}</Typography>
            <TextField select disabled size="small" value={session.shell} slotProps={{select: {native: true}}} sx={{width: 135, ...fieldSx}}>
                <option value={session.shell}>{session.shell}</option>
            </TextField>
            <TextField select disabled size="small" value={session.user} slotProps={{select: {native: true}}} sx={{width: 145, ...fieldSx}}>
                <option value={session.user}>{session.user || 'Container context'}</option>
            </TextField>
            <TextField select size="small" value={fontSize} onChange={event => changeFont(Number(event.target.value))}
                       slotProps={{select: {native: true}}} sx={{width: 70, ...fieldSx}}>
                {sizes.map(value => <option key={value} value={value}>{value}px</option>)}
            </TextField>
            <Tooltip title="Clear terminal"><IconButton size="small" onClick={() => xterm.current?.clear()}><DeleteSweep sx={{fontSize: 18}}/></IconButton></Tooltip>
            <Tooltip title={copied ? 'Copied!' : 'Copy selection or terminal'}><IconButton size="small" onClick={() => void copy()}>
                {copied ? <Check sx={{fontSize: 18, color: '#66bb6a'}}/> : <ContentCopy sx={{fontSize: 18}}/>}
            </IconButton></Tooltip>
            <Typography sx={{ml: 'auto !important', color: '#81c784', fontSize: '0.66rem'}}>Interactive session</Typography>
        </Stack>
        <Box sx={{flex: 1, minHeight: 0, overflow: 'hidden', bgcolor: '#09090b'}}>
            <AppTerminal {...controlled} fit={fit} isActive={isActive} fontSize={fontSize}/>
        </Box>
    </Box>;
}
