import { BrandMark } from '../icons'

const NAV_LINKS = [
  { label: 'How it works', href: '#how' },
  { label: 'Platform', href: '#modules' },
  { label: 'Compliance', href: '#compliance' },
  { label: "Who it's for", href: '#accountants' },
  { label: 'Developers', href: '#developers' },
  { label: 'Pricing', href: '#pricing' },
]

export function Nav({ onSignIn }: { onSignIn: () => void }) {
  return (
    <header
      style={{
        position: 'sticky',
        top: 0,
        zIndex: 50,
        background: 'rgba(247,249,250,0.72)',
        backdropFilter: 'blur(16px)',
        WebkitBackdropFilter: 'blur(16px)',
        borderBottom: '1px solid var(--line-1)',
      }}
    >
      <div
        style={{
          maxWidth: 1280,
          margin: '0 auto',
          padding: '0 32px',
          height: 64,
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
        }}
      >
        <a href="#top" style={{ display: 'flex', alignItems: 'center', gap: 10, color: 'var(--fg-1)' }}>
          <BrandMark size={22} />
          <span style={{ fontWeight: 600, fontSize: 16, letterSpacing: '-0.02em' }}>InvoiceOS</span>
          <span
            className="mono"
            style={{
              fontSize: 10,
              fontWeight: 500,
              letterSpacing: '0.08em',
              color: 'var(--fg-3)',
              border: '1px solid var(--line-2)',
              borderRadius: 3,
              padding: '2px 5px',
            }}
          >
            AFRICA
          </span>
        </a>
        <nav className="ios-hide-mobile" style={{ display: 'flex', alignItems: 'center', gap: 30 }}>
          {NAV_LINKS.map((l) => (
            <a key={l.href} href={l.href} className="ios-link" style={{ fontSize: 14, color: 'var(--fg-2)' }}>
              {l.label}
            </a>
          ))}
        </nav>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          {/* Sign in opens the mock persona picker → OTP → routes to the workspace the
              chosen role may open (task-21). */}
          <button onClick={onSignIn} className="v2-btn v2-btn-ghost" style={{ height: 38, cursor: 'pointer' }}>
            Sign in
          </button>
          <a href="#demo" className="v2-btn v2-btn-primary" style={{ height: 38 }}>
            Book a demo
          </a>
        </div>
      </div>
    </header>
  )
}
