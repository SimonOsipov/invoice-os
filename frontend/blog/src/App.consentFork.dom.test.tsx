// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://blog-pr-1-944095690109.africa-south1.run.app/blog/" }
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import './testStorage'
import { w } from '../../landing/src/hubspotChat.test.util'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

type TestWindow = Window & { dataLayer?: IArguments[] }
type Act = (cb: () => void) => void

const ID = 'G-TEST'
const KEY = 'asc_consent'
const GRANT = '{"analytics":true,"ts":"","v":1}'
const ANY_TAG = 'script[src^="https://www.googletagmanager.com/"]'

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

describe('blog consent off the blog host', () => {
  it('CO-03 Accept loads no tag on a PR fork host', async () => {
    await mount()
    click(byText('Accept'))
    expect(JSON.parse(localStorage.getItem(KEY)!).analytics).toBe(true)
    expect(document.querySelectorAll(ANY_TAG)).toHaveLength(0)
    expect(document.cookie).not.toContain(KEY)
  })

  it('CO-04 a stored grant loads no tag on a PR fork host', async () => {
    localStorage.setItem(KEY, GRANT)
    const m = await mount()
    expect(m.analytics.bootBlogAnalytics()).toBe(false)
    expect(document.querySelectorAll(ANY_TAG)).toHaveLength(0)
    expect((window as TestWindow).dataLayer).toBeUndefined()
  })
})

describe('blog chat off the blog host', () => {
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
    expect(m.analytics.bootBlogChat()).toBe(false)
    act(() => void document.dispatchEvent(new Event('visibilitychange')))
    expect(loaders()).toHaveLength(0)
  })
})
