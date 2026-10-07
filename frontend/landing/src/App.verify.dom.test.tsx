// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// The emailed-link landing: `?verified=1` and `?verify=failed` (set by internal/gateway/register.go) show a notice.
/// <reference types="node" />
import { StrictMode, act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const DIALOG = '[role="dialog"]'
const STATUS = '[role="status"]'
const VERIFIED = 'Your email address is verified. Please sign in.'
const FAILED = 'That link did not work. It may have expired or already been used.'
const STATE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'

let container: HTMLDivElement
let root: Root
let consoleError: ReturnType<typeof vi.spyOn>

function memoryStore() {
  const map = new Map<string, string>()
  return {
    getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
    setItem: (k: string, v: string) => void map.set(k, String(v)),
    removeItem: (k: string) => void map.delete(k),
    clear: () => map.clear(),
    key: () => null,
    get length() {
      return map.size
    },
  }
}

beforeEach(() => {
  vi.stubGlobal('localStorage', memoryStore())
  vi.stubGlobal('sessionStorage', memoryStore())
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
  vi.stubEnv('VITE_APP_URL', 'https://app.x')
  vi.resetModules()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  window.history.replaceState(null, '', '/')
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function setUrl(path: string): void {
  window.history.replaceState(null, '', path)
  expect(window.location.pathname + window.location.search + window.location.hash).toBe(path)
}

async function renderApp(strict = false): Promise<void> {
  const mod = (await import('./App')) as { default: () => ReturnType<typeof createElement> }
  await act(async () => {
    root.render(strict ? createElement(StrictMode, null, createElement(mod.default)) : createElement(mod.default))
  })
}

async function bootAt(path: string, strict = false): Promise<void> {
  setUrl(path)
  await renderApp(strict)
}

async function rebootAt(path: string): Promise<void> {
  await act(async () => root.unmount())
  root = createRoot(container)
  vi.resetModules()
  await bootAt(path)
}

const statuses = () => Array.from(document.querySelectorAll<HTMLElement>(STATUS))
const dialogs = () => document.querySelectorAll(DIALOG)

function pageMounted(): void {
  expect(document.querySelectorAll('header button').length).toBeGreaterThan(0)
}

function onlyNotice(text: string): HTMLElement {
  const s = statuses()
  expect(s.length).toBe(1)
  expect(s[0].textContent).toContain(text)
  return s[0]
}

describe('verify notice: the emailed-link landing', () => {
  it('verified=1 shows the verified notice and opens nothing', async () => {
    await bootAt('/?verified=1')
    pageMounted()
    const n = onlyNotice(VERIFIED)
    expect(n.textContent).not.toContain(FAILED)
    expect(dialogs().length).toBe(0)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('verify=failed shows the failed notice and opens nothing', async () => {
    await bootAt('/?verify=failed')
    pageMounted()
    const n = onlyNotice(FAILED)
    expect(n.textContent).not.toContain(VERIFIED)
    expect(dialogs().length).toBe(0)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('the notice sits in a container between the header and the page content, with a Dismiss button', async () => {
    await bootAt('/?verified=1')
    const n = onlyNotice(VERIFIED)
    const header = document.querySelector('header')!
    const h1 = document.querySelector('h1')!
    expect(header).not.toBeNull()
    expect(h1).not.toBeNull()
    expect(n.closest('.container')).not.toBeNull()
    expect(header.compareDocumentPosition(n) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(n.compareDocumentPosition(h1) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(Array.from(n.querySelectorAll('button')).filter((b) => b.textContent?.trim() === 'Dismiss').length).toBe(1)
  })

  it('the privacy page shows both notices too, on its own path', async () => {
    await bootAt('/privacy?verified=1')
    pageMounted()
    expect(document.querySelector('h1')?.textContent).toContain('Privacy')
    onlyNotice(VERIFIED)
    expect(dialogs().length).toBe(0)
    expect(window.location.pathname).toBe('/privacy')

    await rebootAt('/privacy?verify=failed')
    onlyNotice(FAILED)
    expect(dialogs().length).toBe(0)
    expect(window.location.pathname).toBe('/privacy')
  })

  it('the verify params are stripped and others stay', async () => {
    setUrl('/?a=1&verified=1&b=2#faq')
    const replace = vi.spyOn(window.history, 'replaceState')
    await renderApp()
    expect(window.location.search).toBe('?a=1&b=2')
    expect(window.location.hash).toBe('#faq')
    expect(replace).toHaveBeenCalledTimes(1)
    expect(replace).toHaveBeenCalledWith(null, '', '/?a=1&b=2#faq')
    onlyNotice(VERIFIED)

    await rebootAt('/privacy?keep=1&verify=failed#top')
    expect(window.location.pathname).toBe('/privacy')
    expect(window.location.search).toBe('?keep=1')
    expect(window.location.hash).toBe('#top')
    onlyNotice(FAILED)
  })

  it('the notice outlives the strip and a re-render, read once at boot with StrictMode included', async () => {
    await bootAt('/?verified=1', true)
    onlyNotice(VERIFIED)
    expect(window.location.search).toBe('')

    // A re-render after the strip must not re-read the URL.
    await rebootAt('/?verify=failed')
    const login = Array.from(document.querySelectorAll('header button')).find((b) => b.textContent?.trim() === 'Sign in')
    expect(login).toBeDefined()
    await act(async () => (login as HTMLButtonElement).click())
    expect(dialogs().length).toBe(1)
    onlyNotice(FAILED)
  })

  it('Dismiss removes the notice and nothing else', async () => {
    for (const [search, text] of [
      ['?verified=1', VERIFIED],
      ['?verify=failed', FAILED],
    ] as const) {
      await rebootAt(`/${search}`)
      const n = onlyNotice(text)
      const dismiss = Array.from(n.querySelectorAll('button')).find((b) => b.textContent?.trim() === 'Dismiss')
      expect(dismiss, search).toBeDefined()
      await act(async () => (dismiss as HTMLButtonElement).click())
      expect(statuses().length, search).toBe(0)
      expect(document.body.textContent, search).not.toContain(text)
      expect(dialogs().length, search).toBe(0)
      pageMounted()

      // A later re-render does not bring it back.
      const login = Array.from(document.querySelectorAll('header button')).find((b) => b.textContent?.trim() === 'Sign in')
      expect(login, search).toBeDefined()
      await act(async () => (login as HTMLButtonElement).click())
      expect(dialogs().length, search).toBe(1)
      expect(statuses().length, search).toBe(0)
    }
  })

  it('other verify values show nothing', async () => {
    // Positive control first, so an always-empty render cannot pass.
    await bootAt('/?verified=1')
    onlyNotice(VERIFIED)

    const searches = [
      '?verified=0',
      '?verified=true',
      '?verified=',
      '?verified=1&verified=1',
      '?verified=1&verified=0',
      '?verified=0&verified=1',
      '?verify=ok',
      '?verify=',
      '?verify=failed&verify=failed',
      '?verify=FAILED',
      '?verify=Failed',
      '?Verified=1',
      '?verified=%201',
      '?verified=1&verify=failed',
      '?verify=failed&verified=1',
    ]
    expect(searches.length).toBeGreaterThan(0)
    for (const search of searches) {
      await rebootAt(`/${search}`)
      pageMounted()
      expect(statuses().length, search).toBe(0)
      expect(document.body.textContent, search).not.toContain(VERIFIED)
      expect(document.body.textContent, search).not.toContain(FAILED)
      expect(dialogs().length, search).toBe(0)
      // Owned params go for any value, a mixed pair included.
      const left = new URLSearchParams(window.location.search)
      expect(left.has('verified'), search).toBe(false)
      expect(left.has('verify'), search).toBe(false)
    }
  })
})

describe('verify notice: coexisting with the sign-in params', () => {
  it('verified=1 beside a sign-in state opens nothing and strips every owned param', async () => {
    vi.stubGlobal('fetch', vi.fn().mockReturnValue(new Promise(() => undefined)))
    await bootAt(`/?state=${STATE}&verified=1&keep=1`)
    expect(window.location.search).toBe('?keep=1')
    expect(dialogs().length).toBe(0)
    onlyNotice(VERIFIED)
  })
})
