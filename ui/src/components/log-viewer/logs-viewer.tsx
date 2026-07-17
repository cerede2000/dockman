import {type CSSProperties, useCallback, useEffect, useMemo, useRef, useState} from "react";
import {
    Box,
    Chip,
    IconButton,
    InputAdornment,
    MenuItem,
    Popover,
    Select,
    Stack,
    TextField,
    Tooltip,
    Typography,
} from "@mui/material";
import SearchIcon from "@mui/icons-material/Search";
import FilterAltIcon from "@mui/icons-material/FilterAlt";
import KeyboardArrowUpIcon from "@mui/icons-material/KeyboardArrowUp";
import KeyboardArrowDownIcon from "@mui/icons-material/KeyboardArrowDown";
import AccessTimeIcon from "@mui/icons-material/AccessTime";
import WrapTextIcon from "@mui/icons-material/WrapText";
import VerticalAlignBottomIcon from "@mui/icons-material/VerticalAlignBottom";
import PauseIcon from "@mui/icons-material/Pause";
import PlayArrowIcon from "@mui/icons-material/PlayArrow";
import DeleteSweepIcon from "@mui/icons-material/DeleteSweep";
import ContentCopyIcon from "@mui/icons-material/ContentCopy";
import DownloadIcon from "@mui/icons-material/Download";
import DateRangeIcon from "@mui/icons-material/DateRange";
import scrollbarStyles from "../scrollbar-style.tsx";
import {
    containerColor,
    formatLogTime,
    highlightSegments,
    type LogEntry,
    logsToText,
    matchesQuery,
    segmentsFor,
} from "./log-model.ts";
import {type LogStreamStatus, useLogsStream} from "./use-logs-stream.ts";

export interface LogsViewerContainer {
    id: string;
    name?: string;
}

interface LogsViewerProps {
    containers: LogsViewerContainer[];
}

// scroll distance from the bottom under which the view is considered "at the
// bottom" and auto-scroll stays engaged
const BOTTOM_STICKINESS_PX = 40;

const PREF_TIMESTAMPS = 'dockman-logs-timestamps';
const PREF_WRAP = 'dockman-logs-wrap';
const PREF_TAIL = 'dockman-logs-tail';

const TAIL_OPTIONS = [100, 500, 1000, 2000];

const STATUS_META: Record<LogStreamStatus, { label: string; color: string }> = {
    idle: {label: 'Idle', color: '#9e9e9e'},
    connecting: {label: 'Connecting', color: '#ffb74d'},
    live: {label: 'Live', color: '#66bb6a'},
    reconnecting: {label: 'Reconnecting', color: '#ffb74d'},
    paused: {label: 'Paused', color: '#9e9e9e'},
    ended: {label: 'Ended', color: '#64b5f6'},
};

const readBoolPref = (key: string, fallback: boolean) => {
    const raw = localStorage.getItem(key);
    return raw === null ? fallback : raw === 'true';
};

function LogRow({entry, lowerQuery, isCurrentMatch, showTimestamps, showName, nameColor, wrap}: {
    entry: LogEntry;
    lowerQuery: string;
    isCurrentMatch: boolean;
    showTimestamps: boolean;
    showName: boolean;
    nameColor: string;
    wrap: boolean;
}) {
    const pieces = highlightSegments(segmentsFor(entry.text), lowerQuery);
    return (
        <div
            data-log-id={entry.id}
            style={{
                whiteSpace: wrap ? 'pre-wrap' : 'pre',
                wordBreak: wrap ? 'break-all' : 'normal',
                borderLeft: `2px solid ${entry.stream === 2 ? 'rgba(244,67,54,0.55)' : 'transparent'}`,
                paddingLeft: 6,
                minHeight: '1.4em',
                lineHeight: 1.4,
            }}
        >
            {showTimestamps && entry.timeNano !== 0n && (
                <span style={{color: '#858585'}}>{formatLogTime(entry.timeNano)} </span>
            )}
            {showName && entry.containerName !== "" && (
                <span style={{color: nameColor, fontWeight: 600}}>[{entry.containerName}] </span>
            )}
            {pieces.map((piece, i) => {
                const s = piece.segment;
                const style: CSSProperties = {
                    color: s.color,
                    backgroundColor: s.background,
                    fontWeight: s.bold ? 700 : undefined,
                    opacity: s.dim ? 0.6 : undefined,
                    fontStyle: s.italic ? 'italic' : undefined,
                    textDecoration: s.underline ? 'underline' : undefined,
                };
                if (!piece.isMatch) {
                    return <span key={i} style={style}>{s.text}</span>;
                }
                return (
                    <mark
                        key={i}
                        style={{
                            ...style,
                            backgroundColor: isCurrentMatch ? 'rgba(255,193,7,0.95)' : 'rgba(255,193,7,0.4)',
                            color: isCurrentMatch ? '#000' : s.color ?? '#fff',
                        }}
                    >
                        {s.text}
                    </mark>
                );
            })}
        </div>
    );
}

export function LogsViewer({containers}: LogsViewerProps) {
    const isMerged = containers.length > 1;

    // merged view: which containers are enabled (all by default)
    const [disabledIds, setDisabledIds] = useState<ReadonlySet<string>>(new Set());
    const activeIds = useMemo(
        () => containers.map(c => c.id).filter(id => !disabledIds.has(id)),
        [containers, disabledIds],
    );

    const [query, setQuery] = useState("");
    const [filterMode, setFilterMode] = useState(false);
    const [currentMatch, setCurrentMatch] = useState(0);

    const [showTimestamps, setShowTimestamps] = useState(() => readBoolPref(PREF_TIMESTAMPS, false));
    const [wrap, setWrap] = useState(() => readBoolPref(PREF_WRAP, true));
    const [tail, setTail] = useState(() => Number(localStorage.getItem(PREF_TAIL)) || 1000);

    const [paused, setPaused] = useState(false);
    const [autoScroll, setAutoScroll] = useState(true);

    // time range: unix seconds once applied; an upper bound ends the stream
    const [range, setRange] = useState<{ since?: number; until?: number }>({});
    const [rangeAnchor, setRangeAnchor] = useState<HTMLElement | null>(null);
    const [sinceInput, setSinceInput] = useState("");
    const [untilInput, setUntilInput] = useState("");

    const {entries, status, clear} = useLogsStream({
        containerIds: activeIds,
        tail,
        since: range.since,
        until: range.until,
        follow: range.until === undefined,
        paused,
    });

    const toggleTimestamps = () => setShowTimestamps(prev => {
        localStorage.setItem(PREF_TIMESTAMPS, String(!prev));
        return !prev;
    });
    const toggleWrap = () => setWrap(prev => {
        localStorage.setItem(PREF_WRAP, String(!prev));
        return !prev;
    });
    const changeTail = (value: number) => {
        localStorage.setItem(PREF_TAIL, String(value));
        setTail(value);
    };

    const lowerQuery = query.trim().toLowerCase();
    const displayed = useMemo(
        () => (filterMode && lowerQuery ? entries.filter(e => matchesQuery(e, lowerQuery)) : entries),
        [entries, filterMode, lowerQuery],
    );
    const matchIds = useMemo(
        () => (lowerQuery ? displayed.filter(e => matchesQuery(e, lowerQuery)).map(e => e.id) : []),
        [displayed, lowerQuery],
    );
    const boundedMatch = matchIds.length === 0 ? 0 : Math.min(currentMatch, matchIds.length - 1);
    const currentMatchId = matchIds.length > 0 ? matchIds[boundedMatch] : undefined;

    const colorFor = useCallback((containerId: string) => {
        const idx = containers.findIndex(c => c.id === containerId);
        return containerColor(idx < 0 ? 0 : idx);
    }, [containers]);

    // --- scrolling ---
    const scrollRef = useRef<HTMLDivElement | null>(null);
    const programmaticScroll = useRef(false);

    useEffect(() => {
        if (!autoScroll || paused) return;
        const el = scrollRef.current;
        if (!el) return;
        programmaticScroll.current = true;
        el.scrollTop = el.scrollHeight;
        requestAnimationFrame(() => {
            programmaticScroll.current = false;
        });
    }, [entries, autoScroll, paused, wrap, showTimestamps]);

    const handleScroll = () => {
        if (programmaticScroll.current) return;
        const el = scrollRef.current;
        if (!el) return;
        const fromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
        if (fromBottom <= BOTTOM_STICKINESS_PX) {
            if (!autoScroll) setAutoScroll(true);
        } else if (autoScroll) {
            setAutoScroll(false);
        }
    };

    const scrollToEntry = useCallback((entryId: number) => {
        setAutoScroll(false);
        const el = scrollRef.current?.querySelector(`[data-log-id="${entryId}"]`);
        el?.scrollIntoView({block: 'center'});
    }, []);

    const goToMatch = useCallback((direction: 1 | -1) => {
        if (matchIds.length === 0) return;
        const next = (boundedMatch + direction + matchIds.length) % matchIds.length;
        setCurrentMatch(next);
        scrollToEntry(matchIds[next]);
    }, [matchIds, boundedMatch, scrollToEntry]);

    // --- clipboard / file export ---
    const exportText = () => logsToText(displayed, showTimestamps, isMerged);
    const handleCopy = () => navigator.clipboard?.writeText(exportText());
    const handleDownload = () => {
        const label = isMerged ? 'stack' : (containers[0]?.name ?? containers[0]?.id.substring(0, 12) ?? 'container');
        const blob = new Blob([exportText()], {type: 'text/plain'});
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = `${label}-logs.txt`;
        a.click();
        URL.revokeObjectURL(url);
    };

    // --- time range popover ---
    const applyRange = () => {
        const parse = (value: string) => {
            if (!value) return undefined;
            const ms = new Date(value).getTime();
            return Number.isNaN(ms) ? undefined : Math.floor(ms / 1000);
        };
        setRange({since: parse(sinceInput), until: parse(untilInput)});
        setRangeAnchor(null);
    };
    const clearRange = () => {
        setSinceInput("");
        setUntilInput("");
        setRange({});
        setRangeAnchor(null);
    };
    const rangeActive = range.since !== undefined || range.until !== undefined;

    const toggleContainer = (id: string) => {
        setDisabledIds(prev => {
            if (prev.has(id)) {
                const next = new Set(prev);
                next.delete(id);
                return next;
            }
            // never disable the last enabled container
            if (activeIds.length <= 1) return prev;
            return new Set(prev).add(id);
        });
    };

    const statusMeta = STATUS_META[status];
    const iconSx = (active: boolean) => ({
        color: active ? 'primary.main' : 'text.secondary',
        p: 0.5,
    });

    return (
        <Box sx={{height: '100%', display: 'flex', flexDirection: 'column', minHeight: 0, bgcolor: '#1E1E1E'}}>
            {/* toolbar */}
            <Stack
                direction="row"
                spacing={0.5}
                alignItems="center"
                flexWrap="wrap"
                useFlexGap
                sx={{px: 1, py: 0.5, borderBottom: '1px solid', borderColor: 'divider', flexShrink: 0}}
            >
                <TextField
                    size="small"
                    placeholder="Search logs..."
                    value={query}
                    onChange={(e) => {
                        setQuery(e.target.value);
                        setCurrentMatch(0);
                    }}
                    onKeyDown={(e) => {
                        if (e.key === 'Enter') {
                            e.preventDefault();
                            goToMatch(e.shiftKey ? -1 : 1);
                        }
                    }}
                    sx={{width: 230, '& .MuiInputBase-root': {fontSize: '0.8rem'}}}
                    slotProps={{
                        input: {
                            startAdornment: (
                                <InputAdornment position="start">
                                    <SearchIcon sx={{fontSize: 16}}/>
                                </InputAdornment>
                            ),
                        },
                    }}
                />
                {lowerQuery && (
                    <>
                        <Typography variant="caption" sx={{color: 'text.secondary', minWidth: 48, textAlign: 'center'}}>
                            {matchIds.length === 0 ? '0/0' : `${boundedMatch + 1}/${matchIds.length}`}
                        </Typography>
                        <Tooltip title="Previous match (Shift+Enter)">
                            <IconButton size="small" sx={iconSx(false)} onClick={() => goToMatch(-1)}>
                                <KeyboardArrowUpIcon sx={{fontSize: 18}}/>
                            </IconButton>
                        </Tooltip>
                        <Tooltip title="Next match (Enter)">
                            <IconButton size="small" sx={iconSx(false)} onClick={() => goToMatch(1)}>
                                <KeyboardArrowDownIcon sx={{fontSize: 18}}/>
                            </IconButton>
                        </Tooltip>
                    </>
                )}
                <Tooltip title={filterMode ? "Show all lines" : "Only show matching lines"}>
                    <IconButton size="small" sx={iconSx(filterMode)} onClick={() => setFilterMode(f => !f)}>
                        <FilterAltIcon sx={{fontSize: 18}}/>
                    </IconButton>
                </Tooltip>

                <Box sx={{flexGrow: 1}}/>

                <Select
                    size="small"
                    value={tail}
                    onChange={(e) => changeTail(Number(e.target.value))}
                    sx={{fontSize: '0.8rem', '& .MuiSelect-select': {py: 0.5}}}
                >
                    {TAIL_OPTIONS.map(option => (
                        <MenuItem key={option} value={option} sx={{fontSize: '0.8rem'}}>
                            {option} lines
                        </MenuItem>
                    ))}
                </Select>

                <Tooltip title="Time range">
                    <IconButton size="small" sx={iconSx(rangeActive)} onClick={(e) => setRangeAnchor(e.currentTarget)}>
                        <DateRangeIcon sx={{fontSize: 18}}/>
                    </IconButton>
                </Tooltip>
                <Tooltip title="Toggle timestamps">
                    <IconButton size="small" sx={iconSx(showTimestamps)} onClick={toggleTimestamps}>
                        <AccessTimeIcon sx={{fontSize: 18}}/>
                    </IconButton>
                </Tooltip>
                <Tooltip title="Toggle word wrap">
                    <IconButton size="small" sx={iconSx(wrap)} onClick={toggleWrap}>
                        <WrapTextIcon sx={{fontSize: 18}}/>
                    </IconButton>
                </Tooltip>
                <Tooltip title="Follow new lines">
                    <IconButton size="small" sx={iconSx(autoScroll)} onClick={() => {
                        const next = !autoScroll;
                        setAutoScroll(next);
                        if (next) {
                            const el = scrollRef.current;
                            if (el) el.scrollTop = el.scrollHeight;
                        }
                    }}>
                        <VerticalAlignBottomIcon sx={{fontSize: 18}}/>
                    </IconButton>
                </Tooltip>
                <Tooltip title={paused ? "Resume stream" : "Pause stream"}>
                    <IconButton size="small" sx={iconSx(paused)} onClick={() => setPaused(p => !p)}>
                        {paused ? <PlayArrowIcon sx={{fontSize: 18}}/> : <PauseIcon sx={{fontSize: 18}}/>}
                    </IconButton>
                </Tooltip>
                <Tooltip title="Clear">
                    <IconButton size="small" sx={iconSx(false)} onClick={clear}>
                        <DeleteSweepIcon sx={{fontSize: 18}}/>
                    </IconButton>
                </Tooltip>
                <Tooltip title="Copy to clipboard">
                    <IconButton size="small" sx={iconSx(false)} onClick={handleCopy}>
                        <ContentCopyIcon sx={{fontSize: 18}}/>
                    </IconButton>
                </Tooltip>
                <Tooltip title="Download as .txt">
                    <IconButton size="small" sx={iconSx(false)} onClick={handleDownload}>
                        <DownloadIcon sx={{fontSize: 18}}/>
                    </IconButton>
                </Tooltip>

                <Stack direction="row" spacing={0.75} alignItems="center" sx={{ml: 0.5}}>
                    <Box sx={{width: 8, height: 8, borderRadius: '50%', bgcolor: statusMeta.color}}/>
                    <Typography variant="caption" sx={{color: 'text.secondary'}}>
                        {statusMeta.label} · {displayed.length}
                    </Typography>
                </Stack>
            </Stack>

            {/* merged view: container chips */}
            {isMerged && (
                <Stack
                    direction="row"
                    spacing={0.5}
                    flexWrap="wrap"
                    useFlexGap
                    sx={{px: 1, py: 0.5, borderBottom: '1px solid', borderColor: 'divider', flexShrink: 0}}
                >
                    {containers.map((c, idx) => {
                        const enabled = !disabledIds.has(c.id);
                        return (
                            <Chip
                                key={c.id}
                                size="small"
                                label={c.name ?? c.id.substring(0, 12)}
                                onClick={() => toggleContainer(c.id)}
                                variant={enabled ? 'filled' : 'outlined'}
                                icon={<Box sx={{
                                    width: 8, height: 8, borderRadius: '50%',
                                    bgcolor: containerColor(idx), ml: '6px !important',
                                }}/>}
                                sx={{
                                    fontSize: '0.72rem',
                                    opacity: enabled ? 1 : 0.5,
                                    bgcolor: enabled ? 'rgba(255,255,255,0.08)' : 'transparent',
                                }}
                            />
                        );
                    })}
                </Stack>
            )}

            {/* log lines */}
            <Box
                ref={scrollRef}
                onScroll={handleScroll}
                sx={{
                    flexGrow: 1,
                    overflow: 'auto',
                    px: 1,
                    py: 0.5,
                    fontFamily: 'Menlo, Monaco, "Courier New", monospace',
                    fontSize: 12,
                    color: '#CCCCCC',
                    ...scrollbarStyles,
                }}
            >
                {displayed.length === 0 ? (
                    <Typography variant="body2" sx={{color: 'text.secondary', p: 2, fontStyle: 'italic'}}>
                        {status === 'connecting' ? 'Waiting for logs...' : 'No log lines'}
                    </Typography>
                ) : (
                    displayed.map(entry => (
                        <LogRow
                            key={entry.id}
                            entry={entry}
                            lowerQuery={lowerQuery}
                            isCurrentMatch={entry.id === currentMatchId}
                            showTimestamps={showTimestamps}
                            showName={isMerged}
                            nameColor={colorFor(entry.containerId)}
                            wrap={wrap}
                        />
                    ))
                )}
            </Box>

            {/* time range popover */}
            <Popover
                open={rangeAnchor !== null}
                anchorEl={rangeAnchor}
                onClose={() => setRangeAnchor(null)}
                anchorOrigin={{vertical: 'bottom', horizontal: 'right'}}
                transformOrigin={{vertical: 'top', horizontal: 'right'}}
            >
                <Stack spacing={1.5} sx={{p: 2, width: 260}}>
                    <Typography variant="subtitle2">Time range</Typography>
                    <TextField
                        label="From"
                        type="datetime-local"
                        size="small"
                        value={sinceInput}
                        onChange={(e) => setSinceInput(e.target.value)}
                        slotProps={{inputLabel: {shrink: true}}}
                    />
                    <TextField
                        label="To"
                        type="datetime-local"
                        size="small"
                        value={untilInput}
                        onChange={(e) => setUntilInput(e.target.value)}
                        slotProps={{inputLabel: {shrink: true}}}
                        helperText="Setting an upper bound stops following"
                    />
                    <Stack direction="row" spacing={1} justifyContent="flex-end">
                        <Chip size="small" label="Clear" onClick={clearRange} variant="outlined"/>
                        <Chip size="small" label="Apply" onClick={applyRange} color="primary"/>
                    </Stack>
                </Stack>
            </Popover>
        </Box>
    );
}

export default LogsViewer;
