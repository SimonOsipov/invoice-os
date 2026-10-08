// @vitest-environment jsdom
import { act, StrictMode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from './App'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// Node's own localStorage global shadows jsdom's on some versions; install a known store.
const store = new Map<string, string>()
Object.defineProperty(globalThis, 'localStorage', {
  configurable: true,
  value: {
    getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
    setItem: (k: string, v: string) => void store.set(k, String(v)),
    clear: () => store.clear(),
  },
})

let container: HTMLDivElement
let root: Root
let errorSpy: ReturnType<typeof vi.spyOn>

function mount(path: string) {
  window.history.replaceState(null, '', path)
  localStorage.setItem('asc_consent', '{"analytics":false,"ts":"","v":1}')
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => root.render(<StrictMode><App /></StrictMode>))
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
  errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  vi.unstubAllEnvs()
  localStorage.clear()
  vi.useRealTimers()
  const errors = errorSpy.mock.calls.length
  errorSpy.mockRestore()
  expect(errors).toBe(0)
})

const byText = (sel: string, text: string) =>
  [...container.querySelectorAll<HTMLElement>(sel)].find((e) => e.textContent?.trim() === text)!
const nav = (gid: string) => container.querySelector<HTMLElement>(`#nav-${gid}`)!
const main = () => container.querySelector<HTMLElement>('main#lib-main')!
const click = (el: Element) => act(() => el.dispatchEvent(new MouseEvent('click', { bubbles: true })))
const card = (name: string) => [...container.querySelectorAll<HTMLElement>('.lib-card')].find((e) => e.textContent!.includes(name))!
const stage = (label: string) => [...container.querySelectorAll<HTMLElement>('.lib-stage')].find((e) => e.textContent!.includes(label))!
const h2s = () => [...main().querySelectorAll('h2')].map((h) => h.textContent)
const pos = () => [...main().querySelectorAll('span.mono')].map((e) => e.textContent!).find((t) => /^\d\d \/ \d\d$/.test(t))
const clock = () => [...main().querySelectorAll('span.mono')].map((e) => e.textContent!).find((t) => /^\d:\d\d \/ \d:\d\d$/.test(t))
const advance = (ms: number) => act(() => vi.advanceTimersByTime(ms))
const playBtn = () => main().querySelector<HTMLElement>('.lib-play')!
const links = (text: string) => [...container.querySelectorAll('a')].filter((a) => a.textContent!.includes(text))

async function move(go: () => void) {
  const popped = new Promise<void>((r) => window.addEventListener('popstate', () => r(), { once: true }))
  await act(async () => {
    go()
    await popped
  })
}

describe('App', () => {
  it('root_isTheAscAppFrameWithTheSidebarAndTheScrollingMain', () => {
    mount('/')
    const app = container.firstElementChild as HTMLElement
    expect(app.className).toBe('asc-app')
    expect([app.style.height, app.style.display, app.style.overflow]).toEqual(['100vh', 'flex', 'hidden'])
    const aside = app.firstElementChild as HTMLElement
    expect(aside.tagName).toBe('ASIDE')
    expect(aside.style.width).toBe('288px')
    const m = main().style
    expect([m.flexGrow, m.minWidth, m.overflowY]).toEqual(['1', '0', 'auto'])
    expect(main().firstElementChild!.textContent).toContain('INVOICE JOURNEY')
  })

  it('boot_aPathOpensItsView', () => {
    mount('/rules')
    expect(nav('rules').style.fontWeight).toBe('700')
    expect(h2s()).toContain('Rules & validation')
    act(() => root.unmount())
    container.remove()

    mount('/clearance/submit-clear')
    expect(byText('aside button', 'Submit for FIRS clearance').style.color).toBe('var(--accent)')
    expect(stage('Clear').style.background).toBe('var(--sage-panel)')
    expect(stage('Validate').style.background).toBe('transparent')
    expect(main().querySelector('h2')!.textContent).toBe('Submit for FIRS clearance')
    act(() => root.unmount())
    container.remove()

    mount('/')
    expect(container.textContent).toContain('Feature library')
  })

  it('boot_anUnknownPathShowsTheHomeAndKeepsTheUrl', () => {
    for (const path of ['/nope', '/invoices/learns']) {
      window.history.replaceState(null, '', path)
      const len = window.history.length
      mount(path)
      expect(container.textContent).toContain('Feature library')
      expect(window.location.pathname).toBe(path)
      expect(window.history.length).toBe(len)
      act(() => root.unmount())
      container.remove()
    }
    mount('/')
  })

  it('click_pushesOnePathPerView', () => {
    mount('/')
    const len = window.history.length
    click(card('Rules & validation'))
    expect(window.location.pathname).toBe('/rules')
    click(container.querySelector('#fc-validate')!)
    expect(window.location.pathname).toBe('/rules/validate')
    click(byText('aside button', 'Overview'))
    expect(window.location.pathname).toBe('/')
    expect(window.history.length).toBe(len + 3)
    click(stage('Validate'))
    expect(window.location.pathname).toBe('/rules')
    expect(window.history.length).toBe(len + 4)
    click(nav('rules'))
    expect(window.history.length).toBe(len + 4)
    act(() => root.unmount())
    container.remove()

    mount('/invoices/')
    const l2 = window.history.length
    click(nav('invoices'))
    expect(window.history.length).toBe(l2)
    expect(window.location.pathname).toBe('/invoices')
    act(() => root.unmount())
    container.remove()

    mount('/nope')
    const l3 = window.history.length
    click(byText('aside button', 'Overview'))
    expect(window.history.length).toBe(l3)
    expect(window.location.pathname).toBe('/')
    act(() => root.unmount())
    container.remove()

    mount('/rules')
    const l4 = window.history.length
    click(byText('aside button', 'Plain validation messages'))
    expect(window.location.pathname).toBe('/rules/validate')
    expect(window.history.length).toBe(l4 + 1)
  })

  it('backAndForward_restoreEachView', async () => {
    mount('/')
    click(card('Rules & validation'))
    click(container.querySelector('#fc-validate')!)
    click(byText('aside button', 'Overview'))
    const len = window.history.length
    const validate = () => byText('aside button', 'Plain validation messages')

    await move(() => window.history.back())
    expect(validate().style.color).toBe('var(--accent)')
    await move(() => window.history.back())
    expect(h2s()).toContain('Rules & validation')
    await move(() => window.history.forward())
    expect(validate().style.color).toBe('var(--accent)')
    expect(window.history.length).toBe(len)
  })

  it('navigation_scrollsMainToTheTop', async () => {
    mount('/')
    main().scrollTop = 500
    click(nav('audit'))
    expect(main().scrollTop).toBe(0)
    main().scrollTop = 500
    await move(() => window.history.back())
    expect(main().scrollTop).toBe(0)
    const len = window.history.length
    main().scrollTop = 500
    click(byText('aside button', 'Overview'))
    expect(main().scrollTop).toBe(0)
    expect(window.history.length).toBe(len)
  })

  it('links_followTheEnvironment', () => {
    vi.stubEnv('VITE_APP_URL', 'https://a.example')
    vi.stubEnv('VITE_LANDING_URL', 'https://l.example')
    mount('/')
    const demo = links('Book the Demo')
    expect(demo.map((a) => a.getAttribute('href'))).toEqual(['https://l.example/?demo', 'https://l.example/?demo'])
    click(nav('rules'))
    expect(links('Open in Platform').map((a) => a.getAttribute('href'))).toEqual(['https://a.example/rules?via=library'])
    for (const gid of ['notifications', 'settings']) {
      click(nav(gid))
      expect(links('Open in Platform')).toHaveLength(0)
    }
    act(() => root.unmount())
    container.remove()

    vi.stubEnv('VITE_APP_URL', '')
    vi.stubEnv('VITE_LANDING_URL', '')
    mount('/')
    expect(links('Book the Demo')).toHaveLength(0)
    click(nav('rules'))
    expect(links('Book the Demo')).toHaveLength(0)
    expect(links('Open in Platform')).toHaveLength(0)
  })

  it('featurePath_showsOpenInPlatformOnlyForShippedFeatures', () => {
    vi.stubEnv('VITE_APP_URL', 'https://a.example')
    mount('/rules/validate')
    expect(links('Open in Platform').map((a) => a.getAttribute('href'))).toEqual(['https://a.example/invoices?via=library'])
    act(() => root.unmount())
    container.remove()

    mount('/notifications/alerts')
    expect(links('Open in Platform')).toHaveLength(0)
    expect(main().textContent).toContain('Coming soon')
    advance(3400)
    expect(pos()).toBe('02 / 03')
  })

  it('feature_autoplaysOneStepEvery3_4Seconds', () => {
    mount('/invoices/import-files')
    expect([pos(), clock()]).toEqual(['01 / 04', '0:00 / 0:13'])
    advance(3300)
    expect(pos()).toBe('01 / 04')
    advance(100)
    expect([pos(), clock()]).toEqual(['02 / 04', '0:03 / 0:13'])
    expect(main().textContent).toContain('Columns are matched to invoice fields')
    advance(3400)
    expect(pos()).toBe('03 / 04')
  })

  it('feature_stopsOnTheLastStepAndReplays', () => {
    mount('/invoices/import-files')
    advance(13600)
    expect([pos(), clock()]).toEqual(['04 / 04', '0:13 / 0:13'])
    expect(playBtn().innerHTML).toContain('M21 3v5h-5')
    expect(vi.getTimerCount()).toBe(0)
    advance(5000)
    expect([pos(), clock()]).toEqual(['04 / 04', '0:13 / 0:13'])
    click(playBtn())
    expect([pos(), clock()]).toEqual(['01 / 04', '0:00 / 0:13'])
    expect(vi.getTimerCount()).toBe(1)
  })

  it('feature_pausesAndResumes', () => {
    mount('/invoices/import-files')
    advance(3400)
    expect(pos()).toBe('02 / 04')
    click(playBtn())
    expect(vi.getTimerCount()).toBe(0)
    advance(10000)
    expect(pos()).toBe('02 / 04')
    click(playBtn())
    advance(3400)
    expect(pos()).toBe('03 / 04')
  })

  it('feature_scrubMovesToTheClickedTime', () => {
    mount('/invoices/import-files')
    const scrub = container.querySelector<HTMLElement>('#lib-scrub')!
    scrub.getBoundingClientRect = () => ({ left: 0, width: 400 }) as DOMRect
    act(() => scrub.dispatchEvent(new MouseEvent('click', { bubbles: true, clientX: 300 })))
    expect([pos(), clock()]).toEqual(['04 / 04', '0:10 / 0:13'])
    expect(vi.getTimerCount()).toBe(1)
    advance(3400)
    expect([pos(), clock()]).toEqual(['04 / 04', '0:13 / 0:13'])
    expect(vi.getTimerCount()).toBe(0)
  })

  it('feature_stepListJumpsAndPlays', () => {
    mount('/invoices/import-files')
    click(playBtn())
    const third = [...main().querySelectorAll('button')].find((b) => b.textContent === '03One invoice is created per row')!
    click(third)
    expect([pos(), clock()]).toEqual(['03 / 04', '0:06 / 0:13'])
    advance(3400)
    expect(pos()).toBe('04 / 04')
  })

  it('feature_leavingStopsTheTimerAndReturningStartsAtStepOne', async () => {
    mount('/invoices/import-files')
    advance(6800)
    expect(pos()).toBe('03 / 04')
    click(nav('invoices'))
    expect(vi.getTimerCount()).toBe(0)
    expect(window.location.pathname).toBe('/invoices')
    click(container.querySelector('#fc-import-files')!)
    expect([pos(), clock()]).toEqual(['01 / 04', '0:00 / 0:13'])
    expect(vi.getTimerCount()).toBe(1)
    advance(6800)
    await move(() => window.history.back())
    expect(vi.getTimerCount()).toBe(0)
    await move(() => window.history.forward())
    expect(pos()).toBe('01 / 04')
    expect(vi.getTimerCount()).toBe(1)
  })

  it('feature_relatedOpensThatFeatureAtStepOne', () => {
    mount('/invoices/import-files')
    advance(3400)
    click(card('Read PDFs and scans'))
    expect(window.location.pathname).toBe('/recognition/read-documents')
    expect(h2s()).toEqual(['Read PDFs and scans'])
    expect(pos()).toBe('01 / 03')
    expect(vi.getTimerCount()).toBe(1)
    act(() => root.unmount())
    container.remove()

    mount('/rules/validate')
    click(card('A rule set for every invoice'))
    expect(window.location.pathname).toBe('/rules/rule-library')
    expect(pos()).toBe('01 / 03')
    expect(main().textContent).toContain('Coming soon')
  })

  it('feature_clickingTheCurrentFeatureRestartsItsDemo', () => {
    mount('/invoices/import-files')
    advance(13600)
    expect(pos()).toBe('04 / 04')
    expect(vi.getTimerCount()).toBe(0)
    const len = window.history.length
    click(byText('aside button', 'Import from CSV or Excel'))
    expect([pos(), clock()]).toEqual(['01 / 04', '0:00 / 0:13'])
    expect(vi.getTimerCount()).toBe(1)
    expect(window.history.length).toBe(len)
    advance(3400)
    expect(pos()).toBe('02 / 04')
    click(playBtn())
    click(byText('aside button', 'Import from CSV or Excel'))
    expect(pos()).toBe('01 / 04')
    expect(vi.getTimerCount()).toBe(1)
  })

  it('feature_backButtonOpensTheGroup', () => {
    mount('/rules/validate')
    click(main().querySelector('.lib-back')!)
    expect(window.location.pathname).toBe('/rules')
    expect(h2s()).toEqual(['Rules & validation'])
    expect(vi.getTimerCount()).toBe(0)
  })

  it('feature_unmountingMidPlayClearsTheInterval', () => {
    mount('/invoices/import-files')
    advance(3400)
    expect(vi.getTimerCount()).toBe(1)
    act(() => root.unmount())
    container.remove()
    expect(vi.getTimerCount()).toBe(0)
    advance(5000)
    mount('/')
  })

  it('feature_relatedToComingSoonHidesOpenInPlatformAndShowsThePill', () => {
    vi.stubEnv('VITE_APP_URL', 'https://a.example')
    mount('/rules/validate')
    expect(links('Open in Platform')).toHaveLength(1)
    expect(main().textContent).not.toContain('Coming soon')
    click(card('A rule set for every invoice'))
    expect(window.location.pathname).toBe('/rules/rule-library')
    expect(links('Open in Platform')).toHaveLength(0)
    expect(main().textContent).toContain('Coming soon')
    click(card('Plain validation messages'))
    expect(links('Open in Platform').map((a) => a.getAttribute('href'))).toEqual(['https://a.example/invoices?via=library'])
    expect(main().textContent).not.toContain('Coming soon')
  })

  it('feature_stepListJumpsFromTheEndedStateAndPlays', () => {
    mount('/invoices/import-files')
    advance(13600)
    expect(vi.getTimerCount()).toBe(0)
    const second = [...main().querySelectorAll('button')].find((b) => b.textContent === '02Columns are matched to invoice fields')!
    click(second)
    expect([pos(), clock()]).toEqual(['02 / 04', '0:03 / 0:13'])
    expect(vi.getTimerCount()).toBe(1)
    expect(playBtn().innerHTML).not.toContain('M21 3v5h-5')
    advance(3400)
    expect(pos()).toBe('03 / 04')
  })

  it('feature_backToAFeatureRestartsItWithExactlyOneTimer', async () => {
    mount('/invoices/import-files')
    advance(6800)
    click(card('Read PDFs and scans'))
    advance(3400)
    expect(pos()).toBe('02 / 03')
    await move(() => window.history.back())
    expect(window.location.pathname).toBe('/invoices/import-files')
    expect([pos(), clock()]).toEqual(['01 / 04', '0:00 / 0:13'])
    expect(vi.getTimerCount()).toBe(1)
    advance(3400)
    expect(pos()).toBe('02 / 04')
  })

  it('feature_repeatedRestartsLeaveOneTimerAndNoHistory', () => {
    mount('/invoices/import-files')
    const len = window.history.length
    for (let i = 0; i < 3; i++) {
      advance(3400)
      click(byText('aside button', 'Import from CSV or Excel'))
    }
    expect(pos()).toBe('01 / 04')
    expect(vi.getTimerCount()).toBe(1)
    expect(window.history.length).toBe(len)
  })
})
