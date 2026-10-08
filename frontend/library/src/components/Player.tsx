import type { MouseEvent, ReactNode } from 'react'
import { Icon } from '../icons'
import { formatSeconds, STEP_SECONDS, stepAt } from '../timing'
import { sceneState } from '../scene'
import type { Feature } from '../types'
import type { Clock } from '../player'
import { SceneView } from './SceneView'

const dot = <span style={{ width: 9, height: 9, borderRadius: '50%', background: 'var(--input)' }} />
const bar = <span style={{ width: 4, height: 14, borderRadius: 1, background: 'currentColor' }} />

export function Player({ feature, clock, onToggle, onSeek }: {
  feature: Feature
  clock: Clock
  onToggle(): void
  onSeek(frac: number): void
}): ReactNode {
  const sc = feature.sc
  const n = sc.steps.length
  const idx = stepAt(clock.tenths / 10, n)
  const s = sceneState(sc, idx)
  const total = Math.round(n * STEP_SECONDS * 10)
  const showPlay = !clock.playing && !clock.ended
  return (
    <div style={{ borderRadius: 10, overflow: 'hidden', background: 'var(--surface)', boxShadow: 'var(--shadow-elegant)' }}>
      <div style={{ padding: '28px 28px 0' }}>
        <div style={{ background: 'var(--card)', borderRadius: 8, overflow: 'hidden', color: 'var(--ink)' }}>
          <div style={{ height: 36, display: 'flex', alignItems: 'center', gap: 14, padding: '0 14px', background: 'var(--muted)', borderBottom: '1px solid var(--border)' }}>
            <span style={{ display: 'flex', gap: 6 }}>{dot}{dot}{dot}</span>
            <span className="mono" style={{ fontSize: 11, color: 'var(--muted-foreground)', letterSpacing: '0.03em' }}>{sc.win}</span>
          </div>
          <div style={{ position: 'relative', height: 350, padding: '20px 22px', overflow: 'hidden' }}>
            <SceneView sc={sc} idx={idx} size="player" />
          </div>
        </div>
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 16, padding: '18px 28px 6px', minHeight: 66 }}>
        <span className="mono" style={{ flex: 'none', fontSize: 12, fontWeight: 700, color: 'var(--accent)', letterSpacing: '0.08em' }}>{s.stepNum}</span>
        <span style={{ fontSize: 16, fontWeight: 600, color: 'var(--surface-foreground)', letterSpacing: '-0.01em' }}>{s.cap}</span>
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 16, padding: '8px 28px 22px' }}>
        <button
          type="button"
          className="lib-play"
          aria-label="Play or pause"
          onClick={onToggle}
          style={{ flex: 'none', width: 40, height: 40, borderRadius: '50%', border: 0, cursor: 'pointer', background: 'var(--accent)', color: 'var(--accent-foreground)', display: 'grid', placeItems: 'center' }}
        >
          {clock.playing && <span style={{ display: 'flex', gap: 4 }}>{bar}{bar}</span>}
          {showPlay && <Icon name="play" size={16} />}
          {clock.ended && <Icon name="rotate-cw" size={16} />}
        </button>
        <span className="mono" style={{ flex: 'none', width: 82, fontSize: 12, color: 'var(--surface-body)' }}>
          {`${formatSeconds(clock.tenths / 10)} / ${formatSeconds(n * STEP_SECONDS)}`}
        </span>
        <div
          id="lib-scrub"
          onClick={(e: MouseEvent<HTMLDivElement>) => {
            const r = e.currentTarget.getBoundingClientRect()
            onSeek((e.clientX - r.left) / r.width)
          }}
          style={{ flex: 1, height: 24, display: 'flex', alignItems: 'center', cursor: 'pointer' }}
        >
          <div style={{ position: 'relative', width: '100%', height: 6, borderRadius: 3, background: 'var(--on-dark-10)' }}>
            <div style={{ position: 'absolute', left: 0, top: 0, bottom: 0, width: `${Math.min(100, (clock.tenths / total) * 100)}%`, borderRadius: 3, background: 'var(--accent)', transition: 'width 100ms linear' }} />
            {sc.steps.slice(1).map((_, i) => (
              <span key={i} style={{ position: 'absolute', top: -3, bottom: -3, left: `${((i + 1) / n) * 100}%`, width: 2, background: 'var(--surface)', borderRadius: 1 }} />
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}
