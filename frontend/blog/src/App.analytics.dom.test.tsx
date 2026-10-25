// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/blog/" }
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import './testStorage'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

type TestWindow = Window & { dataLayer?: IArguments[] }
type Act = (cb: () => void) => void

const ID = 'G-TEST'
const KEY = 'asc_consent'
const GRANT = '{"analytics":true,"ts":"","v":1}'

let container: HTMLDivElement
let unmount: () => void
let act: Act

async function mount(path = '/') {
  vi.resetModules()
  const React = await import('react')
  const { createRoot } = await import('react-dom/client')
  act = React.act as Act
  const { App } = await import('./App')
  const analytics = await import('./analytics')
  analytics.bootBlogAnalytics()
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
})

afterEach(() => {
  unmount()
  container.remove()
  localStorage.clear()
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
  delete (document as { cookie?: string }).cookie
})

const all = () => ((window as TestWindow).dataLayer ?? []).map((a) => Array.from(a))
const events = () => all().filter((e) => e[0] === 'event')
const click = (el: Element) => act(() => void el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true })))
const byText = (sel: string, text: string) =>
  [...container.querySelectorAll<HTMLElement>(sel)].find((b) => b.textContent?.trim() === text)!
const brand = () => container.querySelector<HTMLElement>('aside button')!
const crumb = (text: string) => byText('#blog-header nav[aria-label="Breadcrumb"] button', text)

describe('blog events', () => {
  it('EV-01 the first path is the config page view only', async () => {
    localStorage.setItem(KEY, GRANT)
    await mount('/')
    expect(events()).toEqual([])
    expect(all().filter((e) => e[0] === 'config')).toHaveLength(1)
  })

  it('EV-02 each new path sends one page_view', async () => {
    localStorage.setItem(KEY, GRANT)
    await mount('/')
    click(byText('aside button', 'Subscribe'))
    expect(window.location.pathname).toBe('/blog/subscribe')
    click(crumb('Blog'))
    expect(window.location.pathname).toBe('/blog/')
    expect(events()).toEqual([
      ['event', 'page_view', {}],
      ['event', 'page_view', {}],
    ])
  })

  it('EV-03 the same view and a correction send none', async () => {
    localStorage.setItem(KEY, GRANT)
    await mount('/')
    click(brand())
    expect(events()).toEqual([])
    unmount()
    container.remove()

    await mount('/subscribe/')
    click(byText('aside button', 'Subscribe'))
    expect(window.location.pathname).toBe('/blog/subscribe')
    expect(events()).toEqual([])
  })

  it('EV-04 nothing is sent before consent', async () => {
    await mount('/')
    click(byText('aside button', 'Subscribe'))
    click(crumb('Blog'))
    expect((window as TestWindow).dataLayer).toBeUndefined()
  })

  it('EV-05 Book the Demo sends demo_open with cta_location blog', async () => {
    localStorage.setItem(KEY, GRANT)
    await mount('/')
    click(byText('aside a', 'Book the Demo'))
    expect(events()).toEqual([['event', 'demo_open', { cta_location: 'blog' }]])
  })

  it('EV-06 Book the Demo sends nothing before consent', async () => {
    await mount('/')
    click(byText('aside a', 'Book the Demo'))
    expect((window as TestWindow).dataLayer).toBeUndefined()
  })
})

describe('blog tag on the www path', () => {
  it('AN-01 a stored grant loads the gtag script once', async () => {
    localStorage.setItem(KEY, GRANT)
    await mount('/blog/')
    expect(document.querySelectorAll(`script[src="https://www.googletagmanager.com/gtag/js?id=${ID}"]`)).toHaveLength(1)
  })
})
