import Button from '@mui/material/Button'
import Dialog from '@mui/material/Dialog'
import DialogActions from '@mui/material/DialogActions'
import DialogTitle from '@mui/material/DialogTitle'
import {Alert, Box, CircularProgress, Stack, TextField, Typography} from "@mui/material"
import {create} from "zustand";
import {useFiles} from "../../../context/file-context.tsx";
import {useHostStore} from "../state/files.ts";
import {reconcileDeletedGitFile, refreshGitStackStatuses, refreshGitTrackedFile, useGitTrackedFileInfo} from "../../../components/git-stack-status-store.ts";
import {withProtectedAPI} from "../../../lib/api.ts";
import {useSnackbar} from "../../../hooks/snackbar.ts";
import {useEffect, useState} from "react";

interface FolderDeletionState {
    bindingId: string; host: string; stackPath: string; repositoryName: string; repositoryBranch: string;
    stackCount: number; state: string; localChanges: number; gitChanges: number; conflicts: number; unreadableLocal: number;
}

export const useFileDelete = create<{
    fileToDelete: string;
    isDir: boolean;
    close: () => void;
    open: (filename: string, isDir?: boolean) => void;
}>(set => ({
    fileToDelete: "",
    isDir: false,
    close: () => {
        set({fileToDelete: "", isDir: false})
    },
    open: (filename: string, isDir = false) => {
        set({fileToDelete: filename, isDir})
    }
}))

const FileDelete = () => {
    const fileToDelete = useFileDelete(state => state.fileToDelete)
    const isDir = useFileDelete(state => state.isDir)
    const onClose = useFileDelete(state => state.close)

    const {deleteFile, listFiles} = useFiles()
    const host = useHostStore(state => state.host)
    const gitFile = useGitTrackedFileInfo(host, fileToDelete)
    const {showError, showSuccess} = useSnackbar()
    const [busy, setBusy] = useState(false)
    const [folderState, setFolderState] = useState<FolderDeletionState | null>(null)
    const [folderStateError, setFolderStateError] = useState('')
    const [gitDeleteConfirmation, setGitDeleteConfirmation] = useState('')

    const linkedFolderRoot = isDir && gitFile?.folderLinkRoot && Boolean(gitFile.bindingId)

    useEffect(() => {
        setFolderState(null)
        setFolderStateError('')
        setGitDeleteConfirmation('')
        if (!linkedFolderRoot || !gitFile?.bindingId) return
        let active = true
        setBusy(true)
        void fetch(withProtectedAPI(`/git/bindings/${gitFile.bindingId}/folder-deletion`)).then(async (response) => {
            if (!response.ok) {
                const body = await response.json().catch(() => ({})) as {error?: string; message?: string}
                throw new Error(body.error || body.message || `Folder Link verification failed (${response.status})`)
            }
            return response.json() as Promise<FolderDeletionState>
        }).then((state) => { if (active) setFolderState(state) }).catch((reason) => {
            if (active) setFolderStateError((reason as Error).message)
        }).finally(() => { if (active) setBusy(false) })
        return () => { active = false }
    }, [gitFile?.bindingId, linkedFolderRoot])

    const onCancel = () => {
        onClose()
    }

    const onDelete = async (deleteFromGit = false) => {
        if (!fileToDelete || busy) return
        if (linkedFolderRoot) return
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
                // The local delete schedules an intermediate status read. The
                // authoritative refresh is chained after it so the committed
                // Git deletion cannot leave a stale orange state in the UI.
                await refreshGitTrackedFile(host, gitFile.path)
                await refreshGitStackStatuses(host, true)
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

    const deleteLinkedFolder = async (action: 'preserve_git' | 'sync_git' | 'delete_git') => {
        if (!gitFile?.bindingId || busy) return
        setBusy(true)
        try {
            const response = await fetch(withProtectedAPI(`/git/bindings/${gitFile.bindingId}/folder-deletion`), {
                method: 'POST', headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({
                    action,
                    confirmation: action === 'delete_git' ? gitDeleteConfirmation : 'DELETE LOCAL LINKED FOLDER',
                }),
            })
            if (!response.ok) {
                const body = await response.json().catch(() => ({})) as {error?: string; message?: string}
                throw new Error(body.error || body.message || `Folder deletion failed (${response.status})`)
            }
            const result = await response.json() as {message: string}
            showSuccess(result.message)
            await listFiles('', [])
            await refreshGitStackStatuses(host, true)
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

            {linkedFolderRoot && <Stack spacing={1.5} sx={{mt: 2, maxWidth: 680}}>
                <Alert severity="error">This directory is the root of a Git Folder Link. It cannot be deleted as an ordinary folder. Dockman will verify Git first and remove the Folder Link with it.</Alert>
                {busy && !folderState && !folderStateError && <Box sx={{display: 'flex', justifyContent: 'center', py: 2}}><CircularProgress size={24}/></Box>}
                {folderStateError && <Alert severity="error">Git consistency could not be verified: {folderStateError}. Deletion is blocked.</Alert>}
                {folderState && <Alert severity={folderState.state === 'up_to_date' ? 'success' : folderState.conflicts || folderState.unreadableLocal ? 'error' : 'warning'}>
                    <Typography variant="body2"><strong>{folderState.repositoryName}</strong> · {folderState.repositoryBranch} · {folderState.stackCount} stack{folderState.stackCount === 1 ? '' : 's'}</Typography>
                    <Typography variant="body2">State: {folderState.state.replaceAll('_', ' ')} · local changes: {folderState.localChanges} · Git changes: {folderState.gitChanges} · conflicts: {folderState.conflicts}</Typography>
                    {folderState.unreadableLocal > 0 && <Typography variant="body2">{folderState.unreadableLocal} synchronized local item(s) cannot be read.</Typography>}
                </Alert>}
                {folderState && <Alert severity="info">Choose whether Git must first receive readable local changes, remain untouched, or have the synchronized folder content deleted as well. No container or stack deployment is performed.</Alert>}
                {folderState && <TextField size="small" label='Type DELETE FOLDER FROM GIT to enable Git deletion' value={gitDeleteConfirmation} onChange={(event) => setGitDeleteConfirmation(event.target.value)} fullWidth/>}
            </Stack>}

            {!linkedFolderRoot && gitFile?.tracked && gitFile.mutable && <Alert severity="warning" sx={{mt: 2, maxWidth: 560}}>
                <Typography variant="body2">This file is synchronized with Git. You can preserve the Git copy, or delete it locally and commit the same deletion to Git in one operation.</Typography>
            </Alert>}

            <DialogActions sx={{pt: 3, flexWrap: 'wrap', gap: 1}}>
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

                {linkedFolderRoot ? <Stack direction={{xs: 'column', sm: 'row'}} spacing={1} sx={{ml: 'auto'}}>
                    <Button variant="outlined" color="warning" disabled={busy || !folderState} onClick={() => void deleteLinkedFolder('preserve_git')}>Keep Git · delete local & unlink</Button>
                    <Button variant="contained" color="primary" disabled={busy || !folderState || folderState.conflicts > 0 || folderState.unreadableLocal > 0} onClick={() => void deleteLinkedFolder('sync_git')}>Update Git · delete local & unlink</Button>
                    <Button variant="contained" color="error" disabled={busy || !folderState || gitDeleteConfirmation !== 'DELETE FOLDER FROM GIT'} onClick={() => void deleteLinkedFolder('delete_git')}>Delete local, Git & link</Button>
                </Stack> : <Stack direction="row" spacing={1} sx={{ml: 'auto'}}>
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
                </Stack>}
            </DialogActions>
        </Dialog>
    )
}

export default FileDelete
