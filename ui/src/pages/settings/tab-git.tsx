import {useCallback, useEffect, useState} from "react";
import {
    Alert, Box, Button, Chip, CircularProgress, Dialog, DialogActions, DialogContent,
    DialogTitle, FormControl, IconButton, InputLabel, MenuItem, Paper, Select, Stack,
    Table, TableBody, TableCell, TableContainer, TableHead, TableRow, TextField,
    Tooltip, Typography,
} from "@mui/material";
import {Add, DeleteOutlined, EditOutlined, KeyOutlined, SyncOutlined} from "@mui/icons-material";
import {withProtectedAPI} from "../../lib/api.ts";
import {useSnackbar} from "../../hooks/snackbar.ts";

type AuthType = "public" | "https_token" | "ssh_key";

interface GitStatus {
    enabled: boolean;
    phase: string;
    repositorySyncAvailable: boolean;
}

interface Credential {
    id: string;
    name: string;
    authType: AuthType;
    username?: string;
    hasSecret: boolean;
    secretHint?: string;
    createdAt: string;
    updatedAt: string;
}

interface CredentialForm {
    name: string;
    authType: AuthType;
    username: string;
    token: string;
    privateKey: string;
    passphrase: string;
}

const emptyForm: CredentialForm = {
    name: "", authType: "public", username: "", token: "", privateKey: "", passphrase: "",
};

async function api<T>(path: string, init?: RequestInit): Promise<T> {
    const headers = new Headers(init?.headers);
    if (init?.body) headers.set("Content-Type", "application/json");
    const response = await fetch(withProtectedAPI(`/git${path}`), {...init, headers});
    if (!response.ok) {
        const body = await response.json().catch(() => ({error: response.statusText})) as {error?: string};
        throw new Error(body.error || `HTTP ${response.status}`);
    }
    if (response.status === 204) return undefined as T;
    return response.json() as Promise<T>;
}

function authLabel(type: AuthType) {
    if (type === "https_token") return "GitHub HTTPS token";
    if (type === "ssh_key") return "SSH key";
    return "Public / no authentication";
}

export default function TabGit() {
    const {showError, showSuccess} = useSnackbar();
    const [status, setStatus] = useState<GitStatus | null>(null);
    const [credentials, setCredentials] = useState<Credential[]>([]);
    const [loading, setLoading] = useState(true);
    const [dialogOpen, setDialogOpen] = useState(false);
    const [editing, setEditing] = useState<Credential | null>(null);
    const [form, setForm] = useState<CredentialForm>(emptyForm);
    const [repositoryUrl, setRepositoryUrl] = useState("");
    const [busy, setBusy] = useState<string | null>(null);
    const [deleteTarget, setDeleteTarget] = useState<Credential | null>(null);

    const load = useCallback(async () => {
        setLoading(true);
        try {
            const nextStatus = await api<GitStatus>("/status");
            setStatus(nextStatus);
            setCredentials(nextStatus.enabled ? await api<Credential[]>("/credentials") : []);
        } catch (error) {
            showError(`Unable to load Git settings: ${(error as Error).message}`);
        } finally {
            setLoading(false);
        }
    }, [showError]);

    useEffect(() => { void load(); }, [load]);

    const openCreate = () => {
        setEditing(null);
        setForm(emptyForm);
        setDialogOpen(true);
    };

    const openEdit = (credential: Credential) => {
        setEditing(credential);
        setForm({
            name: credential.name,
            authType: credential.authType,
            username: credential.username || "",
            token: "",
            privateKey: "",
            passphrase: "",
        });
        setDialogOpen(true);
    };

    const save = async () => {
        setBusy("save");
        try {
            const path = editing ? `/credentials/${editing.id}` : "/credentials";
            await api<Credential>(path, {method: editing ? "PUT" : "POST", body: JSON.stringify(form)});
            showSuccess(editing ? "Git credential updated." : "Git credential created.");
            setDialogOpen(false);
            await load();
        } catch (error) {
            showError((error as Error).message);
        } finally {
            setBusy(null);
        }
    };

    const testSaved = async (credential: Credential) => {
        setBusy(`test-${credential.id}`);
        try {
            const result = await api<{ok: boolean; message: string}>(`/credentials/${credential.id}/test`, {
                method: "POST", body: JSON.stringify({repositoryUrl: repositoryUrl.trim()}),
            });
            showSuccess(result.message);
        } catch (error) {
            showError((error as Error).message);
        } finally {
            setBusy(null);
        }
    };

    const remove = async () => {
        if (!deleteTarget) return;
        setBusy(`delete-${deleteTarget.id}`);
        try {
            await api<void>(`/credentials/${deleteTarget.id}`, {method: "DELETE"});
            showSuccess("Git credential deleted.");
            setDeleteTarget(null);
            await load();
        } catch (error) {
            showError((error as Error).message);
        } finally {
            setBusy(null);
        }
    };

    if (loading && !status) return <Box sx={{display: "grid", placeItems: "center", p: 6}}><CircularProgress/></Box>;

    if (status && !status.enabled) {
        return (
            <Box sx={{maxWidth: 900, mx: "auto", p: {xs: 1, md: 3}}}>
                <Alert severity="info" variant="outlined">
                    <Typography sx={{fontWeight: 700}}>Git synchronization is disabled by default</Typography>
                    <Typography variant="body2" sx={{mt: .5}}>
                        Enable this experimental foundation with <code>DOCKMAN_GIT_SYNC=true</code>, then restart Dockman.
                        For production, mount a 32-byte key as a Docker secret and set <code>DOCKMAN_GIT_MASTER_KEY_FILE=/run/secrets/dockman_git_key</code>.
                    </Typography>
                </Alert>
            </Box>
        );
    }

    return (
        <Box sx={{maxWidth: 1100, mx: "auto", p: {xs: 1, md: 3}}}>
            <Stack spacing={2}>
                <Paper variant="outlined" sx={{p: 2.25, borderRadius: 2}}>
                    <Stack direction={{xs: "column", md: "row"}} sx={{justifyContent: "space-between", gap: 2}}>
                        <Box>
                            <Typography variant="h6">Git credentials</Typography>
                            <Typography variant="body2" color="text.secondary">
                                Lot 0–1 foundation. Credentials are encrypted at rest; repository import, push and synchronization remain disabled until lot 2.
                            </Typography>
                        </Box>
                        <Button variant="contained" startIcon={<Add/>} onClick={openCreate}>Add credential</Button>
                    </Stack>
                    <TextField
                        value={repositoryUrl}
                        onChange={(event) => setRepositoryUrl(event.target.value)}
                        label="Repository URL for connection tests (optional)"
                        placeholder="https://github.com/owner/repository.git"
                        size="small"
                        fullWidth
                        sx={{mt: 2}}
                        helperText="URLs are restricted to credential-free github.com addresses during this security-sensitive phase."
                    />
                </Paper>

                <TableContainer component={Paper} variant="outlined" sx={{borderRadius: 2}}>
                    <Table size="small">
                        <TableHead><TableRow>
                            <TableCell>Name</TableCell><TableCell>Type</TableCell><TableCell>Identity</TableCell><TableCell>Secret</TableCell><TableCell align="right">Actions</TableCell>
                        </TableRow></TableHead>
                        <TableBody>
                            {credentials.length === 0 && <TableRow><TableCell colSpan={5} align="center" sx={{py: 5, color: "text.secondary"}}>No Git credentials configured.</TableCell></TableRow>}
                            {credentials.map((credential) => (
                                <TableRow key={credential.id} hover>
                                    <TableCell sx={{fontWeight: 650}}>{credential.name}</TableCell>
                                    <TableCell><Chip size="small" variant="outlined" label={authLabel(credential.authType)}/></TableCell>
                                    <TableCell>{credential.username || "—"}</TableCell>
                                    <TableCell sx={{fontFamily: "monospace"}}>{credential.secretHint || (credential.hasSecret ? "••••" : "—")}</TableCell>
                                    <TableCell align="right">
                                        <Tooltip title="Test connection"><span><IconButton size="small" disabled={busy !== null} onClick={() => void testSaved(credential)}>
                                            {busy === `test-${credential.id}` ? <CircularProgress size={18}/> : <SyncOutlined fontSize="small"/>}
                                        </IconButton></span></Tooltip>
                                        <Tooltip title="Edit"><IconButton size="small" disabled={busy !== null} onClick={() => openEdit(credential)}><EditOutlined fontSize="small"/></IconButton></Tooltip>
                                        <Tooltip title="Delete"><IconButton size="small" color="error" disabled={busy !== null} onClick={() => setDeleteTarget(credential)}><DeleteOutlined fontSize="small"/></IconButton></Tooltip>
                                    </TableCell>
                                </TableRow>
                            ))}
                        </TableBody>
                    </Table>
                </TableContainer>
            </Stack>

            <Dialog open={dialogOpen} onClose={() => busy === null && setDialogOpen(false)} fullWidth maxWidth="sm">
                <DialogTitle sx={{display: "flex", alignItems: "center", gap: 1}}><KeyOutlined/>{editing ? "Edit Git credential" : "Add Git credential"}</DialogTitle>
                <DialogContent dividers>
                    <Stack spacing={2} sx={{pt: .5}}>
                        <TextField label="Name" value={form.name} onChange={(event) => setForm({...form, name: event.target.value})} autoFocus required/>
                        <FormControl><InputLabel>Authentication</InputLabel>
                            <Select label="Authentication" value={form.authType} onChange={(event) => setForm({...form, authType: event.target.value as AuthType, token: "", privateKey: "", passphrase: ""})}>
                                <MenuItem value="public">Public / no authentication</MenuItem>
                                <MenuItem value="https_token">GitHub HTTPS token</MenuItem>
                                <MenuItem value="ssh_key">SSH private key</MenuItem>
                            </Select>
                        </FormControl>
                        {form.authType === "https_token" && <>
                            <TextField label="Username (optional)" value={form.username} onChange={(event) => setForm({...form, username: event.target.value})} helperText="Defaults to x-access-token, suitable for GitHub fine-grained PATs."/>
                            <TextField label={editing ? "New token (leave blank to keep current)" : "Token"} type="password" value={form.token} onChange={(event) => setForm({...form, token: event.target.value})} required={!editing} autoComplete="new-password"/>
                        </>}
                        {form.authType === "ssh_key" && <>
                            <TextField label={editing ? "New private key (leave blank to keep current)" : "Private key"} value={form.privateKey} onChange={(event) => setForm({...form, privateKey: event.target.value})} required={!editing} multiline minRows={7} maxRows={13} sx={{"& textarea": {fontFamily: "monospace"}}}/>
                            <TextField label="Passphrase (if encrypted)" type="password" value={form.passphrase} onChange={(event) => setForm({...form, passphrase: event.target.value})} autoComplete="new-password"/>
                        </>}
                        {editing && form.authType !== "public" && <Alert severity="info">Stored secrets are never returned by the API. Leave the secret empty to retain it unchanged.</Alert>}
                    </Stack>
                </DialogContent>
                <DialogActions><Button onClick={() => setDialogOpen(false)} disabled={busy !== null}>Cancel</Button><Button variant="contained" onClick={() => void save()} disabled={busy !== null || !form.name.trim()}>{busy === "save" && <CircularProgress size={16} sx={{mr: 1}}/>}Save</Button></DialogActions>
            </Dialog>

            <Dialog open={deleteTarget !== null} onClose={() => busy === null && setDeleteTarget(null)} maxWidth="xs" fullWidth>
                <DialogTitle>Delete Git credential?</DialogTitle>
                <DialogContent><Typography>This removes <strong>{deleteTarget?.name}</strong>. The encrypted secret cannot be recovered.</Typography></DialogContent>
                <DialogActions><Button onClick={() => setDeleteTarget(null)} disabled={busy !== null}>Cancel</Button><Button color="error" variant="contained" onClick={() => void remove()} disabled={busy !== null}>Delete</Button></DialogActions>
            </Dialog>
        </Box>
    );
}
