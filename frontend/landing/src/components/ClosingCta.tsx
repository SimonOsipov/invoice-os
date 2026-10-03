import { Button } from './ds/Button'
import { Eyebrow } from './ds/Eyebrow'
import { Section } from './ds/Section'

export function ClosingCta({ onBookDemo }: { onBookDemo: () => void }) {
  return (
    <Section tone="cream" paddingBlock="0 var(--section-y)">
      <div
        data-closing
        style={{
          position: 'relative',
          overflow: 'hidden',
          background: 'var(--peach-card)',
          borderRadius: 'var(--radius-md)',
          minHeight: 446,
          padding: 'clamp(32px, 6vw, 72px)',
          display: 'flex',
          alignItems: 'center',
          color: 'var(--accent-foreground)',
        }}
      >
        <div style={{ position: 'relative', zIndex: 1, display: 'grid', gap: 22, justifyItems: 'start', maxWidth: 520 }}>
          <Eyebrow>LET'S MAKE COMPLIANCE CLEARER.</Eyebrow>
          <h2 className="t-h2" style={{ margin: 0, fontSize: 'clamp(35px, 4vw, 52px)' }}>
            A better way
            <br /> to move forward.
          </h2>
          <p style={{ margin: 0, fontSize: 16, lineHeight: 'var(--lh-body)', color: 'var(--accent-foreground)' }}>
            See how ASComply fits your invoices, your systems and your team.
          </p>
          <Button onClick={onBookDemo}>Book a demo</Button>
        </div>
        <div
          className="cta-mark"
          aria-hidden="true"
          style={{
            position: 'absolute',
            right: 'clamp(24px, 5vw, 72px)',
            top: '50%',
            transform: 'translateY(-50%)',
            textAlign: 'right',
            fontSize: 'clamp(120px, 15vw, 210px)',
            fontWeight: 800,
            lineHeight: 0.88,
            letterSpacing: '-0.06em',
            color: 'color-mix(in srgb, var(--peach-card) 86%, var(--accent-foreground))',
            userSelect: 'none',
            pointerEvents: 'none',
          }}
        >
          All
          <br />
          clear.
        </div>
      </div>
    </Section>
  )
}
