// @vitest-environment jsdom
import { act, cleanup, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { alignSeries, CHART_H } from '../helpers'
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

  it('the chart opens on the window with no self-zoom', async () => {
    const { u } = await mount({ start: 100, end: 4000 })
    const o = opts(u())
    expect([o.scales.x.min, o.scales.x.max]).toEqual([100, 4000])
    expect(o.cursor).toEqual({ y: false, drag: { x: false, y: false, setScale: false } })
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
})
