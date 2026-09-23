// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// Mode A (RED) — transcribes Test Specs D1-D9. Mounts the real <DemoCta /> (not
// DemoLeadForm directly, which DemoLeadForm.dom.test.tsx already drives) so these
// rows pin DemoCta's own wiring of idPrefix="dc"/variant="card". Harness copied
// from DemoModal.retry.dom.test.tsx; fetch is the seam per [fetch-is-the-seam] —
// no vi.mock in this package.
/// <reference types="node" />
import { act, createElement } from 'react'
import type { ReactElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { DemoCta } from './DemoCta'
import { CONSENT_TEXT, DEFAULT_TAXPAYER_SIZE } from './demoForm'
import { buildSubmission, hubspotTarget, submissionUrl, type DemoLead } from '../hubspot'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// DemoCta's prop shape is mid-migration (loses onBookDemo); erase it so this
// file typechecks on both sides of that edit.
const Cta = DemoCta as unknown as () => ReactElement

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
  vi.useRealTimers()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function mount(): Promise<void> {
  await act(async () => {
    root.render(createElement(Cta))
  })
}

function $<T extends HTMLElement>(sel: string): T {
  const el = document.querySelector<T>(sel)
  expect(el, `expected ${sel}`).not.toBeNull()
  return el!
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

async function fillValid(): Promise<void> {
  await act(async () => {
    typeInto($('#dc-name'), 'Ada Okafor')
    typeInto($('#dc-email'), 'ada@okafor.ng')
    typeInto($('#dc-company'), 'Okafor & Partners')
  })
  await act(async () => {
    $<HTMLInputElement>('#dc-consent').click()
  })
}

async function flushAsync(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

describe('D1 (AC-T1, NEW-BEHAVIOUR): typing and selecting change the values', () => {
  it('the three inputs and the size select read back what was typed/chosen', async () => {
    await mount()
    await act(async () => {
      typeInto($('#dc-name'), 'Ada Okafor')
      typeInto($('#dc-email'), 'ada@okafor.ng')
      typeInto($('#dc-company'), 'Okafor & Partners')
    })
    await act(async () => {
      const sizeSelect = $<HTMLSelectElement>('#dc-size')
      sizeSelect.value = 'Large ₦5bn+'
      sizeSelect.dispatchEvent(new Event('change', { bubbles: true }))
    })

    expect(($('#dc-name') as HTMLInputElement).value).toBe('Ada Okafor')
    expect(($('#dc-email') as HTMLInputElement).value).toBe('ada@okafor.ng')
    expect(($('#dc-company') as HTMLInputElement).value).toBe('Okafor & Partners')
    expect(($('#dc-size') as HTMLSelectElement).value).toBe('Large ₦5bn+')
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('D2 (AC-T1, NEW-BEHAVIOUR): each missing required field names itself and takes focus', () => {
  it('walks name -> email -> company on successive blank submits', async () => {
    await mount()
    const submit = $<HTMLButtonElement>('button[type="submit"]')

    await act(async () => submit.click())
    expect($('#dc-name-error').getAttribute('role')).toBe('alert')
    expect(($('#dc-name') as HTMLInputElement).getAttribute('aria-invalid')).toBe('true')
    expect(document.activeElement?.id).toBe('dc-name')

    await act(async () => typeInto($('#dc-name'), 'Ada Okafor'))
    await act(async () => submit.click())
    expect(document.activeElement?.id).toBe('dc-email')

    await act(async () => typeInto($('#dc-email'), 'ada@okafor.ng'))
    await act(async () => submit.click())
    expect(document.activeElement?.id).toBe('dc-company')
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('D3 (AC-T1, NEW-BEHAVIOUR): an unticked consent blocks the submit', () => {
  it('leaves the form up with a consent error, no success', async () => {
    await mount()
    await act(async () => {
      typeInto($('#dc-name'), 'Ada Okafor')
      typeInto($('#dc-email'), 'ada@okafor.ng')
      typeInto($('#dc-company'), 'Okafor & Partners')
    })
    await act(async () => $<HTMLButtonElement>('button[type="submit"]').click())

    expect($('#dc-consent-error')).not.toBeNull()
    expect(document.activeElement?.id).toBe('dc-consent')
    expect(document.querySelector('#dc-name')).not.toBeNull()
    expect(container.textContent).not.toContain("You're booked")
  })
})

describe('D4 (AC-1.5, NEW-BEHAVIOUR): a valid submit reaches the wire once and shows the thank-you', () => {
  it('POSTs the seven mapped fields to the HubSpot target and swaps in the success panel', async () => {
    openGate()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    await mount()
    await fillValid()
    await act(async () => $<HTMLButtonElement>('button[type="submit"]').click())
    await flushAsync()

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const call = fetchMock.mock.calls[0]
    const target = hubspotTarget()
    expect(target).not.toBeNull()
    expect(call?.[0]).toBe(submissionUrl(target!))
    expect((call?.[1] as RequestInit | undefined)?.method).toBe('POST')

    const lead: DemoLead = {
      name: 'Ada Okafor',
      email: 'ada@okafor.ng',
      company: 'Okafor & Partners',
      role: 'Finance or Accounting lead',
      size: DEFAULT_TAXPAYER_SIZE,
      volume: '1k–10k',
      consent: true,
    }
    expect(JSON.parse((call?.[1] as RequestInit)?.body as string)).toEqual(buildSubmission(lead, CONSENT_TEXT))

    expect(container.textContent).toContain("You're booked")
    expect(container.querySelector('#dc-name')).toBeNull()
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('D5 ([card-success-focus], NEW-BEHAVIOUR): the success panel takes focus and offers no Done', () => {
  it('a successful submit focuses #dc-success and renders no #dc-success-done', async () => {
    openGate()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, status: 200 }))
    await mount()
    await fillValid()
    await act(async () => $<HTMLButtonElement>('button[type="submit"]').click())
    await flushAsync()

    expect(document.activeElement?.id).toBe('dc-success')
    expect(container.querySelector('#dc-success-done')).toBeNull()
  })
})

describe('D6 (AC-T1, NEW-BEHAVIOUR): a tripped honeypot sends nothing, still thanks', () => {
  it('bypasses the wire and reaches the success panel after the stub delay', async () => {
    openGate()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    await mount()
    await fillValid()
    // Uncontrolled and read straight off the form — a direct DOM write is what a
    // naive bot does.
    $<HTMLInputElement>('input[name="website"]').value = 'https://spam.example'

    vi.useFakeTimers()
    await act(async () => $<HTMLButtonElement>('button[type="submit"]').click())
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1400)
    })
    vi.useRealTimers()

    expect(fetchMock).not.toHaveBeenCalled()
    expect(container.textContent).toContain("You're booked")
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('D7 (AC-1.3 failure half, NEW-BEHAVIOUR): retry without retyping, re-focuses the name field', () => {
  it('shows the error panel, restores every typed value plus consent, and re-focuses dc-name', async () => {
    openGate()
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('net')))
    await mount()
    await fillValid()
    await act(async () => $<HTMLButtonElement>('button[type="submit"]').click())
    await flushAsync()

    expect(container.textContent).toContain('Something went wrong')
    const retry = $<HTMLButtonElement>('#dc-error-retry')
    expect(document.activeElement).toBe(retry)

    await act(async () => retry.click())

    expect(($('#dc-name') as HTMLInputElement).value).toBe('Ada Okafor')
    expect(($('#dc-email') as HTMLInputElement).value).toBe('ada@okafor.ng')
    expect(($('#dc-company') as HTMLInputElement).value).toBe('Okafor & Partners')
    expect(($('#dc-consent') as HTMLInputElement).checked).toBe(true)
    expect(document.activeElement?.id).toBe('dc-name')
  })
})

// Measured GREEN at HEAD too: the facade has no focus-management code at all, so
// document.activeElement is vacuously document.body. Stays green post-implementation
// because DemoLeadForm's firstFormPanel skip (variant==='card') suppresses the effect.
// D7 above is this row's partner — together they prove the skip is real, not absent.
describe('D8 ([card-no-mount-focus]): no focus steal on first paint', () => {
  it('mounting the card leaves document.body focused, not #dc-name', async () => {
    await mount()
    expect(document.activeElement).toBe(document.body)
  })
})

describe('D9 (AC-1.3, NEW-BEHAVIOUR): a double click does not send twice', () => {
  it('a second click while submitting reaches the wire only once, and the button disables', async () => {
    openGate()
    const fetchMock = vi.fn(() => new Promise(() => {})) // never settles
    vi.stubGlobal('fetch', fetchMock)
    await mount()
    await fillValid()
    const submit = $<HTMLButtonElement>('button[type="submit"]')

    await act(async () => submit.click())
    await act(async () => submit.click())

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(submit.disabled).toBe(true)
    expect(consoleError).not.toHaveBeenCalled()
  })
})
