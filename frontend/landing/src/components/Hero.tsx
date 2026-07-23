import { HERO_CHECKS } from '../data'

export function Hero({ onBookDemo }: { onBookDemo: () => void }) {
  return (
    <section id="top" className="grid-bg" style={{ position: 'relative', borderBottom: '1px solid var(--line-1)' }}>
      <div
        className="ios-grid ios-2"
        style={{
          maxWidth: 1280,
          margin: '0 auto',
          padding: '80px 32px 72px',
          display: 'grid',
          gridTemplateColumns: '1.02fr 0.98fr',
          gap: 56,
          alignItems: 'center',
        }}
      >
        {/* left */}
        <div>
          <div
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              gap: 8,
              border: '1px solid var(--status-amber-border)',
              background: 'var(--status-amber-bg)',
              borderRadius: 999,
              padding: '5px 12px 5px 10px',
              marginBottom: 24,
            }}
          >
            <span
              style={{
                width: 7,
                height: 7,
                borderRadius: 99,
                background: 'var(--status-amber-text)',
                animation: 'pulseDot 1.6s ease-in-out infinite',
              }}
            />
            <span className="mono" style={{ fontSize: 11, fontWeight: 600, letterSpacing: '0.06em', color: 'var(--status-amber-text)' }}>
              MBS MANDATE · PHASED ROLLOUT ACTIVE
            </span>
          </div>
          <h1 className="ios-hero-h1" style={{ fontSize: 58, lineHeight: 1.02, letterSpacing: '-0.04em', fontWeight: 600, margin: '0 0 22px' }}>
            Get e-invoicing ready
            <br />
            without replacing your
            <br />
            accounting system.
          </h1>
          <p style={{ fontSize: 18, lineHeight: 1.6, color: 'var(--fg-2)', margin: '0 0 32px', maxWidth: 520 }}>
            FiscalBridge Africa is the compliance layer between your business and Nigeria's Merchant Buyer Solution. Create,
            validate, approve, archive, and transmit compliant invoices — through the dashboard or the API.
          </p>
          <div style={{ display: 'flex', alignItems: 'center', gap: 14, marginBottom: 36 }}>
            <button onClick={onBookDemo} className="v2-btn v2-btn-primary" style={{ height: 46, padding: '0 22px', fontSize: 15, cursor: 'pointer' }}>
              Book a demo →
            </button>
            <a href="#" className="v2-btn v2-btn-ghost" style={{ height: 46, padding: '0 22px', fontSize: 15 }}>
              Explore the platform
            </a>
          </div>
          {/* rollout timeline */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 0, maxWidth: 480 }}>
            <div style={{ flex: 1 }}>
              <div className="label" style={{ marginBottom: 6 }}>
                Large taxpayers
              </div>
              <div style={{ height: 4, background: 'var(--accent)', borderRadius: 'var(--radius-xs)' }} />
              <div className="mono" style={{ fontSize: 11, color: 'var(--accent)', marginTop: 6, fontWeight: 600 }}>
                LIVE
              </div>
            </div>
            <div style={{ width: 16 }} />
            <div style={{ flex: 1 }}>
              <div className="label" style={{ marginBottom: 6 }}>
                Medium taxpayers
              </div>
              <div style={{ height: 4, background: 'var(--status-amber-text)', opacity: 0.55, borderRadius: 'var(--radius-xs)' }} />
              <div className="mono" style={{ fontSize: 11, color: 'var(--status-amber-text)', marginTop: 6, fontWeight: 600 }}>
                NEXT
              </div>
            </div>
            <div style={{ width: 16 }} />
            <div style={{ flex: 1 }}>
              <div className="label" style={{ marginBottom: 6 }}>
                Small / SME
              </div>
              <div style={{ height: 4, background: 'var(--line-3)', borderRadius: 'var(--radius-xs)' }} />
              <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 6, fontWeight: 600 }}>
                PLANNED
              </div>
            </div>
          </div>
        </div>

        {/* right: product mock */}
        <div style={{ position: 'relative' }}>
          <div
            style={{
              background: 'var(--bg-2)',
              border: '1px solid var(--line-2)',
              borderRadius: 'var(--radius-xl)',
              overflow: 'hidden',
              boxShadow: '0 1px 0 rgba(20,23,26,0.02), 0 24px 48px -24px rgba(20,23,26,0.18)',
            }}
          >
            {/* window chrome */}
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                padding: '11px 14px',
                borderBottom: '1px solid var(--line-1)',
                background: 'var(--bg-1)',
              }}
            >
              <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
                  INV-2026-00481
                </span>
              </div>
              <div
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: 6,
                  background: 'var(--status-amber-bg)',
                  border: '1px solid var(--status-amber-border)',
                  borderRadius: 999,
                  padding: '3px 9px',
                }}
              >
                <span style={{ width: 6, height: 6, borderRadius: 99, background: 'var(--status-amber-text)' }} />
                <span className="mono" style={{ fontSize: 10, fontWeight: 600, color: 'var(--status-amber-text)', letterSpacing: '0.05em' }}>
                  VALIDATING
                </span>
              </div>
            </div>
            {/* validation rows */}
            <div style={{ position: 'relative', padding: '6px 0' }}>
              <div
                style={{
                  position: 'absolute',
                  left: 0,
                  right: 0,
                  top: 0,
                  height: 28,
                  background: 'linear-gradient(180deg, var(--accent-tint), transparent)',
                  pointerEvents: 'none',
                  animation: 'scanline 2.6s var(--ease-out) infinite',
                }}
              />
              {HERO_CHECKS.map((c, i) => (
                <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 11, padding: '9px 16px' }}>
                  <span
                    style={{
                      flex: 'none',
                      width: 18,
                      height: 18,
                      borderRadius: 99,
                      display: 'grid',
                      placeItems: 'center',
                      background: c.bg,
                      color: c.fg,
                    }}
                  >
                    {c.icon}
                  </span>
                  <span style={{ flex: 1, fontSize: 13, color: 'var(--fg-2)' }}>{c.label}</span>
                  <span className="mono" style={{ fontSize: 11, color: c.fg, fontWeight: 500 }}>
                    {c.tag}
                  </span>
                </div>
              ))}
            </div>
            {/* footer summary */}
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                padding: '13px 16px',
                borderTop: '1px solid var(--line-1)',
                background: 'var(--bg-1)',
              }}
            >
              <span className="mono" style={{ fontSize: 11, color: 'var(--status-red-text)', fontWeight: 600 }}>
                1 ERROR · 1 WARNING
              </span>
              <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
                14 / 16 CHECKS PASSED
              </span>
            </div>
          </div>
          {/* floating ref tag */}
          <div
            style={{
              position: 'absolute',
              bottom: -16,
              right: -14,
              background: 'var(--slate-900)',
              color: '#fff',
              borderRadius: 'var(--radius-lg)',
              padding: '10px 14px',
              boxShadow: '0 16px 32px -16px rgba(20,23,26,0.5)',
            }}
          >
            <div className="mono" style={{ fontSize: 10, letterSpacing: '0.08em', color: 'var(--teal-300)', marginBottom: 2 }}>
              FIRS REFERENCE
            </div>
            <div className="mono" style={{ fontSize: 13, fontWeight: 600, letterSpacing: '0.02em' }}>
              CSID · pending transmit
            </div>
          </div>
        </div>
      </div>
    </section>
  )
}
