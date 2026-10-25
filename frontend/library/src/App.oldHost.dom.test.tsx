// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://library.ascomply.com/library/" }
import { afterEach, describe, expect, it, vi } from 'vitest'
import { readConsentCookie } from '../../landing/src/consent'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const ID = 'G-TEST'
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

// ceiling: the old library host stays admitted until its 301 is live.
describe('library on the old library host', () => {
  afterEach(() => {
    store.clear()
    vi.unstubAllEnvs()
    delete (document as { cookie?: string }).cookie
  })

  it('OH-01 a stored grant loads the tag and a Reject expires _ga on the old host', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
    document.cookie = `asc_consent=${encodeURIComponent('{"analytics":true,"ts":"","v":1}')}; Domain=ascomply.com; Path=/; Secure`
    vi.resetModules()
    const React = await import('react')
    const { createRoot } = await import('react-dom/client')
    const { App } = await import('./App')
    const analytics = await import('./analytics')
    expect(analytics.bootLibraryAnalytics()).toBe(true)
    expect(document.querySelectorAll(`script[src="https://www.googletagmanager.com/gtag/js?id=${ID}"]`)).toHaveLength(1)

    const container = document.createElement('div')
    document.body.appendChild(container)
    const root = createRoot(container)
    React.act(() => root.render(React.createElement(App)))
    document.cookie = '_ga=x'
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
    const click = (label: string) =>
      React.act(() => void [...container.querySelectorAll<HTMLElement>('button')].find((b) => b.textContent?.trim() === label)!.dispatchEvent(new MouseEvent('click', { bubbles: true })))
    click('Cookie choices')
    click('Reject')
    const expiry = (d: string) => writes.some((w) => w.startsWith('_ga=; ') && w.includes('Max-Age=0') && w.endsWith(`domain=${d}`))
    expect(expiry('library.ascomply.com')).toBe(true)
    expect(expiry('.ascomply.com')).toBe(true)
    expect(readConsentCookie(document)!.analytics).toBe(false)
    React.act(() => root.unmount())
  })
})
