import { HERO_CHECKS } from '../data'
import { GLYPHS, Icon, type GlyphName } from '../icons'
import { Badge } from './ds/Badge'
import { Button } from './ds/Button'
import { Eyebrow } from './ds/Eyebrow'
import { IconTile } from './ds/IconTile'
import { Section } from './ds/Section'

const NOTES = ['Your systems, connected', 'Audit-ready invoice records']

const TILES: { icon: GlyphName; tone: 'primary' | 'accent'; title: string; caption: string }[] = [
  { icon: 'check-check', tone: 'primary', title: 'Invoice workflow', caption: 'Validate. Review. Keep the record.' },
  { icon: 'sparkles', tone: 'accent', title: 'Regulatory intelligence', caption: 'AI-supported.' },
]

export function Hero({ onBookDemo }: { onBookDemo: () => void }) {
  return (
    <Section tone="dark" id="top" paddingBlock="clamp(48px, 6vw, 80px) 0">
      <div className="split" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(0, 1.05fr)', gap: 64, alignItems: 'start' }}>
        <div style={{ display: 'grid', gap: 28, justifyItems: 'start' }}>
          <Eyebrow tone="dark">E-INVOICING SOLUTION FOR NIGERIA AND AFRICA</Eyebrow>
          <h1 className="t-h1" style={{ margin: 0, color: 'var(--surface-foreground)' }}>
            Africa moves.
            <br /> Compliance
            <br /> <span className="t-hl">keeps up.</span>
          </h1>
          <p className="t-lead" style={{ margin: 0, maxWidth: 480, color: 'var(--surface-body)' }}>
            Bring invoices, approvals and changing country requirements into one connected solution. Available for Nigeria.
          </p>
          <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '16px 28px' }}>
            <Button variant="accent" onClick={onBookDemo}>
              Book a demo
            </Button>
            <Button variant="ghostDark" href="#platform">
              Explore the platform
            </Button>
          </div>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: '12px 32px', marginTop: 4 }}>
            {NOTES.map((note) => (
              <div key={note} style={{ display: 'flex', alignItems: 'center', gap: 10, fontSize: 14, fontWeight: 600, color: 'var(--surface-body)' }}>
                <span style={{ display: 'inline-flex', color: 'var(--accent)' }}>
                  <Icon paths={GLYPHS.check} size={16} strokeWidth={2} />
                </span>
                {note}
              </div>
            ))}
          </div>
        </div>

        <div style={{ minWidth: 0 }}>
          <div className="card-floating hero-card" style={{ position: 'relative', marginTop: 40, padding: '22px 24px 24px', boxShadow: 'var(--shadow-elegant)' }}>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, marginBottom: 12 }}>
              <span className="t-card-title">ASComply Platform</span>
              <span className="t-meta">Illustrative view</span>
            </div>
            <div
              style={{
                background: 'var(--card)',
                border: '1px solid var(--border)',
                borderRadius: 'var(--radius-md)',
                overflow: 'hidden',
                marginBottom: 16,
              }}
            >
              <div
                style={{
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  gap: 12,
                  padding: '10px 14px',
                  borderBottom: '1px solid var(--border)',
                  background: 'var(--muted)',
                }}
              >
                <span className="t-meta">INV-2026-00481</span>
                <Badge tone="progress" dot>
                  Validating
                </Badge>
              </div>
              <div style={{ position: 'relative', padding: '4px 0', overflow: 'hidden' }}>
                <div className="hero-scan" aria-hidden="true" />
                {HERO_CHECKS.map((c, i) => (
                  <div key={c.label} className="hero-row" style={{ animationDelay: `${i * 120}ms` }}>
                    <span
                      style={{
                        flex: 'none',
                        width: 18,
                        height: 18,
                        borderRadius: 'var(--radius-pill)',
                        display: 'grid',
                        placeItems: 'center',
                        background: c.bg,
                        color: c.fg,
                      }}
                    >
                      <Icon paths={GLYPHS[c.icon]} size={11} strokeWidth={2} />
                    </span>
                    <span style={{ flex: 1, fontSize: 13, color: 'var(--foreground)' }}>{c.label}</span>
                    <span className="t-meta" style={{ color: c.fg, fontWeight: 700 }}>
                      {c.tag}
                    </span>
                  </div>
                ))}
              </div>
              <div
                style={{
                  display: 'flex',
                  flexWrap: 'wrap',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  gap: '4px 12px',
                  padding: '10px 14px',
                  borderTop: '1px solid var(--border)',
                  background: 'var(--muted)',
                }}
              >
                {/* data-tally: hooks for the tally tests in Hero.validationPreview.dom.test.tsx and landing-content.spec.ts */}
                <span data-tally="failures" className="t-meta" style={{ color: 'var(--destructive)', fontWeight: 700 }}>
                  1 ERROR · 1 WARNING
                </span>
                <span data-tally="passed" className="t-meta">
                  14 / 16 CHECKS PASSED
                </span>
              </div>
            </div>
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(210px, 1fr))', gap: '14px 20px' }}>
              {TILES.map((t) => (
                <div key={t.title} style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
                  <IconTile name={t.icon} tone={t.tone} size={40} />
                  <div>
                    <div style={{ fontSize: 14, fontWeight: 700, color: 'var(--ink)' }}>{t.title}</div>
                    <div className="t-caption" style={{ lineHeight: 1.4 }}>
                      {t.caption}
                    </div>
                  </div>
                </div>
              ))}
            </div>
          </div>
        </div>
      </div>
      <div
        style={{
          marginTop: 64,
          padding: '28px 0',
          borderTop: '1px solid var(--on-dark-10)',
          display: 'flex',
          flexWrap: 'wrap',
          justifyContent: 'space-between',
          gap: '12px 24px',
          fontSize: 10,
          fontWeight: 700,
          letterSpacing: 'var(--tracking-eyebrow)',
          textTransform: 'uppercase',
          color: 'var(--eyebrow-on-dark)',
        }}
      >
        <span>Local expertise. Pan-African ambition.</span>
        <a href="#platform" style={{ color: 'var(--eyebrow-on-dark)' }}>
          Explore the platform ↓
        </a>
      </div>
    </Section>
  )
}
