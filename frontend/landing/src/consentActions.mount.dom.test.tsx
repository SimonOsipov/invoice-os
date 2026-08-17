// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// Adversarial mount coverage added at QA: the states a visitor can reach that the
// happy-path specs do not — a choice made under an open modal, a double click, and a
// stored record from a version this build does not understand. Same setup contract as
// consentActions.dom.test.tsx: production URL so ensureTag's gate can open, an
// installed memory store because the ambient localStorage is machine-dependent.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CONSENT_STORAGE_KEY, CONSENT_VERSION, type ConsentStore } from './consent'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

type TestWindow = Window & { dataLayer?: IArguments[]; gtag?: (...args: unknown[]) => void }
type MemoryStore = ConsentStore & { calls: () => number }

const ID = 'G-E409H76XYY'
const NOTICE = '[aria-label="Cookie notice"]'
const GTAG_SCRIPT = 'script[src^="https://www.googletagmanager.com/"]'

function memoryStorage(): MemoryStore {
  const map = new Map<string, string>()
  let writes = 0
  return {
    getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
    setItem: (k: string, v: string) => {
      writes += 1
      map.set(k, String(v))
    },
    calls: () => writes,
  }
}

let container: HTMLDivElement
let root: Root
let store: MemoryStore
let originalStorage: PropertyDescriptor | undefined
let consoleError: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  document.head.innerHTML = ''
  const w = window as TestWindow
  delete w.dataLayer
  delete w.gtag

  originalStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  store = memoryStorage()
  Object.defineProperty(globalThis, 'localStorage', { value: store, configurable: true, writable: true })

  vi.resetModules()
  vi.stubEnv('VITE_GA_MEASUREMENT_ID', ID)
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
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

function consentButton(choice: 'accept' | 'reject'): HTMLButtonElement | null {
  return document.querySelector<HTMLButtonElement>(`[data-consent="${choice}"]`)
}

async function clickByText(text: string): Promise<void> {
  const button = Array.from(document.querySelectorAll('button')).find((b) => b.textContent?.trim() === text)
  expect(button, `expected a button labelled "${text}"`).toBeDefined()
  await act(async () => {
    button!.click()
  })
}

describe('a choice made while a modal is open', () => {
  it('still applies, leaves the modal open, and takes the notice down with it', async () => {
    // inert removes the card from the tab order and from hit-testing in a browser, but
    // the state machine must not depend on that: if a choice arrives anyway, it must
    // land cleanly rather than desync the two.
    await mountApp()
    await clickByText('Explore the platform')
    expect(document.querySelectorAll('[role="dialog"]').length, 'the sign-in modal did not open').toBe(1)

    const accept = consentButton('accept')
    expect(accept, 'the notice must stay mounted under the scrim, not unmount').not.toBeNull()
    expect(document.querySelector(NOTICE)!.hasAttribute('inert')).toBe(true)

    await act(async () => {
      accept!.click()
    })

    expect(document.querySelectorAll(NOTICE).length, 'the notice survived a choice').toBe(0)
    expect(document.querySelectorAll('[role="dialog"]').length, 'the choice closed the modal').toBe(1)
    expect(JSON.parse(store.getItem(CONSENT_STORAGE_KEY)!).analytics).toBe(true)
    expect(document.querySelectorAll(GTAG_SCRIPT).length).toBe(1)
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('a double click on Accept', () => {
  it('injects and configures once, whatever the store write count', async () => {
    // Measured at QA: two clicks dispatched inside ONE task each reach applyChoice,
    // because React has not re-rendered the unmount between them, so writeConsent runs
    // twice and the stored `ts` is the second click's. Harmless — same answer, same
    // shape, and ensureTag is idempotent — and unreachable from a real pointer, where
    // the two clicks are separate tasks and the second hits a detached node (below).
    // What must hold is the outcome, not the write count, so that is what is pinned.
    await mountApp()
    const accept = consentButton('accept')!
    await act(async () => {
      accept.click()
      accept.click()
    })

    const record = JSON.parse(store.getItem(CONSENT_STORAGE_KEY)!) as Record<string, unknown>
    expect(record.analytics).toBe(true)
    expect(record.v).toBe(CONSENT_VERSION)
    expect(Number.isFinite(Date.parse(record.ts as string))).toBe(true)

    expect(document.querySelectorAll(GTAG_SCRIPT).length, 'the tag was injected twice').toBe(1)
    // js + config, once. A second ensureTag that did not short-circuit would push
    // another pair and double-count the property.
    expect(((window as TestWindow).dataLayer ?? []).length, 'the property was configured twice').toBe(2)
    expect(document.querySelectorAll(NOTICE).length).toBe(0)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('a click on the detached button after the notice unmounts changes nothing', async () => {
    await mountApp()
    const reject = consentButton('reject')!
    await act(async () => {
      reject.click()
    })
    expect(document.querySelectorAll(NOTICE).length).toBe(0)
    const after = store.calls()

    await act(async () => {
      reject.click()
    })
    expect(store.calls(), 'a detached node still reached the store').toBe(after)
    expect(document.querySelectorAll(GTAG_SCRIPT).length).toBe(0)
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('a stored record this build does not understand', () => {
  it.each([
    ['a future version', { analytics: true, ts: '2026-01-01T00:00:00.000Z', v: CONSENT_VERSION + 1 }],
    ['a missing version', { analytics: true, ts: '2026-01-01T00:00:00.000Z' }],
    ['a non-boolean answer', { analytics: 'true', ts: '', v: CONSENT_VERSION }],
    ['a bare null', null],
  ])('%s is not a choice: the notice returns and nothing is written or loaded', async (_label, stored) => {
    store.setItem(CONSENT_STORAGE_KEY, JSON.stringify(stored))
    const before = store.calls()

    await mountApp()

    expect(document.querySelectorAll(NOTICE).length, 'an unreadable record suppressed the notice').toBe(1)
    expect(store.calls(), 'mounting rewrote the store before a choice').toBe(before)
    expect(document.querySelectorAll(GTAG_SCRIPT).length, 'an unreadable record loaded the tag').toBe(0)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('unparseable JSON is not a choice either', async () => {
    store.setItem(CONSENT_STORAGE_KEY, '{not json')
    await mountApp()
    expect(document.querySelectorAll(NOTICE).length).toBe(1)
    expect(document.querySelectorAll(GTAG_SCRIPT).length).toBe(0)
    expect(consoleError).not.toHaveBeenCalled()
  })
})
