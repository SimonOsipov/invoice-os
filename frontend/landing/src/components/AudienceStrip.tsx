import { Section } from './ds/Section'

const AUDIENCES = ['Finance teams', 'Accounting firms', 'Growing businesses', 'Fintech', 'Technology partners']

export function AudienceStrip() {
  // data-strip: stable selector for the audience-strip test oracle
  return (
    <Section tone="sage" paddingBlock="30px">
      <div data-strip="audience" style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '16px 48px' }}>
        <div
          style={{
            flex: 'none',
            fontSize: 10,
            fontWeight: 700,
            letterSpacing: 'var(--tracking-eyebrow)',
            textTransform: 'uppercase',
            color: 'var(--primary)',
            whiteSpace: 'nowrap',
          }}
        >
          Built for the way your business works
        </div>
        <div style={{ flex: '1 1 520px', display: 'flex', flexWrap: 'wrap', justifyContent: 'space-between', gap: '12px 32px' }}>
          {AUDIENCES.map((a) => (
            <span
              key={a}
              style={{ fontSize: 16, fontWeight: 700, letterSpacing: 'var(--tracking-card)', color: 'var(--ink)', whiteSpace: 'nowrap' }}
            >
              {a}
            </span>
          ))}
        </div>
      </div>
    </Section>
  )
}
