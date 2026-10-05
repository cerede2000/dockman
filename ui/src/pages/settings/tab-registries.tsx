import {useCallback, useEffect, useRef, useState} from "react";
import {
    Alert, Box, Button, Chip, CircularProgress, Dialog, DialogActions, DialogContent, DialogTitle,
    IconButton, InputAdornment, Paper, Stack, Table, TableBody, TableCell, TableContainer, TableHead,
    TableRow, TextField, Tooltip, Typography,
} from "@mui/material";
import {Add, CheckCircleOutlined, DeleteOutlined, EditOutlined, ErrorOutlineOutlined, LockOutlined, Refresh, Visibility, VisibilityOff} from "@mui/icons-material";
import {
    registryAPI as api,
    registryDateLabel as dateLabel,
    type RegistryCredential,
    type RegistryTestResult,
} from "../../lib/registry-api.ts";
import {useSnackbar} from "../../hooks/snackbar.ts";

interface CredentialForm {
    registry: string;
    username: string;
    secret: string;
}

const emptyForm: CredentialForm = {registry: "", username: "", secret: ""};

export default function TabRegistries() {
    const snackbar = useSnackbar();
    // The snackbar comes from a context, so its functions are new on every
    // provider render. Holding them in a ref keeps the initial load out of
    // that dependency: with it, the effect re-ran on each render and the tab
    // reloaded itself in a loop.
    const snackbarRef = useRef(snackbar);
    snackbarRef.current = snackbar;
    const {showError, showSuccess, showWarning} = snackbar;
    const [credentials, setCredentials] = useState<RegistryCredential[]>([]);
    const [loading, setLoading] = useState(true);
    const [dialogOpen, setDialogOpen] = useState(false);
    const [editing, setEditing] = useState<RegistryCredential | null>(null);
    const [form, setForm] = useState<CredentialForm>(emptyForm);
    const [revealSecret, setRevealSecret] = useState(false);
    const [saving, setSaving] = useState(false);
    const [testing, setTesting] = useState<string | null>(null);
    const [deleting, setDeleting] = useState<RegistryCredential | null>(null);

    const load = useCallback(async () => {
        setLoading(true);
        try {
            setCredentials(await api<RegistryCredential[]>("/credentials"));
        } catch (error) {
            snackbarRef.current.showError(`Unable to load registry credentials: ${(error as Error).message}`);
        } finally {
            setLoading(false);
        }
    }, []);

    useEffect(() => {
        void load();
    }, [load]);

    const openCreate = () => {
        setEditing(null);
        setForm(emptyForm);
        setRevealSecret(false);
        setDialogOpen(true);
    };

    const openEdit = (credential: RegistryCredential) => {
        setEditing(credential);
        setForm({registry: credential.registry, username: credential.username, secret: ""});
        setRevealSecret(false);
        setDialogOpen(true);
    };

    const save = async () => {
        setSaving(true);
        try {
            if (editing) {
                await api(`/credentials/${editing.uuid}`, {method: "PUT", body: JSON.stringify(form)});
                showSuccess(`Credential for ${form.registry} updated.`);
            } else {
                await api("/credentials", {method: "POST", body: JSON.stringify(form)});
                showSuccess(`Credential for ${form.registry} added.`);
            }
            setDialogOpen(false);
            await load();
        } catch (error) {
            showError((error as Error).message);
        } finally {
            setSaving(false);
        }
    };

    // The form's secret is sent when there is one, so a typo is caught before
    // it is saved; an empty one tests what is stored.
    const test = async (credential?: RegistryCredential) => {
        const key = credential?.uuid ?? "form";
        setTesting(key);
        try {
            const path = credential ? `/credentials/${credential.uuid}/test` : "/credentials/test";
            const body = credential && !form.secret ? undefined : JSON.stringify(form);
            const result = await api<RegistryTestResult>(path, {method: "POST", body});
            if (result.ok) {
                showSuccess("The registry accepted these credentials.");
            } else {
                showWarning(result.error ?? "The registry refused these credentials.");
            }
            if (credential) await load();
        } catch (error) {
            showError((error as Error).message);
        } finally {
            setTesting(null);
        }
    };

    const remove = async () => {
        if (!deleting) return;
        try {
            await api(`/credentials/${deleting.uuid}`, {method: "DELETE"});
            showSuccess(`Credential for ${deleting.registry} removed.`);
            setDeleting(null);
            await load();
        } catch (error) {
            showError((error as Error).message);
        }
    };

    const canSave = form.registry.trim() !== "" && form.username.trim() !== "" && (editing !== null || form.secret !== "");

    return (
        <Box sx={{p: 2}}>
            <Stack direction="row" sx={{alignItems: "center", justifyContent: "space-between", mb: 2}}>
                <Box>
                    <Typography variant="h6">Private registries</Typography>
                    <Typography variant="body2" color="text.secondary">
                        Logins used to pull images: deployments, updates and the builds that start from a private base image.
                    </Typography>
                </Box>
                <Stack direction="row" spacing={1}>
                    <Tooltip title="Reload">
                        <span>
                            <IconButton onClick={() => void load()} disabled={loading} aria-label="Reload registry credentials">
                                <Refresh/>
                            </IconButton>
                        </span>
                    </Tooltip>
                    <Button variant="contained" startIcon={<Add/>} onClick={openCreate}>Add registry</Button>
                </Stack>
            </Stack>

            <Alert severity="info" sx={{mb: 2}}>
                Credentials are encrypted with the registry master key in <code>/config/registry</code> and written to the
                Docker configuration Dockman owns. Hosts reached over SSH pull with their own <code>docker login</code>.
            </Alert>

            {loading ? <CircularProgress/> : credentials.length === 0 ? (
                <Paper variant="outlined" sx={{p: 3, textAlign: "center"}}>
                    <LockOutlined color="disabled"/>
                    <Typography variant="body2" color="text.secondary">
                        No registry credential yet. Add one for ghcr.io, Docker Hub or your own registry.
                    </Typography>
                </Paper>
            ) : (
                <TableContainer component={Paper} variant="outlined">
                    <Table size="small">
                        <TableHead>
                            <TableRow>
                                <TableCell>Registry</TableCell>
                                <TableCell>Username</TableCell>
                                <TableCell>Secret</TableCell>
                                <TableCell>Last test</TableCell>
                                <TableCell align="right">Actions</TableCell>
                            </TableRow>
                        </TableHead>
                        <TableBody>
                            {credentials.map(credential => (
                                <TableRow key={credential.uuid}>
                                    <TableCell>{credential.registry}</TableCell>
                                    <TableCell>{credential.username}</TableCell>
                                    <TableCell>
                                        <Chip size="small" label={credential.secretHint || "stored"} variant="outlined"/>
                                    </TableCell>
                                    <TableCell>
                                        {credential.lastTestedAt ? (
                                            <Stack direction="row" spacing={1} sx={{alignItems: "center"}}>
                                                {credential.lastTestError
                                                    ? <ErrorOutlineOutlined color="warning" fontSize="small"/>
                                                    : <CheckCircleOutlined color="success" fontSize="small"/>}
                                                <Tooltip title={credential.lastTestError || "Accepted"}>
                                                    <span>{dateLabel(credential.lastTestedAt)}</span>
                                                </Tooltip>
                                            </Stack>
                                        ) : "—"}
                                    </TableCell>
                                    <TableCell align="right">
                                        <Button size="small" onClick={() => void test(credential)}
                                                disabled={testing === credential.uuid}>
                                            {testing === credential.uuid ? "Testing…" : "Test"}
                                        </Button>
                                        <Tooltip title="Edit">
                                            <IconButton size="small" onClick={() => openEdit(credential)}
                                                        aria-label={`Edit ${credential.registry}`}>
                                                <EditOutlined fontSize="small"/>
                                            </IconButton>
                                        </Tooltip>
                                        <Tooltip title="Remove">
                                            <IconButton size="small" onClick={() => setDeleting(credential)}
                                                        aria-label={`Remove ${credential.registry}`}>
                                                <DeleteOutlined fontSize="small"/>
                                            </IconButton>
                                        </Tooltip>
                                    </TableCell>
                                </TableRow>
                            ))}
                        </TableBody>
                    </Table>
                </TableContainer>
            )}

            <Dialog open={dialogOpen} onClose={() => setDialogOpen(false)} fullWidth maxWidth="sm">
                <DialogTitle>{editing ? `Edit ${editing.registry}` : "Add a registry"}</DialogTitle>
                <DialogContent>
                    <Stack spacing={2} sx={{mt: 1}}>
                        <TextField label="Registry" value={form.registry} fullWidth
                                   onChange={event => setForm({...form, registry: event.target.value})}
                                   placeholder="ghcr.io"
                                   helperText="Host only: ghcr.io, docker.io, registry.example.com:5000. A pasted repository URL keeps its host."/>
                        <TextField label="Username" value={form.username} fullWidth
                                   onChange={event => setForm({...form, username: event.target.value})}
                                   helperText="For GitHub, your account name; the token carries the permissions."/>
                        <TextField label={editing ? "New password or token" : "Password or token"}
                                   value={form.secret} fullWidth
                                   type={revealSecret ? "text" : "password"}
                                   onChange={event => setForm({...form, secret: event.target.value})}
                                   helperText={editing ? "Leave empty to keep the stored secret." : "Stored encrypted; it is never shown again."}
                                   slotProps={{
                                       input: {
                                           endAdornment: (
                                               <InputAdornment position="end">
                                                   <IconButton onClick={() => setRevealSecret(!revealSecret)}
                                                               aria-label={revealSecret ? "Hide the secret" : "Show the secret"}>
                                                       {revealSecret ? <VisibilityOff/> : <Visibility/>}
                                                   </IconButton>
                                               </InputAdornment>
                                           ),
                                       },
                                   }}/>
                    </Stack>
                </DialogContent>
                <DialogActions>
                    <Button onClick={() => void test(editing ?? undefined)}
                            disabled={testing !== null || form.registry.trim() === "" || form.username.trim() === ""}>
                        {testing ? "Testing…" : "Test"}
                    </Button>
                    <Box sx={{flexGrow: 1}}/>
                    <Button onClick={() => setDialogOpen(false)}>Cancel</Button>
                    <Button variant="contained" onClick={() => void save()} disabled={!canSave || saving}>
                        {saving ? "Saving…" : "Save"}
                    </Button>
                </DialogActions>
            </Dialog>

            <Dialog open={deleting !== null} onClose={() => setDeleting(null)}>
                <DialogTitle>Remove the credential for {deleting?.registry}?</DialogTitle>
                <DialogContent>
                    <Typography variant="body2">
                        Pulls from {deleting?.registry} become anonymous again, and stop working if that registry is private.
                    </Typography>
                </DialogContent>
                <DialogActions>
                    <Button onClick={() => setDeleting(null)}>Cancel</Button>
                    <Button color="error" variant="contained" onClick={() => void remove()}>Remove</Button>
                </DialogActions>
            </Dialog>
        </Box>
    );
}
