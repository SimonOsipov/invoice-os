import { Fragment, useState } from 'react'
import { COUNTRY_ORDER, INTEL, INTEL_STEPS, type CountryId } from '../countries'
import { GLYPHS, Icon } from '../icons'
import { Button } from './ds/Button'
import { Eyebrow } from './ds/Eyebrow'
import { IconTile } from './ds/IconTile'
import { Section } from './ds/Section'
import { Flag } from './Flag'

export function Intelligence() {
  const [country, setCountry] = useState<CountryId>('NG')
  const [step, setStep] = useState(0)
  const cty = INTEL[country]
  return (
    <Section tone="dark2" id="intelligence">
      <div style={{ display: 'flex', flexWrap: 'wrap', justifyContent: 'space-between', alignItems: 'flex-end', gap: '24px 48px', marginBottom: 40 }}>
        <div style={{ display: 'grid', gap: 20 }}>
          <Eyebrow tone="dark">AI-SUPPORTED REGULATORY INTELLIGENCE</Eyebrow>
          <h2 className="t-h2" style={{ margin: 0, color: 'var(--surface-foreground)' }}>
            Regulations move.
            <br /> <span className="t-hl-dark2">Keep your next step clear.</span>
          </h2>
        </div>
        <div style={{ maxWidth: 320, display: 'grid', gap: 14, justifyItems: 'start' }}>
          <p className="t-body" style={{ margin: 0, color: 'var(--surface-body)' }}>
            Our roadmap connects official updates, AI-assisted analysis and human review. A clearer way to follow regulatory change across Africa.
          </p>
        </div>
      </div>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 10, marginBottom: 20 }}>
        {COUNTRY_ORDER.map((id) => {
          const on = id === country
          return (
            <button
              key={id}
              type="button"
              className="a-card-btn"
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
                background: on ? 'var(--accent)' : 'var(--on-dark-5)',
                color: on ? 'var(--accent-foreground)' : 'var(--surface-foreground)',
                border: `1px solid ${on ? 'var(--accent)' : 'var(--on-dark-10)'}`,
              }}
            >
              <Flag id={id} width={22} height={15} />
              {INTEL[id].name}
              <span style={{ fontSize: 11, fontWeight: 600, opacity: 0.85 }}>{INTEL[id].tag}</span>
            </button>
          )
        })}
      </div>
      <div
        className="split"
        data-intel-workspace
        style={{
          gridTemplateColumns: '300px minmax(0, 1fr)',
          background: 'var(--cream-card)',
          border: '1px solid var(--cream-card-border)',
          borderRadius: 'var(--radius-md)',
          overflow: 'hidden',
          color: 'var(--ink)',
        }}
      >
        <aside style={{ background: 'var(--sage-panel)', padding: '28px 24px', display: 'flex', flexDirection: 'column', gap: 20 }}>
          <span className="t-step">Regulatory workspace</span>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
            <Flag id={country} width={30} height={20} />
            <div>
              <div style={{ fontSize: 18, fontWeight: 700, letterSpacing: '-0.01em' }}>{cty.name}</div>
              <div className="t-caption">{cty.authority}</div>
            </div>
          </div>
          <div>
            <div className="t-step" style={{ marginBottom: 6 }}>
              Monitoring focus
            </div>
            <div style={{ fontSize: 14, fontWeight: 600, lineHeight: 1.5 }}>{cty.focus}</div>
          </div>
          <Button variant="text" href={cty.href} target="_blank" rel="noopener noreferrer">
            View official source ↗
          </Button>
          <div style={{ marginTop: 'auto', display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, fontWeight: 700, color: 'var(--step-label)' }}>
            <Icon paths={GLYPHS.link} size={15} strokeWidth={2} />
            Source-linked. Human-reviewed.
          </div>
        </aside>
        <div style={{ padding: 'clamp(20px, 3vw, 32px)', display: 'grid', gap: 24, minWidth: 0 }}>
          <div style={{ display: 'flex', flexWrap: 'wrap', justifyContent: 'space-between', gap: '8px 16px' }}>
            <span className="t-step">Change → Understanding → Action</span>
          </div>
          <div data-intel-steps style={{ overflowX: 'auto' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 520 }}>
              {INTEL_STEPS.map((s, i) => {
                const on = i === step
                return (
                  <Fragment key={s.n}>
                    <button
                      type="button"
                      className="a-card-btn"
                      data-intel-step
                      aria-pressed={on}
                      onClick={() => setStep(i)}
                      style={{
                        flex: 1,
                        display: 'flex',
                        alignItems: 'center',
                        gap: 10,
                        padding: '10px 6px 12px',
                        background: 'transparent',
                        border: 0,
                        borderBottom: `2px solid ${on ? 'var(--accent-foreground)' : 'transparent'}`,
                        cursor: 'pointer',
                        color: on ? 'var(--ink)' : 'var(--muted-foreground)',
                        fontSize: 14,
                        fontWeight: 700,
                        textAlign: 'left',
                      }}
                    >
                      <IconTile name={s.icon} tone={on ? 'accent' : 'primary'} size={36} />
                      <span style={{ whiteSpace: 'nowrap' }}>
                        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, fontWeight: 400, marginRight: 6, color: 'var(--step-label)' }}>{s.n}</span>
                        {s.label}
                      </span>
                    </button>
                    {i < INTEL_STEPS.length - 1 && (
                      <span style={{ display: 'inline-flex', color: 'var(--step-label)' }}>
                        <Icon paths={GLYPHS['chevron-right']} size={16} />
                      </span>
                    )}
                  </Fragment>
                )
              })}
            </div>
          </div>
          <div style={{ display: 'grid' }}>
            {INTEL_STEPS.map((s, i) => (
              <div
                key={s.n}
                className="split"
                data-intel-panel
                aria-hidden={i !== step ? true : undefined}
                style={{
                  gridArea: '1 / 1',
                  visibility: i === step ? 'visible' : 'hidden',
                  gridTemplateColumns: 'minmax(0, 1.3fr) minmax(0, 0.9fr)',
                  gap: 32,
                  alignItems: 'stretch',
                }}
              >
                <div style={{ display: 'grid', gap: 14 }}>
                  <span className="t-step">{s.step}</span>
                  <h3 style={{ margin: 0, fontSize: 'clamp(22px, 2.2vw, 26px)', lineHeight: 1.2, letterSpacing: 'var(--tracking-h3)', fontWeight: 700 }}>{s.title}</h3>
                  {/* Inline colour beats .band-dark2 .t-body, which is light-on-cream here; IN-09 pins it. */}
                  <p className="t-body" style={{ margin: 0, color: 'var(--text-copy)' }}>
                    {s.body}
                  </p>
                </div>
                <div className="card" style={{ padding: '18px 20px', display: 'flex', flexDirection: 'column', gap: 12 }}>
                  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10 }}>
                    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, fontSize: 12, fontWeight: 700, color: 'var(--step-label)' }}>
                      <Icon paths={GLYPHS[s.icon]} size={15} strokeWidth={2} />
                      {s.card}
                    </span>
                  </div>
                  <div>
                    <div className="t-card-title">{cty.cardTitle}</div>
                    <div className="t-body-sm" style={{ marginTop: 4, lineHeight: 1.6 }}>
                      {cty.cardSub}
                    </div>
                  </div>
                  <div
                    style={{
                      marginTop: 'auto',
                      display: 'flex',
                      alignItems: 'center',
                      gap: 8,
                      paddingTop: 12,
                      borderTop: '1px solid var(--border)',
                      fontSize: 13,
                      fontWeight: 600,
                      color: 'var(--status-success-fg)',
                    }}
                  >
                    <span style={{ width: 6, height: 6, borderRadius: 'var(--radius-pill)', background: 'currentColor', flex: 'none' }} />
                    {s.status}
                  </div>
                </div>
              </div>
            ))}
          </div>
        </div>
      </div>
      <div style={{ marginTop: 32, display: 'flex', flexWrap: 'wrap', justifyContent: 'space-between', alignItems: 'center', gap: '12px 24px' }}>
        <p style={{ margin: 0, fontSize: 16, fontWeight: 700, color: 'var(--surface-foreground)' }}>AI helps your team review. People approve the changes.</p>
        <Button variant="text" className="btn-on-dark" href="#coverage">
          Explore the roadmap →
        </Button>
      </div>
    </Section>
  )
}
