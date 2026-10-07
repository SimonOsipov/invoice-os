// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// F-2: the nav's "Sign in" control opens the sign-in modal specifically (the hero has no sign-in control),
// without navigating away, and dismissing it restores the page. Same setup contract as
// consentActions.mount.dom.test.tsx: production URL, an installed memory localStorage,
// a console.error spy asserted empty. The control is reached scoped to `header`.
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { ConsentStore } from './consent'
import { captureNavigation } from './navigation.test.util'
import { PREFLIGHT_MS, SIGN_IN_UNAVAILABLE } from './signIn'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const SIGN_IN_CTA = 'Sign in'
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
// `document` and cannot express "the nav one". The `root` parameter
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

  it('landing_hasNoPlatformLoginText', async () => {
    window.history.replaceState(null, '', `/?state=${'A'.repeat(43)}&signin=ready`)
    await mountApp()
    await act(async () => {
      document.querySelector<HTMLButtonElement>('header button.a-burger')!.click()
    })
    expect(document.querySelector('.a-menu'), 'control: menu open').not.toBeNull()
    expect(document.body.textContent).not.toMatch(/platform login/i)
  })

  it('F2-b / HD-16: exactly one "Sign in" in header and on the page, and no retired trigger label', async () => {
    await mountApp()
    const header = document.querySelector('header')!
    const inHeader = Array.from(header.querySelectorAll('button')).filter((b) => b.textContent?.trim() === SIGN_IN_CTA)
    const onPage = Array.from(document.querySelectorAll('button')).filter((b) => b.textContent?.trim() === SIGN_IN_CTA)
    const oldLabel = Array.from(header.querySelectorAll('button')).filter(
      (b) => b.textContent?.trim() === 'Explore the platform',
    )
    expect(inHeader.length, 'nav CTA missing or duplicated').toBe(1)
    expect(onPage.length, 'expected the header copy only').toBe(1)
    expect(oldLabel.length, 'the retired trigger label is still on a header control').toBe(0)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('F2-c/d/e: clicking opens the Sign in dialog, does not navigate, and Close restores the page', async () => {
    await mountApp()
    const header = document.querySelector('header')!
    const sectionsBefore = document.querySelectorAll('section[id]').length
    const buttonsBefore = document.querySelectorAll('button').length
    const pathBefore = window.location.pathname

    await clickByText(header, SIGN_IN_CTA)

    // F2-c: exactly one dialog, and it is the Sign in one -- not Book a demo.
    expect(document.querySelectorAll(DIALOG).length).toBe(1)
    const dialog = document.querySelector(DIALOG)!
    expect(dialog.getAttribute('aria-label')).toBe('Sign in')

    // F2-d: no navigation.
    expect(window.location.pathname).toBe(pathBefore)
    expect(document.querySelector('#faq')).not.toBeNull()

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

describe('F-2 footer: Open the cockpit', () => {
  it('FT-11: the footer Open the cockpit opens the Sign-in dialog and Close leaves none', async () => {
    await mountApp()
    const footer = document.querySelector('footer')!
    expect(document.querySelectorAll(DIALOG).length, 'control: no dialog yet').toBe(0)

    await clickByText(footer, 'Open the cockpit')

    expect(document.querySelectorAll(DIALOG).length).toBe(1)
    const dialog = document.querySelector(DIALOG)!
    expect(dialog.getAttribute('aria-label')).toBe('Sign in')
    await act(async () => {
      dialog.querySelector<HTMLButtonElement>('button[aria-label="Close"]')!.click()
    })
    expect(document.querySelectorAll(DIALOG).length).toBe(0)
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('F-2 menu: sign-in from the burger menu', () => {
  it('F2-g: the menu Sign in opens the Sign-in dialog and the menu is gone behind it', async () => {
    await mountApp()
    const burger = document.querySelector<HTMLButtonElement>('header button.a-burger')
    expect(burger, 'expected the header burger').not.toBeNull()
    await act(async () => {
      burger!.click()
    })
    const menu = document.querySelector('.a-menu')
    expect(menu, 'control: the burger opened the menu').not.toBeNull()
    expect(document.querySelectorAll(DIALOG).length, 'control: no dialog yet').toBe(0)

    await clickByText(menu!, SIGN_IN_CTA)

    expect(document.querySelectorAll(DIALOG).length).toBe(1)
    expect(document.querySelector(DIALOG)!.getAttribute('aria-label')).toBe('Sign in')
    expect(document.querySelector('.a-menu'), 'the menu closes before the modal opens').toBeNull()
    expect(burger!.getAttribute('aria-expanded')).toBe('false')
    expect(consoleError).not.toHaveBeenCalled()
  })
})

// The boot URL's `state` and `signin`.
describe('AUTH-05-07: the boot sign-in params', () => {
  const STATE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'
  // Outcome copy.
  const NO_WORKSPACE = 'This account has no workspace yet. If you were invited, open the invite link in your email.'
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
    await bootAt(`/?state=${STATE}&signin=ready&keep=1`)

    expect(document.querySelectorAll(DIALOG).length).toBe(1)
    const d = document.querySelector<HTMLElement>(DIALOG)!
    expect(dialogAlerts().length).toBe(0)
    const email = d.querySelectorAll<HTMLInputElement>('input[type="email"]')
    const password = d.querySelectorAll<HTMLInputElement>('input[type="password"]')
    expect(email.length).toBe(1)
    expect(password.length).toBe(1)
    expect(d.textContent).not.toContain('Continue with email')
    expect(window.location.search).toBe('?keep=1')

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
    const nav = captureNavigation()
    const fetchMock = vi.fn().mockResolvedValue(new Response(null))
    vi.stubGlobal('fetch', fetchMock)
    await bootAt('/?state=short&signin=ready')
    expect(document.querySelectorAll(DIALOG).length).toBe(1)
    const d = document.querySelector<HTMLElement>(DIALOG)!
    expect(d.textContent).not.toContain('Continue with email')
    expect(d.querySelectorAll('input').length).toBeGreaterThan(0)
    expect(dialogAlerts().length).toBe(0)
    expect(window.location.search).toBe('')

    // No held state: a submit bounces to the app and posts nothing.
    const email = d.querySelector<HTMLInputElement>('input[type="email"]')!
    const password = d.querySelector<HTMLInputElement>('input[type="password"]')!
    const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
    await act(async () => {
      setValue.call(email, 'ada@okafor.ng')
      email.dispatchEvent(new Event('input', { bubbles: true }))
      setValue.call(password, 'pw')
      password.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await act(async () => {
      d.querySelector<HTMLButtonElement>('button[type="submit"]')!.click()
    })
    nav.restore()
    expect(nav.assigned).toEqual(['https://app.x?auth=start'])
    expect(fetchMock.mock.calls.filter((c) => (c[1] as RequestInit | undefined)?.method === 'POST')).toEqual([])
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('the console hand-back', () => {
  const STATE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'
  // D8 copy, verbatim.
  const NOT_STAFF = 'This account cannot open the ASComply consoles.'
  const NO_WORKSPACE = 'This account has no workspace yet. If you were invited, open the invite link in your email.'

  let nav: ReturnType<typeof captureNavigation>
  let assigned: string[]

  beforeEach(() => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
    vi.stubEnv('VITE_APP_URL', 'https://app.x')
    vi.stubEnv('VITE_OPS_URL', 'https://ops.x')
    vi.stubEnv('VITE_SUPPORT_URL', 'https://support.x')
    nav = captureNavigation()
    assigned = nav.assigned
  })

  afterEach(() => {
    nav.restore()
    window.history.replaceState(null, '', '/')
    vi.unstubAllEnvs()
    vi.unstubAllGlobals()
  })

  async function bootAt(path: string): Promise<void> {
    window.history.replaceState(null, '', path)
    await mountApp()
  }

  async function signInFromNav(): Promise<void> {
    await clickByText(document.querySelector('header')!, SIGN_IN_CTA)
    const d = document.querySelector<HTMLElement>(DIALOG)!
    expect(d, 'expected the sign-in dialog').not.toBeNull()
    const email = d.querySelectorAll<HTMLInputElement>('input[type="email"]')
    const password = d.querySelectorAll<HTMLInputElement>('input[type="password"]')
    expect(email.length).toBe(1)
    expect(password.length).toBe(1)
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
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0))
    })
  }

  function codeFetch() {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify({ code: 'the-code' }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    return fetchMock
  }

  it('signIn_returnsTheCodeToTheConsoleThatAsked', async () => {
    const fetchMock = codeFetch()
    await bootAt(`/?state=${STATE}&console=ops`)
    await signInFromNav()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(assigned).toEqual(['https://ops.x?handoff=the-code'])
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('signIn_withoutAConsoleStillOpensTheApp', async () => {
    const fetchMock = codeFetch()
    await bootAt(`/?state=${STATE}`)
    await signInFromNav()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(assigned).toEqual(['https://app.x?handoff=the-code'])
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('boot_notStaffShowsItsMessage', async () => {
    await bootAt('/?signin=not-staff')
    expect(document.querySelectorAll(DIALOG).length).toBe(1)
    expect(document.querySelector(DIALOG)!.getAttribute('aria-label')).toBe('Sign in')
    const alerts = Array.from(document.querySelector(DIALOG)!.querySelectorAll<HTMLElement>('[role="alert"]'))
    expect(alerts.length).toBe(1)
    expect(alerts[0].textContent).toContain(NOT_STAFF)
    expect(alerts[0].textContent).not.toContain(NO_WORKSPACE)
    expect(window.location.search).toBe('')
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('the header click: one way in', () => {
  const STATE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'
  const START = 'https://app.x?auth=start'
  let nav: ReturnType<typeof captureNavigation>

  beforeEach(() => {
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
    vi.stubEnv('VITE_APP_URL', 'https://app.x')
    vi.stubEnv('VITE_OPS_URL', 'https://ops.x')
    nav = captureNavigation()
  })

  afterEach(() => {
    nav.restore()
    vi.useRealTimers()
    window.history.replaceState(null, '', '/')
    vi.unstubAllEnvs()
    vi.unstubAllGlobals()
  })

  async function bootAt(path: string): Promise<void> {
    window.history.replaceState(null, '', path)
    await mountApp()
  }

  const dialogs = () => document.querySelectorAll<HTMLElement>(DIALOG)
  const navSignIn = () => clickByText(document.querySelector('header')!, SIGN_IN_CTA)
  const posts = (m: ReturnType<typeof vi.fn>) => m.mock.calls.filter((c) => (c[1] as RequestInit | undefined)?.method === 'POST')

  function expectFields(d: HTMLElement): void {
    expect(d.querySelectorAll('input[type="email"]').length).toBe(1)
    expect(d.querySelectorAll('input[type="password"]').length).toBe(1)
    expect(d.textContent).not.toContain('Continue with email')
  }

  function expectUnavailable(): void {
    expect(dialogs().length).toBe(1)
    const alerts = Array.from(dialogs()[0].querySelectorAll('[role="alert"]'))
    expect(alerts.map((a) => a.textContent)).toEqual([expect.stringContaining(SIGN_IN_UNAVAILABLE)])
    expectFields(dialogs()[0])
    expect(nav.assigned).toEqual([])
  }

  it('click_noState_navigatesToStartUrl', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null))
    vi.stubGlobal('fetch', fetchMock)
    await bootAt('/')
    await navSignIn()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0][0]).toBe('https://app.x')
    expect(nav.assigned).toEqual([START])
    expect(dialogs().length).toBe(0)
    expect(posts(fetchMock)).toEqual([])
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('click_liveState_opensTheFormWithoutNavigating', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null))
    vi.stubGlobal('fetch', fetchMock)
    await bootAt(`/?state=${STATE}`)
    await navSignIn()
    expect(dialogs().length).toBe(1)
    expectFields(dialogs()[0])
    expect(nav.assigned).toEqual([])
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('click_preflightRejects_opensTheUnavailableWindow', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))
    await bootAt('/')
    await navSignIn()
    expectUnavailable()
  })

  it('click_preflightTimesOut_opensTheUnavailableWindow', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    vi.stubGlobal(
      'fetch',
      vi.fn(
        (_u: string, init: RequestInit) =>
          new Promise((_res, rej) => init.signal!.addEventListener('abort', () => rej(init.signal!.reason))),
      ),
    )
    await bootAt('/')
    await navSignIn()
    expect(dialogs().length, 'control: still waiting').toBe(0)
    await act(async () => {
      vi.advanceTimersByTime(PREFLIGHT_MS)
    })
    expectUnavailable()
  })

  it('click_secondClickWhilePreflighting_isIgnored', async () => {
    let answer!: (r: Response) => void
    const fetchMock = vi.fn(() => new Promise<Response>((r) => (answer = r)))
    vi.stubGlobal('fetch', fetchMock)
    await bootAt('/')
    await navSignIn()
    await navSignIn()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await act(async () => answer(new Response(null)))
    expect(nav.assigned).toEqual([START])
  })

  it('click_afterAFailedPreflight_retries', async () => {
    const fetchMock = vi.fn().mockRejectedValueOnce(new TypeError('Failed to fetch')).mockResolvedValue(new Response(null))
    vi.stubGlobal('fetch', fetchMock)
    await bootAt('/')
    await navSignIn()
    expectUnavailable()
    await act(async () => {
      document.querySelector<HTMLButtonElement>(`${DIALOG} button[aria-label="Close"]`)!.click()
    })
    await navSignIn()
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(nav.assigned).toEqual([START])
  })

  it('click_consoleTarget_noState_bouncesToTheConsole', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null))
    vi.stubGlobal('fetch', fetchMock)
    await bootAt('/?console=ops')
    await navSignIn()
    expect(fetchMock.mock.calls[0][0]).toBe('https://ops.x')
    expect(nav.assigned).toEqual(['https://ops.x?auth=start'])
  })

  it('click_consoleWithNoConsoleUrl_opensTheWindow', async () => {
    vi.stubEnv('VITE_OPS_URL', '')
    const fetchMock = vi.fn().mockResolvedValue(new Response(null))
    vi.stubGlobal('fetch', fetchMock)
    await bootAt('/?console=ops')
    await navSignIn()
    expect(fetchMock).not.toHaveBeenCalled()
    expectUnavailable()
  })

  it('reload_holdsNoState_clickNavigates', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null))
    vi.stubGlobal('fetch', fetchMock)
    await bootAt(`/?state=${STATE}&signin=ready`)
    expect(dialogs().length).toBe(1)
    await act(async () => root.unmount())
    root = createRoot(container)
    vi.resetModules()
    await bootAt('/')
    expect(dialogs().length).toBe(0)
    await navSignIn()
    expect(nav.assigned).toEqual([START])
  })

  it('menuSignIn_noState_navigatesAndClosesTheMenu', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null)))
    await bootAt('/')
    await act(async () => {
      document.querySelector<HTMLButtonElement>('header button.a-burger')!.click()
    })
    await clickByText(document.querySelector('.a-menu')!, SIGN_IN_CTA)
    expect(nav.assigned).toEqual([START])
    expect(document.querySelector('.a-menu')).toBeNull()
    expect(dialogs().length).toBe(0)
  })

  it('menuSignIn_liveState_opensTheForm', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null)))
    await bootAt(`/?state=${STATE}`)
    await act(async () => {
      document.querySelector<HTMLButtonElement>('header button.a-burger')!.click()
    })
    await clickByText(document.querySelector('.a-menu')!, SIGN_IN_CTA)
    expect(dialogs().length).toBe(1)
    expectFields(dialogs()[0])
    expect(document.querySelector('.a-menu')).toBeNull()
    expect(nav.assigned).toEqual([])
  })

  it('click_unconfigured_opensTheUnavailableWindow', async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null))
    vi.stubGlobal('fetch', fetchMock)
    for (const unset of [['VITE_APP_URL'], ['VITE_APP_URL', 'VITE_GATEWAY_URL']]) {
      for (const k of unset) vi.stubEnv(k, '')
      await bootAt('/')
      await navSignIn()
      expect(dialogs().length, unset.join()).toBe(1)
      expect(dialogs()[0].textContent, unset.join()).toContain(SIGN_IN_UNAVAILABLE)
      expect(nav.assigned, unset.join()).toEqual([])
      await act(async () => root.unmount())
      root = createRoot(container)
      vi.resetModules()
    }
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('footerCockpit_follows_theSameRule', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null)))
    await bootAt('/')
    await clickByText(document.querySelector('footer')!, 'Open the cockpit')
    expect(nav.assigned).toEqual([START])
    expect(dialogs().length).toBe(0)

    await act(async () => root.unmount())
    root = createRoot(container)
    vi.resetModules()
    await bootAt(`/?state=${STATE}`)
    await clickByText(document.querySelector('footer')!, 'Open the cockpit')
    expect(dialogs().length).toBe(1)
    expect(nav.assigned).toEqual([START])
  })
})
