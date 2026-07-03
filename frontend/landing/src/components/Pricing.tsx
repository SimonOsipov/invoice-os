import { useState } from 'react'
import { Icon } from '../icons'
import { PLANS, PLAN_COLORS } from '../data'

const seg = (active: boolean) => ({
  bg: active ? 'var(--bg-2)' : 'transparent',
  fg: active ? 'var(--fg-1)' : 'var(--fg-3)',
})

export function Pricing() {
  const [annual, setAnnual] = useState(false)
  const m = seg(!annual)
  const a = seg(annual)

  return (
    <section id="pricing" style={{ borderBottom: '1px solid var(--line-1)' }}>
      <div style={{ maxWidth: 1280, margin: '0 auto', padding: '88px 32px' }}>
        <div style={{ textAlign: 'center', marginBottom: 16 }}>
          <div className="label" style={{ marginBottom: 14 }}>
            / 06 — PRICING
          </div>
          <h2 style={{ fontSize: 40, lineHeight: 1.08, letterSpacing: '-0.03em', fontWeight: 600, margin: '0 0 14px' }}>
            Priced by compliance need, not seats.
          </h2>
          <p style={{ fontSize: 16, color: 'var(--fg-2)', margin: '0 0 26px' }}>
            Start with validation and archiving. Activate live MBS/FIRS transmission when you're ready.
          </p>
          <div style={{ display: 'inline-flex', alignItems: 'center', gap: 4, background: 'var(--bg-3)', borderRadius: 999, padding: 4 }}>
            <button
              type="button"
              onClick={() => setAnnual(false)}
              style={{
                border: 0,
                cursor: 'pointer',
                height: 32,
                padding: '0 16px',
                borderRadius: 999,
                fontSize: 13,
                fontWeight: 500,
                fontFamily: 'var(--font-sans)',
                background: m.bg,
                color: m.fg,
              }}
            >
              Monthly
            </button>
            <button
              type="button"
              onClick={() => setAnnual(true)}
              style={{
                border: 0,
                cursor: 'pointer',
                height: 32,
                padding: '0 16px',
                borderRadius: 999,
                fontSize: 13,
                fontWeight: 500,
                fontFamily: 'var(--font-sans)',
                display: 'inline-flex',
                alignItems: 'center',
                gap: 7,
                background: a.bg,
                color: a.fg,
              }}
            >
              Annual{' '}
              <span className="mono" style={{ fontSize: 10, color: 'var(--accent)', background: 'var(--accent-tint)', padding: '1px 5px', borderRadius: 3 }}>
                –2 MONTHS
              </span>
            </button>
          </div>
        </div>
        <div className="ios-grid ios-3" style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 20, marginTop: 40, alignItems: 'stretch' }}>
          {PLANS.map((p) => {
            const c = PLAN_COLORS[p.variant]
            const price = annual ? p.priceAnnual : p.priceMonthly
            const meta = annual ? p.metaAnnual : p.metaMonthly
            return (
              <div
                key={p.name}
                className="ios-price"
                style={{
                  background: c.cardBg,
                  border: `1px solid ${c.cardBorder}`,
                  borderRadius: 8,
                  padding: '28px 26px 30px',
                  display: 'flex',
                  flexDirection: 'column',
                  transition: 'border-color 160ms ease-out',
                }}
              >
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 6 }}>
                  <span style={{ fontSize: 16, fontWeight: 600, color: c.titleColor }}>{p.name}</span>
                  {p.featured && (
                    <span
                      className="mono"
                      style={{ fontSize: 10, fontWeight: 600, letterSpacing: '0.06em', background: 'var(--teal-700)', color: '#fff', padding: '3px 8px', borderRadius: 3 }}
                    >
                      POPULAR
                    </span>
                  )}
                </div>
                <p style={{ fontSize: 13, lineHeight: 1.5, color: c.subColor, margin: '0 0 22px', minHeight: 38 }}>{p.tagline}</p>
                <div style={{ display: 'flex', alignItems: 'baseline', gap: 6, marginBottom: 4 }}>
                  <span style={{ fontSize: 38, fontWeight: 700, letterSpacing: '-0.03em', color: c.titleColor }}>{price}</span>
                  <span className="mono" style={{ fontSize: 12, color: c.subColor }}>
                    {p.unit}
                  </span>
                </div>
                <div className="mono" style={{ fontSize: 11, color: c.subColor, marginBottom: 24 }}>
                  {meta}
                </div>
                <a
                  href="#demo"
                  className="v2-btn"
                  style={{ background: c.btnBg, color: c.btnFg, border: `1px solid ${c.btnBorder}`, width: '100%', justifyContent: 'center', marginBottom: 24 }}
                >
                  {p.cta}
                </a>
                <div style={{ display: 'flex', flexDirection: 'column', gap: 11 }}>
                  {p.features.map((f) => (
                    <div key={f} style={{ display: 'flex', alignItems: 'flex-start', gap: 9 }}>
                      <span style={{ color: c.checkColor, flex: 'none', marginTop: 1 }}>
                        <Icon paths={['M20 6 9 17l-5-5']} size={14} strokeWidth={2} />
                      </span>
                      <span style={{ fontSize: 13, lineHeight: 1.45, color: c.featColor }}>{f}</span>
                    </div>
                  ))}
                </div>
              </div>
            )
          })}
        </div>
      </div>
    </section>
  )
}
