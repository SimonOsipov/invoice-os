// Chart, format & billing math layer (M4-20-01). STUB — the executor implements the
// bodies next; every export below throws `new Error('not implemented')` so the RED
// specs in charts.test.ts fail on a thrown/assertion mismatch, not an import or type
// error. Signatures are pinned by the Obsidian M4-20 story's Test Specs table + the
// task-138 validation addenda (parts 1-3) — do not change a signature here without
// re-deriving the corresponding golden values in the story.
//
// Source of truth: `Developer Console.dc.html` (Claude Design project
// 6269a212-5677-4abd-b8a9-08aad10b1c65, read-only) lines 768-780 (rng/series/
// lineChart/formatters), 888-924 (spend bars, outcome columns, rejection reasons),
// 1044 (upStrip), 1029-1034 (billing line items implying SCALE_PLAN). `SCALE_PLAN`,
// `computeBillLine`, `computeQuota` and `compactCount` have NO prototype source
// (task-138 GAP-4/GAP-1) — they are new derivations that reproduce the prototype's
// hardcoded literals, not ports.
//
// Kept dependency-free of React so these stay unit-testable pure functions, matching
// the existing helpers.ts doc comment.

export function rng(_seed: number): () => number {
  throw new Error('not implemented')
}

export function series(_n: number, _base: number, _amp: number, _trend: number, _seed: number): number[] {
  throw new Error('not implemented')
}

export function lineChart(
  _pts: number[],
  _W: number,
  _H: number,
  _padTop: number,
  _padBottom: number,
): { line: string; area: string } {
  throw new Error('not implemented')
}

export function fmt(_n: number): string {
  throw new Error('not implemented')
}

export function naira(_n: number): string {
  throw new Error('not implemented')
}

export function nairaC(_n: number): string {
  throw new Error('not implemented')
}

// GAP-1 — a third formatter distinct from nairaC: no currency symbol, one decimal
// place with trailing-zero stripping (48214 -> '48.2K', 40000 -> '40K').
export function compactCount(_n: number): string {
  throw new Error('not implemented')
}

export interface SpendBar {
  h: string
  fill: string
  border: string
  proj: boolean
}

export function buildSpendBars(): SpendBar[] {
  throw new Error('not implemented')
}

export interface OutcomeCol {
  acc: string
  rej: string
  fail: string
  pend: string
}

export function buildOutcomeColumns(): OutcomeCol[] {
  throw new Error('not implemented')
}

// GAP-2 — the prototype's input shape (lines 916-922) is {label, count}[], not bare
// numbers; label is required by the consumer.
export interface RejectionInput {
  label: string
  count: number
}

export interface RejectionBar {
  label: string
  count: string
  width: string
  color: string
}

export function buildRejectionReasons(_raw: RejectionInput[]): RejectionBar[] {
  throw new Error('not implemented')
}

export function upStrip(_seed: number, _badIdx: number[]): { fill: string }[] {
  throw new Error('not implemented')
}

// GAP-4 — no prototype source; new derivation reproducing the sidebar string (line
// 1081) and the static billing markup (lines 468-487) literals. Stubbed to a bad
// sentinel (not the real rates) so any test reading SCALE_PLAN directly fails loudly
// rather than silently resolving a plausible-looking value.
export const SCALE_PLAN: {
  clearedRate: 40
  overageRate: 42
  includedRequests: 40000
  baseFee: 1200000
} = undefined as unknown as {
  clearedRate: 40
  overageRate: 42
  includedRequests: 40000
  baseFee: 1200000
}

export function computeBillLine(_qty: number, _rate: number): number {
  throw new Error('not implemented')
}

export function computeQuota(
  _used: number,
  _included: number,
): {
  pct: number
  widthPct: number
  over: number
  detail: string
} {
  throw new Error('not implemented')
}
