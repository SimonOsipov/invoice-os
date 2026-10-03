import { useState } from 'react'

import { FAQS } from '../data'
import { Button } from './ds/Button'
import { Eyebrow } from './ds/Eyebrow'
import { FAQItem } from './ds/FAQItem'
import { Section } from './ds/Section'

export function Faq({ onBookDemo }: { onBookDemo: () => void }) {
  const [open, setOpen] = useState<number>(0)
  return (
    <Section tone="cream" id="faq">
      <div
        className="split"
        style={{ gridTemplateColumns: 'minmax(0, 0.8fr) minmax(0, 1.4fr)', gap: 'clamp(32px, 6vw, 96px)', alignItems: 'start' }}
      >
        <div
          data-faq-aside
          style={{ display: 'grid', gap: 20, justifyItems: 'start', position: 'sticky', top: 'calc(var(--header-h) + 24px)' }}
        >
          <Eyebrow>A LITTLE MORE CLARITY</Eyebrow>
          <h2 className="t-h2" style={{ margin: 0 }}>
            Good questions.
            <br /> <span style={{ color: 'var(--teal)' }}>Clear answers.</span>
          </h2>
          <p className="t-body" style={{ margin: 0 }}>
            <strong style={{ color: 'var(--ink)' }}>Need to go deeper?</strong>
            <br />
            We'll walk through your workflow.
          </p>
          <Button variant="text" onClick={onBookDemo}>
            Talk to our team →
          </Button>
        </div>
        <div data-faq-list style={{ borderTop: '1px solid var(--border)' }}>
          {FAQS.map(({ q, a }, i) => (
            <FAQItem key={q} question={q} open={open === i} onToggle={() => setOpen(open === i ? -1 : i)}>
              {a}
            </FAQItem>
          ))}
        </div>
      </div>
    </Section>
  )
}
