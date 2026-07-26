import { MODULES } from '../data'

export function Modules() {
  return (
    <section id="modules" className="band-gradient" style={{ background: 'var(--gradient-hero)', color: 'var(--text-on-dark)' }}>
      <div style={{ maxWidth: 1280, margin: '0 auto', padding: '88px 32px' }}>
        <div style={{ marginBottom: 44 }}>
          <div className="eyebrow eyebrow-dark" style={{ marginBottom: 14 }}>
            02 — THE PLATFORM
          </div>
          <h2
            style={{
              fontSize: 40,
              lineHeight: 1.08,
              letterSpacing: '-0.03em',
              margin: '0 0 14px',
              maxWidth: 640,
              color: 'var(--text-on-dark)',
            }}
          >
            One compliance operating layer. Twelve modules.
          </h2>
          <p style={{ fontSize: 16, lineHeight: 1.6, color: 'var(--on-dark-70)', maxWidth: 560, margin: 0 }}>
            Everything finance, tax, and engineering teams need to issue audit-ready invoices — and nothing they have to
            rip out.
          </p>
        </div>
        <div
          className="ios-grid ios-4"
          style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(4, 1fr)',
            gap: 1,
            background: 'var(--border-on-dark)',
            border: '1px solid var(--border-on-dark)',
            borderRadius: 'var(--radius-lg)',
            overflow: 'hidden',
          }}
        >
          {MODULES.map((m, i) => (
            <div
              key={m.title}
              style={{ background: 'var(--on-dark-5)', padding: '24px 22px 26px', minHeight: 168, display: 'flex', flexDirection: 'column' }}
            >
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
                <span
                  style={{
                    flex: 'none',
                    width: 36,
                    height: 36,
                    borderRadius: 'var(--radius-sm)',
                    background: 'var(--on-dark-10)',
                    color: 'var(--text-on-dark)',
                    display: 'grid',
                    placeItems: 'center',
                  }}
                >
                  {m.glyph}
                </span>
                <span className="mono" style={{ fontSize: 12, fontWeight: 600, color: 'var(--accent)' }}>
                  {String(i + 1).padStart(2, '0')}
                </span>
              </div>
              <h3 style={{ fontSize: 15, letterSpacing: '-0.01em', margin: '0 0 6px', color: 'var(--text-on-dark)' }}>{m.title}</h3>
              <p style={{ fontSize: 13, lineHeight: 1.55, color: 'var(--on-dark-70)', margin: 0 }}>{m.body}</p>
            </div>
          ))}
        </div>
      </div>
    </section>
  )
}
