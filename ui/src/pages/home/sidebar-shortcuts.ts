import {useEffect, useRef} from 'react';
import {altDigit, claimShortcut} from '../../lib/shortcut.ts';

export interface SidebarShortcutItem {
    id: string;
    path: string;
}

// Alt+1..9 opens the sidebar entry at that position - the position shown in
// each entry's tooltip, so the numbers follow the sidebar as it is displayed.
//
// The listener sits on document so it runs before the views' own listeners on
// window: when it navigates it claims the keystroke (see claimShortcut), and a
// view that also binds that key knows the page is leaving.
export function useSidebarShortcuts(
    items: SidebarShortcutItem[],
    navigate: (path: string) => void,
    pathname: string,
) {
    // read at key time, so a navigation doesn't re-register the listener
    const pathnameRef = useRef(pathname);
    useEffect(() => {
        pathnameRef.current = pathname;
    }, [pathname]);

    useEffect(() => {
        const handleKeyDown = (e: KeyboardEvent) => {
            const position = altDigit(e);
            if (position === null) return;
            // Ours whatever happens next: on Linux, Firefox binds Alt+1..9 to
            // its own tabs and only a prevented event keeps the page in charge.
            e.preventDefault();

            const item = items[position - 1];
            if (!item) return;
            // The Files view binds Alt+1 to its file bar, and the Files entry
            // only redirects back to the file already open: from inside Files,
            // the file bar is the only thing the keystroke can do.
            if (item.id === 'files' && pathnameRef.current.startsWith(item.path)) return;

            claimShortcut(e);
            navigate(item.path);
        };
        document.addEventListener('keydown', handleKeyDown);
        return () => document.removeEventListener('keydown', handleKeyDown);
    }, [items, navigate]);
}
