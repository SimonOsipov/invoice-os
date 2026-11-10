// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/check/" }
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readConsentCookie } from '../../landing/src/consent'
import { CHECK_H1 } from './copy'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

type Act = (cb: () => void) => void

const KEY = 'asc_consent'
const DENY = '{"analytics":false,"ts":"","v":1}'

// Node's own localStorage global shadows jsdom's on some versions; install a known store.
const store = new Map<string, string>()
Object.defineProperty(globalThis, 'localStorage', {
  configurable: true,
  value: {
    getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
    setItem: (k: string, v: string) => void store.set(k, String(v)),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  },
})

let container: HTMLDivElement
let unmount: () => void
let act: Act

async function mount() {
  vi.resetModules()
  const React = await import('react')
  const { createRoot } = await import('react-dom/client')
  act = React.act as Act
  const { App } = await import('./App')
  container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  act(() => root.render(React.createElement(App)))
  unmount = () => act(() => root.unmount())
}

beforeEach(() => {
  document.head.innerHTML = ''
  document.body.innerHTML = ''
  vi.stubEnv('VITE_GA_MEASUREMENT_ID', 'G-TEST')
})

afterEach(() => {
  unmount()
  container.remove()
  localStorage.clear()
  vi.unstubAllEnvs()
  document.cookie = `${KEY}=; Max-Age=0; Path=/; Domain=ascomply.com`
})

const notices = () => container.querySelectorAll('[role="region"][aria-label="Cookie notice"]')
const byText = (text: string) =>
  [...container.querySelectorAll<HTMLElement>('button')].find((b) => b.textContent?.trim() === text)!
const click = (el: Element) => act(() => void el.dispatchEvent(new MouseEvent('click', { bubbles: true })))

describe('check consent', () => {
  it('app_rendersOneH1First', async () => {
    await mount()
    const h = [...container.querySelectorAll('h1,h2,h3,h4,h5,h6')]
    expect(h.filter((e) => e.tagName === 'H1')).toHaveLength(1)
    expect(h[0].tagName).toBe('H1')
    expect(h[0].textContent).toBe(CHECK_H1)
  })

  it('notice_showsWithNoRecordAndAcceptHidesIt', async () => {
    await mount()
    expect(notices()).toHaveLength(1)
    expect(notices()[0].querySelector<HTMLAnchorElement>('a.cn-link')?.getAttribute('href')).toBe('https://www.ascomply.com/privacy')
    click(byText('Accept'))
    expect(readConsentCookie(document)).toMatchObject({ analytics: true, v: 1 })
    expect(notices()).toHaveLength(0)
    // Control for previewHost_loadsNoTag: the same Accept loads the tag on the production host.
    expect(document.querySelectorAll('script[src*="googletagmanager"]')).toHaveLength(1)
  })

  it('cookieChoices_bringsTheNoticeBack', async () => {
    document.cookie = `${KEY}=${encodeURIComponent(DENY)}; Domain=ascomply.com; Path=/; Secure`
    await mount()
    expect(notices()).toHaveLength(0)
    click(byText('Cookie choices'))
    expect(notices()).toHaveLength(1)
    expect(container.querySelector('.cn-setting')?.textContent).toBe('Analytics cookies are off.')
    click(byText('Accept'))
    expect(notices()).toHaveLength(0)
  })

  it('footer_linksThePrivacyPage', async () => {
    await mount()
    const a = [...container.querySelectorAll<HTMLAnchorElement>('footer a')].find((x) => x.textContent === 'Privacy')
    expect(a?.getAttribute('href')).toBe('https://www.ascomply.com/privacy')
  })
})
