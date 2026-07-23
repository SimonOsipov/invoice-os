export function DemoCta({ onBookDemo }: { onBookDemo: () => void }) {
  return (
    <section id="demo" className="dot-bg" style={{ borderBottom: '1px solid var(--line-1)' }}>
      <div style={{ maxWidth: 1280, margin: '0 auto', padding: '96px 32px' }}>
        <div
          className="ios-grid ios-2 ios-demo-card"
          style={{
            background: 'var(--slate-900)',
            borderRadius: 'var(--radius-xl)',
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
            <div className="mono" style={{ fontSize: 11, letterSpacing: '0.08em', color: 'var(--teal-300)', marginBottom: 16 }}>
              BOOK A DEMO
            </div>
            <h2 style={{ fontSize: 42, lineHeight: 1.06, letterSpacing: '-0.035em', fontWeight: 600, color: '#fff', margin: '0 0 16px' }}>
              See your invoices pass
              <br />
              compliance in real time.
            </h2>
            <p style={{ fontSize: 16, lineHeight: 1.6, color: 'var(--slate-300)', margin: 0, maxWidth: 440 }}>
              A 20-minute walkthrough with a compliance specialist. Bring a sample invoice file — we'll validate it live.
            </p>
          </div>
          <div style={{ background: 'var(--bg-2)', borderRadius: 'var(--radius-xl)', padding: 24 }}>
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
                    borderRadius: 'var(--radius-xl)',
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
                    borderRadius: 'var(--radius-xl)',
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
                    borderRadius: 'var(--radius-xl)',
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
                    borderRadius: 'var(--radius-xl)',
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
                <div style={{ flex: 1 }}>
                  <div className="label" style={{ marginBottom: 6 }}>
                    Taxpayer size <span style={{ color: 'var(--fg-4)' }}>(opt.)</span>
                  </div>
                  <div
                    style={{
                      height: 42,
                      background: 'var(--bg-1)',
                      border: '1px solid var(--line-2)',
                      borderRadius: 'var(--radius-xl)',
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      padding: '0 13px',
                      fontSize: 14,
                      color: 'var(--fg-2)',
                    }}
                  >
                    Medium <span style={{ color: 'var(--fg-3)' }}>▾</span>
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
                      borderRadius: 'var(--radius-xl)',
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
