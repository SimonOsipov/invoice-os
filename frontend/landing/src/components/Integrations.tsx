import { PARTNERS } from '../data'
import { Badge } from './ds/Badge'
import { Button } from './ds/Button'
import { Eyebrow } from './ds/Eyebrow'
import { Section } from './ds/Section'

const SQUARE_FILLS = ['var(--ink)', 'var(--teal)', 'var(--teal)', 'var(--ink)']

export function Integrations({ onBookDemo }: { onBookDemo: () => void }) {
  return (
    <Section tone="peach" id="integrations">
      <div style={{ display: 'flex', flexWrap: 'wrap', justifyContent: 'space-between', alignItems: 'flex-end', gap: '24px 48px', marginBottom: 40 }}>
        <div style={{ display: 'grid', gap: 20 }}>
          <Eyebrow>INTEGRATIONS &amp; PARTNERS</Eyebrow>
          <h2 className="t-h2" style={{ margin: 0 }}>
            Your systems.
            <br /> <span className="t-hl-peach">A connected future.</span>
          </h2>
        </div>
      </div>
      <div className="cols6" data-partners style={{ gridTemplateColumns: 'repeat(3, minmax(0, 1fr))', gap: 16 }}>
        {PARTNERS.map((p) => (
          <div
            key={p.mark}
            className="card"
            data-partner
            style={{ borderRadius: 'var(--radius-sm)', padding: 24, display: 'flex', flexDirection: 'column', gap: 22, minHeight: 168 }}
          >
            <div style={{ display: 'flex', alignItems: 'center', gap: 10, minHeight: 34 }}>
              {p.glyph && (
                <span aria-hidden="true" style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 2, width: 22, height: 22, flex: 'none' }}>
                  {SQUARE_FILLS.map((fill, i) => (
                    <span key={i} style={{ background: fill }} />
                  ))}
                </span>
              )}
              <span style={{ fontSize: p.size, fontWeight: p.weight, letterSpacing: p.track ?? '-0.02em', color: 'var(--ink)', lineHeight: 1 }}>{p.mark}</span>
            </div>
            <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', gap: 12, marginTop: 'auto' }}>
              <span className="t-body-sm">{p.desc}</span>
              <Badge tone="progress">In progress</Badge>
            </div>
          </div>
        ))}
      </div>
      <div style={{ marginTop: 24, display: 'flex', flexWrap: 'wrap', justifyContent: 'space-between', alignItems: 'center', gap: '12px 24px' }}>
        <Button variant="text" className="btn-on-peach" onClick={onBookDemo}>
          Discuss your integration →
        </Button>
      </div>
    </Section>
  )
}
