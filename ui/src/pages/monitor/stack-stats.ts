import type {MonitorRow, StackStats} from './monitor-table.tsx';
import {memoryCeiling} from '../../lib/memory-ceiling.ts';

// sums the member containers' live metrics and their history windows;
// sparklines scale to the window's shape, so a summed series keeps the
// aggregate's evolution readable.
//
// hostMemTotal caps the summed memory limits into the stack's real ceiling
// (see memoryCeiling); while it is unknown the ceiling is 0 and the row shows
// the memory used without a "/ limit".
export function aggregateStack(
    rows: MonitorRow[],
    history: Map<string, { cpu: number[]; mem: number[] }>,
    hostMemTotal: number,
): StackStats | null {
    let cpu = 0, memUsed = 0, memLimitSum = 0, netRx = 0, netTx = 0, seen = 0;
    for (const r of rows) {
        const s = r.stats;
        if (!s) continue;
        seen++;
        cpu += Math.max(s.cpuUsage, 0);
        memUsed += Number(s.memoryUsage);
        memLimitSum += Number(s.memoryLimit);
        netRx += Number(s.networkRx);
        netTx += Number(s.networkTx);
    }
    if (seen === 0) return null;

    const cpuSeries: number[][] = [];
    const memSeries: number[][] = [];
    for (const r of rows) {
        const h = history.get(r.info.name);
        if (!h) continue;
        cpuSeries.push(h.cpu);
        memSeries.push(h.mem);
    }

    return {
        cpu,
        memUsed,
        memLimit: memoryCeiling(memLimitSum, hostMemTotal),
        netRx,
        netTx,
        cpuHist: sumSeries(cpuSeries),
        memHist: sumSeries(memSeries),
    };
}

// element-wise sum of series aligned on their most recent points
export function sumSeries(series: number[][]): number[] {
    const len = Math.max(0, ...series.map(s => s.length));
    const out: number[] = [];
    for (let k = len; k >= 1; k--) {
        let sum = 0;
        for (const s of series) {
            const v = s[s.length - k];
            if (v !== undefined) sum += v;
        }
        out.push(sum);
    }
    return out;
}
