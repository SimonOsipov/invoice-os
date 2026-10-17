// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://blog.ascomply.com/" }
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import './testStorage'
import { readConsentCookie } from '../../landing/src/consent'
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

const main = () => container.querySelector<HTMLElement>('#blog-main')!
const notices = () => container.querySelectorAll('[role="region"][aria-label="Cookie notice"]')
const byText = (text: string) =>
  [...container.querySelectorAll<HTMLElement>('button')].find((b) => b.textContent?.trim() === text)!
const click = (el: Element) => act(() => void el.dispatchEvent(new MouseEvent('click', { bubbles: true })))
const record = () => {
  expect(localStorage.getItem(KEY)).toBeNull()
  return readConsentCookie(document)!
}
function spyWrites(): string[] {
  const desc = Object.getOwnPropertyDescriptor(Document.prototype, 'cookie')!
  const writes: string[] = []
  Object.defineProperty(document, 'cookie', {
    configurable: true,
    get: () => desc.get!.call(document),
    set: (v: string) => {
      writes.push(v)
      desc.set!.call(document, v)
    },
  })
  return writes
}

describe('blog consent', () => {
  it('CO-01 the notice shows with no answer and not with one', async () => {
    await mount()
    expect(notices()).toHaveLength(1)
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

  it('CO-02 a choice is stored in the shared cookie and closes the notice', async () => {
    const writes = spyWrites()
    await mount()
    click(byText('Reject'))
    expect(record()).toMatchObject({ analytics: false, v: 1 })
    expect(writes.some((w) => w.startsWith(`${KEY}=`) && w.includes('Domain=ascomply.com'))).toBe(true)
    expect(notices()).toHaveLength(0)
  })

  it('CO-06 a choice stored by the landing shows no notice on the blog', async () => {
    const grant = encodeURIComponent('{"analytics":true,"ts":"2026-01-01T00:00:00.000Z","v":1}')
    document.cookie = `${KEY}=${grant}; Domain=ascomply.com; Path=/; Secure`
    await mount()
    expect(notices()).toHaveLength(0)
  })

  it('CO-03 Accept loads the tag on the blog host', async () => {
    await mount()
    click(byText('Accept'))
    expect(document.querySelectorAll(TAG)).toHaveLength(1)
  })

  it('CO-04 boot loads the tag for a stored grant', async () => {
    localStorage.setItem(KEY, GRANT)
    const m = await mount()
    expect(m.analytics.bootBlogAnalytics()).toBe(true)
    expect(document.querySelectorAll(TAG)).toHaveLength(1)
  })

  it('BL-01 a boot with a stored Reject expires the leftover _ga', async () => {
    document.cookie = `${KEY}=${encodeURIComponent(DENY)}; Domain=ascomply.com; Path=/; Secure`
    document.cookie = '_ga=x'
    const writes = spyWrites()
    const m = await mount()
    expect(m.analytics.bootBlogAnalytics()).toBe(false)
    expect(document.querySelectorAll('script[src^="https://www.googletagmanager.com/"]')).toHaveLength(0)
    const gaExpiries = writes.filter((w) => w.startsWith('_ga=; Max-Age=0'))
    expect(gaExpiries.some((w) => !w.includes('domain='))).toBe(true)
    for (const d of ['blog.ascomply.com', '.blog.ascomply.com', 'ascomply.com', '.ascomply.com']) {
      expect(gaExpiries.some((w) => w.endsWith(`domain=${d}`)), d).toBe(true)
    }
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

  it('CSS-01 the app renders the cookie-notice slice', async () => {
    await mount()
    const styles = container.querySelectorAll('style')
    expect(styles).toHaveLength(1)
    expect(styles[0].textContent).not.toBe('')
    expect(styles[0].textContent).toBe(sliceCookieNoticeCss(readFileSync(LANDING_CSS, 'utf8')))
  })
})
