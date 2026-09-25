// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// AUTH-05-07 AC-1..7: the landing sign-in form. Real signIn.ts, fetch stubbed.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SignInForm } from './SignInForm'
import { SignInModal } from './SignInModal'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const HOME = 'https://www.ascomply.com/'
const STATE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'
// D12 401 copy.
const INCORRECT = 'Email or password is incorrect.'
// signInForm.ts validator copy.
const EMAIL_REQUIRED = 'Enter your work email.'
const PASSWORD_REQUIRED = 'Enter your password.'

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

function unconfigure(): void {
  vi.stubEnv('VITE_GATEWAY_URL', '')
  vi.stubEnv('VITE_APP_URL', 'https://app.x/')
}

async function mountForm(state: string | null, initialError?: string): Promise<void> {
  await act(async () => {
    root.render(createElement(SignInForm, { state, initialError }))
  })
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

// React's value tracker swallows a plain `input.value = …` write on a controlled input.
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

function sentBody(fetchMock: ReturnType<typeof vi.fn>, call = 0): Record<string, unknown> {
  const [url, init] = fetchMock.mock.calls[call] as [string, RequestInit]
  expect(url).toBe('https://gw.x/auth/sign-in')
  return JSON.parse(init.body as string) as Record<string, unknown>
}

function expectIdle(): void {
  expect(emailInput().disabled).toBe(false)
  expect(passwordInput().disabled).toBe(false)
  expect(submitButton().disabled).toBe(false)
  expect(submitButton().textContent).toContain('Sign in →')
  expect(submitButton().textContent).not.toContain('Checking…')
}

function expectBusy(): void {
  expect(emailInput().disabled).toBe(true)
  expect(passwordInput().disabled).toBe(true)
  expect(submitButton().disabled).toBe(true)
  expect(submitButton().textContent).toContain('Checking…')
  expect(submitButton().textContent).not.toContain('Sign in →')
}

describe('AC-1: the form sits in the modal only when configured', () => {
  it('the form renders only when configured', async () => {
    configure()
    await act(async () => {
      root.render(createElement(SignInModal, { onClose: vi.fn(), state: STATE }))
    })
    let d = one<HTMLElement>(document, '[role="dialog"]')
    const picker = one<HTMLElement>(d, '[data-testid="persona-picker"]')
    expect(picker.querySelectorAll('[data-persona]').length).toBe(4)
    expect(d.textContent).toContain('Sign in to your workspace')
    expect(d.textContent).toContain('or explore with a demo profile')
    const pw = one<HTMLInputElement>(d, 'input[type="password"]')
    expect(picker.contains(pw)).toBe(false)
    // D7: the form sits above the persona list.
    expect(pw.compareDocumentPosition(picker) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    const heading = Array.from(d.querySelectorAll('h3')).map((h) => h.textContent)
    expect(heading.indexOf('Sign in to your workspace')).toBeGreaterThanOrEqual(0)
    expect(heading.indexOf('Sign in to your workspace')).toBeLessThan(heading.indexOf('Choose an account'))

    await act(async () => root.unmount())
    root = createRoot(container)
    unconfigure()
    await act(async () => {
      root.render(createElement(SignInModal, { onClose: vi.fn(), state: STATE }))
    })
    d = one<HTMLElement>(document, '[role="dialog"]')
    expect(d.querySelectorAll('[data-persona]').length).toBe(4)
    expect(d.textContent).toContain('Choose an account')
    expect(d.querySelectorAll('input').length).toBe(0)
    expect(d.querySelectorAll('form').length).toBe(0)
    expect(d.textContent).not.toContain('Sign in to your workspace')
    expect(d.textContent).not.toContain('or explore with a demo profile')
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('AC-2: the held state', () => {
  it('with no state the form offers Continue with email', async () => {
    configure()
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    await mountForm(null)
    expect(container.querySelectorAll('input').length).toBe(0)
    const buttons = Array.from(container.querySelectorAll('button'))
    expect(buttons.length).toBe(1)
    expect(buttons[0].textContent?.trim()).toBe('Continue with email')
    await act(async () => {
      buttons[0].click()
    })
    expect(locationStub.href).toBe('https://app.x?auth=start')
    expect(fetchMock).not.toHaveBeenCalled()
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('the submit carries the held state', async () => {
    configure()
    const fetchMock = vi.fn().mockReturnValue(new Promise(() => undefined))
    vi.stubGlobal('fetch', fetchMock)
    await mountForm(STATE)
    expect(container.textContent).not.toContain('Continue with email')
    // Email is trimmed; the password is sent as typed (AUTH-05-06 QA note).
    await fill('  ada@okafor.ng  ', ' s3cret ')
    await submit()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const body = sentBody(fetchMock)
    expect(Object.keys(body).sort()).toEqual(['email', 'password', 'state'])
    expect(body).toStrictEqual({ email: 'ada@okafor.ng', password: ' s3cret ', state: STATE })
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('AC-4: validation', () => {
  it('empty submit shows field errors and sends nothing', async () => {
    configure()
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    await mountForm(STATE)
    expect(alerts().length).toBe(0)
    expect(emailInput().getAttribute('aria-invalid')).not.toBe('true')
    await submit()

    const got = alerts()
    expect(got.length).toBe(2)
    const cases: [HTMLInputElement, string][] = [
      [emailInput(), EMAIL_REQUIRED],
      [passwordInput(), PASSWORD_REQUIRED],
    ]
    expect(cases.length).toBe(2)
    for (const [input, message] of cases) {
      expect(input.getAttribute('aria-invalid')).toBe('true')
      const id = input.getAttribute('aria-describedby')
      expect(id, `${input.type} has aria-describedby`).toBeTruthy()
      const target = document.getElementById(id!)
      expect(target, `#${id} exists`).not.toBeNull()
      expect(target!.getAttribute('role')).toBe('alert')
      expect(target!.textContent).toContain(message)
    }
    expect(fetchMock).not.toHaveBeenCalled()
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('AC-5: busy', () => {
  it('controls are disabled while submitting', async () => {
    configure()
    vi.stubGlobal('fetch', vi.fn().mockReturnValue(new Promise(() => undefined)))
    await mountForm(STATE)
    expectIdle()
    await fill('ada@okafor.ng', 'pw')
    await submit()
    expectBusy()
    // D22: the retired OTP label stays banned.
    expect(container.textContent).not.toContain('Signing in')
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('AC-6: outcomes', () => {
  it('success navigates to the app hand-off', async () => {
    configure()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(200, { code: 'C' })))
    await mountForm(STATE)
    await fill('ada@okafor.ng', 'pw')
    await submit()
    await flush()
    expect(locationStub.href).toBe('https://app.x?handoff=C')
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('a refusal shows the message and re-enables', async () => {
    configure()
    const fetchMock = vi.fn().mockImplementation(async () => jsonResponse(401, { error: 'invalid credentials' }))
    vi.stubGlobal('fetch', fetchMock)
    await mountForm(STATE)
    await fill('ada@okafor.ng', 'wrong')
    await submit()
    await flush()

    const got = alerts()
    expect(got.length).toBe(1)
    expect(got[0].textContent).toContain(INCORRECT)
    expectIdle()
    expect(locationStub.href).toBe(HOME)

    // A 401 keeps the same state for the next attempt.
    await fill('ada@okafor.ng', 'right')
    await submit()
    await flush()
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(sentBody(fetchMock, 1).state).toBe(STATE)
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('AC-7: back/forward cache', () => {
  it('a restored page returns the form to idle', async () => {
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
    expect(alerts().length).toBe(1)

    await fill('ada@okafor.ng', 'pw')
    await submit()
    expectBusy()

    await act(async () => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
    })
    expectIdle()
    expect(passwordInput().value).toBe('')
    expect(alerts().length).toBe(0)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('a fresh pageshow changes nothing', async () => {
    configure()
    vi.stubGlobal('fetch', vi.fn().mockReturnValue(new Promise(() => undefined)))
    await mountForm(STATE)
    await fill('ada@okafor.ng', 'pw')
    await submit()
    expectBusy()

    await act(async () => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: false }))
    })
    expectBusy()
    expect(passwordInput().value).toBe('pw')
    expect(consoleError).not.toHaveBeenCalled()
  })
})
