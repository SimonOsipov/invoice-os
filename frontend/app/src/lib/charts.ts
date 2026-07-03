// Chart-path builders ported exactly from the prototype (Platform.dc.html ~L1084-1116):
// sparkline paths, the 12-week readiness trend, the status donut, and the top-failures list.

import type { ChartScore, DonutSeg, FailureRow, Invoice, ValidationResult } from '../types'

export function spark(vals: number[]): string {
  const w = 88
  const h = 30
  const p = 3
  const mn = Math.min(...vals)
  const mx = Math.max(...vals)
  const sp = mx - mn || 1
  return vals
    .map((v, i) => {
      const x = (i / (vals.length - 1)) * (w - 2 * p) + p
      const y = h - p - ((v - mn) / sp) * (h - 2 * p)
      return (i ? 'L' : 'M') + x.toFixed(1) + ' ' + y.toFixed(1)
    })
    .join(' ')
}

export function trend(rnd: () => number, dir: number): number[] {
  return Array.from({ length: 12 }, (_, i) => (dir > 0 ? i : 11 - i) + rnd() * 3.2)
}

export function chartScore(rnd: () => number, finalScore: number): ChartScore {
  const start = Math.max(22, finalScore - (8 + Math.round(rnd() * 10)))
  const vals = Array.from({ length: 12 }, (_, i) => {
    const t = i / 11
    return start + (finalScore - start) * t + (rnd() - 0.5) * 4
  })
  vals[11] = finalScore
  const cw = 680
  const ch = 176
  const pts = vals.map((v, i): [number, number] => [(i / 11) * cw, ch - 8 - (Math.max(0, Math.min(100, v)) / 100) * (ch - 26)])
  const line = 'M' + pts.map((p) => p[0].toFixed(1) + ' ' + p[1].toFixed(1)).join(' L ')
  const delta = Math.round(finalScore - vals[0])
  return {
    line,
    area: line + ` L ${cw} ${ch} L 0 ${ch} Z`,
    grid: [0.25, 0.5, 0.75].map((f) => (ch * f).toFixed(1)),
    months: ['April', 'May', 'June'],
    now: finalScore,
    deltaLabel: (delta >= 0 ? '▲ +' + delta : '▼ ' + delta) + ' pts vs 12 wks',
  }
}

export function donutFrom(counts: Record<string, number>): { seg: DonutSeg[]; meta: { r: number; total: string } } {
  const order: [string, string][] = [
    ['Transmitted', 'var(--status-green-text)'],
    ['Approved', 'var(--teal-400)'],
    ['Pending', 'var(--status-amber-text)'],
    ['Rejected', 'var(--status-red-text)'],
    ['Draft', 'var(--status-muted-border)'],
  ]
  const segs = order.map(([label, color]) => ({ label, color, count: counts[label] || 0 })).filter((s) => s.count > 0)
  const total = segs.reduce((s, x) => s + x.count, 0) || 1
  const R = 49
  const C = 2 * Math.PI * R
  let acc = 0
  const seg: DonutSeg[] = segs.map((s) => {
    const len = (s.count / total) * C
    const o: DonutSeg = {
      label: s.label,
      color: s.color,
      count: String(s.count),
      pct: Math.round((s.count / total) * 100) + '%',
      dash: len.toFixed(1) + ' ' + (C - len).toFixed(1),
      offset: (-acc).toFixed(1),
    }
    acc += len
    return o
  })
  return { seg, meta: { r: R, total: String(total) } }
}

const FAILURE_MAP: Record<string, [string, string]> = {
  tin: ['Buyer TIN missing or malformed', 'MBS-TIN-01'],
  addr: ['Billing address incomplete', 'MBS-ADR-02'],
}

export function failuresFrom(inv: Invoice[], errs: ValidationResult[]): FailureRow[] {
  const tally: Record<string, number> = {}
  inv.forEach((_it, k) => errs[k].errors.forEach((e) => (tally[e.id] = (tally[e.id] || 0) + 1)))
  const list: FailureRow[] = Object.keys(tally).map((id) => ({
    label: FAILURE_MAP[id][0],
    rule: FAILURE_MAP[id][1],
    glyphId: 'cross',
    count: tally[id],
    bar: '0%',
  }))
  list.sort((a, b) => b.count - a.count)
  const fmax = Math.max(1, ...list.map((f) => f.count))
  list.forEach((f) => (f.bar = Math.round((f.count / fmax) * 100) + '%'))
  return list
}
