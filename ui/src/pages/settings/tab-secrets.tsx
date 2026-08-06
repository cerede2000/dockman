import {useCallback, useEffect, useMemo, useState} from "react";
import {
    Alert, Autocomplete, Box, Button, Chip, CircularProgress, Dialog, DialogActions, DialogContent, DialogTitle,
    IconButton, InputAdornment, Paper, Stack, Table, TableBody, TableCell, TableContainer, TableHead,
    TableRow, TextField, Tooltip, Typography,
} from "@mui/material";
import {Add, ContentCopy, DeleteOutlined, Download, History, KeyOutlined, LockOutlined, Refresh, Restore, SettingsSuggest, Upload, Visibility, VisibilityOff} from "@mui/icons-material";
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
    name: string; file?: string; environment?: string; runtimeName?: string; services: string[]; readOnlyServices?: string[]; external: boolean; managed: boolean; exists: boolean; issue?: string;
}
interface ComposeAnalysis { manifests: string[]; secrets: ComposeSecretReference[] }
interface StackOption { path: string; alias: string; manifests: string[] }
interface SOPSStatus { available: boolean; sourcePath: string; sourceExists: boolean; mode: "materialized" | "inline"; recoveryScript?: string; recipient?: string; issue?: string }

const initialForm: SecretForm = {name: "", value: ""};
const shellQuote = (value: string) => `'${value.replaceAll("'", `'"'"'`)}'`;

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
    const [stackOptions, setStackOptions] = useState<StackOption[]>([]);
    const [catalogLoading, setCatalogLoading] = useState(false);
    const [sopsStatus, setSopsStatus] = useState<SOPSStatus | null>(null);
    const [sopsAction, setSopsAction] = useState<"export" | "materialize" | "inline-enable" | null>(null);
    const [hostWizardOpen, setHostWizardOpen] = useState(false);
    const [hostContainer, setHostContainer] = useState("dockman");
    const [hostStackRoot, setHostStackRoot] = useState("/server/stacks");
    const [containerAgeKey, setContainerAgeKey] = useState("/config/secrets/dockman-sops-age-key.txt");
    const [hostAgeKey, setHostAgeKey] = useState("/etc/dockman-secrets/age-key.txt");
    const [hostSshTarget, setHostSshTarget] = useState("");
    const [hostFileMode, setHostFileMode] = useState("0444");

    const base = useMemo(() => `${getBaseUrl("host", host)}/secrets`, [host]);
    const hostInstallCommand = useMemo(() => {
        const container = hostContainer.trim() || "dockman";
        const agePath = hostAgeKey.trim();
        const ageDirectory = agePath.includes("/") ? agePath.slice(0, agePath.lastIndexOf("/")) || "/" : ".";
        const prepare = `set -eu
umask 077
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
docker cp ${shellQuote(`${container}:/usr/local/libexec/dockman-secrets-host`)} "$workdir/dockman-secrets-host"
docker cp ${shellQuote(`${container}:/usr/local/bin/sops`)} "$workdir/sops"
docker cp ${shellQuote(`${container}:${containerAgeKey.trim()}`)} "$workdir/age-key.txt"
chmod 0600 "$workdir/age-key.txt"
chmod 0755 "$workdir/dockman-secrets-host" "$workdir/sops"`;
        if (host !== "local") {
            const target = hostSshTarget.trim() || "USER@REMOTE_HOST";
            return `${prepare}
remote_tmp="$(ssh ${shellQuote(target)} mktemp -d)"
scp "$workdir/dockman-secrets-host" "$workdir/sops" "$workdir/age-key.txt" ${shellQuote(target)}:"$remote_tmp/"
ssh -t ${shellQuote(target)} sudo install -d -m 0700 ${shellQuote(ageDirectory)}
ssh -t ${shellQuote(target)} sudo install -m 0600 "$remote_tmp/age-key.txt" ${shellQuote(agePath)}
status=0
ssh -t ${shellQuote(target)} sudo "$remote_tmp/dockman-secrets-host" install --stack-root ${shellQuote(hostStackRoot.trim())} --age-key ${shellQuote(agePath)} --sops-source "$remote_tmp/sops" --file-mode ${shellQuote(hostFileMode)} --activate || status=$?
ssh ${shellQuote(target)} rm -rf "$remote_tmp"
exit "$status"`;
        }
        return `${prepare}
sudo install -d -m 0700 ${shellQuote(ageDirectory)}
sudo install -m 0600 "$workdir/age-key.txt" ${shellQuote(agePath)}
sudo "$workdir/dockman-secrets-host" install \
  --stack-root ${shellQuote(hostStackRoot.trim())} \
  --age-key ${shellQuote(agePath)} \
  --sops-source "$workdir/sops" \
  --file-mode ${shellQuote(hostFileMode)} \
  --activate
sudo systemctl --no-pager status dockman-secrets-host.service`;
    }, [containerAgeKey, host, hostAgeKey, hostContainer, hostFileMode, hostSshTarget, hostStackRoot]);

    const copyHostCommand = async () => {
        try {
            if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(hostInstallCommand);
            else {
                const area = document.createElement("textarea");
                area.value = hostInstallCommand;
                area.style.position = "fixed";
                area.style.opacity = "0";
                document.body.appendChild(area);
                area.select();
                document.execCommand("copy");
                area.remove();
            }
            showSuccess("Host installation command copied.");
        } catch (error) {
            showError(`Unable to copy the command: ${(error as Error).message}`);
        }
    };

    const loadStackOptions = useCallback(async () => {
        setCatalogLoading(true);
        try {
            const response = await fetch(`${base}/stacks`);
            if (!response.ok) throw new Error(await responseError(response));
            setStackOptions(await response.json() as StackOption[]);
        } catch (error) {
            setStackOptions([]);
            showError(`Unable to discover Compose stacks: ${(error as Error).message}`);
        } finally {
            setCatalogLoading(false);
        }
    }, [base, showError]);

    const load = useCallback(async (requestedPath = stackPath.trim()) => {
        if (!requestedPath) {
            showError("Enter an alias-qualified stack directory, for example compose/myapp.");
            return;
        }
        setLoading(true);
        try {
            const [response, composeResponse, archivedResponse, sopsResponse] = await Promise.all([
                fetch(`${base}/?stack=${encodeURIComponent(requestedPath)}`),
                fetch(`${base}/compose?stack=${encodeURIComponent(requestedPath)}`),
                fetch(`${base}/history?stack=${encodeURIComponent(requestedPath)}`),
                fetch(`${base}/sops?stack=${encodeURIComponent(requestedPath)}`),
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
            setSopsStatus(sopsResponse.ok ? await sopsResponse.json() as SOPSStatus : null);
            setLoadedPath(requestedPath);
        } catch (error) {
            setItems([]);
            setLoadedPath("");
            setAnalysis(null);
            setArchived([]);
            setSopsStatus(null);
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
        setSopsStatus(null);
        setStackOptions([]);
        void loadStackOptions();
    }, [host, loadStackOptions]);

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

    const runSOPS = async () => {
        if (!sopsAction || !loadedPath || confirmation !== TYPED_CONFIRMATION) return;
        const action = sopsAction;
        setSaving(true);
        try {
            const endpoint = action === "inline-enable" ? "sops/inline/enable" : `sops/${action}`;
            const response = await fetch(`${base}/${endpoint}`, {
                method: "POST",
                headers: {"Content-Type": "application/json"},
                body: JSON.stringify({stackPath: loadedPath, composeFile: analysis?.manifests[0] || "compose.yml"}),
            });
            if (!response.ok) throw new Error(await responseError(response));
            const result = await response.json() as {names: string[]};
            setSopsAction(null);
            setConfirmation("");
            await load(loadedPath);
            showSuccess(action === "export" ? `${result.names.length} runtime secret(s) encrypted into secrets.sops.yaml.`
                : action === "materialize" ? `${result.names.length} encrypted secret(s) materialized securely.`
                    : `${result.names.length} secret(s) now stay encrypted at rest. Install or refresh the host boot runtime before recreating file-secret consumers.`);
        } catch (error) {
            showError(`Unable to ${action} SOPS secrets: ${(error as Error).message}`);
        } finally {
            setSaving(false);
        }
    };

    return <Box sx={{p: 3, maxWidth: 1100, mx: "auto"}}>
        <Stack direction={{xs: "column", md: "row"}} spacing={2} sx={{justifyContent: "space-between", mb: 2}}>
            <Box>
                <Typography variant="h5" sx={{fontWeight: 800}}>Compose secrets</Typography>
                <Typography variant="body2" color="text.secondary">
                    Portable Compose secrets for <strong>{host}</strong>. Values stay with the selected host and are never stored in Dockman&apos;s database.
                </Typography>
            </Box>
            <Stack direction="row" spacing={1}>
                <Chip icon={sopsStatus?.mode === "inline" ? <LockOutlined/> : <KeyOutlined/>} color={sopsStatus?.mode === "inline" ? "success" : "warning"} variant="outlined"
                      label={sopsStatus?.mode === "inline" ? "Encrypted runtime · active" : "Migration mode · plaintext files"}/>
                <Chip icon={<LockOutlined/>}
                      color={sopsStatus?.available ? "success" : "default"}
                      variant="outlined"
                      label={!loadedPath ? "SOPS/age · select a stack" : sopsStatus?.available ? "SOPS/age · ready" : "SOPS/age · not configured"}/>
            </Stack>
        </Stack>
        <Alert severity="info" sx={{mb: 2}}>
            {sopsStatus?.mode === "inline" ? <><code>secrets.sops.yaml</code> is the only persistent store. File delivery uses host tmpfs and inline delivery decrypts only for an explicit Compose action.</> : <>Migration mode still writes runtime values to persistent <code>.secrets/</code>. Encrypt and enable the autonomous runtime to reach the ciphertext-only target.</>}
        </Alert>
        <Paper variant="outlined" sx={{p: 2, mb: 2}}>
            <Stack direction={{xs: "column", sm: "row"}} spacing={1}>
                <Autocomplete freeSolo fullWidth options={stackOptions} inputValue={stackPath}
                              loading={catalogLoading}
                              groupBy={option => option.alias}
                              getOptionLabel={option => typeof option === "string" ? option : option.path}
                              onInputChange={(_, value) => setStackPath(value)}
                              onChange={(_, value) => {
                                  const path = typeof value === "string" ? value : value?.path || "";
                                  setStackPath(path);
                                  if (path) void load(path);
                              }}
                              renderOption={(props, option) => <li {...props} key={typeof option === "string" ? option : option.path}>
                                  <Box><Typography variant="body2" sx={{fontFamily: "monospace"}}>{typeof option === "string" ? option : option.path}</Typography>
                                      {typeof option !== "string" && <Typography variant="caption" color="text.secondary">{option.manifests.join(", ")}</Typography>}
                                  </Box>
                              </li>}
                              renderInput={params => <TextField {...params} size="small" label="Compose stack" placeholder="Select or enter compose/myapp"
                                                                onKeyDown={event => event.key === "Enter" && stackPath.trim() && void load()}
                                                                slotProps={{...params.slotProps, input: {...params.slotProps.input, endAdornment: <>{catalogLoading && <CircularProgress size={16}/>} {params.slotProps.input?.endAdornment}</>}}}/>} />
                <Tooltip title="Refresh stack list"><span><IconButton disabled={catalogLoading} onClick={() => void loadStackOptions()}>{catalogLoading ? <CircularProgress size={18}/> : <Refresh/>}</IconButton></span></Tooltip>
                <Button variant="contained" startIcon={loading ? <CircularProgress size={16}/> : <Refresh/>} disabled={loading} onClick={() => void load()}>
                    Load
                </Button>
                <Button variant="outlined" startIcon={<Add/>} disabled={!loadedPath || loading} onClick={openCreate}>New secret</Button>
                <Button variant="outlined" startIcon={<SettingsSuggest/>} onClick={() => setHostWizardOpen(true)}>Host boot wizard</Button>
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
                            <Tooltip title={sopsStatus?.mode === "inline" ? "Inline values have no plaintext history" : "Version history"}><span><IconButton size="small" onClick={() => void openHistory(item)} disabled={saving || sopsStatus?.mode === "inline"}><History/></IconButton></span></Tooltip>
                            <Tooltip title="Delete"><IconButton size="small" color="error" onClick={() => {setDeleteItem(item); setConfirmation("");}} disabled={saving}><DeleteOutlined/></IconButton></Tooltip>
                        </TableCell>
                    </TableRow>)}
                    {!loading && loadedPath && items.length === 0 && <TableRow><TableCell colSpan={4} align="center" sx={{py: 5, color: "text.secondary"}}>No runtime secret in this stack.</TableCell></TableRow>}
                    {!loadedPath && <TableRow><TableCell colSpan={4} align="center" sx={{py: 5, color: "text.secondary"}}>Choose a stack directory to manage its secrets.</TableCell></TableRow>}
                </TableBody>
            </Table>
        </TableContainer>

        {loadedPath && <Paper variant="outlined" sx={{p: 2, mt: 2}}>
            <Stack direction={{xs: "column", md: "row"}} spacing={2} sx={{alignItems: {md: "center"}}}>
                <Box sx={{flex: 1}}>
                    <Typography variant="subtitle1" sx={{fontWeight: 750}}>Encrypted source · SOPS/age</Typography>
                    <Typography variant="body2" color="text.secondary">
                        {sopsStatus?.sourceExists ? <><code>secrets.sops.yaml</code> is present for this stack.</> : <>No encrypted source exists for this stack.</>}
                    </Typography>
                    {!sopsStatus?.available && <Typography variant="caption" color="warning.main">{sopsStatus?.issue || "SOPS/age is not configured on this Dockman instance."}</Typography>}
                    {sopsStatus?.available && sopsStatus.recipient && <Typography component="div" variant="caption" color="text.secondary" sx={{overflowWrap: "anywhere"}}>Recipient: <code>{sopsStatus.recipient}</code></Typography>}
                    {sopsStatus?.mode === "inline" && <Typography component="div" variant="caption" color="success.main">Inline encrypted-at-rest mode · recovery: <code>{sopsStatus.recoveryScript || "compose-sops.sh"}</code></Typography>}
                </Box>
                <Stack direction={{xs: "column", sm: "row"}} spacing={1}>
                    {sopsStatus?.mode !== "inline" && <Button variant="outlined" startIcon={<Upload/>} disabled={saving || !sopsStatus?.available || items.length === 0}
                            onClick={() => {setSopsAction("export"); setConfirmation("");}}>Encrypt runtime</Button>}
                    {sopsStatus?.mode !== "inline" && <Button variant="contained" startIcon={<Download/>} disabled={saving || !sopsStatus?.available || !sopsStatus.sourceExists}
                            onClick={() => {setSopsAction("materialize"); setConfirmation("");}}>Materialize source</Button>}
                    {sopsStatus?.mode !== "inline" && <Button color="success" variant="contained" startIcon={<LockOutlined/>}
                            disabled={saving || !sopsStatus?.available || items.length === 0 || !analysis?.manifests.length || analysis.secrets.some(reference => Boolean(reference.file && reference.services.length > 0 && !reference.managed) || Boolean(reference.environment && (reference.issue || reference.services.length > 0 && !items.some(item => item.name === reference.environment))))}
                            onClick={() => {setSopsAction("inline-enable"); setConfirmation("");}}>Enable encrypted runtime</Button>}
                </Stack>
                {host !== "local" && <TextField fullWidth size="small" label="SSH target used from the Dockman host" placeholder="user@remote-host" value={hostSshTarget} onChange={event => setHostSshTarget(event.target.value)} helperText="The command exports the signed-in Dockman image tools, transfers them over your SSH client, then installs the runtime on the selected remote host."/>}
            </Stack>
            <Alert severity="info" sx={{mt: 1.5}}>{sopsStatus?.mode === "inline"
                ? "Every create, edit and delete rewrites and decrypt-verifies the encrypted source atomically. Compose actions receive values in their process environment only."
                : "Materialization replaces matching runtime values atomically one by one. Enabling inline verifies the ciphertext before removing .secrets and its plaintext history."}</Alert>
            {sopsStatus?.mode !== "inline" && analysis?.secrets.some(reference => reference.managed) && <Alert severity="warning" sx={{mt: 1}}>
                File secrets remain compatible through <code>file: ./.secrets/token</code>, but the host boot runtime must be installed first. It recreates <code>.secrets</code> as tmpfs before Docker starts; no plaintext persists on disk.
            </Alert>}
        </Paper>}

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
                        <TableCell sx={{fontFamily: "monospace"}}>{reference.external ? "external" : reference.environment ? `environment: ${reference.environment}` : reference.file || "—"}</TableCell>
                        <TableCell><Tooltip title={reference.issue || (reference.environment && reference.services.length > 0 && !items.some(item => item.name === reference.environment) ? `Create ${reference.environment} before enabling inline mode.` : "")}><Chip size="small" color={reference.issue || reference.environment && reference.services.length > 0 && !items.some(item => item.name === reference.environment) ? "error" : reference.external || reference.environment || reference.exists ? "success" : reference.managed ? "warning" : "default"} variant="outlined" label={reference.issue ? "invalid" : reference.environment && reference.services.length > 0 && !items.some(item => item.name === reference.environment) ? "missing encrypted value" : reference.external ? "external" : reference.environment ? "encrypted inline → file" : reference.exists ? "ready" : reference.managed ? "missing" : "not managed"}/></Tooltip></TableCell>
                        <TableCell align="right">{((reference.managed && !reference.exists) || (reference.environment && !items.some(item => item.name === reference.environment))) && <Button size="small" onClick={() => openCreateNamed(reference.runtimeName || reference.name)}>Create</Button>}</TableCell>
                    </TableRow>)}</TableBody>
                </Table>
            </>}
        </Paper>}

        <Dialog open={formOpen} onClose={saving ? undefined : closeForm} fullWidth maxWidth="sm">
            <DialogTitle>{items.some(item => item.name === form.name) ? "Edit runtime secret" : "Create runtime secret"}</DialogTitle>
            <DialogContent><Stack spacing={2} sx={{mt: 1}}>
                <TextField label="Name" value={form.name} disabled={saving || items.some(item => item.name === form.name)}
                           helperText={sopsStatus?.mode === "inline" ? "File names may use dots or hyphens. Inline environment consumers require a name such as API_TOKEN." : "Letters, numbers, dots, underscores and hyphens; maximum 128 characters."}
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

        <Dialog open={sopsAction !== null} onClose={saving ? undefined : () => setSopsAction(null)} fullWidth maxWidth="xs">
            <DialogTitle>{sopsAction === "export" ? "Encrypt runtime secrets?" : sopsAction === "materialize" ? "Materialize encrypted secrets?" : "Enable encrypted runtime?"}</DialogTitle>
            <DialogContent><Stack spacing={2} sx={{mt: 1}}>
                <Alert severity={sopsAction === "export" || sopsAction === "inline-enable" ? "warning" : "info"}>
                    {sopsAction === "export"
                        ? "This replaces secrets.sops.yaml with every current runtime secret after encrypting and verifying it with the configured age identity."
                        : sopsAction === "materialize" ? "This decrypts secrets.sops.yaml in memory and replaces matching files under .secrets. Values absent from the source are preserved."
                            : `Dockman encrypts and verifies every value, supports both file: ./.secrets/name and inline environment references, creates compose-sops.sh for independent recovery, then removes persistent .secrets and its plaintext history. The host boot runtime must be installed before file-secret services are recreated. Compose manifest: ${analysis?.manifests[0] || "compose.yml"}.`}
                </Alert>
                <TypedConfirmationField value={confirmation} onChange={setConfirmation}/>
            </Stack></DialogContent>
            <DialogActions><Button onClick={() => setSopsAction(null)} disabled={saving}>Cancel</Button><Button variant="contained" onClick={() => void runSOPS()} disabled={saving || confirmation !== TYPED_CONFIRMATION}>{saving ? <CircularProgress size={18}/> : "Confirm"}</Button></DialogActions>
        </Dialog>

        <Dialog open={hostWizardOpen} onClose={() => setHostWizardOpen(false)} fullWidth maxWidth="md">
            <DialogTitle>Autonomous encrypted secret runtime</DialogTitle>
            <DialogContent><Stack spacing={2} sx={{mt: 1}}>
                <Alert severity="info">Run this once on each Docker host. The installed one-shot systemd service decrypts into per-stack tmpfs mounts before Docker starts. It has no daemon, polling, database dependency or idle overhead.</Alert>
                <Stack direction={{xs: "column", sm: "row"}} spacing={1}>
                    <TextField fullWidth size="small" label="Dockman container name" value={hostContainer} onChange={event => setHostContainer(event.target.value)}/>
                    <TextField fullWidth size="small" label="Host stack root" value={hostStackRoot} onChange={event => setHostStackRoot(event.target.value)}/>
                </Stack>
                <Stack direction={{xs: "column", sm: "row"}} spacing={1}>
                    <TextField fullWidth size="small" label="Age key inside Dockman" value={containerAgeKey} onChange={event => setContainerAgeKey(event.target.value)}/>
                    <TextField fullWidth size="small" label="Protected age key on host" value={hostAgeKey} onChange={event => setHostAgeKey(event.target.value)}/>
                    <TextField sx={{minWidth: 150}} size="small" label="File mode" value={hostFileMode} onChange={event => setHostFileMode(event.target.value)} helperText="0444 for non-root images"/>
                </Stack>
                <TextField multiline minRows={12} maxRows={18} value={hostInstallCommand} slotProps={{input: {readOnly: true}}}/>
                <Alert severity="warning">The command copies the existing identity with mode 0600; it never prints it. Back this key up separately. Losing it makes every encrypted source unreadable. Configure the Dockman stack bind as <code>{`${hostStackRoot}:${hostStackRoot}:rslave`}</code>, then recreate Dockman once. The one-way rslave propagation makes new host tmpfs mounts visible without granting mount propagation back to the host.</Alert>
                <Typography variant="body2" color="text.secondary">Recovery without Dockman: run <code>sudo systemctl restart dockman-secrets-host</code>, then execute <code>./compose-sops.sh up</code> in the stack directory. For a remote host, run the generated command on that host with its own identity.</Typography>
            </Stack></DialogContent>
            <DialogActions><Button onClick={() => setHostWizardOpen(false)}>Close</Button><Button variant="contained" startIcon={<ContentCopy/>} onClick={() => void copyHostCommand()}>Copy command</Button></DialogActions>
        </Dialog>
    </Box>;
}
