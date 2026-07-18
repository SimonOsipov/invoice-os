// Chart, format & billing math layer (M4-20-01). Signatures are pinned by the Obsidian
// M4-20 story's Test Specs table + the task-138 validation addenda (parts 1-3) — do not
// change a signature here without re-deriving the corresponding golden values in the
// story.
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

export function rng(seed: number): () => number {
  let s = (seed >>> 0) || 1
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0
    return s / 4294967296
  }
}

export function series(n: number, base: number, amp: number, trend: number, seed: number): number[] {
  const r = rng(seed)
  const o: number[] = []
  for (let i = 0; i < n; i++) o.push(Math.max(1, base + trend * i + (r() - 0.5) * amp))
  return o
}

export function lineChart(
  pts: number[],
  W: number,
  H: number,
  padTop: number,
  padBottom: number,
): { line: string; area: string } {
  const mx = Math.max(...pts)
  const mn = Math.min(...pts)
  const rg = mx - mn || 1
  const n = pts.length
  const st = W / (n - 1)
  const y = (v: number): number => padTop + (1 - (v - mn) / rg) * (H - padTop - padBottom)
  const xy = pts.map((p, i): [number, number] => [i * st, y(p)])
  const line = xy.map((p, i) => (i ? 'L' : 'M') + p[0].toFixed(1) + ' ' + p[1].toFixed(1)).join(' ')
  const area = line + ' L' + W + ' ' + H + ' L0 ' + H + ' Z'
  return { line, area }
}

export function fmt(n: number): string {
  return Math.round(n).toLocaleString('en-US')
}

export function naira(n: number): string {
  return '₦' + Math.round(n).toLocaleString('en-US')
}

export function nairaC(n: number): string {
  if (n >= 1e9) return '₦' + (n / 1e9).toFixed(1) + 'B'
  if (n >= 1e6) return '₦' + (n / 1e6).toFixed(2) + 'M'
  if (n >= 1e3) return '₦' + Math.round(n / 1e3) + 'K'
  return '₦' + Math.round(n)
}

// GAP-1 — a third formatter distinct from nairaC: no currency symbol, one decimal
// place with trailing-zero stripping (48214 -> '48.2K', 40000 -> '40K'). The numeric
// round-trip through String() is what drops the '.0' that .toFixed(1) would keep.
export function compactCount(n: number): string {
  if (n >= 1000) return String(Math.round((n / 1000) * 10) / 10) + 'K'
  return String(n)
}

export interface SpendBar {
  h: string
  fill: string
  border: string
  proj: boolean
}

// Prototype lines 888-892: the spend month is 22 actual days + 8 projected, and the
// actual days come from one seeded series. `buildSpendBars` and `spendTotals` MUST read
// the same series or the bars and the headline numbers drift — hence the shared private
// helper rather than two copies of the seed tuple.
const SPEND_ACTUAL = 22
const SPEND_N = 30
function spendSeries(): number[] {
  return series(SPEND_ACTUAL, 148000, 52000, 1500, 213)
}

export function buildSpendBars(): SpendBar[] {
  const spActual = SPEND_ACTUAL
  const spN = SPEND_N
  const spSeries = spendSeries()
  const spMTDnum = spSeries.reduce((a, b) => a + b, 0)
  const spAvg = spMTDnum / spActual
  const spMax = Math.max(Math.max(...spSeries), spAvg) * 1.08
  const spendBars: SpendBar[] = []
  for (let i = 0; i < spN; i++) {
    const proj = i >= spActual
    const val = proj ? spAvg : (spSeries[i] as number)
    spendBars.push({
      h: ((val / spMax) * 100).toFixed(1) + '%',
      fill: proj ? 'var(--accent-tint)' : 'var(--accent)',
      border: proj ? '1px dashed var(--accent)' : 'none',
      proj,
    })
  }
  return spendBars
}

// Prototype lines 890-892. `buildSpendBars` returns only bar geometry, so the two
// headline figures the Spend card and the `Spend MTD` KPI render (₦3.72M month-to-date,
// ₦5.08M projected month-end) had no export to come from. Additive — `buildSpendBars`'s
// signature and output are unchanged.
export interface SpendTotals {
  mtd: number
  proj: number
}

export function spendTotals(): SpendTotals {
  const mtd = spendSeries().reduce((a, b) => a + b, 0)
  return { mtd, proj: (mtd / SPEND_ACTUAL) * SPEND_N }
}

export interface OutcomeCol {
  acc: string
  rej: string
  fail: string
  pend: string
}

export function buildOutcomeColumns(): OutcomeCol[] {
  const or = rng(413)
  const outcomeCols: OutcomeCol[] = []
  for (let i = 0; i < 24; i++) {
    const acc = 0.945 + or() * 0.04
    const rej = (1 - acc) * (0.5 + or() * 0.3)
    const fail = (1 - acc - rej) * 0.5
    const pend = 1 - acc - rej - fail
    outcomeCols.push({
      acc: (acc * 100).toFixed(2) + '%',
      rej: (rej * 100).toFixed(2) + '%',
      fail: (fail * 100).toFixed(2) + '%',
      pend: (pend * 100).toFixed(2) + '%',
    })
  }
  return outcomeCols
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

export function buildRejectionReasons(raw: RejectionInput[]): RejectionBar[] {
  const rejMax = (raw[0] as RejectionInput).count
  return raw.map((r, i) => ({
    label: r.label,
    count: fmt(r.count),
    width: ((r.count / rejMax) * 100).toFixed(1) + '%',
    color: i === 0 ? 'var(--status-red-text)' : i < 3 ? 'var(--accent)' : 'var(--fg-3)',
  }))
}

export function upStrip(seed: number, badIdx: number[]): { fill: string }[] {
  const r = rng(seed)
  const out: { fill: string }[] = []
  for (let i = 0; i < 90; i++) {
    const bad = badIdx.includes(i)
    const warn = !bad && r() > 0.985
    out.push({
      fill: bad
        ? 'var(--status-red-text)'
        : warn
          ? 'var(--status-amber-text)'
          : 'var(--status-green-text)',
    })
  }
  return out
}

// GAP-4 — no prototype source; new derivation reproducing the sidebar string (line
// 1081) and the static billing markup (lines 468-487) literals. The prototype is
// internally inconsistent here (46,820 cleared + 1,020 exports != 48,214 requests) —
// these are independent literals, never derived from one another.
export const SCALE_PLAN: {
  clearedRate: 40
  overageRate: 42
  includedRequests: 40000
  baseFee: 1200000
} = {
  clearedRate: 40,
  overageRate: 42,
  includedRequests: 40000,
  baseFee: 1200000,
}

export function computeBillLine(qty: number, rate: number): number {
  return qty * rate
}

export function computeQuota(
  used: number,
  included: number,
): {
  pct: number
  widthPct: number
  over: number
  detail: string
} {
  // Math.floor, never Math.round: rounding would render 100% while `over` is still 0
  // (39999/40000 -> 99.9975), and would read 121 at the pinned 48214/40000 value.
  const pct = Math.floor((used / included) * 100)
  // widthPct is clamped bar geometry and is deliberately separate from pct, which
  // legitimately reads 120.
  const widthPct = Math.min(100, pct)
  const over = Math.max(0, used - included)
  // Separator is U+00B7 MIDDLE DOT, not an ASCII period.
  const detail = `${compactCount(used)} / ${compactCount(included)} included · ${compactCount(over)} over`
  return { pct, widthPct, over, detail }
}
