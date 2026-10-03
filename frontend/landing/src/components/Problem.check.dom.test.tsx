// @vitest-environment jsdom
// Drives the invoice check card with a stub observer and fake timers. live() counts the 520ms intervals not yet cleared; vi.getTimerCount() would count React's timers too.
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from 'vitest'

import { GLYPHS } from '../icons'
import { Problem } from './Problem'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const STEP = 520
const V759 = [
  ['Missing or incomplete tax fields', 'FAIL'],
  ['Incorrect customer or supplier information', 'FAIL'],
  ['Duplicate invoice numbers', 'FAIL'],
  ['Weak approval workflows', 'WARN'],
  ['Poor audit trails', 'WARN'],
  ['Manual invoice corrections', 'WARN'],
  ['Disconnected accounting and ERP systems', 'WARN'],
  ['Lack of readiness for structured e-invoicing requirements', 'FAIL'],
] as const
const OUTCOME = {
  FAIL: { bg: 'color-mix(in srgb, var(--destructive) 14%, var(--card))', fg: 'var(--destructive)', glyph: GLYPHS.x },
  WARN: { bg: 'var(--status-progress-bg)', fg: 'var(--status-progress-fg)', glyph: GLYPHS['triangle-alert'] },
} as const
const DONE = '4 errors · 4 warnings · Not ready to submit'

type Entry = { isIntersecting: boolean; target: Element; intersectionRatio: number }
class StubIO {
  static all: StubIO[] = []
  targets: Element[] = []
  disconnects = 0
  constructor(
    public cb: (entries: Entry[], io: StubIO) => void,
    public options?: { rootMargin?: string },
  ) {
    StubIO.all.push(this)
  }
  observe(el: Element) {
    this.targets.push(el)
  }
  unobserve() {}
  disconnect() {
    this.disconnects++
  }
}

let container: HTMLDivElement
let root: Root
let mounted = false
let reduced = false
let setSpy: MockInstance<typeof setInterval>
let clearSpy: MockInstance<typeof clearInterval>
let errorSpy: MockInstance<typeof console.error>

beforeEach(() => {
  vi.useFakeTimers()
  setSpy = vi.spyOn(globalThis, 'setInterval')
  clearSpy = vi.spyOn(globalThis, 'clearInterval')
  errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
  StubIO.all = []
  reduced = false
  ;(globalThis as { IntersectionObserver?: unknown }).IntersectionObserver = StubIO
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    writable: true,
    value: (q: string) => ({ matches: reduced && q === '(prefers-reduced-motion: reduce)', media: q }),
  })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  if (mounted) act(() => root.unmount())
  mounted = false
  container.remove()
  const errors = errorSpy.mock.calls.length
  vi.restoreAllMocks()
  vi.useRealTimers()
  delete (globalThis as { IntersectionObserver?: unknown }).IntersectionObserver
  expect(errors, 'console.error stays silent').toBe(0)
})

const live = () => {
  const cleared = new Set<unknown>(clearSpy.mock.calls.map((c) => c[0]))
  return setSpy.mock.calls
    .map((c, i) => ({ delay: c[1], id: setSpy.mock.results[i].value as unknown }))
    .filter((x) => x.delay === STEP && !cleared.has(x.id))
    .map((x) => x.id)
}
const mount = () => {
  act(() => root.render(createElement(Problem)))
  mounted = true
}
const enterFresh = () => {
  mount()
  expectState(0)
  fire(true)
}
const fire = (isIntersecting: boolean) => {
  const io = StubIO.all.filter((o) => o.disconnects === 0).at(-1)
  expect(io, 'a live observer exists').toBeDefined()
  const target = io!.targets[0] ?? container
  act(() => io!.cb([{ isIntersecting, target, intersectionRatio: isIntersecting ? 1 : 0 }], io!))
}
const tick = (ms: number) => act(() => void vi.advanceTimersByTime(ms))
const steps = (k: number) => {
  for (let i = 0; i < k; i++) tick(STEP)
}
const runAgain = () => {
  const btn = container.querySelector<HTMLButtonElement>('#problem-check button.ds-btn--text')
  expect(btn, 'Run check again button').not.toBeNull()
  act(() => btn!.click())
}

const styleOf = (el: Element | null | undefined): Record<string, string> => {
  const out: Record<string, string> = {}
  for (const decl of (el?.getAttribute('style') ?? '').split(/;(?![^(]*\))/)) {
    const i = decl.indexOf(':')
    if (i > 0) out[decl.slice(0, i).trim()] = decl.slice(i + 1).trim()
  }
  return out
}
const norm = (s: string | null | undefined) => (s ?? '').replace(/\s+/g, ' ').trim()
const rowEls = () => [...container.querySelectorAll('#problem-check [data-check="row"]')]
const tagOf = (i: number) => norm(rowEls()[i].children[1].textContent)
const footerEl = () => container.querySelector('#problem-check [data-check="footer"]')

// Rows 0..k-1 show their V759 outcome, the rest read CHECKING; label opacity is 1 up to row k.
function expectState(k: number) {
  const rows = rowEls()
  expect(rows.length, 'eight rows').toBe(8)
  rows.forEach((r, i) => {
    const [label, outcome] = V759[i]
    const [lab, tag] = [r.children[0], r.children[1]]
    const ctx = `after ${k} steps, row ${i}`
    expect(norm(lab.textContent), `${ctx} label`).toBe(label)
    expect(styleOf(lab).opacity, `${ctx} opacity`).toBe(i <= k ? '1' : '0.55')
    const paths = [...tag.querySelectorAll('path')].map((p) => p.getAttribute('d'))
    if (i < k) {
      const o = OUTCOME[outcome]
      expect(norm(tag.textContent), `${ctx} tag`).toBe(outcome)
      expect(paths, `${ctx} glyph`).toEqual([...o.glyph])
      expect(styleOf(tag).background, `${ctx} bg`).toBe(o.bg)
      expect(styleOf(tag).color, `${ctx} fg`).toBe(o.fg)
    } else {
      expect(norm(tag.textContent), `${ctx} tag`).toBe('CHECKING')
      expect(paths, `${ctx} glyph`).toEqual([...GLYPHS['loader-circle']])
      expect(styleOf(tag).background, `${ctx} bg`).toBe('var(--muted)')
      expect(styleOf(tag).color, `${ctx} fg`).toBe('var(--muted-foreground)')
    }
  })
  const footer = footerEl()
  expect(footer, 'footer').not.toBeNull()
  if (k >= 8) {
    expect(norm(footer!.textContent)).toBe(DONE)
    expect(styleOf(footer).color).toBe('var(--destructive)')
  } else {
    expect(norm(footer!.textContent)).toBe(`Checking ${k + 1} of 8`)
    expect(styleOf(footer).color).toBe('var(--muted-foreground)')
  }
}

describe('PC-01 before entry the card is checking and nothing runs', () => {
  it('shows eight CHECKING rows, "Checking 1 of 8", no timer, and one observer on #problem-check', () => {
    mount()
    expectState(0)
    expect(live()).toEqual([])
    expect(setSpy, 'nothing polls').not.toHaveBeenCalled()
    expect(StubIO.all.length).toBe(1)
    expect(StubIO.all[0].options?.rootMargin).toBe('-20% 0px -30% 0px')
    expect(StubIO.all[0].targets).toEqual([container.querySelector('#problem-check')])
    expect(StubIO.all[0].targets[0]).toBeDefined()
  })
})

describe('PC-02 the run resolves one row per 520ms', () => {
  it('shows k outcomes after k steps and ends with no timer', () => {
    enterFresh()
    for (let k = 1; k <= 8; k++) {
      tick(STEP)
      expectState(k)
    }
    expect(live()).toEqual([])
  })
})

describe('PC-03 the first row resolves at 520ms, not 519', () => {
  it('keeps row 0 CHECKING at 519ms and resolves it at 520ms', () => {
    enterFresh()
    tick(STEP - 1)
    expectState(0)
    tick(1)
    expectState(1)
    expect(tagOf(0)).toBe('FAIL')
    expect(tagOf(1)).toBe('CHECKING')
  })
})

describe('PC-04 one run per entry', () => {
  it('ignores a second entry without an exit and keeps one interval', () => {
    enterFresh()
    steps(2)
    expectState(2)
    const first = live()
    expect(first.length).toBe(1)
    fire(true)
    expectState(2)
    expect(live()).toEqual(first)
    tick(STEP)
    expectState(3)
  })

  it('starts a new run on exit then re-entry after the run finished; an exit alone changes nothing', () => {
    enterFresh()
    steps(8)
    expectState(8)
    fire(false)
    expectState(8)
    expect(live()).toEqual([])
    fire(true)
    expectState(0)
    expect(live().length).toBe(1)
    steps(8)
    expectState(8)
    expect(live()).toEqual([])
  })
})

describe('PC-05 Run check again replays and restarts', () => {
  it('replays a finished run from CHECKING to the end text', () => {
    enterFresh()
    steps(8)
    expectState(8)
    runAgain()
    expectState(0)
    expect(live().length).toBe(1)
    steps(8)
    expectState(8)
    expect(live()).toEqual([])
  })

  it('restarts a run in progress with one interval, row 0 resolving 520ms after the click', () => {
    enterFresh()
    steps(3)
    expectState(3)
    const before = live()
    runAgain()
    expectState(0)
    const after = live()
    expect(after.length).toBe(1)
    expect(after[0]).not.toBe(before[0])
    tick(STEP - 1)
    expectState(0)
    tick(1)
    expectState(1)
  })
})

describe('PC-06 reduced motion shows the end state at once', () => {
  it('mounts in the end state with no observer and no timer, and replays to the end state', () => {
    reduced = true
    mount()
    expectState(8)
    expect(StubIO.all.length).toBe(0)
    expect(live()).toEqual([])
    runAgain()
    expectState(8)
    expect(live()).toEqual([])
    tick(STEP * 10)
    expectState(8)
    expect(setSpy).not.toHaveBeenCalled()
  })
})

describe('PC-07 reduced motion is read at each start', () => {
  it('shows the end state at once and starts no timer when the query starts matching before a replay', () => {
    enterFresh()
    steps(8)
    expectState(8)
    const started = setSpy.mock.calls.length
    reduced = true
    runAgain()
    expectState(8)
    expect(live()).toEqual([])
    expect(setSpy.mock.calls.length).toBe(started)
  })
})

describe('PC-08 no observer, no failure', () => {
  it('renders the end state without logging, and a replay still plays', () => {
    delete (globalThis as { IntersectionObserver?: unknown }).IntersectionObserver
    mount()
    expectState(8)
    expect(live()).toEqual([])
    runAgain()
    expectState(0)
    expect(live().length).toBe(1)
    steps(8)
    expectState(8)
    expect(live()).toEqual([])
  })
})

describe('PC-09 unmount mid-run cleans up', () => {
  it('clears the interval and disconnects the observer once', () => {
    enterFresh()
    steps(2)
    expectState(2)
    expect(live().length).toBe(1)
    act(() => root.unmount())
    mounted = false
    expect(live()).toEqual([])
    expect(StubIO.all.length).toBe(1)
    expect(StubIO.all[0].disconnects).toBe(1)
  })
})

describe('PC-10 the sequence matches V759 outcome by outcome', () => {
  it('records FAIL FAIL FAIL WARN WARN WARN WARN FAIL, each tag fixed once resolved', () => {
    enterFresh()
    const seen: string[] = []
    for (let k = 1; k <= 8; k++) {
      tick(STEP)
      seen.push(tagOf(k - 1))
      for (let i = 0; i < k; i++) expect(tagOf(i), `row ${i} after ${k} steps`).toBe(seen[i])
    }
    expect(seen).toEqual(['FAIL', 'FAIL', 'FAIL', 'WARN', 'WARN', 'WARN', 'WARN', 'FAIL'])
  })
})

describe('PC-11 an exit and re-entry mid-run restarts the run', () => {
  it('keeps the run through the exit, then restarts from CHECKING with a new interval', () => {
    enterFresh()
    steps(2)
    expectState(2)
    const first = live()
    expect(first.length).toBe(1)
    fire(false)
    expectState(2)
    expect(live()).toEqual(first)
    fire(true)
    expectState(0)
    const second = live()
    expect(second.length).toBe(1)
    expect(second[0]).not.toBe(first[0])
    tick(STEP - 1)
    expectState(0)
    tick(1)
    expectState(1)
  })
})
