// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// T3-5, T3-6, T3-7, T3-10, T3-11 and T3-12, driven through a real App mount (createRoot + act, the Nav.scrollSpy.test.tsx
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
// ensureTag / clearGaCookies. The behavioural oracles used instead — the injected gtag
// script element and the cookie jar itself — are stronger than a call count, and they
// keep vi.mock out of frontend/landing/, where it has no precedent.
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

// T4-6 and T4-11 (task-563). The footer control is the ONLY way to reach the notice once a
// record is stored, so both cases start from a granted record and go through the footer.
describe('T4-6: the footer control reopens the notice with the stored setting', () => {
  it('AC-9: a granted record hides it, Cookie choices brings it back showing "on", and a choice closes it', async () => {
    store.setItem(
      CONSENT_STORAGE_KEY,
      JSON.stringify({ analytics: true, ts: '2026-01-01T00:00:00.000Z', v: CONSENT_VERSION }),
    )
    await mountApp()
    expect(noticeNodes().length, 'a stored record did not suppress the notice').toBe(0)

    await clickByText('Cookie choices')
    expect(noticeNodes().length, 'the footer control did not reopen the notice').toBe(1)
    expect(
      document.querySelector(NOTICE)!.textContent,
      'the reopened notice does not show the stored setting',
    ).toContain('Analytics cookies are on.')

    // Without setReopened(false) in onChoose the notice never closes again: consent is
    // already non-null, so the mount condition stays true on `reopened` alone.
    await clickConsent('accept')
    expect(noticeNodes().length, 'the reopened notice stayed up after a choice').toBe(0)
    expectNoConsoleCalls(consoleSpies)
  })
})

describe('T4-11: Reject from a reopened notice persists and closes', () => {
  it('AC-9: the notice goes away and the store holds a denied record', async () => {
    store.setItem(
      CONSENT_STORAGE_KEY,
      JSON.stringify({ analytics: true, ts: '2026-01-01T00:00:00.000Z', v: CONSENT_VERSION }),
    )
    await mountApp()
    expect(noticeNodes().length).toBe(0)

    await clickByText('Cookie choices')
    expect(noticeNodes().length, 'the footer control did not reopen the notice').toBe(1)

    await clickConsent('reject')
    expect(noticeNodes().length, 'the reopened notice stayed up after Reject').toBe(0)

    const record = storedRecord()
    expect(record.analytics, 'Reject from a reopened notice did not persist').toBe(false)
    expect(record.v).toBe(CONSENT_VERSION)
    expectNoConsoleCalls(consoleSpies)
  })
})

// Adversarial coverage (QA, task-563). T4-6/T4-11 walk the happy round trip; these are the
// ways the control can be reached that the round trip never touches.

function cookieChoicesControl(): HTMLButtonElement {
  const button = Array.from(document.querySelectorAll('button')).find(
    (b) => b.textContent?.trim() === 'Cookie choices',
  )
  expect(button, 'expected the footer Cookie choices control to be mounted').toBeDefined()
  return button as HTMLButtonElement
}

describe('the Cookie choices control under repeated and out-of-order use', () => {
  const granted = () =>
    store.setItem(
      CONSENT_STORAGE_KEY,
      JSON.stringify({ analytics: true, ts: '2026-01-01T00:00:00.000Z', v: CONSENT_VERSION }),
    )

  it('clicking it twice reopens once, not twice, and writes nothing', async () => {
    granted()
    await mountApp()
    const setItem = vi.spyOn(store, 'setItem')

    await clickByText('Cookie choices')
    await clickByText('Cookie choices')

    expect(noticeNodes().length, 'a second click stacked a second notice').toBe(1)
    expect(setItem, 'reopening the notice wrote to the store').not.toHaveBeenCalled()
    expectNoConsoleCalls(consoleSpies)
  })

  it('clicking it with the notice already open changes nothing and stores nothing', async () => {
    await mountApp()
    const setItem = vi.spyOn(store, 'setItem')
    expect(noticeNodes().length, 'an empty store should already show the notice').toBe(1)
    // No record stored, so `consent === null` already holds the notice open; the control
    // sets `reopened` on top of that and must be a no-op rather than a remount.
    expect(document.querySelector(NOTICE)!.querySelector('.cn-setting'), 'a setting line with no record').toBeNull()

    await clickByText('Cookie choices')

    expect(noticeNodes().length).toBe(1)
    expect(document.querySelector(NOTICE)!.querySelector('.cn-setting'), 'a setting line appeared from nowhere').toBeNull()
    expect(setItem).not.toHaveBeenCalled()
    expect(store.getItem(CONSENT_STORAGE_KEY)).toBeNull()
    expectNoConsoleCalls(consoleSpies)
  })

  it('clicking it while a modal is open reopens the notice INERT, and the close un-inerts it', async () => {
    granted()
    await mountApp()

    await clickByText('Explore the platform')
    expect(document.querySelectorAll('[role="dialog"]').length, 'the sign-in modal did not open').toBe(1)

    await clickByText('Cookie choices')
    const card = document.querySelector(NOTICE)
    expect(card, 'the control did not reopen the notice while a modal was open').not.toBeNull()
    // Same contract as T3-11: a notice that mounts under an open scrim must not be reachable.
    expect(card!.hasAttribute('inert'), 'the reopened notice is reachable under the scrim').toBe(true)

    await clickBySelector('button[aria-label="Close"]')
    expect(document.querySelector(NOTICE)!.hasAttribute('inert'), 'it stayed inert after the modal closed').toBe(false)
    expectNoConsoleCalls(consoleSpies)
  })

  it('it survives a choice: the control is still there after the notice closes again', async () => {
    granted()
    await mountApp()
    await clickByText('Cookie choices')
    await clickConsent('reject')

    expect(noticeNodes().length).toBe(0)
    // A control that vanishes after use would strand the visitor on their last answer.
    expect(cookieChoicesControl().isConnected, 'the control unmounted with the notice').toBe(true)
    await clickByText('Cookie choices')
    expect(noticeNodes().length, 'the control stopped working after one use').toBe(1)
    expect(document.querySelector(NOTICE)!.textContent, 'the reopened notice shows a stale setting').toContain(
      'Analytics cookies are off.',
    )
    expectNoConsoleCalls(consoleSpies)
  })
})

describe('the Cookie choices control is reachable by keyboard and assistive technology', () => {
  it('its accessible name survives the wrapper, and nothing on the ancestor chain hides it', async () => {
    await mountApp()
    const control = cookieChoicesControl()

    // Control needle: the DOM really did resolve a distinct sibling to compare against.
    const sibling = Array.from(document.querySelectorAll('button')).find(
      (b) => b.textContent?.trim() === 'Book a demo',
    )
    expect(sibling, 'control: the Book a demo sibling is missing').toBeDefined()
    expect(sibling).not.toBe(control)

    expect(control.textContent?.trim()).toBe('Cookie choices')
    expect(control.getAttribute('aria-label'), 'an aria-label overrides the visible name').toBeNull()
    expect(control.getAttribute('aria-labelledby')).toBeNull()
    expect(control.getAttribute('title')).toBeNull()
    expect(control.disabled, 'the control ships disabled beside an enabled sibling').toBe(false)
    expect(control.closest('[aria-hidden="true"]'), 'an ancestor hides the control from AT').toBeNull()
    expect(control.closest('[inert]'), 'an ancestor makes the control unreachable').toBeNull()
    expect(control.closest('footer'), 'the control left the footer').not.toBeNull()
  })

  it('it takes focus, and the footer tab order runs Book a demo then Cookie choices', async () => {
    await mountApp()
    const control = cookieChoicesControl()

    expect(control.tabIndex, 'the control was taken out of the tab order').toBe(0)
    control.focus()
    expect(document.activeElement, 'the control cannot take focus').toBe(control)

    const footer = document.querySelector('footer')!
    const focusable = Array.from(footer.querySelectorAll<HTMLElement>('a[href], button')).filter(
      (el) => el.tabIndex >= 0,
    )
    expect(focusable.length, 'control: the footer exposes no focusable elements').toBeGreaterThan(1)
    expect(focusable[focusable.length - 1], 'the control is not the last footer tab stop').toBe(control)
    expectNoConsoleCalls(consoleSpies)
  })
})
