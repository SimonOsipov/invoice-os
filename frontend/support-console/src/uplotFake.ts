// Test-only stand-in for uPlot: jsdom has no canvas. Records options, hooks and calls.
import type uPlot from 'uplot'

type Hook = (u: FakeUPlot, ...args: unknown[]) => void
export type Call = [name: string, ...args: unknown[]]

// Canvas stand-in; each stroke logs the style and dash in force.
export class RecordingCtx {
  log: Call[] = []
  strokeStyle = ''
  lineWidth = 1
  private dash: number[] = []
  save() {}
  restore() {}
  beginPath() {}
  moveTo(x: number, y: number) {
    this.log.push(['moveTo', x, y])
  }
  lineTo(x: number, y: number) {
    this.log.push(['lineTo', x, y])
  }
  setLineDash(d: number[]) {
    this.dash = d
    this.log.push(['setLineDash', d])
  }
  stroke() {
    this.log.push(['stroke', this.strokeStyle, this.dash])
  }
}

export class FakeUPlot {
  static instances: FakeUPlot[] = []
  static tzDate = (d: Date, _tz: string) => new Date(d.getTime())

  root = document.createElement('div')
  over = document.createElement('div')
  ctx = new RecordingCtx() as unknown as CanvasRenderingContext2D
  bbox = { left: 0, top: 0, width: 0, height: 0 }
  scales: Record<string, { min: number | null; max: number | null }>
  series: (uPlot.Series & { show: boolean })[]
  data: uPlot.AlignedData
  width: number
  height: number
  cursor: { left: number; top: number; idx: number | null } = { left: -10, top: -10, idx: null }
  select = { left: 0, top: 0, width: 0, height: 0 }
  calls: Call[] = []
  destroyed = false

  constructor(public opts: uPlot.Options, data: uPlot.AlignedData, target: HTMLElement) {
    FakeUPlot.instances.push(this)
    this.data = data
    this.width = opts.width
    this.height = opts.height
    this.bbox = { left: 0, top: 0, width: opts.width, height: opts.height }
    this.series = opts.series.map((s) => ({ ...s, show: s.show ?? true }))
    this.scales = { x: { min: opts.scales?.x?.min ?? null, max: opts.scales?.x?.max ?? null }, y: { min: 0, max: 1 } }
    this.over.className = 'u-over'
    this.root.className = 'uplot'
    this.root.append(this.over)
    target.append(this.root)
  }

  // Proportional to width, so a resize test can see the position move.
  valToPos(v: number, key: string, _canvas?: boolean): number {
    const { min, max } = this.scales[key]
    const span = (max ?? 1) - (min ?? 0) || 1
    return key === 'x' ? ((v - (min ?? 0)) / span) * this.width : this.height - ((v - (min ?? 0)) / span) * this.height
  }

  posToVal(pos: number, key: string): number {
    const { min, max } = this.scales[key]
    return (min ?? 0) + (pos / this.width) * ((max ?? 1) - (min ?? 0))
  }

  setSelect(sel: { left: number; top: number; width: number; height: number }, fire = true) {
    this.calls.push(['setSelect', sel, fire])
    this.select = { ...this.select, ...sel }
    if (fire) this.fire('setSelect')
  }

  setData(data: uPlot.AlignedData, reset?: boolean) {
    this.calls.push(['setData', data, reset])
    this.data = data
  }

  setScale(key: string, lim: { min: number; max: number }) {
    this.calls.push(['setScale', key, lim])
    this.scales[key] = { min: lim.min, max: lim.max }
    this.fire('setScale', key)
  }

  setCursor(pos: { left: number; top: number }, fire = true) {
    this.calls.push(['setCursor', pos])
    this.cursor.left = pos.left
    this.cursor.top = pos.top
    const xs = Array.from(this.data[0])
    const dist = (i: number) => Math.abs(this.valToPos(xs[i], 'x') - pos.left)
    this.cursor.idx = pos.left < 0 || !xs.length ? null : xs.reduce((best, _, i) => (dist(i) < dist(best) ? i : best), 0)
    if (fire) this.fire('setCursor')
  }

  setSize(size: { width: number; height: number }) {
    this.calls.push(['setSize', size])
    this.width = size.width
    this.height = size.height
    this.fire('setSize')
  }

  destroy() {
    this.calls.push(['destroy'])
    this.destroyed = true
    this.root.remove()
  }

  fire(name: string, ...args: unknown[]) {
    const h = (this.opts.hooks as Record<string, Hook | Hook[] | undefined> | undefined)?.[name]
    for (const f of [h].flat()) f?.(this, ...args)
  }
}
