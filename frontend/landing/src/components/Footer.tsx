import { BrandMark } from '../icons'

const COLS = [
  {
    title: 'Platform',
    links: [
      { label: 'Modules', href: '#modules' },
      { label: 'Validation engine', href: '#compliance' },
      { label: 'Open the app', href: '#' }, // stub — Platform app not built in Phase 1
    ],
  },
  {
    title: 'Solutions',
    links: [
      { label: "Who it's for", href: '#accountants' },
      { label: 'For developers', href: '#developers' },
      { label: 'Pricing', href: '#pricing' },
    ],
  },
  {
    title: 'Company',
    links: [
      { label: 'Book a demo', href: '#demo' },
      { label: 'Security', href: '#' },
      { label: 'Status', href: '#' },
    ],
  },
]

export function Footer({ onBookDemo }: { onBookDemo: () => void }) {
  return (
    <footer style={{ background: 'var(--bg-2)' }}>
      <div style={{ maxWidth: 1280, margin: '0 auto', padding: '56px 32px 40px' }}>
        <div
          style={{
            display: 'flex',
            justifyContent: 'space-between',
            gap: 40,
            flexWrap: 'wrap',
            paddingBottom: 36,
            borderBottom: '1px solid var(--line-1)',
          }}
        >
          <div style={{ maxWidth: 300 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14 }}>
              <BrandMark size={20} />
              <span style={{ fontWeight: 600, fontSize: 15 }}>ASComply Africa</span>
            </div>
            <p style={{ fontSize: 13, lineHeight: 1.6, color: 'var(--fg-3)', margin: 0 }}>
              E-invoicing compliance infrastructure for African businesses. Built for Nigeria's MBS, designed to expand.
            </p>
          </div>
          <div style={{ display: 'flex', gap: 56, flexWrap: 'wrap' }}>
            {COLS.map((col) => (
              <div key={col.title}>
                <div className="label" style={{ marginBottom: 14 }}>
                  {col.title}
                </div>
                <div style={{ display: 'flex', flexDirection: 'column', gap: 9 }}>
                  {col.links.map((l) =>
                    l.label === 'Book a demo' ? (
                      <button
                        key={l.label}
                        onClick={onBookDemo}
                        className="ios-link"
                        style={{ fontSize: 13, color: 'var(--fg-2)', textAlign: 'left', background: 'none', border: 0, padding: 0, cursor: 'pointer', fontFamily: 'var(--font-sans)' }}
                      >
                        {l.label}
                      </button>
                    ) : (
                      <a key={l.label} href={l.href} className="ios-link" style={{ fontSize: 13, color: 'var(--fg-2)' }}>
                        {l.label}
                      </a>
                    ),
                  )}
                </div>
              </div>
            ))}
          </div>
        </div>
        <div
          style={{
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'center',
            paddingTop: 24,
            flexWrap: 'wrap',
            gap: 12,
          }}
        >
          <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
            © 2026 ASCOMPLY AFRICA · LAGOS · NG
          </span>
          <span className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', letterSpacing: '0.04em' }}>
            v 1.0 · MBS ADAPTER · SANDBOX
          </span>
        </div>
      </div>
    </footer>
  )
}
