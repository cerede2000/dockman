import type {LogLine} from "../../gen/docker/v1/docker_pb.ts";
import {type AnsiSegment, parseAnsi} from "./ansi.ts";

export interface LogEntry {
    id: number;
    text: string;
    timeNano: bigint; // 0n when the line had no parsable daemon timestamp
    stream: number; // 1 = stdout, 2 = stderr
    containerId: string;
    containerName: string;
}

// soft cap: the buffer may grow to 2x before being compacted back, so steady
// streaming does not reslice the whole array on every batch
export const LOG_BUFFER_CAP = 2000;

let nextEntryId = 1;

// an empty frame is the server keepalive, not a log line
export const isKeepAlive = (line: LogLine) =>
    line.containerId === "" && line.text === "" && line.timeNano === 0n;

export function toLogEntry(line: LogLine): LogEntry {
    return {
        id: nextEntryId++,
        text: line.text,
        timeNano: line.timeNano,
        stream: line.stream,
        containerId: line.containerId,
        containerName: line.containerName,
    };
}

export function appendEntries(buffer: LogEntry[], batch: LogEntry[]): LogEntry[] {
    if (batch.length === 0) return buffer;
    // an oversized batch alone can exceed the cap, pre-trim it
    const trimmed = batch.length > LOG_BUFFER_CAP ? batch.slice(-LOG_BUFFER_CAP) : batch;
    const next = [...buffer, ...trimmed];
    if (next.length > LOG_BUFFER_CAP * 2) {
        return next.slice(-LOG_BUFFER_CAP);
    }
    return next;
}

// merged-view container prefix palette, assigned by position in the request
export const CONTAINER_COLORS = [
    '#64b5f6', '#81c784', '#ffb74d', '#e57373', '#ba68c8',
    '#f06292', '#4db6ac', '#fff176', '#a1887f', '#90a4ae',
];

export const containerColor = (index: number) =>
    CONTAINER_COLORS[((index % CONTAINER_COLORS.length) + CONTAINER_COLORS.length) % CONTAINER_COLORS.length];

export function formatLogTime(timeNano: bigint): string {
    if (timeNano === 0n) return "";
    const date = new Date(Number(timeNano / 1000000n));
    const pad = (n: number, w = 2) => String(n).padStart(w, '0');
    return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} `
        + `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}.${pad(date.getMilliseconds(), 3)}`;
}

export const matchesQuery = (entry: LogEntry, lowerQuery: string) =>
    entry.text.toLowerCase().includes(lowerQuery)
    || entry.containerName.toLowerCase().includes(lowerQuery);

// plain-text export used by copy and download
export function logsToText(entries: LogEntry[], withTimestamps: boolean, withNames: boolean): string {
    return entries.map(e => {
        let line = "";
        if (withTimestamps && e.timeNano !== 0n) line += `${formatLogTime(e.timeNano)} `;
        if (withNames && e.containerName) line += `[${e.containerName}] `;
        return line + stripAnsi(e.text);
    }).join("\n");
}

export function stripAnsi(text: string): string {
    return segmentsFor(text).map(s => s.text).join("");
}

// per-line ANSI parse cache; entries are immutable so the cache never stales
const ansiCache = new Map<string, AnsiSegment[]>();
const ANSI_CACHE_MAX = LOG_BUFFER_CAP * 4;

export function segmentsFor(text: string): AnsiSegment[] {
    const hit = ansiCache.get(text);
    if (hit) return hit;
    const parsed = parseAnsi(text);
    if (ansiCache.size >= ANSI_CACHE_MAX) ansiCache.clear();
    ansiCache.set(text, parsed);
    return parsed;
}

// splits ANSI segments around query matches so the matching parts can be
// wrapped in <mark> without disturbing the styling
export interface HighlightedPiece {
    segment: AnsiSegment;
    isMatch: boolean;
}

export function highlightSegments(segments: AnsiSegment[], lowerQuery: string): HighlightedPiece[] {
    if (!lowerQuery) return segments.map(segment => ({segment, isMatch: false}));

    const pieces: HighlightedPiece[] = [];
    for (const segment of segments) {
        const lower = segment.text.toLowerCase();
        let from = 0;
        while (from < segment.text.length) {
            const at = lower.indexOf(lowerQuery, from);
            if (at < 0) {
                pieces.push({segment: {...segment, text: segment.text.slice(from)}, isMatch: false});
                break;
            }
            if (at > from) {
                pieces.push({segment: {...segment, text: segment.text.slice(from, at)}, isMatch: false});
            }
            pieces.push({segment: {...segment, text: segment.text.slice(at, at + lowerQuery.length)}, isMatch: true});
            from = at + lowerQuery.length;
        }
    }
    return pieces.filter(p => p.segment.text !== "");
}
