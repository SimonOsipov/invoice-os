// @vitest-environment jsdom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from './App'
import type { Block, BlogData } from './data'
import { TEST_DATA } from './testData'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let container: HTMLDivElement
let root: Root

const dataWith = (body: Block[]): BlogData => ({
  ...TEST_DATA,
  posts: TEST_DATA.posts.map((p) => (p.id === 'csv-import' ? { ...p, body } : p)),
})
const IMPORT: Block = { type: 'demo', demo: 'import' }

async function mount(body: Block[] = [IMPORT]) {
  window.history.replaceState(null, '', '/csv-import')
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => root.render(<App load={() => Promise.resolve(dataWith(body))} />))
}
const play = (i = 0) => container.querySelectorAll<HTMLButtonElement>('button[aria-label="Play or pause"]')[i]
const text = () => container.textContent ?? ''
const advance = (ms: number) => act(async () => void vi.advanceTimersByTime(ms))
const click = (el: Element, init: MouseEventInit = {}) =>
  act(async () => void el.dispatchEvent(new MouseEvent('click', { bubbles: true, ...init })))

beforeEach(() => vi.useFakeTimers())
afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.useRealTimers()
})

describe('demo block', () => {
  it('DE-01 the demo block renders the frame paused at step 1', async () => {
    await mount()
    for (const t of ['FEATURE LIBRARY · DEMO', 'Import from CSV or Excel', 'Import · sahara-foods-june.csv', '01 / 04', 'Upload the export from your accounting system', '0:00 / 0:13'])
      expect(text()).toContain(t)
    expect(play().querySelector('svg')).not.toBeNull()
    expect(play().textContent).toBe('')
    expect(play().querySelector('svg')?.innerHTML).not.toContain('M21 12a9 9 0 1 1-9-9')
    const ticks = [...container.querySelectorAll<HTMLElement>('[data-testid="blog-demo-scrub"] span')].map((s) => s.style.left)
    expect(ticks).toEqual(['25%', '50%', '75%'])
  })

  it('DE-02 play advances a step per 3.4 s', async () => {
    await mount()
    await click(play())
    expect(play().querySelectorAll('span > span').length).toBe(2)
    await advance(3500)
    expect(text()).toContain('02 / 04')
    await advance(3500)
    expect(text()).toContain('03 / 04')
  })

  it('DE-03 the end shows replay and replays from zero', async () => {
    await mount()
    await click(play())
    await advance(14000)
    expect(text()).toContain('04 / 04')
    expect(text()).toContain('0:13 / 0:13')
    expect(play().querySelector('svg')?.innerHTML).toContain('M21 12a9 9 0 1 1-9-9')
    await click(play())
    expect(text()).toContain('01 / 04')
    expect(play().querySelectorAll('span > span').length).toBe(2)
  })

  it('DE-04 a scrub click seeks', async () => {
    await mount()
    const scrub = container.querySelector<HTMLElement>('[data-testid="blog-demo-scrub"]')!
    scrub.getBoundingClientRect = () => ({ left: 0, width: 400, right: 400, top: 0, bottom: 22, height: 22, x: 0, y: 0, toJSON: () => ({}) })
    await click(scrub, { clientX: 250 })
    expect(text()).toContain('03 / 04')
    await advance(3500)
    expect(text()).toContain('03 / 04')
  })

  it('DE-05 each demo block keeps its own clock', async () => {
    await mount([IMPORT, IMPORT])
    await click(play(0))
    await advance(3500)
    const caps = [...container.querySelectorAll('.mono')].map((e) => e.textContent).filter((t) => /^\d\d \/ \d\d$/.test(t ?? ''))
    expect(caps).toEqual(['02 / 04', '01 / 04'])
  })

  it('DE-06 the flow demo shows its status', async () => {
    await mount([{ type: 'demo', demo: 'clearance' }])
    expect(text()).toContain('STATUS')
    expect(text()).toContain('Invoice converted and signed')
    const scene = container.querySelector('[data-testid="blog-demo-scene"]')?.innerHTML
    expect(scene).toContain('width: 34px')
    expect(scene).not.toContain('font-size: 11.5px')
    for (const l of ['Approved', 'Prepared', 'Submitted', 'Response', 'Cleared']) expect(text()).toContain(l)
  })
})
