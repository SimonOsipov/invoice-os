import { Logo } from './ds/Logo'

// Only sections on the page; App.render.test.tsx AN-03 pins that each href resolves.
export const PLATFORM_LINKS: { label: string; href: string }[] = [
  { label: 'Invoice workflows', href: '#platform' },
  { label: 'Country roadmap', href: '#coverage' },
  { label: 'AI-supported intelligence', href: '#intelligence' },
]

// Only in-page anchors take the prefix; /privacy is already absolute.
export function footerHref(href: string, prefix: string): string {
  return href.startsWith('#') && href !== '#' ? `${prefix}${href}` : href
}

const column = { display: 'grid', gap: 12, alignContent: 'start' } as const

export function Footer({
  onBookDemo,
  onSignIn = () => undefined,
  hrefPrefix = '',
  onCookieChoices = () => undefined,
}: {
  onBookDemo: () => void
  onSignIn?: () => void
  hrefPrefix?: string
  onCookieChoices?: () => void
}) {
  return (
    <footer style={{ background: 'var(--background)', borderTop: '1px solid var(--header-border)' }}>
      <div className="container" style={{ paddingBlock: '56px 32px' }}>
        <div style={{ display: 'flex', flexWrap: 'wrap', justifyContent: 'space-between', gap: '40px 64px', paddingBottom: 40 }}>
          <div style={{ display: 'grid', gap: 18, maxWidth: 320 }}>
            <Logo size={28} />
            <p className="t-body-sm" style={{ margin: 0 }}>
              Clarity for every invoice.
              <br /> Confidence for your business.
            </p>
          </div>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: '40px 80px' }}>
            <div style={column}>
              <span className="t-step">Platform</span>
              {PLATFORM_LINKS.map((l) => (
                <a key={l.href} href={footerHref(l.href, hrefPrefix)} className="a-link">
                  {l.label}
                </a>
              ))}
            </div>
            <div style={column}>
              <span className="t-step">Connect</span>
              <button type="button" className="a-link" onClick={onBookDemo}>
                Book a demo
              </button>
              <button type="button" className="a-link" onClick={onSignIn}>
                Open the cockpit
              </button>
              <button type="button" className="a-link" onClick={onBookDemo}>
                Contact ASComply
              </button>
            </div>
          </div>
        </div>
        <div
          style={{
            paddingTop: 24,
            borderTop: '1px solid var(--border)',
            display: 'flex',
            flexWrap: 'wrap',
            justifyContent: 'space-between',
            gap: '12px 24px',
            fontSize: 13,
            color: 'var(--muted-foreground)',
          }}
        >
          <span>© 2026 ASComply Africa Limited · Lagos, Nigeria</span>
          <span style={{ display: 'flex', flexWrap: 'wrap', gap: 20 }}>
            <a href="/privacy" className="a-link" style={{ fontSize: 13 }}>
              Privacy policy
            </a>
            <button type="button" className="a-link" onClick={onCookieChoices} style={{ fontSize: 13 }}>
              Cookie choices
            </button>
          </span>
        </div>
      </div>
    </footer>
  )
}
