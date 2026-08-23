import {MonacoEditor} from "./editor.tsx";
import {useCallback, useEffect, useRef, useState} from "react";
import {useSnackbar} from "../../../hooks/snackbar.ts";
import {Alert, AlertTitle, Box, Button, CircularProgress, Dialog, DialogActions, DialogContent, DialogTitle, Link, Typography} from '@mui/material';
import {ErrorOutlined, WarningAmber} from '@mui/icons-material';
import {type SaveState, useSaveStatus} from "../hooks/status-hook.tsx";
import {ErrFileNotSupported} from "../../../context/file-context.tsx";
import {DiffEditor} from "@monaco-editor/react";
import {getLanguageFromExtension} from "../../../lib/editor.ts";
import type {YamlOutlineItem} from "./yaml-outline.ts";

interface TextEditorProps {
    filename: string
    // returns str err
    saveFile: (filename: string, contents: string, revision: string, session: string) => Promise<{ revision: string; err: string; conflict: boolean }>
    getFile: (filename: string) => Promise<{ contents: string; revision: string; err: string }>
    setEditorLease?: (filename: string, session: string, dirty: boolean) => Promise<void>
    editorEventsUrl?: string

    setFileSaveStatus: (status: SaveState) => void
    onOutlineChange?: (items: YamlOutlineItem[]) => void
    registerOutlineNavigation?: (navigate: ((item: YamlOutlineItem) => void) | null) => void
}

function EditorCommon({filename, setFileSaveStatus, saveFile, getFile, setEditorLease, editorEventsUrl, onOutlineChange, registerOutlineNavigation}: TextEditorProps) {
    const {showError} = useSnackbar();

    const [contents, setContents] = useState<string>("");
    const [loading, setLoading] = useState(true);
    const [err, setErr] = useState("");
    const [draft, setDraft] = useState("");
    const [remoteVersion, setRemoteVersion] = useState<{contents: string; revision: string} | null>(null);
    const [compareOpen, setCompareOpen] = useState(false);
    const loadRequest = useRef(0);
    const dirty = useRef(false);
    const revisionRef = useRef("");
    const filenameRef = useRef(filename);
    filenameRef.current = filename;
    const session = useRef(typeof crypto.randomUUID === 'function' ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`);
    const updateLease = useCallback((dirty: boolean) => setEditorLease?.(filename, session.current, dirty) ?? Promise.resolve(), [filename, setEditorLease]);

    const {status, handleContentChange} = useSaveStatus(500, filename);

    const loadFile = useCallback(async (background = false) => {
        const request = ++loadRequest.current;
        setErr("")
        if (!background) setLoading(true)

        const {contents, revision, err} = await getFile(filename)
        if (request !== loadRequest.current) return;

        if (err) {
            setErr(err)
        } else {
            setContents(contents)
            setDraft(contents)
            revisionRef.current = revision
            dirty.current = false
            setRemoteVersion(null)
            void updateLease(false)
        }

        if (!background) setLoading(false);
    }, [filename, getFile, updateLease]);

    const saveContents = useCallback(async (newContent: string): Promise<SaveState> => {
        const savedFilename = filename;
        const result = await saveFile(savedFilename, newContent, revisionRef.current, session.current);
        // A save flushed while the editor moved on to another file still has to
        // reach the server - those were the last keystrokes typed - but its
        // result must never be written back: this component keeps one revision
        // and one session across files, and they now describe a different one.
        if (savedFilename !== filenameRef.current) {
            return result.err ? 'error' : 'success';
        }
        if (result.err) {
            if (result.conflict) {
                const latest = await getFile(filename)
                if (!latest.err) {
                    setRemoteVersion({contents: latest.contents, revision: latest.revision})
                    setCompareOpen(true)
                }
            } else {
                showError(`Could not save contents: ${result.err}`);
            }
            return 'error'
        } else {
            revisionRef.current = result.revision
            dirty.current = false
            void updateLease(false)
            return 'success'
        }
    }, [filename, getFile, saveFile, showError, updateLease]);

    useEffect(() => {
        setFileSaveStatus(status)
    }, [setFileSaveStatus, status]);

    useEffect(() => {
        loadFile().then();
    }, [loadFile]);

    useEffect(() => {
        if (!editorEventsUrl) return;
        const source = new EventSource(editorEventsUrl);
        const onChange = (event: MessageEvent<string>) => {
            const change = JSON.parse(event.data) as {path: string; session?: string};
            if (change.path !== filename || change.session === session.current) return;
            if (!dirty.current) {
                void loadFile(true);
                return;
            }
            void getFile(filename).then((latest) => {
                if (!latest.err) {
                    setRemoteVersion({contents: latest.contents, revision: latest.revision});
                    setCompareOpen(true);
                }
            });
        };
        source.addEventListener('file-change', onChange as EventListener);
        return () => source.close();
    }, [editorEventsUrl, filename, getFile, loadFile]);

    useEffect(() => {
        const timer = window.setInterval(() => {
            if (dirty.current) void updateLease(true);
        }, 45_000);
        return () => {
            window.clearInterval(timer);
            void updateLease(false);
        };
    }, [updateLease]);

    const onContentChange = useCallback((value: string | undefined) => {
        if (value === undefined) return;
        setDraft(value)
        if (!dirty.current) {
            dirty.current = true
            void updateLease(true)
        }
        handleContentChange(value, saveContents)
    }, [handleContentChange, saveContents, updateLease])

    const reloadRemote = useCallback(() => {
        if (!remoteVersion) return;
        setContents(remoteVersion.contents)
        setDraft(remoteVersion.contents)
        revisionRef.current = remoteVersion.revision
        dirty.current = false
        setRemoteVersion(null)
        setCompareOpen(false)
        void updateLease(false)
    }, [remoteVersion, updateLease]);

    const overwriteRemote = useCallback(async () => {
        if (!remoteVersion) return;
        revisionRef.current = remoteVersion.revision
        const state = await saveContents(draft)
        if (state === 'success') {
            setRemoteVersion(null)
            setCompareOpen(false)
        }
    }, [draft, remoteVersion, saveContents]);

    if (loading) {
        return (
            <Box sx={{
                display: 'flex',
                flexDirection: 'column',
                alignItems: 'center',
                justifyContent: 'center',
                height: '100%',
                gap: 2
            }}>
                <CircularProgress size={40}/>
                <Typography variant="body2" sx={{
                    color: "text.secondary"
                }}>Loading {filename}...</Typography>
            </Box>
        );
    }

    if (err) {
        return (
            <Box sx={{p: 3}}>
                {
                    err.startsWith(ErrFileNotSupported) ?
                        <BinaryErrView err={err}/> :
                        <NormalErrView
                            err={err}
                            retry={() => void loadFile()}
                        />
                }
            </Box>
        );
    }

    return (
        // clip Monaco overlays (e.g. the sticky scroll band, which keeps a
        // stale width when the widget panel resizes the editor) so they can
        // never paint over neighboring panels
        <Box sx={{flexGrow: 1, position: 'relative', overflow: 'hidden'}}>
            {remoteVersion && <Alert severity="warning" sx={{position: 'absolute', zIndex: 4, top: 8, left: 8, right: 8}} action={
                <Button color="inherit" size="small" onClick={() => setCompareOpen(true)}>Compare</Button>
            }>This file changed outside the editor. Your draft was preserved and was not overwritten.</Alert>}
            <MonacoEditor
                selectedFile={filename}
                fileContent={contents}
                handleEditorChange={onContentChange}
                onOutlineChange={onOutlineChange}
                registerOutlineNavigation={registerOutlineNavigation}
            />
            <Dialog open={compareOpen && remoteVersion !== null} onClose={() => setCompareOpen(false)} maxWidth="xl" fullWidth>
                <DialogTitle>File changed while editing — {filename}</DialogTitle>
                <DialogContent sx={{display: 'flex', flexDirection: 'column', gap: 1, height: '65vh'}}>
                    <Box sx={{display: 'grid', gridTemplateColumns: '1fr 1fr'}}>
                        <Typography sx={{fontWeight: 700}}>Current file (Git or external change)</Typography>
                        <Typography sx={{fontWeight: 700}}>Your Dockman draft</Typography>
                    </Box>
                    <Box sx={{flex: 1, minHeight: 0, border: 1, borderColor: 'divider'}}>
                        <DiffEditor
                            original={remoteVersion?.contents ?? ''}
                            modified={draft}
                            language={getLanguageFromExtension(filename)}
                            theme="vs-dark"
                            options={{readOnly: true, renderSideBySide: true, minimap: {enabled: false}, automaticLayout: true}}
                        />
                    </Box>
                </DialogContent>
                <DialogActions>
                    <Button onClick={() => setCompareOpen(false)}>Keep editing</Button>
                    <Button color="warning" onClick={reloadRemote}>Use current file</Button>
                    <Button color="error" variant="contained" onClick={overwriteRemote}>Overwrite with my draft</Button>
                </DialogActions>
            </Dialog>
        </Box>
    );
}

const NormalErrView = ({err, retry}: { err: string, retry: () => void }) => {
    return (
        <Alert
            severity="error"
            variant="outlined"
            icon={<ErrorOutlined/>}
            sx={{borderRadius: 2, bgcolor: 'background.paper'}}
        >
            <AlertTitle sx={{fontWeight: 700}}>
                Download Failed
            </AlertTitle>
            <Typography variant="body2">
                An error occurred while trying to retrieve the file content.
            </Typography>
            <Button variant='outlined' onClick={retry}>
                Reload
            </Button>
            <Box sx={{
                mt: 1,
                p: 1,
                borderRadius: 1,
                fontFamily: 'monospace',
                fontSize: '0.7rem'
            }}>
                {err}
            </Box>
        </Alert>
    )
}

const BinaryErrView = ({err}: { err: string }) => {
    return (
        <Alert
            severity="warning"
            variant="outlined"
            icon={<WarningAmber/>}
            sx={{borderRadius: 2, bgcolor: 'background.paper'}}
        >
            <AlertTitle sx={{fontWeight: 700}}>Binary File Detected</AlertTitle>
            <Typography variant="body1" sx={{mb: 1.5}}>
                Dockman has determined that this is not a valid text file. To prevent accidental
                corruption, editing binary files is not allowed.
            </Typography>
            <Typography
                variant="caption"
                sx={{
                    color: "text.secondary",
                    display: "block"
                }}>
                If you believe this file should be editable,{' '}
                <Link
                    href="https://github.com/ra341/dockman/issues"
                    target="_blank"
                    rel="noopener"
                    sx={{fontWeight: 700, textDecoration: 'underline'}}
                >
                    submit an issue
                </Link>
                .
            </Typography>
            <Box sx={{
                mt: 1,
                p: 1,
                borderRadius: 1,
                fontFamily: 'monospace',
                fontSize: '0.8rem'
            }}>
                {err}
            </Box>
        </Alert>
    );
}

export default EditorCommon;
