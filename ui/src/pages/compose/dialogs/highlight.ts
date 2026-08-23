export type HighlightRun = { text: string; match: boolean }

// groupHighlightRuns collapses consecutive characters of the same kind into a
// single run.
//
// The renderer above it was labelled "Optimized Highlighting: Groups
// characters to minimize DOM nodes" and did the exact opposite: one styled
// <span> per character, each carrying its own sx object. A 60-character path
// in a 50-row result list is 3000 spans and 3000 style objects, rebuilt on
// every keystroke of a dialog whose whole purpose is to be typed into.
//
// Indices are UTF-16 code-unit offsets, exactly as the previous split('')
// indexing read them: matching stays byte-for-byte what it was.
export function groupHighlightRuns(text: string, indices?: number[]): HighlightRun[] {
    if (!text) return []
    const matched = new Set(indices ?? [])
    const runs: HighlightRun[] = []
    let start = 0
    for (let i = 1; i <= text.length; i++) {
        if (i < text.length && matched.has(i) === matched.has(start)) continue
        runs.push({text: text.slice(start, i), match: matched.has(start)})
        start = i
    }
    return runs
}
