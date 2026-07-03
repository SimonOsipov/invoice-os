// Shared "scanline" progress mock used by both the upload-parsing and MBS-validation
// steps of the create flow — structurally identical in the prototype (Platform.dc.html
// ~L450-467 for parsing, ~L530-547 for validating), just different label lists / timings.

import type { ReactNode } from 'react'
import { tickGlyph13 } from '../glyphs'

function StepIcon({ state }: { state: 'done' | 'active' | 'pending' }) {
  if (state === 'done') return tickGlyph13
  if (state === 'active') return <span style={{ width: 13, height: 13, borderRadius: 99, border: '2px solid var(--bg-3)', borderTopColor: 'var(--accent)', display: 'block', animation: 'spin 0.7s linear infinite' }} />
  return <span style={{ width: 7, height: 7, borderRadius: 99, border: '1.5px solid var(--line-3)', display: 'block' }} />
}

export function ScanlineSteps({
  title,
  subtitle,
  labels,
  idx,
  unitLabel,
  transformMs,
  widthMs,
}: {
  title: ReactNode
  subtitle: ReactNode
  labels: string[]
  idx: number
  unitLabel: string
  transformMs: number
  widthMs: number
}) {
  const scroll = -(idx * 30) + 'px'
  const progressPct = Math.min(100, Math.round((idx / labels.length) * 100))

  return (
    <div style={{ background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 8, padding: '44px 20px 52px', display: 'flex', flexDirection: 'column', alignItems: 'center' }}>
      <div style={{ fontSize: 17, fontWeight: 600, marginBottom: 6 }}>{title}</div>
      <div className="mono" style={{ fontSize: 12, color: 'var(--fg-3)', marginBottom: 24 }}>
        {subtitle}
      </div>
      <div
        style={{
          height: 132,
          width: 380,
          maxWidth: '100%',
          overflow: 'hidden',
          position: 'relative',
          marginBottom: 26,
          WebkitMaskImage: 'linear-gradient(180deg, transparent, #000 30%, #000 70%, transparent)',
          maskImage: 'linear-gradient(180deg, transparent, #000 30%, #000 70%, transparent)',
        }}
      >
        <div style={{ position: 'absolute', left: 0, right: 0, top: 51, transform: `translateY(${scroll})`, transition: `transform ${transformMs}ms ease-out` }}>
          {labels.map((label, i) => {
            const done = i < idx
            const act = i === idx
            const color = act ? 'var(--accent)' : done ? 'var(--fg-2)' : 'var(--fg-4)'
            const weight = act ? 600 : done ? 500 : 400
            const iconColor = act ? 'var(--accent)' : done ? 'var(--status-green-text)' : 'var(--fg-4)'
            return (
              <div key={label} style={{ height: 30, display: 'flex', alignItems: 'center', gap: 11, padding: '0 16px' }}>
                <span style={{ flex: 'none', width: 16, height: 16, display: 'grid', placeItems: 'center', color: iconColor }}>
                  <StepIcon state={done ? 'done' : act ? 'active' : 'pending'} />
                </span>
                <span style={{ fontSize: 14, color, fontWeight: weight, whiteSpace: 'nowrap' }}>{label}</span>
              </div>
            )
          })}
        </div>
      </div>
      <div style={{ width: 320, maxWidth: '100%', height: 6, borderRadius: 99, background: 'var(--bg-3)', overflow: 'hidden' }}>
        <div style={{ height: '100%', width: progressPct + '%', borderRadius: 99, background: 'var(--accent)', transition: `width ${widthMs}ms ease-out` }} />
      </div>
      <div className="mono" style={{ fontSize: 11, color: 'var(--fg-3)', marginTop: 12 }}>
        {progressPct}% {unitLabel}
      </div>
    </div>
  )
}
