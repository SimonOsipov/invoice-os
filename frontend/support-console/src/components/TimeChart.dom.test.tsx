// @vitest-environment jsdom
import { act, cleanup, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { alignSeries, CHART_H } from '../helpers'
import { formatShown } from '../metricsApi'
import { FakeUPlot, type RecordingCtx } from '../uplotFake'
import { TimeChart } from './TimeChart'

vi.mock('./uplot', () => ({ default: FakeUPlot }))

const TOKENS: Record<string, string> = {
  '--series-1': '#111111',
  '--fg-4': '#222222',
  '--fg-3': '#333333',
  '--line-1': '#444444',
  '--font-mono': 'TestMono',
}

class Observers {
  static all: Observers[] = []
  disconnected = false
  constructor(public cb: (e: { contentRect: { width: number } }[]) => void) {
    Observers.all.push(this)
  }
  observe() {}
  disconnect() {
    this.disconnected = true
  }
  report(width: number) {
    this.cb([{ contentRect: { width } }])
  }
}

const lines = (over: { a?: [number, number][]; b?: [number, number][] } = {}) => [
  { name: 'a', color: 'var(--series-1)', unit: 'count', points: over.a ?? ([[0, 1], [60, 2]] as [number, number][]) },
  { name: 'b', color: 'var(--fg-4)', unit: 'count', points: over.b ?? ([[0, 3], [60, 4]] as [number, number][]), folded: true },
]
const base = { start: 0, end: 3600, step: 60 }
const mount = async (props: Partial<Parameters<typeof TimeChart>[0]> = {}) => {
  const view = render(<TimeChart lines={lines()} {...base} {...props} />)
  await waitFor(() => expect(FakeUPlot.instances.length).toBeGreaterThan(0))
  return { view, u: () => FakeUPlot.instances[FakeUPlot.instances.length - 1] }
}
const root = (c: HTMLElement) => c.querySelector('[data-chart]')!
const opts = (u: FakeUPlot) => u.opts as any

beforeEach(() => {
  FakeUPlot.instances.length = 0
  Observers.all.length = 0
  vi.stubGlobal('ResizeObserver', Observers)
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(1000)
  vi.spyOn(window, 'getComputedStyle').mockReturnValue({ getPropertyValue: (n: string) => ` ${TOKENS[n] ?? ''}` } as CSSStyleDeclaration)
})
afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('TimeChart wrapper', () => {
  it('TimeChart creates one uPlot with a series per line in token colours', async () => {
    const { u } = await mount()
    expect(FakeUPlot.instances).toHaveLength(1)
    const o = opts(u())
    expect(o.series.slice(1).map((s: any) => s.label)).toEqual(['a', 'b'])
    expect(o.series.slice(1).map((s: any) => s.stroke)).toEqual(['#111111', '#222222'])
    expect(o.series.slice(1).map((s: any) => s.width)).toEqual([1.6, 1.6])
    expect(o.legend.show).toBe(false)
    expect(u().data).toEqual(alignSeries(lines(), 0, 3600, 60))
  })

  it('the draw hook writes the shown series names', async () => {
    const { view, u } = await mount()
    u().series[2].show = false
    u().fire('draw')
    expect(root(view.container).getAttribute('data-drawn')).toBe('a')
    u().series[2].show = true
    u().fire('draw')
    expect(root(view.container).getAttribute('data-drawn')).toBe('a,b')
  })

  it('the draw hook strokes each guide across the plot in canvas pixels', async () => {
    vi.stubGlobal('devicePixelRatio', 2)
    const spy = vi.spyOn(FakeUPlot.prototype, 'valToPos')
    const { u } = await mount({ guides: [{ value: 80, label: '80 %', dashed: true }, { value: 64, label: 'budget 64', dashed: false }] })
    u().fire('draw')
    const log = (u().ctx as unknown as RecordingCtx).log
    expect(log.filter(([n]) => n === 'moveTo').map(([, x]) => x)).toEqual([u().bbox.left, u().bbox.left])
    expect(log.filter(([n]) => n === 'lineTo').map(([, x]) => x)).toEqual([u().bbox.left + u().bbox.width, u().bbox.left + u().bbox.width])
    expect(log.filter(([n]) => n === 'setLineDash').map(([, d]) => d)).toEqual([[8, 8], []])
    expect((u().ctx as unknown as RecordingCtx).lineWidth).toBe(2)
    const guideCalls = spy.mock.calls.filter(([v, k]) => k === 'y' && (v === 80 || v === 64))
    expect(guideCalls).toEqual([[80, 'y', true], [64, 'y', true]])
  })

  it('the draw hook paints each guide dashed or solid in the guide token', async () => {
    const { u } = await mount({ guides: [{ value: 80, label: '80 %', dashed: true }, { value: 64, label: 'budget 64', dashed: false }] })
    u().fire('draw')
    const log = (u().ctx as unknown as RecordingCtx).log
    expect(log.filter(([n]) => n === 'moveTo').map(([, , y]) => y)).toEqual([u().valToPos(80, 'y', true), u().valToPos(64, 'y', true)])
    expect(log.filter(([n]) => n === 'setLineDash').map(([, d]) => d)).toEqual([[4, 4], []])
    expect(log.filter(([n]) => n === 'stroke').map(([, style]) => style)).toEqual(['#333333', '#333333'])
  })

  it('the y range covers the top guide', async () => {
    const { u } = await mount({ guides: [{ value: 80, label: '80 %', dashed: true }] })
    expect(opts(u()).scales.y.range(u(), 0, 5)).toEqual([0, 80])
    cleanup()
    const plain = await mount({ guides: [] })
    expect(opts(plain.u()).scales.y.range(plain.u(), 0, 0)).toEqual([0, 1])
  })

  it('markers sit in the over layer with their title and hide outside the scale', async () => {
    const { u } = await mount({ markers: [{ t: 1800, title: 'rev-1 · 10:00 UTC' }, { t: 3720, title: 'rev-2 · 11:00 UTC' }] })
    act(() => u().setScale('x', { min: 0, max: 3600 }))
    const els = [...u().over.querySelectorAll<HTMLElement>('[data-deploy]')]
    expect(els.map((e) => e.title)).toEqual(['rev-1 · 10:00 UTC', 'rev-2 · 11:00 UTC'])
    expect(els[1].style.left).toBe(`${u().valToPos(3600, 'x')}px`)
    expect(els.map((e) => e.style.display)).toEqual(['', ''])
    act(() => u().setScale('x', { min: 0, max: 900 }))
    expect(els.map((e) => e.style.display)).toEqual(['none', 'none'])
  })

  it('a marker before the window clamps to the left edge and hides once the scale starts after it', async () => {
    const { u } = await mount({ start: 1000, end: 4600, markers: [{ t: 400, title: 'old · 09:00 UTC' }, { t: 2000, title: 'mid · 10:00 UTC' }] })
    const els = [...u().over.querySelectorAll<HTMLElement>('[data-deploy]')]
    expect(els.map((e) => e.style.left)).toEqual([`${u().valToPos(1000, 'x')}px`, `${u().valToPos(2000, 'x')}px`])
    act(() => u().setScale('x', { min: 1500, max: 4600 }))
    expect(els.map((e) => e.style.display)).toEqual(['none', ''])
  })

  it('a resize re-places the markers', async () => {
    const { u } = await mount({ markers: [{ t: 1800, title: 'rev-1 · 10:00 UTC' }] })
    const el = u().over.querySelector<HTMLElement>('[data-deploy]')!
    const wide = parseFloat(el.style.left)
    expect(wide).toBe(u().valToPos(1800, 'x'))
    act(() => Observers.all[0].report(500))
    expect(parseFloat(el.style.left)).toBe(wide / 2)
  })

  it('a new deploy inside the same window is placed', async () => {
    const same = lines()
    const { view, u } = await mount({ lines: same, markers: [{ t: 1800, title: 'rev-1 · 10:00 UTC' }] })
    view.rerender(<TimeChart lines={same} {...base} markers={[{ t: 1800, title: 'rev-1 · 10:00 UTC' }, { t: 2700, title: 'rev-2 · 10:45 UTC' }]} />)
    const els = [...u().over.querySelectorAll<HTMLElement>('[data-deploy]')]
    expect(els.map((e) => e.title)).toEqual(['rev-1 · 10:00 UTC', 'rev-2 · 10:45 UTC'])
    expect(els.map((e) => e.style.left)).toEqual([`${u().valToPos(1800, 'x')}px`, `${u().valToPos(2700, 'x')}px`])
    expect(u().calls).toEqual([])
  })

  it('a deploy renamed inside the same window replaces the marker title', async () => {
    const same = lines()
    const { view, u } = await mount({ lines: same, markers: [{ t: 1800, title: 'rev-1 · 10:00 UTC' }] })
    view.rerender(<TimeChart lines={same} {...base} markers={[{ t: 1800, title: 'rev-9 · 10:00 UTC' }]} />)
    expect([...u().over.querySelectorAll<HTMLElement>('[data-deploy]')].map((e) => e.title)).toEqual(['rev-9 · 10:00 UTC'])
  })

  it('the chart reads time in UTC with dated ticks from 24 h', async () => {
    const { u } = await mount({ start: 0, end: 6 * 3600 })
    const o = opts(u())
    expect(o.tzDate(0)).toEqual(FakeUPlot.tzDate(new Date(0), 'Etc/UTC'))
    const zone = vi.spyOn(FakeUPlot, 'tzDate')
    o.tzDate(90)
    expect(zone).toHaveBeenCalledWith(new Date(90_000), 'Etc/UTC')
    const t = Date.UTC(2026, 9, 14, 23, 5) / 1000
    expect(o.axes[0].values(u(), [t])).toEqual(['23:05 UTC'])
    cleanup()
    const day = await mount({ start: 0, end: 86400 })
    expect(opts(day.u()).axes[0].values(day.u(), [t])).toEqual(['10-14 23:05'])
  })

  it('axes and grid take their colours and font from tokens', async () => {
    const { u } = await mount()
    for (const axis of opts(u()).axes) {
      expect(axis.stroke).toBe('#333333')
      expect(axis.grid.stroke).toBe('#444444')
      expect(axis.ticks.stroke).toBe('#444444')
      expect(axis.font).toBe('11px TestMono')
    }
  })

  it('the chart follows its container width', async () => {
    const { u } = await mount()
    act(() => Observers.all[0].report(512))
    expect(u().calls).toContainEqual(['setSize', { width: 512, height: CHART_H + 28 }])
  })

  it('the chart opens on the window and never zooms itself', async () => {
    const { u } = await mount({ start: 100, end: 4000 })
    const o = opts(u())
    expect([o.scales.x.min, o.scales.x.max]).toEqual([100, 4000])
    expect(o.cursor.drag).toEqual({ x: true, y: false, setScale: false, dist: 5 })
  })

  it('the observer ignores a zero or unchanged width', async () => {
    const { u } = await mount()
    act(() => Observers.all[0].report(0))
    act(() => Observers.all[0].report(1000))
    expect(u().calls.filter(([n]) => n === 'setSize')).toEqual([])
  })

  it('new data reuses the chart and keeps the range', async () => {
    const { view, u } = await mount()
    const next = lines({ a: [[0, 9], [60, 8]] })
    view.rerender(<TimeChart lines={next} {...base} />)
    expect(FakeUPlot.instances).toHaveLength(1)
    expect(u().calls.map(([n]) => n)).toEqual(['setData', 'setScale'])
    expect(u().calls[0]).toEqual(['setData', alignSeries(next, 0, 3600, 60), false])
    expect(u().calls[1]).toEqual(['setScale', 'x', { min: 0, max: 3600 }])
  })

  it('a different series list recreates the chart', async () => {
    const { view } = await mount()
    const first = FakeUPlot.instances[0]
    view.rerender(<TimeChart lines={lines().slice(0, 1)} {...base} />)
    await waitFor(() => expect(FakeUPlot.instances).toHaveLength(2))
    expect(first.destroyed).toBe(true)
    expect(FakeUPlot.instances[1].destroyed).toBe(false)
  })

  it('a renamed line in the same colour recreates the chart', async () => {
    const { view } = await mount()
    view.rerender(<TimeChart lines={lines().map((l, i) => (i === 0 ? { ...l, name: 'z' } : l))} {...base} />)
    await waitFor(() => expect(FakeUPlot.instances).toHaveLength(2))
    expect((FakeUPlot.instances[1].opts as any).series.slice(1).map((s: any) => s.label)).toEqual(['z', 'b'])
  })

  it('unmount destroys the chart; an early unmount creates none', async () => {
    const { view, u } = await mount()
    const first = u()
    view.unmount()
    expect(first.destroyed).toBe(true)
    expect(Observers.all[0].disconnected).toBe(true)
    FakeUPlot.instances.length = 0
    vi.mocked(window.getComputedStyle).mockClear()
    render(<TimeChart lines={lines()} {...base} />).unmount()
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0))
    })
    expect(FakeUPlot.instances).toHaveLength(0)
    expect(window.getComputedStyle, 'no setup runs after an early unmount').not.toHaveBeenCalled()
  })

  it('the setScale hook writes the window to data-x', async () => {
    const { view, u } = await mount()
    expect(root(view.container).getAttribute('data-x')).toBe('0,3600')
    act(() => u().setScale('x', { min: 100, max: 200 }))
    expect(root(view.container).getAttribute('data-x')).toBe('100,200')
  })

  it('every chart joins the health cursor sync on x', async () => {
    const { u } = await mount()
    const { y, sync } = opts(u()).cursor
    expect(y).toBe(false)
    expect(sync.key).toBe('health')
    expect(sync.scales).toEqual(['x', null])
    expect(sync.filters.pub('mousedown')).toBe(false)
    expect(sync.filters.pub('mouseup')).toBe(false)
    expect(sync.filters.pub('mousemove')).toBe(true)
  })

  it('the setCursor hook writes the time at the cursor', async () => {
    const { view, u } = await mount({ start: 0, end: 120 })
    u().cursor.idx = 1
    u().cursor.left = 40
    act(() => u().fire('setCursor'))
    expect(root(view.container).getAttribute('data-cursor')).toBe('60')
    u().cursor.left = -10
    act(() => u().fire('setCursor'))
    expect(root(view.container).getAttribute('data-cursor')).toBe('')
  })
})

const hover = (u: FakeUPlot, left = 40, top = 20) =>
  act(() => {
    u.over.dispatchEvent(new Event('mouseenter'))
    u.setCursor({ left, top })
  })
const rowNames = (c: HTMLElement) => [...c.querySelectorAll('[data-readout-row]')].map((e) => e.getAttribute('data-readout-row'))

describe('TimeChart lone points', () => {
  it('series draw an isolated sample as a dot in the series colour', async () => {
    const { u } = await mount()
    const [a, b] = opts(u()).series.slice(1)
    expect([a.points.stroke, a.points.fill, b.points.stroke, b.points.fill]).toEqual(['#111111', '#111111', '#222222', '#222222'])
    const self = (v: (number | null)[]) => ({ data: [v.map((_, i) => i), v] })
    expect(a.points.filter(self([null, 5, null, 6, 7, 8, null, 9]), 1, false)).toEqual([1, 7])
    expect(a.points.filter(self([5, null, 6, 7]), 1, false)).toEqual([0])
    expect(a.points.filter(self([5]), 1, false)).toEqual([0])
    expect(a.points.filter(self([1, 2, 3, null]), 1, false)).toBeNull()
    expect(a.points.filter(self([null, 5, null]), 1, true)).toBeNull()
  })
})

describe('TimeChart x axis spacing', () => {
  it('ticks sit at least a label width and a gap apart, short and long ranges', async () => {
    const t = Date.UTC(2026, 9, 14, 23, 5) / 1000
    for (const end of [6 * 3600, 7 * 86400]) {
      const { u } = await mount({ start: 0, end })
      const axis = opts(u()).axes[0]
      const widest = Math.max(...axis.values(u(), [t, 0]).map((l: string) => l.length)) * 0.6 * 11
      expect(axis.space(u(), 0, 0, end, 500)).toBeGreaterThanOrEqual(widest + 16)
      cleanup()
    }
  })
})

describe('TimeChart x axis spacing after a range change', () => {
  it('the same chart widens its tick spacing when the range grows past a day', async () => {
    const { view, u } = await mount({ start: 0, end: 6 * 3600 })
    const first = u()
    const short = opts(first).axes[0].space(first, 0, 0, 6 * 3600, 500)
    view.rerender(<TimeChart lines={lines()} start={0} end={7 * 86400} step={60} />)
    expect(FakeUPlot.instances).toHaveLength(1)
    expect(opts(first).axes[0].space(first, 0, 0, 7 * 86400, 500)).toBeGreaterThan(short)
  })
})

describe('TimeChart readout', () => {
  it('only the hovered chart shows the readout', async () => {
    const { container } = render(
      <>
        <TimeChart lines={lines()} {...base} />
        <TimeChart lines={lines()} {...base} />
      </>,
    )
    await waitFor(() => expect(FakeUPlot.instances).toHaveLength(2))
    const [one, two] = FakeUPlot.instances
    act(() => {
      one.over.dispatchEvent(new Event('mouseenter'))
      one.setCursor({ left: 40, top: 20 })
      two.setCursor({ left: 40, top: 20 })
    })
    const found = container.querySelectorAll('[data-readout]')
    expect(found).toHaveLength(1)
    expect(container.querySelectorAll('[data-chart]')[0].contains(found[0])).toBe(true)
  })

  it('the readout prints the time and the rows', async () => {
    const t0 = Date.UTC(2026, 9, 14, 10, 0) / 1000
    const at = t0 + 5 * 60
    const mk = (name: string, v: number, folded?: boolean) => ({ name, color: 'var(--series-1)', unit: 'count', points: [[at, v]] as [number, number][], folded })
    const set = [mk('a', 1), mk('g', 2, true), mk('b', 3)]
    const { view, u } = await mount({ lines: set, start: t0, end: t0 + 6 * 3600 })
    hover(u(), u().valToPos(at, 'x'))
    const c = view.container
    expect(c.querySelector('[data-readout-time]')!.textContent).toBe('10:05 UTC')
    expect(rowNames(c)).toEqual(['a', 'b', 'g'])
    expect([...c.querySelectorAll('[data-readout-row] .mono')].map((e) => e.textContent)).toEqual([1, 3, 2].map((v) => formatShown('count', v)))
    view.unmount()
    FakeUPlot.instances.length = 0
    const day = await mount({ lines: set, start: t0, end: t0 + 24 * 3600 })
    hover(day.u(), day.u().valToPos(at, 'x'))
    expect(day.view.container.querySelector('[data-readout-time]')!.textContent).toBe('10-14 10:05 UTC')
  })

  it('the readout ignores the pointer so the cursor can move under it', async () => {
    const { view, u } = await mount()
    hover(u())
    expect((view.container.querySelector('[data-readout]') as HTMLElement).style.pointerEvents).toBe('none')
  })

  it('the cursor follows the pointer over a child of the plot, not outside it', async () => {
    const { u } = await mount()
    const over = u().over
    const child = over.appendChild(document.createElement('div'))
    const outside = document.createElement('div')
    const handler = vi.fn()
    const move = opts(u()).cursor.bind.mousemove(u(), over, handler)
    move({ target: child })
    expect(handler).toHaveBeenCalledTimes(1)
    move({ target: outside })
    expect(handler).toHaveBeenCalledTimes(1)
  })

  it('a step with no point prints a dash', async () => {
    const { view, u } = await mount()
    hover(u(), u().valToPos(120, 'x'))
    expect([...view.container.querySelectorAll('[data-readout-row] .mono')].map((e) => e.textContent)).toEqual(['—', '—'])
  })

  it('the readout rows read the data uPlot holds at the cursor index', async () => {
    const { view, u } = await mount()
    hover(u(), u().valToPos(60, 'x'))
    u().data = [u().data[0], u().data[1].map((_, i) => 100 + i), u().data[2].map((_, i) => 200 + i)] as any
    act(() => u().setCursor({ left: u().valToPos(60, 'x'), top: 21 }))
    expect([...view.container.querySelectorAll('[data-readout-row] .mono')].map((e) => e.textContent)).toEqual([101, 201].map((v) => formatShown('count', v)))
  })

  it('mouseleave removes the readout', async () => {
    const { view, u } = await mount()
    hover(u())
    expect(view.container.querySelector('[data-readout]')).not.toBeNull()
    act(() => u().over.dispatchEvent(new Event('mouseleave')))
    expect(view.container.querySelector('[data-readout]')).toBeNull()
  })

  it('a refresh keeps a held crosshair and shows the new values', async () => {
    const mk = (v: number) => lines({ a: [[120, v]] })
    const { view, u } = await mount({ lines: mk(5) })
    hover(u())
    expect(view.container.querySelector('[data-readout-row=a] .mono')!.textContent).toBe(formatShown('count', 5))
    view.rerender(<TimeChart lines={mk(9)} {...base} />)
    expect(u().calls.map(([n]) => n)).toEqual(['setCursor', 'setData', 'setScale', 'setCursor'])
    expect(u().calls[1][2]).toBe(false)
    expect(u().calls[3]).toEqual(['setCursor', { left: 40, top: 20 }])
    expect(view.container.querySelector('[data-readout-row=a] .mono')!.textContent).toBe(formatShown('count', 9))
  })

  it('a refresh with no held cursor does not set one', async () => {
    const { view, u } = await mount()
    view.rerender(<TimeChart lines={lines({ a: [[0, 9]] })} {...base} />)
    expect(u().calls.map(([n]) => n)).toEqual(['setData', 'setScale'])
  })

  it('a refresh that changes the series keeps the crosshair and the readout', async () => {
    const { view, u } = await mount()
    const first = u()
    hover(first)
    const three = [...lines(), { name: 'c', color: 'var(--fg-4)', unit: 'count', points: [[0, 1]] as [number, number][], folded: true }]
    view.rerender(<TimeChart lines={three} {...base} />)
    await waitFor(() => expect(FakeUPlot.instances).toHaveLength(2))
    const second = FakeUPlot.instances[1]
    expect(first.destroyed).toBe(true)
    expect(second.calls[0]).toEqual(['setCursor', { left: 40, top: 20 }])
    expect(view.container.querySelector('[data-readout]')).not.toBeNull()
    expect(rowNames(view.container)).toEqual(['a', 'b', 'c'])
  })
})

describe('TimeChart readout edges', () => {
  const rects = (over: { left: number; top: number; width: number }, box: { width: number; height: number }) =>
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
      const r = this.hasAttribute('data-readout') ? { left: 0, top: 0, ...box } : { ...over, height: 100 }
      return { ...r, right: r.left + r.width, bottom: r.top + r.height, x: r.left, y: r.top, toJSON() {} } as DOMRect
    })

  it('the readout clamps to the left edge of the viewport', async () => {
    rects({ left: 0, top: 50, width: 100 }, { width: 200, height: 40 })
    const { view, u } = await mount()
    hover(u(), 95)
    expect(view.container.querySelector<HTMLElement>('[data-readout]')!.style.left).toBe('8px')
  })

  it('the readout clamps to the right edge of the viewport', async () => {
    rects({ left: 900, top: 50, width: 100 }, { width: 200, height: 40 })
    const { view, u } = await mount()
    hover(u(), 5)
    const b = view.container.querySelector<HTMLElement>('[data-readout]')!
    expect(b.style.left).toBe(`${window.innerWidth - 200 - 8}px`)
    expect(b.style.top).toBe('50px')
  })

  it('the readout clamps to the bottom edge of the viewport', async () => {
    rects({ left: 0, top: 700, width: 100 }, { width: 200, height: 200 })
    const { view, u } = await mount()
    hover(u(), 10)
    expect(view.container.querySelector<HTMLElement>('[data-readout]')!.style.top).toBe(`${window.innerHeight - 200 - 8}px`)
  })

  it('a hover on a panel with no lines shows the time and no rows', async () => {
    const { view, u } = await mount({ lines: [] })
    hover(u(), 10)
    expect(view.container.querySelector('[data-readout-time]')).not.toBeNull()
    expect(rowNames(view.container)).toEqual([])
  })

  it('a cursor past the last point reads the last step', async () => {
    const { view, u } = await mount({ start: 0, end: 120 })
    hover(u(), 99999)
    expect(root(view.container).getAttribute('data-cursor')).toBe('120')
    expect(view.container.querySelector('[data-readout]')).not.toBeNull()
  })

  it('a synced cursor on an unhovered chart gets a crosshair but no readout, also after a refresh', async () => {
    const { view, u } = await mount()
    act(() => u().setCursor({ left: 40, top: 20 }))
    expect(root(view.container).getAttribute('data-cursor')).not.toBe('')
    expect(view.container.querySelector('[data-readout]')).toBeNull()
    view.rerender(<TimeChart lines={lines({ a: [[0, 9]] })} {...base} />)
    expect(view.container.querySelector('[data-readout]')).toBeNull()
  })

  it('after the cursor leaves, a synced move does not bring the readout back', async () => {
    const { view, u } = await mount()
    hover(u())
    act(() => {
      u().over.dispatchEvent(new Event('mouseleave'))
      u().setCursor({ left: -10, top: -10 })
    })
    expect(view.container.querySelector('[data-readout]')).toBeNull()
    act(() => u().setCursor({ left: 40, top: 20 }))
    expect(view.container.querySelector('[data-readout]')).toBeNull()
  })

  it('moving the cursor off the plot while hovered removes the readout', async () => {
    const { view, u } = await mount()
    hover(u())
    act(() => u().setCursor({ left: -10, top: -10 }))
    expect(view.container.querySelector('[data-readout]')).toBeNull()
    expect(root(view.container).getAttribute('data-cursor')).toBe('')
  })

  it('a leave before a recreate carries no crosshair to the new chart', async () => {
    const { view, u } = await mount()
    const first = u()
    hover(first)
    act(() => {
      first.over.dispatchEvent(new Event('mouseleave'))
      first.setCursor({ left: -10, top: -10 })
    })
    const three = [...lines(), { name: 'c', color: 'var(--fg-4)', unit: 'count', points: [[0, 1]] as [number, number][], folded: true }]
    view.rerender(<TimeChart lines={three} {...base} />)
    await waitFor(() => expect(FakeUPlot.instances).toHaveLength(2))
    expect(FakeUPlot.instances[1].calls.some(([n]) => n === 'setCursor')).toBe(false)
    expect(view.container.querySelector('[data-readout]')).toBeNull()
  })

  it('after a recreate the new chart still follows the pointer', async () => {
    const { view, u } = await mount({ start: 0, end: 120 })
    hover(u(), 40)
    const three = [...lines(), { name: 'c', color: 'var(--fg-4)', unit: 'count', points: [[0, 1]] as [number, number][], folded: true }]
    view.rerender(<TimeChart lines={three} {...base} start={0} end={120} />)
    await waitFor(() => expect(FakeUPlot.instances).toHaveLength(2))
    const second = FakeUPlot.instances[1]
    act(() => second.setCursor({ left: second.valToPos(120, 'x'), top: 20 }))
    expect(view.container.querySelector('[data-readout-time]')!.textContent).toBe('00:02 UTC')
  })
})

describe('TimeChart zoom', () => {
  const zoomed = (u: FakeUPlot, left: number, width: number) => {
    u.select = { left, top: 0, width, height: 10 }
    act(() => u.fire('setSelect'))
  }

  it('a drag reports its window and clears the box', async () => {
    const onZoom = vi.fn()
    const { u } = await mount({ onZoom })
    zoomed(u(), 100, 200)
    expect(onZoom).toHaveBeenCalledTimes(1)
    expect(onZoom).toHaveBeenCalledWith([u().posToVal(100, 'x'), u().posToVal(300, 'x')])
    expect(u().calls.filter(([n]) => n === 'setSelect')).toEqual([['setSelect', expect.objectContaining({ width: 0, height: 0 }), false]])
    expect(u().calls.some(([n]) => n === 'setScale')).toBe(false)
  })

  it('a drag narrower than a step widens to two steps around its centre', async () => {
    const onZoom = vi.fn()
    const { u } = await mount({ onZoom, start: 0, end: 7 * 86400, step: 7200 })
    zoomed(u(), 500, 8)
    const [lo, hi] = onZoom.mock.calls[0][0]
    expect(hi - lo).toBeGreaterThanOrEqual(2 * 7200)
    expect((lo + hi) / 2).toBeCloseTo(u().posToVal(504, 'x'), 3)
    expect(u().data[0].filter((x) => x >= lo && x <= hi).length).toBeGreaterThanOrEqual(2)
    zoomed(u(), 0, 1)
    expect(onZoom.mock.calls[1][0]).toEqual([0, 14400])
    zoomed(u(), 1000, 1)
    expect(onZoom.mock.calls[2][0]).toEqual([7 * 86400 - 14400, 7 * 86400])
  })

  it('a drag in a range shorter than two steps reports the whole range', async () => {
    const onZoom = vi.fn()
    const { u } = await mount({ onZoom, start: 0, end: 100, step: 60 })
    zoomed(u(), 500, 8)
    expect(onZoom.mock.calls[0][0]).toEqual([0, 100])
  })

  it('a zero-width select reports nothing', async () => {
    const onZoom = vi.fn()
    const { u } = await mount({ onZoom })
    zoomed(u(), 100, 0)
    expect(onZoom).not.toHaveBeenCalled()
  })

  it('the zoom prop sets the window on the chart', async () => {
    const { view, u } = await mount()
    view.rerender(<TimeChart lines={lines()} {...base} zoom={[600, 1800]} />)
    expect(u().calls.filter(([n]) => n === 'setScale').at(-1)).toEqual(['setScale', 'x', { min: 600, max: 1800 }])
    view.rerender(<TimeChart lines={lines()} {...base} zoom={null} />)
    expect(u().calls[u().calls.length - 1]).toEqual(['setScale', 'x', { min: 0, max: 3600 }])
    expect(root(view.container).getAttribute('data-x')).toBe('0,3600')
  })

  it('new data keeps the zoom window after setData', async () => {
    const { view, u } = await mount({ zoom: [600, 1800] })
    view.rerender(<TimeChart lines={lines({ a: [[0, 9], [60, 8]] })} {...base} zoom={[600, 1800]} />)
    expect(u().calls.map(([n]) => n)).toEqual(['setData', 'setScale'])
    expect(u().calls[0][2]).toBe(false)
    expect(u().calls[1]).toEqual(['setScale', 'x', { min: 600, max: 1800 }])
  })

  it('a recreated chart starts in the zoom window', async () => {
    const { view } = await mount({ zoom: [600, 1800] })
    view.rerender(<TimeChart lines={lines().slice(0, 1)} {...base} zoom={[600, 1800]} />)
    await waitFor(() => expect(FakeUPlot.instances).toHaveLength(2))
    const o = opts(FakeUPlot.instances[1])
    expect([o.scales.x.min, o.scales.x.max]).toEqual([600, 1800])
    expect(root(view.container).getAttribute('data-x')).toBe('600,1800')
  })

  it('double-click is unbound', async () => {
    const { u } = await mount()
    expect(opts(u()).cursor.bind.dblclick(u(), u().over, () => {})).toBeNull()
  })
})

describe('TimeChart isolation', () => {
  const three = () => [...lines(), { name: 'c', color: 'var(--series-1)', unit: 'count', points: [[0, 5], [60, 6]] as [number, number][] }]

  it('isolation shows one series on the chart', async () => {
    const { view, u } = await mount({ lines: three() })
    const first = u()
    view.rerender(<TimeChart lines={three()} {...base} isolated="b" />)
    const sets = first.calls.filter(([n]) => n === 'setSeries')
    expect(sets).toEqual([['setSeries', 1, { show: false }], ['setSeries', 2, { show: true }], ['setSeries', 3, { show: false }]])
    expect(first.series.slice(1).map((s) => s.show)).toEqual([false, true, false])
    view.rerender(<TimeChart lines={three()} {...base} isolated={null} />)
    expect(first.series.slice(1).map((s) => s.show)).toEqual([true, true, true])
    cleanup()
    const fresh = await mount({ lines: three(), isolated: 'b' })
    expect(opts(fresh.u()).series.slice(1).map((s: any) => s.show)).toEqual([false, true, false])
  })

  it('a chart recreated while isolated starts with only that series shown', async () => {
    const { view } = await mount({ lines: three(), isolated: 'b' })
    view.rerender(<TimeChart lines={three().slice(1)} {...base} isolated="b" />)
    await waitFor(() => expect(FakeUPlot.instances).toHaveLength(2))
    expect(opts(FakeUPlot.instances[1]).series.slice(1).map((s: any) => s.show)).toEqual([true, false])
  })

  it('the readout lists only the isolated series', async () => {
    const { view, u } = await mount({ isolated: 'b' })
    hover(u())
    expect(rowNames(view.container)).toEqual(['b'])
  })
})
