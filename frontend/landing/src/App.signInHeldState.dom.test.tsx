// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// The held boot state expires 9 min after boot, and a bfcache restore drops it.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const DIALOG = '[role="dialog"]'
const STATE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'
const CONTINUE = 'Continue with email'
const HOLD_MS = 9 * 60 * 1000
const T0 = new Date('2026-09-25T12:00:00Z').getTime()

let container: HTMLDivElement
let root: Root
let fetchMock: ReturnType<typeof vi.fn>

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
  fetchMock = vi.fn().mockReturnValue(new Promise(() => undefined))
  vi.stubGlobal('fetch', fetchMock)
  vi.resetModules()
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
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
  const b = Array.from(document.querySelectorAll('header button')).find((x) => x.textContent?.trim() === 'Explore the platform')
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

// Continue with email shown, no fields.
function expectContinue(d: HTMLElement): void {
  expect(continueButtons(d).length, 'Continue with email buttons').toBe(1)
  expect(d.querySelectorAll('input').length, 'form inputs').toBe(0)
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

function postedStates(): unknown[] {
  return fetchMock.mock.calls.map((c) => (JSON.parse((c[1] as RequestInit).body as string) as { state: unknown }).state)
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

  it('9 min after boot, opening the modal shows Continue with email and posts nothing', async () => {
    await bootAt(`/?state=${STATE}`)
    await advance(HOLD_MS)
    await openFromNav()
    expectContinue(onlyDialog())
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('a submit 9 min after boot never posts the stale state and shows Continue with email', async () => {
    await bootAt(`/?state=${STATE}&signin=ready`)
    const d = onlyDialog()
    expectFields(d)
    await advance(HOLD_MS - 1)
    expectFields(onlyDialog())
    await advance(1)
    // A submit at expiry, whether the fields are still on screen or not.
    const submit = onlyDialog().querySelector<HTMLButtonElement>('button[type="submit"]')
    if (submit) await fillAndSubmit(onlyDialog())
    expect(fetchMock, 'a sign-in post with the stale state').not.toHaveBeenCalled()
    expectContinue(onlyDialog())
  })

  it('an expired state stays dropped across a close and reopen', async () => {
    await bootAt(`/?state=${STATE}&signin=ready`)
    expectFields(onlyDialog())
    await closeDialog()
    await advance(HOLD_MS)
    await openFromNav()
    expectContinue(onlyDialog())
    await closeDialog()
    await advance(60 * 1000)
    await openFromNav()
    expectContinue(onlyDialog())
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('F3: a bfcache restore drops the held state', () => {
  it('pageshow persisted=true swaps the fields for Continue with email', async () => {
    await bootAt(`/?state=${STATE}&signin=ready`)
    expectFields(onlyDialog())
    await act(async () => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
    })
    expectContinue(onlyDialog())
    await closeDialog()
    await openFromNav()
    expectContinue(onlyDialog())
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('pageshow persisted=true with the modal closed: the next open shows Continue with email', async () => {
    await bootAt(`/?state=${STATE}`)
    await act(async () => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
    })
    await openFromNav()
    expectContinue(onlyDialog())
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
  })
})

describe('held state: adversarial', () => {
  it('a retry after a 401 that crosses 9 min posts nothing and shows Continue with email', async () => {
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ error: 'invalid email or password' }), { status: 401 }))
    await bootAt(`/?state=${STATE}&signin=ready`)
    await fillAndSubmit(onlyDialog())
    expect(postedStates()).toEqual([STATE])
    expectFields(onlyDialog())
    await advance(HOLD_MS)
    await act(async () => onlyDialog().querySelector<HTMLButtonElement>('button[type="submit"]')!.click())
    expect(fetchMock, 'one post only').toHaveBeenCalledTimes(1)
    expectContinue(onlyDialog())
  })

  it('a bfcache restore mid-submit drops the state; no second post', async () => {
    await bootAt(`/?state=${STATE}&signin=ready`)
    await fillAndSubmit(onlyDialog())
    expect(postedStates()).toEqual([STATE])
    await act(async () => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
    })
    expectContinue(onlyDialog())
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
