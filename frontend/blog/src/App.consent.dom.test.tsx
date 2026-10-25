// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/blog/" }
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import './testStorage'
import { readConsentCookie } from '../../landing/src/consent'
import { sliceCookieNoticeCss } from './cookieNoticeCss'
import { COOKIES, container as hsContainer, installSdk, jarNames, resetJar, seed, w } from '../../landing/src/hubspotChat.test.util'
import { CHAT_SCRIPT_URL } from '../../landing/src/hubspotChat'

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
  delete w._hsq
  delete w.HubSpotConversations
  delete w.hsConversationsSettings
  document.body.innerHTML = ''
  resetJar()
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
    for (const d of ['www.ascomply.com', '.www.ascomply.com', 'ascomply.com', '.ascomply.com']) {
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

  it('CO-07 an Accept from another tab closes the notice and loads the tag once', async () => {
    await mount()
    expect(notices()).toHaveLength(1)
    document.cookie = `${KEY}=${encodeURIComponent(GRANT)}; Domain=ascomply.com; Path=/; Secure`
    act(() => void document.dispatchEvent(new Event('visibilitychange')))
    expect(notices()).toHaveLength(0)
    expect(document.querySelectorAll(TAG)).toHaveLength(1)
    act(() => void document.dispatchEvent(new Event('visibilitychange')))
    click(byText('Cookie choices'))
    expect(document.querySelectorAll(TAG)).toHaveLength(1)
  })

  it('CO-08 a Reject from another tab expires _ga on every blog domain form and sends nothing', async () => {
    document.cookie = `${KEY}=${encodeURIComponent(GRANT)}; Domain=ascomply.com; Path=/; Secure`
    const m = await mount()
    expect(m.analytics.bootBlogAnalytics()).toBe(true)
    document.cookie = '_ga=x'
    const writes = spyWrites()
    document.cookie = `${KEY}=${encodeURIComponent(DENY)}; Domain=ascomply.com; Path=/; Secure`
    act(() => void document.dispatchEvent(new Event('visibilitychange')))
    const exp = writes.filter((w) => w.startsWith('_ga=; Max-Age=0'))
    expect(exp.filter((w) => !w.includes('domain='))).toHaveLength(1)
    expect(exp.filter((w) => w.includes('domain=')).map((w) => w.split('domain=')[1]).sort()).toEqual(
      ['.ascomply.com', '.www.ascomply.com', 'ascomply.com', 'www.ascomply.com'],
    )
    click(byText('Subscribe'))
    const events = ((window as TestWindow).dataLayer ?? []).map((a) => Array.from(a)).filter((e) => e[0] === 'event')
    expect(events).toEqual([])
  })

  it('CO-10 Accept then Reject at /blog/ on www write asc_consent on the shared domain and expire _ga and HubSpot cookies on www and its parents', async () => {
    const writes = spyWrites()
    await mount('/blog/')
    click(byText('Accept'))
    document.cookie = '_ga=x'
    seed('__hstc')
    click(byText('Cookie choices'))
    click(byText('Reject'))
    const consent = writes.filter((v) => v.startsWith(`${KEY}=`))
    expect(consent.length).toBeGreaterThanOrEqual(2)
    for (const v of consent) expect(v).toMatch(/; Domain=ascomply\.com; Path=\/;/)
    const forms = ['www.ascomply.com', '.www.ascomply.com', 'ascomply.com', '.ascomply.com']
    for (const name of ['_ga', '__hstc']) {
      const exp = writes.filter((v) => v.startsWith(`${name}=; Max-Age=0`))
      expect(exp.filter((v) => !v.includes('domain='))).not.toHaveLength(0)
      for (const d of forms) expect(exp.some((v) => v.endsWith(`domain=${d}`)), `${name} ${d}`).toBe(true)
    }
  })

  it('CO-09 a hidden tab keeps its notice until it is visible', async () => {
    await mount()
    document.cookie = `${KEY}=${encodeURIComponent(GRANT)}; Domain=ascomply.com; Path=/; Secure`
    const state = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    act(() => void document.dispatchEvent(new Event('visibilitychange')))
    expect(notices()).toHaveLength(1)
    expect(document.querySelectorAll(TAG)).toHaveLength(0)
    state.mockReturnValue('visible')
    act(() => void document.dispatchEvent(new Event('visibilitychange')))
    expect(notices()).toHaveLength(0)
  })

  it('blog mounts the shared notice that names the Blog', async () => {
    await mount()
    expect(notices()[0].querySelector('.cn-body')?.textContent).toContain('the Feature Library and the Blog.')
  })

  it('CSS-01 the app renders the cookie-notice slice', async () => {
    await mount()
    const styles = container.querySelectorAll('style')
    expect(styles).toHaveLength(1)
    expect(styles[0].textContent).not.toBe('')
    expect(styles[0].textContent).toBe(sliceCookieNoticeCss(readFileSync(LANDING_CSS, 'utf8')))
  })
})

describe('blog chat', () => {
  const loaders = () => document.querySelectorAll<HTMLScriptElement>('script#hs-script-loader')
  const grantCookie = () => void (document.cookie = `${KEY}=${encodeURIComponent(GRANT)}; Domain=ascomply.com; Path=/; Secure`)
  const denyCookie = () => void (document.cookie = `${KEY}=${encodeURIComponent(DENY)}; Domain=ascomply.com; Path=/; Secure`)

  it('CH-01 Accept loads the chat once on the blog host', async () => {
    await mount()
    click(byText('Accept'))
    expect(loaders()).toHaveLength(1)
    click(byText('Cookie choices'))
    click(byText('Accept'))
    expect(loaders()).toHaveLength(1)
    expect(loaders()[0].src).toBe(CHAT_SCRIPT_URL)
  })

  it('CH-02 Reject after Accept removes the chat in place', async () => {
    await mount()
    click(byText('Accept'))
    const sdk = installSdk()
    seed('__hstc', 'messagesUtk')
    click(byText('Cookie choices'))
    click(byText('Reject'))
    expect(hsContainer()).toBeNull()
    expect(sdk.widget.remove).toHaveBeenCalledTimes(1)
    expect(sdk.clear).toHaveBeenCalledWith()
    expect(w._hsq?.at(-1)).toEqual(['doNotTrack'])
    expect(jarNames()).not.toContain('__hstc')
    expect(jarNames()).not.toContain('messagesUtk')
  })

  it('CH-03 Accept, Reject, Accept in one page shows the widget again with one loader', async () => {
    await mount()
    click(byText('Accept'))
    const sdk = installSdk()
    click(byText('Cookie choices'))
    click(byText('Reject'))
    const n = w._hsq!.length
    click(byText('Cookie choices'))
    click(byText('Accept'))
    expect(w._hsq!.slice(n)).toContainEqual(['doNotTrack', { track: true }])
    expect(sdk.widget.load).toHaveBeenCalledTimes(1)
    expect(loaders()).toHaveLength(1)
    expect(hsContainer()).not.toBeNull()
  })

  it('CH-04 an Accept from another tab loads the chat once', async () => {
    await mount()
    expect(notices()).toHaveLength(1)
    grantCookie()
    act(() => void document.dispatchEvent(new Event('visibilitychange')))
    act(() => void document.dispatchEvent(new Event('visibilitychange')))
    expect(notices()).toHaveLength(0)
    expect(loaders()).toHaveLength(1)
  })

  it('CH-05 a Reject from another tab removes the chat', async () => {
    grantCookie()
    const m = await mount()
    m.analytics.bootBlogChat()
    installSdk()
    seed('__hssc')
    denyCookie()
    act(() => void document.dispatchEvent(new Event('visibilitychange')))
    expect(hsContainer()).toBeNull()
    expect(w._hsq?.at(-1)).toEqual(['doNotTrack'])
    expect(jarNames()).not.toContain('__hssc')
  })

  it('CH-06 boot loads the chat for a stored grant', async () => {
    grantCookie()
    const m = await mount()
    expect(m.analytics.bootBlogChat()).toBe(true)
    expect(loaders()).toHaveLength(1)
  })

  it('CH-07 boot with a stored Reject loads nothing and clears HubSpot cookies', async () => {
    denyCookie()
    seed(...COOKIES)
    const m = await mount()
    expect(m.analytics.bootBlogChat()).toBe(false)
    expect(loaders()).toHaveLength(0)
    expect(jarNames().filter((n) => COOKIES.includes(n))).toEqual([])
    expect(w._hsq).toBeUndefined()
  })

  it('CH-08 a first Reject loads nothing and pushes nothing', async () => {
    await mount()
    click(byText('Reject'))
    expect(loaders()).toHaveLength(0)
    expect(w._hsq).toBeUndefined()
  })

  it('CH-11 Accept on a fresh page lifts a stale opt-out', async () => {
    seed('__hs_do_not_track')
    expect(jarNames()).toContain('__hs_do_not_track')
    await mount()
    click(byText('Accept'))
    expect(loaders()).toHaveLength(1)
    expect(jarNames()).not.toContain('__hs_do_not_track')
    expect((w._hsq ?? []).filter((c) => Array.isArray(c) && c[0] === 'doNotTrack' && c.length === 1)).toEqual([])
  })

  it('CH-12 boot with a stored grant and a stale opt-out lifts it before the loader', async () => {
    grantCookie()
    seed('__hs_do_not_track')
    const m = await mount()
    let atLoader: unknown[] | undefined
    const append = document.head.appendChild.bind(document.head)
    vi.spyOn(document.head, 'appendChild').mockImplementation(((node: Node) => {
      if ((node as HTMLElement).id === 'hs-script-loader') atLoader = [...(w._hsq ?? [])]
      return append(node)
    }) as typeof document.head.appendChild)
    m.analytics.bootBlogChat()
    expect(atLoader?.[0]).toEqual(['doNotTrack', { track: true }])
    expect(loaders()).toHaveLength(1)
  })
})
