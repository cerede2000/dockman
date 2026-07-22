import {useState} from "react";

// The Clipboard API is restricted to secure contexts by most browsers. Dockman
// is also commonly served over plain HTTP on a private IP, so retain a
// user-gesture fallback instead of throwing when navigator.clipboard is absent.
export async function copyText(text: string): Promise<boolean> {
    try {
        if (navigator.clipboard?.writeText) {
            await navigator.clipboard.writeText(text);
            return true;
        }
    } catch {
        // Permission rejection can still happen in an iframe or an HTTP setup.
    }

    const textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.readOnly = true;
    textarea.style.position = "fixed";
    textarea.style.inset = "0 auto auto -9999px";
    textarea.style.opacity = "0";
    document.body.appendChild(textarea);
    textarea.focus();
    textarea.select();
    try {
        return document.execCommand("copy");
    } catch {
        return false;
    } finally {
        textarea.remove();
    }
}

export function useCopyButton() {
    const [copiedId, setCopiedId] = useState<string | null>(null);
    const handleCopy = (id: string) => {
        void copyText(id).then((copied) => {
            if (!copied) return;
            setCopiedId(id);
            window.setTimeout(() => setCopiedId(null), 1500);
        });
    };

    return {handleCopy, copiedId};
}
