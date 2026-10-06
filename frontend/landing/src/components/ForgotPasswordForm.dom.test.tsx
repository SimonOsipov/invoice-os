// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// The forgot view's form. Real passwordReset.ts and register.ts, fetch stubbed.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ForgotPasswordForm } from './ForgotPasswordForm'
import { useResend } from './useResend'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const URL_ = 'https://gw.x/auth/request-password-reset'
// Pinned here, not imported: a wording change is a deliberate edit (story D15, Core AC 2).
const SENT = 'If this address has an account, a reset link is on its way.'
// register.ts RESEND_FAILED, reused for the reset request.
const RESEND_FAILED = 'The link could not be sent right now. Try again shortly.'
// signInForm.ts validator copy.
const EMAIL_REQUIRED = 'Enter your work email.'
const EMAIL_INVALID = 'Enter a valid work email address.'
const SEND = 'Send reset link'
const SENDING = 'Sending…'
const BACK = 'Back to sign in'

let container: HTMLDivElement
let root: Root
let consoleError: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x/')
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(async () => {
  await act(async () => root.unmount())
  container.remove()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function mountForm(onBack: () => void = vi.fn()): Promise<void> {
  await act(async () => {
    root.render(createElement(ForgotPasswordForm, { onBack }))
  })
}

async function flush(): Promise<void> {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0))
  })
}

function one<T extends Element>(sel: string): T {
  const all = container.querySelectorAll<T>(sel)
  expect(all.length, `expected exactly one ${sel}`).toBe(1)
  return all[0]
}

const emailInput = () => one<HTMLInputElement>('input[type="email"]')
const form = () => one<HTMLFormElement>('form')
const submitButton = () => one<HTMLButtonElement>('button[type="submit"]')
const backButton = () => {
  const all = Array.from(container.querySelectorAll<HTMLButtonElement>('button')).filter((b) => b.textContent?.trim() === BACK)
  expect(all.length, `expected exactly one "${BACK}" button`).toBe(1)
  return all[0]
}
const alerts = () => Array.from(container.querySelectorAll<HTMLElement>('[role="alert"]'), (a) => a.textContent?.trim())
const statusText = () => one<HTMLElement>('[role="status"]').textContent?.trim()

// React's value tracker swallows a plain `input.value = …` write on a controlled input.
function typeInto(input: HTMLInputElement, value: string): void {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

async function fillEmail(value: string): Promise<void> {
  await act(async () => typeInto(emailInput(), value))
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
  expect(submitButton().disabled).toBe(false)
  expect(submitButton().textContent?.trim()).toBe(SEND)
  expect(backButton().disabled).toBe(false)
}

describe('the forgot form', () => {
  it('renders the field, the submit, Back and an always-mounted empty status region', async () => {
    await mountForm()
    expect(container.textContent).toContain('Enter your work email. If it has an account, we will send a link to choose a new password.')
    expect(Array.from(container.querySelectorAll('label'), (l) => l.textContent)).toEqual(['Work email'])
    expect(emailInput().value, 'D25: starts empty').toBe('')
    expect(submitButton().textContent?.trim()).toBe(SEND)
    expect(submitButton().className).toContain('ds-btn--primary')
    expect(backButton().type).toBe('button')
    expect(statusText(), 'the region is mounted and empty before a send').toBe('')
    expect(alerts()).toEqual([])
  })

  it('Back to sign in calls onBack', async () => {
    const onBack = vi.fn()
    await mountForm(onBack)
    await act(async () => {
      backButton().click()
    })
    expect(onBack).toHaveBeenCalledTimes(1)
  })

  it('an invalid address sends nothing', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    await mountForm()

    await submit()
    expect(alerts(), 'empty').toEqual([EMAIL_REQUIRED])
    expect(emailInput().getAttribute('aria-invalid')).toBe('true')

    await fillEmail('not-an-email')
    await submit()
    expect(alerts(), 'malformed').toEqual([EMAIL_INVALID])
    expect(fetchMock).not.toHaveBeenCalled()
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('one submit posts once and disables the form while sending', async () => {
    let release!: (r: Response) => void
    const fetchMock = vi.fn().mockReturnValue(
      new Promise<Response>((resolve) => {
        release = resolve
      }),
    )
    vi.stubGlobal('fetch', fetchMock)
    await mountForm()
    await fillEmail('  a@corp.example ')

    await submit()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe(URL_)
    expect(init.method).toBe('POST')
    expect(init.body).toBe('{"email":"a@corp.example"}')

    expect(emailInput().disabled).toBe(true)
    expect(submitButton().disabled).toBe(true)
    expect(submitButton().textContent).toContain(SENDING)
    expect(backButton().disabled).toBe(true)

    // A second click and a raw submit event: neither may send.
    await submit()
    await act(async () => {
      form().dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    })
    expect(fetchMock, 'a second submit sends nothing').toHaveBeenCalledTimes(1)

    await act(async () => release(jsonResponse(202, { status: 'accepted' })))
    await flush()
    expectIdle()
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('every 202 shows the same notice', async () => {
    // internal/gateway/password_reset.go answers {"status":"accepted"}; the text must not depend on the body.
    const bodies: unknown[] = [{ status: 'accepted' }, {}]
    let n = 0
    const fetchMock = vi.fn().mockImplementation(async () => jsonResponse(202, bodies[n++]))
    vi.stubGlobal('fetch', fetchMock)
    await mountForm()

    for (const [i, body] of bodies.entries()) {
      await fillEmail('a@corp.example')
      await submit()
      await flush()
      expect(statusText(), `202 ${JSON.stringify(body)}`).toBe(SENT)
      expect(fetchMock).toHaveBeenCalledTimes(i + 1)
      expect(alerts()).toEqual([])
      expectIdle()
    }
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('a failed request shows the failure alert', async () => {
    const answers: [string, () => Response | Promise<Response>][] = [
      ['400', () => jsonResponse(400, { error: 'invalid email address' })],
      ['502', () => new Response('bad gateway', { status: 502 })],
      ['503', () => jsonResponse(503, { error: 'registration is not configured' })],
      ['network failure', () => Promise.reject(new TypeError('Failed to fetch'))],
    ]
    for (const [label, answer] of answers) {
      vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => answer()))
      await mountForm()
      await fillEmail('a@corp.example')
      await submit()
      await flush()
      expect(alerts(), label).toEqual([RESEND_FAILED])
      expect(statusText(), `${label}: no success notice`).toBe('')
      expectIdle()
      await act(async () => root.render(null))
    }

    vi.stubEnv('VITE_GATEWAY_URL', '')
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    await mountForm()
    await fillEmail('a@corp.example')
    await submit()
    await flush()
    expect(alerts(), 'gateway unset').toEqual([RESEND_FAILED])
    expect(fetchMock).not.toHaveBeenCalled()
    expectIdle()
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('a new submit removes the old outcome before sending', async () => {
    let release!: (r: Response) => void
    const answers: (() => Response | Promise<Response>)[] = [
      () => jsonResponse(503, {}),
      () =>
        new Promise<Response>((resolve) => {
          release = resolve
        }),
      () => jsonResponse(503, {}),
    ]
    let n = 0
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => answers[n++]()))
    await mountForm()
    await fillEmail('a@corp.example')
    await submit()
    await flush()
    expect(alerts(), 'control: the first request failed').toEqual([RESEND_FAILED])

    await submit()
    expect(submitButton().textContent, 'control: the second request is in flight').toContain(SENDING)
    expect(alerts(), 'the old alert is gone while the next request runs').toEqual([])

    await act(async () => release(jsonResponse(202, { status: 'accepted' })))
    await flush()
    expect(statusText()).toBe(SENT)

    await submit()
    await flush()
    expect(statusText(), 'the old notice is gone after a failure').toBe('')
    expect(alerts()).toEqual([RESEND_FAILED])
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('useResend with a given sender', () => {
  // A probe, so the hook runs under React; it renders what the hook returns.
  function Probe({ send, grab }: { send: (email: string) => Promise<void>; grab: (r: ReturnType<typeof useResend>) => void }) {
    grab(useResend(undefined, send))
    return null
  }

  it('useResend sends through the given sender', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    const send = vi.fn().mockResolvedValue(undefined)
    let latest!: ReturnType<typeof useResend>
    await act(async () => {
      root.render(createElement(Probe, { send, grab: (r) => (latest = r) }))
    })

    await act(async () => {
      await latest.resend('a@corp.example')
    })

    expect(send).toHaveBeenCalledTimes(1)
    expect(send).toHaveBeenCalledWith('a@corp.example')
    expect(fetchMock, 'the default sender is not used').not.toHaveBeenCalled()
    expect(latest.note).toEqual({ ok: true })
    expect(latest.resending).toBe(false)

    send.mockRejectedValueOnce(new Error('refused'))
    await act(async () => {
      await latest.resend('b@corp.example')
    })
    expect(send).toHaveBeenLastCalledWith('b@corp.example')
    expect(latest.note).toEqual({ ok: false })
  })
})
