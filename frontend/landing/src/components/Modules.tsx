import { MODULES } from '../data'
import { GLYPHS, Icon } from '../icons'
import { Eyebrow } from './ds/Eyebrow'
import { Section } from './ds/Section'

export function Modules() {
  return (
    <Section tone="dark" id="solution">
      <div style={{ display: 'grid', gap: 20, justifyItems: 'start', marginBottom: 44 }}>
        <Eyebrow tone="dark">THE SOLUTION</Eyebrow>
        <h2 className="t-h2" style={{ margin: 0, color: 'var(--surface-foreground)', maxWidth: 640 }}>
          ASComply is your invoice
          <br /> <span className="t-hl-dark2">compliance solution.</span>
        </h2>
        <p className="t-body" style={{ margin: 0, maxWidth: 560, color: 'var(--surface-body)' }}>
          ASComply sits between your business, your accounting system, your tax adviser and the regulated e-invoicing
          infrastructure.
        </p>
        <p className="t-body" style={{ margin: 0, maxWidth: 560, color: 'var(--surface-body)' }}>
          We help your team validate invoices before they are submitted, manage approvals internally, store audit-ready
          records and submit them to the regulatory bodies.
        </p>
      </div>
      <div
        className="mod-grid"
        style={{
          display: 'grid',
          gap: 1,
          background: 'var(--on-dark-10)',
          border: '1px solid var(--on-dark-10)',
          borderRadius: 'var(--radius-md)',
          overflow: 'hidden',
        }}
      >
        {MODULES.map((m) => (
          <div
            key={m.title}
            className="mod-cell"
            style={{ background: 'var(--surface)', padding: '24px 22px 26px', minHeight: 168, display: 'grid', gap: 8, alignContent: 'start' }}
          >
            <span className="mod-icon" style={{ display: 'inline-flex', justifySelf: 'start', marginBottom: 8, color: 'var(--accent)' }}>
              <Icon paths={GLYPHS[m.icon]} size={20} strokeWidth={2} />
            </span>
            <h3 style={{ margin: 0, fontSize: 15, fontWeight: 700, letterSpacing: 'var(--tracking-card)', color: 'var(--surface-foreground)' }}>
              {m.title}
            </h3>
            <p className="t-body-sm mod-body" style={{ margin: 0, color: 'var(--surface-body)' }}>
              {m.body}
            </p>
          </div>
        ))}
      </div>
    </Section>
  )
}
