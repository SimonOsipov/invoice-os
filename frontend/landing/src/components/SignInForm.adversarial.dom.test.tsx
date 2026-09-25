// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// Adversarial coverage for SignInForm and its SignInModal gate. Real signIn.ts, fetch stubbed.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SignInForm } from './SignInForm'
import { SignInModal } from './SignInModal'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const HERE = dirname(fileURLToPath(import.meta.url))
const HOME = 'https://www.ascomply.com/'
const STATE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'
// Error copy.
const UNVERIFIED = 'Verify your email address first. The link is in your inbox.'
const THROTTLED = 'Too many attempts. Try again in a minute.'
const UNAVAILABLE = 'Sign-in is unavailable right now. Try again shortly.'
const INCORRECT = 'Email or password is incorrect.'
const DIVIDER = 'or explore with a demo profile'
const FORM_HEADING = 'Sign in to your workspace'

let container: HTMLDivElement
let root: Root
let consoleError: ReturnType<typeof vi.spyOn>
let locationStub: { href: string }
let originalLocation: PropertyDescriptor | undefined

beforeEach(() => {
  originalLocation = Object.getOwnPropertyDescriptor(window, 'location')
  locationStub = { href: HOME }
  Object.defineProperty(window, 'location', { value: locationStub, writable: true, configurable: true })
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(async () => {
  await act(async () => root.unmount())
  container.remove()
  if (originalLocation) Object.defineProperty(window, 'location', originalLocation)
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function configure(): void {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x/')
  vi.stubEnv('VITE_APP_URL', 'https://app.x/')
  vi.stubEnv('VITE_OPS_URL', '')
  vi.stubEnv('VITE_SUPPORT_URL', '')
}

async function mountForm(state: string | null, initialError?: string): Promise<void> {
  await act(async () => {
    root.render(createElement(SignInForm, { heldState: () => state, initialError }))
  })
}

async function mountModal(state: string | null, initialError?: string): Promise<HTMLElement> {
  await act(async () => {
    root.render(createElement(SignInModal, { onClose: vi.fn(), heldState: () => state, initialError }))
  })
  const all = document.querySelectorAll<HTMLElement>('[role="dialog"]')
  expect(all.length).toBe(1)
  return all[0]
}

async function flush(): Promise<void> {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0))
  })
}

function one<T extends Element>(scope: ParentNode, sel: string): T {
  const all = scope.querySelectorAll<T>(sel)
  expect(all.length, `expected exactly one ${sel}`).toBe(1)
  return all[0]
}

const emailInput = () => one<HTMLInputElement>(container, 'input[type="email"]')
const passwordInput = () => one<HTMLInputElement>(container, 'input[type="password"]')
const submitButton = () => one<HTMLButtonElement>(container, 'button[type="submit"]')
const alerts = () => Array.from(container.querySelectorAll<HTMLElement>('[role="alert"]'))

function typeInto(input: HTMLInputElement, value: string): void {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

async function fill(email: string, password: string): Promise<void> {
  await act(async () => {
    typeInto(emailInput(), email)
    typeInto(passwordInput(), password)
  })
}

async function submit(): Promise<void> {
  await act(async () => {
    submitButton().click()
  })
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function expectIdle(): void {
  expect(emailInput().disabled).toBe(false)
  expect(passwordInput().disabled).toBe(false)
  expect(submitButton().disabled).toBe(false)
  expect(submitButton().textContent).toContain('Sign in →')
}

function expectBusy(): void {
  expect(emailInput().disabled).toBe(true)
  expect(passwordInput().disabled).toBe(true)
  expect(submitButton().disabled).toBe(true)
  expect(submitButton().textContent).toContain('Checking…')
}

describe('SignInForm adversarial: submit', () => {
  it('a second submit while busy sends nothing more', async () => {
    configure()
    const fetchMock = vi.fn().mockReturnValue(new Promise(() => undefined))
    vi.stubGlobal('fetch', fetchMock)
    await mountForm(STATE)
    await fill('ada@okafor.ng', 'pw')
    await submit()
    expectBusy()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    // Bypass the disabled button: the handler's own guard must refuse.
    const form = one<HTMLFormElement>(container, 'form')
    await act(async () => {
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    })
    await act(async () => {
      submitButton().click()
    })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('a 200 keeps the form busy until the page leaves', async () => {
    configure()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(200, { code: 'C' })))
    await mountForm(STATE)
    await fill('ada@okafor.ng', 'pw')
    await submit()
    await flush()
    expect(locationStub.href).toBe('https://app.x?handoff=C')
    expectBusy()
    expect(alerts().length).toBe(0)
  })

  it('a null hand-off URL returns the form to idle and stays put', async () => {
    // Gateway set, app base unset: handoffUrl is null.
    vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x/')
    vi.stubEnv('VITE_APP_URL', '')
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse(200, { code: 'C' }))
    vi.stubGlobal('fetch', fetchMock)
    await mountForm(STATE)
    await fill('ada@okafor.ng', 'pw')
    await submit()
    await flush()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(locationStub.href).toBe(HOME)
    expectIdle()
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('each refusal shows its D12 copy and re-enables the form', async () => {
    configure()
    const cases: [string, () => Promise<Response>, string][] = [
      ['403', async () => jsonResponse(403, { error: 'email not confirmed' }), UNVERIFIED],
      ['429', async () => jsonResponse(429, { error: 'too many requests' }), THROTTLED],
      ['500', async () => jsonResponse(500, { error: 'boom' }), UNAVAILABLE],
      ['network', async () => Promise.reject(new TypeError('Failed to fetch')), UNAVAILABLE],
      ['200 without a code', async () => jsonResponse(200, {}), UNAVAILABLE],
    ]
    expect(cases.length).toBe(5)
    for (const [label, respond, copy] of cases) {
      vi.stubGlobal('fetch', vi.fn().mockImplementation(respond))
      await mountForm(STATE)
      await fill('ada@okafor.ng', 'pw')
      await submit()
      await flush()
      const got = alerts()
      expect(got.length, label).toBe(1)
      expect(got[0].textContent, label).toContain(copy)
      expectIdle()
      expect(locationStub.href, label).toBe(HOME)
      await act(async () => root.unmount())
      root = createRoot(container)
    }
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('a new submit clears the previous form error while busy', async () => {
    configure()
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(async () => jsonResponse(401, { error: 'invalid credentials' }))
      .mockReturnValue(new Promise(() => undefined))
    vi.stubGlobal('fetch', fetchMock)
    await mountForm(STATE)
    await fill('ada@okafor.ng', 'wrong')
    await submit()
    await flush()
    expect(alerts().map((a) => a.textContent)).toEqual([expect.stringContaining(INCORRECT)])
    await fill('ada@okafor.ng', 'right')
    await submit()
    expectBusy()
    expect(alerts().length).toBe(0)
  })
})

describe('SignInForm adversarial: fields', () => {
  it('fields carry their types and autocomplete tokens', async () => {
    configure()
    await mountForm(STATE)
    expect(emailInput().getAttribute('autocomplete')).toBe('email')
    expect(passwordInput().getAttribute('autocomplete')).toBe('current-password')
    expect(container.querySelector('label[for="' + emailInput().id + '"]')?.textContent).toBe('Work email')
    expect(container.querySelector('label[for="' + passwordInput().id + '"]')?.textContent).toBe('Password')
    expect(one<HTMLFormElement>(container, 'form').noValidate).toBe(true)
  })

  it('a field error clears when its own field is edited, not the other', async () => {
    configure()
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    await mountForm(STATE)
    await submit()
    expect(alerts().length).toBe(2)

    await act(async () => typeInto(emailInput(), 'a'))
    expect(emailInput().getAttribute('aria-invalid')).toBe('false')
    expect(emailInput().hasAttribute('aria-describedby')).toBe(false)
    expect(passwordInput().getAttribute('aria-invalid')).toBe('true')
    expect(alerts().length).toBe(1)

    await act(async () => typeInto(passwordInput(), 'p'))
    expect(passwordInput().getAttribute('aria-invalid')).toBe('false')
    expect(passwordInput().hasAttribute('aria-describedby')).toBe(false)
    expect(alerts().length).toBe(0)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('focus goes to the first invalid field', async () => {
    configure()
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    await mountForm(STATE)
    await submit()
    expect(document.activeElement).toBe(emailInput())

    await fill('ada@okafor.ng', '')
    await submit()
    expect(document.activeElement).toBe(passwordInput())
    expect(alerts().length).toBe(1)
    expect(passwordInput().getAttribute('aria-invalid')).toBe('true')
    expect(emailInput().getAttribute('aria-invalid')).toBe('false')

    // A malformed email alone blocks the request too.
    await fill('a b@c.d', 'pw')
    await submit()
    expect(document.activeElement).toBe(emailInput())
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('aria-describedby resolves inside the same form, and ids are unique on the page', async () => {
    configure()
    vi.stubGlobal('fetch', vi.fn())
    const d = await mountModal(STATE)
    await act(async () => one<HTMLButtonElement>(d, 'button[type="submit"]').click())
    const form = one<HTMLFormElement>(d, 'form')
    const described = Array.from(form.querySelectorAll('input[aria-describedby]'))
    expect(described.length).toBe(2)
    for (const input of described) {
      const target = document.getElementById(input.getAttribute('aria-describedby')!)
      expect(target).not.toBeNull()
      expect(form.contains(target)).toBe(true)
    }
    const ids = Array.from(document.querySelectorAll('[id]')).map((e) => e.id)
    expect(ids.length).toBeGreaterThan(0)
    expect(new Set(ids).size).toBe(ids.length)
  })

  it('SignInForm has exactly one mount site, so its fixed ids cannot collide', () => {
    // ceiling: ids are the constant `si-form-*`; a second mount site needs a per-instance prefix.
    const srcRoot = join(HERE, '..')
    const hits: string[] = []
    const walk = (dir: string) => {
      for (const name of readdirSync(dir)) {
        const p = join(dir, name)
        if (statSync(p).isDirectory()) walk(p)
        else if (/\.tsx?$/.test(name) && !/\.test\.tsx?$/.test(name)) {
          const n = (readFileSync(p, 'utf8').match(/<SignInForm[\s/>]/g) ?? []).length
          for (let i = 0; i < n; i++) hits.push(p)
        }
      }
    }
    walk(srcRoot)
    expect(hits.map((p) => p.slice(srcRoot.length))).toEqual(['/components/SignInModal.tsx'])
  })
})

describe('SignInForm adversarial: initial error and state', () => {
  it('initialError shows beside the state-less Continue with email button', async () => {
    configure()
    await mountForm(null, 'This account has no workspace yet.')
    const buttons = Array.from(container.querySelectorAll('button'))
    expect(buttons.map((b) => b.textContent?.trim())).toEqual(['Continue with email'])
    const got = alerts()
    expect(got.length).toBe(1)
    expect(got[0].textContent).toContain('This account has no workspace yet.')
    expect(container.querySelectorAll('input').length).toBe(0)
  })

  it('initialError shows under the fields until the next submit', async () => {
    configure()
    vi.stubGlobal('fetch', vi.fn().mockReturnValue(new Promise(() => undefined)))
    await mountForm(STATE, "We couldn't open your workspace. Sign in again.")
    const got = alerts()
    expect(got.length).toBe(1)
    expect(got[0].textContent).toContain("We couldn't open your workspace. Sign in again.")
    // Under the button.
    expect(submitButton().compareDocumentPosition(got[0]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    await fill('ada@okafor.ng', 'pw')
    await submit()
    expect(alerts().length).toBe(0)
  })

  it('no state is written to storage on any path', async () => {
    configure()
    // Node 25 ships its own storage globals; install spies the form would have to go through.
    const writes: unknown[] = []
    const spyStore = () => ({ getItem: () => null, setItem: (...a: unknown[]) => void writes.push(a), removeItem: () => undefined, clear: () => undefined, key: () => null, length: 0 })
    vi.stubGlobal('localStorage', spyStore())
    vi.stubGlobal('sessionStorage', spyStore())
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(async () => jsonResponse(401, { error: 'invalid credentials' }))
      .mockImplementationOnce(async () => jsonResponse(200, { code: 'C' }))
    vi.stubGlobal('fetch', fetchMock)
    await mountForm(STATE)
    await submit()
    await fill('ada@okafor.ng', 'wrong')
    await submit()
    await flush()
    await fill('ada@okafor.ng', 'right')
    await submit()
    await flush()
    await act(async () => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
    })
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(locationStub.href).toBe('https://app.x?handoff=C')
    expect(writes).toEqual([])
    expect(document.cookie).not.toContain(STATE)
  })
})

describe('SignInForm adversarial: pageshow', () => {
  it('a fresh pageshow keeps a shown error and the typed password', async () => {
    configure()
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => jsonResponse(401, { error: 'invalid credentials' })))
    await mountForm(STATE)
    await fill('ada@okafor.ng', 'wrong')
    await submit()
    await flush()
    expect(alerts().length).toBe(1)
    await act(async () => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: false }))
    })
    expect(alerts().length).toBe(1)
    expect(passwordInput().value).toBe('wrong')
    expectIdle()
  })

  it('a restored page clears the error even when idle', async () => {
    configure()
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => jsonResponse(401, { error: 'invalid credentials' })))
    await mountForm(STATE)
    await fill('ada@okafor.ng', 'wrong')
    await submit()
    await flush()
    expect(alerts().length).toBe(1)
    await act(async () => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
    })
    expect(alerts().length).toBe(0)
    expect(passwordInput().value).toBe('')
    expect(emailInput().value).toBe('ada@okafor.ng')
  })

  it('the pageshow listener is removed on unmount', async () => {
    configure()
    const add = vi.spyOn(window, 'addEventListener')
    const remove = vi.spyOn(window, 'removeEventListener')
    await mountForm(STATE)
    const added = add.mock.calls.filter((c) => c[0] === 'pageshow')
    expect(added.length).toBe(1)
    await act(async () => root.unmount())
    const removed = remove.mock.calls.filter((c) => c[0] === 'pageshow')
    expect(removed.length).toBe(1)
    expect(removed[0][1]).toBe(added[0][1])
    root = createRoot(container)
    await act(async () => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
    })
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('SignInModal adversarial: the configured gate', () => {
  it('the unconfigured modal holds no form, divider or form heading, for either missing base', async () => {
    configure()
    const configuredPicker = one<HTMLElement>(await mountModal(STATE), '[data-testid="persona-picker"]').innerHTML
    expect(configuredPicker.length).toBeGreaterThan(0)
    await act(async () => root.unmount())

    const halves: [string, string, string][] = [
      ['gateway unset', '', 'https://app.x/'],
      ['app unset', 'https://gw.x/', ''],
    ]
    for (const [label, gw, app] of halves) {
      root = createRoot(container)
      vi.stubEnv('VITE_GATEWAY_URL', gw)
      vi.stubEnv('VITE_APP_URL', app)
      const d = await mountModal(STATE, 'This account has no workspace yet.')
      expect(d.querySelectorAll('form').length, label).toBe(0)
      expect(d.querySelectorAll('input').length, label).toBe(0)
      expect(d.querySelectorAll('[role="alert"]').length, label).toBe(0)
      expect(Array.from(d.querySelectorAll('h3')).map((h) => h.textContent), label).toEqual(['Choose an account'])
      expect(d.textContent, label).not.toContain(DIVIDER)
      expect(d.textContent, label).not.toContain(FORM_HEADING)
      expect(d.textContent, label).not.toContain('Continue with email')
      // The persona block is the same markup either way.
      expect(one<HTMLElement>(d, '[data-testid="persona-picker"]').innerHTML, label).toBe(configuredPicker)
      await act(async () => root.unmount())
    }
    root = createRoot(container)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('the configured modal passes initialError and state through to the form', async () => {
    configure()
    const d = await mountModal(null, 'This account has no workspace yet.')
    const got = Array.from(d.querySelectorAll('[role="alert"]'))
    expect(got.length).toBe(1)
    expect(got[0].textContent).toContain('This account has no workspace yet.')
    expect(d.textContent).toContain('Continue with email')
    expect(d.querySelectorAll('input').length).toBe(0)
  })

  it('the card scrolls on a short viewport', async () => {
    configure()
    const d = await mountModal(STATE)
    const card = Array.from(d.children).find((c) => c.tagName === 'DIV') as HTMLElement
    expect(card).toBeDefined()
    expect(card.contains(one(d, 'form'))).toBe(true)
    expect(card.style.maxHeight).toBe('calc(100vh - 48px)')
    expect(card.style.overflowY).toBe('auto')
  })

  it('the D20 header comment no longer says no backend call', () => {
    const src = readFileSync(join(HERE, 'SignInModal.tsx'), 'utf8')
    expect(src).toContain('signInConfigured')
    expect(src).not.toContain('No backend call here')
    const header = src.split('\n').slice(0, 3).join('\n')
    expect(header).toContain('gateway')
  })
})
