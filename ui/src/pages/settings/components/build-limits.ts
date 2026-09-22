// Build limits as the host form edits them. The server (compose.BuildLimits)
// stays the authority; these mirror its bounds so the form can say what is
// wrong before a save is refused. Below them BuildKit itself cannot run, and
// the build would fail far from the setting that caused it.
export const MIN_BUILD_CPUS = 0.1;
export const MIN_BUILD_MEMORY_GIB = 0.25;

const MIB = 1024 ** 2;
const GIB = 1024 ** 3;

// A typed field: empty (or zero) means no limit.
export function parseCpus(text: string): number {
    const cpus = parseFloat(text);
    return Number.isFinite(cpus) && cpus > 0 ? cpus : 0;
}

// GiB typed in the form, stored in bytes at MiB precision.
export function parseMemory(text: string): bigint {
    const gib = parseFloat(text);
    if (!Number.isFinite(gib) || gib <= 0) return 0n;
    return BigInt(Math.round(gib * 1024)) * BigInt(MIB);
}

export function cpusText(cpus: number): string {
    return cpus > 0 ? String(cpus) : '';
}

export function memoryText(bytes: bigint): string {
    return bytes > 0n ? String(Number(bytes) / GIB) : '';
}

export function buildLimitErrors(cpuText: string, memText: string): { cpu?: string; memory?: string } {
    const errors: { cpu?: string; memory?: string } = {};
    const cpu = cpuText.trim() === '' ? 0 : parseFloat(cpuText);
    if (!Number.isFinite(cpu) || cpu < 0 || (cpu > 0 && cpu < MIN_BUILD_CPUS)) {
        errors.cpu = `At least ${MIN_BUILD_CPUS} core, or empty for no limit`;
    }
    const gib = memText.trim() === '' ? 0 : parseFloat(memText);
    if (!Number.isFinite(gib) || gib < 0 || (gib > 0 && gib < MIN_BUILD_MEMORY_GIB)) {
        errors.memory = `At least ${MIN_BUILD_MEMORY_GIB} GiB, or empty for no limit`;
    }
    return errors;
}
