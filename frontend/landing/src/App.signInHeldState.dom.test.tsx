// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// The held boot state expires 9 min after boot, and a bfcache restore drops it.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { captureNavigation } from './navigation.test.util'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const DIALOG = '[role="dialog"]'
const STATE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'
const CONTINUE = 'Continue with email'
const START = 'https://app.x?auth=start'
const HOLD_MS = 9 * 60 * 1000
const T0 = new Date('2026-09-25T12:00:00Z').getTime()

let container: HTMLDivElement
let root: Root
let fetchMock: ReturnType<typeof vi.fn>
let nav: ReturnType<typeof captureNavigation>

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
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date', 'performance'] })
  vi.setSystemTime(T0)
  vi.stubGlobal('localStorage', memoryStore())
  vi.stubGlobal('sessionStorage', memoryStore())
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
  vi.stubEnv('VITE_APP_URL', 'https://app.x')
  // The preflight is a GET that resolves; a sign-in POST never answers.
  fetchMock = vi.fn((_url: string, init?: RequestInit) => (init?.method === 'POST' ? new Promise(() => undefined) : Promise.resolve(new Response(null))))
  vi.stubGlobal('fetch', fetchMock)
  nav = captureNavigation()
  vi.resetModules()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  nav.restore()
  act(() => root.unmount())
  container.remove()
  window.history.replaceState(null, '', '/')
  vi.useRealTimers()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function bootAt(path: string): Promise<void> {
  window.history.replaceState(null, '', path)
  const mod = (await import('./App')) as { default: () => ReturnType<typeof createElement> }
  await act(async () => {
    root.render(createElement(mod.default))
  })
}

function onlyDialog(): HTMLElement {
  const d = Array.from(document.querySelectorAll<HTMLElement>(DIALOG))
  expect(d.length).toBe(1)
  return d[0]
}

async function openFromNav(): Promise<void> {
  const b = Array.from(document.querySelectorAll('header button')).find((x) => x.textContent?.trim() === 'Sign in')
  expect(b).toBeDefined()
  await act(async () => (b as HTMLButtonElement).click())
}

async function closeDialog(): Promise<void> {
  await act(async () => onlyDialog().querySelector<HTMLButtonElement>('button[aria-label="Close"]')!.click())
  expect(document.querySelectorAll(DIALOG).length).toBe(0)
}

async function advance(ms: number): Promise<void> {
  await act(async () => {
    vi.advanceTimersByTime(ms)
  })
}

function continueButtons(d: HTMLElement): HTMLButtonElement[] {
  return Array.from(d.querySelectorAll('button')).filter((b) => b.textContent?.trim() === CONTINUE)
}

// Fields shown, no Continue with email.
function expectFields(d: HTMLElement): void {
  expect(d.querySelectorAll('input[type="email"]').length, 'email inputs').toBe(1)
  expect(d.querySelectorAll('input[type="password"]').length, 'password inputs').toBe(1)
  expect(continueButtons(d).length, 'Continue with email buttons').toBe(0)
}

async function fillAndSubmit(d: HTMLElement): Promise<void> {
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  const email = d.querySelector<HTMLInputElement>('input[type="email"]')!
  const password = d.querySelector<HTMLInputElement>('input[type="password"]')!
  await act(async () => {
    setValue.call(email, 'ada@okafor.ng')
    email.dispatchEvent(new Event('input', { bubbles: true }))
    setValue.call(password, 'pw')
    password.dispatchEvent(new Event('input', { bubbles: true }))
  })
  await act(async () => d.querySelector<HTMLButtonElement>('button[type="submit"]')!.click())
}

function posts(): unknown[][] {
  return fetchMock.mock.calls.filter((c) => (c[1] as RequestInit | undefined)?.method === 'POST')
}

function postedStates(): unknown[] {
  return posts().map((c) => (JSON.parse((c[1] as RequestInit).body as string) as { state: unknown }).state)
}

describe('F1a: the held state expires 9 minutes after boot', () => {
  it('9 min minus 1 ms after boot, opening the modal shows the fields and posts the state', async () => {
    await bootAt(`/?state=${STATE}`)
    await advance(HOLD_MS - 1)
    await openFromNav()
    const d = onlyDialog()
    expectFields(d)
    await fillAndSubmit(d)
    expect(postedStates()).toEqual([STATE])
  })

  it('click_after9Minutes_navigates', async () => {
    await bootAt(`/?state=${STATE}`)
    await advance(HOLD_MS - 1)
    await openFromNav()
    expectFields(onlyDialog())
    expect(nav.assigned).toEqual([])
    await closeDialog()
    await advance(1)
    await openFromNav()
    expect(nav.assigned).toEqual([START])
    expect(document.querySelectorAll(DIALOG).length).toBe(0)
    expect(posts()).toEqual([])
  })

  it('submit_afterTheStateExpiresWhileOpen_navigatesAndPostsNothing', async () => {
    await bootAt(`/?state=${STATE}&signin=ready`)
    expectFields(onlyDialog())
    await advance(HOLD_MS - 1)
    expectFields(onlyDialog())
    await advance(1)
    await fillAndSubmit(onlyDialog())
    expect(nav.assigned).toEqual([START])
    expect(posts(), 'a sign-in post with the stale state').toEqual([])
  })

  it('an expired state stays dropped across a close and reopen', async () => {
    await bootAt(`/?state=${STATE}&signin=ready`)
    expectFields(onlyDialog())
    await closeDialog()
    await advance(HOLD_MS)
    await openFromNav()
    expect(nav.assigned).toEqual([START])
    expect(document.querySelectorAll(DIALOG).length).toBe(0)
    await advance(60 * 1000)
    // The first bounce left the page; a Back restore lets the next click bounce again.
    await act(async () => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
    })
    await openFromNav()
    expect(nav.assigned).toEqual([START, START])
    expect(posts()).toEqual([])
  })
})

describe('F3: a bfcache restore drops the held state', () => {
  it('submit_afterBfcacheRestore_withOpenWindow_navigates', async () => {
    await bootAt(`/?state=${STATE}&signin=ready`)
    expectFields(onlyDialog())
    await act(async () => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
    })
    const d = onlyDialog()
    expectFields(d)
    await fillAndSubmit(d)
    expect(nav.assigned).toEqual([START])
    expect(posts()).toEqual([])
  })

  it('click_afterBfcacheRestore_navigates', async () => {
    await bootAt(`/?state=${STATE}`)
    await act(async () => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
    })
    await openFromNav()
    expect(nav.assigned).toEqual([START])
    expect(document.querySelectorAll(DIALOG).length).toBe(0)
  })

  it('pageshow persisted=false keeps the held state', async () => {
    await bootAt(`/?state=${STATE}&signin=ready`)
    await act(async () => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: false }))
    })
    const d = onlyDialog()
    expectFields(d)
    await fillAndSubmit(d)
    expect(postedStates()).toEqual([STATE])
    expect(nav.assigned).toEqual([])
  })
})

describe('held state: adversarial', () => {
  it('a retry after a 401 that crosses 9 min posts nothing more and navigates', async () => {
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ error: 'invalid email or password' }), { status: 401 }))
    await bootAt(`/?state=${STATE}&signin=ready`)
    await fillAndSubmit(onlyDialog())
    expect(postedStates()).toEqual([STATE])
    expectFields(onlyDialog())
    await advance(HOLD_MS)
    await act(async () => onlyDialog().querySelector<HTMLButtonElement>('button[type="submit"]')!.click())
    expect(posts(), 'one post only').toHaveLength(1)
    expect(nav.assigned).toEqual([START])
  })

  it('a bfcache restore mid-submit drops the state; no second post', async () => {
    await bootAt(`/?state=${STATE}&signin=ready`)
    await fillAndSubmit(onlyDialog())
    expect(postedStates()).toEqual([STATE])
    await act(async () => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
    })
    const d = onlyDialog()
    expectFields(d)
    await fillAndSubmit(d)
    expect(posts()).toHaveLength(1)
    expect(nav.assigned).toEqual([START])
  })
})
