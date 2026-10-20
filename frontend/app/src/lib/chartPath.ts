// Pure SVG path maths for dashboard charts. Series in, path strings out.

// 88x30 sparkline path, 3px padding.
export function sparkPath(vals: number[]): string {
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

// 680x176 trend line and area for a 0-100 series; values clamp to that range.
export function trendPath(vals: number[]): { line: string; area: string } {
  const cw = 680
  const ch = 176
  const last = vals.length - 1
  const pts = vals.map((v, i): [number, number] => [
    (i / last) * cw,
    ch - 8 - (Math.max(0, Math.min(100, v)) / 100) * (ch - 26),
  ])
  const line = 'M' + pts.map((p) => p[0].toFixed(1) + ' ' + p[1].toFixed(1)).join(' L ')
  return { line, area: line + ` L ${cw} ${ch} L 0 ${ch} Z` }
}
