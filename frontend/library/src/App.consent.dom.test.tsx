// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://library.ascomply.com/" }
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { sliceCookieNoticeCss } from './cookieNoticeCss'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

type TestWindow = Window & { dataLayer?: IArguments[] }
type Act = (cb: () => void) => void

const ID = 'G-TEST'
const KEY = 'asc_consent'
const GRANT = '{"analytics":true,"ts":"","v":1}'
const DENY = '{"analytics":false,"ts":"","v":1}'
const TAG = `script[src="https://www.googletagmanager.com/gtag/js?id=${ID}"]`
const LANDING_CSS = join(dirname(fileURLToPath(import.meta.url)), '../../landing/src/styles/landing.css')

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
let unmount: () => void
let act: Act

async function load() {
  vi.resetModules()
  const React = await import('react')
  const { createRoot } = await import('react-dom/client')
  act = React.act as Act
  const { App } = await import('./App')
  const analytics = await import('./analytics')
  return { React, createRoot, App, analytics }
}

async function mount(path = '/') {
  const m = await load()
  window.history.replaceState(null, '', path)
  container = document.createElement('div')
  document.body.appendChild(container)
  const root = m.createRoot(container)
  act(() => root.render(m.React.createElement(m.App)))
  unmount = () => act(() => root.unmount())
  return m
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

const main = () => container.querySelector<HTMLElement>('#lib-main')!
const notices = () => container.querySelectorAll('[role="region"][aria-label="Cookie notice"]')
const byText = (text: string) =>
  [...container.querySelectorAll<HTMLElement>('button')].find((b) => b.textContent?.trim() === text)!
const click = (el: Element) => act(() => void el.dispatchEvent(new MouseEvent('click', { bubbles: true })))
const record = () => JSON.parse(localStorage.getItem(KEY)!)
const entries = () => ((window as TestWindow).dataLayer ?? []).map((a) => Array.from(a))

describe('library consent', () => {
  it('CO-01 the notice shows with no answer and not with one', async () => {
    await mount()
    expect(notices()).toHaveLength(1)
    const kids = [...main().children]
    expect(kids[kids.length - 2]).toBe(notices()[0])
    expect(main().contains(notices()[0])).toBe(true)
    expect(byText('Accept')).toBeDefined()
    expect(byText('Reject')).toBeDefined()
    expect(container.querySelector('.cn-setting')).toBeNull()
    expect(notices()[0].querySelector<HTMLAnchorElement>('a.cn-link')?.getAttribute('href')).toBe('https://www.ascomply.com/privacy')
    unmount()
    container.remove()

    localStorage.setItem(KEY, DENY)
    await mount()
    expect(notices()).toHaveLength(0)
  })

  it('CO-02 a choice is stored on this origin and closes the notice', async () => {
    await mount()
    click(byText('Accept'))
    expect(record()).toMatchObject({ analytics: true, v: 1 })
    expect(notices()).toHaveLength(0)
    unmount()
    container.remove()

    localStorage.clear()
    await mount()
    click(byText('Reject'))
    expect(record()).toMatchObject({ analytics: false, v: 1 })
    expect(notices()).toHaveLength(0)
  })

  it('CO-03 Accept loads the tag on the library host only', async () => {
    await mount()
    click(byText('Accept'))
    expect(document.querySelectorAll(TAG)).toHaveLength(1)
  })

  it('CO-04 boot loads the tag for a stored grant on the library host', async () => {
    localStorage.setItem(KEY, GRANT)
    const granted = await mount()
    expect(granted.analytics.bootLibraryAnalytics()).toBe(true)
    expect(document.querySelectorAll(TAG)).toHaveLength(1)
    unmount()
    container.remove()

    document.head.innerHTML = ''
    localStorage.clear()
    const none = await mount()
    expect(none.analytics.bootLibraryAnalytics()).toBe(false)
    expect(document.querySelectorAll('script[src^="https://www.googletagmanager.com/"]')).toHaveLength(0)
  })

  it('CO-05 Cookie choices reopens the notice with the setting', async () => {
    localStorage.setItem(KEY, GRANT)
    await mount()
    expect(notices()).toHaveLength(0)
    click(byText('Cookie choices'))
    expect(notices()).toHaveLength(1)
    expect(container.querySelector('.cn-setting')?.textContent).toBe('Analytics cookies are on.')
    click(byText('Reject'))
    expect(notices()).toHaveLength(0)
    expect(record().analytics).toBe(false)
  })

  it('CO-06 the library keeps its _ga on its own host', async () => {
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
    await mount()
    click(byText('Accept'))
    expect(entries().find((e) => e[0] === 'config')).toEqual(['config', ID, { cookie_domain: 'library.ascomply.com' }])

    click(byText('Cookie choices'))
    click(byText('Reject'))
    expect(writes.some((w) => w.includes('domain=library.ascomply.com'))).toBe(true)
    expect(writes.filter((w) => /domain=\.?ascomply\.com/.test(w))).toEqual([])
  })

  it('CSS-03 the app renders the slice', async () => {
    await mount()
    const styles = container.querySelectorAll('style')
    expect(styles).toHaveLength(1)
    expect(styles[0].textContent).toBe(sliceCookieNoticeCss(readFileSync(LANDING_CSS, 'utf8')))
  })
})
