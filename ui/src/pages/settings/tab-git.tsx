import {useCallback, useDeferredValue, useEffect, useMemo, useRef, useState} from "react";
import {DiffEditor} from "@monaco-editor/react";
import {
    Alert, Box, Button, Checkbox, Chip, CircularProgress, Dialog, DialogActions, DialogContent,
    DialogTitle, FormControl, FormControlLabel, IconButton, InputLabel, Menu, MenuItem, Paper,
    Select, Stack, Switch, Table, TableBody, TableCell, TableContainer, TableHead, TableRow,
    TablePagination, TextField, Tooltip, Typography,
} from "@mui/material";
import {
    Add, BlockOutlined, CheckCircleOutlined, CloudDownloadOutlined, CloudUploadOutlined, CompareArrowsOutlined, DeleteOutlined, EditOutlined,
    FolderOffOutlined, FolderOpenOutlined, HistoryOutlined, KeyOutlined, LinkOutlined, RefreshOutlined, SearchOutlined, SyncOutlined, TuneOutlined, UndoOutlined,
} from "@mui/icons-material";
import {withProtectedAPI} from "../../lib/api.ts";
import {formatBytes} from "../../lib/editor.ts";
import {useSnackbar} from "../../hooks/snackbar.ts";
import {useCopyButton} from "../../hooks/copy.ts";
import CopyButton from "../../components/copy-button.tsx";

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

interface StackTarget { host: string; path: string; composePaths: string[]; scope: "all_stacks" | "folder"; stackCount: number; }
interface Binding {
    id: string; repositoryId: string; repositoryName: string; host: string; stackPath: string;
    subPath: string; composePaths: string[]; syncProfile: "compose_config" | "all_files";
    includePatterns: string[]; excludePatterns: string[]; enabled: boolean;
    autoSyncEnabled: boolean; autoSyncIntervalMinutes: number; autoSyncState: string;
    autoSyncError?: string; lastAutoSyncAt?: string; lastAutoSyncSuccessAt?: string;
}
interface AutoSyncResult { bindingId: string; state: string; changed: number; conflicts: number; backup?: string; message: string; }
interface PreviewEntry {
    path: string; status: "add" | "modify" | "conflict" | "skipped_sensitive" | "skipped_oversized" | "skipped_type" | "skipped_excluded" | "skipped_unavailable"; sourceSha?: string;
    targetSha?: string; size?: number; sensitive?: boolean; directory?: boolean; conflictKind?: "no_baseline" | "destination_changed";
}
interface TransferPreview {
    bindingId: string; direction: TransferDirection; entries: PreviewEntry[]; changed: number;
    unchanged: number; skipped: number; conflicts: number; deletionMode: string;
    previewToken: string;
}
interface TransferResult { preview: TransferPreview; commitSha?: string; backup?: string; message: string; }
interface ComparisonSide { sha256: string; size: number; content?: string; }
interface FileComparison { path: string; dockman: ComparisonSide; git: ComparisonSide; comparable: boolean; reason?: string; }
type TransferDirection = "stack_to_repository" | "repository_to_stack";
type PreviewStatus = PreviewEntry["status"];

const previewStatuses: PreviewStatus[] = ["conflict", "add", "modify", "skipped_type", "skipped_excluded", "skipped_sensitive", "skipped_oversized", "skipped_unavailable"];

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
    const [bindingForm, setBindingForm] = useState({repositoryId: "", host: "", stackPath: "", subPath: "stacks", targetMode: "repository_folder" as "repository_folder" | "repository_root"});
    const [transferBinding, setTransferBinding] = useState<Binding | null>(null);
    const [transferDirection, setTransferDirection] = useState<TransferDirection>("stack_to_repository");
    const [transferPreview, setTransferPreview] = useState<TransferPreview | null>(null);
    const [includeSensitive, setIncludeSensitive] = useState(false);
    const [sensitiveConfirmation, setSensitiveConfirmation] = useState("");
    const [resolvedConflictPaths, setResolvedConflictPaths] = useState<Set<string>>(new Set());
    const [selectedTransferPaths, setSelectedTransferPaths] = useState<Set<string>>(new Set());
    const [comparison, setComparison] = useState<FileComparison | null>(null);
    const commitMessageRef = useRef<HTMLInputElement | null>(null);
    const [deleteBinding, setDeleteBinding] = useState<Binding | null>(null);
    const [policyBinding, setPolicyBinding] = useState<Binding | null>(null);
    const [policyForm, setPolicyForm] = useState({profile: "compose_config" as "compose_config" | "all_files", includes: "", excludes: ""});
    const [automationBinding, setAutomationBinding] = useState<Binding | null>(null);
    const [automationForm, setAutomationForm] = useState({enabled: false, intervalMinutes: 15});
    const [excludeMenu, setExcludeMenu] = useState<{anchor: HTMLElement; entry: PreviewEntry} | null>(null);
    const [previewPage, setPreviewPage] = useState(0);
    const [previewRowsPerPage, setPreviewRowsPerPage] = useState(50);
    const [previewSearch, setPreviewSearch] = useState("");
    const [previewStatus, setPreviewStatus] = useState<"all" | PreviewStatus>("all");
    const [previewPageInput, setPreviewPageInput] = useState("1");
    const [selectedPreviewPaths, setSelectedPreviewPaths] = useState<Set<string>>(() => new Set());
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
    const selectablePreviewEntries = visiblePreviewEntries.filter((entry) => entry.status !== "skipped_excluded" && entry.status !== "conflict");
    const selectedVisibleCount = selectablePreviewEntries.filter((entry) => selectedPreviewPaths.has(entry.path)).length;
    const allowableSelectedEntries = visiblePreviewEntries.filter((entry) => selectedPreviewPaths.has(entry.path) && entry.status === "skipped_type");
    const safeTransferCount = Math.max(0, (transferPreview?.changed || 0) - (transferPreview?.conflicts || 0));
    const unresolvedConflictCount = Math.max(0, (transferPreview?.conflicts || 0) - resolvedConflictPaths.size);

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

    useEffect(() => {
        if (!feature?.enabled) return;
        const timer = window.setInterval(() => {
            if (document.visibilityState !== "visible" || busy !== null) return;
            void api<Binding[]>("/bindings").then(setBindings).catch(() => undefined);
        }, 30_000);
        return () => window.clearInterval(timer);
    }, [busy, feature?.enabled]);

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
        setBindingForm({repositoryId: repositories[0]?.id || "", host: first?.host || "", stackPath: first?.path || "", subPath: "stacks", targetMode: "repository_folder"});
        setBindingDialogOpen(true);
    };

    const saveBinding = async () => {
        setBusy("binding-save");
        try {
            await api<Binding>("/bindings", {method: "POST", body: JSON.stringify({repositoryId: bindingForm.repositoryId, host: bindingForm.host, stackPath: bindingForm.stackPath, subPath: bindingForm.subPath})});
            showSuccess("Complete folder linked to the Git repository.");
            setBindingDialogOpen(false);
            await load();
        } catch (error) {
            showError((error as Error).message);
        } finally { setBusy(null); }
    };

    const previewTransfer = async (binding: Binding, direction: TransferDirection, sensitive = false, resolvedPath?: string, selectedPath?: string) => {
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
            setPreviewSearch(""); setPreviewStatus("all"); setPreviewPageInput("1"); setSelectedPreviewPaths(new Set());
            setResolvedConflictPaths(resolvedPath && preview.entries.some((entry) => entry.path === resolvedPath && entry.status === "conflict") ? new Set([resolvedPath]) : new Set());
            setSelectedTransferPaths(selectedPath && preview.entries.some((entry) => entry.path === selectedPath && ["add", "modify", "conflict"].includes(entry.status)) ? new Set([selectedPath]) : new Set());
        } catch (error) { showError((error as Error).message); }
        finally { setBusy(null); }
    };

    const closeTransfer = () => {
        setTransferBinding(null); setTransferPreview(null); setIncludeSensitive(false);
        setSensitiveConfirmation(""); setResolvedConflictPaths(new Set()); setSelectedTransferPaths(new Set()); setComparison(null); setExcludeMenu(null); setPreviewPage(0); setPreviewSearch(""); setPreviewStatus("all");
        setPreviewPageInput("1"); setSelectedPreviewPaths(new Set());
        if (commitMessageRef.current) commitMessageRef.current.value = "";
    };

    const runTransfer = async () => {
        if (!transferBinding) return;
        const action = transferDirection === "stack_to_repository" ? "export" : "import";
        setBusy(`transfer-${transferBinding.id}`);
        try {
            const result = await api<TransferResult>(`/bindings/${transferBinding.id}/${action}`, {
                method: "POST", body: JSON.stringify({includeSensitive, sensitiveConfirmation, commitMessage: commitMessageRef.current?.value || "", previewToken: transferPreview?.previewToken, resolvedPaths: [...resolvedConflictPaths], selectedPaths: [...selectedTransferPaths]}),
            });
            showSuccess(result.message + (result.backup ? ` Backup: ${result.backup}` : ""));
            closeTransfer();
            await load();
        } catch (error) { showError((error as Error).message); }
        finally { setBusy(null); }
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

    const openBindingAutomation = (binding: Binding) => {
        setAutomationBinding(binding);
        setAutomationForm({enabled: binding.autoSyncEnabled, intervalMinutes: binding.autoSyncIntervalMinutes || 15});
    };

    const saveBindingAutomation = async () => {
        if (!automationBinding) return;
        setBusy(`binding-automation-${automationBinding.id}`);
        try {
            await api<Binding>(`/bindings/${automationBinding.id}/automation`, {
                method: "PUT", body: JSON.stringify(automationForm),
            });
            showSuccess(automationForm.enabled ? "Automatic Git monitoring enabled." : "Automatic Git monitoring disabled.");
            setAutomationBinding(null);
            await load();
        } catch (error) { showError((error as Error).message); }
        finally { setBusy(null); }
    };

    const runBindingAutomation = async (binding: Binding) => {
        setBusy(`binding-auto-run-${binding.id}`);
        try {
            const result = await api<AutoSyncResult>(`/bindings/${binding.id}/automation/run`, {method: "POST"});
            if (result.state === "conflict" || result.state === "blocked") showError(result.message);
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
                            <TableCell><Typography variant="body2">{binding.repositoryName}</Typography><Typography variant="caption" color="text.secondary" sx={{fontFamily: "monospace"}}>{binding.subPath === "." ? "/" : `/${binding.subPath}`}</Typography><Box sx={{mt: .5}}><Chip size="small" variant="outlined" color={binding.syncProfile === "all_files" ? "warning" : "info"} label={binding.syncProfile === "all_files" ? "All regular files" : "Configuration files"}/></Box></TableCell>
                            <TableCell>{binding.composePaths.length ? <Stack direction="row" spacing={.5} sx={{alignItems: "center"}}>{binding.composePaths.slice(0, 2).map((path) => <Chip key={path} size="small" variant="outlined" label={path}/>)}{binding.composePaths.length > 2 && <Chip size="small" color="info" variant="outlined" label={`+${binding.composePaths.length - 2}`}/>}</Stack> : <Chip size="small" color="warning" variant="outlined" label="Import target"/>}</TableCell>
                            <TableCell sx={{minWidth: 190}}>
                                <Stack direction="row" spacing={.5} sx={{alignItems: "center"}}>
                                    <Tooltip title={binding.autoSyncError || (binding.autoSyncEnabled ? `Every ${binding.autoSyncIntervalMinutes} minutes` : "Disabled by default")}><Chip size="small" variant="outlined" color={!binding.autoSyncEnabled ? "default" : binding.autoSyncState === "up_to_date" ? "success" : binding.autoSyncState === "conflict" || binding.autoSyncState === "error" ? "error" : binding.autoSyncState === "blocked" ? "warning" : "info"} label={!binding.autoSyncEnabled ? "off" : binding.autoSyncState.replaceAll("_", " ")}/></Tooltip>
                                    <Tooltip title="Configure automatic monitoring"><IconButton size="small" disabled={busy !== null} onClick={() => openBindingAutomation(binding)}><SyncOutlined fontSize="small"/></IconButton></Tooltip>
                                    {binding.autoSyncEnabled && <Tooltip title="Check and synchronize now"><span><IconButton size="small" disabled={busy !== null} onClick={() => void runBindingAutomation(binding)}>{busy === `binding-auto-run-${binding.id}` ? <CircularProgress size={17}/> : <RefreshOutlined fontSize="small"/>}</IconButton></span></Tooltip>}
                                </Stack>
                                {binding.lastAutoSyncAt && <Typography variant="caption" color="text.secondary">Checked {dateLabel(binding.lastAutoSyncAt)}</Typography>}
                            </TableCell>
                            <TableCell align="right" sx={{whiteSpace: "nowrap"}}>
                                <Tooltip title="Synchronization policy"><IconButton size="small" disabled={busy !== null} onClick={() => openBindingPolicy(binding)}><TuneOutlined fontSize="small"/></IconButton></Tooltip>
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
            <DialogTitle>Link a complete stacks folder to Git</DialogTitle>
            <DialogContent dividers><Stack spacing={2} sx={{pt: .5}}>
                <FormControl><InputLabel>Repository</InputLabel><Select label="Repository" value={bindingForm.repositoryId} onChange={(event) => setBindingForm({...bindingForm, repositoryId: event.target.value})}>
                    {repositories.map((repository) => <MenuItem key={repository.id} value={repository.id}>{repository.name}</MenuItem>)}
                </Select></FormControl>
                {stackTargets.length > 0 && <FormControl><InputLabel>Source folder</InputLabel><Select label="Source folder" value={stackTargets.some((target) => `${target.host}\n${target.path}` === `${bindingForm.host}\n${bindingForm.stackPath}`) ? `${bindingForm.host}\n${bindingForm.stackPath}` : ""} onChange={(event) => {
                    const target = stackTargets.find((item) => `${item.host}\n${item.path}` === event.target.value);
                    if (target) setBindingForm({...bindingForm, host: target.host, stackPath: target.path});
                }}><MenuItem value=""><em>Custom folder</em></MenuItem>{stackTargets.map((target) => <MenuItem key={`${target.host}-${target.path}`} value={`${target.host}\n${target.path}`}>{target.scope === "all_stacks" ? "All stacks" : "Folder"} — {target.host} / {target.path} ({target.stackCount} stack{target.stackCount === 1 ? "" : "s"})</MenuItem>)}</Select></FormControl>}
                <Stack direction={{xs: "column", sm: "row"}} spacing={2}><TextField fullWidth label="Host" value={bindingForm.host} onChange={(event) => setBindingForm({...bindingForm, host: event.target.value})} required/><TextField fullWidth label="Complete source folder" value={bindingForm.stackPath} onChange={(event) => setBindingForm({...bindingForm, stackPath: event.target.value})} placeholder="compose" required/></Stack>
                <FormControl><InputLabel>Git destination</InputLabel><Select label="Git destination" value={bindingForm.targetMode} onChange={(event) => {
                    const targetMode = event.target.value as "repository_folder" | "repository_root";
                    setBindingForm({...bindingForm, targetMode, subPath: targetMode === "repository_root" ? "." : (bindingForm.subPath === "." ? "stacks" : bindingForm.subPath)});
                }}><MenuItem value="repository_folder">A folder inside a shared repository</MenuItem><MenuItem value="repository_root">The root of a dedicated repository</MenuItem></Select></FormControl>
                {bindingForm.targetMode === "repository_folder" && <TextField label="Repository folder" value={bindingForm.subPath} onChange={(event) => setBindingForm({...bindingForm, subPath: event.target.value})} placeholder="stacks" helperText="Every stack subfolder is preserved below this destination." required/>}
                <Alert severity="info">All subfolders and compose stacks below this source are handled by one link. Creating it copies nothing; every transfer still requires a preview and confirmation.</Alert>
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
                    <Chip label={`${transferPreview?.skipped || 0} skipped`} variant="outlined" color={transferPreview?.skipped ? "warning" : "default"}/>
                    {!!transferPreview?.conflicts && <Chip label={`${transferPreview.conflicts} conflict${transferPreview.conflicts === 1 ? "" : "s"}`} color="error"/>}
                    <Typography variant="body2" color="text.secondary" sx={{ml: {sm: "auto!important"}}}>No source-side deletion is propagated.</Typography>
                </Stack>
                {!!transferPreview?.skipped && <Alert severity="warning">Skipped files are never copied. Files skipped only by type can be permanently allowed here. Oversized, unavailable, sensitive, and explicitly excluded files keep their dedicated protection.</Alert>}
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
                    {visiblePreviewEntries.map((entry) => <TableRow key={entry.path} selected={selectedPreviewPaths.has(entry.path)}><TableCell padding="checkbox"><Checkbox size="small" checked={selectedPreviewPaths.has(entry.path)} disabled={busy !== null || entry.status === "skipped_excluded" || entry.status === "conflict"} onChange={(_, checked) => togglePreviewEntry(entry.path, checked)} slotProps={{input: {"aria-label": `Select ${entry.path}`}}}/></TableCell><TableCell sx={{fontFamily: "monospace", overflowWrap: "anywhere"}}>{entry.path}</TableCell><TableCell><Chip size="small" variant="outlined" color={entry.status === "conflict" ? "error" : entry.status.startsWith("skipped_") ? "warning" : entry.status === "modify" ? "info" : "success"} label={entry.status === "conflict" && entry.conflictKind === "no_baseline" ? "initial conflict" : entry.status.replaceAll("_", " ")}/></TableCell><TableCell>{entry.size === undefined ? "—" : formatBytes(entry.size)}</TableCell><TableCell>{entry.status === "conflict" && <Stack direction="row" spacing={.5} sx={{alignItems: "center"}}><Button size="small" variant="outlined" startIcon={<CompareArrowsOutlined/>} disabled={busy !== null} onClick={() => void compareConflict(entry)}>Compare</Button>{resolvedConflictPaths.has(entry.path) ? <Button size="small" color="warning" startIcon={<UndoOutlined/>} onClick={() => leaveConflictPending(entry.path)}>Pending</Button> : <Button size="small" color="error" variant="contained" onClick={() => keepCurrentSource(entry.path)}>{transferDirection === "stack_to_repository" ? "Keep Dockman" : "Keep Git"}</Button>}</Stack>}</TableCell><TableCell align="right"><Tooltip title="Add a permanent exclusion"><span><IconButton size="small" disabled={busy !== null || entry.status === "skipped_excluded" || entry.status === "conflict"} onClick={(event) => setExcludeMenu({anchor: event.currentTarget, entry})}><BlockOutlined fontSize="small"/></IconButton></span></Tooltip></TableCell></TableRow>)}
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
                {!!transferPreview?.conflicts && <Alert severity={resolvedConflictPaths.size ? "warning" : "info"}>{resolvedConflictPaths.size} conflict{resolvedConflictPaths.size === 1 ? "" : "s"} approved in this direction; {unresolvedConflictCount} left pending. {selectedTransferPaths.size === 0 && "Non-conflicting changes are still transferred."}</Alert>}
                {selectedTransferPaths.size > 0 && <Alert severity="info">This direction was opened from a per-file decision. Only <code>{[...selectedTransferPaths][0]}</code> will be transferred; every other change remains pending.</Alert>}
                <FormControlLabel control={<Switch checked={includeSensitive} onChange={(event) => {
                    const checked = event.target.checked;
                    setIncludeSensitive(checked); setSensitiveConfirmation("");
                    if (!checked && transferBinding) void previewTransfer(transferBinding, transferDirection, false);
                }}/>} label="Include sensitive files for this transfer only"/>
                {includeSensitive && <><Alert severity="error">This may commit tokens, private keys, or .env secrets. It is disabled by default and never remembered.</Alert><TextField label='Type "INCLUDE SENSITIVE FILES"' value={sensitiveConfirmation} onChange={(event) => setSensitiveConfirmation(event.target.value)} onBlur={() => transferBinding && sensitiveConfirmation === "INCLUDE SENSITIVE FILES" && void previewTransfer(transferBinding, transferDirection, true)} fullWidth/></>}
                {transferDirection === "stack_to_repository" && <TextField inputRef={commitMessageRef} label="Commit message (optional)" defaultValue="" placeholder={`chore(stack): sync ${transferBinding?.stackPath || "stack"} from Dockman`} slotProps={{htmlInput: {maxLength: 300}}}/>} 
            </Stack></DialogContent>
            <DialogActions><Button onClick={closeTransfer} disabled={busy !== null}>Cancel</Button><Button variant="contained" color={transferDirection === "repository_to_stack" ? "warning" : "primary"} disabled={busy !== null || !transferPreview || (transferPreview.changed > 0 && safeTransferCount === 0 && resolvedConflictPaths.size === 0) || (includeSensitive && sensitiveConfirmation !== "INCLUDE SENSITIVE FILES")} onClick={() => void runTransfer()}>{busy?.startsWith("transfer-") && <CircularProgress size={16} sx={{mr: 1}}/>}{transferPreview?.changed === 0 ? "Confirm baseline" : transferDirection === "stack_to_repository" ? "Commit selected and push" : "Backup and import selected"}</Button></DialogActions>
        </Dialog>

        <Dialog open={comparison !== null} onClose={() => busy === null && setComparison(null)} fullWidth maxWidth="lg">
            <DialogTitle sx={{display: "flex", alignItems: "center", gap: 1}}><CompareArrowsOutlined/>Compare conflict — <Box component="span" sx={{fontFamily: "monospace", fontSize: ".9em", overflowWrap: "anywhere"}}>{comparison?.path}</Box></DialogTitle>
            <DialogContent dividers sx={{p: 0}}>
                {comparison?.comparable ? <>
                    <Stack direction="row" sx={{px: 2, py: 1, bgcolor: "background.paper", borderBottom: 1, borderColor: "divider"}}><Typography variant="body2" sx={{width: "50%", fontWeight: 700}}>Dockman · {formatBytes(comparison.dockman.size)} · {comparison.dockman.sha256.slice(0, 12)}</Typography><Typography variant="body2" sx={{width: "50%", fontWeight: 700}}>Git · {formatBytes(comparison.git.size)} · {comparison.git.sha256.slice(0, 12)}</Typography></Stack>
                    <DiffEditor height="52vh" theme="vs-dark" original={comparison.dockman.content || ""} modified={comparison.git.content || ""} language={comparisonLanguage(comparison.path)} options={{readOnly: true, renderSideBySide: true, minimap: {enabled: false}, wordWrap: "on", originalEditable: false, automaticLayout: true}}/>
                </> : <Stack spacing={2} sx={{p: 3}}><Alert severity="warning">{comparison?.reason || "This file cannot be displayed as a text comparison."}</Alert><Typography>Dockman: {comparison && formatBytes(comparison.dockman.size)} · <code>{comparison?.dockman.sha256}</code></Typography><Typography>Git: {comparison && formatBytes(comparison.git.size)} · <code>{comparison?.git.sha256}</code></Typography></Stack>}
            </DialogContent>
            <DialogActions sx={{justifyContent: "space-between"}}><Button onClick={() => setComparison(null)}>Leave pending</Button><Stack direction="row" spacing={1}><Button color="warning" variant="outlined" onClick={() => comparison && (transferDirection === "stack_to_repository" ? keepCurrentTarget(comparison.path) : keepCurrentSource(comparison.path))}>Keep Git</Button><Button color="primary" variant="contained" onClick={() => comparison && (transferDirection === "stack_to_repository" ? keepCurrentSource(comparison.path) : keepCurrentTarget(comparison.path))}>Keep Dockman</Button></Stack></DialogActions>
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

        <Dialog open={automationBinding !== null} onClose={() => busy === null && setAutomationBinding(null)} fullWidth maxWidth="xs">
            <DialogTitle sx={{display: "flex", alignItems: "center", gap: 1}}><SyncOutlined/>Automatic Git monitoring</DialogTitle>
            <DialogContent dividers><Stack spacing={2} sx={{pt: .5}}>
                <FormControlLabel control={<Switch checked={automationForm.enabled} onChange={(event) => setAutomationForm({...automationForm, enabled: event.target.checked})}/>} label="Synchronize changes from Git automatically"/>
                <TextField label="Check interval (minutes)" type="number" value={automationForm.intervalMinutes} onChange={(event) => setAutomationForm({...automationForm, intervalMinutes: Number(event.target.value)})} disabled={!automationForm.enabled} slotProps={{htmlInput: {min: 5, max: 1440, step: 5}}} helperText="Between 5 minutes and 24 hours."/>
                <Alert severity="info">Dockman fetches Git and fast-forwards the managed repository, then imports allowed files with a backup. Missing source files are not deleted.</Alert>
                <Alert severity="warning">A dirty/diverged repository or any file conflict blocks the complete automatic import. Deployment and container actions remain manual.</Alert>
                {automationBinding?.lastAutoSyncSuccessAt && <Typography variant="body2" color="text.secondary">Last successful synchronization: {dateLabel(automationBinding.lastAutoSyncSuccessAt)}</Typography>}
                {automationBinding?.autoSyncError && <Alert severity="error">{automationBinding.autoSyncError}</Alert>}
            </Stack></DialogContent>
            <DialogActions><Button onClick={() => setAutomationBinding(null)} disabled={busy !== null}>Cancel</Button><Button variant="contained" onClick={() => void saveBindingAutomation()} disabled={busy !== null || (automationForm.enabled && (automationForm.intervalMinutes < 5 || automationForm.intervalMinutes > 1440))}>{busy?.startsWith("binding-automation-") && <CircularProgress size={16} sx={{mr: 1}}/>}Save</Button></DialogActions>
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
