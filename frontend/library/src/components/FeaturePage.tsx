import { useEffect, useReducer } from 'react'
import { FEATURES, GROUPS } from '../content'
import { Icon } from '../icons'
import { nextClock, START } from '../player'
import { stepAt } from '../timing'
import type { Feature, Group } from '../types'
import { Button } from './Button'
import { ComingSoonPill } from './ComingSoonPill'
import { Player } from './Player'

type FeaturePageProps = {
  group: Group
  feature: Feature
  openHref: string | null
  onGroup: (g: Group) => void
  onFeature: (f: Feature) => void
}

const panel = { display: 'flex', flexDirection: 'column', gap: 18 } as const

export function FeaturePage({ group, feature, openHref, onGroup, onFeature }: FeaturePageProps) {
  const steps = feature.sc.steps.length
  const [clock, dispatch] = useReducer((c: typeof START, a: Parameters<typeof nextClock>[1]) => nextClock(c, a, steps), START)

  useEffect(() => {
    if (!clock.playing) return
    const iv = setInterval(() => dispatch({ type: 'tick' }), 100)
    return () => clearInterval(iv)
  }, [clock.playing])

  const idx = stepAt(clock.tenths / 10, steps)
  const related = feature.rel.flatMap((id) => {
    const f = FEATURES.find((x) => x.id === id)
    const g = f && GROUPS.find((x) => x.id === f.gid)
    return f && g ? [{ f, group: g.name }] : []
  })

  return (
    <section className="lib-px" style={{ padding: '40px 40px 88px', maxWidth: 1080, display: 'flex', flexDirection: 'column', gap: 36 }}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 18, maxWidth: 720 }}>
        <button
          type="button"
          className="lib-back"
          onClick={() => onGroup(group)}
          style={{ alignSelf: 'flex-start', display: 'inline-flex', alignItems: 'center', gap: 6, background: 'none', border: 0, padding: 0, cursor: 'pointer', fontSize: 13, fontWeight: 700, color: 'var(--link)' }}
        >
          <Icon name="chevron-left" size={15} />
          <span>{group.name}</span>
        </button>
        {feature.status === 'soon' && <ComingSoonPill tone="light" style={{ alignSelf: 'flex-start' }} />}
        <h2 className="t-h2" style={{ margin: 0, fontSize: 44 }}>
          {feature.title}
        </h2>
        <p className="t-lead" style={{ margin: 0 }}>
          {feature.desc}
        </p>
        {openHref && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap', marginTop: 4 }}>
            <Button variant="primary" size="sm" arrow href={openHref}>
              Open in Platform
            </Button>
          </div>
        )}
      </div>

      <Player feature={feature} clock={clock} onToggle={() => dispatch({ type: 'toggle' })} onSeek={(frac) => dispatch({ type: 'seek', frac })} />

      <div className="lib-cols" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(280px, 1fr))', gap: 40, alignItems: 'start' }}>
        <div style={panel}>
          <div className="t-eyebrow">What you get</div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
            {feature.benefits.map((b) => (
              <div key={b} style={{ display: 'flex', gap: 12, alignItems: 'flex-start' }}>
                <span style={{ color: 'var(--teal)', display: 'inline-flex', marginTop: 2 }}>
                  <Icon name="circle-check" size={18} />
                </span>
                <span className="t-body" style={{ color: 'var(--foreground)' }}>
                  {b}
                </span>
              </div>
            ))}
          </div>
        </div>
        <div style={panel}>
          <div className="t-eyebrow">In this demo</div>
          <div style={{ display: 'flex', flexDirection: 'column' }}>
            {feature.sc.steps.map((s, i) => {
              const a = i === idx
              return (
                <button
                  key={i}
                  type="button"
                  aria-current={a ? 'step' : undefined}
                  onClick={() => dispatch({ type: 'jump', step: i })}
                  style={{ display: 'flex', gap: 14, alignItems: 'baseline', textAlign: 'left', cursor: 'pointer', padding: '11px 0', background: 'transparent', border: 0, borderTop: '1px solid var(--border)', color: a ? 'var(--ink)' : 'var(--text-copy)' }}
                >
                  <span className="mono" style={{ fontSize: 11, fontWeight: 700, color: a ? 'var(--teal)' : 'var(--step-label)' }}>
                    {String(i + 1).padStart(2, '0')}
                  </span>
                  <span style={{ fontSize: 14, fontWeight: a ? 700 : 500, lineHeight: 1.45 }}>{s.cap}</span>
                </button>
              )
            })}
          </div>
        </div>
        <div style={panel}>
          <div className="t-eyebrow">Related</div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            {related.map(({ f, group: name }) => (
              <button
                key={f.id}
                type="button"
                className="lib-card"
                onClick={() => onFeature(f)}
                style={{ textAlign: 'left', cursor: 'pointer', display: 'flex', flexDirection: 'column', gap: 4, padding: '14px 16px', background: 'var(--card)', border: '1px solid var(--border)', borderRadius: 6, color: 'var(--ink)' }}
              >
                <span className="t-step">{name}</span>
                <span style={{ fontSize: 15, fontWeight: 700, letterSpacing: '-0.01em' }}>{f.title}</span>
              </button>
            ))}
          </div>
        </div>
      </div>
    </section>
  )
}
