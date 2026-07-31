import { DEFAULT_TAXPAYER_SIZE } from './demoForm'

export function DemoCta({ onBookDemo }: { onBookDemo: () => void }) {
  // No texture here: the system sanctions exactly one, on the hero band only.
  return (
    <section id="demo" style={{ borderBottom: '1px solid var(--line-1)' }}>
      <div style={{ maxWidth: 1280, margin: '0 auto', padding: '96px 32px' }}>
        <div
          className="ios-grid ios-2 ios-demo-card"
          style={{
            background: 'var(--gradient-hero)',
            borderRadius: 'var(--radius-xl)',
            boxShadow: 'var(--shadow-elegant)',
            padding: '64px 56px',
            display: 'grid',
            gridTemplateColumns: '1.2fr 0.8fr',
            gap: 48,
            alignItems: 'center',
            position: 'relative',
            overflow: 'hidden',
          }}
        >
          <div>
            <div className="eyebrow eyebrow-dark" style={{ marginBottom: 16 }}>
              BOOK A DEMO
            </div>
            <h2 style={{ fontSize: 42, lineHeight: 1.06, letterSpacing: '-0.035em', color: 'var(--text-on-dark)', margin: '0 0 16px' }}>
              See your invoices pass
              <br />
              compliance in real time.
            </h2>
            <p style={{ fontSize: 16, lineHeight: 1.6, color: 'var(--on-dark-70)', margin: 0, maxWidth: 440 }}>
              A 20-minute walkthrough with a compliance specialist. Bring a sample invoice file — we'll validate it live.
            </p>
          </div>
          <div style={{ background: 'var(--bg-2)', borderRadius: 'var(--radius-lg)', padding: 24 }}>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
              <div>
                <div className="label" style={{ marginBottom: 6 }}>
                  Full name <span style={{ color: 'var(--status-red-text)' }}>*</span>
                </div>
                <div
                  style={{
                    height: 42,
                    background: 'var(--bg-1)',
                    border: '1px solid var(--line-2)',
                    borderRadius: 'var(--radius-input)',
                    display: 'flex',
                    alignItems: 'center',
                    padding: '0 13px',
                    fontSize: 14,
                    color: 'var(--fg-4)',
                  }}
                >
                  Ada Okafor
                </div>
              </div>
              <div>
                <div className="label" style={{ marginBottom: 6 }}>
                  Work email <span style={{ color: 'var(--status-red-text)' }}>*</span>
                </div>
                <div
                  style={{
                    height: 42,
                    background: 'var(--bg-1)',
                    border: '1px solid var(--line-2)',
                    borderRadius: 'var(--radius-input)',
                    display: 'flex',
                    alignItems: 'center',
                    padding: '0 13px',
                    fontSize: 14,
                    color: 'var(--fg-4)',
                  }}
                >
                  you@company.com
                </div>
              </div>
              <div>
                <div className="label" style={{ marginBottom: 6 }}>
                  Company <span style={{ color: 'var(--status-red-text)' }}>*</span>
                </div>
                <div
                  style={{
                    height: 42,
                    background: 'var(--bg-1)',
                    border: '1px solid var(--line-2)',
                    borderRadius: 'var(--radius-input)',
                    display: 'flex',
                    alignItems: 'center',
                    padding: '0 13px',
                    fontSize: 14,
                    color: 'var(--fg-4)',
                  }}
                >
                  Okafor &amp; Partners
                </div>
              </div>
              <div>
                <div className="label" style={{ marginBottom: 6 }}>
                  Role <span style={{ color: 'var(--fg-4)' }}>(opt.)</span>
                </div>
                <div
                  style={{
                    height: 42,
                    background: 'var(--bg-1)',
                    border: '1px solid var(--line-2)',
                    borderRadius: 'var(--radius-input)',
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'space-between',
                    padding: '0 13px',
                    fontSize: 14,
                    color: 'var(--fg-2)',
                  }}
                >
                  Finance or Accounting lead <span style={{ color: 'var(--fg-3)' }}>▾</span>
                </div>
              </div>
              <div style={{ display: 'flex', gap: 12, alignItems: 'flex-end' }}>
                {/* minWidth: 0 — see the guard on the value below. Without it this flex item keeps
                    min-width:auto, is floored at its own content width, and squeezes its sibling. */}
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div className="label" style={{ marginBottom: 6 }}>
                    Taxpayer size <span style={{ color: 'var(--fg-4)' }}>(opt.)</span>
                  </div>
                  <div
                    style={{
                      height: 42,
                      background: 'var(--bg-1)',
                      border: '1px solid var(--line-2)',
                      borderRadius: 'var(--radius-input)',
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      padding: '0 13px',
                      fontSize: 14,
                      color: 'var(--fg-2)',
                    }}
                  >
                    {/* OVERFLOW GUARD — do not remove. The band string is ~2.7x the 'Medium' this
                        fixed height:42 box was sized for, so unguarded it wraps to 2-3 lines between
                        ~921 and ~1150 and below ~500, and bleeds ~4.5px out of the box. The value
                        needs its OWN element: a bare text node is an ANONYMOUS flex item, which can
                        take neither min-width:0 (so it never shrinks) nor text-overflow (not
                        inherited) — so on the bare node the guard silently does nothing but clip the
                        ▾ caret out of the box. Measured at seven widths by landing-demo.spec.ts E6. */}
                    <span style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', minWidth: 0 }}>
                      {DEFAULT_TAXPAYER_SIZE}
                    </span>
                    <span style={{ color: 'var(--fg-3)' }}>▾</span>
                  </div>
                </div>
                <div style={{ flex: 1 }}>
                  <div className="label" style={{ marginBottom: 6 }}>
                    Monthly invoices <span style={{ color: 'var(--fg-4)' }}>(opt.)</span>
                  </div>
                  <div
                    style={{
                      height: 42,
                      background: 'var(--bg-1)',
                      border: '1px solid var(--line-2)',
                      borderRadius: 'var(--radius-input)',
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      padding: '0 13px',
                      fontSize: 14,
                      color: 'var(--fg-2)',
                    }}
                  >
                    1k–10k <span style={{ color: 'var(--fg-3)' }}>▾</span>
                  </div>
                </div>
              </div>
              <button onClick={onBookDemo} className="v2-btn v2-btn-primary" style={{ width: '100%', justifyContent: 'center', height: 44, marginTop: 4, cursor: 'pointer' }}>
                Book my demo →
              </button>
              <p style={{ fontSize: 12, color: 'var(--fg-3)', textAlign: 'center', margin: '2px 0 0' }}>
                No card required · Data resident in-region
              </p>
            </div>
          </div>
        </div>
      </div>
    </section>
  )
}
