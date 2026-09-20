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
    const fetchMock = vi.fn(() => new Promise(() => {})) // never settles
    vi.stubGlobal('fetch', fetchMock)
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

describe('S15 (CHARACTERIZATION, regression oracle): a valid submit reaches HubSpot exactly once', () => {
  it('POSTs the seven mapped fields and focuses the success panel', async () => {
    openGate()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    await mount()
    const d = dialog()
    await fillValidForm(d)
    const submitButton = d.querySelector<HTMLButtonElement>('button[type="submit"]')!

    await act(async () => {
      submitButton.click()
    })
    await flushAsync()

    expect(fetchMock).toHaveBeenCalledTimes(1)
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
    }
    expect(JSON.parse(init?.body as string)).toEqual(buildSubmission(lead, CONSENT_TEXT))

    expect(d.textContent).toContain("You're booked")
    expect(document.activeElement?.id).toBe('dm-success-done')
    expect(consoleError).not.toHaveBeenCalled()
  })
})
