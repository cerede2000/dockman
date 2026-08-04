import {Alert, Box, Button, Chip, CircularProgress, Stack, Typography} from "@mui/material";
import {Refresh, RestartAlt, SystemUpdateAlt} from "@mui/icons-material";
import {useState} from "react";
import {getBaseUrl} from "../../lib/api.ts";
import {useSnackbar} from "../../hooks/snackbar.ts";

export default function TabDockman() {
    const {showError, showSuccess} = useSnackbar();
    const [updating, setUpdating] = useState(false);
    const [restarting, setRestarting] = useState(false);
    const [checking, setChecking] = useState(false);
    const [updateCheck, setUpdateCheck] = useState<{
        status: "available" | "current" | "skipped" | "error";
        image: string;
        currentDigest?: string;
        remoteDigest?: string;
        reason?: string;
    } | null>(null);
    const busy = updating || restarting || checking;

    const handleCheck = async () => {
        setChecking(true);
        try {
            const res = await fetch(`${getBaseUrl("host", "local")}/docker/update/dockman/check`);
            if (!res.ok) {
                showError(`Update check failed: ${res.status} ${await res.text()}`);
                return;
            }
            setUpdateCheck(await res.json());
        } catch (e) {
            showError(`Update check failed: ${(e as Error).message}`);
        } finally {
            setChecking(false);
        }
    };

    const handleUpdate = async () => {
        const ok = window.confirm(
            "Pull the latest Dockman image and recreate the container?\n\n" +
            "Dockman will briefly go offline while it restarts."
        );
        if (!ok) return;

        setUpdating(true);
        try {
            const res = await fetch(`${getBaseUrl("host", "local")}/docker/update/dockman`, {
                method: "POST",
            });
            if (!res.ok) {
                showError(`Update failed: ${res.status} ${await res.text()}`);
                return;
            }
            showSuccess("Update started — Dockman will restart shortly.");
        } catch (e) {
            showError(`Update failed: ${(e as Error).message}`);
        } finally {
            setUpdating(false);
        }
    };

    const handleRestart = async () => {
        const ok = window.confirm(
            "Restart the Dockman container now?\n\n" +
            "Dockman will briefly go offline. No image will be pulled."
        );
        if (!ok) return;

        setRestarting(true);
        try {
            const res = await fetch(`${getBaseUrl("host", "local")}/docker/restart/dockman`, {
                method: "POST",
            });
            if (!res.ok) {
                showError(`Restart failed: ${res.status} ${await res.text()}`);
                return;
            }
            showSuccess("Restart scheduled — Dockman will be back shortly.");
        } catch (e) {
            showError(`Restart failed: ${(e as Error).message}`);
        } finally {
            setRestarting(false);
        }
    };

    return (
        <Box sx={{display: "flex", justifyContent: "center", p: 4}}>
            <Box sx={{
                width: "100%",
                maxWidth: 640,
                border: "1px solid",
                borderColor: "divider",
                borderRadius: 2,
                p: 3,
            }}>
                <Typography variant="h6" gutterBottom>Dockman maintenance</Typography>
                <Typography
                    variant="body2"
                    sx={{
                        color: "text.secondary",
                        mb: 2
                    }}>
                    Restart the current container, or pull the latest Dockman image and
                    recreate it through a short-lived helper. In both cases Dockman is
                    briefly unavailable and your compose configuration is reused as-is.
                </Typography>
                {updateCheck && <Alert
                    severity={updateCheck.status === "available" ? "warning" : updateCheck.status === "current" ? "success" : updateCheck.status === "error" ? "error" : "info"}
                    sx={{mb: 2}}
                    action={<Chip
                        size="small"
                        color={updateCheck.status === "available" ? "warning" : updateCheck.status === "current" ? "success" : updateCheck.status === "error" ? "error" : "default"}
                        label={updateCheck.status === "available" ? "Update available" : updateCheck.status === "current" ? "Up to date" : updateCheck.status}
                    />}
                >
                    <Typography variant="body2" sx={{fontWeight: 600}}>{updateCheck.image}</Typography>
                    {updateCheck.reason && <Typography variant="caption" component="div">{updateCheck.reason}</Typography>}
                    {updateCheck.currentDigest && <Typography variant="caption" component="div" sx={{fontFamily: "monospace"}}>Current: sha256:{updateCheck.currentDigest.slice(0, 12)}</Typography>}
                    {updateCheck.remoteDigest && <Typography variant="caption" component="div" sx={{fontFamily: "monospace"}}>Remote: sha256:{updateCheck.remoteDigest.slice(0, 12)}</Typography>}
                </Alert>}
                <Stack direction={{xs: "column", sm: "row"}} spacing={1.25}>
                    <Button
                        variant="outlined"
                        onClick={() => void handleCheck()}
                        disabled={busy}
                        startIcon={checking
                            ? <CircularProgress size={16} color="inherit"/>
                            : <Refresh/>}
                    >
                        {checking ? "Checking…" : "Check for update"}
                    </Button>
                    <Button
                        variant="outlined"
                        onClick={handleRestart}
                        disabled={busy}
                        startIcon={restarting
                            ? <CircularProgress size={16} color="inherit"/>
                            : <RestartAlt/>}
                    >
                        {restarting ? "Scheduling restart…" : "Restart Dockman"}
                    </Button>
                    <Button
                        variant="contained"
                        onClick={handleUpdate}
                        disabled={busy}
                        startIcon={updating
                            ? <CircularProgress size={16} color="inherit"/>
                            : <SystemUpdateAlt/>}
                    >
                        {updating ? "Starting update…" : "Update Dockman"}
                    </Button>
                </Stack>
            </Box>
        </Box>
    );
}
