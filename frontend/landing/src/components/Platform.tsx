import { useState } from 'react'

import { CAPABILITIES, PLATFORM_TABS, type PlatformTabId } from '../data'
import { GLYPHS, Icon } from '../icons'
import { Badge, TagPill } from './ds/Badge'
import { Button } from './ds/Button'
import { Eyebrow } from './ds/Eyebrow'
import { IconTile } from './ds/IconTile'
import { Section } from './ds/Section'
import { Tabs } from './ds/Tabs'

const teal = { display: 'inline-flex', color: 'var(--teal)' } as const

export function Platform({ onBookDemo }: { onBookDemo: () => void }) {
  const [tab, setTab] = useState<PlatformTabId>('validate')
  const p = PLATFORM_TABS.find((t) => t.id === tab) ?? PLATFORM_TABS[0]
  return (
    <Section id="platform">
      <div
        style={{ display: 'flex', flexWrap: 'wrap', justifyContent: 'space-between', alignItems: 'flex-end', gap: '24px 48px', marginBottom: 48 }}
      >
        <div style={{ display: 'grid', gap: 20 }}>
          <Eyebrow>THE ASCOMPLY PLATFORM</Eyebrow>
          <h2 className="t-h2" style={{ margin: 0 }}>
            From invoice chaos
            <br /> <span style={{ color: 'var(--teal)' }}>to complete clarity.</span>
          </h2>
        </div>
        <p className="t-body" style={{ margin: 0, maxWidth: 300 }}>
          Prove and organise your invoices. Less time moving between tools. More confidence in every step.
        </p>
      </div>
      <Tabs
        className="a-tabs"
        aria-label="ASComply platform steps"
        tabs={PLATFORM_TABS.map((t) => ({
          id: t.id,
          label: (
            <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
              <Icon paths={GLYPHS[t.icon]} size={18} strokeWidth={2} />
              {t.label}
            </span>
          ),
        }))}
        value={tab}
        onChange={(id) => setTab(id as PlatformTabId)}
        panelStyle={{ padding: 'clamp(24px, 4vw, 48px)' }}
      >
        <div className="split" style={{ gridTemplateColumns: 'minmax(0, 1fr) minmax(0, 1fr)', gap: 40, alignItems: 'start' }}>
          <div style={{ display: 'grid', gap: 20, justifyItems: 'start' }}>
            <span className="t-step">{p.stepLabel}</span>
            <h3 className="t-h3" style={{ margin: 0 }}>
              {p.h1}
              <br /> {p.h2}
            </h3>
            <p className="t-body" style={{ margin: 0, maxWidth: 460 }}>
              {p.body}
            </p>
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
              {p.tags.map((t) => (
                <TagPill key={t}>{t}</TagPill>
              ))}
            </div>
            <Button variant="text" onClick={onBookDemo}>
              {p.link}
            </Button>
          </div>
          <div className="card" style={{ boxShadow: 'var(--shadow-card)', padding: '22px 24px' }}>
            <div
              style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, paddingBottom: 16, borderBottom: '1px solid var(--border)' }}
            >
              <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
                <IconTile name={p.cardIcon} tone="primary" size={36} />
                <span className="t-card-title">{p.cardTitle}</span>
              </div>
            </div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '18px 0' }}>
              <span style={teal}>
                <Icon paths={GLYPHS['shield-check']} size={28} strokeWidth={2} />
              </span>
              <div>
                <div className="t-step">{p.kind}</div>
                <div style={{ fontSize: 20, fontWeight: 800, letterSpacing: '-0.02em', color: 'var(--ink)', margin: '6px 0 2px' }}>{p.result}</div>
                <div className="t-caption">{p.sub}</div>
              </div>
            </div>
            <div style={{ display: 'grid' }}>
              {p.rows.map((r) => (
                <div
                  key={r.label}
                  style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, padding: '11px 0', borderTop: '1px solid var(--border)' }}
                >
                  <div style={{ display: 'flex', alignItems: 'center', gap: 10, fontSize: 14, color: 'var(--ink)' }}>
                    <span style={teal}>
                      <Icon paths={GLYPHS['circle-check']} size={17} strokeWidth={2} />
                    </span>
                    {r.label}
                  </div>
                  <Badge tone="success">{r.status}</Badge>
                </div>
              ))}
            </div>
            <div style={{ marginTop: 6, paddingTop: 14, borderTop: '1px solid var(--border)', fontSize: 13, fontWeight: 700, color: 'var(--step-label)' }}>
              Clarity at every checkpoint.
            </div>
          </div>
        </div>
      </Tabs>
      <div className="cols3" style={{ gap: '24px 48px', marginTop: 48 }}>
        {CAPABILITIES.map((c) => (
          <div key={c.title} style={{ borderTop: '1px solid var(--tab-border)', paddingTop: 24, display: 'grid', gap: 12, justifyItems: 'start' }}>
            <IconTile name={c.icon} tone="primary" size={40} />
            <div className="t-card-title">{c.title}</div>
            <p className="t-body-sm" style={{ margin: 0 }}>
              {c.body}
            </p>
          </div>
        ))}
      </div>
    </Section>
  )
}
