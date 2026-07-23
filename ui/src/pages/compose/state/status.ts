import {immer} from "zustand/middleware/immer";
import {getContextKey} from "../../../context/tab-context.tsx";
import {type Status, StatusSchema} from "../../../gen/docker/v1/docker_pb.ts";
import {create as createMessage, equals} from "@bufbuild/protobuf";
import {create} from "zustand";

interface OpenFilesState {
    // contextKey -> Set of directory paths
    openFiles: Record<string, Record<string, Status>>;
    // Runtime index returned from Docker labels. Unlike openFiles, this is not
    // pruned when folders collapse, so nested stacks continue to be monitored.
    knownFiles: Record<string, Record<string, Status>>;
    // Aggregate of the indexed compose statuses for every ancestor. This lets
    // a folder reflect stacks located several levels below it while collapsed.
    folderStatuses: Record<string, Record<string, Status>>;
    delete: (dir: string, keep?: string) => void;
    trackComposeStatus: (path: string) => void;
    setStatus: (status: { [p: string]: Status }, contextKey?: string) => void
}

export const useComposeFileState = create<OpenFilesState>()(
    immer((set) => ({
        openFiles: {},
        knownFiles: {},
        folderStatuses: {},

        delete: (dir: string, keep?: string) => {
            const key = getContextKey();
            set((state) => {
                const openStatuses = state.openFiles[key];
                if (!openStatuses) {
                    return
                }

                for (const trackingFile of Object.keys(openStatuses)) {
                    // keep lets a collapsed stack folder retain its own compose
                    // status (so its dot stays visible) while dropping the files
                    // nested inside it.
                    if (trackingFile !== keep && trackingFile.startsWith(dir)) {
                        delete state.openFiles[key][trackingFile];
                    }
                }
            });
        },

        trackComposeStatus: (path: string) => {
            const key = getContextKey();
            set((state) => {
                if (!state.openFiles[key]) {
                    state.openFiles[key] = <Record<string, Status>>{};
                }

                // Only initialise when not already tracked. Re-tracking a path
                // (e.g. when a folder is expanded and its compose child mounts)
                // must not reset an already-known status to empty, otherwise the
                // dot flickers to grey until the next poll.
                if (!state.openFiles[key][path]) {
                    state.openFiles[key][path] = state.knownFiles[key]?.[path]
                        ?? createMessage(StatusSchema);
                }
            });
        },

        setStatus(input: { [p: string]: Status }, contextKey?: string) {
            set((state) => {
                const key = contextKey ?? getContextKey();

                if (!state.openFiles[key]) {
                    state.openFiles[key] = {};
                }

                // ComposeFileStatus also returns every deployed stack found in
                // Docker labels. Keep only the alias displayed by this tree and
                // replace its index so removed containers cannot leave stale dots.
                const alias = key.slice(key.lastIndexOf('/') + 1);
                const prefix = `${alias}/`;
                const previousKnown = state.knownFiles[key] ?? {};
                const nextKnown: Record<string, Status> = {};
                for (const [file, value] of Object.entries(input)) {
                    if (file !== alias && !file.startsWith(prefix)) continue;
                    const previous = previousKnown[file];
                    nextKnown[file] = previous && equals(StatusSchema, previous as Status, value)
                        ? previous as Status
                        : value;
                }
                const knownChanged = Object.keys(previousKnown).length !== Object.keys(nextKnown).length
                    || Object.entries(nextKnown).some(([file, value]) => previousKnown[file] !== value);
                if (knownChanged) {
                    state.knownFiles[key] = nextKnown;
                }

                for (const [file, value] of Object.entries(input)) {
                    // Only update tracked files whose status ACTUALLY changed:
                    // blindly assigning fresh message objects gives openFiles a
                    // new identity on every poll, which re-renders every dot —
                    // and re-triggers any effect depending on the store.
                    const existing = state.openFiles[key][file];
                    if (existing && !equals(StatusSchema, existing as Status, value)) {
                        state.openFiles[key][file] = value;
                    }
                }

                const aggregates: Record<string, {servicesUp: number; servicesDown: number; servicesHealthy: number; servicesUnHealthy: number}> = {};
                const knownStatuses = knownChanged ? nextKnown : previousKnown;
                for (const [file, status] of Object.entries(knownStatuses)) {
                    const parts = file.replaceAll('\\', '/').split('/').filter(Boolean);
                    parts.pop();
                    while (parts.length > 0) {
                        const directory = parts.join('/');
                        const aggregate = aggregates[directory] ??= {servicesUp: 0, servicesDown: 0, servicesHealthy: 0, servicesUnHealthy: 0};
                        aggregate.servicesUp += status.servicesUp;
                        aggregate.servicesDown += status.servicesDown;
                        aggregate.servicesHealthy += status.servicesHealthy;
                        aggregate.servicesUnHealthy += status.servicesUnHealthy;
                        parts.pop();
                    }
                }
                const previousFolders = state.folderStatuses[key] ?? {};
                const nextFolders: Record<string, Status> = {};
                for (const [directory, aggregate] of Object.entries(aggregates)) {
                    const value = createMessage(StatusSchema, aggregate);
                    const previous = previousFolders[directory];
                    nextFolders[directory] = previous && equals(StatusSchema, previous as Status, value)
                        ? previous as Status
                        : value;
                }
                const foldersChanged = Object.keys(previousFolders).length !== Object.keys(nextFolders).length
                    || Object.entries(nextFolders).some(([directory, value]) => previousFolders[directory] !== value);
                if (foldersChanged) {
                    state.folderStatuses[key] = nextFolders;
                }
            })
        }
    }))
);
