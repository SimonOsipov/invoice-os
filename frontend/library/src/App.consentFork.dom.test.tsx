// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://library-pr-1-944095690109.africa-south1.run.app/library/" }
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { w } from '../../landing/src/hubspotChat.test.util'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

type TestWindow = Window & { dataLayer?: IArguments[] }
type Act = (cb: () => void) => void

const ID = 'G-TEST'
const KEY = 'asc_consent'
const GRANT = '{"analytics":true,"ts":"","v":1}'
const ANY_TAG = 'script[src^="https://www.googletagmanager.com/"]'

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
  delete w._hsq
  delete w.HubSpotConversations
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

const byText = (text: string) =>
  [...container.querySelectorAll<HTMLElement>('button')].find((b) => b.textContent?.trim() === text)!
const click = (el: Element) => act(() => void el.dispatchEvent(new MouseEvent('click', { bubbles: true })))

describe('library consent off the library host', () => {
  it('CO-03 Accept loads no tag on a PR fork host', async () => {
    await mount()
    click(byText('Accept'))
    expect(JSON.parse(localStorage.getItem(KEY)!).analytics).toBe(true)
    expect(document.querySelectorAll(ANY_TAG)).toHaveLength(0)
  })

  it('CO-04 a stored grant loads no tag on a PR fork host', async () => {
    localStorage.setItem(KEY, GRANT)
    const m = await mount()
    expect(m.analytics.bootLibraryAnalytics()).toBe(false)
    expect(document.querySelectorAll(ANY_TAG)).toHaveLength(0)
    expect((window as TestWindow).dataLayer).toBeUndefined()
  })

  it('CO-05 a stored Reject on a PR fork host boots nothing and keeps the record', async () => {
    localStorage.setItem(KEY, '{"analytics":false,"ts":"","v":1}')
    const m = await mount()
    expect(m.analytics.bootLibraryAnalytics()).toBe(false)
    expect(document.querySelectorAll(ANY_TAG)).toHaveLength(0)
    expect(JSON.parse(localStorage.getItem(KEY)!).analytics).toBe(false)
  })
})

describe('library chat off the library host', () => {
  const loaders = () => document.querySelectorAll('script#hs-script-loader')

  it('CH-09 Accept loads no chat on a PR fork host', async () => {
    await mount()
    click(byText('Accept'))
    expect(loaders()).toHaveLength(0)
    expect(w._hsq).toBeUndefined()
  })

  it('CH-10 a stored grant boots and syncs no chat on a PR fork host', async () => {
    localStorage.setItem(KEY, GRANT)
    const m = await mount()
    expect(m.analytics.bootLibraryChat()).toBe(false)
    act(() => void document.dispatchEvent(new Event('visibilitychange')))
    expect(loaders()).toHaveLength(0)
  })
})
