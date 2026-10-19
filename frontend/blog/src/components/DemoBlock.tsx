import { useEffect, useReducer } from 'react'
import { SceneView } from '../../../library/src/components/SceneView'
import { nextClock, START } from '../../../library/src/player'
import { formatSeconds, STEP_SECONDS, stepAt } from '../../../library/src/timing'
import { DEMOS, toScene, type BlogDemo } from '../demos'
import { Icon } from '../icons'

const dot = <span style={{ width: 8, height: 8, borderRadius: '50%', background: 'var(--input)' }} />
const two = (n: number) => String(n).padStart(2, '0')

export function DemoBlock({ name }: { name: string }) {
  const d = (DEMOS as Record<string, BlogDemo | undefined>)[name]
  return d ? <Demo d={d} /> : null
}

function Demo({ d }: { d: BlogDemo }) {
  const n = d.steps.length
  const [clock, dispatch] = useReducer((c: typeof START, a: Parameters<typeof nextClock>[1]) => nextClock(c, a, n), {
    ...START,
    playing: false,
  })
  useEffect(() => {
    const t = setInterval(() => dispatch({ type: 'tick' }), 100)
    return () => clearInterval(t)
  }, [])
  const step = stepAt(clock.tenths / 10, n)
  const total = n * STEP_SECONDS

  return (
    <div data-testid="blog-demo" style={{ margin: '8px 0', borderRadius: 10, overflow: 'hidden', background: 'var(--surface)', boxShadow: 'var(--shadow-elegant)' }}>
      <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 16, flexWrap: 'wrap', padding: '20px 22px 16px' }}>
        <span className="mono" style={{ fontSize: 10, fontWeight: 700, letterSpacing: '0.1em', color: 'var(--accent)' }}>FEATURE LIBRARY · DEMO</span>
        <span style={{ fontSize: 15, fontWeight: 700, color: 'var(--surface-foreground)', letterSpacing: '-0.01em' }}>{d.title}</span>
      </div>
      <div style={{ padding: '0 22px' }}>
        <div style={{ background: 'var(--card)', borderRadius: 8, overflow: 'hidden', color: 'var(--ink)' }}>
          <div style={{ height: 32, display: 'flex', alignItems: 'center', gap: 12, padding: '0 12px', background: 'var(--muted)', borderBottom: '1px solid var(--border)' }}>
            <span style={{ display: 'flex', gap: 5 }}>{dot}{dot}{dot}</span>
            <span className="mono" style={{ fontSize: 10.5, color: 'var(--muted-foreground)' }}>{d.win}</span>
          </div>
          <div data-testid="blog-demo-scene" style={{ height: 248, padding: '16px 18px', overflow: 'hidden' }}>
            <SceneView sc={toScene(d)} idx={step} size="inline" />
          </div>
        </div>
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '16px 22px 4px', minHeight: 56 }}>
        <span className="mono" style={{ flex: 'none', fontSize: 11, fontWeight: 700, color: 'var(--accent)', letterSpacing: '0.08em' }}>
          {two(step + 1)} / {two(n)}
        </span>
        <span style={{ fontSize: 15, fontWeight: 600, color: 'var(--surface-foreground)', letterSpacing: '-0.01em' }}>{d.steps[step].cap}</span>
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '8px 22px 20px' }}>
        <button
          type="button"
          className="blog-play"
          aria-label="Play or pause"
          onClick={() => dispatch({ type: 'toggle' })}
          style={{ flex: 'none', width: 36, height: 36, borderRadius: '50%', border: 0, cursor: 'pointer', background: 'var(--accent)', color: 'var(--accent-foreground)', display: 'grid', placeItems: 'center' }}
        >
          {clock.playing ? (
            <span style={{ display: 'flex', gap: 4 }}>
              <span style={{ width: 4, height: 13, borderRadius: 1, background: 'currentColor' }} />
              <span style={{ width: 4, height: 13, borderRadius: 1, background: 'currentColor' }} />
            </span>
          ) : (
            <Icon name={clock.ended ? 'rotate-cw' : 'play'} size={15} />
          )}
        </button>
        <span className="mono" style={{ flex: 'none', width: 76, fontSize: 11.5, color: 'var(--surface-body)' }}>
          {formatSeconds(clock.tenths / 10)} / {formatSeconds(total)}
        </span>
        <div
          data-testid="blog-demo-scrub"
          onClick={(e) => {
            const r = e.currentTarget.getBoundingClientRect()
            dispatch({ type: 'seek', frac: (e.clientX - r.left) / r.width })
          }}
          style={{ flex: 1, height: 22, display: 'flex', alignItems: 'center', cursor: 'pointer' }}
        >
          <div style={{ position: 'relative', width: '100%', height: 6, borderRadius: 3, background: 'var(--on-dark-10)' }}>
            <div style={{ position: 'absolute', left: 0, top: 0, bottom: 0, width: `${(clock.tenths / (n * STEP_SECONDS * 10)) * 100}%`, borderRadius: 3, background: 'var(--accent)', transition: 'width 100ms linear' }} />
            {d.steps.slice(0, -1).map((_, i) => (
              <span key={i} style={{ position: 'absolute', top: -3, bottom: -3, left: `${((i + 1) / n) * 100}%`, width: 2, background: 'var(--surface)', borderRadius: 1 }} />
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}
