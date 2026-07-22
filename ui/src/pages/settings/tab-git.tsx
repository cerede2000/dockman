import {useCallback, useEffect, useMemo, useState} from "react";
import {
    Alert, Box, Button, Chip, CircularProgress, Dialog, DialogActions, DialogContent,
    DialogTitle, FormControl, FormControlLabel, IconButton, InputLabel, MenuItem, Paper,
    Select, Stack, Switch, Table, TableBody, TableCell, TableContainer, TableHead, TableRow,
    TextField, Tooltip, Typography,
} from "@mui/material";
import {
    Add, CloudDownloadOutlined, CloudUploadOutlined, CompareArrowsOutlined, DeleteOutlined, EditOutlined,
    FolderOpenOutlined, HistoryOutlined, KeyOutlined, LinkOutlined, RefreshOutlined, SyncOutlined,
} from "@mui/icons-material";
import {withProtectedAPI} from "../../lib/api.ts";
import {useSnackbar} from "../../hooks/snackbar.ts";

type AuthType = "public" | "https_token" | "ssh_key";
type RepositoryDialogMode = "import" | "github";

interface GitFeatureStatus {
    enabled: boolean;
    phase: string;
    repositorySyncAvailable: boolean;
    stackSyncAvailable: boolean;
}

interface Credential {
    id: string;
    name: string;
    authType: AuthType;
    username?: string;
    hasSecret: boolean;
    secretHint?: string;
}

interface CredentialForm {
    name: string;
    authType: AuthType;
    username: string;
    token: string;
    privateKey: string;
    passphrase: string;
}

interface Repository {
    id: string;
    name: string;
    provider: string;
    remoteUrl: string;
    defaultBranch: string;
    credentialId?: string;
    status: string;
    lastError?: string;
    lastFetchedAt?: string;
    workspacePresent: boolean;
}

interface RepositoryStatus {
    repositoryId: string;
    branch: string;
    head?: string;
    remoteHead?: string;
    clean: boolean;
    ahead: number;
    behind: number;
    diverged: boolean;
    state: string;
}

interface Operation {
    id: string;
    type: string;
    state: string;
    startedAt?: string;
    finishedAt?: string;
    error?: string;
}

interface RepositoryForm {
    mode: RepositoryDialogMode;
    name: string;
    remoteUrl: string;
    defaultBranch: string;
    credentialId: string;
    description: string;
    private: boolean;
}

interface StackTarget { host: string; path: string; composePaths: string[]; }
interface Binding {
    id: string; repositoryId: string; repositoryName: string; host: string; stackPath: string;
    subPath: string; composePaths: string[]; enabled: boolean;
}
interface PreviewEntry {
    path: string; status: "add" | "modify" | "skipped_sensitive"; sourceSha?: string;
    targetSha?: string; size?: number; sensitive?: boolean;
}
interface TransferPreview {
    bindingId: string; direction: TransferDirection; entries: PreviewEntry[]; changed: number;
    unchanged: number; skipped: number; deletionMode: string;
    previewToken: string;
}
interface TransferResult { preview: TransferPreview; commitSha?: string; backup?: string; message: string; }
type TransferDirection = "stack_to_repository" | "repository_to_stack";

const emptyCredential: CredentialForm = {
    name: "", authType: "public", username: "", token: "", privateKey: "", passphrase: "",
};

const emptyRepository: RepositoryForm = {
    mode: "import", name: "", remoteUrl: "", defaultBranch: "main", credentialId: "",
    description: "", private: true,
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

function statusColor(state?: string): "default" | "success" | "warning" | "error" | "info" {
    if (state === "up-to-date") return "success";
    if (state === "ahead" || state === "behind" || state === "local-only") return "info";
    if (state === "diverged" || state === "dirty") return "warning";
    if (state === "error") return "error";
    return "default";
}

function dateLabel(value?: string) {
    return value ? new Date(value).toLocaleString() : "—";
}

export default function TabGit() {
    const {showError, showSuccess} = useSnackbar();
    const [feature, setFeature] = useState<GitFeatureStatus | null>(null);
    const [credentials, setCredentials] = useState<Credential[]>([]);
    const [repositories, setRepositories] = useState<Repository[]>([]);
    const [repositoryStatuses, setRepositoryStatuses] = useState<Record<string, RepositoryStatus>>({});
    const [loading, setLoading] = useState(true);
    const [busy, setBusy] = useState<string | null>(null);

    const [credentialDialogOpen, setCredentialDialogOpen] = useState(false);
    const [editingCredential, setEditingCredential] = useState<Credential | null>(null);
    const [credentialForm, setCredentialForm] = useState<CredentialForm>(emptyCredential);
    const [repositoryUrl, setRepositoryUrl] = useState("");
    const [deleteCredential, setDeleteCredential] = useState<Credential | null>(null);

    const [repositoryDialogOpen, setRepositoryDialogOpen] = useState(false);
    const [repositoryForm, setRepositoryForm] = useState<RepositoryForm>(emptyRepository);
    const [deleteRepository, setDeleteRepository] = useState<Repository | null>(null);
    const [historyRepository, setHistoryRepository] = useState<Repository | null>(null);
    const [operations, setOperations] = useState<Operation[]>([]);
    const [bindings, setBindings] = useState<Binding[]>([]);
    const [stackTargets, setStackTargets] = useState<StackTarget[]>([]);
    const [bindingDialogOpen, setBindingDialogOpen] = useState(false);
    const [bindingForm, setBindingForm] = useState({repositoryId: "", host: "", stackPath: "", subPath: "."});
    const [transferBinding, setTransferBinding] = useState<Binding | null>(null);
    const [transferDirection, setTransferDirection] = useState<TransferDirection>("stack_to_repository");
    const [transferPreview, setTransferPreview] = useState<TransferPreview | null>(null);
    const [includeSensitive, setIncludeSensitive] = useState(false);
    const [sensitiveConfirmation, setSensitiveConfirmation] = useState("");
    const [commitMessage, setCommitMessage] = useState("");
    const [deleteBinding, setDeleteBinding] = useState<Binding | null>(null);

    const loadRepositoryStatuses = useCallback(async (rows: Repository[]) => {
        const pairs = await Promise.all(rows.filter((row) => row.workspacePresent).map(async (row) => {
            try {
                return [row.id, await api<RepositoryStatus>(`/repositories/${row.id}/status`)] as const;
            } catch {
                return null;
            }
        }));
        setRepositoryStatuses(Object.fromEntries(pairs.filter((pair): pair is readonly [string, RepositoryStatus] => pair !== null)));
    }, []);

    const load = useCallback(async () => {
        setLoading(true);
        try {
            const nextFeature = await api<GitFeatureStatus>("/status");
            setFeature(nextFeature);
            if (!nextFeature.enabled) {
                setCredentials([]);
                setRepositories([]);
                setBindings([]);
                setStackTargets([]);
                return;
            }
            const [nextCredentials, nextRepositories, nextBindings, nextTargets] = await Promise.all([
                api<Credential[]>("/credentials"), api<Repository[]>("/repositories"),
                api<Binding[]>("/bindings"), api<StackTarget[]>("/stack-targets"),
            ]);
            setCredentials(nextCredentials);
            setRepositories(nextRepositories);
            setBindings(nextBindings);
            setStackTargets(nextTargets);
            await loadRepositoryStatuses(nextRepositories);
        } catch (error) {
            showError(`Unable to load Git settings: ${(error as Error).message}`);
        } finally {
            setLoading(false);
        }
    }, [loadRepositoryStatuses, showError]);

    useEffect(() => { void load(); }, [load]);

    const credentialNames = useMemo(() => Object.fromEntries(credentials.map((item) => [item.id, item.name])), [credentials]);

    const openCredentialCreate = () => {
        setEditingCredential(null);
        setCredentialForm(emptyCredential);
        setCredentialDialogOpen(true);
    };

    const openCredentialEdit = (credential: Credential) => {
        setEditingCredential(credential);
        setCredentialForm({
            name: credential.name, authType: credential.authType, username: credential.username || "",
            token: "", privateKey: "", passphrase: "",
        });
        setCredentialDialogOpen(true);
    };

    const saveCredential = async () => {
        setBusy("credential-save");
        try {
            const path = editingCredential ? `/credentials/${editingCredential.id}` : "/credentials";
            await api<Credential>(path, {method: editingCredential ? "PUT" : "POST", body: JSON.stringify(credentialForm)});
            showSuccess(editingCredential ? "Git credential updated." : "Git credential created.");
            setCredentialDialogOpen(false);
            await load();
        } catch (error) {
            showError((error as Error).message);
        } finally {
            setBusy(null);
        }
    };

    const testCredential = async (credential: Credential) => {
        setBusy(`credential-test-${credential.id}`);
        try {
            const result = await api<{message: string}>(`/credentials/${credential.id}/test`, {
                method: "POST", body: JSON.stringify({repositoryUrl: repositoryUrl.trim()}),
            });
            showSuccess(result.message);
        } catch (error) {
            showError((error as Error).message);
        } finally {
            setBusy(null);
        }
    };

    const confirmDeleteCredential = async () => {
        if (!deleteCredential) return;
        setBusy(`credential-delete-${deleteCredential.id}`);
        try {
            await api<void>(`/credentials/${deleteCredential.id}`, {method: "DELETE"});
            showSuccess("Git credential deleted.");
            setDeleteCredential(null);
            await load();
        } catch (error) {
            showError((error as Error).message);
        } finally {
            setBusy(null);
        }
    };

    const openRepositoryCreate = () => {
        setRepositoryForm(emptyRepository);
        setRepositoryDialogOpen(true);
    };

    const saveRepository = async () => {
        setBusy("repository-save");
        try {
            if (repositoryForm.mode === "import") {
                await api<Repository>("/repositories", {
                    method: "POST",
                    body: JSON.stringify({
                        name: repositoryForm.name, remoteUrl: repositoryForm.remoteUrl,
                        defaultBranch: repositoryForm.defaultBranch, credentialId: repositoryForm.credentialId,
                    }),
                });
                showSuccess("Repository imported into Dockman.");
            } else {
                await api<Repository>("/repositories/github", {
                    method: "POST",
                    body: JSON.stringify({
                        name: repositoryForm.name, description: repositoryForm.description,
                        private: repositoryForm.private, defaultBranch: repositoryForm.defaultBranch,
                        credentialId: repositoryForm.credentialId,
                    }),
                });
                showSuccess("GitHub repository created and imported.");
            }
            setRepositoryDialogOpen(false);
            await load();
        } catch (error) {
            showError((error as Error).message);
            await load();
        } finally {
            setBusy(null);
        }
    };

    const repositoryAction = async (repository: Repository, action: "fetch" | "pull" | "push") => {
        setBusy(`${action}-${repository.id}`);
        try {
            const next = await api<RepositoryStatus>(`/repositories/${repository.id}/${action}`, {method: "POST"});
            setRepositoryStatuses((current) => ({...current, [repository.id]: next}));
            showSuccess(`${action[0].toUpperCase()}${action.slice(1)} completed for ${repository.name}.`);
            await load();
        } catch (error) {
            showError((error as Error).message);
            await load();
        } finally {
            setBusy(null);
        }
    };

    const openHistory = async (repository: Repository) => {
        setHistoryRepository(repository);
        setOperations([]);
        try {
            setOperations(await api<Operation[]>(`/repositories/${repository.id}/operations?limit=50`));
        } catch (error) {
            showError((error as Error).message);
        }
    };

    const confirmDeleteRepository = async () => {
        if (!deleteRepository) return;
        setBusy(`repository-delete-${deleteRepository.id}`);
        try {
            await api<void>(`/repositories/${deleteRepository.id}`, {method: "DELETE"});
            showSuccess("Local repository workspace deleted. The GitHub repository was not modified.");
            setDeleteRepository(null);
            await load();
        } catch (error) {
            showError((error as Error).message);
        } finally {
            setBusy(null);
        }
    };

    const openBindingCreate = () => {
        const first = stackTargets[0];
        setBindingForm({repositoryId: repositories[0]?.id || "", host: first?.host || "", stackPath: first?.path || "", subPath: "."});
        setBindingDialogOpen(true);
    };

    const saveBinding = async () => {
        setBusy("binding-save");
        try {
            await api<Binding>("/bindings", {method: "POST", body: JSON.stringify(bindingForm)});
            showSuccess("Stack linked to the Git repository.");
            setBindingDialogOpen(false);
            await load();
        } catch (error) {
            showError((error as Error).message);
        } finally { setBusy(null); }
    };

    const previewTransfer = async (binding: Binding, direction: TransferDirection, sensitive = false) => {
        if (!sensitive) {
            setIncludeSensitive(false); setSensitiveConfirmation(""); setCommitMessage("");
        }
        setBusy(`preview-${binding.id}`);
        try {
            const confirmation = sensitive ? sensitiveConfirmation : "";
            const preview = await api<TransferPreview>(`/bindings/${binding.id}/preview/${direction}`, {
                method: "POST", body: JSON.stringify({includeSensitive: sensitive, sensitiveConfirmation: confirmation}),
            });
            setTransferBinding(binding); setTransferDirection(direction); setTransferPreview(preview);
        } catch (error) { showError((error as Error).message); }
        finally { setBusy(null); }
    };

    const closeTransfer = () => {
        setTransferBinding(null); setTransferPreview(null); setIncludeSensitive(false);
        setSensitiveConfirmation(""); setCommitMessage("");
    };

    const runTransfer = async () => {
        if (!transferBinding) return;
        const action = transferDirection === "stack_to_repository" ? "export" : "import";
        setBusy(`transfer-${transferBinding.id}`);
        try {
            const result = await api<TransferResult>(`/bindings/${transferBinding.id}/${action}`, {
                method: "POST", body: JSON.stringify({includeSensitive, sensitiveConfirmation, commitMessage, previewToken: transferPreview?.previewToken}),
            });
            showSuccess(result.message + (result.backup ? ` Backup: ${result.backup}` : ""));
            closeTransfer();
            await load();
        } catch (error) { showError((error as Error).message); }
        finally { setBusy(null); }
    };

    const confirmDeleteBinding = async () => {
        if (!deleteBinding) return;
        setBusy(`binding-delete-${deleteBinding.id}`);
        try {
            await api<void>(`/bindings/${deleteBinding.id}`, {method: "DELETE"});
            showSuccess("Stack link removed. No stack or repository file was deleted.");
            setDeleteBinding(null); await load();
        } catch (error) { showError((error as Error).message); }
        finally { setBusy(null); }
    };

    if (loading && !feature) return <Box sx={{display: "grid", placeItems: "center", p: 6}}><CircularProgress/></Box>;

    if (feature && !feature.enabled) {
        return <Box sx={{maxWidth: 900, mx: "auto", p: {xs: 1, md: 3}}}><Alert severity="info" variant="outlined">
            <Typography sx={{fontWeight: 700}}>Git synchronization is disabled by default</Typography>
            <Typography variant="body2" sx={{mt: .5}}>
                Enable it with <code>DOCKMAN_GIT_SYNC=true</code>, then restart Dockman. Mount a 32-byte key as a Docker secret and set <code>DOCKMAN_GIT_MASTER_KEY_FILE=/run/secrets/dockman_git_key</code>.
            </Typography>
        </Alert></Box>;
    }

    return <Box sx={{maxWidth: 1200, mx: "auto", p: {xs: 1, md: 3}}}>
        <Stack spacing={3}>
            <Alert severity="info" variant="outlined">
                Git synchronization is manual and non-destructive: files missing from the source are never deleted. Import creates a backup and never deploys or restarts the stack.
            </Alert>

            <Paper variant="outlined" sx={{borderRadius: 2, overflow: "hidden"}}>
                <Stack direction={{xs: "column", md: "row"}} sx={{p: 2.25, justifyContent: "space-between", gap: 2}}>
                    <Box>
                        <Typography variant="h6">Repositories</Typography>
                        <Typography variant="body2" color="text.secondary">Import an existing GitHub repository or create a dedicated one, then fetch, pull, and inspect its state.</Typography>
                    </Box>
                    <Button variant="contained" startIcon={<FolderOpenOutlined/>} onClick={openRepositoryCreate}>Add repository</Button>
                </Stack>
                <TableContainer>
                    <Table size="small">
                        <TableHead><TableRow>
                            <TableCell>Repository</TableCell><TableCell>Branch</TableCell><TableCell>Credential</TableCell><TableCell>State</TableCell><TableCell>Last fetch</TableCell><TableCell align="right">Actions</TableCell>
                        </TableRow></TableHead>
                        <TableBody>
                            {repositories.length === 0 && <TableRow><TableCell colSpan={6} align="center" sx={{py: 5, color: "text.secondary"}}>No repository managed by Dockman.</TableCell></TableRow>}
                            {repositories.map((repository) => {
                                const gitStatus = repositoryStatuses[repository.id];
                                const state = repository.status === "error" ? "error" : gitStatus?.state || repository.status;
                                return <TableRow key={repository.id} hover>
                                    <TableCell sx={{minWidth: 220}}>
                                        <Typography variant="body2" sx={{fontWeight: 700}}>{repository.name}</Typography>
                                        <Tooltip title={repository.remoteUrl}><Typography variant="caption" color="text.secondary" sx={{display: "block", maxWidth: 360, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap"}}>{repository.remoteUrl}</Typography></Tooltip>
                                        {repository.lastError && <Typography variant="caption" color="error" sx={{display: "block", maxWidth: 360}}>{repository.lastError}</Typography>}
                                    </TableCell>
                                    <TableCell sx={{fontFamily: "monospace"}}>{repository.defaultBranch}</TableCell>
                                    <TableCell>{repository.credentialId ? credentialNames[repository.credentialId] || "Unknown" : "None (public)"}</TableCell>
                                    <TableCell>
                                        <Stack direction="row" spacing={.75} sx={{alignItems: "center"}}>
                                            <Chip size="small" color={statusColor(state)} variant="outlined" label={state}/>
                                            {gitStatus && !gitStatus.clean && <Chip size="small" color="warning" label="dirty"/>}
                                            {gitStatus && (gitStatus.ahead > 0 || gitStatus.behind > 0) && <Typography variant="caption" color="text.secondary">↑{gitStatus.ahead} ↓{gitStatus.behind}</Typography>}
                                        </Stack>
                                    </TableCell>
                                    <TableCell>{dateLabel(repository.lastFetchedAt)}</TableCell>
                                    <TableCell align="right" sx={{whiteSpace: "nowrap"}}>
                                        <Tooltip title="Fetch remote state"><span><IconButton size="small" disabled={busy !== null || !repository.workspacePresent} onClick={() => void repositoryAction(repository, "fetch")}><RefreshOutlined fontSize="small"/></IconButton></span></Tooltip>
                                        <Tooltip title="Pull fast-forward changes"><span><IconButton size="small" disabled={busy !== null || !repository.workspacePresent} onClick={() => void repositoryAction(repository, "pull")}><CloudDownloadOutlined fontSize="small"/></IconButton></span></Tooltip>
                                        <Tooltip title="Push local commits"><span><IconButton size="small" disabled={busy !== null || !repository.workspacePresent} onClick={() => void repositoryAction(repository, "push")}><CloudUploadOutlined fontSize="small"/></IconButton></span></Tooltip>
                                        <Tooltip title="Operation history"><IconButton size="small" disabled={busy !== null} onClick={() => void openHistory(repository)}><HistoryOutlined fontSize="small"/></IconButton></Tooltip>
                                        <Tooltip title="Remove local workspace"><IconButton size="small" color="error" disabled={busy !== null} onClick={() => setDeleteRepository(repository)}><DeleteOutlined fontSize="small"/></IconButton></Tooltip>
                                    </TableCell>
                                </TableRow>;
                            })}
                        </TableBody>
                    </Table>
                </TableContainer>
            </Paper>

            <Paper variant="outlined" sx={{borderRadius: 2, overflow: "hidden"}}>
                <Stack direction={{xs: "column", md: "row"}} sx={{p: 2.25, justifyContent: "space-between", gap: 2}}>
                    <Box>
                        <Typography variant="h6">Stack links</Typography>
                        <Typography variant="body2" color="text.secondary">Link one host stack folder to one repository folder, preview differences, then transfer explicitly.</Typography>
                    </Box>
                    <Button variant="contained" startIcon={<LinkOutlined/>} disabled={repositories.length === 0 || busy !== null} onClick={openBindingCreate}>Link stack</Button>
                </Stack>
                <TableContainer><Table size="small">
                    <TableHead><TableRow><TableCell>Stack</TableCell><TableCell>Repository folder</TableCell><TableCell>Compose</TableCell><TableCell align="right">Manual transfer</TableCell></TableRow></TableHead>
                    <TableBody>
                        {bindings.length === 0 && <TableRow><TableCell colSpan={4} align="center" sx={{py: 5, color: "text.secondary"}}>No stack linked to a repository.</TableCell></TableRow>}
                        {bindings.map((binding) => <TableRow key={binding.id} hover>
                            <TableCell><Typography variant="body2" sx={{fontWeight: 700}}>{binding.stackPath}</Typography><Typography variant="caption" color="text.secondary">Host: {binding.host}</Typography></TableCell>
                            <TableCell><Typography variant="body2">{binding.repositoryName}</Typography><Typography variant="caption" color="text.secondary" sx={{fontFamily: "monospace"}}>{binding.subPath === "." ? "/" : `/${binding.subPath}`}</Typography></TableCell>
                            <TableCell>{binding.composePaths.length ? binding.composePaths.map((path) => <Chip key={path} size="small" variant="outlined" label={path} sx={{mr: .5}}/>) : <Chip size="small" color="warning" variant="outlined" label="Import target"/>}</TableCell>
                            <TableCell align="right" sx={{whiteSpace: "nowrap"}}>
                                <Tooltip title="Preview stack → Git"><span><IconButton size="small" disabled={busy !== null} onClick={() => void previewTransfer(binding, "stack_to_repository")}><CloudUploadOutlined fontSize="small"/></IconButton></span></Tooltip>
                                <Tooltip title="Preview Git → stack"><span><IconButton size="small" disabled={busy !== null} onClick={() => void previewTransfer(binding, "repository_to_stack")}><CloudDownloadOutlined fontSize="small"/></IconButton></span></Tooltip>
                                <Tooltip title="Remove link"><IconButton size="small" color="error" disabled={busy !== null} onClick={() => setDeleteBinding(binding)}><DeleteOutlined fontSize="small"/></IconButton></Tooltip>
                            </TableCell>
                        </TableRow>)}
                    </TableBody>
                </Table></TableContainer>
            </Paper>

            <Paper variant="outlined" sx={{borderRadius: 2, overflow: "hidden"}}>
                <Stack direction={{xs: "column", md: "row"}} sx={{p: 2.25, justifyContent: "space-between", gap: 2}}>
                    <Box>
                        <Typography variant="h6">Git credentials</Typography>
                        <Typography variant="body2" color="text.secondary">Secrets are encrypted at rest and are never returned by the API.</Typography>
                    </Box>
                    <Button variant="outlined" startIcon={<Add/>} onClick={openCredentialCreate}>Add credential</Button>
                </Stack>
                <Box sx={{px: 2.25, pb: 2}}><TextField value={repositoryUrl} onChange={(event) => setRepositoryUrl(event.target.value)} label="Repository URL for connection tests (optional)" placeholder="https://github.com/owner/repository.git" size="small" fullWidth helperText="Only credential-free github.com URLs are accepted."/></Box>
                <TableContainer><Table size="small">
                    <TableHead><TableRow><TableCell>Name</TableCell><TableCell>Type</TableCell><TableCell>Identity</TableCell><TableCell>Secret</TableCell><TableCell align="right">Actions</TableCell></TableRow></TableHead>
                    <TableBody>
                        {credentials.length === 0 && <TableRow><TableCell colSpan={5} align="center" sx={{py: 5, color: "text.secondary"}}>No Git credentials configured.</TableCell></TableRow>}
                        {credentials.map((credential) => <TableRow key={credential.id} hover>
                            <TableCell sx={{fontWeight: 650}}>{credential.name}</TableCell><TableCell><Chip size="small" variant="outlined" label={authLabel(credential.authType)}/></TableCell><TableCell>{credential.username || "—"}</TableCell><TableCell sx={{fontFamily: "monospace"}}>{credential.secretHint || (credential.hasSecret ? "••••" : "—")}</TableCell>
                            <TableCell align="right">
                                <Tooltip title="Test connection"><span><IconButton size="small" disabled={busy !== null} onClick={() => void testCredential(credential)}>{busy === `credential-test-${credential.id}` ? <CircularProgress size={18}/> : <SyncOutlined fontSize="small"/>}</IconButton></span></Tooltip>
                                <Tooltip title="Edit"><IconButton size="small" disabled={busy !== null} onClick={() => openCredentialEdit(credential)}><EditOutlined fontSize="small"/></IconButton></Tooltip>
                                <Tooltip title="Delete"><IconButton size="small" color="error" disabled={busy !== null} onClick={() => setDeleteCredential(credential)}><DeleteOutlined fontSize="small"/></IconButton></Tooltip>
                            </TableCell>
                        </TableRow>)}
                    </TableBody>
                </Table></TableContainer>
            </Paper>
        </Stack>

        <Dialog open={bindingDialogOpen} onClose={() => busy === null && setBindingDialogOpen(false)} fullWidth maxWidth="sm">
            <DialogTitle>Link a stack to Git</DialogTitle>
            <DialogContent dividers><Stack spacing={2} sx={{pt: .5}}>
                <FormControl><InputLabel>Repository</InputLabel><Select label="Repository" value={bindingForm.repositoryId} onChange={(event) => setBindingForm({...bindingForm, repositoryId: event.target.value})}>
                    {repositories.map((repository) => <MenuItem key={repository.id} value={repository.id}>{repository.name}</MenuItem>)}
                </Select></FormControl>
                {stackTargets.length > 0 && <FormControl><InputLabel>Detected stack</InputLabel><Select label="Detected stack" value={stackTargets.some((target) => `${target.host}\n${target.path}` === `${bindingForm.host}\n${bindingForm.stackPath}`) ? `${bindingForm.host}\n${bindingForm.stackPath}` : ""} onChange={(event) => {
                    const target = stackTargets.find((item) => `${item.host}\n${item.path}` === event.target.value);
                    if (target) setBindingForm({...bindingForm, host: target.host, stackPath: target.path});
                }}><MenuItem value=""><em>Custom path</em></MenuItem>{stackTargets.map((target) => <MenuItem key={`${target.host}-${target.path}`} value={`${target.host}\n${target.path}`}>{target.host} — {target.path}</MenuItem>)}</Select></FormControl>}
                <Stack direction={{xs: "column", sm: "row"}} spacing={2}><TextField fullWidth label="Host" value={bindingForm.host} onChange={(event) => setBindingForm({...bindingForm, host: event.target.value})} required/><TextField fullWidth label="Stack path" value={bindingForm.stackPath} onChange={(event) => setBindingForm({...bindingForm, stackPath: event.target.value})} placeholder="compose/my-stack" required/></Stack>
                <TextField label="Repository subfolder" value={bindingForm.subPath} onChange={(event) => setBindingForm({...bindingForm, subPath: event.target.value})} placeholder="stacks/my-stack" helperText="Use . for the repository root. Absolute paths and .git are refused." required/>
                <Alert severity="info">The link alone copies nothing. You will preview and confirm each direction afterward.</Alert>
            </Stack></DialogContent>
            <DialogActions><Button onClick={() => setBindingDialogOpen(false)} disabled={busy !== null}>Cancel</Button><Button variant="contained" onClick={() => void saveBinding()} disabled={busy !== null || !bindingForm.repositoryId || !bindingForm.host.trim() || !bindingForm.stackPath.trim() || !bindingForm.subPath.trim()}>{busy === "binding-save" && <CircularProgress size={16} sx={{mr: 1}}/>}Link</Button></DialogActions>
        </Dialog>

        <Dialog open={transferBinding !== null} onClose={() => busy === null && closeTransfer()} fullWidth maxWidth="md">
            <DialogTitle sx={{display: "flex", alignItems: "center", gap: 1}}><CompareArrowsOutlined/>{transferDirection === "stack_to_repository" ? "Export stack to Git" : "Import Git into stack"}</DialogTitle>
            <DialogContent dividers><Stack spacing={2}>
                <Alert severity={transferDirection === "stack_to_repository" ? "info" : "warning"}>
                    {transferDirection === "stack_to_repository" ? "Changed stack files will be committed and pushed. Remote-only files are preserved." : "Changed repository files will be copied into the stack after a backup. Stack deployment is never triggered."}
                </Alert>
                <Stack direction={{xs: "column", sm: "row"}} spacing={1} sx={{alignItems: {sm: "center"}}}>
                    <Chip label={`${transferPreview?.changed || 0} changed`} color={transferPreview?.changed ? "warning" : "success"}/>
                    <Chip label={`${transferPreview?.skipped || 0} sensitive skipped`} variant="outlined"/>
                    <Typography variant="body2" color="text.secondary" sx={{ml: {sm: "auto!important"}}}>No source-side deletion is propagated.</Typography>
                </Stack>
                <TableContainer component={Paper} variant="outlined" sx={{maxHeight: 310}}><Table size="small" stickyHeader><TableHead><TableRow><TableCell>File</TableCell><TableCell>Status</TableCell><TableCell>Size</TableCell></TableRow></TableHead><TableBody>
                    {!transferPreview?.entries.length && <TableRow><TableCell colSpan={3} align="center" sx={{py: 4, color: "text.secondary"}}>No difference.</TableCell></TableRow>}
                    {transferPreview?.entries.map((entry) => <TableRow key={entry.path}><TableCell sx={{fontFamily: "monospace", overflowWrap: "anywhere"}}>{entry.path}</TableCell><TableCell><Chip size="small" variant="outlined" color={entry.status === "skipped_sensitive" ? "warning" : entry.status === "modify" ? "info" : "success"} label={entry.status.replaceAll("_", " ")}/></TableCell><TableCell>{entry.size === undefined ? "—" : `${entry.size} B`}</TableCell></TableRow>)}
                </TableBody></Table></TableContainer>
                <FormControlLabel control={<Switch checked={includeSensitive} onChange={(event) => {
                    const checked = event.target.checked;
                    setIncludeSensitive(checked); setSensitiveConfirmation("");
                    if (!checked && transferBinding) void previewTransfer(transferBinding, transferDirection, false);
                }}/>} label="Include sensitive files for this transfer only"/>
                {includeSensitive && <><Alert severity="error">This may commit tokens, private keys, or .env secrets. It is disabled by default and never remembered.</Alert><TextField label='Type "INCLUDE SENSITIVE FILES"' value={sensitiveConfirmation} onChange={(event) => setSensitiveConfirmation(event.target.value)} onBlur={() => transferBinding && sensitiveConfirmation === "INCLUDE SENSITIVE FILES" && void previewTransfer(transferBinding, transferDirection, true)} fullWidth/></>}
                {transferDirection === "stack_to_repository" && <TextField label="Commit message (optional)" value={commitMessage} onChange={(event) => setCommitMessage(event.target.value)} placeholder={`chore(stack): sync ${transferBinding?.stackPath || "stack"} from Dockman`} slotProps={{htmlInput: {maxLength: 300}}}/>}
            </Stack></DialogContent>
            <DialogActions><Button onClick={closeTransfer} disabled={busy !== null}>Cancel</Button><Button variant="contained" color={transferDirection === "repository_to_stack" ? "warning" : "primary"} disabled={busy !== null || !transferPreview || transferPreview.changed === 0 || (includeSensitive && sensitiveConfirmation !== "INCLUDE SENSITIVE FILES")} onClick={() => void runTransfer()}>{busy?.startsWith("transfer-") && <CircularProgress size={16} sx={{mr: 1}}/>}{transferDirection === "stack_to_repository" ? "Commit and push" : "Backup and import"}</Button></DialogActions>
        </Dialog>

        <Dialog open={deleteBinding !== null} onClose={() => busy === null && setDeleteBinding(null)} maxWidth="xs" fullWidth>
            <DialogTitle>Remove stack link?</DialogTitle><DialogContent><Typography>Unlink <strong>{deleteBinding?.stackPath}</strong> from <strong>{deleteBinding?.repositoryName}</strong>? No file or Git history will be deleted.</Typography></DialogContent><DialogActions><Button onClick={() => setDeleteBinding(null)} disabled={busy !== null}>Cancel</Button><Button color="error" variant="contained" onClick={() => void confirmDeleteBinding()} disabled={busy !== null}>Remove link</Button></DialogActions>
        </Dialog>

        <Dialog open={repositoryDialogOpen} onClose={() => busy === null && setRepositoryDialogOpen(false)} fullWidth maxWidth="sm">
            <DialogTitle>Add Git repository</DialogTitle>
            <DialogContent dividers><Stack spacing={2} sx={{pt: .5}}>
                <FormControl><InputLabel>Source</InputLabel><Select label="Source" value={repositoryForm.mode} onChange={(event) => setRepositoryForm({...emptyRepository, mode: event.target.value as RepositoryDialogMode})}>
                    <MenuItem value="import">Import an existing repository</MenuItem><MenuItem value="github">Create a new GitHub repository</MenuItem>
                </Select></FormControl>
                <TextField label="Dockman repository name" value={repositoryForm.name} onChange={(event) => setRepositoryForm({...repositoryForm, name: event.target.value})} required autoFocus helperText="A local identifier; for GitHub creation this is also the remote repository name."/>
                {repositoryForm.mode === "import" ? <TextField label="GitHub clone URL" value={repositoryForm.remoteUrl} onChange={(event) => setRepositoryForm({...repositoryForm, remoteUrl: event.target.value})} placeholder="https://github.com/owner/repository.git" required/> : <>
                    <TextField label="Description (optional)" value={repositoryForm.description} onChange={(event) => setRepositoryForm({...repositoryForm, description: event.target.value})} slotProps={{htmlInput: {maxLength: 300}}}/>
                    <FormControlLabel control={<Switch checked={repositoryForm.private} onChange={(event) => setRepositoryForm({...repositoryForm, private: event.target.checked})}/>} label={repositoryForm.private ? "Private repository" : "Public repository"}/>
                </>}
                <TextField label="Default branch" value={repositoryForm.defaultBranch} onChange={(event) => setRepositoryForm({...repositoryForm, defaultBranch: event.target.value})} required/>
                <FormControl><InputLabel>Credential</InputLabel><Select label="Credential" value={repositoryForm.credentialId} onChange={(event) => setRepositoryForm({...repositoryForm, credentialId: event.target.value})}>
                    {repositoryForm.mode === "import" && <MenuItem value="">None (public repository)</MenuItem>}
                    {credentials.filter((credential) => repositoryForm.mode === "import" || credential.authType === "https_token").map((credential) => <MenuItem key={credential.id} value={credential.id}>{credential.name} — {authLabel(credential.authType)}</MenuItem>)}
                </Select></FormControl>
                {repositoryForm.mode === "github" && <Alert severity="info">Creation requires a GitHub HTTPS token allowed to create repositories. Dockman initializes and immediately clones the repository.</Alert>}
            </Stack></DialogContent>
            <DialogActions><Button onClick={() => setRepositoryDialogOpen(false)} disabled={busy !== null}>Cancel</Button><Button variant="contained" onClick={() => void saveRepository()} disabled={busy !== null || !repositoryForm.name.trim() || (repositoryForm.mode === "import" ? !repositoryForm.remoteUrl.trim() : !repositoryForm.credentialId)}>{busy === "repository-save" && <CircularProgress size={16} sx={{mr: 1}}/>}{repositoryForm.mode === "import" ? "Import" : "Create"}</Button></DialogActions>
        </Dialog>

        <Dialog open={credentialDialogOpen} onClose={() => busy === null && setCredentialDialogOpen(false)} fullWidth maxWidth="sm">
            <DialogTitle sx={{display: "flex", alignItems: "center", gap: 1}}><KeyOutlined/>{editingCredential ? "Edit Git credential" : "Add Git credential"}</DialogTitle>
            <DialogContent dividers><Stack spacing={2} sx={{pt: .5}}>
                <TextField label="Name" value={credentialForm.name} onChange={(event) => setCredentialForm({...credentialForm, name: event.target.value})} autoFocus required/>
                <FormControl><InputLabel>Authentication</InputLabel><Select label="Authentication" value={credentialForm.authType} onChange={(event) => setCredentialForm({...credentialForm, authType: event.target.value as AuthType, token: "", privateKey: "", passphrase: ""})}>
                    <MenuItem value="public">Public / no authentication</MenuItem><MenuItem value="https_token">GitHub HTTPS token</MenuItem><MenuItem value="ssh_key">SSH private key</MenuItem>
                </Select></FormControl>
                {credentialForm.authType === "https_token" && <><TextField label="Username (optional)" value={credentialForm.username} onChange={(event) => setCredentialForm({...credentialForm, username: event.target.value})} helperText="Defaults to x-access-token."/><TextField label={editingCredential ? "New token (leave blank to keep current)" : "Token"} type="password" value={credentialForm.token} onChange={(event) => setCredentialForm({...credentialForm, token: event.target.value})} required={!editingCredential} autoComplete="new-password"/></>}
                {credentialForm.authType === "ssh_key" && <><TextField label={editingCredential ? "New private key (leave blank to keep current)" : "Private key"} value={credentialForm.privateKey} onChange={(event) => setCredentialForm({...credentialForm, privateKey: event.target.value})} required={!editingCredential} multiline minRows={7} maxRows={13} sx={{"& textarea": {fontFamily: "monospace"}}}/><TextField label="Passphrase (if encrypted)" type="password" value={credentialForm.passphrase} onChange={(event) => setCredentialForm({...credentialForm, passphrase: event.target.value})} autoComplete="new-password"/></>}
                {editingCredential && credentialForm.authType !== "public" && <Alert severity="info">Stored secrets are never returned. Leave the secret empty to retain it unchanged.</Alert>}
            </Stack></DialogContent>
            <DialogActions><Button onClick={() => setCredentialDialogOpen(false)} disabled={busy !== null}>Cancel</Button><Button variant="contained" onClick={() => void saveCredential()} disabled={busy !== null || !credentialForm.name.trim()}>{busy === "credential-save" && <CircularProgress size={16} sx={{mr: 1}}/>}Save</Button></DialogActions>
        </Dialog>

        <Dialog open={historyRepository !== null} onClose={() => setHistoryRepository(null)} fullWidth maxWidth="md">
            <DialogTitle>Operation history — {historyRepository?.name}</DialogTitle><DialogContent dividers>
                <Table size="small"><TableHead><TableRow><TableCell>Operation</TableCell><TableCell>State</TableCell><TableCell>Started</TableCell><TableCell>Finished</TableCell><TableCell>Details</TableCell></TableRow></TableHead><TableBody>
                    {operations.length === 0 && <TableRow><TableCell colSpan={5} align="center" sx={{py: 4, color: "text.secondary"}}>No operation recorded.</TableCell></TableRow>}
                    {operations.map((operation) => <TableRow key={operation.id}><TableCell>{operation.type}</TableCell><TableCell><Chip size="small" color={operation.state === "success" ? "success" : operation.state === "failed" ? "error" : "info"} variant="outlined" label={operation.state}/></TableCell><TableCell>{dateLabel(operation.startedAt)}</TableCell><TableCell>{dateLabel(operation.finishedAt)}</TableCell><TableCell sx={{maxWidth: 320, overflowWrap: "anywhere"}}>{operation.error || "—"}</TableCell></TableRow>)}
                </TableBody></Table>
            </DialogContent><DialogActions><Button onClick={() => setHistoryRepository(null)}>Close</Button></DialogActions>
        </Dialog>

        <Dialog open={deleteRepository !== null} onClose={() => busy === null && setDeleteRepository(null)} maxWidth="xs" fullWidth>
            <DialogTitle>Remove managed repository?</DialogTitle><DialogContent><Alert severity="warning" sx={{mb: 2}}>This deletes only Dockman’s isolated local clone. It never deletes the GitHub repository.</Alert><Typography>Remove <strong>{deleteRepository?.name}</strong> and its local operation history?</Typography></DialogContent><DialogActions><Button onClick={() => setDeleteRepository(null)} disabled={busy !== null}>Cancel</Button><Button color="error" variant="contained" onClick={() => void confirmDeleteRepository()} disabled={busy !== null}>Remove local clone</Button></DialogActions>
        </Dialog>

        <Dialog open={deleteCredential !== null} onClose={() => busy === null && setDeleteCredential(null)} maxWidth="xs" fullWidth>
            <DialogTitle>Delete Git credential?</DialogTitle><DialogContent><Typography>This removes <strong>{deleteCredential?.name}</strong>. The encrypted secret cannot be recovered.</Typography></DialogContent><DialogActions><Button onClick={() => setDeleteCredential(null)} disabled={busy !== null}>Cancel</Button><Button color="error" variant="contained" onClick={() => void confirmDeleteCredential()} disabled={busy !== null}>Delete</Button></DialogActions>
        </Dialog>
    </Box>;
}
