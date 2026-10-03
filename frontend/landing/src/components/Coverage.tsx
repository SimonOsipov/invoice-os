import { useState } from 'react'
import { COUNTRY_ORDER, COVERAGE, LEGEND, ROADMAP, type CountryId } from '../countries'
import { GLYPHS, Icon } from '../icons'
import { Badge } from './ds/Badge'
import { Button } from './ds/Button'
import { Eyebrow } from './ds/Eyebrow'
import { Section } from './ds/Section'
import { Flag } from './Flag'

const teal = (name: 'shield-check' | 'check') => (
  <span style={{ display: 'inline-flex', color: 'var(--teal)' }}>
    <Icon paths={GLYPHS[name]} size={16} strokeWidth={2} />
  </span>
)

export function Coverage({ onBookDemo }: { onBookDemo: () => void }) {
  const [country, setCountry] = useState<CountryId>('NG')
  const cty = COVERAGE[country]
  return (
    <Section tone="peach" id="coverage">
      <div style={{ display: 'flex', flexWrap: 'wrap', justifyContent: 'space-between', alignItems: 'flex-end', gap: '24px 48px', marginBottom: 48 }}>
        <div style={{ display: 'grid', gap: 20 }}>
          <Eyebrow>NIGERIA FIRST. AFRICA IN VIEW.</Eyebrow>
          <h2 className="t-h2" style={{ margin: 0 }}>
            One continent.
            <br /> <span className="t-hl-peach">Every country has its context.</span>
          </h2>
        </div>
        <p style={{ margin: 0, maxWidth: 300, fontSize: 16, lineHeight: 'var(--lh-body)', color: 'var(--accent-foreground)' }}>
          Start with a focused launch. Build toward a connected African market, with workflows shaped around local requirements.
        </p>
      </div>
      <div className="split" style={{ gridTemplateColumns: 'minmax(0, 1.05fr) minmax(0, 0.95fr)', gap: 24, alignItems: 'stretch' }}>
        <div style={{ display: 'grid', gap: 16, alignContent: 'start', minWidth: 0 }}>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
            {COUNTRY_ORDER.map((id) => {
              const on = id === country
              return (
                <button
                  key={id}
                  type="button"
                  className="a-card-btn a-tab-pill"
                  aria-pressed={on}
                  onClick={() => setCountry(id)}
                  style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 10,
                    height: 44,
                    padding: '0 16px',
                    borderRadius: 'var(--radius-btn)',
                    cursor: 'pointer',
                    fontSize: 14,
                    fontWeight: 700,
                    background: on ? 'var(--primary)' : 'transparent',
                    color: on ? 'var(--primary-foreground)' : 'var(--primary)',
                    border: `1px solid ${on ? 'var(--primary)' : 'var(--primary-20)'}`,
                  }}
                >
                  <Flag id={id} width={22} height={15} />
                  {COVERAGE[id].name}
                </button>
              )
            })}
          </div>
          <div className="card-floating" data-cov-card style={{ padding: 'clamp(22px, 3vw, 32px)', display: 'grid', gap: 18, background: 'var(--cream-card)' }}>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
              <Badge tone={cty.pillTone} dot>
                {cty.pill}
              </Badge>
              <span className="t-meta">{cty.code} / Africa</span>
            </div>
            <h3 className="t-h3" style={{ margin: 0, fontSize: 'clamp(28px, 3vw, 34px)' }}>
              {cty.title}
            </h3>
            <p className="t-body" style={{ margin: 0 }}>
              {cty.body}
            </p>
            <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '8px 14px', padding: '12px 14px', background: 'var(--sage)', borderRadius: 'var(--radius-md)' }}>
              <span className="t-step">Country context</span>
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, fontSize: 14, fontWeight: 700, color: 'var(--ink)' }}>
                {teal('shield-check')}
                {cty.context}
              </span>
            </div>
            <div>
              <div className="t-step" style={{ marginBottom: 8 }}>
                {cty.flowLabel}
              </div>
              <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: '0 24px' }}>
                {cty.steps.map((s, i) => (
                  <div
                    key={s}
                    style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '10px 0', borderTop: '1px solid var(--cream-card-border)', fontSize: 14, color: 'var(--ink)' }}
                  >
                    <span style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: 'var(--step-label)' }}>{`0${i + 1}`}</span>
                    <span style={{ flex: 1 }}>{s}</span>
                    {teal('check')}
                  </div>
                ))}
              </div>
            </div>
            <p className="t-caption" style={{ margin: 0, lineHeight: 1.6 }}>
              {cty.disclaimer}
            </p>
            <div>
              <Button variant="text" href={cty.href} target="_blank" rel="noopener noreferrer">
                {cty.linkLabel}
              </Button>
            </div>
          </div>
        </div>
        <div
          data-cov-panel
          style={{
            background: 'var(--surface-panel)',
            border: '1px solid var(--surface-panel-border)',
            borderRadius: 'var(--radius-md)',
            padding: 24,
            display: 'flex',
            flexDirection: 'column',
            minWidth: 0,
            color: 'var(--surface-foreground)',
          }}
        >
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
            <span style={{ fontSize: 10, fontWeight: 700, letterSpacing: 'var(--tracking-eyebrow)', textTransform: 'uppercase', color: 'var(--eyebrow-on-dark)' }}>
              The ASComply Africa roadmap
            </span>
          </div>
          <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: '12px 0' }} />
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: '10px 22px', fontSize: 12, fontWeight: 600, color: 'var(--surface-body)' }}>
            {LEGEND.map((l) => (
              <span key={l.label} style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
                <span style={{ width: 10, height: 10, borderRadius: 'var(--radius-pill)', background: l.color, border: `1px solid ${l.border}` }} />
                {l.label}
              </span>
            ))}
          </div>
        </div>
      </div>
      <div className="cols3" data-roadmap style={{ display: 'grid', gridTemplateColumns: 'repeat(3, minmax(0, 1fr))', gap: 16, marginTop: 32 }}>
        {ROADMAP.map((r) => (
          <div key={r.n} style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
            <div style={{ flex: 1, minWidth: 0, display: 'flex', gap: 14, alignItems: 'flex-start' }}>
              <span style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: 'var(--primary)', paddingTop: 3 }}>{r.n}</span>
              <div>
                <div className="t-card-title" style={{ color: 'var(--primary)' }}>
                  {r.t}
                </div>
                <div style={{ fontSize: 14, lineHeight: 1.5, color: 'var(--accent-foreground)' }}>{r.s}</div>
              </div>
            </div>
            <span className="rm-chev" style={{ display: r.chev, color: 'var(--primary)' }}>
              <Icon paths={GLYPHS['chevron-right']} size={20} />
            </span>
          </div>
        ))}
      </div>
      <div
        style={{
          marginTop: 32,
          paddingTop: 22,
          borderTop: '1px solid var(--accent-20)',
          display: 'flex',
          flexWrap: 'wrap',
          justifyContent: 'space-between',
          alignItems: 'center',
          gap: '12px 24px',
        }}
      >
        <p style={{ margin: 0, fontSize: 14, color: 'var(--accent-foreground)', maxWidth: 640 }}>
          Roadmap markets represent our direction. Availability and launch dates will be confirmed by the ASComply team.
        </p>
        <Button variant="text" className="btn-on-peach" onClick={onBookDemo}>
          Discuss your country →
        </Button>
      </div>
    </Section>
  )
}
