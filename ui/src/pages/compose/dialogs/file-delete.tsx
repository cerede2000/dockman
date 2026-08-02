import Button from '@mui/material/Button'
import Dialog from '@mui/material/Dialog'
import DialogActions from '@mui/material/DialogActions'
import DialogTitle from '@mui/material/DialogTitle'
import {Alert, Box, CircularProgress, Stack, Typography} from "@mui/material"
import {create} from "zustand";
import {useFiles} from "../../../context/file-context.tsx";
import {useHostStore} from "../state/files.ts";
import {reconcileDeletedGitFile, refreshGitStackStatuses, useGitTrackedFileInfo} from "../../../components/git-stack-status-store.ts";
import {withProtectedAPI} from "../../../lib/api.ts";
import {useSnackbar} from "../../../hooks/snackbar.ts";
import {useState} from "react";

export const useFileDelete = create<{
    fileToDelete: string;
    close: () => void;
    open: (filename: string) => void;
}>(set => ({
    fileToDelete: "",
    close: () => {
        set({fileToDelete: ""})
    },
    open: (filename: string) => {
        set({fileToDelete: filename})
    }
}))

const FileDelete = () => {
    const fileToDelete = useFileDelete(state => state.fileToDelete)
    const onClose = useFileDelete(state => state.close)

    const {deleteFile} = useFiles()
    const host = useHostStore(state => state.host)
    const gitFile = useGitTrackedFileInfo(host, fileToDelete)
    const {showError, showSuccess} = useSnackbar()
    const [busy, setBusy] = useState(false)

    const onCancel = () => {
        onClose()
    }

    const onDelete = async (deleteFromGit = false) => {
        if (!fileToDelete || busy) return
        setBusy(true)
        try {
            if (!await deleteFile(fileToDelete)) return
            if (deleteFromGit) {
                if (!gitFile?.bindingId || !gitFile.composePath || !gitFile.relativePath) {
                    throw new Error('The Git Folder Link for this file is no longer available. Its local deletion is preserved and can be resolved from the stack synchronization popup.')
                }
                const encodedCompose = gitFile.composePath.split('/').map(encodeURIComponent).join('/')
                const response = await fetch(withProtectedAPI(`/git/bindings/${gitFile.bindingId}/local-deletion/${encodedCompose}`), {
                    method: 'POST', headers: {'Content-Type': 'application/json'},
                    body: JSON.stringify({action: 'delete_git', path: gitFile.relativePath, confirmation: 'DELETE FILE FROM GIT'}),
                })
                if (!response.ok) {
                    let message = `Git deletion failed (${response.status})`
                    try {
                        const body = await response.json() as {error?: string; message?: string}
                        message = body.error || body.message || message
                    } catch { /* keep the bounded fallback */ }
                    throw new Error(`${message}. The local deletion is preserved: restore it, exclude it, or retry the Git deletion from the stack synchronization popup.`)
                }
                const result = await response.json() as {message: string}
                await refreshGitStackStatuses(host)
                showSuccess(result.message)
            } else if (gitFile?.tracked && gitFile.mutable) {
                // Re-evaluate the exact stack immediately. This removes a
                // one-file rule created from the context menu, preserves broad
                // rules, and clears false "push" states when Git never had it.
                await reconcileDeletedGitFile(host, gitFile)
            }
            onClose()
        } catch (reason) {
            showError((reason as Error).message)
        } finally {
            setBusy(false)
        }
    }

    return (
        <Dialog
            open={!!fileToDelete}
            onClose={onCancel}
            slotProps={{
                transition: {
                    onExited: onClose
                },
                paper: {
                    sx: {
                        backgroundColor: "#000000",
                        color: "#d6d6d6",
                        borderRadius: 3,
                        border: "2px solid #444",
                        p: 2
                    }
                }
            }}
        >
            <DialogTitle sx={{
                border: "3px solid #444",
                borderRadius: 1,
                p: 3,
            }}>
                Delete
                <Box component="span" sx={{
                    color: "#ff6b6b",
                    pl: 1,
                }}>
                    {fileToDelete}
                </Box>
            </DialogTitle>

            {gitFile?.tracked && gitFile.mutable && <Alert severity="warning" sx={{mt: 2, maxWidth: 560}}>
                <Typography variant="body2">This file is synchronized with Git. You can preserve the Git copy, or delete it locally and commit the same deletion to Git in one operation.</Typography>
            </Alert>}

            <DialogActions sx={{pt: 3, flexWrap: 'wrap'}}>
                <Button
                    onClick={onCancel}
                    disabled={busy}
                    variant="outlined"
                    sx={{
                        borderColor: "#666",
                        color: "#fff",
                        borderRadius: 2,
                        "&:hover": {
                            borderColor: "#888",
                            backgroundColor: "#2a2a2a"
                        }
                    }}
                >
                    Cancel
                </Button>

                <Stack direction="row" spacing={1} sx={{ml: 'auto'}}>
                <Button
                    onClick={() => void onDelete(false)}
                    variant="outlined"
                    color="error"
                    disabled={busy}
                    sx={{
                        borderColor: "#ff4d4d",
                        borderRadius: 2,
                        "&:hover": {
                            borderColor: "#ff6666",
                            backgroundColor: "rgba(255,77,77,0.1)"
                        }
                    }}
                >
                    {busy ? <CircularProgress size={16}/> : gitFile?.tracked ? 'Delete locally only' : 'Delete'}
                </Button>
                {gitFile?.tracked && gitFile.mutable && <Button onClick={() => void onDelete(true)} variant="contained" color="error" disabled={busy}>
                    {busy ? <CircularProgress size={16}/> : 'Delete locally and from Git'}
                </Button>}
                </Stack>
            </DialogActions>
        </Dialog>
    )
}

export default FileDelete
