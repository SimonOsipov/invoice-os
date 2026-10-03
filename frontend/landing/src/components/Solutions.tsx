import { useId, useState } from 'react'
import { SOLUTIONS, type SolutionId } from '../data'
import { Badge } from './ds/Badge'
import { Button } from './ds/Button'
import { ChecklistItem } from './ds/ChecklistItem'
import { Eyebrow } from './ds/Eyebrow'
import { IconTile } from './ds/IconTile'
import { SegmentedTabs } from './ds/SegmentedTabs'
import { Section } from './ds/Section'

const ROW_TONE = { Validated: 'success', Connected: 'success', Mapped: 'success', 'In review': 'progress' } as const

export function Solutions({ onBookDemo }: { onBookDemo: () => void }) {
  const [tab, setTab] = useState<SolutionId>('fin')
  const base = useId()
  return (
    <Section tone="cream" id="solutions">
      <div style={{ display: 'grid', gap: 20, justifyItems: 'start', textAlign: 'left', marginBottom: 36 }}>
        <Eyebrow>YOUR TEAM. YOUR WORKFLOW.</Eyebrow>
        <h2 className="t-h2" style={{ margin: 0 }}>
          One platform.
          <br /> <span style={{ color: 'var(--teal)' }}>Different perspectives.</span>
        </h2>
        <p className="t-body" style={{ margin: 0, maxWidth: 460 }}>
          For the people who own invoices, clients or integrations, each view shows what they need to move forward.
        </p>
      </div>
      <div data-sol-tabs style={{ display: 'flex', justifyContent: 'flex-start', marginBottom: 32, maxWidth: '100%', overflowX: 'auto' }}>
        <SegmentedTabs
          options={SOLUTIONS.map((s) => ({ id: s.id, label: s.tab }))}
          value={tab}
          onChange={(id) => setTab(id as SolutionId)}
          aria-label="Who it's for"
          idBase={base}
        />
      </div>
      <div data-sol-panels style={{ display: 'grid' }}>
        {SOLUTIONS.map((s) => {
          const on = s.id === tab
          return (
            <div
              key={s.id}
              className="split"
              data-sol-panel={s.id}
              role="tabpanel"
              id={`${base}-panel-${s.id}`}
              aria-labelledby={`${base}-tab-${s.id}`}
              aria-hidden={on ? undefined : true}
              style={{
                gridArea: '1 / 1',
                visibility: on ? 'visible' : 'hidden',
                gridTemplateColumns: 'minmax(0, 1fr) minmax(0, 1fr)',
                gap: 'clamp(24px, 5vw, 64px)',
                alignItems: 'start',
              }}
            >
              <div style={{ background: 'var(--sage)', borderRadius: 'var(--radius-lg)', padding: 'clamp(20px, 3vw, 36px)' }}>
                <div className="card-sage" data-sol-card style={{ boxShadow: 'var(--shadow-soft)', padding: '20px 22px' }}>
                  <div
                    style={{
                      display: 'flex',
                      alignItems: 'center',
                      justifyContent: 'space-between',
                      gap: 12,
                      paddingBottom: 14,
                      borderBottom: '1px solid var(--sage-card-border)',
                    }}
                  >
                    <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
                      <IconTile name={s.cardIcon} tone="primary" size={36} />
                      <span className="t-card-title">{s.cardTitle}</span>
                    </div>
                    <span className="t-meta">Sample data</span>
                  </div>
                  <div style={{ padding: '16px 0 8px' }}>
                    <div className="t-step">{s.overview}</div>
                    <div style={{ fontSize: 22, fontWeight: 800, letterSpacing: '-0.03em', color: 'var(--ink)', marginTop: 6 }}>{s.cardHead}</div>
                  </div>
                  <div>
                    {s.rows.map(([name, status]) => (
                      <div
                        key={name}
                        style={{
                          display: 'flex',
                          alignItems: 'center',
                          justifyContent: 'space-between',
                          gap: 12,
                          padding: '12px 0',
                          borderTop: '1px solid var(--sage-card-border)',
                          fontSize: 14,
                          fontWeight: 600,
                          color: 'var(--ink)',
                        }}
                      >
                        <span>{name}</span>
                        <Badge tone={ROW_TONE[status]}>{status}</Badge>
                      </div>
                    ))}
                  </div>
                  <div style={{ paddingTop: 14, borderTop: '1px solid var(--sage-card-border)', fontSize: 13, fontWeight: 700, color: 'var(--step-label)' }}>
                    One workspace. A shared view.
                  </div>
                </div>
              </div>
              <div style={{ display: 'grid', gap: 18, justifyItems: 'start' }}>
                <span className="t-step">{s.label}</span>
                <h3 className="t-h3" style={{ margin: 0 }}>
                  {s.h3}
                </h3>
                <p className="t-body" style={{ margin: 0, maxWidth: 480 }}>
                  {s.body}
                </p>
                <div style={{ display: 'grid', gap: 10 }}>
                  {s.points.map((p) => (
                    <ChecklistItem key={p}>{p}</ChecklistItem>
                  ))}
                </div>
                <Button onClick={onBookDemo}>{s.cta}</Button>
              </div>
            </div>
          )
        })}
      </div>
    </Section>
  )
}
