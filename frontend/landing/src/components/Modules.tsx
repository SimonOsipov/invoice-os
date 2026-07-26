import { MODULES } from '../data'

// Ported from the prototype's #modules section: a --gradient-hero band, a 16px
// single panel divided by --on-dark-10 hairlines, and cells that carry a bare
// amber glyph (no tile) with no numeral. The cells DO hover — .mod-cell lifts
// its fill to --on-dark-10, which is the design system's "buttons on dark"
// behaviour, plus a brightness/translate on the icon. Motion tokens only.
export function Modules() {
  return (
    <section id="modules" className="band-gradient" style={{ backgroundImage: 'var(--gradient-hero)', color: 'var(--text-on-dark)' }}>
      <div style={{ maxWidth: 1280, margin: '0 auto', padding: '88px 32px' }}>
        <div style={{ marginBottom: 44 }}>
          <div className="eyebrow eyebrow-dark" style={{ marginBottom: 14 }}>
            THE PLATFORM
          </div>
          <h2
            style={{
              fontSize: 40,
              lineHeight: 1.08,
              letterSpacing: '-0.02em',
              margin: '0 0 14px',
              maxWidth: 640,
              color: 'var(--surface-foreground)',
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
            background: 'var(--on-dark-10)',
            border: '1px solid var(--on-dark-10)',
            borderRadius: 'var(--radius-lg)',
            overflow: 'hidden',
          }}
        >
          {MODULES.map((m) => (
            <div
              key={m.title}
              className="mod-cell"
              style={{ background: 'var(--on-dark-5)', padding: '24px 22px 26px', minHeight: 168, display: 'flex', flexDirection: 'column' }}
            >
              <span className="mod-icon" style={{ color: 'var(--accent)', marginBottom: 16, display: 'inline-flex' }}>
                {m.glyph}
              </span>
              <h3 style={{ fontSize: 15, letterSpacing: '-0.01em', margin: '0 0 6px', color: 'var(--surface-foreground)' }}>{m.title}</h3>
              <p className="mod-body" style={{ fontSize: 13, lineHeight: 1.55, color: 'var(--on-dark-70)', margin: 0 }}>
                {m.body}
              </p>
            </div>
          ))}
        </div>
      </div>
    </section>
  )
}
