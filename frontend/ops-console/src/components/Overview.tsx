import type { CSSProperties, ReactNode } from 'react'
import {
  buildOutcomeColumns,
  buildRejectionReasons,
  buildSpendBars,
  fmt,
  lineChart,
  nairaC,
  series,
  spendTotals,
} from '../charts'
import { ARROW_DOWN_ICON, ARROW_UP_ICON, UPDATED_AGO } from '../data'
import type { Range } from '../types'

// Overview — ported against prototype lines 118–246 (markup) and 876–946 (derivations).
//
// Two rules govern every chart on this screen:
//  1. CSS `var()` does NOT resolve inside SVG presentation attributes. Every SVG
//     `fill`/`stroke` carrying a token is therefore written as an inline `style`
//     object — `<path style={{ fill: … }} />` — never as a presentation attribute.
//     That applies to the three `<line>` gridlines too, not just the `<path>`s.
//  2. `var()` IS valid in a plain `<div>` style, so the spend bars, outcome columns,
//     rejection bars and legend swatches keep `background: var(--…)` as-is.
//
// All chart math lives in `charts.ts` (M4-20-01, unit-tested). Nothing here re-derives
// a value — this module is the presentation of tested results plus the prototype's
// literal copy.

type Props = {
  range: Range
  onRangeChange: (r: Range) => void
}

const RANGES: Range[] = ['7d', '30d', '90d']

// UPDATED_AGO (prototype line 1090) moved to data.tsx in M4-20-08: the Status header
// renders the same field, and the prototype models it as a single state value.
const ACCEPT_RATE = '97.1%'

// Prototype line 882. Kept module-local (presentational date formatting, not chart
// math) and deliberately NOT `toLocaleDateString`: locale-dependent output would make
// the visual-regression gate flaky. The prototype's "today" is 18 Jul 2026.
const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']
function dateLabel(daysAgo: number): string {
  const d = new Date(2026, 6, 18)
  d.setDate(d.getDate() - daysAgo)
  return MONTHS[d.getMonth()] + ' ' + d.getDate()
}

// ---------------------------------------------------------------- KPI strip (129–142)
type KpiDef = {
  label: string
  value: string
  delta: string
  up: boolean
  // Three-way on purpose (prototype 942–944): `null` is the neutral treatment, which is
  // what `Spend MTD` renders — spending more is neither good nor bad.
  good: boolean | null
  sub: string
  seed: number
  base: number
  trend: number
}

const SPEND = spendTotals()

const KPI_DEFS: KpiDef[] = [
  { label: 'API requests', value: '48,214', delta: '+12.4%', up: true, good: true, sub: 'vs 42,900 last mo', seed: 11, base: 1500, trend: 40 },
  { label: 'Invoices cleared', value: '46,820', delta: '+11.1%', up: true, good: true, sub: '97.1% of requests', seed: 22, base: 1450, trend: 38 },
  { label: 'Acceptance rate', value: '97.1%', delta: '+0.6pp', up: true, good: true, sub: 'target ≥ 96%', seed: 33, base: 96, trend: 0.1 },
  { label: 'Spend MTD', value: nairaC(SPEND.mtd), delta: '+9.8%', up: true, good: null, sub: 'proj ' + nairaC(SPEND.proj) + ' month-end', seed: 44, base: 120000, trend: 3000 },
  { label: 'Invoice value processed', value: '₦18.7B', delta: '+14.2%', up: true, good: true, sub: 'flowing through FiscalBridge', seed: 55, base: 500, trend: 20 },
  // U+2212 MINUS SIGN, not an ASCII hyphen. Down arrow in green: clearance time falling
  // is good, so the colour follows `good`, never the arrow direction.
  { label: 'Avg clearance time', value: '1.8s', delta: '−0.3s', up: false, good: true, sub: 'p95 across submissions', seed: 66, base: 2.0, trend: -0.03 },
]

type Kpi = {
  label: string
  value: string
  delta: string
  deltaColor: string
  deltaGlyph: ReactNode
  sub: string
  line: string
  area: string
  stroke: string
  fill: string
}

const KPIS: Kpi[] = KPI_DEFS.map((k) => {
  const sp = lineChart(series(12, k.base, k.base * 0.4, k.trend, k.seed), 120, 26, 3, 3)
  return {
    label: k.label,
    value: k.value,
    delta: k.delta,
    deltaColor: k.good === null ? 'var(--fg-2)' : k.good ? 'var(--status-green-text)' : 'var(--status-red-text)',
    deltaGlyph: k.up ? ARROW_UP_ICON : ARROW_DOWN_ICON,
    sub: k.sub,
    line: sp.line,
    area: sp.area,
    stroke: k.good === false ? 'var(--status-red-text)' : k.good === null ? 'var(--fg-3)' : 'var(--accent)',
    fill: k.good === false ? 'var(--status-red-bg)' : k.good === null ? 'var(--bg-3)' : 'var(--accent-tint)',
  }
})

// ------------------------------------------------- range-independent charts (171–242)
const SPEND_BARS = buildSpendBars()
const OUTCOME_COLS = buildOutcomeColumns()

const OUTCOME_LEGEND: { label: string; color: string }[] = [
  { label: 'CLEARED', color: 'var(--status-green-text)' },
  { label: 'REJECTED', color: 'var(--status-red-text)' },
  // Prototype line 911 — a raw hex, not a design token. Left verbatim.
  { label: 'FAILED', color: '#8A1F18' },
  { label: 'PENDING', color: 'var(--status-amber-text)' },
]

const REJ_REASONS = buildRejectionReasons([
  { label: 'Invalid buyer TIN', count: 412 },
  { label: 'VAT math mismatch', count: 287 },
  { label: 'Missing line description', count: 176 },
  { label: 'Duplicate invoice number', count: 98 },
  { label: 'Unsupported currency', count: 44 },
])

const LAT_CHART = lineChart(series(30, 1.55, 0.5, 0.012, 88), 400, 90, 8, 6)

const CARD: CSSProperties = {
  border: '1px solid var(--line-1)',
  background: 'var(--bg-2)',
  borderRadius: 10,
}

export function Overview({ range, onRangeChange }: Props) {
  // Prototype 878–885. The range control genuinely re-derives the series, its total
  // and the five axis labels — it is not decorative.
  const rangeN = range === '7d' ? 7 : range === '90d' ? 90 : 30
  const reqSeries = series(rangeN, 1500, 640, 5, 71).map((v) => Math.round(v))
  const reqTotal = fmt(reqSeries.reduce((a, b) => a + b, 0))
  const reqChart = lineChart(reqSeries, 1000, 240, 12, 6)
  const reqAxis = [0, 1, 2, 3, 4].map((i) => dateLabel(Math.round(rangeN * (1 - i / 4))))
  const periodLabel = range === '7d' ? 'LAST 7 DAYS' : range === '90d' ? 'LAST 90 DAYS' : 'LAST 30 DAYS'

  return (
    <div className="ops-screen-pad">
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 20, gap: 24 }}>
        <div>
          <div className="label" style={{ marginBottom: 8 }}>
            / 01 — INTEGRATION HEALTH
          </div>
          <h1 style={{ fontSize: 24, fontWeight: 600, letterSpacing: '-0.03em', margin: 0 }}>Overview</h1>
        </div>
        <span className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', letterSpacing: '0.05em' }}>
          {periodLabel} · UPDATED {UPDATED_AGO}
        </span>
      </div>

      {/* KPI ROW — prototype 129–142. The grid class carries only @media overrides by
          design (ops.css:128–135), so the base columns are set inline here. */}
      <div className="ops-kpi-strip" style={{ display: 'grid', gridTemplateColumns: 'repeat(6, 1fr)', gap: 12, marginBottom: 16 }}>
        {KPIS.map((k) => (
          <div
            key={k.label}
            style={{ border: '1px solid var(--line-1)', background: 'var(--bg-2)', borderRadius: 9, padding: '14px 15px', display: 'flex', flexDirection: 'column', minHeight: 122 }}
          >
            <div className="label" style={{ lineHeight: 1.3, marginBottom: 9, minHeight: 24 }}>
              {k.label}
            </div>
            <div className="mono" style={{ fontSize: 23, fontWeight: 700, letterSpacing: '-0.02em', color: 'var(--fg-1)', lineHeight: 1 }}>
              {k.value}
            </div>
            <div style={{ marginTop: 7, display: 'flex', alignItems: 'center', gap: 5 }}>
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 2, color: k.deltaColor }}>
                {k.deltaGlyph}
                <span className="mono" style={{ fontSize: 11, fontWeight: 700 }}>
                  {k.delta}
                </span>
              </span>
            </div>
            <div
              className="mono"
              style={{ fontSize: 9.5, color: 'var(--fg-3)', marginTop: 5, letterSpacing: '0.02em', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}
            >
              {k.sub}
            </div>
            <svg viewBox="0 0 120 26" width="100%" height="22" preserveAspectRatio="none" style={{ display: 'block', overflow: 'visible', marginTop: 'auto' }}>
              <path d={k.area} style={{ fill: k.fill }} />
              <path d={k.line} vectorEffect="non-scaling-stroke" strokeLinecap="round" strokeLinejoin="round" style={{ fill: 'none', stroke: k.stroke, strokeWidth: 1.5 }} />
            </svg>
          </div>
        ))}
      </div>

      {/* REQUESTS OVER TIME — prototype 144–167 */}
      <div style={{ ...CARD, padding: '18px 20px 14px', marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 14 }}>
          <div>
            <div style={{ fontSize: 14, fontWeight: 600, marginBottom: 3 }}>API requests over time</div>
            <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
              <span className="mono" style={{ fontSize: 22, fontWeight: 700, letterSpacing: '-0.02em' }}>
                {reqTotal}
              </span>
              <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
                requests · {periodLabel}
              </span>
            </div>
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 4, background: 'var(--bg-1)', border: '1px solid var(--line-1)', borderRadius: 7, padding: 3 }}>
            {RANGES.map((r) => {
              const active = range === r
              return (
                <button
                  key={r}
                  type="button"
                  onClick={() => onRangeChange(r)}
                  className="ops-btn"
                  style={{
                    border: 0,
                    cursor: 'pointer',
                    height: 26,
                    padding: '0 12px',
                    borderRadius: 5,
                    fontFamily: 'var(--font-mono)',
                    fontSize: 10.5,
                    fontWeight: 700,
                    letterSpacing: '0.04em',
                    background: active ? 'var(--bg-2)' : 'transparent',
                    color: active ? 'var(--accent)' : 'var(--fg-3)',
                  }}
                >
                  {r.toUpperCase()}
                </button>
              )
            })}
          </div>
        </div>
        <svg viewBox="0 0 1000 240" width="100%" height="240" preserveAspectRatio="none" style={{ display: 'block', overflow: 'visible' }}>
          {/* Gridlines carry a token too — inline style, same as the paths. */}
          <line x1="0" y1="60" x2="1000" y2="60" vectorEffect="non-scaling-stroke" style={{ stroke: 'var(--line-1)', strokeWidth: 1 }} />
          <line x1="0" y1="120" x2="1000" y2="120" vectorEffect="non-scaling-stroke" style={{ stroke: 'var(--line-1)', strokeWidth: 1 }} />
          <line x1="0" y1="180" x2="1000" y2="180" vectorEffect="non-scaling-stroke" style={{ stroke: 'var(--line-1)', strokeWidth: 1 }} />
          <path d={reqChart.area} style={{ fill: 'var(--accent-tint)' }} />
          <path d={reqChart.line} vectorEffect="non-scaling-stroke" strokeLinecap="round" strokeLinejoin="round" style={{ fill: 'none', stroke: 'var(--accent)', strokeWidth: 2 }} />
        </svg>
        <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 8 }}>
          {reqAxis.map((a, i) => (
            <span key={i} className="mono" style={{ fontSize: 10, color: 'var(--fg-4)' }}>
              {a}
            </span>
          ))}
        </div>
      </div>

      {/* SPEND + OUTCOMES — prototype 170–214 */}
      <div className="ops-overview-grid" style={{ display: 'grid', gridTemplateColumns: '1.15fr 1fr', gap: 16, marginBottom: 16 }}>
        <div style={{ ...CARD, padding: '18px 20px 16px' }}>
          <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 4 }}>
            <div style={{ fontSize: 14, fontWeight: 600 }}>Spend over time</div>
            <span className="mono" style={{ fontSize: 9.5, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
              ₦ FEES / DAY · JUL
            </span>
          </div>
          <div style={{ display: 'flex', alignItems: 'baseline', gap: 14, marginBottom: 16 }}>
            <div>
              <div className="mono" style={{ fontSize: 22, fontWeight: 700, letterSpacing: '-0.02em' }}>
                {nairaC(SPEND.mtd)}
              </div>
              <div className="label" style={{ marginTop: 2 }}>
                Month to date
              </div>
            </div>
            <div>
              <div className="mono" style={{ fontSize: 16, fontWeight: 600, color: 'var(--fg-3)' }}>
                {nairaC(SPEND.proj)}
              </div>
              <div className="label" style={{ marginTop: 2 }}>
                Projected month-end
              </div>
            </div>
          </div>
          <div style={{ display: 'flex', alignItems: 'flex-end', gap: 3, height: 150 }}>
            {SPEND_BARS.map((b, i) => (
              <div
                key={i}
                className="ops-bar"
                style={{ flex: 1, height: b.h, background: b.fill, border: b.border, borderRadius: '2px 2px 0 0', minHeight: 2 }}
              />
            ))}
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 16, marginTop: 12 }}>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
              <span style={{ width: 10, height: 10, borderRadius: 2, background: 'var(--accent)' }} />
              <span className="mono" style={{ fontSize: 10, color: 'var(--fg-3)' }}>
                ACTUAL · 22 DAYS
              </span>
            </span>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
              <span style={{ width: 10, height: 10, borderRadius: 2, background: 'var(--accent-tint)', border: '1px dashed var(--accent)' }} />
              <span className="mono" style={{ fontSize: 10, color: 'var(--fg-3)' }}>
                PROJECTED
              </span>
            </span>
          </div>
        </div>

        <div style={{ ...CARD, padding: '18px 20px 16px' }}>
          <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 16 }}>
            <div>
              <div style={{ fontSize: 14, fontWeight: 600, marginBottom: 3 }}>Submission outcomes</div>
              <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
                last 24 days · % of daily volume
              </div>
            </div>
            <span className="mono" style={{ fontSize: 22, fontWeight: 700, letterSpacing: '-0.02em', color: 'var(--status-green-text)' }}>
              {ACCEPT_RATE}
            </span>
          </div>
          <div style={{ display: 'flex', gap: 4, height: 150, alignItems: 'stretch' }}>
            {OUTCOME_COLS.map((c, i) => (
              <div key={i} style={{ flex: 1, display: 'flex', flexDirection: 'column', borderRadius: 2, overflow: 'hidden' }}>
                {/* Stack order top→bottom is pend, fail, rej, acc (prototype 203–206) —
                    deliberately NOT the legend order. */}
                <div style={{ height: c.pend, background: 'var(--status-amber-text)' }} />
                <div style={{ height: c.fail, background: '#8A1F18' }} />
                <div style={{ height: c.rej, background: 'var(--status-red-text)' }} />
                <div style={{ height: c.acc, background: 'var(--status-green-text)' }} />
              </div>
            ))}
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 14, marginTop: 12, flexWrap: 'wrap' }}>
            {OUTCOME_LEGEND.map((l) => (
              <span key={l.label} style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                <span style={{ width: 9, height: 9, borderRadius: 2, background: l.color }} />
                <span className="mono" style={{ fontSize: 10, color: 'var(--fg-3)' }}>
                  {l.label}
                </span>
              </span>
            ))}
          </div>
        </div>
      </div>

      {/* REJECTIONS + LATENCY — prototype 217–243 */}
      <div className="ops-overview-grid" style={{ display: 'grid', gridTemplateColumns: '1.15fr 1fr', gap: 16 }}>
        <div style={{ ...CARD, padding: '18px 20px' }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 4 }}>
            <div style={{ fontSize: 14, fontWeight: 600 }}>Top rejection reasons</div>
            <span className="mono" style={{ fontSize: 9.5, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
              CAUGHT PRE-SUBMISSION
            </span>
          </div>
          <p style={{ fontSize: 12, color: 'var(--fg-3)', margin: '0 0 16px', lineHeight: 1.5 }}>
            Errors FiscalBridge caught before the tax authority — protecting your acceptance rate.
          </p>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 13 }}>
            {REJ_REASONS.map((r) => (
              <div key={r.label}>
                <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', marginBottom: 5 }}>
                  <span style={{ fontSize: 12.5, fontWeight: 500, color: 'var(--fg-1)' }}>{r.label}</span>
                  <span className="mono" style={{ fontSize: 12, fontWeight: 600, color: 'var(--fg-2)' }}>
                    {r.count}
                  </span>
                </div>
                <div style={{ height: 7, background: 'var(--bg-3)', borderRadius: 4, overflow: 'hidden' }}>
                  <div className="ops-bar" style={{ width: r.width, height: '100%', background: r.color, borderRadius: 4 }} />
                </div>
              </div>
            ))}
          </div>
        </div>

        <div style={{ ...CARD, padding: '18px 20px', display: 'flex', flexDirection: 'column' }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 4 }}>
            <div style={{ fontSize: 14, fontWeight: 600 }}>Clearance latency</div>
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
              <span style={{ width: 7, height: 7, borderRadius: 99, background: 'var(--status-amber-text)' }} />
              <span className="mono" style={{ fontSize: 9.5, fontWeight: 700, color: 'var(--status-amber-text)', letterSpacing: '0.04em' }}>
                ELEVATED
              </span>
            </span>
          </div>
          <div style={{ display: 'flex', alignItems: 'baseline', gap: 6, margin: '10px 0 4px' }}>
            <span className="mono" style={{ fontSize: 30, fontWeight: 700, letterSpacing: '-0.02em' }}>
              1.8
            </span>
            <span className="mono" style={{ fontSize: 13, color: 'var(--fg-3)' }}>
              s p95
            </span>
          </div>
          <div className="mono" style={{ fontSize: 10.5, color: 'var(--fg-3)', marginBottom: 16 }}>
            avg 1.2s · 30-day trend
          </div>
          <svg viewBox="0 0 400 90" width="100%" height="90" preserveAspectRatio="none" style={{ display: 'block', overflow: 'visible', marginTop: 'auto' }}>
            <path d={LAT_CHART.area} style={{ fill: 'var(--status-amber-bg)' }} />
            <path
              d={LAT_CHART.line}
              vectorEffect="non-scaling-stroke"
              strokeLinecap="round"
              strokeLinejoin="round"
              style={{ fill: 'none', stroke: 'var(--status-amber-text)', strokeWidth: 1.8 }}
            />
          </svg>
          <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 8 }}>
            <span className="mono" style={{ fontSize: 10, color: 'var(--fg-4)' }}>
              30d ago
            </span>
            <span className="mono" style={{ fontSize: 10, color: 'var(--fg-4)' }}>
              today
            </span>
          </div>
        </div>
      </div>
    </div>
  )
}
