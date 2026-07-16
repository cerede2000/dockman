import {useCallback, useEffect, useRef, useState} from 'react';
import {create} from "@bufbuild/protobuf";
import {callRPC, useHostClient} from '../lib/api.ts';
import {type ContainerStats, ContainerStatsSchema, DockerService, ORDER, SORT_FIELD} from '../gen/docker/v1/docker_pb.ts';
import {useSnackbar} from "./snackbar.ts";
import {useHostStore} from "../pages/compose/state/files.ts";
import {useConfig} from "./config.ts";

// cpuUsage sentinel marking a row seeded from the container list whose
// metrics haven't arrived yet (the first stats read takes ~1s of daemon
// sampling; the list is immediate).
export const METRICS_PENDING = -1;

// Rolling per-container metric history driving the sparklines: ~40 points at
// the 2.5s default poll interval is a little over a minute and a half of
// live history per container.
export interface StatHistory {
    cpu: number[];
    mem: number[];
}

const HISTORY_CAP = 40;

// Module-level on purpose: history must survive component remounts (a ref
// resets with its component, losing everything between two polls) and is
// shared between the host-wide stats page and per-stack stats tabs, which
// see the same containers.
const statHistories = new Map<string, StatHistory>();
let statHistoriesHost: string | null = null;

function recordHistory(host: string, list: ContainerStats[], fullListing: boolean) {
    if (statHistoriesHost !== host) {
        statHistories.clear();
        statHistoriesHost = host;
    }

    const seen = new Set<string>();
    for (const c of list) {
        seen.add(c.id);
        let h = statHistories.get(c.id);
        if (!h) {
            h = {cpu: [], mem: []};
            statHistories.set(c.id, h);
        }
        h.cpu.push(c.cpuUsage);
        const limit = Number(c.memoryLimit);
        h.mem.push(limit > 0 ? (Number(c.memoryUsage) / limit) * 100 : 0);
        if (h.cpu.length > HISTORY_CAP) h.cpu.shift();
        if (h.mem.length > HISTORY_CAP) h.mem.shift();
    }

    // drop history of containers that disappeared — but only from a full host
    // listing: a stack-scoped poll only sees its own containers and must not
    // evict everyone else's history
    if (fullListing) {
        for (const id of [...statHistories.keys()]) {
            if (!seen.has(id)) statHistories.delete(id);
        }
    }
}

// This map remains very useful for clean, client-side sorting.
const sortFieldToKeyMap: Record<SORT_FIELD, keyof ContainerStats> = {
    [SORT_FIELD.NAME]: 'name',
    [SORT_FIELD.CPU]: 'cpuUsage',
    [SORT_FIELD.MEM]: 'memoryUsage',
    [SORT_FIELD.NETWORK_RX]: 'networkRx',
    [SORT_FIELD.NETWORK_TX]: 'networkTx',
    [SORT_FIELD.DISK_R]: 'blockRead',
    [SORT_FIELD.DISK_W]: 'blockWrite',
    [SORT_FIELD.STARTED]: 'startedAt',
};

// Maps a dockman.yml `stats.sort.field` string to a SORT_FIELD. Accepts the
// column labels and a few obvious aliases, case-insensitively. Falls back to
// MEM, preserving the historical default.
const configFieldToSortField = (field?: string): SORT_FIELD => {
    switch ((field ?? '').trim().toLowerCase()) {
        case 'name':
        case 'container':
            return SORT_FIELD.NAME;
        case 'cpu':
        case 'cpu usage':
            return SORT_FIELD.CPU;
        case 'mem':
        case 'memory':
        case 'memory usage':
            return SORT_FIELD.MEM;
        case 'network_rx':
        case 'rx':
            return SORT_FIELD.NETWORK_RX;
        case 'network_tx':
        case 'tx':
            return SORT_FIELD.NETWORK_TX;
        case 'disk_r':
        case 'disk read':
            return SORT_FIELD.DISK_R;
        case 'disk_w':
        case 'disk write':
            return SORT_FIELD.DISK_W;
        case 'started':
        case 'uptime':
            return SORT_FIELD.STARTED;
        default:
            return SORT_FIELD.MEM;
    }
};

const configOrderToOrder = (order?: string): ORDER =>
    (order ?? '').trim().toLowerCase() === 'asc' ? ORDER.ASC : ORDER.DSC;

export function useDockerStats(selectedPage?: string) {
    const dockerService = useHostClient(DockerService)
    const {showError} = useSnackbar();
    const selectedHost = useHostStore(state => state.host)
    const {dockYaml} = useConfig();

    const [rawContainers, setRawContainers] = useState<ContainerStats[]>([]);
    const [loading, setLoading] = useState(true);
    // proof-of-life for the polling loop, shown in the header and bumped on
    // every successful poll (which also guarantees a redraw per tick)
    const [pollInfo, setPollInfo] = useState({seq: 0, at: 0});
    const gotStats = useRef(false);

    const [sortField, setSortField] = useState(SORT_FIELD.MEM);
    const [sortOrder, setSortOrder] = useState(ORDER.DSC);
    const [refreshInterval, setRefreshInterval] = useState(2500);
    const isInitialLoad = useRef(true);
    const resort = useRef(false)
    // Once the user sorts by hand we stop applying the dockman.yml default so a
    // late-arriving (or per-host) config never clobbers their choice.
    const userSorted = useRef(false)

    // Seed the sort from dockman.yml (stats.sort) until the user sorts manually.
    useEffect(() => {
        if (userSorted.current) return;
        const cfg = dockYaml?.statsPage?.sort;
        if (!cfg) return;
        setSortField(configFieldToSortField(cfg.sortField));
        setSortOrder(configOrderToOrder(cfg.sortOrder));
    }, [dockYaml]);

    useEffect(() => {
        let isCancelled = false;
        let timer: ReturnType<typeof setTimeout> | null = null;

        const tick = async () => {
            try {
                const {val, err} = await callRPC(() => dockerService.containerStats({
                    sortBy: sortField,
                    order: sortOrder,
                    host: selectedHost,
                    file: selectedPage ? {filename: selectedPage} : undefined
                }));

                if (isCancelled) return;

                if (err) {
                    showError(err);
                } else {
                    const list = val?.containers || [];
                    recordHistory(selectedHost, list, !selectedPage);
                    gotStats.current = true;
                    setRawContainers(list);
                    setPollInfo(p => ({seq: p.seq + 1, at: Date.now()}));
                }

                if (isInitialLoad.current) {
                    setLoading(false);
                    isInitialLoad.current = false;
                }
            } finally {
                // self-scheduling chain: the next poll is armed once this one
                // fully finished — whatever happened above — so a slow response
                // can't overlap the next one and no failure mode can silently
                // stop the refresh loop
                if (!isCancelled) {
                    timer = setTimeout(tick, refreshInterval);
                }
            }
        };

        tick();

        return () => {
            isCancelled = true;
            if (timer !== null) clearTimeout(timer);
        };
    }, [selectedHost, dockerService, selectedPage, sortField, sortOrder, refreshInterval]);

    useEffect(() => {
        // clear containers on host change (history clears itself, keyed by host)
        setRawContainers([])
        setLoading(true)
        isInitialLoad.current = true;
        gotStats.current = false;
        // re-apply the configured default for the newly selected host
        userSorted.current = false;
    }, [selectedHost]);

    // Instant first paint: seed the rows from the (immediate) container list
    // while the first stats read spends ~1s sampling in the daemon; metrics
    // cells render as pending until the first poll replaces the rows. Only
    // for the host-wide view — a stack tab can't be scoped from the list.
    useEffect(() => {
        if (selectedPage) return;
        let cancelled = false;

        const seed = async () => {
            const {val} = await callRPC(() => dockerService.containerList({}));
            // never overwrite real stats with placeholders
            if (cancelled || gotStats.current || !val) return;

            const rows = val.list
                .filter(c => c.state === 'running')
                .map(c => create(ContainerStatsSchema, {
                    id: c.id.substring(0, 12),
                    name: c.name,
                    image: c.imageName,
                    state: c.state,
                    health: c.health,
                    ipAddress: c.IPAddress,
                    cpuUsage: METRICS_PENDING,
                }))
                .sort((a, b) => a.name.localeCompare(b.name));

            setRawContainers(rows);
            setLoading(false);
        };

        seed();
        return () => {
            cancelled = true;
        };
    }, [selectedHost, dockerService, selectedPage]);

    // Optimistic Client-Side Sorting
    // This useMemo provides the INSTANT sort feedback to the UI.
    // It runs immediately whenever `rawContainers` or the sort state changes.
    useEffect(() => {
        if (resort.current) {
            // sort and let the server handle subsequent sorts until order is changed
            resort.current = false
            const key = sortFieldToKeyMap[sortField];
            const res = [...rawContainers].sort((a, b) => {
                const valA = a[key];
                const valB = b[key];
                let comparison = 0;

                if (typeof valA === 'bigint' && typeof valB === 'bigint') {
                    if (valA < valB) comparison = -1;
                    if (valA > valB) comparison = 1;
                } else if (typeof valA === 'number' && typeof valB === 'number') {
                    comparison = valA - valB;
                } else {
                    comparison = String(valA).localeCompare(String(valB));
                }

                return sortOrder === ORDER.ASC ? comparison : -comparison;
            })
            setRawContainers(res)
        }
    }, [rawContainers, sortField, sortOrder]);


    const handleSortChange = useCallback((newField: SORT_FIELD, newOrderBy: ORDER) => {
        userSorted.current = true
        setSortField(newField)
        setSortOrder(newOrderBy)
        // immediate resort for ui
        resort.current = true
    }, []);

    return {
        containers: rawContainers,
        history: statHistories,
        pollInfo,
        loading,
        sortField,
        sortOrder,
        handleSortChange,
        setRefreshInterval,
        refreshInterval,
    };
}
