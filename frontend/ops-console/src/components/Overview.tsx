import type { Range } from '../types'

// M4-20-02 scaffold. The eyebrow, <h1> and the range control are the screen's
// permanent shell chrome (prototype lines 120–127 and 151–155); the KPI strip
// and charts land in M4-20-03 under `.ops-kpi-strip` / `.ops-overview-grid`.

type Props = {
  range: Range
  onRangeChange: (r: Range) => void
}

const RANGES: Range[] = ['7d', '30d', '90d']

export function Overview({ range, onRangeChange }: Props) {
  return (
    <div className="ops-screen-pad">
      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 20, gap: 24 }}>
        <div>
          <div className="label" style={{ marginBottom: 8 }}>
            / 01 — INTEGRATION HEALTH
          </div>
          <h1 style={{ fontSize: 24, fontWeight: 600, letterSpacing: '-0.03em', margin: 0 }}>Overview</h1>
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
    </div>
  )
}
