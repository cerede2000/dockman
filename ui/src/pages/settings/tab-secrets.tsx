import {useCallback, useEffect, useMemo, useState} from "react";
import {
    Alert, Box, Button, Chip, CircularProgress, Dialog, DialogActions, DialogContent, DialogTitle,
    IconButton, InputAdornment, Paper, Stack, Table, TableBody, TableCell, TableContainer, TableHead,
    TableRow, TextField, Tooltip, Typography,
} from "@mui/material";
import {Add, DeleteOutlined, History, KeyOutlined, Refresh, Restore, Visibility, VisibilityOff} from "@mui/icons-material";
import {getBaseUrl} from "../../lib/api.ts";
import {useHostStore} from "../compose/state/files.ts";
import {useSnackbar} from "../../hooks/snackbar.ts";
import TypedConfirmationField, {TYPED_CONFIRMATION} from "../../components/typed-confirmation-field.tsx";

interface RuntimeSecret {
    name: string;
    size: number;
    modifiedAt: string;
}

interface SecretForm {
    name: string;
    value: string;
}

interface SecretVersion { id: string; size: number; modifiedAt: string }
interface ArchivedSecret { name: string; versions: number }
interface ComposeSecretReference {
    name: string; file?: string; services: string[]; external: boolean; managed: boolean; exists: boolean; issue?: string;
}
interface ComposeAnalysis { manifests: string[]; secrets: ComposeSecretReference[] }

const initialForm: SecretForm = {name: "", value: ""};

async function responseError(response: Response): Promise<string> {
    const message = (await response.text()).trim();
    return message || `${response.status} ${response.statusText}`;
}

export default function TabSecrets() {
    const host = useHostStore(state => state.host) || "local";
    const {showError, showSuccess} = useSnackbar();
    const [stackPath, setStackPath] = useState("");
    const [loadedPath, setLoadedPath] = useState("");
    const [items, setItems] = useState<RuntimeSecret[]>([]);
    const [loading, setLoading] = useState(false);
    const [saving, setSaving] = useState(false);
    const [formOpen, setFormOpen] = useState(false);
    const [form, setForm] = useState<SecretForm>(initialForm);
    const [visible, setVisible] = useState(false);
    const [deleteItem, setDeleteItem] = useState<RuntimeSecret | null>(null);
    const [confirmation, setConfirmation] = useState("");
    const [analysis, setAnalysis] = useState<ComposeAnalysis | null>(null);
    const [analysisError, setAnalysisError] = useState("");
    const [historyItem, setHistoryItem] = useState<RuntimeSecret | null>(null);
    const [versions, setVersions] = useState<SecretVersion[]>([]);
    const [archived, setArchived] = useState<ArchivedSecret[]>([]);

    const base = useMemo(() => `${getBaseUrl("host", host)}/secrets`, [host]);

    const load = useCallback(async (requestedPath = stackPath.trim()) => {
        if (!requestedPath) {
            showError("Enter an alias-qualified stack directory, for example compose/myapp.");
            return;
        }
        setLoading(true);
        try {
            const [response, composeResponse, archivedResponse] = await Promise.all([
                fetch(`${base}/?stack=${encodeURIComponent(requestedPath)}`),
                fetch(`${base}/compose?stack=${encodeURIComponent(requestedPath)}`),
                fetch(`${base}/history?stack=${encodeURIComponent(requestedPath)}`),
            ]);
            if (!response.ok) throw new Error(await responseError(response));
            setItems(await response.json() as RuntimeSecret[]);
            if (composeResponse.ok) {
                setAnalysis(await composeResponse.json() as ComposeAnalysis);
                setAnalysisError("");
            } else {
                setAnalysis(null);
                setAnalysisError(await responseError(composeResponse));
            }
            setArchived(archivedResponse.ok ? await archivedResponse.json() as ArchivedSecret[] : []);
            setLoadedPath(requestedPath);
        } catch (error) {
            setItems([]);
            setLoadedPath("");
            setAnalysis(null);
            setArchived([]);
            showError(`Unable to load secrets: ${(error as Error).message}`);
        } finally {
            setLoading(false);
        }
    }, [base, showError, stackPath]);

    useEffect(() => {
        setItems([]);
        setLoadedPath("");
        setStackPath("");
        setAnalysis(null);
        setAnalysisError("");
        setArchived([]);
    }, [host]);

    const openCreate = () => {
        setForm(initialForm);
        setVisible(false);
        setFormOpen(true);
    };

    const openEdit = async (item: RuntimeSecret) => {
        if (!loadedPath) return;
        setSaving(true);
        try {
            const response = await fetch(`${base}/${encodeURIComponent(item.name)}?stack=${encodeURIComponent(loadedPath)}`);
            if (!response.ok) throw new Error(await responseError(response));
            const result = await response.json() as {value: string; encoding: string};
            if (result.encoding !== "base64") throw new Error("unsupported secret encoding");
            const bytes = Uint8Array.from(atob(result.value), char => char.charCodeAt(0));
            setForm({name: item.name, value: new TextDecoder().decode(bytes)});
            bytes.fill(0);
            setVisible(false);
            setFormOpen(true);
        } catch (error) {
            showError(`Unable to reveal secret: ${(error as Error).message}`);
        } finally {
            setSaving(false);
        }
    };

    const closeForm = () => {
        setForm(initialForm);
        setVisible(false);
        setFormOpen(false);
    };

    const openCreateNamed = (name: string) => {
        setForm({name, value: ""});
        setVisible(false);
        setFormOpen(true);
    };

    const openHistory = async (item: RuntimeSecret) => {
        setSaving(true);
        try {
            const response = await fetch(`${base}/${encodeURIComponent(item.name)}/history?stack=${encodeURIComponent(loadedPath)}`);
            if (!response.ok) throw new Error(await responseError(response));
            setVersions(await response.json() as SecretVersion[]);
            setHistoryItem(item);
        } catch (error) {
            showError(`Unable to load secret history: ${(error as Error).message}`);
        } finally {
            setSaving(false);
        }
    };

    const restoreVersion = async (version: SecretVersion) => {
        if (!historyItem) return;
        setSaving(true);
        try {
            const response = await fetch(`${base}/${encodeURIComponent(historyItem.name)}/history/${encodeURIComponent(version.id)}/restore?stack=${encodeURIComponent(loadedPath)}`, {method: "POST"});
            if (!response.ok) throw new Error(await responseError(response));
            setHistoryItem(null);
            setVersions([]);
            await load(loadedPath);
            showSuccess("Previous runtime secret version restored securely.");
        } catch (error) {
            showError(`Unable to restore secret: ${(error as Error).message}`);
        } finally {
            setSaving(false);
        }
    };

    const save = async () => {
        if (!loadedPath || !form.name.trim()) return;
        setSaving(true);
        try {
            const response = await fetch(`${base}/${encodeURIComponent(form.name.trim())}`, {
                method: "PUT",
                headers: {"Content-Type": "application/json"},
                body: JSON.stringify({stackPath: loadedPath, value: form.value, encoding: "utf-8"}),
            });
            if (!response.ok) throw new Error(await responseError(response));
            closeForm();
            await load(loadedPath);
            showSuccess("Runtime secret saved securely.");
        } catch (error) {
            showError(`Unable to save secret: ${(error as Error).message}`);
        } finally {
            setSaving(false);
        }
    };

    const remove = async () => {
        if (!deleteItem || !loadedPath || confirmation !== TYPED_CONFIRMATION) return;
        setSaving(true);
        try {
            const response = await fetch(`${base}/${encodeURIComponent(deleteItem.name)}?stack=${encodeURIComponent(loadedPath)}`, {method: "DELETE"});
            if (!response.ok) throw new Error(await responseError(response));
            setDeleteItem(null);
            setConfirmation("");
            await load(loadedPath);
            showSuccess("Runtime secret deleted.");
        } catch (error) {
            showError(`Unable to delete secret: ${(error as Error).message}`);
        } finally {
            setSaving(false);
        }
    };

    return <Box sx={{p: 3, maxWidth: 1100, mx: "auto"}}>
        <Stack direction={{xs: "column", md: "row"}} spacing={2} sx={{justifyContent: "space-between", mb: 2}}>
            <Box>
                <Typography variant="h5" sx={{fontWeight: 800}}>Compose secrets</Typography>
                <Typography variant="body2" color="text.secondary">
                    File-backed runtime secrets for <strong>{host}</strong>. Values stay with the selected Docker host and are never stored in Dockman&apos;s database.
                </Typography>
            </Box>
            <Chip icon={<KeyOutlined/>} color="success" variant="outlined" label="Plain files · ready"/>
        </Stack>
        <Alert severity="info" sx={{mb: 2}}>
            Enter a stack directory including its Dockman alias. Secrets are written to <code>.secrets/</code> with directory mode 0700 and file mode 0600. SOPS/age encrypted sources arrive in the next lot.
        </Alert>
        <Paper variant="outlined" sx={{p: 2, mb: 2}}>
            <Stack direction={{xs: "column", sm: "row"}} spacing={1}>
                <TextField fullWidth size="small" label="Stack directory" placeholder="compose/myapp" value={stackPath}
                           onChange={event => setStackPath(event.target.value)} onKeyDown={event => event.key === "Enter" && void load()}/>
                <Button variant="contained" startIcon={loading ? <CircularProgress size={16}/> : <Refresh/>} disabled={loading} onClick={() => void load()}>
                    Load
                </Button>
                <Button variant="outlined" startIcon={<Add/>} disabled={!loadedPath || loading} onClick={openCreate}>New secret</Button>
            </Stack>
        </Paper>
        <TableContainer component={Paper} variant="outlined">
            <Table size="small">
                <TableHead><TableRow><TableCell>Name</TableCell><TableCell>Size</TableCell><TableCell>Modified</TableCell><TableCell align="right">Actions</TableCell></TableRow></TableHead>
                <TableBody>
                    {items.map(item => <TableRow key={item.name} hover>
                        <TableCell sx={{fontFamily: "monospace", fontWeight: 650}}>{item.name}</TableCell>
                        <TableCell>{item.size} B</TableCell>
                        <TableCell>{new Date(item.modifiedAt).toLocaleString()}</TableCell>
                        <TableCell align="right">
                            <Tooltip title="Reveal or replace"><IconButton size="small" onClick={() => void openEdit(item)} disabled={saving}><Visibility/></IconButton></Tooltip>
                            <Tooltip title="Version history"><IconButton size="small" onClick={() => void openHistory(item)} disabled={saving}><History/></IconButton></Tooltip>
                            <Tooltip title="Delete"><IconButton size="small" color="error" onClick={() => {setDeleteItem(item); setConfirmation("");}} disabled={saving}><DeleteOutlined/></IconButton></Tooltip>
                        </TableCell>
                    </TableRow>)}
                    {!loading && loadedPath && items.length === 0 && <TableRow><TableCell colSpan={4} align="center" sx={{py: 5, color: "text.secondary"}}>No runtime secret in this stack.</TableCell></TableRow>}
                    {!loadedPath && <TableRow><TableCell colSpan={4} align="center" sx={{py: 5, color: "text.secondary"}}>Choose a stack directory to manage its secrets.</TableCell></TableRow>}
                </TableBody>
            </Table>
        </TableContainer>

        {archived.length > 0 && <Paper variant="outlined" sx={{p: 2, mt: 2}}>
            <Typography variant="subtitle1" sx={{fontWeight: 750}}>Recover deleted secrets</Typography>
            <Typography variant="body2" color="text.secondary" sx={{mb: 1}}>These values remain only in the bounded local history for this host.</Typography>
            <Stack direction="row" spacing={1} sx={{flexWrap: "wrap"}}>{archived.map(item => <Chip key={item.name} icon={<History/>} label={`${item.name} · ${item.versions}`} onClick={() => void openHistory({name: item.name, size: 0, modifiedAt: ""})}/>)}</Stack>
        </Paper>}

        {loadedPath && <Paper variant="outlined" sx={{p: 2, mt: 2}}>
            <Typography variant="subtitle1" sx={{fontWeight: 750, mb: 1}}>Compose references</Typography>
            {analysisError && <Alert severity="warning">Compose analysis is unavailable: {analysisError}</Alert>}
            {analysis && analysis.manifests.length === 0 && <Alert severity="info">No conventional Compose manifest was found at this stack root.</Alert>}
            {analysis && analysis.manifests.length > 0 && <>
                <Typography variant="caption" color="text.secondary">Analyzed: {analysis.manifests.join(", ")}</Typography>
                <Table size="small" sx={{mt: 1}}><TableHead><TableRow><TableCell>Secret</TableCell><TableCell>Services</TableCell><TableCell>Source</TableCell><TableCell>Status</TableCell><TableCell/></TableRow></TableHead>
                    <TableBody>{analysis.secrets.map(reference => <TableRow key={reference.name}>
                        <TableCell sx={{fontFamily: "monospace"}}>{reference.name}</TableCell>
                        <TableCell>{reference.services.join(", ") || "—"}</TableCell>
                        <TableCell sx={{fontFamily: "monospace"}}>{reference.external ? "external" : reference.file || "—"}</TableCell>
                        <TableCell><Chip size="small" color={reference.external || reference.exists ? "success" : reference.managed ? "warning" : "default"} variant="outlined" label={reference.external ? "external" : reference.exists ? "ready" : reference.issue || "unmanaged"}/></TableCell>
                        <TableCell align="right">{reference.managed && !reference.exists && <Button size="small" onClick={() => openCreateNamed(reference.name)}>Create</Button>}</TableCell>
                    </TableRow>)}</TableBody>
                </Table>
            </>}
        </Paper>}

        <Dialog open={formOpen} onClose={saving ? undefined : closeForm} fullWidth maxWidth="sm">
            <DialogTitle>{items.some(item => item.name === form.name) ? "Edit runtime secret" : "Create runtime secret"}</DialogTitle>
            <DialogContent><Stack spacing={2} sx={{mt: 1}}>
                <TextField label="Name" value={form.name} disabled={saving || items.some(item => item.name === form.name)}
                           helperText="Letters, numbers, dots, underscores and hyphens; maximum 128 characters."
                           onChange={event => setForm(current => ({...current, name: event.target.value}))}/>
                <TextField label="Value" multiline minRows={5} value={form.value}
                           onChange={event => setForm(current => ({...current, value: event.target.value}))}
                           sx={{"& .MuiInputBase-input": {WebkitTextSecurity: visible ? "none" : "disc"}}}
                           slotProps={{input: {endAdornment: <InputAdornment position="end"><IconButton aria-label={visible ? "Hide secret value" : "Reveal secret value"} onClick={() => setVisible(value => !value)}>{visible ? <VisibilityOff/> : <Visibility/>}</IconButton></InputAdornment>}}}/>
                <Alert severity="warning">The plaintext is returned only after an explicit reveal and is cleared from this dialog when it closes.</Alert>
            </Stack></DialogContent>
            <DialogActions><Button onClick={closeForm} disabled={saving}>Cancel</Button><Button variant="contained" onClick={() => void save()} disabled={saving || !form.name.trim()}>{saving ? <CircularProgress size={18}/> : "Save"}</Button></DialogActions>
        </Dialog>

        <Dialog open={historyItem !== null} onClose={saving ? undefined : () => setHistoryItem(null)} fullWidth maxWidth="sm">
            <DialogTitle>Version history · {historyItem?.name}</DialogTitle>
            <DialogContent><Stack spacing={1} sx={{mt: 1}}>
                <Alert severity="info">Dockman retains the last {3} replaced or deleted values on this host. Values are never displayed here.</Alert>
                {versions.map(version => <Paper variant="outlined" key={version.id} sx={{p: 1.25}}><Stack direction="row" spacing={1} sx={{alignItems: "center"}}>
                    <Box sx={{flex: 1}}><Typography>{new Date(version.modifiedAt).toLocaleString()}</Typography><Typography variant="caption" color="text.secondary">{version.size} B</Typography></Box>
                    <Button size="small" startIcon={<Restore/>} disabled={saving} onClick={() => void restoreVersion(version)}>Restore</Button>
                </Stack></Paper>)}
                {versions.length === 0 && <Typography color="text.secondary" sx={{py: 2, textAlign: "center"}}>No previous version is available.</Typography>}
            </Stack></DialogContent>
            <DialogActions><Button onClick={() => setHistoryItem(null)} disabled={saving}>Close</Button></DialogActions>
        </Dialog>

        <Dialog open={deleteItem !== null} onClose={saving ? undefined : () => setDeleteItem(null)} fullWidth maxWidth="xs">
            <DialogTitle>Delete runtime secret?</DialogTitle>
            <DialogContent><Stack spacing={2} sx={{mt: 1}}>
                <Alert severity="error">This permanently removes <strong>{deleteItem?.name}</strong> from host <strong>{host}</strong>. Containers already running keep their current mounted value until recreated.</Alert>
                <TypedConfirmationField value={confirmation} onChange={setConfirmation}/>
            </Stack></DialogContent>
            <DialogActions><Button onClick={() => setDeleteItem(null)} disabled={saving}>Cancel</Button><Button color="error" variant="contained" onClick={() => void remove()} disabled={saving || confirmation !== TYPED_CONFIRMATION}>Delete</Button></DialogActions>
        </Dialog>
    </Box>;
}
