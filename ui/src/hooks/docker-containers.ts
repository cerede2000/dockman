import {useCallback, useEffect, useState} from 'react'
import {callRPC, useHostClient} from '../lib/api.ts'
import {DockerService, type ListResponse} from '../gen/docker/v1/docker_pb.ts'
import {useSnackbar} from "./snackbar.ts"
import {useDockerEvents} from "./docker-events.ts";
import {FAST_POLL_MS, IDLE_POLL_MS, isSettling} from "./container-freshness.ts";
import {pollWhileVisible} from "./visibility.ts";

export function useDockerContainers() {
    const dockerService = useHostClient(DockerService)
    const {showWarning} = useSnackbar()
    // container lifecycle events drive the refresh; polling is a safety net
    const eventBump = useDockerEvents()

    const [containers, setContainers] = useState<ListResponse | null>(null)
    const [loading, setLoading] = useState(true)
    const [refreshInterval, setRefreshInterval] = useState(30000)

    const fetchContainers = useCallback(async () => {
        const {val, err} = await callRPC(() => dockerService.containerList({}))
        if (err) {
            showWarning(`Failed to refresh containers: ${err}`)
            setContainers(null)
            return
        }

        setContainers(val)
    }, [dockerService, showWarning])

    const refreshContainers = useCallback(() => {
        fetchContainers().finally(() => setLoading(false))
    }, [fetchContainers]);

    useEffect(() => {
        setLoading(true)
        fetchContainers().then(() => {
            setLoading(false)
        })
    }, [fetchContainers]) // run only once on page load

    // fast cadence while containers settle (start, health checks, restarts),
    // slow safety net once stable
    useEffect(() => {
        const fast = (containers?.list ?? []).some(c => isSettling(c.state, c.health, c.created));
        setRefreshInterval(fast ? FAST_POLL_MS : IDLE_POLL_MS);
    }, [containers])

    // The container events stream is the primary refresh; this interval is the
    // safety net behind it. It runs only while the tab is visible - a hidden
    // tab has nobody to show a stale state to - and refreshes at once when the
    // tab comes back.
    useEffect(() => pollWhileVisible(() => void fetchContainers(), refreshInterval),
        [fetchContainers, refreshInterval, eventBump])

    return {containers, loading, refreshContainers, fetchContainers, refreshInterval, setRefreshInterval}
}
