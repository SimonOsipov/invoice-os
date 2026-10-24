import type { CSSProperties } from 'react'
import { CHART_H, CHART_W, chartPath, chartX, chartY, niceCeil } from '../helpers'

type Line = { name: string; color: string; points: [number, number][] }
type Guide = { value: number; label: string; dashed: boolean }
type Marker = { t: number; title: string }

type Props = { lines: Line[]; start: number; end: number; step: number; guides?: Guide[]; markers?: Marker[] }

const TICK: CSSProperties = { fontSize: 11, color: 'var(--fg-3)', whiteSpace: 'nowrap' }
const DAY = 86400

const pad = (n: number) => String(n).padStart(2, '0')

// t is epoch seconds; ranges over two days show the date.
function clock(t: number, long: boolean): string {
  const d = new Date(t * 1000)
  const hm = `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}`
  return long ? `${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ${hm}` : `${hm} UTC`
}

export function TimeChart({ lines, start, end, step, guides = [], markers = [] }: Props) {
  const values = [...lines.flatMap((l) => l.points.map(([, v]) => v)), ...guides.map((g) => g.value)]
  const yMax = niceCeil(Math.max(0, ...values))
  const long = end - start > 2 * DAY
  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'auto minmax(0, 1fr)', columnGap: 8 }}>
      <div style={{ display: 'flex', flexDirection: 'column', justifyContent: 'space-between', height: CHART_H }}>
        <span style={TICK}>{yMax}</span>
        <span style={TICK}>0</span>
      </div>
      <svg viewBox={`0 0 ${CHART_W} ${CHART_H}`} width="100%" height={CHART_H} preserveAspectRatio="none" style={{ display: 'block', overflow: 'visible' }}>
        {guides.map((g) => (
          <line
            key={g.label}
            data-guide={g.label}
            x1={0}
            x2={CHART_W}
            y1={chartY(g.value, yMax)}
            y2={chartY(g.value, yMax)}
            stroke="var(--fg-3)"
            strokeWidth={1}
            strokeDasharray={g.dashed ? '4 4' : undefined}
            vectorEffect="non-scaling-stroke"
          />
        ))}
        {markers.map((m) => (
          <line key={`${m.t}-${m.title}`} data-deploy="" x1={chartX(m.t, start, end)} x2={chartX(m.t, start, end)} y1={0} y2={CHART_H} stroke="var(--line-3)" strokeWidth={1} vectorEffect="non-scaling-stroke">
            <title>{m.title}</title>
          </line>
        ))}
        {lines.map((l) => (
          <path key={l.name} data-line={l.name} d={chartPath(l.points, { start, end, step, yMax })} fill="none" stroke={l.color} strokeWidth={1.6} vectorEffect="non-scaling-stroke" strokeLinecap="round" strokeLinejoin="round" />
        ))}
      </svg>
      <span />
      <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 4 }}>
        <span style={TICK}>{clock(start, long)}</span>
        <span style={TICK}>{clock(end, long)}</span>
      </div>
    </div>
  )
}
