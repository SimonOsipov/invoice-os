import { MODULES } from '../data'

export function Modules() {
  return (
    <section id="modules" style={{ borderBottom: '1px solid var(--line-1)', background: 'var(--bg-0)' }}>
      <div style={{ maxWidth: 1280, margin: '0 auto', padding: '88px 32px' }}>
        <div style={{ marginBottom: 44 }}>
          <div className="label" style={{ marginBottom: 14 }}>
            / 02 — THE PLATFORM
          </div>
          <h2 style={{ fontSize: 40, lineHeight: 1.08, letterSpacing: '-0.03em', fontWeight: 600, margin: '0 0 14px', maxWidth: 640 }}>
            One compliance operating layer. Twelve modules.
          </h2>
          <p style={{ fontSize: 16, lineHeight: 1.6, color: 'var(--fg-2)', maxWidth: 560, margin: 0 }}>
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
            background: 'var(--line-1)',
            border: '1px solid var(--line-1)',
            borderRadius: 8,
            overflow: 'hidden',
          }}
        >
          {MODULES.map((m) => (
            <div
              key={m.title}
              className="ios-feat"
              style={{ background: 'var(--bg-2)', padding: '24px 22px 26px', minHeight: 168, display: 'flex', flexDirection: 'column' }}
            >
              <span style={{ color: 'var(--accent)', marginBottom: 16 }}>{m.glyph}</span>
              <h3 style={{ fontSize: 15, fontWeight: 600, letterSpacing: '-0.01em', margin: '0 0 6px' }}>{m.title}</h3>
              <p style={{ fontSize: 13, lineHeight: 1.55, color: 'var(--fg-3)', margin: 0 }}>{m.body}</p>
            </div>
          ))}
        </div>
      </div>
    </section>
  )
}
