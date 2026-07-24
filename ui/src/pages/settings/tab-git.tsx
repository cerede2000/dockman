import {useCallback, useDeferredValue, useEffect, useMemo, useRef, useState} from "react";
import {DiffEditor} from "@monaco-editor/react";
import {
    Alert, Box, Button, Checkbox, Chip, CircularProgress, Dialog, DialogActions, DialogContent,
    DialogTitle, FormControl, FormControlLabel, IconButton, InputLabel, Menu, MenuItem, Paper,
    Select, Stack, Switch, Table, TableBody, TableCell, TableContainer, TableHead, TableRow,
    TablePagination, TextField, Tooltip, Typography,
} from "@mui/material";
import {
    Add, ArchiveOutlined, BlockOutlined, CheckCircleOutlined, CloudDownloadOutlined, CloudUploadOutlined, CompareArrowsOutlined, DeleteOutlined, EditOutlined,
    FolderOffOutlined, FolderOpenOutlined, HistoryOutlined, KeyOutlined, LinkOutlined, RefreshOutlined, RestoreOutlined, SearchOutlined, SyncOutlined, TuneOutlined, UndoOutlined,
} from "@mui/icons-material";
import {withProtectedAPI} from "../../lib/api.ts";
import {formatBytes} from "../../lib/editor.ts";
import {useSnackbar} from "../../hooks/snackbar.ts";
import {useCopyButton} from "../../hooks/copy.ts";
import CopyButton from "../../components/copy-button.tsx";
import {useSearchParams} from 'react-router-dom';
import GitBindingRecovery, {type RecoveryBinding} from '../../components/git-binding-recovery.tsx';

type AuthType = "public" | "https_token" | "ssh_key";
type RepositoryDialogMode = "import" | "github";
type BranchCreationMode = "from_default" | "empty";

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
    storageMode: "compact" | "legacy";
    excludePatterns: string[];
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

interface APIErrorBody {
    error?: string;
    code?: string;
    branch?: string;
    sourceBranch?: string;
    canCreate?: boolean;
    canCreateFromDefault?: boolean;
    canCreateEmpty?: boolean;
}

class APIError extends Error {
    constructor(public readonly status: number, public readonly body: APIErrorBody) {
        super(body.error || `HTTP ${status}`);
    }
}

interface StackTarget { host: string; path: string; composePaths: string[]; scope: "all_stacks" | "folder"; stackCount: number; }
interface Binding {
    id: string; repositoryId: string; repositoryName: string; host: string; stackPath: string;
    subPath: string; composePaths: string[]; syncProfile: "compose_config" | "all_files";
    composeSelectionMode: "all" | "selected"; selectedComposePaths: string[];
    includePatterns: string[]; excludePatterns: string[]; enabled: boolean;
    autoSyncEnabled: boolean; autoSyncIntervalMinutes: number; autoSyncState: string;
    autoSyncError?: string; lastAutoSyncAt?: string; lastAutoSyncSuccessAt?: string;
    autoDeployEnabled: boolean; autoDeployNewStacks: boolean; autoDeployRollbackEnabled: boolean; autoDeployComposePaths: string[]; autoDeployState: string;
    autoDeployError?: string; lastAutoDeployAt?: string;
    autoReconcileEnabled: boolean; initialSyncState: string; initialSyncError?: string; initialSyncAt?: string;
}
interface AutoSyncResult { bindingId: string; state: string; changed: number; conflicts: number; backup?: string; deployed?: string[]; deployFailed?: string[]; rolledBack?: string[]; rollbackFailed?: string[]; syncFailed?: string[]; message: string; }
interface Deployment { id: string; commitSha: string; composePath: string; state: string; result?: string; logs?: string; createdAt: string; }
interface PreviewEntry {
    path: string; status: "add" | "modify" | "conflict" | "deleted_on_git" | "deleted_locally" | "skipped_sensitive" | "skipped_oversized" | "skipped_type" | "skipped_excluded" | "skipped_unavailable"; sourceSha?: string;
    targetSha?: string; size?: number; sensitive?: boolean; directory?: boolean; conflictKind?: "no_baseline" | "destination_changed" | "source_deleted_destination_changed" | "destination_deleted";
}
interface TransferPreview {
    bindingId: string; direction: TransferDirection; entries: PreviewEntry[]; changed: number;
    unchanged: number; skipped: number; conflicts: number; preserved: number; localDeletions: number; orphanedComposePaths?: string[]; deletionMode: string;
    previewToken: string;
}
interface TransferResult { preview: TransferPreview; commitSha?: string; backup?: string; message: string; }
interface ComparisonSide { sha256: string; size: number; content?: string; }
interface FileComparison { path: string; dockman: ComparisonSide; git: ComparisonSide; comparable: boolean; reason?: string; }
type TransferDirection = "stack_to_repository" | "repository_to_stack";
type PreviewStatus = PreviewEntry["status"];
type ConflictDecision = "git" | "dockman";

function composeOwner(binding: Binding, filePath: string): string | undefined {
    return binding.composePaths
        .filter((composePath) => {
            const slash = composePath.lastIndexOf("/");
            const directory = slash < 0 ? "" : composePath.slice(0, slash);
            return directory === "" || filePath === directory || filePath.startsWith(`${directory}/`);
        })
        .sort((left, right) => {
            const leftDirectory = left.includes("/") ? left.slice(0, left.lastIndexOf("/")) : "";
            const rightDirectory = right.includes("/") ? right.slice(0, right.lastIndexOf("/")) : "";
            return rightDirectory.length - leftDirectory.length;
        })[0];
}

const previewStatuses: PreviewStatus[] = ["conflict", "deleted_locally", "deleted_on_git", "add", "modify", "skipped_type", "skipped_excluded", "skipped_sensitive", "skipped_oversized", "skipped_unavailable"];

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
        const body = await response.json().catch(() => ({error: response.statusText})) as APIErrorBody;
        throw new APIError(response.status, body);
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

function comparisonLanguage(path: string) {
    const name = path.toLocaleLowerCase();
    if (name.endsWith(".json")) return "json";
    if (name.endsWith(".xml")) return "xml";
    if (name.endsWith(".sh") || name.endsWith(".bash")) return "shell";
    if (name.endsWith(".sql")) return "sql";
    if (name.endsWith(".toml") || name.endsWith(".ini") || name.endsWith(".cfg") || name.endsWith(".conf")) return "ini";
    if (name.endsWith(".md")) return "markdown";
    if (name.endsWith(".yml") || name.endsWith(".yaml")) return "yaml";
    return "plaintext";
}

interface ComposePathSelectorProps {
    paths: string[];
    selectedPaths: Set<string>;
    onChange: (paths: Set<string>) => void;
    selectedLabel?: string;
    unselectedLabel?: string;
    maxHeight?: string;
    disabled?: boolean;
    pathStates?: Record<string, string>;
}

function ComposePathSelector({paths, selectedPaths, onChange, selectedLabel = "selected", unselectedLabel = "not selected", maxHeight = "42vh", disabled = false, pathStates = {}}: ComposePathSelectorProps) {
    const [search, setSearch] = useState("");
    const [status, setStatus] = useState<"all" | "selected" | "not_selected">("all");
    const deferredSearch = useDeferredValue(search);
    const filteredPaths = useMemo(() => {
        const query = deferredSearch.trim().toLocaleLowerCase();
        return paths.filter((path) => {
            const selected = selectedPaths.has(path);
            return (!query || path.toLocaleLowerCase().includes(query)) && (status === "all" || (status === "selected" ? selected : !selected));
        });
    }, [deferredSearch, paths, selectedPaths, status]);
    const updateFiltered = (checked: boolean) => {
        const next = new Set(selectedPaths);
        filteredPaths.forEach((path) => checked ? next.add(path) : next.delete(path));
        onChange(next);
    };
    const togglePath = (path: string, checked: boolean) => {
        const next = new Set(selectedPaths);
        if (checked) next.add(path); else next.delete(path);
        onChange(next);
    };
    const everyFilteredSelected = filteredPaths.length > 0 && filteredPaths.every((path) => selectedPaths.has(path));
    const someFilteredSelected = filteredPaths.some((path) => selectedPaths.has(path));
    return <Stack spacing={1.25}>
        <Stack direction={{xs: "column", md: "row"}} spacing={1}>
            <TextField size="small" fullWidth value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Search a Compose path…" slotProps={{input: {startAdornment: <SearchOutlined fontSize="small" sx={{mr: 1, color: "text.secondary"}}/>}}}/>
            <FormControl size="small" sx={{minWidth: 170}}><InputLabel>Status</InputLabel><Select label="Status" value={status} onChange={(event) => setStatus(event.target.value as "all" | "selected" | "not_selected")}><MenuItem value="all">All</MenuItem><MenuItem value="selected">Selected</MenuItem><MenuItem value="not_selected">Not selected</MenuItem></Select></FormControl>
        </Stack>
        <Stack direction="row" spacing={1} sx={{flexWrap: "wrap", alignItems: "center"}}>
            <Button size="small" disabled={disabled || paths.length === 0} onClick={() => onChange(new Set(paths))}>Select all</Button>
            <Button size="small" disabled={disabled || paths.length === 0} onClick={() => onChange(new Set())}>Select none</Button>
            <Button size="small" disabled={disabled || filteredPaths.length === 0} onClick={() => updateFiltered(true)}>Select filtered</Button>
            <Button size="small" disabled={disabled || filteredPaths.length === 0} onClick={() => updateFiltered(false)}>Deselect filtered</Button>
            <Typography variant="caption" color="text.secondary" sx={{ml: {md: "auto !important"}}}>{selectedPaths.size} / {paths.length} selected · {filteredPaths.length} displayed</Typography>
        </Stack>
        <TableContainer component={Paper} variant="outlined" sx={{maxHeight}}><Table size="small" stickyHeader>
            <TableHead><TableRow><TableCell padding="checkbox"><Checkbox size="small" disabled={disabled || filteredPaths.length === 0} checked={everyFilteredSelected} indeterminate={someFilteredSelected && !everyFilteredSelected} onChange={(_, checked) => updateFiltered(checked)}/></TableCell><TableCell>Compose file</TableCell><TableCell>Status</TableCell><TableCell align="right">Action</TableCell></TableRow></TableHead>
            <TableBody>{filteredPaths.length === 0 ? <TableRow><TableCell colSpan={4} align="center" sx={{py: 4, color: "text.secondary"}}>No stack matches this filter.</TableCell></TableRow> : filteredPaths.map((path) => { const selected = selectedPaths.has(path); const state = pathStates[path]; return <TableRow key={path} hover selected={selected}><TableCell padding="checkbox"><Checkbox size="small" disabled={disabled} checked={selected} onChange={(_, checked) => togglePath(path, checked)}/></TableCell><TableCell sx={{fontFamily: "monospace", overflowWrap: "anywhere"}}>{path}</TableCell><TableCell><Chip size="small" variant="outlined" color={state === "locally_deleted" ? "warning" : selected ? "success" : "default"} label={state === "locally_deleted" ? "deleted locally · still on Git" : selected ? selectedLabel : unselectedLabel}/></TableCell><TableCell align="right"><Tooltip title={selected ? "Remove from selection" : "Add to selection"}><span><IconButton size="small" disabled={disabled} color={selected ? "error" : "primary"} onClick={() => togglePath(path, !selected)}>{selected ? <DeleteOutlined fontSize="small"/> : <Add fontSize="small"/>}</IconButton></span></Tooltip></TableCell></TableRow>; })}</TableBody>
        </Table></TableContainer>
    </Stack>;
}

export default function TabGit() {
    const {showError, showSuccess, showWarning} = useSnackbar();
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
    const [missingBranch, setMissingBranch] = useState<{branch: string; sourceBranch: string; canCreateFromDefault: boolean; canCreateEmpty: boolean} | null>(null);
    const [deleteRepository, setDeleteRepository] = useState<Repository | null>(null);
    const [historyRepository, setHistoryRepository] = useState<Repository | null>(null);
    const [operations, setOperations] = useState<Operation[]>([]);
    const [bindings, setBindings] = useState<Binding[]>([]);
    const [stackTargets, setStackTargets] = useState<StackTarget[]>([]);
    const [bindingDialogOpen, setBindingDialogOpen] = useState(false);
    const [bindingForm, setBindingForm] = useState({repositoryId: "", host: "", stackPath: "", subPath: "stacks", targetMode: "repository_folder" as "repository_folder" | "repository_root", autoReconcile: true, initialSync: "none" as "none" | TransferDirection});
    const [bindingComposePaths, setBindingComposePaths] = useState<Set<string>>(() => new Set());
    const [transferBinding, setTransferBinding] = useState<Binding | null>(null);
    const [transferDirection, setTransferDirection] = useState<TransferDirection>("stack_to_repository");
    const [transferPreview, setTransferPreview] = useState<TransferPreview | null>(null);
    const [includeSensitive, setIncludeSensitive] = useState(false);
    const [sensitiveConfirmation, setSensitiveConfirmation] = useState("");
    const [resolvedConflictPaths, setResolvedConflictPaths] = useState<Set<string>>(new Set());
    const [selectedTransferPaths, setSelectedTransferPaths] = useState<Set<string>>(new Set());
    const [comparison, setComparison] = useState<FileComparison | null>(null);
    const [conflictResolutionMode, setConflictResolutionMode] = useState(false);
    const [conflictDecisions, setConflictDecisions] = useState<Record<string, ConflictDecision>>({});
    const commitMessageRef = useRef<HTMLInputElement | null>(null);
    const [deleteBinding, setDeleteBinding] = useState<Binding | null>(null);
	const [recoveryView, setRecoveryView] = useState<{binding: RecoveryBinding; tab: 'activity' | 'backups'} | null>(null);
    const [policyBinding, setPolicyBinding] = useState<Binding | null>(null);
    const [policyForm, setPolicyForm] = useState({profile: "compose_config" as "compose_config" | "all_files", includes: "", excludes: ""});
    const [automationBinding, setAutomationBinding] = useState<Binding | null>(null);
    const [composeBinding, setComposeBinding] = useState<Binding | null>(null);
    const [selectedComposePaths, setSelectedComposePaths] = useState<Set<string>>(() => new Set());
    const [composePathStates, setComposePathStates] = useState<Record<string, string>>({});
    const [deployments, setDeployments] = useState<Deployment[]>([]);
    const [automationForm, setAutomationForm] = useState({enabled: false, autoReconcile: true, intervalMinutes: 15, deployEnabled: false, deployNewStacks: false, deployRollback: false, deployComposePaths: [] as string[]});
    const automationDeploySelection = useMemo(() => new Set(automationForm.deployComposePaths), [automationForm.deployComposePaths]);
    const [policyRepository, setPolicyRepository] = useState<Repository | null>(null);
    const [repositoryExcludePatterns, setRepositoryExcludePatterns] = useState("");
    const [excludeMenu, setExcludeMenu] = useState<{anchor: HTMLElement; entry: PreviewEntry} | null>(null);
    const [previewPage, setPreviewPage] = useState(0);
    const [previewRowsPerPage, setPreviewRowsPerPage] = useState(50);
    const [previewSearch, setPreviewSearch] = useState("");
    const [previewStatus, setPreviewStatus] = useState<"all" | PreviewStatus>("all");
    const [previewPageInput, setPreviewPageInput] = useState("1");
    const [selectedPreviewPaths, setSelectedPreviewPaths] = useState<Set<string>>(() => new Set());
    const [orphanDecision, setOrphanDecision] = useState<{composePath: string; action: "archive" | "delete"} | null>(null);
    const [orphanConfirmation, setOrphanConfirmation] = useState("");
    const [localDeletionDecision, setLocalDeletionDecision] = useState<{composePath: string; path: string; wholeStack: boolean} | null>(null);
    const [localDeletionConfirmation, setLocalDeletionConfirmation] = useState("");
    const [searchParams, setSearchParams] = useSearchParams();
    const openedGitDeepLink = useRef('');
    const {handleCopy: copyRepositoryUrl, copiedId: copiedRepositoryUrl} = useCopyButton();
    const deferredPreviewSearch = useDeferredValue(previewSearch);
    const previewStatusCounts = useMemo(() => {
        const counts = new Map<PreviewStatus, number>();
        for (const entry of transferPreview?.entries || []) counts.set(entry.status, (counts.get(entry.status) || 0) + 1);
        return counts;
    }, [transferPreview?.entries]);
    const filteredPreviewEntries = useMemo(() => {
        const query = deferredPreviewSearch.trim().toLocaleLowerCase();
        return (transferPreview?.entries || []).filter((entry) => (previewStatus === "all" || entry.status === previewStatus) && (!query || entry.path.toLocaleLowerCase().includes(query)));
    }, [deferredPreviewSearch, previewStatus, transferPreview?.entries]);
    const previewPageCount = Math.max(1, Math.ceil(filteredPreviewEntries.length / previewRowsPerPage));
    const visiblePreviewEntries = useMemo(() => filteredPreviewEntries.slice(
        previewPage * previewRowsPerPage,
        previewPage * previewRowsPerPage + previewRowsPerPage,
    ), [filteredPreviewEntries, previewPage, previewRowsPerPage]);
    const selectablePreviewEntries = visiblePreviewEntries.filter((entry) => entry.status !== "skipped_excluded" && entry.status !== "conflict" && entry.status !== "deleted_on_git" && entry.status !== "deleted_locally");
    const selectedVisibleCount = selectablePreviewEntries.filter((entry) => selectedPreviewPaths.has(entry.path)).length;
    const allowableSelectedEntries = visiblePreviewEntries.filter((entry) => selectedPreviewPaths.has(entry.path) && entry.status === "skipped_type");
    const safeTransferCount = (transferPreview?.entries || []).filter((entry) => entry.status === "add" || entry.status === "modify").length;
    const unresolvedConflictCount = Math.max(0, (transferPreview?.conflicts || 0) - resolvedConflictPaths.size);
    const orphanComposePaths = useMemo(() => new Set(transferPreview?.orphanedComposePaths || []), [transferPreview?.orphanedComposePaths]);

    useEffect(() => {
        setPreviewPage((current) => Math.min(current, previewPageCount - 1));
    }, [previewPageCount]);

    useEffect(() => {
        setPreviewPageInput(String(previewPage + 1));
    }, [previewPage]);

    const loadRepositoryStatuses = useCallback(async (rows: Repository[]) => {
        const pairs = await Promise.all(rows.filter((row) => row.workspacePresent).map(async (row) => {
            try {
                return [row.id, await api<RepositoryStatus>(`/repositories/${row.id}/status`)] as const;
            } catch {
                return null;
            }
        }));
        const successful = pairs.filter((pair): pair is readonly [string, RepositoryStatus] => pair !== null);
        setRepositoryStatuses(Object.fromEntries(successful));
        const compactRepositories = new Set(successful.map(([id]) => id));
        setRepositories((current) => current.map((repository) => compactRepositories.has(repository.id) ? {...repository, storageMode: "compact"} : repository));
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

    useEffect(() => {
        if (!feature?.enabled) return;
        const timer = window.setInterval(() => {
            if (document.visibilityState !== "visible" || busy !== null) return;
            void api<Binding[]>("/bindings").then(setBindings).catch(() => undefined);
        }, 30_000);
        return () => window.clearInterval(timer);
    }, [busy, feature?.enabled]);

    const credentialNames = useMemo(() => Object.fromEntries(credentials.map((item) => [item.id, item.name])), [credentials]);
    const bindingStackTarget = useMemo(() => stackTargets.find((target) => target.host === bindingForm.host && target.path === bindingForm.stackPath), [bindingForm.host, bindingForm.stackPath, stackTargets]);

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
        setMissingBranch(null);
        setRepositoryDialogOpen(true);
    };

    const saveRepository = async (branchCreationMode?: BranchCreationMode) => {
        setBusy("repository-save");
        try {
            if (repositoryForm.mode === "import") {
                await api<Repository>("/repositories", {
                    method: "POST",
                    body: JSON.stringify({
                        name: repositoryForm.name, remoteUrl: repositoryForm.remoteUrl,
                        defaultBranch: repositoryForm.defaultBranch, credentialId: repositoryForm.credentialId,
                        branchCreationMode: branchCreationMode || "",
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
            setMissingBranch(null);
            await load();
        } catch (error) {
            if (error instanceof APIError && error.body.code === "remote_branch_missing" && error.body.canCreate && error.body.branch) {
                setMissingBranch({
                    branch: error.body.branch, sourceBranch: error.body.sourceBranch || "",
                    canCreateFromDefault: Boolean(error.body.canCreateFromDefault),
                    canCreateEmpty: Boolean(error.body.canCreateEmpty),
                });
                return;
            }
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
        setBindingForm({repositoryId: repositories[0]?.id || "", host: first?.host || "", stackPath: first?.path || "", subPath: "stacks", targetMode: "repository_folder", autoReconcile: true, initialSync: "none"});
        setBindingComposePaths(new Set(first?.composePaths || []));
        setBindingDialogOpen(true);
    };

    const saveBinding = async () => {
        setBusy("binding-save");
        try {
            const target = stackTargets.find((item) => item.host === bindingForm.host && item.path === bindingForm.stackPath);
            const selected = target?.composePaths.filter((path) => bindingComposePaths.has(path)) || [];
            const composeSelectionMode = target && selected.length < target.composePaths.length ? "selected" : "all";
            const binding = await api<Binding>("/bindings", {method: "POST", body: JSON.stringify({repositoryId: bindingForm.repositoryId, host: bindingForm.host, stackPath: bindingForm.stackPath, subPath: bindingForm.subPath, autoReconcile: bindingForm.autoReconcile, initialSync: bindingForm.initialSync, composeSelectionMode, selectedComposePaths: selected})});
            if (binding.initialSyncState === "error") showError(`Folder linked, but initialization failed: ${binding.initialSyncError || "unknown error"}`);
            else if (binding.initialSyncState === "reconciled") showSuccess("Folder linked: Dockman and Git were identical, so the synchronization baseline was established automatically.");
            else if (binding.initialSyncState === "exported") showSuccess("Folder linked and initialized from Dockman to Git.");
            else if (binding.initialSyncState === "imported") showSuccess("Folder linked and initialized from Git to Dockman with a backup.");
            else showSuccess("Complete folder linked to the Git repository.");
            setBindingDialogOpen(false);
            await load();
        } catch (error) {
            showError((error as Error).message);
        } finally { setBusy(null); }
    };

    const previewTransfer = async (binding: Binding, direction: TransferDirection, sensitive = false, resolvedPath?: string, selectedPath?: string, conflictMode = false) => {
        setConflictResolutionMode(conflictMode);
        if (!conflictMode) setConflictDecisions({});
        if (!sensitive) {
            setIncludeSensitive(false); setSensitiveConfirmation("");
            if (commitMessageRef.current) commitMessageRef.current.value = "";
        }
        setBusy(`preview-${binding.id}`);
        try {
            const confirmation = sensitive ? sensitiveConfirmation : "";
            const preview = await api<TransferPreview>(`/bindings/${binding.id}/preview/${direction}`, {
                method: "POST", body: JSON.stringify({includeSensitive: sensitive, sensitiveConfirmation: confirmation}),
            });
            setTransferBinding(binding); setTransferDirection(direction); setTransferPreview(preview); setPreviewPage(0);
            setPreviewSearch(""); setPreviewStatus(conflictMode ? "conflict" : "all"); setPreviewPageInput("1"); setSelectedPreviewPaths(new Set());
            setResolvedConflictPaths(resolvedPath && preview.entries.some((entry) => entry.path === resolvedPath && entry.status === "conflict") ? new Set([resolvedPath]) : new Set());
            setSelectedTransferPaths(selectedPath && preview.entries.some((entry) => entry.path === selectedPath && ["add", "modify", "conflict"].includes(entry.status)) ? new Set([selectedPath]) : new Set());
        } catch (error) { showError((error as Error).message); }
        finally { setBusy(null); }
    };

    const closeTransfer = () => {
        setTransferBinding(null); setTransferPreview(null); setIncludeSensitive(false);
        setSensitiveConfirmation(""); setResolvedConflictPaths(new Set()); setSelectedTransferPaths(new Set()); setComparison(null); setExcludeMenu(null); setPreviewPage(0); setPreviewSearch(""); setPreviewStatus("all");
        setConflictResolutionMode(false); setConflictDecisions({});
        setPreviewPageInput("1"); setSelectedPreviewPaths(new Set());
        if (commitMessageRef.current) commitMessageRef.current.value = "";
    };

    const runTransfer = async () => {
        if (!transferBinding) return;
        const completedBinding = transferBinding;
        const action = transferDirection === "stack_to_repository" ? "export" : "import";
        setBusy(`transfer-${transferBinding.id}`);
        try {
            const result = await api<TransferResult>(`/bindings/${transferBinding.id}/${action}`, {
                method: "POST", body: JSON.stringify({includeSensitive, sensitiveConfirmation, commitMessage: commitMessageRef.current?.value || "", previewToken: transferPreview?.previewToken, resolvedPaths: [...resolvedConflictPaths], selectedPaths: [...selectedTransferPaths]}),
            });
            showSuccess(result.message + (result.backup ? ` Backup: ${result.backup}` : ""));
            closeTransfer();
            if (completedBinding.autoSyncEnabled) await api<AutoSyncResult>(`/bindings/${completedBinding.id}/automation/run`, {method: "POST"});
            await load();
        } catch (error) { showError((error as Error).message); }
        finally { setBusy(null); }
    };

    const runOrphanAction = async (composePath: string, action: "restore" | "archive" | "delete") => {
        if (!transferBinding) return;
        setBusy(`orphan-${action}-${composePath}`);
        try {
            const encoded = composePath.split("/").map(encodeURIComponent).join("/");
            const result = await api<{message: string; backup?: string}>(`/bindings/${transferBinding.id}/orphan/${encoded}`, {
                method: "POST", body: JSON.stringify({action, confirmation: action === "restore" ? "" : orphanConfirmation}),
            });
            showSuccess(result.message + (result.backup ? ` Backup: ${result.backup}` : ""));
            setOrphanDecision(null); setOrphanConfirmation(""); closeTransfer(); await load();
        } catch (error) {
            showError((error as Error).message);
            await previewTransfer(transferBinding, "repository_to_stack", false);
        } finally { setBusy(null); }
    };

    const runLocalDeletionAction = async (entry: PreviewEntry, action: "restore" | "delete_git" | "exclude") => {
        if (!transferBinding) return;
        const composePath = composeOwner(transferBinding, entry.path);
        if (!composePath) {
            showError("No synchronized stack owns this deleted file.");
            return;
        }
        const wholeStack = entry.path === composePath;
        if (action === "delete_git" && !localDeletionDecision) {
            setLocalDeletionDecision({composePath, path: wholeStack ? "" : entry.path, wholeStack});
            setLocalDeletionConfirmation("");
            return;
        }
        setBusy(`local-deletion-${action}-${entry.path}`);
        try {
            const encoded = composePath.split("/").map(encodeURIComponent).join("/");
            const result = await api<{message: string}>(`/bindings/${transferBinding.id}/local-deletion/${encoded}`, {
                method: "POST", body: JSON.stringify({
                    action: wholeStack && action === "exclude" ? "deselect" : action,
                    path: wholeStack ? "" : entry.path,
                    confirmation: action === "delete_git" ? localDeletionConfirmation : "",
                }),
            });
            showSuccess(result.message);
            setLocalDeletionDecision(null); setLocalDeletionConfirmation("");
            await previewTransfer(transferBinding, transferDirection, false);
            await load();
        } catch (error) {
            showError((error as Error).message);
        } finally { setBusy(null); }
    };

    const compareConflict = async (entry: PreviewEntry) => {
        if (!transferBinding) return;
        setBusy(`compare-${entry.path}`);
        try {
            setComparison(await api<FileComparison>(`/bindings/${transferBinding.id}/compare/${transferDirection}`, {
                method: "POST", body: JSON.stringify({path: entry.path, includeSensitive, sensitiveConfirmation}),
            }));
        } catch (error) { showError((error as Error).message); }
        finally { setBusy(null); }
    };

    const keepCurrentSource = (path: string) => {
        setResolvedConflictPaths((current) => new Set(current).add(path));
        setComparison(null);
    };

    const keepCurrentTarget = async (path: string) => {
        if (!transferBinding) return;
        setComparison(null);
        const opposite: TransferDirection = transferDirection === "stack_to_repository" ? "repository_to_stack" : "stack_to_repository";
        await previewTransfer(transferBinding, opposite, includeSensitive, path, path);
    };

    const leaveConflictPending = (path: string) => {
        setResolvedConflictPaths((current) => {
            const next = new Set(current); next.delete(path); return next;
        });
    };

    const decideConflict = (path: string, decision: ConflictDecision) => {
        setConflictDecisions((current) => ({...current, [path]: decision}));
        setComparison(null);
    };

    const resolveAutomationConflicts = async () => {
        if (!transferBinding || !transferPreview) return;
        const gitPaths = Object.entries(conflictDecisions).filter(([, decision]) => decision === "git").map(([path]) => path);
        const dockmanPaths = Object.entries(conflictDecisions).filter(([, decision]) => decision === "dockman").map(([path]) => path);
        if (gitPaths.length + dockmanPaths.length === 0) return;
        setBusy(`transfer-${transferBinding.id}`);
        try {
            if (gitPaths.length > 0) {
                await api<TransferResult>(`/bindings/${transferBinding.id}/import`, {method: "POST", body: JSON.stringify({
                    previewToken: transferPreview.previewToken, resolvedPaths: gitPaths, selectedPaths: gitPaths,
                })});
            }
            if (dockmanPaths.length > 0) {
                const preview = await api<TransferPreview>(`/bindings/${transferBinding.id}/preview/stack_to_repository`, {method: "POST", body: JSON.stringify({})});
                const currentConflicts = new Set(preview.entries.filter((entry) => entry.status === "conflict").map((entry) => entry.path));
                const stillConflicting = dockmanPaths.filter((path) => currentConflicts.has(path));
                if (stillConflicting.length !== dockmanPaths.length) throw new Error("Conflict state changed during resolution; refresh and review the remaining files.");
                await api<TransferResult>(`/bindings/${transferBinding.id}/export`, {method: "POST", body: JSON.stringify({
                    previewToken: preview.previewToken, resolvedPaths: dockmanPaths, selectedPaths: dockmanPaths,
                    commitMessage: `chore(stack): resolve Git conflicts for ${transferBinding.stackPath}`,
                })});
            }
            const result = await api<AutoSyncResult>(`/bindings/${transferBinding.id}/automation/run`, {method: "POST"});
            closeTransfer();
            if (result.state === "conflict") showSuccess("Selected conflicts resolved. Other conflicts remain pending.");
            else showSuccess("Conflicts resolved and automatic Git synchronization refreshed.");
            await load();
        } catch (error) {
            showError((error as Error).message);
            await load();
        } finally { setBusy(null); }
    };

    const confirmDeleteBinding = async (forget: boolean) => {
        if (!deleteBinding) return;
        setBusy(`binding-delete-${deleteBinding.id}`);
        try {
            await api<void>(`/bindings/${deleteBinding.id}${forget ? "?forget=true" : ""}`, {method: "DELETE"});
            showSuccess(forget ? "Stack link and synchronization baseline forgotten. No file was deleted." : "Stack link removed. Its synchronization baseline can be restored by recreating the same link.");
            setDeleteBinding(null); await load();
        } catch (error) { showError((error as Error).message); }
        finally { setBusy(null); }
    };

    const openBindingPolicy = (binding: Binding) => {
        setPolicyBinding(binding);
        setPolicyForm({profile: binding.syncProfile || "compose_config", includes: (binding.includePatterns || []).join("\n"), excludes: (binding.excludePatterns || []).join("\n")});
    };

    const saveBindingPolicy = async () => {
        if (!policyBinding) return;
        setBusy(`binding-policy-${policyBinding.id}`);
        try {
            await api<Binding>(`/bindings/${policyBinding.id}/policy`, {method: "PUT", body: JSON.stringify({
                profile: policyForm.profile,
                includePatterns: policyForm.includes.split("\n"),
                excludePatterns: policyForm.excludes.split("\n"),
            })});
            showSuccess("Stack synchronization policy updated.");
            setPolicyBinding(null);
            await load();
        } catch (error) { showError((error as Error).message); }
        finally { setBusy(null); }
    };

    const refreshBindingComposeCatalog = async (binding: Binding) => {
        const refreshed = await api<Binding>(`/bindings/${binding.id}/refresh-compose`, {method: "POST"});
        setBindings((current) => current.map((candidate) => candidate.id === refreshed.id ? refreshed : candidate));
        return refreshed;
    };

    const openComposeSelection = async (binding: Binding) => {
        setBusy(`binding-compose-refresh-${binding.id}`);
        try {
            const refreshed = await refreshBindingComposeCatalog(binding);
            const statuses = await api<Array<{bindingId: string; composePath: string; state: string}>>(`/stack-statuses?host=${encodeURIComponent(refreshed.host)}`);
            setComposeBinding(refreshed);
            setComposePathStates(Object.fromEntries(statuses.filter((status) => status.bindingId === refreshed.id).map((status) => [status.composePath, status.state])));
            setSelectedComposePaths(new Set(refreshed.selectedComposePaths || (refreshed.composeSelectionMode === "selected" ? [] : refreshed.composePaths)));
        } catch (error) { showError((error as Error).message); }
        finally { setBusy(null); }
    };

    const saveComposeSelection = async () => {
        if (!composeBinding) return;
        setBusy(`binding-compose-${composeBinding.id}`);
        try {
            const selected = composeBinding.composePaths.filter((path) => selectedComposePaths.has(path));
            const mode = selected.length === composeBinding.composePaths.length ? "all" : "selected";
            const updated = await api<Binding>(`/bindings/${composeBinding.id}/compose-selection`, {
                method: "PUT", body: JSON.stringify({mode, composePaths: selected}),
            });
            setBindings((current) => current.map((binding) => binding.id === updated.id ? updated : binding));
            setComposeBinding(null);
            showSuccess(mode === "all" ? "All currently discovered stacks are synchronized. Future local stacks remain unselected until approved." : `${selected.length} stack${selected.length === 1 ? "" : "s"} selected for synchronization.`);
        } catch (error) { showError((error as Error).message); }
        finally { setBusy(null); }
    };

    const openBindingAutomation = async (binding: Binding) => {
        setBusy(`binding-compose-refresh-${binding.id}`);
        try {
            const refreshed = await refreshBindingComposeCatalog(binding);
            setAutomationBinding(refreshed);
            setAutomationForm({enabled: refreshed.autoSyncEnabled, autoReconcile: refreshed.autoReconcileEnabled, intervalMinutes: refreshed.autoSyncIntervalMinutes || 15, deployEnabled: refreshed.autoDeployEnabled, deployNewStacks: refreshed.autoDeployNewStacks, deployRollback: refreshed.autoDeployRollbackEnabled, deployComposePaths: refreshed.autoDeployComposePaths || []});
            setDeployments([]);
            void api<Deployment[]>(`/bindings/${binding.id}/deployments`).then(setDeployments).catch(() => setDeployments([]));
        } catch (error) { showError((error as Error).message); }
        finally { setBusy(null); }
    };

    const openRepositoryPolicy = (repository: Repository) => {
        setPolicyRepository(repository);
        setRepositoryExcludePatterns((repository.excludePatterns || []).join("\n"));
    };

    const saveRepositoryPolicy = async () => {
        if (!policyRepository) return;
        setBusy(`repository-policy-${policyRepository.id}`);
        try {
            await api<Repository>(`/repositories/${policyRepository.id}/policy`, {method: "PUT", body: JSON.stringify({excludePatterns: repositoryExcludePatterns.split("\n")})});
            showSuccess("Repository-wide exclusions updated.");
            setPolicyRepository(null);
            await load();
        } catch (error) { showError((error as Error).message); }
        finally { setBusy(null); }
    };

    const openBindingState = (binding: Binding) => {
        if (binding.autoSyncState === "conflict") {
            void previewTransfer(binding, "repository_to_stack", false, undefined, undefined, true);
            return;
        }
        if (binding.autoSyncState === "blocked") {
            void previewTransfer(binding, "repository_to_stack");
            return;
        }
        if (binding.autoSyncState === "error" || binding.autoSyncState === "partial") openBindingAutomation(binding);
    };

    useEffect(() => {
        if (loading || bindings.length === 0) return;
        const bindingID = searchParams.get('gitBinding') || '';
        const action = searchParams.get('gitAction') || '';
        const composePath = searchParams.get('gitCompose') || '';
        const key = `${bindingID}:${action}:${composePath}`;
        if (!bindingID || openedGitDeepLink.current === key) return;
        const binding = bindings.find((item) => item.id === bindingID);
        if (!binding) return;
        openedGitDeepLink.current = key;
        if (action === 'details') {
            setAutomationBinding(binding);
            setAutomationForm({enabled: binding.autoSyncEnabled, autoReconcile: binding.autoReconcileEnabled, intervalMinutes: binding.autoSyncIntervalMinutes || 15, deployEnabled: binding.autoDeployEnabled, deployNewStacks: binding.autoDeployNewStacks, deployRollback: binding.autoDeployRollbackEnabled, deployComposePaths: binding.autoDeployComposePaths || []});
            setDeployments([]);
            void api<Deployment[]>(`/bindings/${binding.id}/deployments`).then(setDeployments).catch(() => setDeployments([]));
        } else {
            const direction: TransferDirection = action === 'conflicts' || action === 'preview_git' || binding.autoSyncState === 'conflict'
                ? 'repository_to_stack'
                : 'stack_to_repository';
            const conflictMode = action === 'conflicts' || binding.autoSyncState === 'conflict';
            setConflictResolutionMode(conflictMode);
            setConflictDecisions({});
            setBusy(`preview-${binding.id}`);
            void api<TransferPreview>(`/bindings/${binding.id}/preview/${direction}`, {
                method: 'POST', body: JSON.stringify({}),
            }).then((preview) => {
                setTransferBinding(binding); setTransferDirection(direction); setTransferPreview(preview); setPreviewPage(0);
                setPreviewStatus(conflictMode ? 'conflict' : 'all'); setPreviewPageInput('1'); setSelectedPreviewPaths(new Set());
                setResolvedConflictPaths(new Set()); setSelectedTransferPaths(new Set());
                const separator = composePath.lastIndexOf('/');
                setPreviewSearch(separator >= 0 ? composePath.slice(0, separator) : '');
            }).catch((error) => showError((error as Error).message)).finally(() => setBusy(null));
        }
        const next = new URLSearchParams(searchParams);
        next.delete('gitBinding'); next.delete('gitAction'); next.delete('gitCompose');
        setSearchParams(next, {replace: true});
    }, [bindings, loading, searchParams, setSearchParams, showError]);

    const saveBindingAutomation = async () => {
        if (!automationBinding) return;
        setBusy(`binding-automation-${automationBinding.id}`);
        try {
            await api<Binding>(`/bindings/${automationBinding.id}/automation`, {
                method: "PUT", body: JSON.stringify(automationForm),
            });
            showSuccess(automationForm.deployEnabled ? "Automatic Git monitoring and controlled deployment enabled." : automationForm.enabled ? "Automatic Git monitoring enabled." : "Automatic Git monitoring disabled.");
            setAutomationBinding(null);
            await load();
        } catch (error) { showError((error as Error).message); }
        finally { setBusy(null); }
    };

    const runBindingAutomation = async (binding: Binding) => {
        setBusy(`binding-auto-run-${binding.id}`);
        try {
            const result = await api<AutoSyncResult>(`/bindings/${binding.id}/automation/run`, {method: "POST"});
            if (result.state === "conflict" || result.state === "blocked" || result.state === "error") showError(result.message);
            else if (result.state === "partial") showWarning(result.message);
            else showSuccess(result.message);
            await load();
        } catch (error) { showError((error as Error).message); await load(); }
        finally { setBusy(null); }
    };

    const addPreviewExclusions = async (entriesToExclude: Array<{path: string; directory: boolean}>) => {
        if (!transferBinding || entriesToExclude.length === 0) return;
        const previousPreview = transferPreview;
        setExcludeMenu(null);
        const exactPaths = new Set(entriesToExclude.filter((entry) => !entry.directory).map((entry) => entry.path));
        const directoryPaths = entriesToExclude.filter((entry) => entry.directory).map((entry) => entry.path);
        setTransferPreview((current) => {
            if (!current) return current;
            let changed = current.changed;
            let skipped = current.skipped;
            const entries = current.entries.map((entry) => {
                const matches = exactPaths.has(entry.path) || directoryPaths.some((path) => entry.path === path || entry.path.startsWith(`${path}/`));
                if (!matches || entry.status === "skipped_excluded") return entry;
                if (entry.status === "add" || entry.status === "modify") changed--;
                if (!entry.status.startsWith("skipped_")) skipped++;
                return {...entry, status: "skipped_excluded" as const};
            });
            return {...current, entries, changed: Math.max(0, changed), skipped};
        });
        setBusy(`binding-exclusion-${transferBinding.id}`);
        try {
            const updated = await api<Binding>(`/bindings/${transferBinding.id}/exclusions/batch`, {
                method: "POST", body: JSON.stringify({entries: entriesToExclude}),
            });
            const preview = await api<TransferPreview>(`/bindings/${updated.id}/preview/${transferDirection}`, {
                method: "POST", body: JSON.stringify({includeSensitive, sensitiveConfirmation: includeSensitive ? sensitiveConfirmation : ""}),
            });
            setTransferBinding(updated); setTransferPreview(preview);
            setBindings((current) => current.map((binding) => binding.id === updated.id ? updated : binding));
            setSelectedPreviewPaths(new Set()); setResolvedConflictPaths(new Set()); setSelectedTransferPaths(new Set());
            showSuccess(entriesToExclude.length === 1 ? `${entriesToExclude[0].directory ? "Folder" : "File"} ${entriesToExclude[0].path} excluded.` : `${entriesToExclude.length} items excluded.`);
        } catch (error) { setTransferPreview(previousPreview); showError((error as Error).message); }
        finally { setBusy(null); }
    };

    const addPreviewInclusions = async (entriesToInclude: PreviewEntry[]) => {
        if (!transferBinding || entriesToInclude.length === 0) return;
        const previousPreview = transferPreview;
        const paths = new Set(entriesToInclude.map((entry) => entry.path));
        setTransferPreview((current) => {
            if (!current) return current;
            let changed = current.changed;
            let skipped = current.skipped;
            const entries = current.entries.map((entry) => {
                if (!paths.has(entry.path) || entry.status !== "skipped_type") return entry;
                changed++;
                skipped--;
                return {...entry, status: "add" as const};
            });
            return {...current, entries, changed, skipped: Math.max(0, skipped)};
        });
        setBusy(`binding-inclusion-${transferBinding.id}`);
        try {
            const updated = await api<Binding>(`/bindings/${transferBinding.id}/inclusions/batch`, {
                method: "POST", body: JSON.stringify({paths: entriesToInclude.map((entry) => entry.path)}),
            });
            const preview = await api<TransferPreview>(`/bindings/${updated.id}/preview/${transferDirection}`, {
                method: "POST", body: JSON.stringify({includeSensitive, sensitiveConfirmation: includeSensitive ? sensitiveConfirmation : ""}),
            });
            setTransferBinding(updated); setTransferPreview(preview);
            setBindings((current) => current.map((binding) => binding.id === updated.id ? updated : binding));
            setSelectedPreviewPaths(new Set()); setResolvedConflictPaths(new Set()); setSelectedTransferPaths(new Set());
            showSuccess(`${entriesToInclude.length} file${entriesToInclude.length === 1 ? "" : "s"} allowed by the synchronization policy.`);
        } catch (error) { setTransferPreview(previousPreview); showError((error as Error).message); }
        finally { setBusy(null); }
    };

    const changePreviewPage = (page: number) => {
        const next = Math.max(0, Math.min(page, previewPageCount - 1));
        setPreviewPage(next);
        setPreviewPageInput(String(next + 1));
        setSelectedPreviewPaths(new Set());
    };

    const toggleVisiblePreviewEntries = (checked: boolean) => {
        setSelectedPreviewPaths(checked ? new Set(selectablePreviewEntries.map((entry) => entry.path)) : new Set());
    };

    const togglePreviewEntry = (path: string, checked: boolean) => {
        setSelectedPreviewPaths((current) => {
            const next = new Set(current);
            if (checked) next.add(path); else next.delete(path);
            return next;
        });
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
                Git transfers remain non-destructive. Optional automatic monitoring is Git → Dockman only, creates a backup before changes, stops on conflicts, and never deploys or restarts a stack.
                {" "}Repositories use a compact shared object store; files are checked out temporarily only while exporting Dockman changes.
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
                                        <Stack direction="row" spacing={.25} sx={{alignItems: "center", maxWidth: 390}}><Tooltip title={repository.remoteUrl}><Typography variant="caption" color="text.secondary" sx={{minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap"}}>{repository.remoteUrl}</Typography></Tooltip><CopyButton handleCopy={copyRepositoryUrl} activeID={copiedRepositoryUrl || ""} thisID={repository.remoteUrl} tooltip="Copy repository URL"/></Stack>
                                        {repository.lastError && <Typography variant="caption" color="error" sx={{display: "block", maxWidth: 360}}>{repository.lastError}</Typography>}
                                    </TableCell>
                                    <TableCell sx={{fontFamily: "monospace"}}>{repository.defaultBranch}</TableCell>
                                    <TableCell>{repository.credentialId ? credentialNames[repository.credentialId] || "Unknown" : "None (public)"}</TableCell>
                                    <TableCell>
                                        <Stack direction="row" spacing={.75} sx={{alignItems: "center"}}>
                                            <Chip size="small" color={statusColor(state)} variant="outlined" label={state}/>
                                            <Chip size="small" variant="outlined" label={repository.storageMode === "compact" ? "compact" : "migration pending"}/>
                                            {gitStatus && !gitStatus.clean && <Chip size="small" color="warning" label="dirty"/>}
                                            {gitStatus && (gitStatus.ahead > 0 || gitStatus.behind > 0) && <Typography variant="caption" color="text.secondary">↑{gitStatus.ahead} ↓{gitStatus.behind}</Typography>}
                                        </Stack>
                                    </TableCell>
                                    <TableCell>{dateLabel(repository.lastFetchedAt)}</TableCell>
                                    <TableCell align="right" sx={{whiteSpace: "nowrap"}}>
                                        <Tooltip title="Fetch remote state"><span><IconButton size="small" disabled={busy !== null || !repository.workspacePresent} onClick={() => void repositoryAction(repository, "fetch")}><RefreshOutlined fontSize="small"/></IconButton></span></Tooltip>
                                        <Tooltip title="Pull fast-forward changes"><span><IconButton size="small" disabled={busy !== null || !repository.workspacePresent} onClick={() => void repositoryAction(repository, "pull")}><CloudDownloadOutlined fontSize="small"/></IconButton></span></Tooltip>
                                        <Tooltip title="Push local commits"><span><IconButton size="small" disabled={busy !== null || !repository.workspacePresent} onClick={() => void repositoryAction(repository, "push")}><CloudUploadOutlined fontSize="small"/></IconButton></span></Tooltip>
                                        <Tooltip title="Repository-wide exclusions"><IconButton size="small" disabled={busy !== null} onClick={() => openRepositoryPolicy(repository)}><TuneOutlined fontSize="small"/></IconButton></Tooltip>
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
                        <Typography variant="h6">Folder links</Typography>
                        <Typography variant="body2" color="text.secondary">Link a complete stacks root to a Git folder or a dedicated repository. Its full subfolder tree is preserved automatically.</Typography>
                    </Box>
                    <Button variant="contained" startIcon={<LinkOutlined/>} disabled={repositories.length === 0 || busy !== null} onClick={openBindingCreate}>Link folder</Button>
                </Stack>
                <TableContainer><Table size="small">
                    <TableHead><TableRow><TableCell>Source folder</TableCell><TableCell>Git destination</TableCell><TableCell>Compose files</TableCell><TableCell>Automatic Git → Dockman</TableCell><TableCell align="right">Actions</TableCell></TableRow></TableHead>
                    <TableBody>
                        {bindings.length === 0 && <TableRow><TableCell colSpan={5} align="center" sx={{py: 5, color: "text.secondary"}}>No stack linked to a repository.</TableCell></TableRow>}
                        {bindings.map((binding) => <TableRow key={binding.id} hover>
                            <TableCell><Typography variant="body2" sx={{fontWeight: 700}}>{binding.stackPath}</Typography><Typography variant="caption" color="text.secondary">Complete folder on {binding.host}</Typography></TableCell>
                            <TableCell><Typography variant="body2">{binding.repositoryName}</Typography><Typography variant="caption" color="text.secondary" sx={{fontFamily: "monospace"}}>{binding.subPath === "." ? "/" : `/${binding.subPath}`}</Typography><Stack direction="row" spacing={.5} sx={{mt: .5, alignItems: "center"}}><Chip size="small" variant="outlined" color={binding.syncProfile === "all_files" ? "warning" : "info"} label={binding.syncProfile === "all_files" ? "All regular files" : "Configuration files"}/><Tooltip title={binding.initialSyncError || "Initial link state"}><Chip size="small" variant="outlined" color={binding.initialSyncState === "error" ? "error" : binding.initialSyncState === "reconciled" || binding.initialSyncState === "imported" || binding.initialSyncState === "exported" ? "success" : "default"} label={(binding.initialSyncState || "pending").replaceAll("_", " ")}/></Tooltip></Stack></TableCell>
                            <TableCell>{binding.composePaths.length ? <Tooltip title="View and choose synchronized stacks"><Chip size="small" clickable color={(binding.selectedComposePaths || []).length === binding.composePaths.length ? "info" : "warning"} variant="outlined" onClick={() => openComposeSelection(binding)} label={(binding.selectedComposePaths || []).length}/></Tooltip> : <Chip size="small" color="warning" variant="outlined" label="0"/>}</TableCell>
                            <TableCell sx={{minWidth: 190}}>
                                <Stack direction="row" spacing={.5} sx={{alignItems: "center"}}>
                                    <Tooltip title={binding.autoSyncState === "conflict" ? "Open and resolve conflicts" : binding.autoSyncState === "blocked" ? "Open preserved changes and Git deletions" : binding.autoSyncState === "error" || binding.autoSyncState === "partial" ? "Open error details" : binding.autoSyncError || (binding.autoSyncEnabled ? `Every ${binding.autoSyncIntervalMinutes} minutes` : "Disabled by default")}><Chip size="small" variant="outlined" clickable={binding.autoSyncState === "conflict" || binding.autoSyncState === "blocked" || binding.autoSyncState === "error" || binding.autoSyncState === "partial"} onClick={() => openBindingState(binding)} color={!binding.autoSyncEnabled ? "default" : binding.autoSyncState === "up_to_date" ? "success" : binding.autoSyncState === "conflict" || binding.autoSyncState === "error" ? "error" : binding.autoSyncState === "blocked" || binding.autoSyncState === "partial" ? "warning" : "info"} label={!binding.autoSyncEnabled ? "off" : binding.autoSyncState.replaceAll("_", " ")}/></Tooltip>
                                    <Tooltip title="Configure automatic monitoring"><IconButton size="small" disabled={busy !== null} onClick={() => openBindingAutomation(binding)}><SyncOutlined fontSize="small"/></IconButton></Tooltip>
                                    {binding.autoSyncEnabled && <Tooltip title="Check and synchronize now"><span><IconButton size="small" disabled={busy !== null} onClick={() => void runBindingAutomation(binding)}>{busy === `binding-auto-run-${binding.id}` ? <CircularProgress size={17}/> : <RefreshOutlined fontSize="small"/>}</IconButton></span></Tooltip>}
                                    {binding.autoDeployEnabled && <Tooltip title={binding.autoDeployState === "failed" || binding.autoDeployState === "partial" ? "Open deployment error details" : binding.autoDeployError || `${binding.autoDeployComposePaths.length} controlled deployment target(s)`}><Chip size="small" variant="outlined" clickable={binding.autoDeployState === "failed" || binding.autoDeployState === "partial"} onClick={binding.autoDeployState === "failed" || binding.autoDeployState === "partial" ? () => openBindingAutomation(binding) : undefined} color={binding.autoDeployState === "failed" || binding.autoDeployState === "partial" ? "error" : "warning"} label={binding.autoDeployState === "success" ? "deployed" : binding.autoDeployState === "partial" ? "partial deploy" : "auto deploy"}/></Tooltip>}
                                </Stack>
                                {binding.lastAutoSyncAt && <Typography variant="caption" color="text.secondary">Checked {dateLabel(binding.lastAutoSyncAt)}</Typography>}
                            </TableCell>
                            <TableCell align="right" sx={{whiteSpace: "nowrap"}}>
								<Tooltip title="Folder-link activity"><IconButton size="small" disabled={busy !== null} onClick={() => setRecoveryView({binding, tab: 'activity'})}><HistoryOutlined fontSize="small"/></IconButton></Tooltip>
								<Tooltip title="Backups and recovery"><IconButton size="small" disabled={busy !== null} onClick={() => setRecoveryView({binding, tab: 'backups'})}><RestoreOutlined fontSize="small"/></IconButton></Tooltip>
                                <Tooltip title="Synchronization policy"><IconButton size="small" disabled={busy !== null} onClick={() => openBindingPolicy(binding)}><TuneOutlined fontSize="small"/></IconButton></Tooltip>
                                <Tooltip title="Preview stack → Git"><span><IconButton size="small" disabled={busy !== null} onClick={() => void previewTransfer(binding, "stack_to_repository")}><CloudUploadOutlined fontSize="small"/></IconButton></span></Tooltip>
                                <Tooltip title="Preview Git → stack"><span><IconButton size="small" disabled={busy !== null} onClick={() => void previewTransfer(binding, "repository_to_stack")}><CloudDownloadOutlined fontSize="small"/></IconButton></span></Tooltip>
                                <Tooltip title="Remove link"><IconButton size="small" color="error" disabled={busy !== null} onClick={() => setDeleteBinding(binding)}><DeleteOutlined fontSize="small"/></IconButton></Tooltip>
                            </TableCell>
                        </TableRow>)}
                    </TableBody>
                </Table></TableContainer>
            </Paper>

			{recoveryView && <GitBindingRecovery binding={recoveryView.binding} initialTab={recoveryView.tab} onClose={() => setRecoveryView(null)}/>}

            <Paper variant="outlined" sx={{borderRadius: 2, overflow: "hidden"}}>
                <Stack direction={{xs: "column", md: "row"}} sx={{p: 2.25, justifyContent: "space-between", gap: 2}}>
                    <Box>
                        <Typography variant="h6">Git credentials</Typography>
                        <Typography variant="body2" color="text.secondary">Secrets are encrypted at rest and are never returned by the API.</Typography>
                    </Box>
                    <Button variant="outlined" startIcon={<Add/>} onClick={openCredentialCreate}>Add credential</Button>
                </Stack>
                <Box sx={{px: 2.25, pb: 2}}><TextField value={repositoryUrl} onChange={(event) => setRepositoryUrl(event.target.value)} label="Repository for connection tests (optional)" placeholder="owner/repository or https://github.com/owner/repository" size="small" fullWidth helperText="GitHub HTTPS/SSH URLs and owner/repository are accepted; embedded credentials remain forbidden."/></Box>
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

        <Dialog open={policyRepository !== null} onClose={() => busy === null && setPolicyRepository(null)} fullWidth maxWidth="sm">
            <DialogTitle sx={{display: "flex", alignItems: "center", gap: 1}}><TuneOutlined/>Repository-wide exclusions</DialogTitle>
            <DialogContent dividers><Stack spacing={2} sx={{pt: .5}}>
                <Alert severity="info">These rules apply to every folder link in this repository and in both synchronization directions. Compose files remain protected.</Alert>
                <TextField label="Exclude rules" value={repositoryExcludePatterns} onChange={(event) => setRepositoryExcludePatterns(event.target.value)} multiline minRows={7} maxRows={16} placeholder={"/README.md\n/docs/**\n**/*.log"} helperText="One glob per line. Start with / to anchor a rule at the repository root; /README.md ignores only the root README." sx={{"& textarea": {fontFamily: "monospace"}}}/>
            </Stack></DialogContent>
            <DialogActions><Button onClick={() => setPolicyRepository(null)} disabled={busy !== null}>Cancel</Button><Button variant="contained" onClick={() => void saveRepositoryPolicy()} disabled={busy !== null}>{busy?.startsWith("repository-policy-") && <CircularProgress size={16} sx={{mr: 1}}/>}Save exclusions</Button></DialogActions>
        </Dialog>

        <Dialog open={bindingDialogOpen} onClose={() => busy === null && setBindingDialogOpen(false)} fullWidth maxWidth="md">
            <DialogTitle>Link a complete stacks folder to Git</DialogTitle>
            <DialogContent dividers><Stack spacing={2} sx={{pt: .5}}>
                <FormControl><InputLabel>Repository</InputLabel><Select label="Repository" value={bindingForm.repositoryId} onChange={(event) => setBindingForm({...bindingForm, repositoryId: event.target.value})}>
                    {repositories.map((repository) => <MenuItem key={repository.id} value={repository.id}>{repository.name}</MenuItem>)}
                </Select></FormControl>
                {stackTargets.length > 0 && <FormControl><InputLabel>Source folder</InputLabel><Select label="Source folder" value={stackTargets.some((target) => `${target.host}\n${target.path}` === `${bindingForm.host}\n${bindingForm.stackPath}`) ? `${bindingForm.host}\n${bindingForm.stackPath}` : ""} onChange={(event) => {
                    const target = stackTargets.find((item) => `${item.host}\n${item.path}` === event.target.value);
                    if (target) {
                        setBindingForm({...bindingForm, host: target.host, stackPath: target.path});
                        setBindingComposePaths(new Set(target.composePaths));
                    }
                }}><MenuItem value=""><em>Custom folder</em></MenuItem>{stackTargets.map((target) => <MenuItem key={`${target.host}-${target.path}`} value={`${target.host}\n${target.path}`}>{target.scope === "all_stacks" ? "All stacks" : "Folder"} — {target.host} / {target.path} ({target.stackCount} stack{target.stackCount === 1 ? "" : "s"})</MenuItem>)}</Select></FormControl>}
                <Stack direction={{xs: "column", sm: "row"}} spacing={2}><TextField fullWidth label="Host" value={bindingForm.host} onChange={(event) => setBindingForm({...bindingForm, host: event.target.value})} required/><TextField fullWidth label="Complete source folder" value={bindingForm.stackPath} onChange={(event) => setBindingForm({...bindingForm, stackPath: event.target.value})} placeholder="compose" required/></Stack>
                <FormControl><InputLabel>Git destination</InputLabel><Select label="Git destination" value={bindingForm.targetMode} onChange={(event) => {
                    const targetMode = event.target.value as "repository_folder" | "repository_root";
                    setBindingForm({...bindingForm, targetMode, subPath: targetMode === "repository_root" ? "." : (bindingForm.subPath === "." ? "stacks" : bindingForm.subPath)});
                }}><MenuItem value="repository_folder">A folder inside a shared repository</MenuItem><MenuItem value="repository_root">The root of a dedicated repository</MenuItem></Select></FormControl>
                {bindingForm.targetMode === "repository_folder" && <TextField label="Repository folder" value={bindingForm.subPath} onChange={(event) => setBindingForm({...bindingForm, subPath: event.target.value})} placeholder="stacks" helperText="Every stack subfolder is preserved below this destination." required/>}
                {bindingStackTarget?.composePaths.length ? <Box><Typography variant="subtitle2" sx={{mb: 1}}>Stacks included in this link</Typography><ComposePathSelector key={`${bindingStackTarget.host}-${bindingStackTarget.path}`} paths={bindingStackTarget.composePaths} selectedPaths={bindingComposePaths} onChange={setBindingComposePaths} selectedLabel="included" unselectedLabel="excluded" maxHeight="32vh"/>{bindingComposePaths.size === 0 && <Alert severity="warning" sx={{mt: 1.25}}>The folder link will be created with no synchronized stack. You can add stacks later.</Alert>}</Box> : <Alert severity="info">For a custom folder, Dockman discovers its Compose files while creating the link and initially includes all of them.</Alert>}
                <FormControlLabel control={<Switch checked={bindingForm.autoReconcile} onChange={(event) => setBindingForm({...bindingForm, autoReconcile: event.target.checked})}/>} label="Automatically reconcile when Dockman and Git are already identical"/>
                <FormControl><InputLabel>Link initialization</InputLabel><Select label="Link initialization" value={bindingForm.initialSync} onChange={(event) => setBindingForm({...bindingForm, initialSync: event.target.value as "none" | TransferDirection})}>
                    <MenuItem value="none">Link only — decide after preview</MenuItem>
                    <MenuItem value="stack_to_repository">Dockman → Git — commit and push allowed files</MenuItem>
                    <MenuItem value="repository_to_stack">Git → Dockman — backup and import allowed files</MenuItem>
                </Select></FormControl>
                <Alert severity={bindingForm.initialSync === "none" ? "info" : "warning"}>{bindingForm.initialSync === "none" ? "The link remains non-destructive. If both sides are identical, automatic reconciliation records their common baseline without copying anything." : bindingForm.initialSync === "stack_to_repository" ? "Dockman becomes the explicit source for differing files. Git-only files are preserved; allowed changes are committed and pushed." : "Git becomes the explicit source for differing files. Dockman-only files are preserved; changed files are backed up before import. Nothing is deployed automatically."}</Alert>
            </Stack></DialogContent>
            <DialogActions><Button onClick={() => setBindingDialogOpen(false)} disabled={busy !== null}>Cancel</Button><Button variant="contained" onClick={() => void saveBinding()} disabled={busy !== null || !bindingForm.repositoryId || !bindingForm.host.trim() || !bindingForm.stackPath.trim() || !bindingForm.subPath.trim()}>{busy === "binding-save" && <CircularProgress size={16} sx={{mr: 1}}/>}Link</Button></DialogActions>
        </Dialog>

        <Dialog open={transferBinding !== null} onClose={() => busy === null && closeTransfer()} fullWidth maxWidth="md">
            <DialogTitle sx={{display: "flex", alignItems: "center", gap: 1}}><CompareArrowsOutlined/>{conflictResolutionMode ? "Resolve synchronization conflicts" : transferDirection === "stack_to_repository" ? "Export stack to Git" : "Import Git into stack"}</DialogTitle>
            <DialogContent dividers><Stack spacing={2}>
                <Alert severity={conflictResolutionMode ? "error" : transferDirection === "stack_to_repository" ? "info" : "warning"}>
                    {conflictResolutionMode ? "Choose Git or Dockman independently for each conflict, then validate. Files left without a decision remain pending." : transferDirection === "stack_to_repository" ? "Changed stack files will be committed and pushed. Remote-only files are preserved." : "Changed repository files will be copied into the stack after a backup. Stack deployment is never triggered."}
                </Alert>
                <Stack direction={{xs: "column", sm: "row"}} spacing={1} sx={{alignItems: {sm: "center"}}}>
                    <Chip label={`${transferPreview?.changed || 0} changed`} color={transferPreview?.changed ? "warning" : "success"}/>
                    <Chip label={`${transferPreview?.skipped || 0} skipped`} variant="outlined" color={transferPreview?.skipped ? "warning" : "default"}/>
                    {!!transferPreview?.preserved && <Chip label={`${transferPreview.preserved} preserved Git deletion${transferPreview.preserved === 1 ? "" : "s"}`} variant="outlined" color="warning"/>}
                    {!!transferPreview?.conflicts && <Chip label={`${transferPreview.conflicts} conflict${transferPreview.conflicts === 1 ? "" : "s"}`} color="error"/>}
                    <Typography variant="body2" color="text.secondary" sx={{ml: {sm: "auto!important"}}}>No source-side deletion is propagated.</Typography>
                </Stack>
                {!!transferPreview?.skipped && <Alert severity="warning">Skipped files are never copied. Files skipped only by type can be permanently allowed here. Oversized, unavailable, sensitive, and explicitly excluded files keep their dedicated protection.</Alert>}
                {!!transferPreview?.preserved && <Alert severity="warning">Files deleted on Git remain local by default. For a whole orphaned stack, restore it to Git, archive it, or explicitly delete its local folder after a backup. Dockman never runs Compose down and never removes Docker volumes here.</Alert>}
                {!!transferPreview?.localDeletions && <Alert severity="warning">A synchronized stack or file was deleted locally but remains on Git. Regular transfer will not delete it from Git. Use the stack synchronization icon to restore it, explicitly delete it from Git, or stop synchronizing it.</Alert>}
                {!!transferPreview?.conflicts && <Alert severity="error">
                    Conflicts are never overwritten automatically. Compare and approve only the files you want to resolve; the others remain pending. An “initial conflict” means that no common synchronization baseline is available.
                </Alert>}
                <Stack direction={{xs: "column", sm: "row"}} spacing={1} sx={{alignItems: {sm: "center"}}}>
                    <TextField size="small" value={previewSearch} onChange={(event) => {
                        setPreviewSearch(event.target.value); setPreviewPage(0); setPreviewPageInput("1"); setSelectedPreviewPaths(new Set());
                    }} placeholder="Search by path…" slotProps={{input: {startAdornment: <SearchOutlined fontSize="small" sx={{mr: 1, color: "text.secondary"}}/>}}} sx={{flex: 1}}/>
                    <FormControl size="small" sx={{minWidth: 205}}><InputLabel id="preview-status-label">Status</InputLabel><Select labelId="preview-status-label" label="Status" value={previewStatus} onChange={(event) => {
                        setPreviewStatus(event.target.value as "all" | PreviewStatus); setPreviewPage(0); setPreviewPageInput("1"); setSelectedPreviewPaths(new Set());
                    }}><MenuItem value="all">All statuses ({transferPreview?.entries.length || 0})</MenuItem>{previewStatuses.filter((status) => (previewStatusCounts.get(status) || 0) > 0).map((status) => <MenuItem key={status} value={status}>{status.replaceAll("_", " ")} ({previewStatusCounts.get(status)})</MenuItem>)}</Select></FormControl>
                    <Button variant="outlined" color="success" startIcon={<CheckCircleOutlined/>} disabled={busy !== null || allowableSelectedEntries.length === 0} onClick={() => void addPreviewInclusions(allowableSelectedEntries)}>
                        Allow ({allowableSelectedEntries.length})
                    </Button>
                    <Button variant="outlined" color="warning" startIcon={<BlockOutlined/>} disabled={busy !== null || selectedPreviewPaths.size === 0} onClick={() => void addPreviewExclusions(visiblePreviewEntries.filter((entry) => selectedPreviewPaths.has(entry.path)).map((entry) => ({path: entry.path, directory: !!entry.directory})))}>
                        Exclude selected ({selectedPreviewPaths.size})
                    </Button>
                </Stack>
                <TableContainer component={Paper} variant="outlined" sx={{maxHeight: 340}}><Table size="small" stickyHeader><TableHead><TableRow><TableCell padding="checkbox"><Checkbox size="small" disabled={busy !== null || selectablePreviewEntries.length === 0} checked={selectablePreviewEntries.length > 0 && selectedVisibleCount === selectablePreviewEntries.length} indeterminate={selectedVisibleCount > 0 && selectedVisibleCount < selectablePreviewEntries.length} onChange={(_, checked) => toggleVisiblePreviewEntries(checked)} slotProps={{input: {"aria-label": "Select all items on this page"}}}/></TableCell><TableCell>File</TableCell><TableCell>Status</TableCell><TableCell>Size</TableCell><TableCell>Resolution</TableCell><TableCell align="right">Exclude</TableCell></TableRow></TableHead><TableBody>
                    {!filteredPreviewEntries.length && <TableRow><TableCell colSpan={6} align="center" sx={{py: 4, color: "text.secondary"}}>{previewSearch ? "No item matches this search." : "No difference."}</TableCell></TableRow>}
                    {visiblePreviewEntries.map((entry) => {
                        const orphanCompose = orphanComposePaths.has(entry.path);
                        const rootOrphan = orphanCompose && !entry.path.includes("/");
                        const deletedConflict = entry.conflictKind === "source_deleted_destination_changed";
                        return <TableRow key={entry.path} selected={selectedPreviewPaths.has(entry.path)}>
                            <TableCell padding="checkbox"><Checkbox size="small" checked={selectedPreviewPaths.has(entry.path)} disabled={busy !== null || entry.status === "skipped_excluded" || entry.status === "conflict" || entry.status === "deleted_on_git" || entry.status === "deleted_locally"} onChange={(_, checked) => togglePreviewEntry(entry.path, checked)} slotProps={{input: {"aria-label": `Select ${entry.path}`}}}/></TableCell>
                            <TableCell sx={{fontFamily: "monospace", overflowWrap: "anywhere"}}>{entry.path}</TableCell>
                            <TableCell><Chip size="small" variant="outlined" color={entry.status === "conflict" ? "error" : entry.status === "deleted_on_git" || entry.status === "deleted_locally" || entry.conflictKind === "destination_deleted" || entry.status.startsWith("skipped_") ? "warning" : entry.status === "modify" ? "info" : "success"} label={deletedConflict ? "deleted on Git · local changed" : entry.conflictKind === "destination_deleted" ? "deleted locally · restore available" : entry.status === "conflict" && entry.conflictKind === "no_baseline" ? "initial conflict" : entry.status.replaceAll("_", " ")}/></TableCell>
                            <TableCell>{entry.size === undefined ? "—" : formatBytes(entry.size)}</TableCell>
                            <TableCell>{entry.status === "deleted_locally" ? <Stack direction="row" spacing={.5} sx={{alignItems: "center", flexWrap: "wrap"}}>
                                <Button size="small" color="success" variant="outlined" startIcon={<CloudDownloadOutlined/>} disabled={busy !== null} onClick={() => void runLocalDeletionAction(entry, "restore")}>Restore from Git</Button>
                                <Button size="small" variant="outlined" startIcon={<BlockOutlined/>} disabled={busy !== null} onClick={() => void runLocalDeletionAction(entry, "exclude")}>{entry.path === composeOwner(transferBinding!, entry.path) ? "Stop syncing stack" : "Stop syncing file"}</Button>
                                <Button size="small" color="error" variant="outlined" startIcon={<DeleteOutlined/>} disabled={busy !== null} onClick={() => void runLocalDeletionAction(entry, "delete_git")}>Delete from Git</Button>
                            </Stack> : orphanCompose ? <Stack direction="row" spacing={.5} sx={{alignItems: "center", flexWrap: "wrap"}}>
                                <Button size="small" color="success" variant="outlined" startIcon={<RestoreOutlined/>} disabled={busy !== null} onClick={() => void runOrphanAction(entry.path, "restore")}>Restore to Git</Button>
                                {!rootOrphan && <Button size="small" color="warning" variant="outlined" startIcon={<ArchiveOutlined/>} disabled={busy !== null} onClick={() => { setOrphanConfirmation(""); setOrphanDecision({composePath: entry.path, action: "archive"}); }}>Archive local</Button>}
                                {!rootOrphan && <Button size="small" color="error" variant="outlined" startIcon={<DeleteOutlined/>} disabled={busy !== null} onClick={() => { setOrphanConfirmation(""); setOrphanDecision({composePath: entry.path, action: "delete"}); }}>Delete local</Button>}
                                {rootOrphan && <Typography variant="caption" color="text.secondary">Local removal is unavailable at the folder-link root.</Typography>}
                            </Stack> : deletedConflict ? <Typography variant="caption" color="warning.main">Handled by the orphaned stack decision.</Typography> : entry.status === "conflict" && (conflictResolutionMode ? <Stack direction="row" spacing={.5} sx={{alignItems: "center"}}><Button size="small" variant="outlined" startIcon={<CompareArrowsOutlined/>} disabled={busy !== null} onClick={() => void compareConflict(entry)}>Compare</Button><Button size="small" color="warning" variant={conflictDecisions[entry.path] === "git" ? "contained" : "outlined"} onClick={() => decideConflict(entry.path, "git")}>Keep Git</Button><Button size="small" color="primary" variant={conflictDecisions[entry.path] === "dockman" ? "contained" : "outlined"} onClick={() => decideConflict(entry.path, "dockman")}>Keep Dockman</Button></Stack> : <Stack direction="row" spacing={.5} sx={{alignItems: "center"}}><Button size="small" variant="outlined" startIcon={<CompareArrowsOutlined/>} disabled={busy !== null} onClick={() => void compareConflict(entry)}>Compare</Button>{resolvedConflictPaths.has(entry.path) ? <Button size="small" color="warning" startIcon={<UndoOutlined/>} onClick={() => leaveConflictPending(entry.path)}>Pending</Button> : <Button size="small" color="error" variant="contained" onClick={() => keepCurrentSource(entry.path)}>{transferDirection === "stack_to_repository" ? "Keep Dockman" : "Keep Git"}</Button>}</Stack>)}</TableCell>
                            <TableCell align="right"><Tooltip title="Add a permanent exclusion"><span><IconButton size="small" disabled={busy !== null || entry.status === "skipped_excluded" || entry.status === "conflict" || entry.status === "deleted_on_git" || entry.status === "deleted_locally"} onClick={(event) => setExcludeMenu({anchor: event.currentTarget, entry})}><BlockOutlined fontSize="small"/></IconButton></span></Tooltip></TableCell>
                        </TableRow>;
                    })}
                </TableBody></Table></TableContainer>
                <Stack direction={{xs: "column", md: "row"}} sx={{alignItems: {md: "center"}, border: 1, borderColor: "divider", borderTop: 0, borderRadius: "0 0 4px 4px"}}>
                    <TablePagination component="div" count={filteredPreviewEntries.length} page={previewPage} onPageChange={(_, page) => changePreviewPage(page)} rowsPerPage={previewRowsPerPage} onRowsPerPageChange={(event) => { setPreviewRowsPerPage(Number(event.target.value)); changePreviewPage(0); }} rowsPerPageOptions={[25, 50, 100]} labelRowsPerPage="Rows" showFirstButton showLastButton sx={{flex: 1, border: 0}}/>
                    <Stack direction="row" spacing={1} sx={{alignItems: "center", px: 2, pb: {xs: 1.5, md: 0}}}>
                        <Typography variant="body2" color="text.secondary">Page</Typography>
                        <TextField size="small" value={previewPageInput} onChange={(event) => setPreviewPageInput(event.target.value.replace(/\D/g, ""))} onBlur={() => changePreviewPage(Number(previewPageInput || 1) - 1)} onKeyDown={(event) => { if (event.key === "Enter") changePreviewPage(Number(previewPageInput || 1) - 1); }} slotProps={{htmlInput: {inputMode: "numeric", min: 1, max: previewPageCount, "aria-label": "Go to page"}}} sx={{width: 76}}/>
                        <Typography variant="body2" color="text.secondary">/ {previewPageCount}</Typography>
                    </Stack>
                </Stack>
                <Menu open={excludeMenu !== null} anchorEl={excludeMenu?.anchor || null} onClose={() => setExcludeMenu(null)}>
                    {excludeMenu && !excludeMenu.entry.directory && <MenuItem onClick={() => void addPreviewExclusions([{path: excludeMenu.entry.path, directory: false}])}><BlockOutlined fontSize="small" sx={{mr: 1.25}}/>Exclude this file</MenuItem>}
                    {excludeMenu && (() => { const path = excludeMenu.entry.directory ? excludeMenu.entry.path : excludeMenu.entry.path.slice(0, excludeMenu.entry.path.lastIndexOf("/")); return path ? <MenuItem onClick={() => void addPreviewExclusions([{path, directory: true}])}><FolderOffOutlined fontSize="small" sx={{mr: 1.25}}/>Exclude folder <code style={{marginLeft: 6}}>{path}</code></MenuItem> : null; })()}
                </Menu>
                {!!transferPreview?.conflicts && (conflictResolutionMode ? <Alert severity={Object.keys(conflictDecisions).length ? "warning" : "info"}>{Object.keys(conflictDecisions).length} decision{Object.keys(conflictDecisions).length === 1 ? "" : "s"} ready; {transferPreview.conflicts - Object.keys(conflictDecisions).length} conflict{transferPreview.conflicts - Object.keys(conflictDecisions).length === 1 ? "" : "s"} left pending.</Alert> : <Alert severity={resolvedConflictPaths.size ? "warning" : "info"}>{resolvedConflictPaths.size} conflict{resolvedConflictPaths.size === 1 ? "" : "s"} approved in this direction; {unresolvedConflictCount} left pending. {selectedTransferPaths.size === 0 && "Non-conflicting changes are still transferred."}</Alert>)}
                {selectedTransferPaths.size > 0 && <Alert severity="info">This direction was opened from a per-file decision. Only <code>{[...selectedTransferPaths][0]}</code> will be transferred; every other change remains pending.</Alert>}
                {!conflictResolutionMode && <FormControlLabel control={<Switch checked={includeSensitive} onChange={(event) => {
                    const checked = event.target.checked;
                    setIncludeSensitive(checked); setSensitiveConfirmation("");
                    if (!checked && transferBinding) void previewTransfer(transferBinding, transferDirection, false);
                }}/>} label="Include sensitive files for this transfer only"/>}
                {!conflictResolutionMode && includeSensitive && <><Alert severity="error">This may commit tokens, private keys, or .env secrets. It is disabled by default and never remembered.</Alert><TextField label='Type "INCLUDE SENSITIVE FILES"' value={sensitiveConfirmation} onChange={(event) => setSensitiveConfirmation(event.target.value)} onBlur={() => transferBinding && sensitiveConfirmation === "INCLUDE SENSITIVE FILES" && void previewTransfer(transferBinding, transferDirection, true)} fullWidth/></>}
                {!conflictResolutionMode && transferDirection === "stack_to_repository" && <TextField inputRef={commitMessageRef} label="Commit message (optional)" defaultValue="" placeholder={`chore(stack): sync ${transferBinding?.stackPath || "stack"} from Dockman`} slotProps={{htmlInput: {maxLength: 300}}}/>}
            </Stack></DialogContent>
            <DialogActions><Button onClick={closeTransfer} disabled={busy !== null}>Cancel</Button>{conflictResolutionMode ? <Button variant="contained" disabled={busy !== null || Object.keys(conflictDecisions).length === 0} onClick={() => void resolveAutomationConflicts()}>{busy?.startsWith("transfer-") && <CircularProgress size={16} sx={{mr: 1}}/>}Resolve selected decisions</Button> : <Button variant="contained" color={transferDirection === "repository_to_stack" ? "warning" : "primary"} disabled={busy !== null || !transferPreview || (transferPreview.changed > 0 && safeTransferCount === 0 && resolvedConflictPaths.size === 0) || (includeSensitive && sensitiveConfirmation !== "INCLUDE SENSITIVE FILES")} onClick={() => void runTransfer()}>{busy?.startsWith("transfer-") && <CircularProgress size={16} sx={{mr: 1}}/>}{transferPreview?.changed === 0 ? "Confirm baseline" : transferDirection === "stack_to_repository" ? "Commit selected and push" : "Backup and import selected"}</Button>}</DialogActions>
        </Dialog>

        <Dialog open={localDeletionDecision !== null} onClose={() => busy === null && setLocalDeletionDecision(null)} maxWidth="sm" fullWidth>
            <DialogTitle>Confirm Git deletion?</DialogTitle><DialogContent><Stack spacing={2} sx={{pt: .5}}>
                <Alert severity="error">This creates and pushes a Git commit deleting {localDeletionDecision?.wholeStack ? "the complete stack" : <strong>{localDeletionDecision?.path}</strong>}. Docker containers and volumes are never removed.</Alert>
                <Typography>Type <strong>{localDeletionDecision?.wholeStack ? "DELETE STACK FROM GIT" : "DELETE FILE FROM GIT"}</strong> to confirm.</Typography>
                <TextField value={localDeletionConfirmation} onChange={(event) => setLocalDeletionConfirmation(event.target.value)} autoComplete="off" autoFocus/>
            </Stack></DialogContent><DialogActions><Button onClick={() => { setLocalDeletionDecision(null); setLocalDeletionConfirmation(""); }} disabled={busy !== null}>Cancel</Button><Button color="error" variant="contained" disabled={busy !== null || localDeletionConfirmation !== (localDeletionDecision?.wholeStack ? "DELETE STACK FROM GIT" : "DELETE FILE FROM GIT")} onClick={() => {
                if (!localDeletionDecision) return;
                void runLocalDeletionAction({path: localDeletionDecision.path || localDeletionDecision.composePath, status: "deleted_locally"}, "delete_git");
            }}>Commit deletion and push</Button></DialogActions>
        </Dialog>

        <Dialog open={orphanDecision !== null} onClose={() => busy === null && setOrphanDecision(null)} fullWidth maxWidth="sm">
            <DialogTitle sx={{display: "flex", alignItems: "center", gap: 1}}>{orphanDecision?.action === "archive" ? <ArchiveOutlined/> : <DeleteOutlined/>}{orphanDecision?.action === "archive" ? "Archive local orphan" : "Delete local orphan"}</DialogTitle>
            <DialogContent dividers><Stack spacing={2}>
                <Alert severity="error">Git no longer contains this complete stack directory. Dockman will create a backup first, then remove only its local folder. It will not run Compose down and will never remove Docker volumes.</Alert>
                <Typography sx={{fontFamily: "monospace", overflowWrap: "anywhere"}}>{orphanDecision?.composePath}</Typography>
                <TextField autoFocus fullWidth label='Type "REMOVE LOCAL ORPHAN"' value={orphanConfirmation} onChange={(event) => setOrphanConfirmation(event.target.value)} slotProps={{htmlInput: {autoComplete: "off"}}}/>
                {orphanDecision?.action === "archive" && <Alert severity="info">The archive is kept separately from rotating synchronization backups.</Alert>}
            </Stack></DialogContent>
            <DialogActions><Button disabled={busy !== null} onClick={() => { setOrphanDecision(null); setOrphanConfirmation(""); }}>Cancel</Button><Button variant="contained" color={orphanDecision?.action === "archive" ? "warning" : "error"} disabled={busy !== null || orphanConfirmation !== "REMOVE LOCAL ORPHAN"} onClick={() => orphanDecision && void runOrphanAction(orphanDecision.composePath, orphanDecision.action)}>{busy?.startsWith("orphan-") && <CircularProgress size={16} sx={{mr: 1}}/>}{orphanDecision?.action === "archive" ? "Archive and remove" : "Backup and delete"}</Button></DialogActions>
        </Dialog>

        <Dialog open={comparison !== null} onClose={() => busy === null && setComparison(null)} fullWidth maxWidth="lg">
            <DialogTitle sx={{display: "flex", alignItems: "center", gap: 1}}><CompareArrowsOutlined/>Compare conflict — <Box component="span" sx={{fontFamily: "monospace", fontSize: ".9em", overflowWrap: "anywhere"}}>{comparison?.path}</Box></DialogTitle>
            <DialogContent dividers sx={{p: 0}}>
                {comparison?.comparable ? <>
                    <Stack direction="row" sx={{px: 2, py: 1, bgcolor: "background.paper", borderBottom: 1, borderColor: "divider"}}><Typography variant="body2" sx={{width: "50%", fontWeight: 700}}>Dockman · {formatBytes(comparison.dockman.size)} · {comparison.dockman.sha256.slice(0, 12)}</Typography><Typography variant="body2" sx={{width: "50%", fontWeight: 700}}>Git · {formatBytes(comparison.git.size)} · {comparison.git.sha256.slice(0, 12)}</Typography></Stack>
                    <DiffEditor height="52vh" theme="vs-dark" original={comparison.dockman.content || ""} modified={comparison.git.content || ""} language={comparisonLanguage(comparison.path)} options={{readOnly: true, renderSideBySide: true, minimap: {enabled: false}, wordWrap: "on", originalEditable: false, automaticLayout: true}}/>
                </> : <Stack spacing={2} sx={{p: 3}}><Alert severity="warning">{comparison?.reason || "This file cannot be displayed as a text comparison."}</Alert><Typography>Dockman: {comparison && formatBytes(comparison.dockman.size)} · <code>{comparison?.dockman.sha256}</code></Typography><Typography>Git: {comparison && formatBytes(comparison.git.size)} · <code>{comparison?.git.sha256}</code></Typography></Stack>}
            </DialogContent>
            <DialogActions sx={{justifyContent: "space-between"}}><Button onClick={() => setComparison(null)}>Leave pending</Button><Stack direction="row" spacing={1}><Button color="warning" variant="outlined" onClick={() => comparison && (conflictResolutionMode ? decideConflict(comparison.path, "git") : transferDirection === "stack_to_repository" ? keepCurrentTarget(comparison.path) : keepCurrentSource(comparison.path))}>Keep Git</Button><Button color="primary" variant="contained" onClick={() => comparison && (conflictResolutionMode ? decideConflict(comparison.path, "dockman") : transferDirection === "stack_to_repository" ? keepCurrentSource(comparison.path) : keepCurrentTarget(comparison.path))}>Keep Dockman</Button></Stack></DialogActions>
        </Dialog>

        <Dialog open={composeBinding !== null} onClose={() => busy === null && setComposeBinding(null)} fullWidth maxWidth="md">
            <DialogTitle sx={{display: "flex", alignItems: "center", gap: 1}}><FolderOpenOutlined/>Synchronized stacks — {composeBinding?.stackPath}</DialogTitle>
            <DialogContent dividers><Stack spacing={1.5}>
                <Alert severity="info">Only selected stack folders are synchronized in either direction. Newly discovered local stacks appear unselected until you explicitly approve them. Removing a stack here never deletes its files and never stops it.</Alert>
                <ComposePathSelector key={composeBinding?.id || "compose-selection"} paths={composeBinding?.composePaths || []} selectedPaths={selectedComposePaths} onChange={setSelectedComposePaths} selectedLabel="synchronized" unselectedLabel="excluded" maxHeight="48vh" pathStates={composePathStates}/>
                {selectedComposePaths.size === 0 && <Alert severity="warning">No stack will be synchronized by this folder link. The link and its baseline are preserved.</Alert>}
            </Stack></DialogContent>
            <DialogActions><Button onClick={() => setComposeBinding(null)} disabled={busy !== null}>Cancel</Button><Button variant="contained" onClick={() => void saveComposeSelection()} disabled={busy !== null}>{busy?.startsWith("binding-compose-") && <CircularProgress size={16} sx={{mr: 1}}/>}Save selection</Button></DialogActions>
        </Dialog>

        <Dialog open={policyBinding !== null} onClose={() => busy === null && setPolicyBinding(null)} fullWidth maxWidth="sm">
            <DialogTitle sx={{display: "flex", alignItems: "center", gap: 1}}><TuneOutlined/>Synchronization policy</DialogTitle>
            <DialogContent dividers><Stack spacing={2} sx={{pt: .5}}>
                <Alert severity="info">The policy applies in both directions. Compose files stay protected. Special files, Git metadata and files over 100 MiB are always excluded.</Alert>
                <FormControl><InputLabel>Base profile</InputLabel><Select label="Base profile" value={policyForm.profile} onChange={(event) => setPolicyForm({...policyForm, profile: event.target.value as "compose_config" | "all_files"})}>
                    <MenuItem value="compose_config">Compose configuration — recommended</MenuItem>
                    <MenuItem value="all_files">All regular files</MenuItem>
                </Select></FormControl>
                <TextField label="Additional include rules" value={policyForm.includes} onChange={(event) => setPolicyForm({...policyForm, includes: event.target.value})} multiline minRows={4} maxRows={10} placeholder={"scripts/**\ncustom-file\n*.py"} helperText="One relative glob per line. These rules extend the selected profile." sx={{"& textarea": {fontFamily: "monospace"}}}/>
                <TextField label="Exclude rules" value={policyForm.excludes} onChange={(event) => setPolicyForm({...policyForm, excludes: event.target.value})} multiline minRows={4} maxRows={10} placeholder={"**/data/**\n**/cache/**\n*.log"} helperText="One relative glob per line. Exclusions always take priority." sx={{"& textarea": {fontFamily: "monospace"}}}/>
                <Typography variant="caption" color="text.secondary">The configuration profile includes Compose/YAML, JSON, TOML, INI, CONF, templates, shell scripts, SQL, documentation, Dockerfile, Containerfile, Caddyfile and environment files. Sensitive files remain protected separately.</Typography>
            </Stack></DialogContent>
            <DialogActions><Button onClick={() => setPolicyBinding(null)} disabled={busy !== null}>Cancel</Button><Button variant="contained" onClick={() => void saveBindingPolicy()} disabled={busy !== null}>{busy?.startsWith("binding-policy-") && <CircularProgress size={16} sx={{mr: 1}}/>}Save policy</Button></DialogActions>
        </Dialog>

        <Dialog open={automationBinding !== null} onClose={() => busy === null && setAutomationBinding(null)} fullWidth maxWidth="md">
            <DialogTitle sx={{display: "flex", alignItems: "center", gap: 1}}><SyncOutlined/>Automatic Git monitoring</DialogTitle>
            <DialogContent dividers><Stack spacing={2} sx={{pt: .5}}>
                <FormControlLabel control={<Switch checked={automationForm.enabled} onChange={(event) => setAutomationForm({...automationForm, enabled: event.target.checked, ...(!event.target.checked ? {deployEnabled: false, deployNewStacks: false, deployRollback: false, deployComposePaths: []} : {})})}/>} label="Synchronize changes from Git automatically"/>
                <FormControlLabel control={<Switch checked={automationForm.autoReconcile} onChange={(event) => setAutomationForm({...automationForm, autoReconcile: event.target.checked})}/>} label="Automatically establish a baseline when both sides are identical"/>
                <TextField label="Check interval (minutes)" type="number" value={automationForm.intervalMinutes} onChange={(event) => setAutomationForm({...automationForm, intervalMinutes: Number(event.target.value)})} disabled={!automationForm.enabled} slotProps={{htmlInput: {min: 5, max: 1440, step: 5}}} helperText="Between 5 minutes and 24 hours."/>
                <Alert severity="info">Dockman fetches Git and fast-forwards the managed repository. Identical files are reconciled automatically, safe Git additions are imported directly with a backup, and a true same-file divergence remains blocked as a conflict. Missing source files are not deleted.</Alert>
                <FormControlLabel control={<Switch checked={automationForm.deployEnabled} disabled={!automationForm.enabled} onChange={(event) => setAutomationForm({...automationForm, deployEnabled: event.target.checked, ...(!event.target.checked ? {deployNewStacks: false, deployRollback: false} : {})})}/>} label="Deploy affected stacks after a successful import"/>
                {automationForm.deployEnabled && <FormControlLabel control={<Switch checked={automationForm.deployNewStacks} onChange={(event) => setAutomationForm({...automationForm, deployNewStacks: event.target.checked})}/>} label="Automatically deploy newly discovered Git stacks"/>}
                {automationForm.deployEnabled && <FormControlLabel control={<Switch checked={automationForm.deployRollback} onChange={(event) => setAutomationForm({...automationForm, deployRollback: event.target.checked})}/>} label="Restore the previous stack automatically when deployment or health checks fail"/>}
                {automationForm.deployEnabled && automationForm.deployRollback && <Alert severity="info">Dockman waits up to 60 seconds for Compose services to become running/healthy. On failure, only that stack is restored from its pre-import backup; its previous configuration is validated and, if Docker was already touched, redeployed and checked again.</Alert>}
                {automationForm.deployEnabled && automationForm.deployNewStacks && <Alert severity="warning">A newly added compose.yml/docker-compose.yml inside this folder link will be validated, dry-run, deployed, then retained as an authorized target. At most 10 new stack folders are accepted per synchronization.</Alert>}
                {automationForm.deployEnabled && <Box><Typography variant="subtitle2" sx={{mb: 1}}>Compose deployment targets</Typography><ComposePathSelector key={`${automationBinding?.id || "automation"}-deploy`} paths={automationBinding?.selectedComposePaths || []} selectedPaths={automationDeploySelection} onChange={(next) => setAutomationForm({...automationForm, deployComposePaths: (automationBinding?.selectedComposePaths || []).filter((path) => next.has(path))})} selectedLabel="authorized" unselectedLabel="not authorized" maxHeight="34vh"/></Box>}
                <Alert severity="warning">Deployment and rollback are disabled by default. Dockman validates the Compose configuration and performs a dry-run before touching Docker. Automatic rollback never affects another stack and refuses to overwrite a file changed after import.</Alert>
                {automationBinding?.lastAutoSyncSuccessAt && <Typography variant="body2" color="text.secondary">Last successful synchronization: {dateLabel(automationBinding.lastAutoSyncSuccessAt)}</Typography>}
                {automationBinding?.autoSyncError && <Alert severity="error" sx={{whiteSpace: "pre-wrap", overflowWrap: "anywhere", userSelect: "text"}}>{automationBinding.autoSyncError}</Alert>}
                {deployments.length > 0 && <Box><Typography variant="subtitle2" sx={{mb: 1}}>Recent controlled deployments</Typography><Stack spacing={1}>{deployments.map((deployment) => { const rolledBack = deployment.state === "rolled_back"; const failed = deployment.state === "failed" || deployment.state === "rollback_failed"; return <Paper key={deployment.id} variant="outlined" sx={{p: 1.25}}><Stack direction="row" sx={{justifyContent: "space-between", gap: 1}}><Typography variant="body2" sx={{fontFamily: "monospace"}}>{deployment.composePath}</Typography><Chip size="small" color={deployment.state === "success" ? "success" : rolledBack ? "warning" : "error"} label={deployment.state.replaceAll("_", " ")}/></Stack><Typography variant="caption" color="text.secondary">{dateLabel(deployment.createdAt)} · {deployment.commitSha.slice(0, 12)}</Typography>{deployment.result && deployment.state !== "success" && <Alert severity={rolledBack ? "warning" : "error"} sx={{mt: 1, whiteSpace: "pre-wrap", overflowWrap: "anywhere", userSelect: "text"}}>{deployment.result}</Alert>}{failed && deployment.state === "rollback_failed" && <Alert severity="error" sx={{mt: 1}}>The imported version and its automatic rollback both failed. Use Backups or Commits for explicit recovery before another deployment.</Alert>}{deployment.logs && <Box component="pre" sx={{mt: 1, mb: 0, p: 1, maxHeight: 140, overflow: "auto", bgcolor: "#050607", color: "grey.300", fontSize: 11, whiteSpace: "pre-wrap", userSelect: "text"}}>{deployment.logs}</Box>}</Paper>;})}</Stack></Box>}
            </Stack></DialogContent>
            <DialogActions><Button onClick={() => setAutomationBinding(null)} disabled={busy !== null}>Cancel</Button><Button variant="contained" onClick={() => void saveBindingAutomation()} disabled={busy !== null || (automationForm.enabled && (automationForm.intervalMinutes < 5 || automationForm.intervalMinutes > 1440)) || (automationForm.deployEnabled && !automationForm.deployNewStacks && automationForm.deployComposePaths.length === 0)}>{busy?.startsWith("binding-automation-") && <CircularProgress size={16} sx={{mr: 1}}/>}Save</Button></DialogActions>
        </Dialog>

        <Dialog open={deleteBinding !== null} onClose={() => busy === null && setDeleteBinding(null)} maxWidth="xs" fullWidth>
            <DialogTitle>Remove stack link?</DialogTitle><DialogContent><Stack spacing={2}><Typography>Unlink <strong>{deleteBinding?.stackPath}</strong> from <strong>{deleteBinding?.repositoryName}</strong>? No stack or repository file will be deleted.</Typography><Alert severity="info"><strong>Unlink</strong> preserves the SHA synchronization baseline. Recreating the exact same link restores unresolved conflicts.</Alert><Alert severity="warning"><strong>Unlink and forget</strong> permanently removes that baseline. Different files will then be reported as initial conflicts.</Alert></Stack></DialogContent><DialogActions sx={{flexWrap: "wrap"}}><Button onClick={() => setDeleteBinding(null)} disabled={busy !== null}>Cancel</Button><Button color="error" onClick={() => void confirmDeleteBinding(true)} disabled={busy !== null}>Unlink and forget</Button><Button variant="contained" onClick={() => void confirmDeleteBinding(false)} disabled={busy !== null}>Unlink</Button></DialogActions>
        </Dialog>

        <Dialog open={repositoryDialogOpen} onClose={() => busy === null && setRepositoryDialogOpen(false)} fullWidth maxWidth="sm">
            <DialogTitle>Add Git repository</DialogTitle>
            <DialogContent dividers><Stack spacing={2} sx={{pt: .5}}>
                <FormControl><InputLabel>Source</InputLabel><Select label="Source" value={repositoryForm.mode} onChange={(event) => setRepositoryForm({...emptyRepository, mode: event.target.value as RepositoryDialogMode})}>
                    <MenuItem value="import">Import an existing repository</MenuItem><MenuItem value="github">Create a new GitHub repository</MenuItem>
                </Select></FormControl>
                <TextField label="Dockman repository name" value={repositoryForm.name} onChange={(event) => setRepositoryForm({...repositoryForm, name: event.target.value})} required autoFocus helperText="A local identifier; for GitHub creation this is also the remote repository name."/>
                {repositoryForm.mode === "import" ? <TextField label="GitHub repository" value={repositoryForm.remoteUrl} onChange={(event) => setRepositoryForm({...repositoryForm, remoteUrl: event.target.value})} placeholder="owner/repository or https://github.com/owner/repository" helperText="The optional .git suffix is added automatically." required/> : <>
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

        <Dialog open={missingBranch !== null} onClose={() => busy === null && setMissingBranch(null)} fullWidth maxWidth="xs">
            <DialogTitle>Create missing branch?</DialogTitle>
            <DialogContent><Stack spacing={2}>
                <Alert severity="warning">The branch <strong>{missingBranch?.branch}</strong> does not exist in the remote repository.</Alert>
                {missingBranch?.canCreateFromDefault && <Typography>Dockman can create it from the current remote default branch <strong>{missingBranch.sourceBranch}</strong>, then import it.</Typography>}
                <Alert severity="info"><strong>Independent empty branch:</strong> creates a separate root commit containing no files. It shares no history with the default branch, which is ideal for a dedicated Dockman synchronization branch but unsuitable for a normal pull request back to the default branch.</Alert>
                <Typography variant="body2" color="text.secondary">Both choices write one new branch to the remote repository. Existing branches and files are never changed.</Typography>
            </Stack></DialogContent>
            <DialogActions sx={{flexWrap: "wrap"}}><Button onClick={() => setMissingBranch(null)} disabled={busy !== null}>Cancel</Button>{missingBranch?.canCreateEmpty && <Button variant="outlined" onClick={() => { setMissingBranch(null); void saveRepository("empty"); }} disabled={busy !== null}>Create empty branch</Button>}{missingBranch?.canCreateFromDefault && <Button variant="contained" onClick={() => { setMissingBranch(null); void saveRepository("from_default"); }} disabled={busy !== null}>{busy === "repository-save" && <CircularProgress size={16} sx={{mr: 1}}/>}Create from {missingBranch.sourceBranch}</Button>}</DialogActions>
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
