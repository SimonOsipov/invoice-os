// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// Mode A (RED) — transcribes Test Specs S11, S12, S14, S15 for BUG-19-01. All four are
// CHARACTERIZATION: today's DemoModal already implements the double-submit guard and the
// error/retry flow, so these must be GREEN now and stay GREEN after the extraction — they
// are this subtask's regression oracle for [error-retry-gets-an-oracle]. DOM harness copied
// from SignInModal.dom.test.tsx; no @testing-library in this package. Real timers throughout
// — with the gate open runStub never runs, so nothing here needs fake timers.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { DemoModal } from './DemoModal'
import { CONSENT_TEXT, DEFAULT_TAXPAYER_SIZE } from './demoForm'
import { buildSubmission, hubspotTarget, submissionUrl, type DemoLead } from '../hubspot'
import { ensureTag } from '../analytics'
import { CONSENT_VERSION } from '../consent'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

function noop() {}

let container: HTMLDivElement
let root: Root
let consoleError: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
  vi.stubEnv('VITE_GATEWAY_URL', '')
})

afterEach(async () => {
  await act(async () => root.unmount())
  container.remove()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function mount(onClose: () => void = noop): Promise<void> {
  await act(async () => {
    root.render(createElement(DemoModal, { onClose }))
  })
}

function dialog(): HTMLElement {
  const d = document.querySelector<HTMLElement>('[role="dialog"]')
  expect(d, 'expected the demo dialog').not.toBeNull()
  return d!
}

// React's value tracker swallows a plain `input.value = …` write; the native prototype
// setter bypasses the tracker so the subsequent 'input' event carries a real change.
function typeInto(input: HTMLInputElement, value: string): void {
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  setValue.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

function openGate(): void {
  vi.stubEnv('VITE_HUBSPOT_PORTAL_ID', '148915098')
  vi.stubEnv('VITE_HUBSPOT_FORM_GUID', 'abc-123')
}

async function fillValidForm(d: HTMLElement): Promise<void> {
  await act(async () => {
    typeInto(d.querySelector<HTMLInputElement>('#dm-name')!, 'Ada Okafor')
    typeInto(d.querySelector<HTMLInputElement>('#dm-email')!, 'ada@okafor.ng')
    typeInto(d.querySelector<HTMLInputElement>('#dm-company')!, 'Okafor & Partners')
  })
  await act(async () => {
    d.querySelector<HTMLInputElement>('#dm-consent')!.click()
  })
}

const GATEWAY_URL = 'https://gw.x/contacts/demo-request'

// Routes by URL so the HubSpot Forms host and the gateway answer separately.
function routeFetch(hs: () => Promise<Response> | Response, gw: () => Promise<Response> | Response) {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
  const fetchMock = vi.fn<typeof fetch>(async (url) => (String(url) === GATEWAY_URL ? gw() : hs()))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}
const hsOk = () => new Response('{}', { status: 200 })
const gwAccepted = () => new Response(JSON.stringify({ status: 'accepted' }), { status: 202 })
const gwError = (status: number, error: string) => () => new Response(JSON.stringify({ error }), { status })
const urls = (fetchMock: { mock: { calls: Parameters<typeof fetch>[] } }) => fetchMock.mock.calls.map((c) => String(c[0]))

// A real macrotask boundary drains the whole microtask queue first, so this settles
// handleSubmit's multi-hop await chain (fetch -> submitDemoLead -> trackedHubSpotSubmit)
// deterministically, unlike a single `await Promise.resolve()`.
async function flushAsync(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

describe('S11 (CHARACTERIZATION, regression oracle): mount focus', () => {
  it('focuses #dm-name on mount', async () => {
    await mount()
    dialog()
    expect(document.activeElement?.id).toBe('dm-name')
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('S12 (CHARACTERIZATION, regression oracle): double-submit guard', () => {
  it('a second submit while submitting does not reach the wire twice', async () => {
    openGate()
    const fetchMock = routeFetch(() => new Promise<Response>(() => {}), gwAccepted) // HubSpot never settles
    await mount()
    const d = dialog()
    await fillValidForm(d)
    const submitButton = d.querySelector<HTMLButtonElement>('button[type="submit"]')!

    await act(async () => {
      submitButton.click()
    })
    await act(async () => {
      submitButton.click()
    })

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(submitButton.disabled).toBe(true)
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('AUTH-17-08: the double-submit guard covers the gateway call', () => {
  it('a second submit while the gateway call is in flight does not post twice', async () => {
    openGate()
    const fetchMock = routeFetch(hsOk, () => new Promise<Response>(() => {}))
    await mount()
    const d = dialog()
    await fillValidForm(d)
    const submitButton = d.querySelector<HTMLButtonElement>('button[type="submit"]')!

    await act(async () => {
      submitButton.click()
    })
    await flushAsync()
    expect(urls(fetchMock), 'control: HubSpot answered, the gateway call is pending').toHaveLength(2)

    await act(async () => {
      d.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    })
    await flushAsync()

    expect(urls(fetchMock)).toEqual([expect.stringContaining('hsforms.com'), GATEWAY_URL])
    expect(submitButton.disabled).toBe(true)
  })
})

describe('S14 (CHARACTERIZATION, regression oracle): failure state retries without retyping', () => {
  it('renders the error panel then restores the typed form on retry', async () => {
    openGate()
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('net')))
    await mount()
    const d = dialog()
    await fillValidForm(d)
    const submitButton = d.querySelector<HTMLButtonElement>('button[type="submit"]')!

    await act(async () => {
      submitButton.click()
    })
    await flushAsync()

    expect(d.textContent).toContain('Something went wrong')
    const retryButton = document.getElementById('dm-error-retry') as HTMLButtonElement | null
    expect(retryButton, 'expected #dm-error-retry').not.toBeNull()
    if (!retryButton) return
    expect(document.activeElement).toBe(retryButton)

    await act(async () => {
      retryButton.click()
    })

    expect((document.getElementById('dm-name') as HTMLInputElement).value).toBe('Ada Okafor')
    expect((document.getElementById('dm-email') as HTMLInputElement).value).toBe('ada@okafor.ng')
    expect((document.getElementById('dm-company') as HTMLInputElement).value).toBe('Okafor & Partners')
    expect((document.getElementById('dm-consent') as HTMLInputElement).checked).toBe(true)
    expect(document.activeElement?.id).toBe('dm-name')
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('AUTH-17-08: a failure on either call shows the error panel and a retry resends both', () => {
  it('a HubSpot failure shows the error panel and skips the gateway', async () => {
    openGate()
    let hs: () => Response = () => new Response('{}', { status: 500 })
    const fetchMock = routeFetch(() => hs(), gwAccepted)
    await mount()
    const d = dialog()
    await fillValidForm(d)
    await act(async () => d.querySelector<HTMLButtonElement>('button[type="submit"]')!.click())
    await flushAsync()

    expect(d.textContent).toContain('Something went wrong')
    expect(urls(fetchMock), 'only the Forms call was made').toEqual([expect.stringContaining('hsforms.com')])

    hs = hsOk
    await act(async () => document.getElementById('dm-error-retry')!.click())
    await act(async () => d.querySelector<HTMLButtonElement>('button[type="submit"]')!.click())
    await flushAsync()

    expect(urls(fetchMock)).toEqual([expect.stringContaining('hsforms.com'), expect.stringContaining('hsforms.com'), GATEWAY_URL])
    expect(d.textContent).toContain("You're booked")
  })

  const failures: [string, () => Promise<Response> | Response][] = [
    ['a 400', gwError(400, 'email is invalid')],
    ['a 502', gwError(502, 'demo request is unavailable')],
    ['a network rejection', () => Promise.reject(new TypeError('Failed to fetch'))],
  ]
  for (const [label, failing] of failures) {
    it(`a gateway failure shows the error panel (${label}) and a retry resends both`, async () => {
      openGate()
      let gw = failing
      const fetchMock = routeFetch(hsOk, () => gw())
      await mount()
      const d = dialog()
      await fillValidForm(d)
      await act(async () => d.querySelector<HTMLButtonElement>('button[type="submit"]')!.click())
      await flushAsync()

      expect(d.textContent).toContain('Something went wrong')
      expect(d.textContent).not.toContain("You're booked")
      expect(urls(fetchMock)).toEqual([expect.stringContaining('hsforms.com'), GATEWAY_URL])

      gw = gwAccepted
      await act(async () => document.getElementById('dm-error-retry')!.click())
      await act(async () => d.querySelector<HTMLButtonElement>('button[type="submit"]')!.click())
      await flushAsync()

      expect(urls(fetchMock).filter((u) => u.includes('hsforms.com'))).toHaveLength(2)
      expect(urls(fetchMock).filter((u) => u === GATEWAY_URL)).toHaveLength(2)
      expect(d.textContent).toContain("You're booked")
    })
  }
})

describe('RESEND2-02-02: a gateway 429 shows the try-later panel', () => {
  async function submit(d: HTMLElement) {
    await act(async () => d.querySelector<HTMLButtonElement>('button[type="submit"]')!.click())
    await flushAsync()
  }

  it('a gateway 429 shows the try-later panel', async () => {
    openGate()
    routeFetch(hsOk, gwError(429, 'too many requests'))
    await mount()
    const d = dialog()
    await fillValidForm(d)
    await submit(d)

    expect(d.textContent).toContain('Too many requests')
    expect(d.textContent).toContain('try again later')
    expect(d.textContent).not.toContain('Something went wrong')
    expect(d.textContent).not.toContain("You're booked")
  })

  it('a HubSpot 429 keeps the generic panel', async () => {
    openGate()
    const fetchMock = routeFetch(() => new Response('{}', { status: 429 }), gwAccepted)
    await mount()
    const d = dialog()
    await fillValidForm(d)
    await submit(d)

    expect(d.textContent).toContain('Something went wrong')
    expect(d.textContent).not.toContain('Too many requests')
    expect(urls(fetchMock)).not.toContain(GATEWAY_URL)
  })

  it('after a 429 Try again restores the form and a later accept books', async () => {
    openGate()
    let gw: () => Response = gwError(429, 'too many requests')
    routeFetch(hsOk, () => gw())
    await mount()
    const d = dialog()
    await fillValidForm(d)
    await submit(d)
    expect(d.textContent).toContain('Too many requests')

    await act(async () => document.getElementById('dm-error-retry')!.click())
    expect((document.getElementById('dm-name') as HTMLInputElement).value).toBe('Ada Okafor')
    expect((document.getElementById('dm-email') as HTMLInputElement).value).toBe('ada@okafor.ng')
    expect((document.getElementById('dm-company') as HTMLInputElement).value).toBe('Okafor & Partners')

    gw = gwAccepted
    await submit(d)
    expect(d.textContent).toContain("You're booked")
  })

  it('a gateway 429 shows the exact try-later copy and keeps the Try again button', async () => {
    openGate()
    routeFetch(hsOk, gwError(429, 'too many requests'))
    await mount()
    const d = dialog()
    await fillValidForm(d)
    await submit(d)

    expect(d.querySelector('h3')?.textContent).toBe('Too many requests')
    expect(d.querySelector('p.t-body-sm')?.textContent).toBe(
      'Too many demo requests came from your network. Please try again later \u2014 your details are still here.',
    )
    expect(d.querySelector('#dm-error-retry')?.textContent).toBe('Try again')
  })

  it('a gateway network error keeps the generic panel', async () => {
    openGate()
    routeFetch(hsOk, () => {
      throw new TypeError('Failed to fetch')
    })
    await mount()
    const d = dialog()
    await fillValidForm(d)
    await submit(d)

    expect(d.textContent).toContain('Something went wrong')
    expect(d.textContent).not.toContain('Too many requests')
  })

  it('a gateway 429 reports generate_lead and no demo_submit_failed only the HubSpot outcome reports', async () => {
    vi.stubEnv('VITE_GA_MEASUREMENT_ID', 'G-E409H76XYY')
    expect(ensureTag('www.ascomply.com', { analytics: true, ts: '2026-01-01T00:00:00.000Z', v: CONSENT_VERSION })).toBe(true)
    const gtag = vi.fn()
    ;(window as unknown as { gtag: unknown }).gtag = gtag
    openGate()
    routeFetch(hsOk, gwError(429, 'too many requests'))
    await mount()
    const d = dialog()
    await fillValidForm(d)
    await submit(d)

    expect(d.textContent).toContain('Too many requests')
    const names = gtag.mock.calls.map((c) => c[1])
    expect(names).toContain('generate_lead')
    expect(names).not.toContain('demo_submit_failed')
  })

  it('a later non-429 failure goes back to the generic panel', async () => {
    openGate()
    let gw: () => Response = gwError(429, 'too many requests')
    routeFetch(hsOk, () => gw())
    await mount()
    const d = dialog()
    await fillValidForm(d)
    await submit(d)
    await act(async () => document.getElementById('dm-error-retry')!.click())
    gw = gwError(502, 'demo request is unavailable')
    await submit(d)
    expect(d.textContent).toContain('Something went wrong')
    expect(d.textContent).not.toContain('Too many requests')
  })
})

describe('S15 (CHARACTERIZATION, regression oracle): a valid submit reaches HubSpot exactly once', () => {
  it('POSTs the seven mapped fields, then the gateway request, and focuses the success panel', async () => {
    openGate()
    const fetchMock = routeFetch(hsOk, gwAccepted)
    await mount()
    const d = dialog()
    await fillValidForm(d)
    const submitButton = d.querySelector<HTMLButtonElement>('button[type="submit"]')!

    await act(async () => {
      submitButton.click()
    })
    await flushAsync()

    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(String(fetchMock.mock.calls[1]?.[0]), 'the gateway call follows the Forms call').toBe(GATEWAY_URL)
    const call = fetchMock.mock.calls[0]
    const url = call?.[0] as string | undefined
    const init = call?.[1] as RequestInit | undefined
    const target = hubspotTarget()
    expect(target).not.toBeNull()
    expect(url).toBe(submissionUrl(target!))
    expect(init?.method).toBe('POST')

    const lead: DemoLead = {
      name: 'Ada Okafor',
      email: 'ada@okafor.ng',
      company: 'Okafor & Partners',
      role: 'Finance or Accounting lead',
      size: DEFAULT_TAXPAYER_SIZE,
      volume: '1k–10k',
      consent: true,
      marketing: false,
    }
    expect(JSON.parse(init?.body as string)).toEqual(buildSubmission(lead, CONSENT_TEXT))

    expect(d.textContent).toContain("You're booked")
    expect(document.activeElement?.id).toBe('dm-success-done')
    expect(consoleError).not.toHaveBeenCalled()
  })
})
