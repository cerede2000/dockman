import {useCallback, useEffect, useMemo, useState} from "react";
import {
    Alert, AlertTitle, Box, Button, Chip, CircularProgress, Divider, IconButton, Paper, Stack,
    Table, TableBody, TableCell, TableContainer, TableHead, TableRow, TextField, Tooltip, Typography,
} from "@mui/material";
import {ContentCopy, HealthAndSafetyOutlined, Refresh, VpnKeyOutlined} from "@mui/icons-material";
import {getBaseUrl} from "../../lib/api.ts";
import {useHostManager} from "../../context/host-context.tsx";
import {useHostStore} from "../compose/state/files.ts";
import {useSnackbar} from "../../hooks/snackbar.ts";

interface StackOption {
    path: string;
    alias: string;
    manifests: string[];
}

interface CatalogStack extends StackOption {
    mode: string;
}

interface SecretCatalog {
    stacks: CatalogStack[];
}

interface SOPSStatus {
    recipient?: string;
    recoveryScript?: string;
}

const shellQuote = (value: string) => `'${value.replaceAll("'", `'"'"'`)}'`;

export default function TabRescue() {
    const selectedHost = useHostStore(state => state.host);
    const setHost = useHostStore(state => state.setHost);
    const {availableHosts} = useHostManager();
    const host = selectedHost || "local";
    const {showError, showSuccess} = useSnackbar();

    const [stacks, setStacks] = useState<CatalogStack[]>([]);
    const [recipient, setRecipient] = useState("");
    const [loading, setLoading] = useState(false);
    const [hostStackRoot, setHostStackRoot] = useState("/server/stacks");
    const [hostAgeKey, setHostAgeKey] = useState("/etc/dockman-secrets/age-key.txt");

    const base = useMemo(() => `${getBaseUrl("host", host)}/secrets`, [host]);
    const encrypted = useMemo(() => stacks.filter(stack => stack.mode === "encrypted"), [stacks]);

    const load = useCallback(async () => {
        setLoading(true);
        await (async () => { try {
            const response = await fetch(`${base}/catalog`);
            if (!response.ok) throw new Error(await response.text());
            const catalog = await response.json() as SecretCatalog;
            setStacks(catalog.stacks ?? []);
            const first = (catalog.stacks ?? []).find(stack => stack.mode === "encrypted");
            if (!first) {
                setRecipient("");
                return;
            }
            const status = await fetch(`${base}/sops?stack=${encodeURIComponent(first.path)}`);
            setRecipient(status.ok ? ((await status.json() as SOPSStatus).recipient ?? "") : "");
        } catch (error) {
            setStacks([]);
            showError(`Unable to build the rescue inventory: ${(error as Error).message}`);
        } })().finally(() => setLoading(false));
    }, [base, showError]);

    useEffect(() => { void load(); }, [load]);

    // Everything below is generated from what this host already reports, so the
    // script is never out of step with the stacks it is meant to bring back.
    const bootstrapScript = useMemo(() => {
        const root = hostStackRoot.trim() || "/server/stacks";
        const key = hostAgeKey.trim() || "/etc/dockman-secrets/age-key.txt";
        const keyDirectory = key.includes("/") ? key.slice(0, key.lastIndexOf("/")) || "/" : ".";
        const list = encrypted.length > 0
            ? encrypted.map(stack => shellQuote(stack.path.split("/").slice(1).join("/") || stack.path)).join(" ")
            : "";
        return `#!/bin/sh
# Dockman rescue kit — bring every encrypted stack back up on a fresh host.
# Nothing here needs Dockman: only Docker, SOPS, this script, the stack
# directory and the age identity you backed up separately.
set -eu

STACK_ROOT=${shellQuote(root)}
AGE_KEY=${shellQuote(key)}
AGE_BACKUP="\${1:-./age-key.txt}"

# 1. Packages. Docker's convenience script covers Debian, Ubuntu, Fedora and
#    friends; adjust if your distribution is not one of them.
if ! command -v docker >/dev/null 2>&1; then
  curl -fsSL https://get.docker.com | sh
  systemctl enable --now docker
fi
if ! command -v sops >/dev/null 2>&1; then
  SOPS_VERSION=v3.9.4
  ARCH=$(uname -m); case "$ARCH" in x86_64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; esac
  curl -fsSL -o /usr/local/bin/sops \\
    "https://github.com/getsops/sops/releases/download/\${SOPS_VERSION}/sops-\${SOPS_VERSION}.linux.\${ARCH}"
  chmod 0755 /usr/local/bin/sops
fi

# 2. The age identity. This is the one artefact whose loss is final: no copy,
#    no recovery. Restore it from your own backup, never from the stack tree.
[ -f "$AGE_BACKUP" ] || { echo "age identity not found at $AGE_BACKUP" >&2; exit 66; }
install -d -m 0700 ${shellQuote(keyDirectory)}
install -m 0600 "$AGE_BACKUP" "$AGE_KEY"
export SOPS_AGE_KEY_FILE="$AGE_KEY"
${recipient ? `# Expected recipient: ${recipient}
# Verify the restored identity matches it before going further.
` : ""}
# 3. Restore the stack directory at "$STACK_ROOT" before running the rest -
#    from your backup, or by cloning the Git repository Dockman synchronizes.
[ -d "$STACK_ROOT" ] || { echo "restore the stack directory at $STACK_ROOT first" >&2; exit 66; }

# 4. Bring each encrypted stack up through its own recovery script. Each one
#    decrypts its secrets itself and mounts them in a tmpfs when run as root.
cd "$STACK_ROOT"
for stack in ${list || '"$@"'}; do
  if [ ! -x "$stack/compose-sops.sh" ]; then
    echo "skipping $stack: no recovery script" >&2
    continue
  fi
  echo "== $stack"
  ( cd "$stack" && ./compose-sops.sh up )
done

echo
echo "Every encrypted stack is up. Dockman is not required and has not been installed."
echo "To manage them from Dockman again, start it and use Settings > Secrets > Host boot wizard"
echo "so that secrets are remounted automatically at every boot."
`;
    }, [encrypted, hostAgeKey, hostStackRoot, recipient]);

    const copyScript = async () => {
        await (async () => { try {
            if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(bootstrapScript);
            else {
                const area = document.createElement("textarea");
                area.value = bootstrapScript;
                area.style.position = "fixed";
                area.style.opacity = "0";
                document.body.appendChild(area);
                area.select();
                document.execCommand("copy");
                area.remove();
            }
            showSuccess("Rescue script copied.");
        } catch (error) {
            showError(`Unable to copy: ${(error as Error).message}`);
        } })();
    };

    return <Box sx={{p: 3, maxWidth: 1400, mx: "auto"}}>
        <Stack direction={{xs: "column", md: "row"}} spacing={2} sx={{justifyContent: "space-between", mb: 2}}>
            <Box>
                <Stack direction="row" spacing={1} sx={{alignItems: "center"}}>
                    <HealthAndSafetyOutlined color="success"/>
                    <Typography variant="h5" sx={{fontWeight: 800}}>Rescue kit</Typography>
                </Stack>
                <Typography variant="body2" color="text.secondary">
                    Everything needed to bring this host&apos;s stacks back on a machine that has only Docker and SOPS — without Dockman.
                </Typography>
            </Box>
            <Stack direction="row" spacing={1} sx={{alignItems: "center"}}>
                <TextField select size="small" label="Host" value={host} sx={{minWidth: 180}}
                           slotProps={{select: {native: true}}}
                           onChange={event => setHost(event.target.value)}>
                    {(availableHosts.length > 0 ? availableHosts : [host]).map(name =>
                        <option key={name} value={name}>{name}</option>)}
                </TextField>
                <Tooltip title="Refresh inventory"><span>
                    <IconButton disabled={loading} onClick={() => void load()}>{loading ? <CircularProgress size={18}/> : <Refresh/>}</IconButton>
                </span></Tooltip>
            </Stack>
        </Stack>

        <Alert severity="error" icon={<VpnKeyOutlined/>} sx={{mb: 2}}>
            <AlertTitle>The age identity is the single point of failure</AlertTitle>
            Every encrypted secret on this host is recoverable with it, and none without it. Keep a copy
            somewhere that does not depend on this machine, on Dockman, or on the stack directory it
            protects — a password manager or an offline medium.
            {recipient && <> The stacks below are encrypted for <code>{recipient}</code>; check your backup matches
                that recipient.</>}
        </Alert>

        <Paper variant="outlined" sx={{p: 2, mb: 2}}>
            <Typography variant="h6" sx={{fontWeight: 750, mb: .5}}>What you must have off this machine</Typography>
            <Typography variant="body2" color="text.secondary" sx={{mb: 1.5}}>
                Losing the host is survivable; losing any of these three is not.
            </Typography>
            <Stack spacing={1}>
                <Box><Chip size="small" color="error" label="1"/> <strong>The age identity</strong> — without it the
                    ciphertext is noise. Backed up separately, never inside the stack directory.</Box>
                <Box><Chip size="small" color="warning" label="2"/> <strong>The stack directory</strong> — compose
                    files, <code>secrets.sops.yaml</code> and <code>compose-sops.sh</code> per stack. A Git remote
                    counts, provided the encrypted sources are synchronized to it.</Box>
                <Box><Chip size="small" label="3"/> <strong>Dockman&apos;s database</strong> — optional. It restores
                    Dockman&apos;s own configuration (hosts, policies, Git bindings). No stack needs it to run.</Box>
            </Stack>
        </Paper>

        <Paper variant="outlined" sx={{p: 2, mb: 2}}>
            <Stack direction={{xs: "column", sm: "row"}} spacing={1} sx={{alignItems: {sm: "center"}, mb: 1.5}}>
                <Box sx={{flex: 1}}>
                    <Typography variant="h6" sx={{fontWeight: 750}}>Encrypted stacks on {host}</Typography>
                    <Typography variant="body2" color="text.secondary">
                        Each one carries its own recovery script and can be started on its own.
                    </Typography>
                </Box>
            </Stack>
            {encrypted.length === 0
                ? <Alert severity="info">No encrypted stack on this host yet. Stacks in migration mode keep their
                    secrets as plaintext files and need no key to restore — but nothing protects them at rest either.</Alert>
                : <TableContainer sx={{maxHeight: 320}}>
                    <Table stickyHeader size="small">
                        <TableHead><TableRow><TableCell>Stack</TableCell><TableCell>Alias</TableCell><TableCell>Manifests</TableCell></TableRow></TableHead>
                        <TableBody>{encrypted.map(stack => <TableRow key={stack.path} hover>
                            <TableCell sx={{fontFamily: "monospace"}}>{stack.path}</TableCell>
                            <TableCell>{stack.alias}</TableCell>
                            <TableCell>{stack.manifests.join(", ")}</TableCell>
                        </TableRow>)}</TableBody>
                    </Table>
                </TableContainer>}
        </Paper>

        <Paper variant="outlined" sx={{p: 2}}>
            <Typography variant="h6" sx={{fontWeight: 750, mb: .5}}>Fresh-host bootstrap script</Typography>
            <Typography variant="body2" color="text.secondary" sx={{mb: 1.5}}>
                Keep a copy next to your backups. Run it as root on the new machine, passing the path to the
                restored age identity: <code>./rescue.sh ./age-key.txt</code>
            </Typography>
            <Stack direction={{xs: "column", sm: "row"}} spacing={1} sx={{mb: 1.5}}>
                <TextField size="small" fullWidth label="Stack root on the host" value={hostStackRoot}
                           onChange={event => setHostStackRoot(event.target.value)}/>
                <TextField size="small" fullWidth label="age identity path on the host" value={hostAgeKey}
                           onChange={event => setHostAgeKey(event.target.value)}/>
            </Stack>
            <Divider sx={{mb: 1.5}}/>
            <Box sx={{position: "relative"}}>
                <Tooltip title="Copy script"><IconButton size="small" onClick={() => void copyScript()}
                                                         sx={{position: "absolute", top: 4, right: 4}}><ContentCopy fontSize="small"/></IconButton></Tooltip>
                <Box component="pre" sx={{
                    m: 0, p: 2, borderRadius: 1, bgcolor: "action.hover", fontSize: 12,
                    overflowX: "auto", maxHeight: 420, whiteSpace: "pre",
                }}>{bootstrapScript}</Box>
            </Box>
            <Stack direction="row" spacing={1} sx={{mt: 1.5}}>
                <Button variant="contained" startIcon={<ContentCopy/>} onClick={() => void copyScript()}>Copy script</Button>
            </Stack>
            <Alert severity="info" sx={{mt: 1.5}}>
                The script stops short of installing Dockman on purpose: the stacks must come back without it.
                Once they are running, start Dockman however you like and use <strong>Settings → Secrets → Host boot
                wizard</strong> to reinstall the boot-time runtime, so secrets are remounted automatically at every reboot.
            </Alert>
        </Paper>
    </Box>;
}
