import { DemoLeadForm, DEMO_FORM_CSS } from './DemoLeadForm'

export function DemoCta() {
  // No texture here: the system sanctions exactly one, on the hero band only.
  return (
    <section id="demo" style={{ borderBottom: '1px solid var(--line-1)' }}>
      <div style={{ maxWidth: 1280, margin: '0 auto', padding: '96px 32px' }}>
        <div
          className="ios-grid ios-2 ios-demo-card"
          style={{
            background: 'var(--gradient-hero)',
            borderRadius: 'var(--radius-xl)',
            boxShadow: 'var(--shadow-elegant)',
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
            <div className="eyebrow eyebrow-dark" style={{ marginBottom: 16 }}>
              BOOK A DEMO
            </div>
            <h2 style={{ fontSize: 42, lineHeight: 1.06, letterSpacing: '-0.035em', color: 'var(--text-on-dark)', margin: '0 0 16px' }}>
              See your invoices pass
              <br />
              compliance in real time.
            </h2>
            <p style={{ fontSize: 16, lineHeight: 1.6, color: 'var(--on-dark-70)', margin: 0, maxWidth: 440 }}>
              A 20-minute walkthrough with a compliance specialist. Bring a sample invoice file — we'll validate it live.
            </p>
          </div>
          <div style={{ background: 'var(--bg-2)', borderRadius: 'var(--radius-lg)', padding: 24 }}>
            <style>{DEMO_FORM_CSS}</style>
            <DemoLeadForm idPrefix="dc" variant="card" />
          </div>
        </div>
      </div>
    </section>
  )
}
