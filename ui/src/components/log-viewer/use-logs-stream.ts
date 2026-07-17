import {useCallback, useEffect, useRef, useState} from "react";
import {useHostClient} from "../../lib/api.ts";
import {DockerService} from "../../gen/docker/v1/docker_pb.ts";
import {appendEntries, isKeepAlive, type LogEntry, toLogEntry} from "./log-model.ts";

export type LogStreamStatus = 'idle' | 'connecting' | 'live' | 'reconnecting' | 'paused' | 'ended';

export interface LogsStreamParams {
    containerIds: string[];
    tail: number;
    since?: number; // unix seconds, undefined = from the tail only
    until?: number;
    follow: boolean;
    paused: boolean;
}

const FLUSH_DELAY_MS = 80;
const RETRY_MIN_MS = 1000;
const RETRY_MAX_MS = 30000;

// Streams the requested containers' logs into an immutable, capped LogEntry
// buffer. Pausing closes the stream but keeps the buffer; resuming (and any
// silent reconnection) replays from the last seen timestamp and drops the
// lines it already has, so the buffer never duplicates.
export function useLogsStream(params: LogsStreamParams) {
    const client = useHostClient(DockerService);
    const [entries, setEntries] = useState<LogEntry[]>([]);
    const [status, setStatus] = useState<LogStreamStatus>('idle');

    const idsKey = params.containerIds.join(',');
    const {tail, since, until, follow, paused} = params;

    // newest timestamp seen per container, used to dedupe replays
    const lastNanoRef = useRef<Map<string, bigint>>(new Map());

    const clear = useCallback(() => {
        setEntries([]);
    }, []);

    // changing what is being streamed starts a fresh buffer; pausing does not
    useEffect(() => {
        setEntries([]);
        lastNanoRef.current = new Map();
    }, [client, idsKey, tail, since, until, follow]);

    useEffect(() => {
        if (!idsKey) {
            setStatus('idle');
            return;
        }
        if (paused) {
            setStatus('paused');
            return;
        }

        const abort = new AbortController();
        let closed = false;
        let pending: LogEntry[] = [];
        let flushTimer: ReturnType<typeof setTimeout> | null = null;

        const flush = () => {
            flushTimer = null;
            if (pending.length === 0) return;
            const batch = pending;
            pending = [];
            setEntries(prev => appendEntries(prev, batch));
        };
        const scheduleFlush = () => {
            if (flushTimer === null) {
                flushTimer = setTimeout(flush, FLUSH_DELAY_MS);
            }
        };

        const containerIds = idsKey.split(',');

        // resume/reconnect from just before the oldest "last seen" timestamp;
        // the per-container dedupe below drops the overlap
        const resumeSince = (): number => {
            const seen = containerIds
                .map(id => lastNanoRef.current.get(id) ?? 0n)
                .filter(n => n !== 0n);
            if (seen.length === 0) return since ?? 0;
            const oldest = seen.reduce((a, b) => (a < b ? a : b));
            return Number(oldest / 1000000000n);
        };

        const run = async () => {
            let attempt = 0;
            let backoff = RETRY_MIN_MS;
            while (!closed) {
                setStatus(attempt === 0 ? 'connecting' : 'reconnecting');
                attempt++;
                let live = false;
                try {
                    const stream = client.containerLogsStream({
                        containerIds,
                        tail,
                        since: BigInt(resumeSince()),
                        until: BigInt(until ?? 0),
                        follow,
                    }, {signal: abort.signal});

                    for await (const line of stream) {
                        if (!live) {
                            live = true;
                            backoff = RETRY_MIN_MS;
                            setStatus('live');
                        }
                        if (isKeepAlive(line)) continue;

                        const entry = toLogEntry(line);
                        const seen = lastNanoRef.current.get(entry.containerId) ?? 0n;
                        if (entry.timeNano !== 0n) {
                            if (entry.timeNano <= seen) continue; // replayed line
                            lastNanoRef.current.set(entry.containerId, entry.timeNano);
                        }
                        pending.push(entry);
                        scheduleFlush();
                    }

                    if (!follow) {
                        // bounded query: the stream ending is the happy path
                        flush();
                        setStatus('ended');
                        return;
                    }
                    // follow stream ended without an abort: server went away
                } catch {
                    if (closed || abort.signal.aborted) return;
                }

                flush();
                setStatus('reconnecting');
                await new Promise(resolve => setTimeout(resolve, backoff));
                backoff = Math.min(backoff * 2, RETRY_MAX_MS);
            }
        };
        void run();

        return () => {
            closed = true;
            abort.abort();
            if (flushTimer !== null) clearTimeout(flushTimer);
        };
    }, [client, idsKey, tail, since, until, follow, paused]);

    return {entries, status, clear};
}
