// memoryCeiling is the most memory a group of containers can use together:
// the sum of their limits, never more than the host has.
//
// A container without an explicit memory limit reports the host's total RAM
// as its limit - the value `docker stats` prints in its LIMIT column. Summing
// those counts the host once per container (15 unlimited containers on a 16 GB
// host read "240 GB"). Taking the largest limit instead fixes that case but
// breaks the opposite one: three containers capped at 512 MB each would share
// a 512 MB ceiling and read "234 %". Capping the sum at the host's memory is
// right in both, and in any mix of the two.
//
// The host total is what tells the two cases apart, and nothing in the
// per-container stats does: three unlimited containers on a 512 MB host and
// three 512 MB containers report exactly the same limits. Without it the
// ceiling is unknown, and 0 says so - a percentage computed against a guess
// would look exactly as trustworthy as the real one.
export function memoryCeiling(limitSum: number, hostMemTotal: number): number {
    if (!(hostMemTotal > 0) || !(limitSum > 0)) return 0
    return Math.min(limitSum, hostMemTotal)
}
