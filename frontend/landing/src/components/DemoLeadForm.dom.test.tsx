// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// QA Mode B gap-fill, behavioural half. DemoModal.retry.dom.test.tsx drives the popup;
// these rows drive the shared form directly, so the guarantees are pinned on the
// component both surfaces mount rather than on one caller. Seam per the plan: the real
// HubSpot gate opened with vi.stubEnv, fetch stubbed, no vi.mock of the component.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { DemoLeadForm } from './DemoLeadForm'
import type { DemoLead } from '../hubspot'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

let container: HTMLDivElement
let root: Root
let consoleError: ReturnType<typeof vi.spyOn>
let consoleLog: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
  consoleLog = vi.spyOn(console, 'log').mockImplementation(() => undefined)
})

afterEach(async () => {
  await act(async () => root.unmount())
  container.remove()
  vi.useRealTimers()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function openGate(): void {
  vi.stubEnv('VITE_HUBSPOT_PORTAL_ID', '148915098')
  vi.stubEnv('VITE_HUBSPOT_FORM_GUID', 'abc-123')
}

// React's value tracker swallows a plain `input.value = …` write on a controlled input.
function typeInto(input: HTMLInputElement, value: string): void {
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  setValue.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

async function mountCard(props: Partial<Parameters<typeof DemoLeadForm>[0]> = {}): Promise<void> {
  await act(async () => {
    root.render(createElement(DemoLeadForm, { idPrefix: 'dc', variant: 'card', ...props }))
  })
}

function $<T extends HTMLElement>(sel: string): T {
  const el = container.querySelector<T>(sel)
  expect(el, `expected ${sel}`).not.toBeNull()
  return el!
}

async function fillValid(prefix = 'dc'): Promise<void> {
  await act(async () => {
    typeInto($<HTMLInputElement>(`#${prefix}-name`), 'Ada Okafor')
    typeInto($<HTMLInputElement>(`#${prefix}-email`), 'ada@okafor.ng')
    typeInto($<HTMLInputElement>(`#${prefix}-company`), 'Okafor & Partners')
  })
  await act(async () => {
    $<HTMLInputElement>(`#${prefix}-consent`).click()
  })
}

async function flushAsync(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

describe('B1: a tripped honeypot is dropped silently', () => {
  it('never reaches the wire, yet still lands on the success panel after the stub delay', async () => {
    openGate()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    await mountCard()
    await fillValid()

    // Uncontrolled and read straight off the form, so a direct DOM write is exactly
    // what a naive bot does.
    $<HTMLInputElement>('input[name="website"]').value = 'https://spam.example'

    vi.useFakeTimers()
    await act(async () => {
      $<HTMLButtonElement>('button[type="submit"]').click()
    })
    expect(fetchMock).not.toHaveBeenCalled()
    expect(container.textContent).toContain('Booking…')

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1400)
    })
    vi.useRealTimers()

    // Indistinguishable from a real submit: same terminal panel, still no request,
    // and nothing written to the console that would tell a bot it was caught.
    expect(container.textContent).toContain("You're booked")
    expect(fetchMock).not.toHaveBeenCalled()
    expect(consoleError).not.toHaveBeenCalled()
    expect(consoleLog).not.toHaveBeenCalled()
  })
})

describe('B2: a failed validation focuses the first field at fault', () => {
  it('walks name → email → company → consent, not always the first input', async () => {
    openGate()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    await mountCard()

    const submit = $<HTMLButtonElement>('button[type="submit"]')

    // Nothing filled: name is at fault first.
    await act(async () => submit.click())
    expect(document.activeElement?.id).toBe('dc-name')
    expect(fetchMock).not.toHaveBeenCalled()

    // Name only: email is next.
    await act(async () => typeInto($<HTMLInputElement>('#dc-name'), 'Ada Okafor'))
    await act(async () => submit.click())
    expect(document.activeElement?.id).toBe('dc-email')

    // A malformed email is still an email fault, not a company one.
    await act(async () => typeInto($<HTMLInputElement>('#dc-email'), 'ada-at-okafor'))
    await act(async () => submit.click())
    expect(document.activeElement?.id).toBe('dc-email')
    expect(container.textContent).toContain('Enter a valid work email address.')

    // Valid email, blank company.
    await act(async () => typeInto($<HTMLInputElement>('#dc-email'), 'ada@okafor.ng'))
    await act(async () => submit.click())
    expect(document.activeElement?.id).toBe('dc-company')

    // Everything but the box: consent is last and still blocks.
    await act(async () => typeInto($<HTMLInputElement>('#dc-company'), 'Okafor & Partners'))
    await act(async () => submit.click())
    expect(document.activeElement?.id).toBe('dc-consent')
    expect(container.textContent).toContain('Please confirm you agree before we can contact you.')
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('B3: the card surface owns the same failure/retry contract as the popup', () => {
  it('shows the error panel, retries without retyping, and re-focuses the first field', async () => {
    openGate()
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('net')))
    await mountCard()
    await fillValid()

    await act(async () => $<HTMLButtonElement>('button[type="submit"]').click())
    await flushAsync()

    expect(container.textContent).toContain('Something went wrong')
    const retry = $<HTMLButtonElement>('#dc-error-retry')
    expect(document.activeElement).toBe(retry)

    await act(async () => retry.click())

    expect($<HTMLInputElement>('#dc-name').value).toBe('Ada Okafor')
    expect($<HTMLInputElement>('#dc-email').value).toBe('ada@okafor.ng')
    expect($<HTMLInputElement>('#dc-company').value).toBe('Okafor & Partners')
    expect($<HTMLInputElement>('#dc-consent').checked).toBe(true)
    // A card mounts unfocused, but a panel that appears LATER still claims focus.
    expect(document.activeElement?.id).toBe('dc-name')
  })

  it('without onDone the success panel renders no button and takes focus itself', async () => {
    openGate()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, status: 200 }))
    await mountCard()
    await fillValid()

    await act(async () => $<HTMLButtonElement>('button[type="submit"]').click())
    await flushAsync()

    expect(container.textContent).toContain("You're booked")
    expect(container.querySelector('#dc-success-done')).toBeNull()
    expect(document.activeElement).toBe($<HTMLElement>('#dc-success'))
  })
})

describe('B4: two instances in one document share nothing', () => {
  it('every id is unique and typing in one form leaves the other empty', async () => {
    await act(async () => {
      root.render(
        createElement(
          'div',
          null,
          createElement(DemoLeadForm, { key: 'dm', idPrefix: 'dm', variant: 'modal' }),
          createElement(DemoLeadForm, { key: 'dc', idPrefix: 'dc', variant: 'card' }),
        ),
      )
    })

    const ids = Array.from(container.querySelectorAll<HTMLElement>('[id]')).map((el) => el.id)
    expect(ids.length).toBe(14)
    expect(new Set(ids).size).toBe(ids.length)

    await act(async () => typeInto($<HTMLInputElement>('#dm-name'), 'Popup Person'))
    await act(async () => $<HTMLInputElement>('#dc-consent').click())

    expect($<HTMLInputElement>('#dm-name').value).toBe('Popup Person')
    expect($<HTMLInputElement>('#dc-name').value).toBe('')
    expect($<HTMLInputElement>('#dc-consent').checked).toBe(true)
    expect($<HTMLInputElement>('#dm-consent').checked).toBe(false)

    // Each label still points at its own control.
    for (const prefix of ['dm', 'dc']) {
      const label = Array.from(container.querySelectorAll('label')).find((l) => l.htmlFor === `${prefix}-name`)
      expect(label, `expected a label for ${prefix}-name`).toBeDefined()
    }
  })
})

describe('B5: the in-flight guard is on the handler, not only the button', () => {
  it('a submit event dispatched while submitting does not reach the wire twice', async () => {
    openGate()
    const fetchMock = vi.fn(() => new Promise(() => {}))
    vi.stubGlobal('fetch', fetchMock)
    await mountCard()
    await fillValid()

    const form = $<HTMLFormElement>('form')
    await act(async () => $<HTMLButtonElement>('button[type="submit"]').click())
    expect(fetchMock).toHaveBeenCalledTimes(1)

    // Bypasses `disabled` entirely — Enter in a text field, or any script, can do this.
    await act(async () => {
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    })

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('B6: the injected submit prop still short-circuits the wire', () => {
  it('an injected submit is awaited instead of HubSpot, and its rejection routes to the error panel', async () => {
    openGate()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    const injected = vi.fn<(lead: DemoLead) => Promise<void>>().mockRejectedValue(new Error('injected'))
    await mountCard({ submit: injected })
    await fillValid()

    await act(async () => $<HTMLButtonElement>('button[type="submit"]').click())
    await flushAsync()

    expect(injected).toHaveBeenCalledTimes(1)
    expect(injected.mock.calls[0][0]).toEqual({
      name: 'Ada Okafor',
      email: 'ada@okafor.ng',
      company: 'Okafor & Partners',
      role: 'Finance or Accounting lead',
      size: 'Medium ₦1bn–₦5bn',
      volume: '1k–10k',
      consent: true,
    })
    expect(fetchMock).not.toHaveBeenCalled()
    expect(container.textContent).toContain('Something went wrong')
  })
})

describe('B7: importing the module is inert in a DOM environment too', () => {
  it('no fetch, no listener, no DOM node created at module scope', async () => {
    vi.resetModules()
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    const addListener = vi.spyOn(window, 'addEventListener')
    const bodyChildren = document.body.childElementCount

    const mod = await import('./DemoLeadForm')

    expect(mod).toHaveProperty('DemoLeadForm')
    expect(typeof mod.DEMO_FORM_CSS).toBe('string')
    expect(fetchMock).not.toHaveBeenCalled()
    expect(addListener).not.toHaveBeenCalled()
    expect(document.body.childElementCount).toBe(bodyChildren)
  })
})
