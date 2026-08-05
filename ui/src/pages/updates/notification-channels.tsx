import {
    Alert, Box, Button, Chip, Dialog, DialogActions, DialogContent, DialogTitle,
    FormControl, FormControlLabel, IconButton, InputLabel, MenuItem, Paper, Select,
    Stack, Switch, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, TextField, Tooltip, Typography,
} from '@mui/material';
import {AddOutlined, DeleteOutlined, EditOutlined, NotificationsActiveOutlined, SendOutlined} from '@mui/icons-material';
import {useCallback, useEffect, useState} from 'react';
import {useHostUrl} from '../../lib/api.ts';
import {useSnackbar} from '../../hooks/snackbar.ts';
import {useHostFromUrl} from '../home/home.tsx';

type ChannelType = 'webhook' | 'gotify' | 'ntfy' | 'discord' | 'apprise';

type NotificationChannel = {
    id?: number; name: string; type: ChannelType; enabled: boolean; url: string; token: string;
    username: string; password: string; target?: string; topic: string; priority: number; tags: string;
    allowInsecureHttp: boolean; notifyUpdates: boolean; notifyErrors: boolean;
    clearCredentials?: boolean;
    configured?: boolean; hasToken?: boolean; hasUsername?: boolean; hasPassword?: boolean;
    error?: string;
};

type Delivery = {id: number; createdAt: string; channelName: string; kind: string; subject: string; success: boolean; error?: string};

const emptyChannel: NotificationChannel = {
    name: '', type: 'gotify', enabled: true, url: '', token: '', username: '', password: '',
    topic: '', priority: 0, tags: '', allowInsecureHttp: false, notifyUpdates: true, notifyErrors: true,
};

const labels: Record<ChannelType, string> = {
    webhook: 'Generic webhook', gotify: 'Gotify', ntfy: 'ntfy', discord: 'Discord', apprise: 'Apprise API',
};

export default function NotificationChannels() {
    const host = useHostFromUrl();
    const hostUrl = useHostUrl();
    const {showError, showSuccess} = useSnackbar();
    const [open, setOpen] = useState(false);
    const [channels, setChannels] = useState<NotificationChannel[]>([]);
    const [deliveries, setDeliveries] = useState<Delivery[]>([]);
    const [draft, setDraft] = useState<NotificationChannel | null>(null);
    const [deleteTarget, setDeleteTarget] = useState<NotificationChannel | null>(null);
    const [confirmation, setConfirmation] = useState('');
    const [busy, setBusy] = useState(false);

    const load = useCallback(async () => {
        const response = await fetch(hostUrl('/docker/updates/notifications/channels'));
        if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
        const payload = await response.json() as {channels?: NotificationChannel[]; deliveries?: Delivery[]};
        setChannels(payload.channels ?? []);
        setDeliveries(payload.deliveries ?? []);
    }, [hostUrl]);

    useEffect(() => { void load().catch(() => undefined); }, [load]);

    const openChannels = async () => {
        setOpen(true);
        try {
            await load();
        } catch (error) {
            showError(`Unable to load notification channels — ${error instanceof Error ? error.message : String(error)}`);
        }
    };

    const edit = (channel?: NotificationChannel) => setDraft(channel ? {...channel, url: '', token: '', username: '', password: ''} : {...emptyChannel});

    const save = async () => {
        if (!draft) return;
        setBusy(true);
        try {
            const response = await fetch(hostUrl('/docker/updates/notifications/channels'), {
                method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(draft),
            });
            if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
            await load();
            setDraft(null);
            showSuccess('Notification channel saved securely');
        } catch (error) {
            showError(`Unable to save notification channel — ${error instanceof Error ? error.message : String(error)}`);
        } finally { setBusy(false); }
    };

    const test = async (channel: NotificationChannel) => {
        if (!channel.id) return;
        setBusy(true);
        try {
            const response = await fetch(hostUrl(`/docker/updates/notifications/channels/${channel.id}/test`), {method: 'POST'});
            if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
            showSuccess(`${channel.name} test notification sent`);
        } catch (error) {
            showError(`Notification test failed — ${error instanceof Error ? error.message : String(error)}`);
        } finally { setBusy(false); }
    };

    const remove = async (channel: NotificationChannel) => {
        if (!channel.id || confirmation !== 'CONFIRM') return;
        setBusy(true);
        try {
            const response = await fetch(hostUrl(`/docker/updates/notifications/channels/${channel.id}`), {method: 'DELETE'});
            if (!response.ok) throw new Error((await response.text()).trim() || `HTTP ${response.status}`);
            await load();
            setDeleteTarget(null);
            setConfirmation('');
            showSuccess('Notification channel deleted');
        } catch (error) {
            showError(`Unable to delete notification channel — ${error instanceof Error ? error.message : String(error)}`);
        } finally { setBusy(false); }
    };

    const enabled = channels.filter(channel => channel.enabled).length;
    return <>
        <Button startIcon={<NotificationsActiveOutlined/>} color={enabled > 0 ? 'success' : 'inherit'} onClick={() => void openChannels()}>
            Channels{enabled > 0 ? ` (${enabled})` : ''}
        </Button>
        <Dialog open={open} onClose={() => !busy && setOpen(false)} fullWidth maxWidth="md">
            <DialogTitle><Stack direction="row" spacing={1} sx={{alignItems: 'center'}}><NotificationsActiveOutlined/><span>Notification channels — {host}</span></Stack></DialogTitle>
            <DialogContent dividers><Stack spacing={1.5}>
                <Alert severity="info">Each channel is isolated: a failing endpoint is recorded and retried without blocking SMTP or the other channels. Credentials are encrypted and are never returned by the API.</Alert>
                {channels.length === 0 && <Typography color="text.secondary">No HTTP notification channel configured.</Typography>}
                {channels.map(channel => <Paper key={channel.id} variant="outlined" sx={{p: 1.5}}><Stack direction={{xs: 'column', sm: 'row'}} spacing={1} sx={{alignItems: {sm: 'center'}}}>
                    <Box sx={{flex: 1}}><Stack direction="row" spacing={1} sx={{alignItems: 'center'}}><Typography sx={{fontWeight: 600}}>{channel.name}</Typography><Chip size="small" label={labels[channel.type]} variant="outlined"/><Chip size="small" color={channel.error ? 'error' : channel.enabled ? 'success' : 'default'} label={channel.error ? 'configuration error' : channel.enabled ? 'enabled' : 'disabled'}/></Stack><Typography variant="body2" color={channel.error ? 'error' : 'text.secondary'}>{channel.error || channel.target || 'Configured endpoint'}{channel.topic ? ` · ${channel.topic}` : ''}</Typography></Box>
                    <Tooltip title="Send test"><span><IconButton disabled={busy} onClick={() => void test(channel)}><SendOutlined/></IconButton></span></Tooltip>
                    <Tooltip title="Edit"><span><IconButton disabled={busy} onClick={() => edit(channel)}><EditOutlined/></IconButton></span></Tooltip>
                    <Tooltip title="Delete"><span><IconButton color="error" disabled={busy} onClick={() => { setDeleteTarget(channel); setConfirmation(''); }}><DeleteOutlined/></IconButton></span></Tooltip>
                </Stack></Paper>)}
                <Box><Button startIcon={<AddOutlined/>} onClick={() => edit()}>Add channel</Button></Box>
                <Box><Typography variant="subtitle2" sx={{mb: .75}}>Recent deliveries</Typography>{deliveries.length === 0 ? <Typography variant="body2" color="text.secondary">No delivery attempt recorded.</Typography> : <TableContainer sx={{maxHeight: 220}}><Table size="small" stickyHeader><TableHead><TableRow><TableCell>Date</TableCell><TableCell>Channel</TableCell><TableCell>Event</TableCell><TableCell>Status</TableCell></TableRow></TableHead><TableBody>{deliveries.slice(0, 20).map(delivery => <TableRow key={delivery.id}><TableCell sx={{whiteSpace: 'nowrap'}}>{new Date(delivery.createdAt).toLocaleString()}</TableCell><TableCell>{delivery.channelName}</TableCell><TableCell>{delivery.kind}</TableCell><TableCell><Tooltip title={delivery.error ?? delivery.subject}><Chip size="small" variant="outlined" color={delivery.success ? 'success' : 'error'} label={delivery.success ? 'sent' : 'failed'}/></Tooltip></TableCell></TableRow>)}</TableBody></Table></TableContainer>}</Box>
            </Stack></DialogContent>
            <DialogActions><Button onClick={() => setOpen(false)} disabled={busy}>Close</Button></DialogActions>
        </Dialog>
        <Dialog open={draft !== null} onClose={() => !busy && setDraft(null)} fullWidth maxWidth="sm">
            <DialogTitle>{draft?.id ? 'Edit' : 'Add'} notification channel</DialogTitle>
            {draft && <DialogContent dividers><Stack spacing={2}>
                <TextField label="Name" value={draft.name} onChange={event => setDraft({...draft, name: event.target.value})} autoFocus fullWidth/>
                <FormControl fullWidth><InputLabel>Provider</InputLabel><Select label="Provider" value={draft.type} onChange={event => setDraft({...draft, type: event.target.value as ChannelType})}>{Object.entries(labels).map(([value, label]) => <MenuItem key={value} value={value}>{label}</MenuItem>)}</Select></FormControl>
                <TextField label={draft.id ? 'Endpoint URL (leave blank to keep current)' : 'Endpoint URL'} value={draft.url} onChange={event => setDraft({...draft, url: event.target.value})} placeholder={draft.type === 'gotify' ? 'https://gotify.example.com' : draft.type === 'ntfy' ? 'https://ntfy.sh' : 'https://…'} helperText={draft.target ? `Current endpoint: ${draft.target}` : 'HTTPS is required by default.'} fullWidth/>
                {(draft.type === 'gotify' || draft.type === 'apprise' || draft.type === 'ntfy' || draft.type === 'webhook') && <TextField type="password" label={draft.hasToken ? 'New token/key (leave blank to keep current)' : draft.type === 'apprise' ? 'Apprise configuration key' : 'Token (optional for webhook/ntfy)'} value={draft.token} onChange={event => setDraft({...draft, token: event.target.value})} autoComplete="new-password" fullWidth/>}
                {draft.type === 'ntfy' && <TextField label="Topic" value={draft.topic} onChange={event => setDraft({...draft, topic: event.target.value})} fullWidth/>}
                {(draft.type === 'ntfy' || draft.type === 'webhook') && <Stack direction={{xs: 'column', sm: 'row'}} spacing={1}><TextField label={draft.hasUsername ? 'New username (blank keeps current)' : 'Username (optional)'} value={draft.username} onChange={event => setDraft({...draft, username: event.target.value})} fullWidth/><TextField type="password" label={draft.hasPassword ? 'New password (blank keeps current)' : 'Password (optional)'} value={draft.password} onChange={event => setDraft({...draft, password: event.target.value})} fullWidth/></Stack>}
                {draft.id && (draft.hasToken || draft.hasUsername || draft.hasPassword) && <FormControlLabel control={<Switch checked={draft.clearCredentials ?? false} onChange={event => setDraft({...draft, clearCredentials: event.target.checked})}/>} label="Remove currently stored authentication credentials"/>}
                {(draft.type === 'gotify' || draft.type === 'ntfy') && <TextField label="Priority (0 = automatic)" type="number" value={draft.priority} onChange={event => setDraft({...draft, priority: Number(event.target.value)})} fullWidth/>}
                {(draft.type === 'ntfy' || draft.type === 'apprise') && <TextField label="Tags (optional)" value={draft.tags} onChange={event => setDraft({...draft, tags: event.target.value})} fullWidth/>}
                <FormControlLabel control={<Switch checked={draft.allowInsecureHttp} onChange={event => setDraft({...draft, allowInsecureHttp: event.target.checked})}/>} label="Allow trusted private-network or HTTP endpoint"/>
                {draft.allowInsecureHttp && <Alert severity="warning">Use only for a trusted LAN service. Loopback, link-local and metadata endpoints remain blocked.</Alert>}
                <Stack direction={{xs: 'column', sm: 'row'}} spacing={1}><FormControlLabel control={<Switch checked={draft.enabled} onChange={event => setDraft({...draft, enabled: event.target.checked})}/>} label="Enable"/><FormControlLabel control={<Switch checked={draft.notifyUpdates} onChange={event => setDraft({...draft, notifyUpdates: event.target.checked})}/>} label="Successful updates"/><FormControlLabel control={<Switch checked={draft.notifyErrors} onChange={event => setDraft({...draft, notifyErrors: event.target.checked})}/>} label="Errors and rollbacks"/></Stack>
            </Stack></DialogContent>}
            <DialogActions><Button onClick={() => setDraft(null)} disabled={busy}>Cancel</Button><Button variant="contained" onClick={() => void save()} disabled={busy}>{busy ? 'Saving…' : 'Save'}</Button></DialogActions>
        </Dialog>
        <Dialog open={deleteTarget !== null} onClose={() => !busy && setDeleteTarget(null)} fullWidth maxWidth="xs">
            <DialogTitle>Delete notification channel</DialogTitle>
            <DialogContent dividers><Stack spacing={2}><Alert severity="warning">The channel and its encrypted credentials will be removed.</Alert><Typography>{deleteTarget?.name}</Typography><TextField label='Type "CONFIRM"' value={confirmation} onChange={event => setConfirmation(event.target.value)} autoFocus fullWidth/></Stack></DialogContent>
            <DialogActions><Button onClick={() => setDeleteTarget(null)} disabled={busy}>Cancel</Button><Button color="error" variant="contained" disabled={busy || confirmation !== 'CONFIRM'} onClick={() => deleteTarget && void remove(deleteTarget)}>Delete</Button></DialogActions>
        </Dialog>
    </>;
}
