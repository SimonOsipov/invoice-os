import { HERO_CHECKS } from '../data'
import { GLYPHS, Icon } from '../icons'
import { Button } from './ds/Button'
import { Eyebrow } from './ds/Eyebrow'
import { Section } from './ds/Section'

const NOTES = ['Your systems, connected', 'Audit-ready invoice records']

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
        <div
          style={{
            background: 'var(--bg-2)',
            border: '1px solid var(--line-2)',
            borderRadius: 'var(--radius-lg)',
            overflow: 'hidden',
            boxShadow: 'var(--shadow-elegant)',
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
                background: 'linear-gradient(180deg, var(--action-tint), transparent)',
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
            {/* data-tally: stable selector for the failure-count test oracle */}
            <span data-tally="failures" className="mono" style={{ fontSize: 11, color: 'var(--status-red-text)', fontWeight: 600 }}>
              1 ERROR · 1 WARNING
            </span>
            {/* data-tally: stable selector for the passed-count test oracle */}
            <span data-tally="passed" className="mono" style={{ fontSize: 11, color: 'var(--fg-3)' }}>
              14 / 16 CHECKS PASSED
            </span>
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
