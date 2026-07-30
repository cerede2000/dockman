import {useCallback, useEffect, useMemo, useState} from "react";
import {
    Alert, Box, Button, Checkbox, Chip, CircularProgress, IconButton, Paper, Stack, Tooltip, Typography,
} from "@mui/material";
import {
    ChevronRight, ExpandMore, FolderOutlined, InsertDriveFileOutlined, RefreshOutlined, UndoOutlined,
} from "@mui/icons-material";
import {gitAPI as api} from "../lib/git-api.ts";

export interface GitPolicyForm {
    profile: "compose_only" | "compose_config" | "all_files";
    includes: string;
    excludes: string;
}

interface PolicyTreeEntry {
    name: string;
    path: string;
    directory: boolean;
    origin: "dockman" | "git" | "both";
    state: "included" | "excluded" | "mixed" | "protected";
    reason?: string;
    selectable: boolean;
    explicitlyIncluded: boolean;
    explicitlyExcluded: boolean;
}

interface PolicyTreeView {
    directory: string;
    entries: PolicyTreeEntry[];
    warnings?: string[];
}

interface Props {
    bindingId: string;
    policy: GitPolicyForm;
    onChange: (policy: GitPolicyForm) => void;
    disabled?: boolean;
}

function lines(value: string): string[] {
    return value.split("\n");
}

function normalizedLines(value: string): string[] {
    return lines(value).map((line) => line.trim()).filter(Boolean);
}

function withoutExactRule(value: string, rule: string): string {
    return lines(value).filter((line) => line.trim() !== rule).join("\n").replace(/^\n+|\n+$/g, "");
}

function withExactRule(value: string, rule: string): string {
    if (normalizedLines(value).includes(rule)) return value;
    const trimmed = value.replace(/\s+$/g, "");
    return trimmed ? `${trimmed}\n${rule}` : rule;
}

function escapeGlobLiteral(value: string): string {
    let escaped = "";
    for (const character of value) {
        escaped += "\\*?[]{}!".includes(character) ? `\\${character}` : character;
    }
    return escaped;
}

function generatedRules(entry: PolicyTreeEntry) {
    const literal = escapeGlobLiteral(entry.path);
    return {
        include: entry.directory ? `/${literal}/**` : `/${literal}`,
        exclude: entry.directory ? `/${literal}/` : `/${literal}`,
    };
}

function stateColor(state: PolicyTreeEntry["state"]): "success" | "warning" | "default" | "info" {
    if (state === "included") return "success";
    if (state === "mixed") return "info";
    if (state === "protected") return "warning";
    return "default";
}

export default function GitPolicyFileTree({bindingId, policy, onChange, disabled = false}: Props) {
    const [views, setViews] = useState<Record<string, PolicyTreeView>>({});
    const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
    const [loading, setLoading] = useState<Set<string>>(() => new Set());
    const [error, setError] = useState("");

    const loadDirectory = useCallback(async (directory: string, candidatePolicy: GitPolicyForm = policy) => {
        setLoading((current) => new Set(current).add(directory));
        setError("");
        try {
            const view = await api<PolicyTreeView>(`/bindings/${bindingId}/policy-tree`, {
                method: "POST",
                body: JSON.stringify({
                    directory,
                    profile: candidatePolicy.profile,
                    includePatterns: candidatePolicy.includes.split("\n"),
                    excludePatterns: candidatePolicy.excludes.split("\n"),
                }),
            });
            setViews((current) => ({...current, [directory]: view}));
        } catch (requestError) {
            setError((requestError as Error).message);
        } finally {
            setLoading((current) => {
                const next = new Set(current);
                next.delete(directory);
                return next;
            });
        }
    }, [bindingId, policy]);

    useEffect(() => {
        setViews({});
        setExpanded(new Set());
        void loadDirectory("");
        // Policy typing is deliberately not a dependency: broad rules are
        // previewed on explicit refresh, while tree-generated rules refresh
        // their parent immediately. This avoids requests on every keystroke.
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [bindingId]);

    const warnings = useMemo(() => Array.from(new Set(Object.values(views).flatMap((view) => view.warnings || []))), [views]);

    const refresh = () => {
        setViews({});
        setExpanded(new Set());
        void loadDirectory("");
    };

    const toggleDirectory = (entry: PolicyTreeEntry) => {
        if (!entry.directory) return;
        const next = new Set(expanded);
        if (next.has(entry.path)) {
            next.delete(entry.path);
        } else {
            next.add(entry.path);
            if (!views[entry.path]) void loadDirectory(entry.path);
        }
        setExpanded(next);
    };

    const applySelection = (entry: PolicyTreeEntry, checked: boolean) => {
        const rules = generatedRules(entry);
        let includes = withoutExactRule(policy.includes, rules.include);
        let excludes = withoutExactRule(policy.excludes, rules.exclude);
        if (checked) includes = withExactRule(includes, rules.include);
        else excludes = withExactRule(excludes, rules.exclude);
        const nextPolicy = {...policy, includes, excludes};
        onChange(nextPolicy);
        const parent = entry.path.includes("/") ? entry.path.slice(0, entry.path.lastIndexOf("/")) : "";
        void loadDirectory(parent, nextPolicy);
    };

    const resetSelection = (entry: PolicyTreeEntry) => {
        const rules = generatedRules(entry);
        const nextPolicy = {
            ...policy,
            includes: withoutExactRule(policy.includes, rules.include),
            excludes: withoutExactRule(policy.excludes, rules.exclude),
        };
        onChange(nextPolicy);
        const parent = entry.path.includes("/") ? entry.path.slice(0, entry.path.lastIndexOf("/")) : "";
        void loadDirectory(parent, nextPolicy);
    };

    const renderDirectory = (directory: string, depth: number): React.ReactNode => {
        const view = views[directory];
        if (!view && loading.has(directory)) return <Box key={`${directory}-loading`} sx={{py: 2, pl: 2 + depth * 2}}><CircularProgress size={18}/></Box>;
        if (!view) return null;
        return view.entries.map((entry) => {
            const rules = generatedRules(entry);
            const exactOverride = normalizedLines(policy.includes).includes(rules.include) || normalizedLines(policy.excludes).includes(rules.exclude);
            const isExpanded = expanded.has(entry.path);
            return <Box key={entry.path}>
                <Box sx={{display: "grid", gridTemplateColumns: "minmax(280px, 1fr) 90px 120px minmax(180px, .8fr) 42px", alignItems: "center", minHeight: 42, borderBottom: 1, borderColor: "divider", "&:hover": {bgcolor: "action.hover"}}}>
                    <Stack direction="row" spacing={.5} sx={{alignItems: "center", minWidth: 0, pl: 1 + depth * 2}}>
                        {entry.directory ? <IconButton size="small" onClick={() => toggleDirectory(entry)}>{isExpanded ? <ExpandMore fontSize="small"/> : <ChevronRight fontSize="small"/>}</IconButton> : <Box sx={{width: 34}}/>}
                        <Checkbox size="small" disabled={disabled || !entry.selectable} checked={entry.state === "included"} indeterminate={entry.state === "mixed"} onChange={(_, checked) => applySelection(entry, checked)}/>
                        {entry.directory ? <FolderOutlined color="warning" fontSize="small"/> : <InsertDriveFileOutlined color="info" fontSize="small"/>}
                        <Tooltip title={entry.path}><Typography variant="body2" onClick={() => toggleDirectory(entry)} sx={{fontFamily: "monospace", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", cursor: entry.directory ? "pointer" : "default"}}>{entry.name}</Typography></Tooltip>
                    </Stack>
                    <Box><Chip size="small" variant="outlined" label={entry.origin} color={entry.origin === "both" ? "success" : "default"}/></Box>
                    <Box><Chip size="small" variant="outlined" label={entry.state} color={stateColor(entry.state)}/></Box>
                    <Tooltip title={entry.reason || ""}><Typography variant="caption" color="text.secondary" sx={{overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", pr: 1}}>{entry.reason || "—"}</Typography></Tooltip>
                    <Tooltip title={exactOverride ? "Remove the precise rule generated by this selector" : "No precise selector rule"}><span><IconButton size="small" disabled={disabled || !exactOverride} onClick={() => resetSelection(entry)}><UndoOutlined fontSize="small"/></IconButton></span></Tooltip>
                </Box>
                {entry.directory && isExpanded && renderDirectory(entry.path, depth + 1)}
            </Box>;
        });
    };

    return <Stack spacing={1.25} sx={{minWidth: 0}}>
        <Stack direction="row" sx={{alignItems: "center", justifyContent: "space-between", gap: 1}}>
            <Box><Typography variant="subtitle2">File and folder selector</Typography><Typography variant="caption" color="text.secondary">Expand folders on demand. A checkbox creates one precise rule; untouched entries keep the base profile.</Typography></Box>
            <Button size="small" startIcon={<RefreshOutlined/>} onClick={refresh} disabled={disabled || loading.size > 0}>Refresh preview</Button>
        </Stack>
        {error && <Alert severity="error">{error}</Alert>}
        {warnings.map((warning) => <Alert key={warning} severity="warning">{warning}</Alert>)}
        <Paper variant="outlined" sx={{overflow: "hidden", minHeight: 300}}>
            <Box sx={{display: "grid", gridTemplateColumns: "minmax(280px, 1fr) 90px 120px minmax(180px, .8fr) 42px", px: 1, py: 1, bgcolor: "background.paper", borderBottom: 1, borderColor: "divider"}}>
                <Typography variant="caption" sx={{fontWeight: 700}}>PATH</Typography><Typography variant="caption" sx={{fontWeight: 700}}>SOURCE</Typography><Typography variant="caption" sx={{fontWeight: 700}}>POLICY</Typography><Typography variant="caption" sx={{fontWeight: 700}}>REASON</Typography><span/>
            </Box>
            <Box sx={{maxHeight: "58vh", overflow: "auto"}}>{loading.has("") && !views[""] ? <Box sx={{display: "grid", placeItems: "center", py: 8}}><CircularProgress size={24}/></Box> : views[""]?.entries.length === 0 ? <Typography color="text.secondary" sx={{p: 4, textAlign: "center"}}>This linked folder is empty on Dockman and Git.</Typography> : renderDirectory("", 0)}</Box>
        </Paper>
        <Typography variant="caption" color="text.secondary">The selector never bypasses hard protections for Git metadata, special files, symlinks, sensitive files or size limits. Use the advanced rules for broad patterns such as <code>*.log</code>.</Typography>
    </Stack>;
}
