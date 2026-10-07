// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// The landing sign-in form. Real signIn.ts, fetch stubbed.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SignInForm } from './SignInForm'
import { SignInModal } from './SignInModal'
import type { ConsoleTarget } from '../signIn'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const HOME = 'https://www.ascomply.com/'
const STATE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMN-_0'
// 401 copy.
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

async function mountForm(state: string | null, initialError?: string, consoleTarget?: ConsoleTarget, onForgot?: () => void): Promise<void> {
  await act(async () => {
    root.render(createElement(SignInForm, { heldState: () => state, initialError, consoleTarget, onForgot }))
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
      root.render(createElement(SignInModal, { onClose: vi.fn(), heldState: () => STATE }))
    })
    let d = one<HTMLElement>(document, '[role="dialog"]')
    const pw = one<HTMLInputElement>(d, 'input[type="password"]')
    expect(d.textContent).toContain('Sign in to your workspace')
    // The form leads the dialog: its email input is the first input in the window.
    const inputs = Array.from(d.querySelectorAll('input'))
    expect(inputs.length).toBeGreaterThan(0)
    expect(inputs[0]).toBe(one<HTMLInputElement>(d, 'input[type="email"]'))
    expect(inputs).toContain(pw)
    expect(d.querySelectorAll('[data-testid="persona-picker"]').length).toBe(0)
    expect(d.querySelectorAll('[data-persona]').length).toBe(0)
    expect(d.textContent).not.toContain('or explore with a demo profile')

    await act(async () => root.unmount())
    root = createRoot(container)
    unconfigure()
    await act(async () => {
      root.render(createElement(SignInModal, { onClose: vi.fn(), heldState: () => STATE }))
    })
    d = one<HTMLElement>(document, '[role="dialog"]')
    expect(d.textContent).toContain('Sign in to your workspace')
    expect(d.querySelectorAll('input').length).toBe(0)
    expect(d.querySelectorAll('form').length).toBe(0)
    expect(d.querySelectorAll('[data-testid="persona-picker"]').length).toBe(0)
    expect(d.textContent).not.toContain('Choose an account')
    expect(d.textContent).not.toContain('or explore with a demo profile')
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('AC-2: the held state', () => {
  it('form_noState_rendersFieldsAndNoContinue', async () => {
    configure()
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    await mountForm(null)
    expect(emailInput()).toBeDefined()
    expect(passwordInput()).toBeDefined()
    expect(submitButton()).toBeDefined()
    expect(Array.from(container.querySelectorAll('button'), (b) => b.textContent?.trim())).not.toContain('Continue with email')
    expect(container.textContent).not.toContain('Continue with email')
    expect(fetchMock).not.toHaveBeenCalled()
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('submit_noState_bouncesToTheApp', async () => {
    configure()
    const fetchMock = vi.fn().mockResolvedValue(new Response(null))
    vi.stubGlobal('fetch', fetchMock)
    await mountForm(null)
    await fill('ada@okafor.ng', 'pw')
    await submit()
    expect(locationStub.href).toBe('https://app.x?auth=start')
    expect(fetchMock.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === 'POST')).toHaveLength(0)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('submit_noState_bouncesThroughTheHeldConsole', async () => {
    configure()
    vi.stubEnv('VITE_SUPPORT_URL', 'https://support.x/')
    const fetchMock = vi.fn().mockResolvedValue(new Response(null))
    vi.stubGlobal('fetch', fetchMock)
    await mountForm(null, undefined, 'support')
    await fill('ada@okafor.ng', 'pw')
    await submit()
    expect(locationStub.href).toBe('https://support.x?auth=start')
    expect(fetchMock.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === 'POST')).toHaveLength(0)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('submit_stateExpiredWhileOpen_bouncesAndPostsNothing', async () => {
    configure()
    const fetchMock = vi.fn().mockResolvedValue(new Response(null))
    vi.stubGlobal('fetch', fetchMock)
    let live: string | null = STATE
    await act(async () => {
      root.render(createElement(SignInForm, { heldState: () => live }))
    })
    await fill('ada@okafor.ng', 'pw')
    live = null
    await submit()
    expect(locationStub.href).toBe('https://app.x?auth=start')
    expect(fetchMock.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === 'POST')).toHaveLength(0)
  })

  it('submit_noState_invalidFields_showsErrorsAndStays', async () => {
    configure()
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    await mountForm(null)
    await submit()
    expect(alerts().map((a) => a.textContent?.trim())).toEqual([EMAIL_REQUIRED, PASSWORD_REQUIRED])
    expect(locationStub.href).toBe(HOME)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('the submit carries the held state', async () => {
    configure()
    const fetchMock = vi.fn().mockReturnValue(new Promise(() => undefined))
    vi.stubGlobal('fetch', fetchMock)
    await mountForm(STATE)
    expect(container.textContent).not.toContain('Continue with email')
    // Email is trimmed; the password is sent as typed.
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
    // The retired OTP label stays banned.
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

describe('the resend control on the unverified sign-in error', () => {
  const ADA = 'ada@corp.example'
  const RESEND = 'Send the link again'
  const SENDING = 'Sending…'
  const UNVERIFIED = 'Verify your email address first. The link is in your inbox.'
  // Pinned here, not imported: a wording change is a deliberate edit.
  const sent = (address: string) => `If ${address} still needs verifying, a new link is on its way. Use the newest one.`
  const RESEND_URL = 'https://gw.x/auth/resend-verification'

  type Answer = () => Response | Promise<Response>

  // Each route takes its answers in order; the last one repeats.
  function routedFetch(routes: { signIn: Answer[]; resend?: Answer[] }) {
    const used = { signIn: 0, resend: 0 }
    const next = (key: 'signIn' | 'resend') => {
      const list = routes[key] ?? [() => jsonResponse(202, { status: 'accepted' })]
      return list[Math.min(used[key]++, list.length - 1)]()
    }
    const fetchMock = vi.fn().mockImplementation(async (url: unknown) => (String(url).endsWith('/auth/sign-in') ? next('signIn') : next('resend')))
    vi.stubGlobal('fetch', fetchMock)
    return fetchMock
  }

  function callsTo(fetchMock: ReturnType<typeof vi.fn>, suffix: string): [string, RequestInit][] {
    return (fetchMock.mock.calls as [string, RequestInit][]).filter(([url]) => String(url).endsWith(suffix))
  }

  const refuse = (status: number): Answer => () => jsonResponse(status, { error: 'refused' })
  const never: Answer = () => new Promise<Response>(() => undefined)

  function resendButtons(): HTMLButtonElement[] {
    return Array.from(container.querySelectorAll<HTMLButtonElement>('button')).filter((b) => [RESEND, SENDING].includes(b.textContent?.trim() ?? ''))
  }

  const resendButton = () => {
    const all = resendButtons()
    expect(all.length, `expected exactly one "${RESEND}" button`).toBe(1)
    return all[0]
  }

  const statusNotes = () => Array.from(container.querySelectorAll('[role="status"]')).map((n) => n.textContent?.trim() ?? '').filter(Boolean)

  async function signInAs(email: string): Promise<void> {
    await fill(email, 'pw')
    await submit()
    await flush()
  }

  async function click(el: HTMLElement): Promise<void> {
    await act(async () => {
      el.click()
    })
    await flush()
  }

  it('the unverified sign-in error offers Send the link again', async () => {
    configure()
    routedFetch({ signIn: [refuse(403)] })
    await mountForm(STATE)

    await signInAs(ADA)

    const got = alerts()
    expect(got.map((a) => a.textContent?.trim())).toEqual([UNVERIFIED])
    const btn = resendButton()
    expect(btn.type, 'a plain button, so it never submits the form').toBe('button')
    expect(got[0].compareDocumentPosition(btn) & Node.DOCUMENT_POSITION_FOLLOWING, 'the button follows the alert').toBeTruthy()
    expect(btn.disabled).toBe(false)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('no resend control before a submit or after another refusal', async () => {
    configure()
    routedFetch({ signIn: [refuse(401)] })
    await mountForm(STATE)
    expect(container.querySelectorAll('button').length, 'control: only the submit button shows').toBe(1)
    expect(resendButtons()).toHaveLength(0)
    await act(async () => root.unmount())

    const cases: [string, Answer][] = [
      ['401', refuse(401)],
      ['429', refuse(429)],
      ['502', refuse(502)],
    ]
    for (const [name, answer] of cases) {
      root = createRoot(container)
      routedFetch({ signIn: [answer] })
      await mountForm(STATE)
      await signInAs(ADA)
      expect(alerts().length, `${name}: control: the refusal shows`).toBe(1)
      expect(resendButtons(), name).toHaveLength(0)
      await act(async () => root.unmount())
    }
    root = createRoot(container)
  })

  it('the live region exists before the click and receives the notice after it', async () => {
    configure()
    routedFetch({ signIn: [refuse(403)] })
    await mountForm(STATE)
    expect(container.querySelector('[role="status"]'), 'control: no region without the resend control').toBeNull()
    await signInAs(ADA)

    const region = container.querySelector('[role="status"]')
    expect(region, 'the region is in the DOM before the click').not.toBeNull()
    expect(region!.textContent).toBe('')

    await click(resendButton())

    expect(container.querySelector('[role="status"]'), 'the same node receives the text').toBe(region)
    expect(region!.textContent?.trim()).toBe(sent(ADA))
  })

  it('the resend posts the address that got the 403', async () => {
    configure()
    const fetchMock = routedFetch({ signIn: [refuse(403)] })
    await mountForm(STATE)
    await signInAs(` ${ADA} `)
    await act(async () => {
      typeInto(emailInput(), 'bob@corp.example')
    })

    await click(resendButton())

    const calls = callsTo(fetchMock, '/auth/resend-verification')
    expect(calls, 'one resend').toHaveLength(1)
    expect(calls[0][0]).toBe(RESEND_URL)
    expect(JSON.parse(calls[0][1].body as string)).toStrictEqual({ email: ADA })
    expect(statusNotes()).toEqual([sent(ADA)])
    expect(callsTo(fetchMock, '/auth/sign-in'), 'the resend is not a sign-in').toHaveLength(1)
  })

  it('the resend button is disabled while sending', async () => {
    configure()
    let release!: (r: Response) => void
    const held = new Promise<Response>((resolve) => {
      release = resolve
    })
    const fetchMock = routedFetch({ signIn: [refuse(403)], resend: [() => held] })
    await mountForm(STATE)
    await signInAs(ADA)
    const btn = resendButton()

    await click(btn)
    await click(btn)

    expect(callsTo(fetchMock, '/auth/resend-verification'), 'one POST for two clicks').toHaveLength(1)
    expect(btn.disabled).toBe(true)
    expect(btn.textContent?.trim()).toBe(SENDING)

    await act(async () => {
      release(jsonResponse(202, { status: 'accepted' }))
    })
    await flush()
    expect(btn.disabled).toBe(false)
    expect(btn.textContent?.trim()).toBe(RESEND)
    expect(statusNotes()).toEqual([sent(ADA)])
  })

  it('a new submit and a bfcache restore clear the resend control', async () => {
    configure()
    routedFetch({ signIn: [refuse(403), never] })
    await mountForm(STATE)
    await signInAs(ADA)
    await click(resendButton())
    expect(statusNotes(), 'control: the notice shows').toEqual([sent(ADA)])

    await fill(ADA, 'pw')
    await submit()

    expect(resendButtons(), 'a new submit removes the button').toHaveLength(0)
    expect(statusNotes(), 'a new submit removes the notice').toEqual([])
    await act(async () => root.unmount())

    root = createRoot(container)
    routedFetch({ signIn: [refuse(403)] })
    await mountForm(STATE)
    await signInAs(ADA)
    await click(resendButton())
    expect(statusNotes(), 'control: the notice shows again').toEqual([sent(ADA)])

    await act(async () => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: false }))
    })
    expect(resendButtons(), 'a fresh pageshow changes nothing').toHaveLength(1)
    expect(statusNotes()).toEqual([sent(ADA)])

    await act(async () => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
    })
    expect(resendButtons(), 'a restore removes the button').toHaveLength(0)
    expect(statusNotes(), 'a restore removes the notice').toEqual([])
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('a failed resend shows the failure alert under the unverified one, and the next click clears it', async () => {
    const FAILED = 'The link could not be sent right now. Try again shortly.'
    const cases: [string, Answer][] = [
      ['400', refuse(400)],
      ['502', refuse(502)],
      ['503', refuse(503)],
      ['network', () => Promise.reject(new TypeError('Failed to fetch'))],
    ]
    let current: Answer = cases[0][1]
    configure()
    routedFetch({ signIn: [refuse(403)], resend: [() => current()] })
    await mountForm(STATE)
    await signInAs(ADA)
    const shown = () => alerts().map((a) => a.textContent?.trim())

    for (const [name, answer] of cases) {
      current = answer
      const btn = resendButton()
      await click(btn)
      expect(shown(), name).toEqual([UNVERIFIED, FAILED])
      expect(statusNotes(), `${name}: no sent notice`).toEqual([])
      expect(btn.disabled, `${name}: re-enabled`).toBe(false)
      expect(btn.textContent?.trim(), name).toBe(RESEND)

      current = never
      await click(btn)
      expect(shown(), `${name}: the old alert is gone while the next resend runs`).toEqual([UNVERIFIED])
      expect(btn.disabled, `${name}: sending`).toBe(true)
      await act(async () => root.unmount())
      root = createRoot(container)
      await mountForm(STATE)
      current = answer
      await signInAs(ADA)
    }

    vi.stubEnv('VITE_GATEWAY_URL', '')
    await click(resendButton())
    expect(shown(), 'gateway unset').toEqual([UNVERIFIED, FAILED])
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('a later resend clears the earlier notice, and a new 403 for another address resends that address', async () => {
    configure()
    let release!: (r: Response) => void
    const held = new Promise<Response>((resolve) => {
      release = resolve
    })
    const fetchMock = routedFetch({
      signIn: [refuse(403), refuse(403), refuse(401)],
      resend: [() => jsonResponse(202, { status: 'accepted' }), () => held, () => jsonResponse(202, {})],
    })
    await mountForm(STATE)
    await signInAs(ADA)
    await click(resendButton())
    expect(statusNotes(), 'control: the notice shows').toEqual([sent(ADA)])

    await click(resendButton())
    expect(statusNotes(), 'the old notice is gone while the next resend runs').toEqual([])
    await act(async () => release(jsonResponse(202, { status: 'accepted' })))
    await flush()
    expect(statusNotes()).toEqual([sent(ADA)])

    await fill('bob@corp.example', 'pw')
    await submit()
    await flush()
    expect(resendButton().disabled, 'a new 403 offers a fresh button').toBe(false)
    expect(statusNotes(), 'the notice for the earlier address is gone').toEqual([])
    await click(resendButton())
    const calls = callsTo(fetchMock, '/auth/resend-verification')
    expect(calls).toHaveLength(3)
    expect(JSON.parse(calls[2][1].body as string)).toStrictEqual({ email: 'bob@corp.example' })
    expect(statusNotes()).toEqual([sent('bob@corp.example')])

    await submit()
    await flush()
    expect(alerts().map((a) => a.textContent?.trim()), 'a 401 after a 403').toEqual([INCORRECT])
    expect(resendButtons(), 'a 401 removes the resend control').toHaveLength(0)
    expect(statusNotes()).toEqual([])
  })

  it.each(['a new submit', 'a bfcache restore'])('a resend answer that lands after %s is not shown against the next 403', async (route) => {
    configure()
    let release!: (r: Response) => void
    const held = new Promise<Response>((resolve) => {
      release = resolve
    })
    routedFetch({ signIn: [refuse(403)], resend: [() => held] })
    await mountForm(STATE)
    await signInAs(ADA)
    await click(resendButton())
    expect(resendButton().textContent?.trim(), 'control: the resend for ada is in flight').toBe(SENDING)

    if (route === 'a bfcache restore') {
      await act(async () => {
        window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
      })
    }
    await fill('bob@corp.example', 'pw')
    await submit()
    await flush()
    expect(resendButtons(), 'control: bob got his own 403 and button').toHaveLength(1)

    await act(async () => release(jsonResponse(202, { status: 'accepted' })))
    await flush()

    expect(statusNotes(), 'the answer was for ada').toEqual([])
    expect(consoleError).not.toHaveBeenCalled()
  })

  it("an older resend answer leaves the next address's resend sending, and only its own answer ends it", async () => {
    configure()
    const releases: ((r: Response) => void)[] = []
    const held = () =>
      new Promise<Response>((resolve) => {
        releases.push(resolve)
      })
    routedFetch({ signIn: [refuse(403)], resend: [held, held] })
    await mountForm(STATE)
    await signInAs(ADA)
    await click(resendButton())
    await fill('bob@corp.example', 'pw')
    await submit()
    await flush()
    await click(resendButton())
    expect(releases, 'control: both resends are in flight').toHaveLength(2)

    await act(async () => releases[0](jsonResponse(202, { status: 'accepted' })))
    await flush()
    expect(resendButton().textContent?.trim(), "ada's answer does not end bob's resend").toBe(SENDING)
    expect(resendButton().disabled).toBe(true)
    expect(statusNotes()).toEqual([])

    await act(async () => releases[1](jsonResponse(202, { status: 'accepted' })))
    await flush()
    expect(resendButton().disabled).toBe(false)
    expect(statusNotes()).toEqual([sent('bob@corp.example')])
  })
})

describe('the Forgot password? control', () => {
  const FORGOT = 'Forgot password?'
  const forgotButtons = () => Array.from(container.querySelectorAll<HTMLButtonElement>('button')).filter((b) => b.textContent?.trim() === FORGOT)
  const forgotButton = () => {
    const all = forgotButtons()
    expect(all.length, `expected exactly one "${FORGOT}" button`).toBe(1)
    return all[0]
  }

  it('the form offers Forgot password? under the password field', async () => {
    configure()
    const fetchMock = vi.fn().mockReturnValue(new Promise(() => undefined))
    vi.stubGlobal('fetch', fetchMock)
    const onForgot = vi.fn()
    await mountForm(STATE, undefined, undefined, onForgot)

    const btn = forgotButton()
    expect(btn.type, 'a plain button, so it never submits the form').toBe('button')
    expect(btn.classList.contains('ds-btn--text')).toBe(true)
    expect(btn.disabled).toBe(false)
    expect(passwordInput().compareDocumentPosition(btn) & Node.DOCUMENT_POSITION_FOLLOWING, 'after the password field').toBeTruthy()
    expect(btn.compareDocumentPosition(submitButton()) & Node.DOCUMENT_POSITION_FOLLOWING, 'before Sign in').toBeTruthy()
    expect(submitButton().textContent).toContain('Sign in →')

    await act(async () => {
      btn.click()
    })
    expect(onForgot).toHaveBeenCalledTimes(1)
    expect(fetchMock, 'the control is not a sign-in').not.toHaveBeenCalled()

    await fill('ada@okafor.ng', 'pw')
    await submit()
    expectBusy()
    expect(forgotButton().disabled, 'disabled while a sign-in runs').toBe(true)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('forgotButton_showsWithoutAHeldState', async () => {
    configure()
    const onForgot = vi.fn()
    await mountForm(null, undefined, undefined, onForgot)
    expect(forgotButtons()).toHaveLength(1)
    await act(async () => {
      forgotButtons()[0].click()
    })
    expect(onForgot).toHaveBeenCalledTimes(1)
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('Forgot password? follows the password error and stays enabled beside the form errors', async () => {
    configure()
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    const onForgot = vi.fn()
    await mountForm(STATE, undefined, undefined, onForgot)

    await submit()
    const errs = alerts()
    expect(errs.map((a) => a.textContent?.trim()), 'control: both field errors show').toEqual([EMAIL_REQUIRED, PASSWORD_REQUIRED])
    const btn = forgotButton()
    expect(errs[1].compareDocumentPosition(btn) & Node.DOCUMENT_POSITION_FOLLOWING, 'after the password alert').toBeTruthy()
    expect(btn.disabled).toBe(false)
    await act(async () => {
      btn.click()
    })
    expect(onForgot).toHaveBeenCalledTimes(1)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('no Forgot password? when onForgot is not given', async () => {
    configure()
    await mountForm(STATE)
    expect(submitButton().textContent, 'control: the credentials form shows').toContain('Sign in →')
    expect(forgotButtons()).toHaveLength(0)
    expect(consoleError).not.toHaveBeenCalled()
  })
})
