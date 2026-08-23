import {Box} from "@mui/material";
import {Editor, type Monaco} from "@monaco-editor/react";
import {getLanguageFromExtension} from "../../../lib/editor";
import {useCallback, useEffect, useRef, useState} from "react";
import * as monacoEditor from "monaco-editor";
import {callRPC, useHostClient} from "../../../lib/api.ts";
import {useSnackbar} from "../../../hooks/snackbar.ts";
import {useTabs, useTabsStore} from "../../../context/tab-context.tsx";
import {FileService} from "../../../gen/files/v1/files_pb.ts";
import {useConfig} from "../../../hooks/config.ts";
import {buildYamlOutline, type YamlOutlineItem} from "./yaml-outline.ts";
import {canReadClipboard, readClipboardText} from "./clipboard.ts";
import {letBrowserMenuThrough} from "./context-menu.ts";

interface MonacoEditorProps {
    selectedFile: string;
    fileContent: string;
    handleEditorChange: (value: string | undefined) => void;
    onOutlineChange?: (items: YamlOutlineItem[]) => void;
    registerOutlineNavigation?: (navigate: ((item: YamlOutlineItem) => void) | null) => void;
}

export function MonacoEditor(
    {
        selectedFile,
        fileContent,
        handleEditorChange,
        onOutlineChange,
        registerOutlineNavigation,
    }: MonacoEditorProps) {
    const file = useHostClient(FileService)
    const {showError} = useSnackbar()

    const editorRef = useRef<monacoEditor.editor.IStandaloneCodeEditor | null>(null);
    const outlineTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
    const saveLineNum = useSaveLineNum()

    const [mounted, setMounted] = useState(false);
    const contextMenuHost = useRef<HTMLDivElement | null>(null);

    useEffect(() => {
        const host = contextMenuHost.current;
        if (!host) return;
        host.addEventListener('contextmenu', letBrowserMenuThrough, true);
        return () => host.removeEventListener('contextmenu', letBrowserMenuThrough, true);
    }, []);
    // bumped on every editor instance creation: the component remounts per
    // file (key={selectedFile}) while `mounted` stays true, so effects that
    // must re-attach to the new instance depend on this counter instead
    const [editorGen, setEditorGen] = useState(0);
    const {setTabDetails} = useTabs()

    const {dockYaml} = useConfig()
    const scrollPastEnd = dockYaml?.editorPage?.scrollPastEnd ?? false

    // dockman.yml editor.scrollPastEnd lets long files scroll half a viewport
    // past their end (the last line stops at mid-view, not at the top), via a
    // bottom padding applied only while the file is taller than the viewport:
    // short files never scroll past their end
    useEffect(() => {
        const editor = editorRef.current;
        if (!editor) return;

        if (!scrollPastEnd) {
            editor.updateOptions({padding: {bottom: 0}});
            return;
        }

        const apply = () => {
            const lineHeight = editor.getOption(monacoEditor.editor.EditorOption.lineHeight);
            const lineCount = editor.getModel()?.getLineCount() ?? 0;
            const height = editor.getLayoutInfo().height;
            const overflows = lineCount * lineHeight > height;
            editor.updateOptions({padding: {bottom: overflows ? Math.round(height / 2) : 0}});
        };

        apply();
        const contentSub = editor.onDidChangeModelContent(apply);
        const layoutSub = editor.onDidLayoutChange(apply);
        return () => {
            contentSub.dispose();
            layoutSub.dispose();
        };
    }, [scrollPastEnd, editorGen]);

    const handleEditorDidMount = (editor: monacoEditor.editor.IStandaloneCodeEditor, monaco: Monaco) => {
        editorRef.current = editor;
        setMounted(true);
        setEditorGen(gen => gen + 1);
        editor.focus();

        editor.addCommand(
            monaco.KeyMod.Alt | monaco.KeyCode.KeyL,
            async () => {
                const {val, err} = await callRPC(() => file.format({filename: selectedFile}))
                if (err) {
                    showError(err)
                } else {
                    const contents = val?.contents;
                    if (contents) {
                        const model = editor.getModel()!;
                        const position = editor.getPosition()!;
                        const offset = model?.getOffsetAt(position);
                        const fullRange = model.getFullModelRange();

                        // Replace content while preserving undo stack
                        editor.executeEdits('format', [{
                            range: fullRange,
                            text: contents,
                            forceMoveMarkers: true
                        }]);

                        // Calculate new position from offset
                        // This handles cases where formatting changes line counts
                        const newPosition = model.getPositionAt(Math.min(offset, contents.length));
                        editor.setPosition(newPosition);
                        editor.revealPositionInCenter(newPosition);
                    }
                }
            }
        );

        // Monaco hides Paste from its own context menu on the web, because a
        // page cannot ask the browser to paste on its own. Reading the
        // clipboard IS possible where the browser allows it, so the entry is
        // offered exactly there - and nowhere else. Where the browser withholds
        // clipboard reads outright, an entry that can only ever answer with an
        // explanation is worse than no entry, so there is none: Ctrl+V and
        // Shift+right-click both work regardless.
        if (canReadClipboard(navigator.clipboard)) {
            editor.addAction({
                id: 'dockman.paste',
                label: 'Paste',
                contextMenuGroupId: '9_cutcopypaste',
                contextMenuOrder: 3,
                run: async (target) => {
                    const result = await readClipboardText(navigator.clipboard)
                    if ('unavailable' in result) {
                        showError(result.unavailable)
                        return
                    }
                    if (!result.text) return
                    const selections = target.getSelections() ?? []
                    if (selections.length === 0) return
                    target.executeEdits('paste', selections.map(selection => ({
                        range: selection,
                        text: result.text,
                        forceMoveMarkers: true,
                    })))
                    target.focus()
                },
            });
        }

        editorRef.current?.getValue();

        editor.onDidChangeCursorPosition((e) => {
            const {lineNumber, column} = e.position;
            saveLineNum({filename: selectedFile, col: column, row: lineNumber}, (value) => {
                setTabDetails(value.filename, {row: value.row, col: value.col});
            });
        });
    };

    useEffect(() => {
        const editor = editorRef.current;
        if (!mounted || !editor || !registerOutlineNavigation) return;

        registerOutlineNavigation((item) => {
            const model = editor.getModel();
            if (!model) return;
            const lineNumber = Math.max(1, Math.min(item.line, model.getLineCount()));
            const column = Math.max(1, Math.min(item.column, model.getLineMaxColumn(lineNumber)));
            editor.setPosition({lineNumber, column});
            editor.revealRangeAtTop({
                startLineNumber: lineNumber,
                startColumn: column,
                endLineNumber: lineNumber,
                endColumn: column,
            }, monacoEditor.editor.ScrollType.Immediate);
            editor.focus();
        });

        return () => registerOutlineNavigation(null);
    }, [mounted, editorGen, registerOutlineNavigation]);

    useEffect(() => {
        if (!mounted || !editorRef.current) return;

        const model = editorRef.current.getModel();
        if (!model) return;

        model.pushStackElement();
        model.setValue(fileContent);

        const contentSubscription = model.onDidChangeContent(() => {
            const value = model.getValue();
            handleEditorChange(value);
            if (outlineTimer.current) clearTimeout(outlineTimer.current);
            outlineTimer.current = setTimeout(() => {
                onOutlineChange?.(buildYamlOutline(value));
                outlineTimer.current = null;
            }, 200);
        });

        onOutlineChange?.(buildYamlOutline(model.getValue()));

        const tab = useTabsStore.getState().allTabs[selectedFile];
        if (tab) {
            const {row, col} = tab;

            // Clamp row/column to model size
            const lineNumber = Math.min(row, model.getLineCount());
            const column = Math.min(col, model.getLineMaxColumn(lineNumber));

            editorRef.current.setPosition({lineNumber, column});
            const padding = 5;
            editorRef.current.revealRangeInCenter({
                startLineNumber: Math.max(1, lineNumber - padding),
                startColumn: 1,
                endLineNumber: lineNumber + padding,
                endColumn: 1,
            });
        }

        return () => {
            contentSubscription.dispose();
            if (outlineTimer.current) {
                clearTimeout(outlineTimer.current);
                outlineTimer.current = null;
            }
        };
        // do not add tabs as dependencies
        // it will mess with the editor typing
        // resetting cursor position when the tab
    }, [editorGen, fileContent, handleEditorChange, mounted, onOutlineChange, selectedFile]);

    return (
        // Shift+right-click is stopped here, in the capture phase, so it never
        // reaches Monaco: Monaco does not call preventDefault, and the browser
        // shows its own menu - whose Paste works without any clipboard
        // permission. A plain right-click is untouched and still opens Monaco's.
        <Box
            ref={contextMenuHost}
            sx={{width: '100%', height: '100%', minWidth: 0, minHeight: 0}}
        >
        <Editor
            key={selectedFile}
            language={getLanguageFromExtension(selectedFile)}
            defaultValue={""}
            onMount={handleEditorDidMount}
            theme="vs-dark"
            options={{
                tabSize: 2,
                selectOnLineNumbers: true,
                minimap: {enabled: false},
                automaticLayout: true,
                // stop the view from scrolling past the last line: short files
                // no longer show a useless scrollbar and long files stop with
                // the last line at the bottom, like classic editors
                // (dockman.yml editor.scrollPastEnd re-enables it dynamically)
                scrollBeyondLastLine: false,
                // hover/suggest widgets render position:fixed so the overflow
                // clip on the editor container cannot cut them off
                fixedOverflowWidgets: true,
            }}
        />
        </Box>
    );
}

type RowColUpdate = { row: number; col: number; filename: string };

function useSaveLineNum(debounceMs: number = 200) {
    const debounceTimeout = useRef<ReturnType<typeof setTimeout> | null>(null);

    const handleContentChange = useCallback(
        (value: RowColUpdate, onSave: (value: RowColUpdate) => void) => {
            if (debounceTimeout.current) {
                clearTimeout(debounceTimeout.current);
            }

            debounceTimeout.current = setTimeout(() => {
                onSave(value);
            }, debounceMs);
        },
        [debounceMs]
    );

    useEffect(() => {
        return () => {
            if (debounceTimeout.current) {
                clearTimeout(debounceTimeout.current);
            }
        };
    }, []);

    return handleContentChange;
}
