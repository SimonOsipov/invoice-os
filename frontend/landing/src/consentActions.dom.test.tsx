// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// RED specs (task-562, LAND-05-03, Test-first) — T3-5, T3-6, T3-7, T3-10, T3-11 and
// T3-12, driven through a real App mount (createRoot + act, the Nav.scrollSpy.test.tsx
// precedent; this package has no @testing-library/react).
//
// The document URL is the production host ON PURPOSE. ensureTag's gate needs
// isProductionHost(hostname) and PRODUCTION_HOSTNAMES is ['www.ascomply.com'], exact
// match only (hubspot.ts:9). Under the default localhost URL the gate closes and both
// "Accept loads the tag" and "Reject does not" pass while asserting nothing. A control
// below pins that the environment option actually took effect.
//
// localStorage is INSTALLED, never assumed. Measured on Node 25: the runtime's own
// experimental `localStorage` global shadows jsdom's and resolves to an empty object
// with no getItem/setItem — exactly the "present but its methods throw" case
// consent.ts:48-50 already guards. CI runs Node 22, where jsdom's real one survives, so
// the ambient store is machine-dependent and unusable as an oracle either way. A memory
// store defined on globalThis makes both hosts behave the same; consent.ts reads the
// global at call time (consent.ts:14-20), so nothing in the source needs a seam.
//
// DELIBERATE DEVIATION from the plan's [C-5]: no vi.mock(…, importOriginal) seam for
// ensureTag / clearGaCookies. vi.mock must resolve the module it names, and
// consentActions.ts and gaCookies.ts do not exist yet, so a mock here turns this RED
// into a collection error. The behavioural oracles used instead — the injected gtag
// script element and the cookie jar itself — are strictly stronger than a call count,
// and they keep vi.mock out of frontend/landing/, where it has no precedent.
//
// T3-8 and T3-14 are NOT in this file: see consentActions.revocation.dom.test.ts.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CONSENT_STORAGE_KEY, CONSENT_VERSION, type ConsentStore } from './consent'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

type TestWindow = Window & { dataLayer?: IArguments[]; gtag?: (...args: unknown[]) => void }
type MemoryStore = ConsentStore & { removeItem: (k: string) => void; clear: () => void }

const ID = 'G-E409H76XYY'
const NOTICE = '[aria-label="Cookie notice"]'
const GTAG_SCRIPT = 'script[src^="https://www.googletagmanager.com/"]'

function memoryStorage(): MemoryStore {
  const map = new Map<string, string>()
  return {
    getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
    setItem: (k: string, v: string) => void map.set(k, String(v)),
    removeItem: (k: string) => void map.delete(k),
    clear: () => map.clear(),
  }
}

function cookieNamesInJar(): string[] {
  return document.cookie
    .split(';')
    .map((pair) => pair.split('=')[0]?.trim() ?? '')
    .filter((name) => name.length > 0)
}

function resetJar() {
  for (const name of cookieNamesInJar()) {
    document.cookie = `${name}=; Max-Age=0; path=/`
  }
}

function spyOnConsole() {
  return {
    error: vi.spyOn(console, 'error').mockImplementation(() => undefined),
    warn: vi.spyOn(console, 'warn').mockImplementation(() => undefined),
    log: vi.spyOn(console, 'log').mockImplementation(() => undefined),
    info: vi.spyOn(console, 'info').mockImplementation(() => undefined),
  }
}

function expectNoConsoleCalls(spies: ReturnType<typeof spyOnConsole>) {
  expect(spies.error).not.toHaveBeenCalled()
  expect(spies.warn).not.toHaveBeenCalled()
  expect(spies.log).not.toHaveBeenCalled()
  expect(spies.info).not.toHaveBeenCalled()
}

let container: HTMLDivElement
let root: Root
let store: MemoryStore
let consoleSpies: ReturnType<typeof spyOnConsole>
let originalStorage: PropertyDescriptor | undefined

beforeEach(() => {
  document.head.innerHTML = ''
  const w = window as TestWindow
  delete w.dataLayer
  delete w.gtag
  resetJar()

  originalStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  store = memoryStorage()
  Object.defineProperty(globalThis, 'localStorage', {
    value: store,
    configurable: true,
    writable: true,
    enumerable: true,
  })

  // Mandatory pair: analytics.ts's module-level `loaded` flag and jsdom's document
  // both survive between cases otherwise, and a later case inherits an earlier one's
  // tag (analytics.dom.test.ts:2-6).
  vi.resetModules()
  vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)

  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  consoleSpies = spyOnConsole()
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  resetJar()
  if (originalStorage) Object.defineProperty(globalThis, 'localStorage', originalStorage)
  else delete (globalThis as { localStorage?: unknown }).localStorage
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

async function mountApp(): Promise<void> {
  const mod = (await import('./App')) as { default: () => ReturnType<typeof createElement> }
  await act(async () => {
    root.render(createElement(mod.default))
  })
}

function noticeNodes(): NodeListOf<Element> {
  return document.querySelectorAll(NOTICE)
}

async function clickConsent(choice: 'accept' | 'reject'): Promise<void> {
  const button = document.querySelector<HTMLButtonElement>(`[data-consent="${choice}"]`)
  expect(button, `expected the mounted notice to render a ${choice} button`).not.toBeNull()
  await act(async () => {
    button!.click()
  })
}

async function clickByText(text: string): Promise<void> {
  const button = Array.from(document.querySelectorAll('button')).find((b) => b.textContent?.trim() === text)
  expect(button, `expected a button labelled "${text}"`).toBeDefined()
  await act(async () => {
    button!.click()
  })
}

// The modal close control carries an icon, not text: SignInModal.tsx names it with
// aria-label only.
async function clickBySelector(selector: string): Promise<void> {
  const button = document.querySelector<HTMLButtonElement>(selector)
  expect(button, `expected a control matching ${selector}`).not.toBeNull()
  await act(async () => {
    button!.click()
  })
}

function storedRecord(): Record<string, unknown> {
  const raw = store.getItem(CONSENT_STORAGE_KEY)
  expect(raw, `nothing stored under ${CONSENT_STORAGE_KEY}`).not.toBeNull()
  return JSON.parse(raw!) as Record<string, unknown>
}

describe('environment controls', () => {
  it('control: the document URL is the allowlisted production host', () => {
    // Without this the whole file can go green while ensureTag never runs.
    expect(window.location.hostname).toBe('www.ascomply.com')
  })

  it('control: the installed store round-trips, so a seed and a read-back mean something', () => {
    expect(window.localStorage).toBe(store)
    store.setItem('probe', 'value')
    expect(store.getItem('probe')).toBe('value')
    expect(store.getItem('absent')).toBeNull()
  })

  it('control: the console spies detect a call', () => {
    console.error('x')
    console.warn('x')
    console.log('x')
    console.info('x')
    expect(consoleSpies.error).toHaveBeenCalledTimes(1)
    expect(consoleSpies.warn).toHaveBeenCalledTimes(1)
    expect(consoleSpies.log).toHaveBeenCalledTimes(1)
    expect(consoleSpies.info).toHaveBeenCalledTimes(1)
  })
})

describe('T3-5: nothing is stored before a choice', () => {
  it('AC-1: the notice is on screen and the store is untouched', async () => {
    const setItem = vi.spyOn(store, 'setItem')
    await mountApp()

    expect(noticeNodes().length, 'expected the notice to mount on an empty store').toBe(1)
    expect(setItem, 'something wrote to the store before a choice').not.toHaveBeenCalled()
    expect(store.getItem(CONSENT_STORAGE_KEY)).toBeNull()
    expectNoConsoleCalls(consoleSpies)
  })
})

describe('T3-6: Accept persists, then loads the tag', () => {
  it('AC-2: the record is granted and the gtag script is injected once', async () => {
    await mountApp()
    expect(document.querySelectorAll(GTAG_SCRIPT).length, 'a tag loaded before any choice').toBe(0)

    await clickConsent('accept')

    const record = storedRecord()
    expect(record.analytics).toBe(true)
    expect(record.v).toBe(CONSENT_VERSION)
    expect(typeof record.ts).toBe('string')
    expect(Number.isFinite(Date.parse(record.ts as string)), `unparseable ts: ${String(record.ts)}`).toBe(true)

    // ensureTag ran with the live hostname: its gate needs isProductionHost, so an
    // injected script IS the call, observed rather than counted.
    const scripts = document.querySelectorAll<HTMLScriptElement>(GTAG_SCRIPT)
    expect(scripts.length, 'expected exactly one gtag script').toBe(1)
    expect(scripts[0].src).toContain(ID)

    expect(noticeNodes().length, 'the notice stayed up after a choice').toBe(0)
    expectNoConsoleCalls(consoleSpies)
  })
})

describe('T3-7: Reject persists, never loads, and clears', () => {
  it('AC-3: the record is denied, no tag is injected, and the GA cookies go', async () => {
    document.cookie = '_ga=GA1.1.1234567890.1700000000; path=/'
    document.cookie = '_ga_E409H76XYY=GS1.1.1700000000.1.1.1700000123.0.0.0; path=/'
    document.cookie = 'hs_keep=stay; path=/'
    expect(cookieNamesInJar().sort(), 'seed did not land').toEqual(['_ga', '_ga_E409H76XYY', 'hs_keep'])

    await mountApp()
    await clickConsent('reject')

    const record = storedRecord()
    expect(record.analytics).toBe(false)
    expect(record.v).toBe(CONSENT_VERSION)

    expect(document.querySelectorAll(GTAG_SCRIPT).length, 'Reject loaded the tag').toBe(0)
    expect((window as TestWindow).dataLayer, 'Reject created a dataLayer').toBeUndefined()

    const left = cookieNamesInJar()
    expect(left, 'the _ga cookie survived Reject').not.toContain('_ga')
    expect(left, 'the _ga_ container cookie survived Reject').not.toContain('_ga_E409H76XYY')
    expect(left, 'an unrelated cookie was collateral damage').toContain('hs_keep')

    expect(noticeNodes().length, 'the notice stayed up after a choice').toBe(0)
    expectNoConsoleCalls(consoleSpies)
  })
})

describe('T3-10: a stored record suppresses the notice on mount', () => {
  it('AC-7: a valid record hides it, an unreadable one brings it back', async () => {
    store.setItem(
      CONSENT_STORAGE_KEY,
      JSON.stringify({ analytics: true, ts: '2026-01-01T00:00:00.000Z', v: CONSENT_VERSION }),
    )
    await mountApp()
    expect(noticeNodes().length, 'a stored record did not suppress the notice').toBe(0)

    await act(async () => root.unmount())
    container.remove()
    container = document.createElement('div')
    document.body.appendChild(container)
    root = createRoot(container)
    vi.resetModules()

    // A record from a superseded version is not a choice: parseConsent rejects it.
    store.setItem(CONSENT_STORAGE_KEY, JSON.stringify({ analytics: true, ts: '', v: 0 }))
    await mountApp()
    expect(noticeNodes().length, 'a v:0 record must not count as a stored choice').toBe(1)
    expectNoConsoleCalls(consoleSpies)
  })
})

describe('T3-11: inert tracks the modal state', () => {
  it('AC-13: the notice is inert while the sign-in modal is open, and not before or after', async () => {
    await mountApp()
    const card = document.querySelector(NOTICE)
    expect(card, 'expected the notice to mount').not.toBeNull()
    expect(card!.hasAttribute('inert'), 'the notice was inert with no modal open').toBe(false)

    await clickByText('Explore the platform')
    expect(document.querySelectorAll('[role="dialog"]').length, 'the sign-in modal did not open').toBe(1)
    expect(document.querySelector(NOTICE)!.hasAttribute('inert'), 'the notice is reachable under the scrim').toBe(true)

    await clickBySelector('button[aria-label="Close"]')
    expect(document.querySelectorAll('[role="dialog"]').length, 'the sign-in modal did not close').toBe(0)
    expect(document.querySelector(NOTICE)!.hasAttribute('inert'), 'the notice stayed inert after close').toBe(false)

    expectNoConsoleCalls(consoleSpies)
  })
})
