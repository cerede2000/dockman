import {describe, expect, it} from 'vitest'
import {groupHighlightRuns} from './highlight.ts'

describe('groupHighlightRuns', () => {
    it('returns nothing for empty text', () => {
        expect(groupHighlightRuns('', [0, 1])).toEqual([])
    })

    it('returns a single plain run when nothing matches', () => {
        expect(groupHighlightRuns('compose.yaml')).toEqual([{text: 'compose.yaml', match: false}])
    })

    it('collapses neighbouring matches into one run', () => {
        expect(groupHighlightRuns('abcd', [1, 2])).toEqual([
            {text: 'a', match: false},
            {text: 'bc', match: true},
            {text: 'd', match: false},
        ])
    })

    it('keeps isolated matches apart', () => {
        expect(groupHighlightRuns('abc', [0, 2])).toEqual([
            {text: 'a', match: true},
            {text: 'b', match: false},
            {text: 'c', match: true},
        ])
    })

    // What the whole thing is for: a realistic row must not cost one node per
    // character. Fuzzy-matching "cyml" against this path lights four letters.
    it('turns a realistic path into a handful of runs, not one per character', () => {
        const path = 'stacks/adguard/compose.yaml'
        const runs = groupHighlightRuns(path, [7, 15, 23, 24])
        expect(runs.map(r => r.text).join('')).toBe(path)
        expect(runs.length).toBeLessThan(path.length / 3)
    })

    it('reproduces the text exactly, whatever the indices', () => {
        const text = 'a/b-c_d.e'
        for (const indices of [[], [0], [8], [0, 8], [2, 3, 4]]) {
            expect(groupHighlightRuns(text, indices).map(r => r.text).join('')).toBe(text)
        }
    })
})
