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

import type { JobFilter } from './types'

// The dead-letter callout's visibility rule (proto:1104). It is suppressed under a
// narrowed filter (the filtered view already lists only those rows, so the callout
// would double-report) and while searching (re-drive-all would act on rows the user
// cannot currently see). The prototype tests `!q` on the lowercased query; lowercasing
// preserves emptiness, so this takes the RAW query and must not lowercase it.
export function showDeadLetterCallout(dlCount: number, filter: JobFilter, query: string): boolean {
  return dlCount > 0 && filter === 'all' && !query
}

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


// M4-20-05 (Mode A RED, task-142 E2) — the evidence-bundle derivation. The 8 seed rows
// (invoice/buyer/btin/raw/cleared/desc) are literal, but each row's id/irn/csid/hash/
// prevHash/response are NOT: four different `.slice()` lengths (id -6, irn -5,
// csid/hash -4, prevHash -3) plus `91 - i` index arithmetic interpolated into
// irn/csid/hash/prevHash, plus the VAT split shared with helpers.ts:35-36's reqJSON.
// That is exactly the "plausible-but-wrong number" surface a screenshot cannot catch,
// so it gets RED specs before the component work, same as spendTotals/
// showDeadLetterCallout. Source: `Developer Console.dc.html` (Claude Design project
// 6269a212-5677-4abd-b8a9-08aad10b1c65, read-only) lines 1203-1236 (`evidenceData()`).
//
// `request` is deliberately excluded from `EvidenceBundle` (task-142 E1): it embeds the
// live sandbox/live `env` toggle via the existing `reqJSON(row, env)` (helpers.ts:34),
// so it must be computed per-render in EvidenceDrawer, not frozen at module scope.
// Everything else here is env-independent and safe to derive once.
//
// Both stubs currently throw `new Error('not implemented')` — that IS the correct RED
// reason (assertion/not-implemented), not a compile/import error.

// Dedup target (task-142 E2 "Dedup"): net/vat here is a verbatim copy of
// helpers.ts:35-36's inline reqJSON math. Both reqJSON and the evidence response are
// meant to call this shared helper once wired (not done in this Mode A commit — stubs
// and specs only).
export function vatSplit(raw: number): { net: number; vat: number } {
  const net = Math.round(raw / 1.075)
  return { net, vat: raw - net }
}

export interface EvidenceBundle {
  id: string
  invoice: string
  irn: string
  buyer: string
  btin: string
  raw: number
  value: string
  cleared: string
  desc: string
  csid: string
  hash: string
  prevHash: string
  response: string
}

// proto:1214-1223 — the literal base rows. Everything on the bundle beyond these six
// fields is derived in buildEvidenceBundles below.
const EVIDENCE_SEED = [
  { invoice: 'ZP-INV-0088412', buyer: 'Konga Online Ltd', btin: '20184412-0001', raw: 4120000, cleared: '2026-07-18 09:14', desc: 'Marketplace settlement' },
  { invoice: 'ZP-INV-0088340', buyer: 'Chowdeck Ltd', btin: '20554418-0001', raw: 412700, cleared: '2026-07-18 07:22', desc: 'Delivery commission' },
  { invoice: 'ZP-INV-0088320', buyer: 'Piggyvest', btin: '22887301-0001', raw: 305000, cleared: '2026-07-18 06:58', desc: 'Savings payout fee' },
  { invoice: 'ZP-INV-0088291', buyer: 'MTN Nigeria', btin: '18772300-0001', raw: 22140000, cleared: '2026-07-17 21:40', desc: 'Airtime bulk settlement' },
  { invoice: 'ZP-INV-0088277', buyer: 'ShopRite NG', btin: '22310984-0001', raw: 1980000, cleared: '2026-07-17 18:03', desc: 'POS settlement' },
  { invoice: 'ZP-INV-0088255', buyer: 'GTBank Merchant Svcs', btin: '21004552-0001', raw: 6410000, cleared: '2026-07-17 15:29', desc: 'Card settlement' },
  { invoice: 'ZP-INV-0088231', buyer: 'Bolt Nigeria', btin: '19847720-0001', raw: 884300, cleared: '2026-07-17 12:11', desc: 'Ride commission' },
  { invoice: 'ZP-INV-0088210', buyer: 'Jumia Foods', btin: '20991043-0001', raw: 1145000, cleared: '2026-07-17 09:47', desc: 'Vendor payout' },
]

// proto:1224-1235. Note the four different slice lengths (id -6, irn -5, csid/hash -4,
// prevHash -3) and that `i` is interpolated into csid/hash/prevHash as well as driving
// the `91 - i` IRN sequence.
export function buildEvidenceBundles(): EvidenceBundle[] {
  return EVIDENCE_SEED.map((e, i) => {
    const irn = 'IRN-NG-' + e.invoice.slice(-5) + '-A' + (91 - i)
    const { net, vat } = vatSplit(e.raw)
    return {
      // The row id (`ev_`) is NOT the id reqJSON receives (`sub_`, proto:1232) — the
      // latter exists only to derive the `idem_` idempotency key. Two ids, on purpose.
      id: 'ev_' + e.invoice.slice(-6),
      invoice: e.invoice,
      irn,
      buyer: e.buyer,
      btin: e.btin,
      raw: e.raw,
      value: naira(e.raw),
      cleared: e.cleared,
      desc: e.desc,
      csid: 'MBS-CSID:9f2a' + e.invoice.slice(-4) + 'e1b7c4d0' + i + '9f8e2c5a1f0b6d3e7c9a4b',
      hash: 'sha256:9f' + e.invoice.slice(-4) + 'a3e1b7c4d09f' + i + '8e2c5a1f0b6d3e7c9a4d21b8',
      // Despite the "HASH-CHAINED" copy, this is NOT a chain: prevHash is derived from
      // the row's own invoice + index, never from bundles[i-1].hash (proto:1231).
      // Ported verbatim; charts.test.ts locks the non-chained behaviour in place.
      prevHash: 'sha256:8e' + e.invoice.slice(-3) + 'c2' + i,
      response:
        '{\n  "status": "CLEARED",\n  "irn": "' +
        irn +
        '",\n  "csid": "MBS.9f2a\u2026c7",\n  "cleared_at": "' +
        e.cleared.replace(' ', 'T') +
        ':00Z",\n  "net": ' +
        net +
        ',\n  "vat": ' +
        vat +
        '\n}',
    }
  })
}
