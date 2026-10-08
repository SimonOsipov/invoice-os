// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://library.ascomply.com/" }
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import './testStorage'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

type TestWindow = Window & { dataLayer?: IArguments[] }
type Act = (cb: () => void | Promise<void>) => void | Promise<void>

const ID = 'G-TEST'
const KEY = 'asc_consent'
const GRANT = '{"analytics":true,"ts":"","v":1}'

let container: HTMLDivElement
let unmount: () => void
let act: Act

// jsdom would navigate on an anchor click; the library's links are external.
const stopNavigation = (e: Event) => e.preventDefault()

async function mount(path = '/') {
  vi.resetModules()
  const React = await import('react')
  const { createRoot } = await import('react-dom/client')
  act = React.act as Act
  const { App } = await import('./App')
  const analytics = await import('./analytics')
  analytics.bootLibraryAnalytics()
  window.history.replaceState(null, '', path)
  container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  act(() => root.render(React.createElement(React.StrictMode, null, React.createElement(App))))
  unmount = () => act(() => root.unmount())
}

beforeEach(() => {
  document.head.innerHTML = ''
  delete (window as TestWindow).dataLayer
  vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
  vi.stubEnv('VITE_LANDING_URL', 'https://l.example')
  vi.stubEnv('VITE_APP_URL', 'https://a.example')
  document.addEventListener('click', stopNavigation, true)
})

afterEach(() => {
  unmount()
  container.remove()
  document.removeEventListener('click', stopNavigation, true)
  localStorage.clear()
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
  delete (document as { cookie?: string }).cookie
})

const all = () => ((window as TestWindow).dataLayer ?? []).map((a) => Array.from(a))
const events = () => all().filter((e) => e[0] === 'event')
const pageViews = () => events().filter((e) => e[1] === 'page_view')
const click = (el: Element) => act(() => void el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true })))
const byText = (sel: string, text: string) =>
  [...container.querySelectorAll<HTMLElement>(sel)].filter((b) => b.textContent?.trim().startsWith(text))
const button = (text: string) => byText('button', text)[0]
const link = (text: string) => byText('a', text)
const nav = (gid: string) => container.querySelector<HTMLElement>(`#nav-${gid}`)!
const card = (fid: string) => container.querySelector<HTMLElement>(`#fc-${fid}`)!
const homeCard = (name: string) =>
  [...container.querySelectorAll<HTMLElement>('#lib-main button.lib-card')].find((b) => b.textContent?.includes(name))!

describe('library events', () => {
  beforeEach(() => localStorage.setItem(KEY, GRANT))

  it('EV-01 the first path is the config page view only', async () => {
    await mount('/rules')
    expect(events()).toEqual([])
    expect(all().filter((e) => e[0] === 'config')).toHaveLength(1)
  })

  it('EV-02 each new path sends one page_view', async () => {
    await mount('/')
    click(homeCard('Rules & validation'))
    expect(window.location.pathname).toBe('/rules')
    click(card('validate'))
    expect(window.location.pathname).toBe('/rules/validate')
    click(button('Overview'))
    expect(window.location.pathname).toBe('/')
    expect(events()).toEqual([
      ['event', 'page_view', {}],
      ['event', 'page_view', {}],
      ['event', 'page_view', {}],
    ])
  })

  it('EV-03 the same view and a correction send none', async () => {
    await mount('/rules')
    click(nav('rules'))
    expect(events()).toEqual([])
    unmount()
    container.remove()

    await mount('/invoices/')
    click(nav('invoices'))
    expect(window.location.pathname).toBe('/invoices')
    expect(events()).toEqual([])
    unmount()
    container.remove()

    await mount('/rules/validate')
    click(container.querySelector<HTMLElement>('.lib-subnav button[aria-current="true"]')!)
    expect(events()).toEqual([])
  })

  it('EV-04 back and forward each send one', async () => {
    await mount('/')
    click(nav('rules'))
    click(card('validate'))
    expect(pageViews()).toHaveLength(2)
    for (const [i, move] of [() => window.history.back(), () => window.history.forward()].entries()) {
      const popped = new Promise((r) => window.addEventListener('popstate', r, { once: true }))
      await act(async () => {
        move()
        await popped
      })
      expect(pageViews()).toHaveLength(3 + i)
    }
  })

  it('EV-05 both Book the Demo links report the library source', async () => {
    await mount('/')
    const links = link('Book the Demo')
    expect(links).toHaveLength(2)
    click(links[0])
    click(links[1])
    expect(events()).toEqual([
      ['event', 'demo_open', { cta_location: 'library' }],
      ['event', 'demo_open', { cta_location: 'library' }],
    ])
  })

  it('EV-06 Open in Platform carries the group or the feature id', async () => {
    await mount('/rules')
    click(link('Open in Platform')[0])
    expect(events()).toEqual([['event', 'open_in_platform', { group_id: 'rules' }]])
    unmount()
    container.remove()

    delete (window as TestWindow).dataLayer
    await mount('/rules/validate')
    click(link('Open in Platform')[0])
    expect(events()).toEqual([['event', 'open_in_platform', { feature_id: 'validate' }]])
  })

  it('EV-07 nothing is sent before consent', async () => {
    localStorage.clear()
    await mount('/')
    click(nav('rules'))
    click(link('Book the Demo')[0])
    expect((window as TestWindow).dataLayer).toBeUndefined()
    click(button('Accept'))
    expect(all().some((e) => e[0] === 'config')).toBe(true)
    click(card('validate'))
    expect(pageViews()).toHaveLength(1)
  })

  it('EV-10 Reject through Cookie choices stops the senders and expires _ga', async () => {
    const desc = Object.getOwnPropertyDescriptor(Document.prototype, 'cookie')!
    document.cookie = '_ga=x'
    const writes: string[] = []
    Object.defineProperty(document, 'cookie', {
      configurable: true,
      get: () => desc.get!.call(document),
      set: (v: string) => {
        writes.push(v)
        desc.set!.call(document, v)
      },
    })
    await mount('/')
    click(nav('rules'))
    expect(events()).toHaveLength(1)
    click(button('Cookie choices'))
    click(button('Reject'))
    expect(writes.some((w) => w.startsWith('_ga=; ') && w.includes('Max-Age=0') && w.includes('domain=library.ascomply.com'))).toBe(true)
    expect(writes.filter((w) => /domain=\.?ascomply\.com/.test(w))).toEqual([])
    click(card('validate'))
    click(link('Book the Demo')[0])
    expect(events()).toHaveLength(1)
    expect(JSON.parse(localStorage.getItem(KEY)!).analytics).toBe(false)
  })
})

describe('library tour events', () => {
  const NOTICE = '[aria-label="Cookie notice"]'
  const tourStarts = () => events().filter((e) => e[1] === 'tour_start')
  const sidebarTour = () => container.querySelector<HTMLElement>('.lib-tour')!

  beforeEach(() => {
    window.matchMedia = ((query: string) => ({
      matches: false,
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
    })) as unknown as typeof window.matchMedia
  })
  afterEach(() => {
    delete (window as { matchMedia?: unknown }).matchMedia
  })

  it('EV-08 each start sends one tour_start', async () => {
    localStorage.setItem(KEY, GRANT)
    await mount('/')
    click(button('Take the tour'))
    expect(tourStarts()).toEqual([['event', 'tour_start', {}]])
    click(sidebarTour())
    click(sidebarTour())
    expect(tourStarts()).toHaveLength(2)
  })

  it('two synchronous tour clicks send one tour_start and leave the tour closed', async () => {
    localStorage.setItem(KEY, GRANT)
    await mount('/')
    const btn = sidebarTour()
    act(() => {
      btn.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
      btn.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    })
    expect(tourStarts()).toHaveLength(1)
    expect(container.querySelector('.lib-tour')?.textContent).toBe('Take the tour')
  })

  it('EV-09 stopping and stepping send no tour_start', async () => {
    localStorage.setItem(KEY, GRANT)
    await mount('/')
    click(button('Take the tour'))
    for (let i = 0; i < 3; i++) click(button('Next'))
    expect(tourStarts()).toHaveLength(1)
    click(sidebarTour())
    expect(container.querySelector('.lib-tour')?.textContent).toBe('Take the tour')
    expect(tourStarts()).toHaveLength(1)
  })

  it('EV-11 a running tour makes the notice inert', async () => {
    await mount('/')
    expect(container.querySelector(NOTICE)!.hasAttribute('inert')).toBe(false)
    click(button('Take the tour'))
    expect(container.querySelector(NOTICE)!.hasAttribute('inert')).toBe(true)
    click(sidebarTour())
    expect(container.querySelector(NOTICE)!.hasAttribute('inert')).toBe(false)
  })
})
