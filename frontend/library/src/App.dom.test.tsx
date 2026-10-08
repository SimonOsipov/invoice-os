// @vitest-environment jsdom
import { act, StrictMode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from './App'
import { GROUPS, TOUR } from './content'
import { CALLOUT_H, CALLOUT_W } from './tour'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let container: HTMLDivElement
let root: Root
let errorSpy: ReturnType<typeof vi.spyOn>

function mount(path: string) {
  window.history.replaceState(null, '', path)
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => root.render(<StrictMode><App /></StrictMode>))
}

type Box = { left: number; top: number; width: number; height: number }
const rects = new Map<string, Box>()
const realRect = Element.prototype.getBoundingClientRect

beforeEach(() => {
  rects.clear()
  Element.prototype.getBoundingClientRect = function (this: Element) {
    const b = rects.get(this.id) ?? { left: 0, top: 0, width: 0, height: 0 }
    return { ...b, x: b.left, y: b.top, right: b.left + b.width, bottom: b.top + b.height, toJSON: () => b } as DOMRect
  }
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
  errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
})

afterEach(() => {
  Element.prototype.getBoundingClientRect = realRect
  act(() => root.unmount())
  container.remove()
  vi.unstubAllEnvs()
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

describe('App tour', () => {
  const sidebarBtn = () => container.querySelector<HTMLElement>('button.lib-tour')!
  const sidebarLabel = () => sidebarBtn().textContent
  const hero = () => [...main().querySelectorAll<HTMLElement>('button')].find((b) => b.textContent === 'Take the tour')
  const overlay = () => container.querySelector<HTMLElement>('.asc-app > div:last-child:not(aside):not(main)')
  const step = () => overlay()?.querySelector('.t-step')?.textContent
  const btn = (label: string) => [...container.querySelectorAll<HTMLElement>('button')].find((b) => b.textContent === label)!
  const spotlight = () => [...container.querySelectorAll<HTMLElement>('.asc-app > div div')].find((d) => d.style.boxShadow.includes('9999px'))!
  const callout = () => [...container.querySelectorAll<HTMLElement>('.asc-app > div div')].find((d) => d.style.width === `${CALLOUT_W}px`)!
  const box = (el: HTMLElement) => [el.style.left, el.style.top, el.style.width, el.style.height].map(parseFloat)
  const encloses = (outer: HTMLElement, r: Box) => {
    const [x, y, w, h] = box(outer)
    return x <= r.left && y <= r.top && x + w >= r.left + r.width && y + h >= r.top + r.height
  }
  const stepperMarked = () => [...container.querySelectorAll<HTMLElement>('.lib-stage[aria-current]')].map((e) => e.textContent!.slice(2))
  const allRects = () => {
    GROUPS.forEach((g, n) => rects.set(`nav-${g.id}`, { left: 10, top: 100 + n * 40, width: 267, height: 36 }))
    TOUR.forEach((t) => rects.set(`fc-${t.f}`, { left: 330, top: 130, width: 340, height: 300 }))
  }
  const NAV_INVOICES = { left: 10, top: 100, width: 267, height: 36 }
  const CARD_IMPORT = { left: 330, top: 130, width: 340, height: 300 }

  it('tour_startsFromTheSidebarAndSpotlightsTheFirstGroup', () => {
    rects.set('nav-invoices', NAV_INVOICES)
    mount('/')
    click(sidebarBtn())
    expect(sidebarLabel()).toBe('Tour 1 of 14 · exit')
    expect(box(spotlight())).toEqual([6, 97, 275, 42])
    expect(encloses(spotlight(), NAV_INVOICES)).toBe(true)
    expect(step()).toBe('STEP 01 OF 14')
    expect(container.textContent).toContain('Open Invoices')
    expect(container.textContent).toContain('Import, create and track every invoice.')
    expect(btn('Back').hasAttribute('disabled')).toBe(true)
    expect(btn('Watch demo')).toBeUndefined()
    expect(window.location.pathname).toBe('/')
  })

  it('tour_heroButtonStartsTheSameTour', () => {
    mount('/')
    expect(overlay()).toBeNull()
    click(hero()!)
    expect(step()).toBe('STEP 01 OF 14')
    expect(sidebarLabel()).toBe('Tour 1 of 14 · exit')
    expect(hero()!.textContent).toBe('Take the tour')
  })

  it('tour_nextOpensTheGroupPageAndSpotlightsTheCard', () => {
    rects.set('nav-invoices', NAV_INVOICES)
    rects.set('fc-import-files', CARD_IMPORT)
    mount('/')
    click(sidebarBtn())
    const push = vi.spyOn(window.history, 'pushState')
    click(btn('Next'))
    expect(window.location.pathname).toBe('/invoices')
    expect(push).toHaveBeenCalledTimes(1)
    push.mockRestore()
    expect(step()).toBe('STEP 02 OF 14')
    expect(container.textContent).toContain('Start with the data you already have')
    expect(btn('Watch demo')).toBeDefined()
    expect(sidebarLabel()).toBe('Tour 2 of 14 · exit')
    expect(main().scrollTop).toBe(74)
    expect(box(spotlight())).toEqual([322, 122, 356, 316])
    expect(encloses(spotlight(), CARD_IMPORT)).toBe(true)
  })

  it('tour_nextAfterACardSpotlightsTheNextGroupWithoutNavigating', () => {
    allRects()
    mount('/')
    click(sidebarBtn())
    click(btn('Next'))
    click(btn('Next'))
    expect(step()).toBe('STEP 03 OF 14')
    expect(container.textContent).toContain('Open Document recognition')
    expect(window.location.pathname).toBe('/invoices')
    expect(box(spotlight())).toEqual([6, 97 + 40, 275, 42])
    click(btn('Next'))
    expect(window.location.pathname).toBe('/recognition')
    expect(container.textContent).toContain('Read PDFs and scans')
  })

  it('tour_walksAllFourteenStepsAndFinishEndsIt', () => {
    allRects()
    mount('/')
    click(sidebarBtn())
    const labels = [sidebarLabel()]
    const paths = [window.location.pathname]
    for (let n = 0; n < 13; n++) {
      click(btn('Next'))
      labels.push(sidebarLabel())
      paths.push(window.location.pathname)
    }
    expect(labels).toEqual(Array.from({ length: 14 }, (_, n) => `Tour ${n + 1} of 14 · exit`))
    expect(paths).toEqual(['/', '/invoices', '/invoices', '/recognition', '/recognition', '/rules', '/rules', '/approvals', '/approvals', '/clearance', '/clearance', '/audit', '/audit', '/reports'])
    expect(step()).toBe('STEP 14 OF 14')
    expect(container.textContent).toContain('See readiness at a glance')
    click(btn('Finish'))
    expect(overlay()).toBeNull()
    expect(sidebarLabel()).toBe('Take the tour')
    expect(window.location.pathname).toBe('/reports')
  })

  it('tour_backStepsBackThroughTheStops', () => {
    allRects()
    mount('/')
    click(sidebarBtn())
    click(btn('Next'))
    click(btn('Next'))
    expect(step()).toBe('STEP 03 OF 14')
    click(btn('Back'))
    expect(step()).toBe('STEP 02 OF 14')
    expect(window.location.pathname).toBe('/invoices')
    click(btn('Back'))
    expect(step()).toBe('STEP 01 OF 14')
    expect(btn('Back').hasAttribute('disabled')).toBe(true)
    click(btn('Next'))
    click(btn('Next'))
    click(btn('Next'))
    expect(step()).toBe('STEP 04 OF 14')
    const path = window.location.pathname
    click(btn('Back'))
    expect(step()).toBe('STEP 03 OF 14')
    expect(container.textContent).toContain('Open Document recognition')
    expect(window.location.pathname).toBe(path)
  })

  it('tour_closeAndExitEndTheTourAtAnyStep', () => {
    allRects()
    mount('/')
    click(sidebarBtn())
    for (let n = 0; n < 4; n++) click(btn('Next'))
    expect(step()).toBe('STEP 05 OF 14')
    let path = window.location.pathname
    click(container.querySelector('[aria-label="Close tour"]')!)
    expect(overlay()).toBeNull()
    expect(sidebarLabel()).toBe('Take the tour')
    expect(window.location.pathname).toBe(path)
    click(sidebarBtn())
    for (let n = 0; n < 5; n++) click(btn('Next'))
    expect(step()).toBe('STEP 06 OF 14')
    expect(sidebarLabel()).toBe('Tour 6 of 14 · exit')
    path = window.location.pathname
    click(sidebarBtn())
    expect(overlay()).toBeNull()
    expect(window.location.pathname).toBe(path)
  })

  it('tour_watchDemoOpensTheStopsFeatureAndEndsTheTour', () => {
    allRects()
    mount('/')
    click(sidebarBtn())
    click(btn('Next'))
    click(btn('Watch demo'))
    expect(window.location.pathname).toBe('/invoices/import-files')
    expect(h2s()).toContain('Import from CSV or Excel')
    expect(pos()).toBe('01 / 04')
    expect(overlay()).toBeNull()
  })

  it('tour_stepperHighlightsTheStopStage', () => {
    allRects()
    mount('/')
    click(sidebarBtn())
    expect(stepperMarked()).toEqual(['Import'])
    click(btn('Next'))
    click(btn('Next'))
    click(btn('Next'))
    expect(step()).toBe('STEP 04 OF 14')
    click(btn('Back'))
    expect(step()).toBe('STEP 03 OF 14')
    expect(window.location.pathname).toBe('/recognition')
    expect(stepperMarked()).toEqual(['Extract'])
    for (let n = 0; n < 11; n++) click(btn('Next'))
    expect(step()).toBe('STEP 14 OF 14')
    expect(stepperMarked()).toEqual([])
    click(btn('Back'))
    expect(step()).toBe('STEP 13 OF 14')
    expect(stepperMarked()).toEqual([])
  })

  it('tour_stepperIgnoresThePageAndTheRouteRuleReturnsAfterClose', () => {
    allRects()
    mount('/audit')
    expect(stepperMarked()).toEqual(['Archive'])
    click(sidebarBtn())
    expect(stepperMarked()).toEqual(['Import'])
    click(container.querySelector('[aria-label="Close tour"]')!)
    expect(stepperMarked()).toEqual(['Archive'])
  })

  it('tour_resizeAndScrollRemeasureTheSpotlight', () => {
    rects.set('nav-invoices', NAV_INVOICES)
    rects.set('fc-import-files', CARD_IMPORT)
    mount('/')
    click(sidebarBtn())
    rects.set('nav-invoices', { ...NAV_INVOICES, top: 300 })
    act(() => window.dispatchEvent(new Event('resize')))
    expect(box(spotlight())[1]).toBe(297)
    rects.set('nav-invoices', { ...NAV_INVOICES, top: 500 })
    act(() => container.querySelector('aside nav')!.dispatchEvent(new Event('scroll')))
    expect(box(spotlight())[1]).toBe(497)
    click(btn('Next'))
    rects.set('fc-import-files', { ...CARD_IMPORT, top: 140 })
    const before = main().scrollTop
    act(() => main().dispatchEvent(new Event('scroll')))
    expect(box(spotlight())[1]).toBe(132)
    expect(main().scrollTop).toBe(before)
    const w = window.innerWidth
    try {
      Object.defineProperty(window, 'innerWidth', { configurable: true, value: 400 })
      act(() => window.dispatchEvent(new Event('resize')))
      expect(callout().style.left).toBe('24px')
    } finally {
      Object.defineProperty(window, 'innerWidth', { configurable: true, value: w })
    }
  })

  it('tour_calloutStaysInsideTheWindowAndOffTheSpotlight', () => {
    rects.set('nav-invoices', NAV_INVOICES)
    rects.set('fc-import-files', CARD_IMPORT)
    mount('/')
    click(sidebarBtn())
    const check = () => {
      const c = callout().style
      const [cx, cy] = [parseFloat(c.left), parseFloat(c.top)]
      const [sx, sy, sw, sh] = box(spotlight())
      expect(cx >= 0 && cy >= 0 && cx + CALLOUT_W <= window.innerWidth && cy + CALLOUT_H <= window.innerHeight).toBe(true)
      const apart = cx + CALLOUT_W <= sx || sx + sw <= cx || cy + CALLOUT_H <= sy || sy + sh <= cy
      expect(apart).toBe(true)
      return cy
    }
    check()
    click(btn('Next'))
    const top = check()
    expect(top).toBe(122 + 316 + 16)
    expect(top + CALLOUT_H).toBeLessThan(window.innerHeight)
  })

  it('tour_startedOnAFeaturePageLeavesNoPlayerRunning', () => {
    allRects()
    mount('/invoices/import-files')
    expect(vi.getTimerCount()).toBe(1)
    click(sidebarBtn())
    expect(step()).toBe('STEP 01 OF 14')
    expect(h2s()).toContain('Import from CSV or Excel')
    expect(vi.getTimerCount()).toBe(1)
    click(btn('Next'))
    expect(window.location.pathname).toBe('/invoices')
    expect(h2s()).not.toContain('Import from CSV or Excel')
    expect(step()).toBe('STEP 02 OF 14')
    expect(vi.getTimerCount()).toBe(0)
    act(() => root.unmount())
    container.remove()
    mount('/')
    click(sidebarBtn())
    expect(vi.getTimerCount()).toBe(0)
    click(btn('Next'))
    expect(vi.getTimerCount()).toBe(0)
  })

  it('tour_otherNavigationEndsTheTour', async () => {
    allRects()
    mount('/')
    click(sidebarBtn())
    click(btn('Next'))
    click(container.querySelector('#nav-rules')!)
    expect(overlay()).toBeNull()
    expect(window.location.pathname).toBe('/rules')
    click(sidebarBtn())
    expect(step()).toBe('STEP 01 OF 14')
    await move(() => window.history.back())
    expect(overlay()).toBeNull()
  })

  it('tour_stepperLogoAndForwardEndTheTourToo', async () => {
    allRects()
    mount('/')
    click(sidebarBtn())
    click(btn('Next'))
    const stages = [...container.querySelectorAll<HTMLElement>('button.lib-stage')]
    expect(stages.length).toBeGreaterThan(0)
    click(stages[2])
    expect(overlay()).toBeNull()
    expect(window.location.pathname).toBe('/rules')
    click(sidebarBtn())
    click(btn('Next'))
    expect(step()).toBe('STEP 02 OF 14')
    click(container.querySelector<HTMLElement>('aside button:not(.lib-tour)')!)
    expect(overlay()).toBeNull()
    expect(window.location.pathname).toBe('/')
    await move(() => window.history.back())
    click(sidebarBtn())
    expect(step()).toBe('STEP 01 OF 14')
    await move(() => window.history.forward())
    expect(overlay()).toBeNull()
  })

  it('tour_everyCardStepSpotlightsItsOwnFeatureCard', () => {
    allRects()
    TOUR.forEach((t, n) => rects.set(`fc-${t.f}`, { left: 330, top: 200 + n * 10, width: 340, height: 300 }))
    mount('/')
    click(sidebarBtn())
    for (let n = 0; n < TOUR.length; n++) {
      click(btn('Next'))
      expect(box(spotlight())[1]).toBe(192 + n * 10)
      if (n < TOUR.length - 1) click(btn('Next'))
    }
  })
})
