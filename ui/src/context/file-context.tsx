import {createContext, type ReactNode, useCallback, useContext, useEffect, useState} from 'react'
import {useNavigate} from 'react-router-dom'
import {callRPC, useHostClient, useHostUrl,} from "../lib/api.ts";
import {useSnackbar} from "../hooks/snackbar.ts";
import {useUploadProgress} from "../hooks/upload-progress.ts";
import {FileService, type FsEntry} from '../gen/files/v1/files_pb.ts';
import {useTabs} from "./tab-context.tsx";
import {useEditorUrl} from "../lib/editor.ts";
import {useHostStore, useOpenFiles} from "../pages/compose/state/files.ts";
import {useFileComponents} from "../pages/compose/state/terminal.tsx";
import {debugError} from "../lib/debug.ts";
import {markGitStackLocal} from '../components/git-stack-status-store.ts';

// btoa only accepts Latin1, so a path with accents, curly quotes or any other
// non-Latin1 character throws. Encode the UTF-8 bytes instead; the backend
// base64-decodes this straight back to the raw UTF-8 path bytes.
function encodePathForMultipart(path: string): string {
    const bytes = new TextEncoder().encode(path);
    let binary = '';
    bytes.forEach((b) => (binary += String.fromCharCode(b)));
    return btoa(binary)
        .replace(/\+/g, '-')
        .replace(/\//g, '_')
        .replace(/=+$/, '');
}

export interface FilesContextType {
    files: FsEntry[]
    isLoading: boolean

    addFile: (filename: string, isDir: boolean) => Promise<void>
    copyFile: (srcFilename: string, destFilename: string, isDir: boolean) => Promise<void>
    deleteFile: (filename: string) => Promise<void>
    renameFile: (oldFilename: string, newFile: string) => Promise<void>
    listFiles: (path: string, depthIndex: number[]) => Promise<void>

    uploadFile: (filename: string, contents: File | string, upload?: boolean) => Promise<string>
    uploadFilesFromPC: (targetDir: string, files: File[]) => Promise<void>

    downloadFile: (filename: string, shouldDownload?: boolean) => Promise<{ file: string; err: string }>
    loadEditableFile: (filename: string) => Promise<{ contents: string; revision: string; err: string }>
    saveEditableFile: (filename: string, contents: string, revision: string, session: string) => Promise<{ revision: string; err: string; conflict: boolean }>
    setEditorLease: (filename: string, session: string, dirty: boolean) => Promise<void>
    editorEventsUrl: string
}

export const FilesContext = createContext<FilesContextType | undefined>(undefined)
export const ErrFileNotSupported = "File is not supported"

export function useFiles() {
    const context = useContext(FilesContext)
    if (context === undefined) {
        throw new Error('useFiles must be used within a FilesProvider')
    }
    return context
}

function FilesProvider({children}: { children: ReactNode }) {
    const client = useHostClient(FileService)
    const {showError, showSuccess} = useSnackbar()
    const navigate = useNavigate()
    const host = useHostStore(state => state.host)

    const {closeTab, renameTab} = useTabs()

    const [files, setFiles] = useState<FsEntry[]>([])
    const [isLoading, setIsLoading] = useState(true)

    // don't use alias store since its dependent on the React lifecycle
    // const alias = useAliasStore(state => state.alias)
    const {alias} = useFileComponents()

    const fetchFiles = useCallback(async (
        path: string = "",
        depthIndex: number[] = []
    ) => {
        if (depthIndex.length < 2) {
            // empty filelist show full spinner
            setIsLoading(true)
        }

        if (path === "") {
            path = `${alias}`
        }

        const {val, err} = await callRPC(() => client.list({
            path: path,
        }))
        if (err) {
            showError(err)
        } else if (val) {
            setFiles(prevState => {
                if (depthIndex.length < 1) {
                    return val.entries
                } else {
                    const newList = [...prevState]
                    insertAtNestedIndex(newList, depthIndex, val.entries)
                    return newList
                }
            })
        }

        setIsLoading(false)
    }, [alias, client, showError]);

    const closeFolder = useOpenFiles(state => state.delete)
    const fileUrl = useEditorUrl()

    const addFile = useCallback(async (
        filename: string,
        isDir: boolean,
    ) => {
        const {err} = await callRPC(() => client.create({filename, isDir}))
        if (err) {
            showError(err)
            return
        } else {
            markGitStackLocal(host, filename)
            if (!isDir) {
                navigate(fileUrl(filename))
            }
            showSuccess(`Created ${filename}`)
        }

        await fetchFiles()
    }, [client, fetchFiles, fileUrl, host, navigate, showError, showSuccess])

    const copyFile = useCallback(async (srcFilename: string, destFilename: string, isDir: boolean) => {
        const {err} = await callRPC(() => client.copy({
            dest: {
                filename: srcFilename,
                isDir: isDir,
            },
            source: {
                filename: destFilename,
                isDir: isDir,
            },
        }))

        if (err) {
            showError(err)
        } else {
            markGitStackLocal(host, destFilename)
            if (!isDir) {
                navigate(fileUrl(destFilename))
            }
            showSuccess(`Copied ${destFilename}`)
        }

        await fetchFiles()
    }, [client, fetchFiles, fileUrl, host, navigate, showError, showSuccess])


    const deleteFile = async (
        filename: string,
    ) => {
        const {err} = await callRPC(() => client.delete({filename}))
        if (err) {
            showError(err)
        } else {
            markGitStackLocal(host, filename)
            showSuccess(`Deleted ${filename}`)
            closeFolder(filename)
            closeTab(filename)
        }

        await fetchFiles()
    }

    const renameFile = async (
        oldFilename: string,
        newFileName: string,
    ) => {
        const {err} = await callRPC(() => client.rename({
            newFilePath: newFileName,
            oldFilePath: oldFilename,
        }))
        if (err) {
            showError(err)
        } else {
            markGitStackLocal(host, oldFilename)
            markGitStackLocal(host, newFileName)
            showSuccess(`${oldFilename} renamed to ${newFileName}`)
            renameTab(oldFilename, newFileName)
        }

        await fetchFiles()
    }

    const getUrl = useHostUrl()
    const editorEventsUrl = getUrl('/file/events')

    const uploadFile = useCallback(function (
        fullPath: string,
        content: File | string,
        isNew: boolean = false,
        onProgress?: (loaded: number, total: number) => void,
    ): Promise<string> {
        const url = getUrl(`/file/save${isNew ? '?create=true' : ''}`)

        const fileBlob = typeof content === 'string'
            // If it's a string (from editor), wrap it.
            ? new File([content], getEntryDisplayName(fullPath))
            // If it's already a File (from DnD), use it.
            : content;

        // XMLHttpRequest (not fetch) so we can observe upload progress via
        // xhr.upload.onprogress — fetch with a FormData body reports nothing.
        return new Promise<string>((resolve) => {
            try {
                const formData = new FormData();
                // A multipart filename is normalized as a filesystem name by
                // Go. URL-safe Base64 avoids '/' being treated as a separator.
                formData.append('contents', fileBlob, encodePathForMultipart(fullPath));

                const xhr = new XMLHttpRequest();
                xhr.open('POST', url, true);

                if (onProgress) {
                    xhr.upload.onprogress = (e) => {
                        if (e.lengthComputable) onProgress(e.loaded, e.total);
                    };
                }

                xhr.onload = () => {
                    if (xhr.status >= 200 && xhr.status < 300) {
                        markGitStackLocal(host, fullPath);
                        resolve("");
                    } else {
                        resolve(`Error: ${xhr.status} - ${xhr.responseText}`);
                    }
                };
                xhr.onerror = () => {
                    debugError("Upload failed");
                    resolve("Network error");
                };

                xhr.send(formData);
            } catch (error) {
                debugError("Upload failed", error);
                resolve("Network error");
            }
        });
    }, [getUrl, host])

    const uploadFilesFromPC = async (targetDir: string, files: File[]) => {
        const cleanDir = targetDir.endsWith('/') ? targetDir.slice(0, -1) : targetDir;

        // Aggregate per-file byte counts into one batch progress figure. Uploads
        // run in parallel, so each file writes into its slot and we sum.
        const totalBytes = files.reduce((sum, f) => sum + f.size, 0);
        const loaded = new Array(files.length).fill(0);
        let doneCount = 0;

        const pushProgress = () => {
            const loadedBytes = loaded.reduce((a, b) => a + b, 0);
            useUploadProgress.getState().update(loadedBytes, doneCount);
        };

        useUploadProgress.getState().start(files.length, totalBytes);

        const results = await Promise.all(files.map((file, i) => {
            const fullPath = `${cleanDir}/${file.name}`;
            return uploadFile(fullPath, file, true, (l) => {
                loaded[i] = l;
                pushProgress();
            }).then((res) => {
                doneCount++;
                loaded[i] = file.size; // a finished file counts as fully sent
                pushProgress();
                return res;
            });
        }));

        useUploadProgress.getState().finish();

        const errors = results.filter(res => res !== "");
        if (errors.length > 0) {
            showError(`${errors.length} files failed to upload.`)
        } else {
            showSuccess(`Uploaded ${results.length} files`);
        }

        await fetchFiles("");
    };


    const downloadFile = useCallback(async function (
        filename: string,
        shouldDownload: boolean = false
    ): Promise<{ file: string; err: string }> {
        const url = getUrl(`/file/load/${encodeURIComponent(filename)}?download=${shouldDownload}`)

        try {
            const response = await fetch(url, {
                cache: 'no-cache',
            });

            const bodyText = await response.text();
            if (!response.ok) {
                if (response.status === 409) {
                    return {file: "", err: `${ErrFileNotSupported}: ${response.status} ${bodyText}`};
                }
                return {file: "", err: `Failed to download file: ${response.status} ${bodyText}`};
            }


            if (shouldDownload) {
                const blob = await response.blob();
                const url = window.URL.createObjectURL(blob);
                const a = document.createElement('a');

                a.href = url;
                a.download = filename;
                document.body.appendChild(a);
                a.click();

                window.URL.revokeObjectURL(url);
                document.body.removeChild(a);

                return {file: "", err: ""};
            }

            return {file: bodyText, err: ""};
        } catch (error: unknown) {
            debugError("File download failed", error);
            return {file: "", err: (error as Error).toString()};
        }
    }, [getUrl])

    const loadEditableFile = useCallback(async (filename: string) => {
        try {
            const response = await fetch(getUrl(`/file/load/${encodeURIComponent(filename)}?download=false`), {cache: 'no-cache'});
            const contents = await response.text();
            if (!response.ok) return {contents: '', revision: '', err: `Failed to load file: ${response.status} ${contents}`};
            return {contents, revision: (response.headers.get('ETag') ?? '').replaceAll('"', ''), err: ''};
        } catch (error) {
            return {contents: '', revision: '', err: String(error)};
        }
    }, [getUrl]);

    const saveEditableFile = useCallback((filename: string, contents: string, revision: string, session: string) => {
        return new Promise<{ revision: string; err: string; conflict: boolean }>((resolve) => {
            const formData = new FormData();
            formData.append('contents', new File([contents], getEntryDisplayName(filename)), encodePathForMultipart(filename));
            const xhr = new XMLHttpRequest();
            xhr.open('POST', getUrl('/file/save'), true);
            if (revision) xhr.setRequestHeader('If-Match', `"${revision}"`);
            xhr.setRequestHeader('X-Dockman-Editor-Session', session);
            xhr.onload = () => {
                const next = (xhr.getResponseHeader('ETag') ?? '').replaceAll('"', '');
                if (xhr.status >= 200 && xhr.status < 300) {
                    markGitStackLocal(host, filename);
                    resolve({revision: next, err: '', conflict: false});
                } else {
                    resolve({revision: next, err: xhr.responseText || `Save failed (${xhr.status})`, conflict: xhr.status === 409});
                }
            };
            xhr.onerror = () => resolve({revision: '', err: 'Network error', conflict: false});
            xhr.send(formData);
        });
    }, [getUrl, host]);

    const setEditorLease = useCallback(async (filename: string, session: string, dirty: boolean) => {
        try {
            await fetch(getUrl('/file/edit-lease'), {
                method: dirty ? 'PUT' : 'DELETE',
                headers: {'Content-Type': 'application/json'},
                body: JSON.stringify({path: filename, session}),
            });
        } catch (error) {
            debugError('Editor lease update failed', error);
        }
    }, [getUrl]);

    useEffect(() => {
        fetchFiles().then()
    }, [fetchFiles])

    const value: FilesContextType = {
        files,
        isLoading,
        addFile,
        copyFile,
        deleteFile,
        renameFile,
        listFiles: fetchFiles,
        uploadFile,
        downloadFile,
        uploadFilesFromPC,
        loadEditableFile,
        saveEditableFile,
        setEditorLease,
        editorEventsUrl,
    }

    return (
        <FilesContext.Provider value={value}>
            {children}
        </FilesContext.Provider>
    )
}


function insertAtNestedIndex(list: FsEntry[], indices: number[], value: FsEntry[]): void {
    if (indices.length === 0) return;

    let current: FsEntry[] | null = list;

    // Navigate to the parent using all indices except the last one
    for (let i = 0; i < indices.length - 1; i++) {
        const index = indices[i];
        if (!current || !current[index] || !current[index].subFiles) {
            debugError('Invalid file-tree path at index', i);
            return;
        }
        current = current[index].subFiles;
    }

    // Set the value at the final index
    const lastIndex = indices[indices.length - 1];
    if (!current || !current[lastIndex]) {
        debugError('Invalid final file-tree index', lastIndex);
        return;
    }

    current[lastIndex].isFetched = true;
    current[lastIndex].subFiles = value;
}

export function getDir(filePath: string): string {
    const lastSlash = filePath.lastIndexOf('/');
    if (lastSlash === -1) return '';
    if (lastSlash === 0) return '';
    return filePath.substring(0, lastSlash);
}

export const getEntryDisplayName = (path: string) => {
    const split = path.split("/");
    const pop = split.pop();
    if (!pop) {
        debugError("Unable to get the last path element", split)
        return "ERR_EMPTY_PATH"
    }
    return pop
}

export default FilesProvider
