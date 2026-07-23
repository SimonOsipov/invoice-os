import { RULES } from '../data'

const BARS = [
  { label: 'Field completeness', pct: '96%', color: 'var(--status-green-text)' },
  { label: 'Tax accuracy (VAT / WHT)', pct: '91%', color: 'var(--status-green-text)' },
  { label: 'Transmit-ready invoices', pct: '74%', color: 'var(--status-amber-text)' },
]

export function Compliance() {
  return (
    <section id="compliance" style={{ borderBottom: '1px solid var(--line-1)' }}>
      <div
        className="ios-grid ios-2"
        style={{
          maxWidth: 1280,
          margin: '0 auto',
          padding: '88px 32px',
          display: 'grid',
          gridTemplateColumns: '0.92fr 1.08fr',
          gap: 64,
          alignItems: 'center',
        }}
      >
        <div>
          <div className="label" style={{ marginBottom: 14 }}>
            / 03 — MBS READINESS
          </div>
          <h2 style={{ fontSize: 38, lineHeight: 1.1, letterSpacing: '-0.03em', fontWeight: 600, margin: '0 0 18px' }}>
            Know exactly how compliant you are — before the auditor does.
          </h2>
          <p style={{ fontSize: 16, lineHeight: 1.65, color: 'var(--fg-2)', margin: '0 0 28px' }}>
            The validation engine checks every invoice against Nigeria-specific rules: TIN and VAT formats, WHT logic,
            mandatory buyer/seller fields, duplicate numbers, totals, and date sequencing. You get a live readiness
            score, not a year-end surprise.
          </p>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
            {RULES.map((r) => (
              <div key={r.title} style={{ display: 'flex', alignItems: 'flex-start', gap: 12 }}>
                <span
                  style={{
                    flex: 'none',
                    width: 22,
                    height: 22,
                    borderRadius: 'var(--radius-md)',
                    background: 'var(--accent-tint)',
                    color: 'var(--accent)',
                    display: 'grid',
                    placeItems: 'center',
                    marginTop: 1,
                  }}
                >
                  {r.glyph}
                </span>
                <div>
                  <div style={{ fontSize: 14, fontWeight: 600, marginBottom: 1 }}>{r.title}</div>
                  <div style={{ fontSize: 13, color: 'var(--fg-3)', lineHeight: 1.5 }}>{r.body}</div>
                </div>
              </div>
            ))}
          </div>
        </div>

        {/* readiness card */}
        <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-2)', borderRadius: 'var(--radius-xl)', overflow: 'hidden' }}>
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
              padding: '16px 22px',
              borderBottom: '1px solid var(--line-1)',
            }}
          >
            <span className="label">Compliance readiness</span>
            <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
              UPDATED 2 MIN AGO
            </span>
          </div>
          <div
            style={{
              padding: '28px 22px',
              display: 'grid',
              gridTemplateColumns: 'auto 1fr',
              gap: 28,
              alignItems: 'center',
              borderBottom: '1px solid var(--line-1)',
            }}
          >
            <div style={{ position: 'relative', width: 132, height: 132 }}>
              <svg width="132" height="132" viewBox="0 0 132 132" style={{ transform: 'rotate(-90deg)' }}>
                <circle cx="66" cy="66" r="58" fill="none" stroke="var(--bg-3)" strokeWidth="12" />
                <circle
                  cx="66"
                  cy="66"
                  r="58"
                  fill="none"
                  stroke="var(--accent)"
                  strokeWidth="12"
                  strokeLinecap="round"
                  strokeDasharray="364.4"
                  strokeDashoffset="47.4"
                />
              </svg>
              <div
                style={{
                  position: 'absolute',
                  inset: 0,
                  display: 'flex',
                  flexDirection: 'column',
                  alignItems: 'center',
                  justifyContent: 'center',
                }}
              >
                <span style={{ fontSize: 34, fontWeight: 700, letterSpacing: '-0.03em', lineHeight: 1 }}>
                  87<span style={{ fontSize: 16, color: 'var(--fg-3)' }}>%</span>
                </span>
                <span className="mono" style={{ fontSize: 10, color: 'var(--fg-3)', marginTop: 2, letterSpacing: '0.06em' }}>
                  READY
                </span>
              </div>
            </div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
              {BARS.map((b) => (
                <div key={b.label}>
                  <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 5 }}>
                    <span style={{ fontSize: 13, color: 'var(--fg-2)' }}>{b.label}</span>
                    <span className="mono" style={{ fontSize: 12, fontWeight: 600, color: b.color }}>
                      {b.pct}
                    </span>
                  </div>
                  <div style={{ height: 6, background: 'var(--bg-3)', borderRadius: 'var(--radius-sm)' }}>
                    <div style={{ width: b.pct, height: '100%', background: b.color, borderRadius: 'var(--radius-sm)' }} />
                  </div>
                </div>
              ))}
            </div>
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)' }}>
            <div style={{ padding: '18px 22px', borderRight: '1px solid var(--line-1)' }}>
              <div className="mono" style={{ fontSize: 22, fontWeight: 600, letterSpacing: '-0.02em' }}>
                1,284
              </div>
              <div className="label" style={{ marginTop: 4 }}>
                Validated
              </div>
            </div>
            <div style={{ padding: '18px 22px', borderRight: '1px solid var(--line-1)' }}>
              <div className="mono" style={{ fontSize: 22, fontWeight: 600, letterSpacing: '-0.02em', color: 'var(--status-red-text)' }}>
                23
              </div>
              <div className="label" style={{ marginTop: 4 }}>
                Failing
              </div>
            </div>
            <div style={{ padding: '18px 22px' }}>
              <div className="mono" style={{ fontSize: 22, fontWeight: 600, letterSpacing: '-0.02em' }}>
                ₦48.2M
              </div>
              <div className="label" style={{ marginTop: 4 }}>
                VAT tracked
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  )
}
