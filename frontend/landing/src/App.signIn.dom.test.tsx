// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// F-2: the nav's "Explore the platform" control opens the sign-in modal specifically,
// without navigating away, and dismissing it restores the page. Same setup contract as
// consentActions.mount.dom.test.tsx: production URL, an installed memory localStorage,
// a console.error spy asserted empty. "Explore the platform" also renders in Hero
// (Hero.tsx:42), so the control is reached scoped to `header`, never by an unscoped find.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { ConsentStore } from './consent'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const SIGN_IN_CTA = 'Explore the platform'
const DIALOG = '[role="dialog"]'

function memoryStorage(): ConsentStore {
  const map = new Map<string, string>()
  return {
    getItem: (k: string) => (map.has(k) ? map.get(k)! : null),
    setItem: (k: string, v: string) => {
      map.set(k, String(v))
    },
  }
}

let container: HTMLDivElement
let root: Root
let originalStorage: PropertyDescriptor | undefined
let consoleError: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  originalStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  Object.defineProperty(globalThis, 'localStorage', { value: memoryStorage(), configurable: true, writable: true })

  vi.resetModules()
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
  vi.restoreAllMocks()
})

async function mountApp(): Promise<void> {
  const mod = (await import('./App')) as { default: () => ReturnType<typeof createElement> }
  await act(async () => {
    root.render(createElement(mod.default))
  })
}

// Local, unexported copy per Decisions -> [click-by-text-duplicated]: the two existing
// copies (consentActions.mount.dom.test.tsx, consentActions.dom.test.tsx) query
// `document` and cannot express "the nav one, not the hero one". The `root` parameter
// is the one difference.
async function clickByText(root: ParentNode, text: string): Promise<void> {
  const button = Array.from(root.querySelectorAll('button')).find((b) => b.textContent?.trim() === text)
  expect(button, `expected a button labelled "${text}" within the given scope`).toBeDefined()
  await act(async () => {
    button!.click()
  })
}

describe('F-2: the sign-in control opens the sign-in modal', () => {
  it('F2-a: control needle -- at rest zero dialogs, and header holds at least one button', async () => {
    await mountApp()
    expect(document.querySelectorAll(DIALOG).length).toBe(0)
    expect(document.querySelectorAll('header button').length).toBeGreaterThan(0)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('F2-b: exactly one "Explore the platform" in header, exactly two on the page', async () => {
    await mountApp()
    const header = document.querySelector('header')!
    const inHeader = Array.from(header.querySelectorAll('button')).filter((b) => b.textContent?.trim() === SIGN_IN_CTA)
    const onPage = Array.from(document.querySelectorAll('button')).filter((b) => b.textContent?.trim() === SIGN_IN_CTA)
    expect(inHeader.length, 'nav CTA missing or duplicated').toBe(1)
    expect(onPage.length, 'expected nav + hero copies').toBe(2)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('F2-c/d/e: clicking opens the Sign-in dialog, does not navigate, and Close restores the page', async () => {
    await mountApp()
    const header = document.querySelector('header')!
    const sectionsBefore = document.querySelectorAll('section[id]').length
    const buttonsBefore = document.querySelectorAll('button').length
    const pathBefore = window.location.pathname

    await clickByText(header, SIGN_IN_CTA)

    // F2-c: exactly one dialog, and it is the Sign-in one -- not Book a demo.
    expect(document.querySelectorAll(DIALOG).length).toBe(1)
    const dialog = document.querySelector(DIALOG)!
    expect(dialog.getAttribute('aria-label')).toBe('Sign in')

    // F2-d: no navigation.
    expect(window.location.pathname).toBe(pathBefore)
    expect(document.querySelector('#pricing')).not.toBeNull()

    // F2-e: Close leaves zero dialogs and restores the pre-open snapshot.
    const closeButton = dialog.querySelector<HTMLButtonElement>('button[aria-label="Close"]')
    expect(closeButton, 'expected the modal Close control').not.toBeNull()
    await act(async () => {
      closeButton!.click()
    })
    expect(document.querySelectorAll(DIALOG).length).toBe(0)
    expect(document.querySelectorAll('section[id]').length).toBe(sectionsBefore)
    expect(document.querySelectorAll('button').length).toBe(buttonsBefore)
    expect(consoleError).not.toHaveBeenCalled()
  })

  // QA addition: a one-shot handler (e.g. a stale-closure effect, or state that never
  // resets) would satisfy every assertion above and still be broken for a returning
  // visitor. Re-open after the first close and require the same dialog again.
  it('F2-f: the control still opens the dialog after a prior open/close cycle', async () => {
    await mountApp()
    const header = document.querySelector('header')!

    await clickByText(header, SIGN_IN_CTA)
    expect(document.querySelectorAll(DIALOG).length).toBe(1)
    await act(async () => {
      document.querySelector<HTMLButtonElement>(`${DIALOG} button[aria-label="Close"]`)!.click()
    })
    expect(document.querySelectorAll(DIALOG).length).toBe(0)

    await clickByText(header, SIGN_IN_CTA)
    expect(document.querySelectorAll(DIALOG).length).toBe(1)
    expect(document.querySelector(DIALOG)!.getAttribute('aria-label')).toBe('Sign in')
    expect(consoleError).not.toHaveBeenCalled()
  })
})

// The boot URL's `state` and `signin`.
describe('AUTH-05-07: the boot sign-in params', () => {
  const STATE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'
  // Outcome copy.
  const NO_WORKSPACE = 'This account has no workspace yet.'
  const FAILED = "We couldn't open your workspace. Sign in again."

  beforeEach(() => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
    vi.stubEnv('VITE_APP_URL', 'https://app.x')
  })

  afterEach(() => {
    window.history.replaceState(null, '', '/')
    vi.unstubAllEnvs()
    vi.unstubAllGlobals()
  })

  async function bootAt(path: string): Promise<void> {
    window.history.replaceState(null, '', path)
    expect(window.location.pathname + window.location.search).toBe(path)
    await mountApp()
  }

  function dialogAlerts(): HTMLElement[] {
    const d = document.querySelector(DIALOG)
    expect(d, 'expected the sign-in dialog').not.toBeNull()
    return Array.from(d!.querySelectorAll<HTMLElement>('[role="alert"]'))
  }

  it('a no-workspace outcome opens the modal with its message', async () => {
    await bootAt('/?signin=no-workspace')
    expect(document.querySelectorAll(DIALOG).length).toBe(1)
    expect(document.querySelector(DIALOG)!.getAttribute('aria-label')).toBe('Sign in')
    const got = dialogAlerts()
    expect(got.length).toBe(1)
    expect(got[0].textContent).toContain(NO_WORKSPACE)
    expect(window.location.search).toBe('')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('a failed outcome opens the modal with its message', async () => {
    await bootAt('/?signin=failed')
    expect(document.querySelectorAll(DIALOG).length).toBe(1)
    const got = dialogAlerts()
    expect(got.length).toBe(1)
    expect(got[0].textContent).toContain(FAILED)
    expect(got[0].textContent).not.toContain(NO_WORKSPACE)
    expect(window.location.search).toBe('')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('an unknown outcome is stripped and opens nothing', async () => {
    await bootAt('/?signin=x')
    // Positive half: the page itself mounted.
    expect(document.querySelectorAll('header button').length).toBeGreaterThan(0)
    expect(document.querySelectorAll(DIALOG).length).toBe(0)
    expect(window.location.search).toBe('')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('a boot state is held and stripped', async () => {
    const setLocal = vi.spyOn(localStorage, 'setItem')
    const fetchMock = vi.fn().mockReturnValue(new Promise(() => undefined))
    vi.stubGlobal('fetch', fetchMock)
    await bootAt(`/?state=${STATE}&signin=ready&verify=failed`)

    expect(document.querySelectorAll(DIALOG).length).toBe(1)
    const d = document.querySelector<HTMLElement>(DIALOG)!
    expect(dialogAlerts().length).toBe(0)
    const email = d.querySelectorAll<HTMLInputElement>('input[type="email"]')
    const password = d.querySelectorAll<HTMLInputElement>('input[type="password"]')
    expect(email.length).toBe(1)
    expect(password.length).toBe(1)
    expect(d.textContent).not.toContain('Continue with email')
    expect(window.location.search).toBe('?verify=failed')

    // Held in memory: the submit carries it.
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
    await act(async () => {
      setValue.call(email[0], 'ada@okafor.ng')
      email[0].dispatchEvent(new Event('input', { bubbles: true }))
      setValue.call(password[0], 'pw')
      password[0].dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => {
      d.querySelector<HTMLButtonElement>('button[type="submit"]')!.click()
    })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect((JSON.parse(init.body as string) as { state: unknown }).state).toBe(STATE)

    // Never written to storage.
    for (const call of setLocal.mock.calls) expect(String(call[1])).not.toContain(STATE)
    for (let i = 0; i < sessionStorage.length; i++) {
      expect(sessionStorage.getItem(sessionStorage.key(i)!) ?? '').not.toContain(STATE)
    }
    expect(document.cookie).not.toContain(STATE)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('a malformed boot state is ignored', async () => {
    await bootAt('/?state=short&signin=ready')
    expect(document.querySelectorAll(DIALOG).length).toBe(1)
    const d = document.querySelector<HTMLElement>(DIALOG)!
    const cont = Array.from(d.querySelectorAll('button')).filter((b) => b.textContent?.trim() === 'Continue with email')
    expect(cont.length).toBe(1)
    expect(d.querySelectorAll('input').length).toBe(0)
    expect(dialogAlerts().length).toBe(0)
    expect(window.location.search).toBe('')
    expect(consoleError).not.toHaveBeenCalled()
  })
})
