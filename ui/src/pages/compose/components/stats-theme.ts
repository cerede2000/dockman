// Shared palette for the stats view (deep navy look).
export const statsTheme = {
    page: '#0a1626',
    panel: '#0e1c31',
    header: '#13233f',
    row: '#0f1e35',
    rowHover: '#152742',
    border: 'rgba(125,160,215,0.14)',
    text: '#dce7f7',
    textDim: '#8ba4c7',
    mono: '"JetBrains Mono", "Roboto Mono", "SFMono-Regular", Consolas, monospace',

    cpuLine: '#60a5fa',
    memLine: '#a78bfa',
    netDown: '#4ade80',
    netUp: '#38bdf8',
    diskRead: '#34d399',
    diskWrite: '#fbbf24',
} as const;

// State badge colors, Dockhand-style.
export const stateBadges: Record<string, { bg: string; fg: string; label: string }> = {
    running: {bg: 'rgba(16,124,73,0.9)', fg: '#d7f8e7', label: 'running'},
    exited: {bg: 'rgba(71,85,105,0.55)', fg: '#cbd5e1', label: 'exited'},
    created: {bg: 'rgba(51,88,138,0.55)', fg: '#cfe2f8', label: 'created'},
    paused: {bg: 'rgba(161,120,17,0.55)', fg: '#fde9b8', label: 'paused'},
    restarting: {bg: 'rgba(180,90,26,0.6)', fg: '#ffe1c7', label: 'restarting'},
    removing: {bg: 'rgba(148,68,68,0.55)', fg: '#ffd9d9', label: 'removing'},
    dead: {bg: 'rgba(153,27,45,0.7)', fg: '#ffd4dc', label: 'dead'},
};

export const healthColors: Record<string, string> = {
    healthy: '#34d399',
    unhealthy: '#f87171',
    starting: '#fbbf24',
};
