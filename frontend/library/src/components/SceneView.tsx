import type { CSSProperties, ReactNode } from 'react'
import { Icon, type GlyphName } from '../icons'
import { CHIP, sceneState, type SceneState } from '../scene'
import type { FeedTag, Mark, Scene, Tone } from '../types'

type Size = 'thumb' | 'player'
type Of<K extends SceneState['kind']> = Extract<SceneState, { kind: K }>

const ell: CSSProperties = { whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }
const label: CSSProperties = { fontWeight: 700, textTransform: 'uppercase', color: 'var(--muted-foreground)' }

const chipColors = (status: keyof typeof CHIP) => {
  const [text, tone] = CHIP[status]
  return { text, fg: `var(--status-${tone}-text)`, bg: `var(--status-${tone}-bg)`, bd: `var(--status-${tone}-border)` }
}

const MK: Record<Mark, readonly [GlyphName, string, string, string]> = {
  ok: ['circle-check', 'var(--status-green-text)', 'var(--status-green-border)', 'var(--status-green-bg)'],
  err: ['triangle-alert', 'var(--status-red-text)', 'var(--status-red-border)', 'var(--status-red-bg)'],
  low: ['triangle-alert', 'var(--status-amber-text)', 'var(--status-amber-border)', 'var(--status-amber-bg)'],
  fix: ['pen-tool', 'var(--teal)', 'var(--ring)', '#fff'],
}

const msgColors = (tone: Tone) => {
  if (tone === 'info') return { bg: 'var(--mint-soft)', fg: 'var(--tab-active-text)', bd: 'var(--border)' }
  const t = tone === 'ok' ? 'green' : 'red'
  return { bg: `var(--status-${t}-bg)`, fg: `var(--status-${t}-text)`, bd: `var(--status-${t}-border)` }
}

const FEED_DOT: Record<FeedTag, string> = {
  g: 'var(--status-green-text)',
  a: 'var(--accent)',
  r: 'var(--status-red-text)',
  m: 'var(--input)',
}

const flowNode = (state: 'done' | 'active' | 'todo', i: number) => ({
  num: state === 'done' ? '✓' : String(i + 1),
  bg: state === 'done' ? 'var(--primary)' : state === 'active' ? 'var(--accent)' : '#fff',
  fg: state === 'done' ? '#fff' : state === 'active' ? 'var(--accent-foreground)' : 'var(--muted-foreground)',
  bd: state === 'done' ? 'var(--primary)' : state === 'active' ? 'var(--accent)' : 'var(--input)',
  tx: state === 'todo' ? 'var(--muted-foreground)' : 'var(--ink)',
  lineBg: state === 'todo' ? 'var(--border)' : 'var(--primary)',
  lineDisplay: i === 0 ? 'none' : 'block',
})

function ListView({ s, size }: { s: Of<'list'>; size: Size }) {
  if (size === 'thumb') {
    const banner = s.banner
    return (
      <div style={{ display: 'flex', flexDirection: 'column' }}>
        {s.rows.slice(0, banner ? 3 : 4).map((r, i) => {
          const c = chipColors(r.status)
          return (
            <div
              key={i}
              style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) auto', gap: 8, alignItems: 'center', padding: '5px 0', borderBottom: '1px solid var(--border)' }}
            >
              <span style={{ fontSize: 10, fontWeight: 600, ...ell }}>{r.c2}</span>
              <span style={{ padding: '1px 6px', borderRadius: 3, fontSize: 9, fontWeight: 700, background: c.bg, color: c.fg, border: `1px solid ${c.bd}` }}>
                {c.text}
              </span>
            </div>
          )
        })}
        {banner && (
          <div
            style={{ marginTop: 6, padding: '4px 8px', borderRadius: 4, fontSize: 9.5, fontWeight: 600, background: 'var(--status-red-bg)', color: 'var(--status-red-text)', border: '1px solid var(--status-red-border)', ...ell }}
          >
            {banner}
          </div>
        )}
      </div>
    )
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      {s.banner && (
        <div
          key={s.banner}
          style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '9px 12px', background: 'var(--mint-soft)', borderRadius: 6, fontSize: 12.5, fontWeight: 600, color: 'var(--tab-active-text)', animation: 'libFade 300ms ease-out' }}
        >
          <Icon name="circle-check" size={15} />
          <span>{s.banner}</span>
        </div>
      )}
      <div
        style={{ display: 'grid', gridTemplateColumns: s.grid, gap: 12, padding: '4px 12px', fontSize: 10, fontWeight: 700, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--muted-foreground)' }}
      >
        {s.cols.map((c) => (
          <span key={c}>{c}</span>
        ))}
      </div>
      <div style={{ display: 'flex', flexDirection: 'column' }}>
        {s.rows.map((r, i) => {
          const c = chipColors(r.status)
          return (
            <div
              key={i}
              style={{ display: 'grid', gridTemplateColumns: s.grid, gap: 12, alignItems: 'center', padding: 12, borderTop: '1px solid var(--border)', fontSize: 13.5, background: r.focused ? 'var(--mint-soft)' : 'transparent', borderRadius: 4, transition: 'background 300ms ease-out', animation: 'libPop 320ms ease-out' }}
            >
              <span style={{ fontWeight: 600, ...ell }}>{r.c1}</span>
              <span style={{ color: 'var(--text-copy)', ...ell }}>{r.c2}</span>
              <span className="mono" style={{ color: 'var(--foreground)' }}>
                {r.c3}
              </span>
              <span
                style={{ justifySelf: 'start', padding: '3px 8px', borderRadius: 4, fontSize: 11, fontWeight: 700, background: c.bg, color: c.fg, border: `1px solid ${c.bd}`, transition: 'background 300ms, color 300ms' }}
              >
                {c.text}
              </span>
            </div>
          )
        })}
      </div>
    </div>
  )
}

function FormView({ s, size }: { s: Of<'form'>; size: Size }) {
  const thumb = size === 'thumb'
  const msg = s.msg && { text: s.msg.text, ...msgColors(s.msg.tone) }
  const docBg = (f: Of<'form'>['fields'][number]) => (f.focused ? 'var(--peach-tint)' : 'transparent')
  const docOutline = (f: Of<'form'>['fields'][number]) =>
    f.focused ? '1.5px solid var(--accent)' : f.mark === 'low' ? '1.5px dashed var(--status-amber-text)' : '1.5px solid transparent'

  if (thumb) {
    // why: doc+msg thumbnail overflows its 4:3 box with 3 fields
    const nf = s.doc ? (msg ? 2 : 3) : 4
    return (
      <div style={{ display: 'grid', gridTemplateColumns: s.doc ? '0.85fr 1.15fr' : '1fr', gap: 10, alignItems: 'start' }}>
        {s.doc && (
          <div style={{ background: '#fff', border: '1px solid var(--border)', borderRadius: 3, padding: '6px 7px', display: 'flex', flexDirection: 'column', gap: 2, boxShadow: 'var(--shadow-soft)' }}>
            <span style={{ fontSize: 8, fontWeight: 800, letterSpacing: '0.14em', marginBottom: 2 }}>INVOICE</span>
            {s.fields.slice(0, 4).map((f) => (
              <div
                key={f.l}
                style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 6, padding: '2px 4px', borderRadius: 2, background: docBg(f), outline: docOutline(f) }}
              >
                <span style={{ fontSize: 7.5, color: 'var(--muted-foreground)' }}>{f.l}</span>
                <span className="mono" style={{ fontSize: 8.5, fontWeight: 600, ...ell }}>
                  {f.v}
                </span>
              </div>
            ))}
          </div>
        )}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 6, minWidth: 0 }}>
          <div style={{ display: 'grid', gridTemplateColumns: s.doc ? '1fr' : '1fr 1fr', gap: '5px 8px' }}>
            {s.fields.slice(0, nf).map((f) => {
              const mk = f.mark ? MK[f.mark] : null
              return (
                <div key={f.l} style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0 }}>
                  <span style={{ fontSize: 8, letterSpacing: '0.08em', ...label, ...ell }}>{f.l}</span>
                  <span
                    style={{ height: 18, display: 'flex', alignItems: 'center', padding: '0 7px', borderRadius: 4, border: `1px solid ${mk ? mk[2] : 'var(--input)'}`, background: mk ? mk[3] : '#fff', fontSize: 10, fontWeight: 600, ...ell }}
                  >
                    {f.val}
                  </span>
                </div>
              )
            })}
          </div>
          {msg && (
            <div style={{ padding: '4px 8px', borderRadius: 4, fontSize: 9.5, fontWeight: 600, ...ell, background: msg.bg, color: msg.fg, border: `1px solid ${msg.bd}` }}>
              {msg.text}
            </div>
          )}
        </div>
      </div>
    )
  }

  return (
    <div style={{ display: 'grid', gridTemplateColumns: s.doc ? '1fr 1.15fr' : '1fr', gap: 22, alignItems: 'start' }}>
      {s.doc && (
        <div
          style={{ background: '#fff', border: '1px solid var(--border)', borderRadius: 4, padding: '16px 16px 14px', display: 'flex', flexDirection: 'column', gap: 9, boxShadow: 'var(--shadow-soft)', height: 306, overflow: 'hidden' }}
        >
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
            <span style={{ fontSize: 11, fontWeight: 800, letterSpacing: '0.14em', color: 'var(--ink)' }}>INVOICE</span>
            <span style={{ width: 44, height: 8, borderRadius: 2, background: 'var(--border)' }} />
          </div>
          <div style={{ height: 6, width: '62%', borderRadius: 2, background: 'var(--muted)' }} />
          {s.fields.map((f) => (
            <div
              key={f.l}
              style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '5px 7px', borderRadius: 3, fontSize: 11.5, background: docBg(f), outline: docOutline(f), transition: 'background 250ms, outline-color 250ms' }}
            >
              <span style={{ color: 'var(--muted-foreground)', fontSize: 10 }}>{f.l}</span>
              <span className="mono" style={{ fontWeight: 600, color: 'var(--ink)' }}>
                {f.v}
              </span>
            </div>
          ))}
          <div style={{ height: 6, width: '80%', borderRadius: 2, background: 'var(--muted)' }} />
          <div style={{ height: 6, width: '54%', borderRadius: 2, background: 'var(--muted)' }} />
        </div>
      )}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 11 }}>
        {/* why: 5 fields plus the message overflow the player scene box */}
        {(msg ? s.fields.slice(0, 4) : s.fields).map((f) => {
          const mk = f.mark ? MK[f.mark] : null
          return (
            <div key={f.l} style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              <span style={{ fontSize: 10, letterSpacing: '0.1em', ...label }}>{f.l}</span>
              <div
                style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8, height: 38, padding: '0 12px', borderRadius: 6, border: `1px solid ${mk ? mk[2] : 'var(--input)'}`, background: mk ? mk[3] : '#fff', boxShadow: f.focused ? '0 0 0 2px var(--ring)' : 'none', transition: 'border-color 250ms, box-shadow 250ms, background 250ms' }}
              >
                <span style={{ fontSize: 13.5, fontWeight: 600, opacity: f.shown ? 1 : 0, transition: 'opacity 400ms', ...ell }}>{f.val}</span>
                {mk && (
                  <span key={mk[0]} style={{ color: mk[1], display: 'inline-flex', animation: 'libFade 300ms' }}>
                    <Icon name={mk[0]} size={16} />
                  </span>
                )}
              </div>
            </div>
          )
        })}
        {msg && (
          <div
            key={msg.text}
            style={{ padding: '9px 12px', borderRadius: 6, fontSize: 12.5, fontWeight: 600, background: msg.bg, color: msg.fg, border: `1px solid ${msg.bd}`, animation: 'libPop 300ms ease-out' }}
          >
            {msg.text}
          </div>
        )}
      </div>
    </div>
  )
}

function FlowView({ s, size }: { s: Of<'flow'>; size: Size }) {
  const thumb = size === 'thumb'
  const nodes = s.nodes.map((n, i) => (
    <div key={i} style={{ flex: '1 1 0', minWidth: 0, display: 'flex', flexDirection: 'column', alignItems: 'center', gap: thumb ? 4 : 10, position: 'relative' }}>
      {(() => {
        const c = flowNode(n.state, i)
        return (
          <>
            <span
              style={{ position: 'absolute', top: thumb ? 10 : 19, right: '50%', width: '100%', height: 2, background: c.lineBg, ...(thumb ? {} : { transition: 'background 400ms' }), display: c.lineDisplay }}
            />
            <span
              style={{ position: 'relative', zIndex: 1, width: thumb ? 22 : 40, height: thumb ? 22 : 40, borderRadius: '50%', display: 'grid', placeItems: 'center', fontSize: thumb ? 9 : 13, fontWeight: 700, background: c.bg, color: c.fg, border: `2px solid ${c.bd}`, ...(thumb ? {} : { transition: 'background 300ms, border-color 300ms, color 300ms' }) }}
            >
              {c.num}
            </span>
            {thumb ? (
              <span style={{ fontSize: 8.5, fontWeight: 700, textAlign: 'center', lineHeight: 1.2, padding: '0 2px', maxHeight: 21, overflow: 'hidden', color: c.tx }}>{n.l}</span>
            ) : (
              <>
                <span style={{ fontSize: 13, fontWeight: 700, textAlign: 'center', lineHeight: 1.3, padding: '0 4px', color: c.tx }}>{n.l}</span>
                <span style={{ fontSize: 11.5, textAlign: 'center', color: 'var(--muted-foreground)', padding: '0 4px' }}>{n.sub}</span>
              </>
            )}
          </>
        )
      })()}
    </div>
  ))
  if (thumb) {
    return (
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        <div style={{ display: 'flex', alignItems: 'flex-start' }}>{nodes}</div>
        <div style={{ padding: '6px 9px', border: '1px solid var(--border)', borderRadius: 4, background: 'var(--sage-card)', fontSize: 10, fontWeight: 600, ...ell }}>{s.out}</div>
      </div>
    )
  }
  return (
    <div style={{ height: '100%', display: 'flex', flexDirection: 'column', justifyContent: 'center', gap: 44 }}>
      <div style={{ display: 'flex', alignItems: 'flex-start' }}>{nodes}</div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '14px 16px', border: '1px solid var(--border)', borderRadius: 6, background: 'var(--sage-card)', minHeight: 54 }}>
        <span className="t-step">STATUS</span>
        <span style={{ fontSize: 14, fontWeight: 600 }}>{s.out}</span>
      </div>
    </div>
  )
}

function MetricsView({ s, size }: { s: Of<'metrics'>; size: Size }) {
  const thumb = size === 'thumb'
  const barBg = (last: boolean) => (last ? 'var(--primary)' : 'var(--sage-panel)')
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: thumb ? 9 : 22 }}>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: thumb ? 7 : 14 }}>
        {s.tiles.map((t) => (
          <div
            key={t.l}
            style={{ padding: thumb ? '6px 8px' : '16px 18px', border: '1px solid var(--border)', borderRadius: thumb ? 4 : 6, display: 'flex', flexDirection: 'column', gap: thumb ? 2 : 6, ...(thumb ? { minWidth: 0 } : {}) }}
          >
            <span style={{ fontSize: thumb ? 8 : 10, letterSpacing: thumb ? '0.08em' : '0.1em', ...label, ...(thumb ? ell : {}) }}>{t.l}</span>
            <span className="mono" style={{ fontSize: thumb ? 15 : 30, fontWeight: 700, letterSpacing: '-0.03em', ...(thumb ? {} : { color: 'var(--ink)' }) }}>
              {t.val}
            </span>
          </div>
        ))}
      </div>
      {thumb ? (
        <div style={{ height: 46, display: 'flex', alignItems: 'flex-end', gap: 6, borderBottom: '1px solid var(--border)' }}>
          {s.bars.map((b, i) => (
            <div key={i} style={{ flex: 1, height: Math.max(3, Math.round(b.h * 0.3)), background: barBg(b.last), borderRadius: '2px 2px 0 0' }} />
          ))}
        </div>
      ) : (
        <div style={{ height: 160, display: 'flex', alignItems: 'flex-end', gap: 14, padding: '0 4px', borderBottom: '1px solid var(--border)' }}>
          {s.bars.map((b, i) => (
            <div key={i} style={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'flex-end', height: '100%', gap: 6 }}>
              <div style={{ width: '100%', height: b.h, background: barBg(b.last), borderRadius: '3px 3px 0 0', transition: 'height 700ms var(--ease-out), background 300ms' }} />
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function TogglesView({ s, size }: { s: Of<'toggles'>; size: Size }) {
  if (size === 'thumb') {
    return (
      <div style={{ display: 'flex', flexDirection: 'column' }}>
        {s.rows.slice(0, 4).map((r) => (
          <div
            key={r.l}
            style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10, padding: '6px 0', borderBottom: '1px solid var(--border)' }}
          >
            <span style={{ fontSize: 10, fontWeight: 600, ...ell }}>{r.l}</span>
            <span style={{ flex: 'none', width: 26, height: 14, borderRadius: 7, background: r.on ? 'var(--primary)' : 'var(--input)', position: 'relative' }}>
              <span style={{ position: 'absolute', top: 2, left: 2, width: 10, height: 10, borderRadius: '50%', background: '#fff', transform: `translateX(${r.on ? 12 : 0}px)` }} />
            </span>
          </div>
        ))}
      </div>
    )
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column' }}>
      {s.rows.map((r) => (
        <div
          key={r.l}
          style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 16, padding: '13px 14px', borderTop: '1px solid var(--border)', borderRadius: 4, background: r.focused ? 'var(--mint-soft)' : 'transparent', transition: 'background 300ms' }}
        >
          <div style={{ display: 'flex', flexDirection: 'column', gap: 2, minWidth: 0 }}>
            <span style={{ fontSize: 13.5, fontWeight: 600 }}>{r.l}</span>
            <span style={{ fontSize: 12, color: 'var(--muted-foreground)' }}>{r.d}</span>
          </div>
          <span style={{ flex: 'none', width: 38, height: 22, borderRadius: 11, background: r.on ? 'var(--primary)' : 'var(--input)', position: 'relative', transition: 'background 250ms' }}>
            <span style={{ position: 'absolute', top: 3, left: 3, width: 16, height: 16, borderRadius: '50%', background: '#fff', transform: `translateX(${r.on ? 16 : 0}px)`, transition: 'transform 250ms var(--ease-out)' }} />
          </span>
        </div>
      ))}
    </div>
  )
}

function FeedView({ s, size }: { s: Of<'feed'>; size: Size }) {
  if (size === 'thumb') {
    return (
      <div style={{ display: 'flex', flexDirection: 'column' }}>
        {s.items.slice(0, 4).map((it, i) => (
          <div
            key={i}
            style={{ display: 'grid', gridTemplateColumns: '34px 8px minmax(0, 1fr)', gap: 8, alignItems: 'center', padding: '6px 0', borderBottom: '1px solid var(--border)' }}
          >
            <span className="mono" style={{ fontSize: 9, color: 'var(--muted-foreground)' }}>
              {it.time}
            </span>
            <span style={{ width: 7, height: 7, borderRadius: '50%', background: FEED_DOT[it.tag] }} />
            <span style={{ fontSize: 10, fontWeight: 600, ...ell }}>{it.l}</span>
          </div>
        ))}
      </div>
    )
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
      {s.items.map((it, i) => (
        <div
          key={i}
          style={{ display: 'grid', gridTemplateColumns: '62px 12px 1fr', gap: 12, alignItems: 'start', padding: '12px 8px', borderTop: '1px solid var(--border)', animation: 'libPop 340ms ease-out' }}
        >
          <span className="mono" style={{ fontSize: 12, color: 'var(--muted-foreground)', paddingTop: 1 }}>
            {it.time}
          </span>
          <span style={{ width: 10, height: 10, borderRadius: '50%', marginTop: 4, background: FEED_DOT[it.tag] }} />
          <span style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
            <span style={{ fontSize: 13.5, fontWeight: 600 }}>{it.l}</span>
            <span style={{ fontSize: 12, color: 'var(--muted-foreground)' }}>{it.d}</span>
          </span>
        </div>
      ))}
    </div>
  )
}

export function SceneView({ sc, idx, size }: { sc: Scene; idx: number; size: Size }): ReactNode {
  const s = sceneState(sc, idx)
  switch (s.kind) {
    case 'list':
      return <ListView s={s} size={size} />
    case 'form':
      return <FormView s={s} size={size} />
    case 'flow':
      return <FlowView s={s} size={size} />
    case 'metrics':
      return <MetricsView s={s} size={size} />
    case 'toggles':
      return <TogglesView s={s} size={size} />
    case 'feed':
      return <FeedView s={s} size={size} />
  }
}
