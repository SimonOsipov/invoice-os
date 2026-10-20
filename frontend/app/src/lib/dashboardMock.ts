// Mock feed for the dashboard panels that have no backend source.
//
// The rollup has no fields for the readiness ring and bars, 12-week trend, KPI sparklines
// or VAT KPI, so they run on this mock until it does.
//
// This module fabricates the trend's shape/history and the sparkline shapes. The trend's
// endpoint is live, derived from liveScorePct. Panels that CAN be
// sourced from the live rollup are not built here — DashboardActive reads those off
// Rollup directly, so the KPI tiles show real counts with only their sparkline shape
// mocked.
//
// Chart-path maths (spark/trend/chartScore) is restored verbatim from the pre-M4-10
// lib/charts.ts, which ported it from Platform.dc.html ~L1084-1116. The COLOUR tokens
// are NOT verbatim: that code predates the design-system rebuild, where `--accent`
// was re-pointed at amber/ochre and `--accent-tint` was dropped entirely. Teal now
// lives on `--action` / `--action-tint`.

// Deterministic per tenant: the same workspace always sees the same shapes, so the
// mock never flickers between renders or looks like live movement.
function mulberrySeed(name: string): () => number {
  let h = 1779033703 ^ name.length
  for (let i = 0; i < name.length; i++) {
    h = Math.imul(h ^ name.charCodeAt(i), 3432918353)
    h = (h << 13) | (h >>> 19)
  }
  let a = h >>> 0
  return () => {
    a |= 0
    a = (a + 0x6d2b79f5) | 0
    let t = Math.imul(a ^ (a >>> 15), 1 | a)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

export type TrendChart = {
  line: string
  area: string
  grid: string[]
  months: string[]
  now: number
  deltaLabel: string
}

export type MockPanels = {
  sparks: string[]
  chart: TrendChart | null
}

// 88x30 sparkline path. Verbatim from pre-M4-10 lib/charts.ts.
function spark(vals: number[]): string {
  const w = 88
  const h = 30
  const p = 3
  const mn = Math.min(...vals)
  const mx = Math.max(...vals)
  const sp = mx - mn || 1
  return vals
    .map((v, i) => {
      const x = (i / (vals.length - 1)) * (w - 2 * p) + p
      const y = h - p - ((v - mn) / sp) * (h - 2 * p)
      return (i ? 'L' : 'M') + x.toFixed(1) + ' ' + y.toFixed(1)
    })
    .join(' ')
}

function trend(rnd: () => number, dir: number): number[] {
  return Array.from({ length: 12 }, (_, i) => (dir > 0 ? i : 11 - i) + rnd() * 3.2)
}

// 12-week readiness curve ending exactly on `finalScore`.
function chartScore(rnd: () => number, finalScore: number): TrendChart {
  const start = Math.max(22, finalScore - (8 + Math.round(rnd() * 10)))
  const vals = Array.from({ length: 12 }, (_, i) => {
    const t = i / 11
    return start + (finalScore - start) * t + (rnd() - 0.5) * 4
  })
  vals[11] = finalScore
  const cw = 680
  const ch = 176
  const pts = vals.map((v, i): [number, number] => [
    (i / 11) * cw,
    ch - 8 - (Math.max(0, Math.min(100, v)) / 100) * (ch - 26),
  ])
  const line = 'M' + pts.map((p) => p[0].toFixed(1) + ' ' + p[1].toFixed(1)).join(' L ')
  const delta = Math.round(finalScore - vals[0])
  return {
    line,
    area: line + ` L ${cw} ${ch} L 0 ${ch} Z`,
    grid: [0.25, 0.5, 0.75].map((f) => (ch * f).toFixed(1)),
    months: ['April', 'May', 'June'],
    now: finalScore,
    deltaLabel: (delta >= 0 ? '▲ +' + delta : '▼ ' + delta) + ' pts vs 12 wks',
  }
}

// `seed` keys every fabricated value; pass the tenant name so a workspace is stable.
// liveScorePct anchors the trend's endpoint to the real readiness score; null (no
// invoices yet) yields chart: null rather than a curve ending at a fabricated 0.
export function buildMockPanels(seed: string, liveScorePct: number | null): MockPanels {
  const rnd = mulberrySeed(seed || 'workspace')

  return {
    // One per KPI tile, in render order; the last trends down (failures should fall).
    sparks: [spark(trend(rnd, 1)), spark(trend(rnd, 1)), spark(trend(rnd, -1)), spark(trend(rnd, 1))],
    chart: liveScorePct !== null ? chartScore(rnd, liveScorePct) : null,
  }
}
