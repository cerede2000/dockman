import {type ReactNode, useCallback, useEffect, useRef, useState} from "react";
import {Typography} from "@mui/material";

export type SaveState = 'idle' | 'typing' | 'saving' | 'success' | 'error'


export type OnSave = (value: string) => Promise<SaveState>
export type SaveCallback = (value: string, onSave: OnSave) => void

interface UseSaveStatusReturn {
    status: SaveState;
    handleContentChange: SaveCallback;
}

export const indicatorMap: Record<SaveState, { color: string, component: ReactNode }> = {
    typing: {
        color: "primary.main",
        component: <Typography variant="button" sx={{
            color: "primary.main"
        }}>Typing</Typography>
    },
    saving: {
        color: "info.main",
        component: <Typography variant="button" sx={{
            color: "info.main"
        }}>Saving</Typography>
    },
    success: {
        color: "success.main",
        component: <Typography variant="button" sx={{
            color: "success.main"
        }}>Saved</Typography>
    },
    error: {
        color: "error.main",
        component: <Typography variant="button" sx={{
            color: "error.main"
        }}>Save Failed</Typography>
    },
    idle: {
        color: "primary.secondary",
        component: <></>
    }
};

export function useSaveStatus(debounceMs: number = 500, filename: string): UseSaveStatusReturn {
    const [status, setStatus] = useState<SaveState>('idle');
    const debounceTimeout = useRef<ReturnType<typeof setTimeout> | null>(null);
    // The editor has no manual save: whatever this debounce is still holding is
    // the only copy of the last keystrokes. Clearing the timer without running
    // it - switching file, closing the tab - dropped them silently.
    const pending = useRef<{ value: string; onSave: OnSave } | null>(null);

    const flushPending = useCallback(() => {
        const queued = pending.current;
        pending.current = null;
        if (debounceTimeout.current) {
            clearTimeout(debounceTimeout.current);
            debounceTimeout.current = null;
        }
        // No status update here on purpose: this runs while the editor is being
        // torn down or already showing another file.
        if (queued) void queued.onSave(queued.value);
    }, []);

    useEffect(() => {
        setStatus('idle');
        return flushPending;
    }, [filename, flushPending]);

    const handleContentChange = useCallback<SaveCallback>((value, onSave) => {
        setStatus('typing');
        pending.current = {value, onSave};

        if (debounceTimeout.current) {
            clearTimeout(debounceTimeout.current);
        }

        debounceTimeout.current = setTimeout(async () => {
            debounceTimeout.current = null;
            pending.current = null;
            setStatus('saving');
            const state = await onSave(value)
            setStatus(state);
        }, debounceMs);
    }, [debounceMs]);

    useEffect(() => {
        if (status === 'success' || status === 'error') {
            const timer = setTimeout(() => {
                setStatus('idle');
            }, 2000);
            return () => clearTimeout(timer);
        }
    }, [status]);

    return {
        status,
        handleContentChange
    };
}
