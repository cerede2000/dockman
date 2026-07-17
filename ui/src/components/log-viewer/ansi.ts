// Minimal ANSI SGR parser: turns a raw log line into styled segments that can
// be rendered as plain React spans (no innerHTML). Non-SGR escape sequences
// (cursor movement, OSC titles...) are stripped.

export interface AnsiSegment {
    text: string;
    color?: string;
    background?: string;
    bold?: boolean;
    dim?: boolean;
    italic?: boolean;
    underline?: boolean;
}

// xterm-ish palette tuned for the app's dark background
const BASE_COLORS = [
    '#3f3f3f', '#e57373', '#81c784', '#ffd54f',
    '#64b5f6', '#ba68c8', '#4dd0e1', '#e0e0e0',
];
const BRIGHT_COLORS = [
    '#9e9e9e', '#ef9a9a', '#a5d6a7', '#fff176',
    '#90caf9', '#ce93d8', '#80deea', '#ffffff',
];

function xterm256Color(n: number): string | undefined {
    if (n < 0 || n > 255) return undefined;
    if (n < 8) return BASE_COLORS[n];
    if (n < 16) return BRIGHT_COLORS[n - 8];
    if (n < 232) {
        // 6x6x6 color cube
        const v = n - 16;
        const steps = [0, 95, 135, 175, 215, 255];
        const r = steps[Math.floor(v / 36) % 6];
        const g = steps[Math.floor(v / 6) % 6];
        const b = steps[v % 6];
        return `rgb(${r},${g},${b})`;
    }
    // grayscale ramp
    const gray = 8 + (n - 232) * 10;
    return `rgb(${gray},${gray},${gray})`;
}

interface SgrState {
    color?: string;
    background?: string;
    bold?: boolean;
    dim?: boolean;
    italic?: boolean;
    underline?: boolean;
}

function applySgr(state: SgrState, params: number[]): SgrState {
    let next = {...state};
    for (let i = 0; i < params.length; i++) {
        const p = params[i];
        if (p === 0) next = {};
        else if (p === 1) next.bold = true;
        else if (p === 2) next.dim = true;
        else if (p === 3) next.italic = true;
        else if (p === 4) next.underline = true;
        else if (p === 22) { delete next.bold; delete next.dim; }
        else if (p === 23) delete next.italic;
        else if (p === 24) delete next.underline;
        else if (p >= 30 && p <= 37) next.color = BASE_COLORS[p - 30];
        else if (p === 39) delete next.color;
        else if (p >= 40 && p <= 47) next.background = BASE_COLORS[p - 40];
        else if (p === 49) delete next.background;
        else if (p >= 90 && p <= 97) next.color = BRIGHT_COLORS[p - 90];
        else if (p >= 100 && p <= 107) next.background = BRIGHT_COLORS[p - 100];
        else if (p === 38 || p === 48) {
            // extended color: 38;5;n or 38;2;r;g;b
            const target = p === 38 ? 'color' : 'background';
            if (params[i + 1] === 5) {
                const c = xterm256Color(params[i + 2] ?? -1);
                if (c) next[target] = c;
                i += 2;
            } else if (params[i + 1] === 2) {
                const [r, g, b] = [params[i + 2] ?? 0, params[i + 3] ?? 0, params[i + 4] ?? 0];
                next[target] = `rgb(${r},${g},${b})`;
                i += 4;
            }
        }
    }
    return next;
}

const ESC = '\x1b';

export function parseAnsi(line: string): AnsiSegment[] {
    if (!line.includes(ESC)) {
        return line ? [{text: line}] : [];
    }

    const segments: AnsiSegment[] = [];
    let state: SgrState = {};
    let plain = '';

    const push = () => {
        if (plain) {
            segments.push({text: plain, ...state});
            plain = '';
        }
    };

    let i = 0;
    while (i < line.length) {
        const ch = line[i];
        if (ch !== ESC) {
            plain += ch;
            i++;
            continue;
        }

        const kind = line[i + 1];
        if (kind === '[') {
            // CSI sequence: params, then one final byte in @-~
            let j = i + 2;
            while (j < line.length && !(line[j] >= '@' && line[j] <= '~')) j++;
            if (j >= line.length) break; // truncated sequence, drop the rest
            if (line[j] === 'm') {
                push();
                const raw = line.slice(i + 2, j);
                const params = raw === '' ? [0] : raw.split(';').map(s => Number(s) || 0);
                state = applySgr(state, params);
            }
            i = j + 1;
        } else if (kind === ']') {
            // OSC sequence: ends with BEL or ESC-backslash
            let j = i + 2;
            while (j < line.length && line[j] !== '\x07' && !(line[j] === ESC && line[j + 1] === '\\')) j++;
            i = line[j] === ESC ? j + 2 : j + 1;
        } else {
            // two-byte escape (ESC c, ESC 7...), skip it
            i += 2;
        }
    }
    push();
    return segments;
}
