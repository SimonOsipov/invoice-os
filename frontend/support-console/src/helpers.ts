// Pure presentation helpers. Everything here returns design-system token NAMES, never literals.

import { formatShown } from './metricsApi'
import type { Severity } from './types'

export interface StatusStyle {
  bg: string
  border: string
  text: string
  label: string
}

// Rule severity: warn is amber, info is muted, there is no green.
const SEVERITY: Record<Severity, StatusStyle> = {
  error: { bg: 'var(--status-red-bg)', border: 'var(--status-red-border)', text: 'var(--status-red-text)', label: 'ERROR' },
  warn: { bg: 'var(--status-amber-bg)', border: 'var(--status-amber-border)', text: 'var(--status-amber-text)', label: 'WARN' },
  info: { bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)', label: 'INFO' },
}

export function severityStyle(severity: Severity): StatusStyle {
  return SEVERITY[severity]
}

// Sparkline paths in a fixed 220x44 box; the range floors at 1 so a flat series draws.
export const SPARK_WIDTH = 220
export const SPARK_HEIGHT = 44

export function sparkline(points: number[]): { line: string; area: string } {
  if (points.length < 2) {
    return { line: '', area: '' }
  }
  const max = Math.max(...points)
  const min = Math.min(...points)
  const range = max - min || 1
  const step = SPARK_WIDTH / (points.length - 1)
  const xy = points.map((p, i) => [i * step, SPARK_HEIGHT - 4 - ((p - min) / range) * (SPARK_HEIGHT - 10)] as const)
  const line = xy.map(([x, y], i) => `${i ? 'L' : 'M'}${x.toFixed(1)} ${y.toFixed(1)}`).join(' ')
  const area = `${line} L${SPARK_WIDTH} ${SPARK_HEIGHT} L0 ${SPARK_HEIGHT} Z`
  return { line, area }
}

export const CHART_W = 600
export const CHART_H = 160

export function niceCeil(v: number): number {
  if (!(v > 0)) {
    return 1
  }
  const mag = 10 ** Math.floor(Math.log10(v))
  const m = v / mag
  const n = m <= 1 ? 1 : m <= 2 ? 2 : m <= 5 ? 5 : 10
  return n * mag
}

export function chartX(t: number, start: number, end: number): number {
  if (end <= start) {
    return 0
  }
  return Math.min(CHART_W, Math.max(0, ((t - start) / (end - start)) * CHART_W))
}

export function chartY(v: number, yMax: number): number {
  return CHART_H - (v / yMax) * CHART_H
}

export function chartPath(points: [number, number][], o: { start: number; end: number; step: number; yMax: number }): string {
  const runs: string[][] = []
  let prev: number | null = null
  for (const [t, v] of points) {
    const xy = `${chartX(t, o.start, o.end).toFixed(1)} ${chartY(v, o.yMax).toFixed(1)}`
    if (prev === null || t - prev > 1.5 * o.step) {
      runs.push([`M${xy}`])
    } else {
      runs[runs.length - 1].push(`L${xy}`)
    }
    prev = t
  }
  // a one-point run gets a zero-length segment so the round cap paints a dot
  return runs.map((r) => (r.length === 1 ? `${r[0]} h0` : r.join(' '))).join(' ')
}

export type Aligned = [number[], ...(number | null)[][]]
type Pts = { points: [number, number][] }

// x = sorted unique union of the step grid and every point time; a null marks a step a line misses.
export function alignSeries(lines: Pts[], start: number, end: number, step: number): Aligned {
  const times = new Set<number>()
  for (let k = 0; k <= Math.floor((end - start) / step); k++) times.add(start + k * step)
  for (const l of lines) for (const [t] of l.points) times.add(t)
  const x = [...times].sort((a, b) => a - b)
  return [x, ...lines.map((l) => {
    const at = new Map(l.points)
    return x.map((t) => at.get(t) ?? null)
  })]
}

export function yTop(lines: Pts[], guides: { value: number }[]): number {
  return Math.max(0, ...lines.flatMap((l) => l.points.map(([, v]) => v)), ...guides.map((g) => g.value))
}

const pad = (n: number) => String(n).padStart(2, '0')

// t is epoch seconds; ranges of a day or more show the date.
export function utcClock(t: number, long: boolean): string {
  const d = new Date(t * 1000)
  const hm = `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}`
  return long ? `${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())} ${hm}` : `${hm} UTC`
}

export type ReadoutRow = { name: string; color: string; value: string }

// Coloured lines first, folded lines after, each in input order.
export function readoutRows(lines: { name: string; color: string; unit: string; folded?: boolean }[], aligned: Aligned, idx: number, isolated: string | null): ReadoutRow[] {
  const shownLines = lines.map((l, i) => ({ l, i })).filter(({ l }) => isolated === null || l.name === isolated)
  const ordered = [...shownLines.filter(({ l }) => !l.folded), ...shownLines.filter(({ l }) => l.folded)]
  return ordered.map(({ l, i }) => {
    const v = idx >= 0 && idx < aligned[0].length ? aligned[i + 1][idx] : null
    return { name: l.name, color: l.color, value: v === null ? '—' : formatShown(l.unit, v) }
  })
}

const GAP = 12
const MARGIN = 8

export function readoutPlacement(at: { x: number; y: number }, plot: { left: number; top: number; width: number }, box: { width: number; height: number }, view: { width: number; height: number }): { left: number; top: number } {
  const left = at.x > plot.left + plot.width / 2 ? at.x - GAP - box.width : at.x + GAP
  return {
    left: Math.max(MARGIN, Math.min(left, view.width - box.width - MARGIN)),
    top: Math.max(MARGIN, Math.min(plot.top, view.height - box.height - MARGIN)),
  }
}

export const tokenName = (v: string): string | null => /^var\(\s*(--[\w-]+)\s*\)$/.exec(v)?.[1] ?? null

// Dot, stroke and fill derive from one tone so a tile cannot mix them.
export function healthTone(tone: 'green' | 'amber' | 'red' | 'muted'): { dot: string; stroke: string; fill: string } {
  if (tone === 'amber') {
    return { dot: 'var(--status-amber-text)', stroke: 'var(--status-amber-text)', fill: 'var(--status-amber-bg)' }
  }
  if (tone === 'red') {
    return { dot: 'var(--status-red-text)', stroke: 'var(--status-red-text)', fill: 'var(--status-red-bg)' }
  }
  if (tone === 'muted') {
    return { dot: 'var(--status-muted-text)', stroke: 'var(--status-muted-text)', fill: 'var(--status-muted-bg)' }
  }
  return { dot: 'var(--status-green-text)', stroke: 'var(--action)', fill: 'var(--action-tint)' }
}

const SENTRY_ISSUES = 'https://as-comply.sentry.io/issues/'
export const sentryTenantUrl = (id: string) => `${SENTRY_ISSUES}?query=tenant_id:${encodeURIComponent(id)}&project=4512190351474768&statsPeriod=14d`
export const sentryServiceUrl = (name: string) => `${SENTRY_ISSUES}?query=server_name:${encodeURIComponent(name)}&project=4512190351474768&statsPeriod=14d`
