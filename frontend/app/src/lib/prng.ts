// Deterministic PRNG ported exactly from the prototype's `hash`/`mulberry` methods
// (Platform.dc.html ~L1005-1006) so seeded values are byte-identical, and switching
// companies always regenerates the same invoices.

/** FNV-1a 32-bit hash. */
export function hash(str: string): number {
  let h = 2166136261
  for (let i = 0; i < str.length; i++) {
    h = Math.imul(h ^ str.charCodeAt(i), 16777619)
  }
  return h >>> 0
}

/** mulberry32 — returns a seeded () => number generator in [0, 1). */
export function mulberry(seed: number): () => number {
  let a = seed
  return function () {
    a = (a + 0x6d2b79f5) | 0
    let t = Math.imul(a ^ (a >>> 15), 1 | a)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}
