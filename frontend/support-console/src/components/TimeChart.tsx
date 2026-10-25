import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import type uPlot from 'uplot'
import { CHART_H, type Aligned, alignSeries, readoutPlacement, readoutRows, tokenName, utcClock, yTop } from '../helpers'

type Point = [number, number]
type Line = { name: string; color: string; unit: string; points: Point[]; folded?: boolean }
type Guide = { value: number; label: string; dashed: boolean }
type Marker = { t: number; title: string }

type Props = { lines: Line[]; start: number; end: number; step: number; guides?: Guide[]; markers?: Marker[]; zoom?: [number, number] | null; onZoom?: (w: [number, number]) => void; isolated?: string | null }

const X_AXIS_H = 28
const DAY = 86400
const NONE: never[] = []
const LABEL_GAP = 16
const MONO_ADVANCE = 0.6 // em; the advance of a monospace glyph
const LONG_LABEL_CHARS = 'MM-DD HH:MM'.length
const SHORT_LABEL_CHARS = 'HH:MM UTC'.length
type Tip = { idx: number; left: number; top: number }

// ceiling: a rejected import is not caught; the chart area stays empty and the global Sentry handler reports it
let loading: Promise<typeof import('./uplot')> | undefined
const load = () => (loading ??= import('./uplot'))

// Indices of values with a null on both sides: uPlot draws a zero-length path for them.
function lonePoints(values: ArrayLike<number | null | undefined>): number[] | null {
  const idxs: number[] = []
  for (let i = 0; i < values.length; i++) if (values[i] != null && values[i - 1] == null && values[i + 1] == null) idxs.push(i)
  return idxs.length ? idxs : null
}

// A selection narrower than two steps widens around its centre, clamped to [start, end].
function minWindow([a, b]: [number, number], { start, end, step }: Props): [number, number] {
  const span = Math.min(2 * step, end - start)
  if (b - a >= span) return [a, b]
  const lo = Math.max(start, Math.min((a + b) / 2 - span / 2, end - span))
  return [lo, lo + span]
}

function placeMarkers(u: uPlot, { markers = NONE, start, end }: Props) {
  let els = [...u.over.querySelectorAll<HTMLElement>('[data-deploy]')]
  if (els.length !== markers.length || els.some((e, i) => e.title !== markers[i].title)) {
    els.forEach((e) => e.remove())
    els = markers.map((m) => {
      const el = document.createElement('div')
      el.setAttribute('data-deploy', '')
      el.title = m.title
      el.style.cssText = 'position:absolute;top:0;bottom:0;width:7px;transform:translateX(-50%)'
      const line = document.createElement('div')
      line.style.cssText = 'position:absolute;top:0;bottom:0;left:3px;width:1px;background:var(--line-3)'
      el.append(line)
      u.over.append(el)
      return el
    })
  }
  const { min, max } = u.scales.x
  els.forEach((el, i) => {
    const t = Math.min(end, Math.max(start, markers[i].t))
    el.style.left = `${u.valToPos(t, 'x')}px`
    el.style.display = min != null && max != null && t >= min && t <= max ? '' : 'none'
  })
}

export function TimeChart(props: Props) {
  const root = useRef<HTMLDivElement>(null)
  const mount = useRef<HTMLDivElement>(null)
  const chart = useRef<uPlot | null>(null)
  const latest = useRef(props)
  latest.current = props
  const hover = useRef(false)
  const held = useRef<{ left: number; top: number } | null>(null)
  const box = useRef<HTMLDivElement>(null)
  const [tip, setTip] = useState<Tip | null>(null)
  const drawn = useRef<{ points: Point[][]; guides: string; start: number; end: number; step: number } | null>(null)

  const { lines, start, end, step, guides = NONE, markers = NONE } = props
  const seriesKey = lines.map((l) => `${l.name}\n${l.color}`).join('\n\n')
  const guidesKey = JSON.stringify(guides)
  const markersKey = JSON.stringify(markers)

  useEffect(() => {
    let dead = false
    let observer: ResizeObserver | undefined
    let u: uPlot | undefined
    load().then(({ default: UPlot }) => {
      const el = root.current!
      if (dead) return
      const css = getComputedStyle(el)
      const tok = (v: string) => {
        const n = tokenName(v)
        return n ? css.getPropertyValue(n).trim() : v
      }
      const fg3 = tok('var(--fg-3)')
      const line1 = tok('var(--line-1)')
      const font = `11px ${tok('var(--font-mono)')}`
      const cur = latest.current
      const axis = { stroke: fg3, font, grid: { stroke: line1, width: 1 }, ticks: { stroke: line1, width: 1 } }
      const opts: uPlot.Options = {
        width: el.clientWidth,
        height: CHART_H + X_AXIS_H,
        tzDate: (ts) => UPlot.tzDate(new Date(ts * 1e3), 'Etc/UTC'),
        legend: { show: false },
        cursor: {
          y: false,
          drag: { x: true, y: false, setScale: false, dist: 5 },
          bind: { dblclick: () => null },
          sync: { key: 'health', scales: ['x', null], filters: { pub: (t) => t !== 'mousedown' && t !== 'mouseup' } },
        },
        scales: {
          x: { time: true, auto: false, min: cur.zoom?.[0] ?? cur.start, max: cur.zoom?.[1] ?? cur.end },
          y: {
            range: (_u, _min, max) => {
              const top = Math.max(max ?? 0, yTop([], latest.current.guides ?? NONE))
              return [0, top > 0 ? top : 1]
            },
          },
        },
        axes: [
          {
            ...axis,
            size: X_AXIS_H,
            space: () => {
              const { start, end } = latest.current
              return (end - start >= DAY ? LONG_LABEL_CHARS : SHORT_LABEL_CHARS) * MONO_ADVANCE * 11 + LABEL_GAP
            },
            values: (_u, splits) => splits.map((t) => utcClock(t, latest.current.end - latest.current.start >= DAY)),
          },
          { ...axis, values: (_u, splits) => splits.map(String) },
        ],
        series: [
          {},
          ...cur.lines.map((l) => {
            const color = tok(l.color)
            return { label: l.name, stroke: color, width: 1.6, show: !cur.isolated || l.name === cur.isolated, points: { stroke: color, fill: color, filter: (self: uPlot, i: number, show: boolean) => (show ? null : lonePoints(self.data[i])) } }
          }),
        ],
        hooks: {
          draw: [
            (self) => {
              const { ctx, bbox } = self
              const r = devicePixelRatio || 1
              ctx.save()
              ctx.strokeStyle = fg3
              ctx.lineWidth = r
              for (const g of latest.current.guides ?? NONE) {
                const y = self.valToPos(g.value, 'y', true)
                ctx.beginPath()
                ctx.setLineDash(g.dashed ? [4 * r, 4 * r] : [])
                ctx.moveTo(bbox.left, y)
                ctx.lineTo(bbox.left + bbox.width, y)
                ctx.stroke()
              }
              ctx.restore()
              el.setAttribute('data-drawn', self.series.slice(1).filter((s) => s.show).map((s) => s.label).join(','))
            },
          ],
          setScale: [
            (self, key) => {
              if (key !== 'x') return
              el.setAttribute('data-x', `${self.scales.x.min},${self.scales.x.max}`)
              placeMarkers(self, latest.current)
            },
          ],
          setSelect: [
            (self) => {
              const { left, width } = self.select
              if (width <= 0) return
              latest.current.onZoom?.(minWindow([self.posToVal(left, 'x'), self.posToVal(left + width, 'x')], latest.current))
              self.setSelect({ left: 0, top: 0, width: 0, height: 0 }, false)
            },
          ],
          setSize: [(self) => placeMarkers(self, latest.current)],
          setCursor: [
            (self) => {
              const { left = -1, top = -1, idx } = self.cursor
              const on = left >= 0 && idx != null && idx >= 0
              el.setAttribute('data-cursor', on ? String(self.data[0][idx]) : '')
              if (hover.current) setTip(on ? { idx, left, top } : null)
            },
          ],
        },
      }
      u = new UPlot(opts, alignSeries(cur.lines, cur.start, cur.end, cur.step), mount.current!)
      chart.current = u
      drawn.current = { points: cur.lines.map((l) => l.points), guides: JSON.stringify(cur.guides ?? NONE), start: cur.start, end: cur.end, step: cur.step }
      el.setAttribute('data-x', `${cur.zoom?.[0] ?? cur.start},${cur.zoom?.[1] ?? cur.end}`)
      placeMarkers(u, cur)
      u.over.addEventListener('mouseenter', () => (hover.current = true))
      u.over.addEventListener('mouseleave', () => {
        hover.current = false
        setTip(null)
      })
      const carried = held.current
      if (carried && carried.left >= 0) u.setCursor(carried)
      let width = opts.width
      observer = new ResizeObserver((entries) => {
        const w = Math.floor(entries[0].contentRect.width)
        if (w > 0 && w !== width) {
          width = w
          u!.setSize({ width: w, height: CHART_H + X_AXIS_H })
        }
      })
      observer.observe(el)
    })
    return () => {
      dead = true
      observer?.disconnect()
      if (u) held.current = { left: u.cursor.left ?? -1, top: u.cursor.top ?? -1 }
      u?.destroy()
      chart.current = null
    }
  }, [seriesKey])

  useEffect(() => {
    const u = chart.current
    const prev = drawn.current
    if (!u || !prev) return
    const same = prev.start === start && prev.end === end && prev.step === step && prev.guides === guidesKey && prev.points.length === lines.length && lines.every((l, i) => l.points === prev.points[i])
    if (same) return
    drawn.current = { points: lines.map((l) => l.points), guides: guidesKey, start, end, step }
    u.setData(alignSeries(lines, start, end, step), false)
    u.setScale('x', { min: latest.current.zoom?.[0] ?? start, max: latest.current.zoom?.[1] ?? end })
    const { left = -1, top = -1 } = u.cursor
    if (left >= 0) u.setCursor({ left, top })
  }, [lines, start, end, step, guidesKey])

  const zoomMin = props.zoom?.[0]
  const zoomMax = props.zoom?.[1]
  useEffect(() => {
    chart.current?.setScale('x', { min: zoomMin ?? latest.current.start, max: zoomMax ?? latest.current.end })
  }, [zoomMin, zoomMax])

  useEffect(() => {
    if (chart.current) placeMarkers(chart.current, latest.current)
  }, [markersKey])

  const { isolated = null } = props
  useEffect(() => {
    latest.current.lines.forEach((l, i) => chart.current?.setSeries(i + 1, { show: !isolated || l.name === isolated }))
  }, [isolated])

  const long = end - start >= DAY
  const uData = chart.current?.data
  const aligned = tip && uData && uData.length === lines.length + 1 ? (uData as Aligned) : null
  const rows = tip && aligned ? readoutRows(lines, aligned, tip.idx, isolated) : []

  useLayoutEffect(() => {
    const over = chart.current?.over
    const b = box.current
    if (!tip || !over || !b) return
    const r = over.getBoundingClientRect()
    const at = readoutPlacement({ x: r.left + tip.left, y: r.top + tip.top }, r, b.getBoundingClientRect(), { width: window.innerWidth, height: window.innerHeight })
    b.style.left = `${at.left}px`
    b.style.top = `${at.top}px`
  })

  return (
    <div ref={root} data-chart="" style={{ position: 'relative', minWidth: 0, minHeight: CHART_H + X_AXIS_H }}>
      <div ref={mount} />
      {tip && aligned && (
        <div
          ref={box}
          data-readout=""
          style={{ position: 'fixed', zIndex: 70, pointerEvents: 'none', left: 0, top: 0, background: 'var(--bg-2)', border: '1px solid var(--line-1)', borderRadius: 'var(--radius-sm)', padding: '6px 8px', fontSize: 11, whiteSpace: 'nowrap' }}
        >
          <div data-readout-time="" style={{ color: 'var(--fg-3)', marginBottom: 4 }}>
            {utcClock(aligned[0][tip.idx], long)}
            {long ? ' UTC' : ''}
          </div>
          {rows.map((r) => (
            <div key={r.name} data-readout-row={r.name} style={{ display: 'flex', alignItems: 'center', gap: 6, lineHeight: '16px' }}>
              <span style={{ width: 8, height: 8, flex: 'none', background: r.color }} />
              <span style={{ color: 'var(--fg-3)' }}>{r.name}</span>
              <span className="mono" style={{ marginLeft: 'auto', paddingLeft: 12, color: 'var(--fg-2)' }}>
                {r.value}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
