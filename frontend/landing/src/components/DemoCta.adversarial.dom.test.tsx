// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// QA gap-fill for the #demo card. D1-D9 drive the happy paths and one failure; these
// rows take the edges they leave open: simultaneous validation, whitespace-only input,
// the absolute option lists, long values, retry of the SELECT choices, focus order,
// the style block, and both surfaces submitted in one session.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { DemoCta } from './DemoCta'
import { DemoModal } from './DemoModal'
import { DEMO_FORM_CSS } from './DemoLeadForm'
import { CONSENT_TEXT, DEFAULT_TAXPAYER_SIZE, ROLE_OPTIONS, TAXPAYER_SIZE_OPTIONS, VOLUME_OPTIONS } from './demoForm'
import { buildSubmission, type DemoLead } from '../hubspot'

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
  vi.useRealTimers()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function mount(): Promise<void> {
  await act(async () => {
    root.render(createElement(DemoCta))
  })
}

function $<T extends HTMLElement>(sel: string): T {
  const el = container.querySelector<T>(sel)
  expect(el, `expected ${sel}`).not.toBeNull()
  return el!
}

// React's value tracker swallows a plain `input.value = …` write.
function typeInto(input: HTMLInputElement, value: string): void {
  const setValue = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  setValue.call(input, value)
  input.dispatchEvent(new Event('input', { bubbles: true }))
}

function choose(select: HTMLSelectElement, value: string): void {
  select.value = value
  select.dispatchEvent(new Event('change', { bubbles: true }))
}

function openGate(): void {
  vi.stubEnv('VITE_HUBSPOT_PORTAL_ID', '148915098')
  vi.stubEnv('VITE_HUBSPOT_FORM_GUID', 'abc-123')
}

async function flushAsync(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

async function fill(name: string, email: string, company: string): Promise<void> {
  await act(async () => {
    typeInto($('#dc-name'), name)
    typeInto($('#dc-email'), email)
    typeInto($('#dc-company'), company)
  })
}

async function submit(): Promise<void> {
  await act(async () => $<HTMLButtonElement>('button[type="submit"]').click())
}

describe('X1: a blank submit names every missing field at once', () => {
  it('renders all four errors, focuses only the first, and reaches no wire', async () => {
    openGate()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    await mount()
    await submit()

    for (const key of ['name', 'email', 'company', 'consent']) {
      const err = $(`#dc-${key}-error`)
      expect(err.getAttribute('role'), `#dc-${key}-error must be an alert`).toBe('alert')
      expect(err.textContent?.trim().length, `#dc-${key}-error must carry copy`).toBeGreaterThan(0)
      expect($(`#dc-${key}`).getAttribute('aria-invalid')).toBe('true')
    }
    expect(document.activeElement?.id).toBe('dc-name')
    expect(fetchMock).not.toHaveBeenCalled()
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('X2: whitespace-only answers are rejected like empty ones', () => {
  it('spaces in all three required fields still block the submit and send nothing', async () => {
    openGate()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    await mount()
    await fill('   ', '  \t ', ' \n ')
    await act(async () => $<HTMLInputElement>('#dc-consent').click())
    await submit()

    expect($('#dc-name-error')).not.toBeNull()
    expect($('#dc-email-error')).not.toBeNull()
    expect($('#dc-company-error')).not.toBeNull()
    expect(container.querySelector('#dc-consent-error'), 'a ticked consent must not error').toBeNull()
    expect(fetchMock).not.toHaveBeenCalled()
    expect(container.textContent).not.toContain("You're booked")
  })

  it('a malformed email is rejected even when the other two are filled', async () => {
    openGate()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    await mount()
    await fill('Ada Okafor', 'ada at okafor', 'Okafor & Partners')
    await act(async () => $<HTMLInputElement>('#dc-consent').click())
    await submit()

    expect($('#dc-email-error').textContent).toContain('valid work email')
    expect(document.activeElement?.id).toBe('dc-email')
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('X3: the card offers the imported option lists by value, not merely the popup\'s', () => {
  it('dc-role/size/volume each render Select… plus their constant, in order', async () => {
    await mount()
    const cases: [string, readonly string[]][] = [
      ['dc-role', ROLE_OPTIONS],
      ['dc-size', TAXPAYER_SIZE_OPTIONS],
      ['dc-volume', VOLUME_OPTIONS],
    ]
    for (const [id, options] of cases) {
      expect(options.length, `${id}'s constant must not be empty`).toBeGreaterThan(0)
      const rendered = Array.from($<HTMLSelectElement>(`#${id}`).options).map((o) => o.textContent)
      expect(rendered, `${id} options`).toEqual(['Select…', ...options])
    }
    expect($<HTMLSelectElement>('#dc-size').value).toBe(DEFAULT_TAXPAYER_SIZE)
  })
})

describe('X4: long answers survive the card intact', () => {
  it('a 600-character company reaches the wire untruncated and the thank-you names the visitor', async () => {
    openGate()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    const company = 'Ω'.repeat(600)
    const name = 'Adaobi'.repeat(40) + ' Okafor'
    await mount()
    await fill(name, 'ada@okafor.ng', company)
    await act(async () => $<HTMLInputElement>('#dc-consent').click())
    await submit()
    await flushAsync()

    const body = JSON.parse((fetchMock.mock.calls[0]?.[1] as RequestInit).body as string)
    const lead: DemoLead = {
      name,
      email: 'ada@okafor.ng',
      company,
      role: 'Finance or Accounting lead',
      size: DEFAULT_TAXPAYER_SIZE,
      volume: '1k–10k',
      consent: true,
    }
    expect(body).toEqual(buildSubmission(lead, CONSENT_TEXT))
    expect(container.textContent).toContain("You're booked")
    expect(container.textContent).toContain('Adaobi'.repeat(40))
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('X5: retry restores the select choices too, not only the typed text', () => {
  it('role, taxpayer size and volume all come back after a failed submit', async () => {
    openGate()
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('net')))
    await mount()
    await fill('Ada Okafor', 'ada@okafor.ng', 'Okafor & Partners')
    await act(async () => {
      choose($<HTMLSelectElement>('#dc-role'), 'Developer / IT')
      choose($<HTMLSelectElement>('#dc-size'), 'Below ₦50m')
      choose($<HTMLSelectElement>('#dc-volume'), '100k+')
    })
    await act(async () => $<HTMLInputElement>('#dc-consent').click())
    await submit()
    await flushAsync()

    await act(async () => $<HTMLButtonElement>('#dc-error-retry').click())

    expect($<HTMLSelectElement>('#dc-role').value).toBe('Developer / IT')
    expect($<HTMLSelectElement>('#dc-size').value).toBe('Below ₦50m')
    expect($<HTMLSelectElement>('#dc-volume').value).toBe('100k+')
    expect($<HTMLInputElement>('#dc-consent').checked).toBe(true)
  })
})

describe('X6: keyboard traversal reaches every control in reading order', () => {
  it('the tabbable sequence is the seven answers then the submit; the honeypot is skipped', async () => {
    await mount()
    const all = Array.from(container.querySelectorAll<HTMLElement>('input, select, button'))
    expect(all.length).toBeGreaterThan(0)
    const tabbable = all.filter((el) => el.tabIndex >= 0).map((el) => el.id || el.getAttribute('type'))
    expect(tabbable).toEqual(['dc-name', 'dc-email', 'dc-company', 'dc-role', 'dc-size', 'dc-volume', 'dc-consent', 'submit'])

    const honeypot = $<HTMLInputElement>('input[name="website"]')
    expect(honeypot.tabIndex, 'the honeypot must stay out of the tab order').toBe(-1)
    // Focus follows the DOM order the assertion above pins.
    for (const id of ['dc-name', 'dc-email', 'dc-company', 'dc-role', 'dc-size', 'dc-volume', 'dc-consent']) {
      $(`#${id}`).focus()
      expect(document.activeElement?.id).toBe(id)
    }
  })
})

describe('X7: the card carries its own <style>, with the rules the facade never had', () => {
  it('exactly one <style>, holding the focus ring and the <480px select stack', async () => {
    await mount()
    const styles = container.querySelectorAll('style')
    expect(styles.length).toBe(1)
    const css = styles[0].textContent ?? ''
    expect(css).toBe(DEMO_FORM_CSS)
    expect(css).toContain('.dm-input:focus')
    expect(css).toContain('@media (max-width: 480px)')
  })
})

describe('X12: a submit that bypasses the disabled button still reaches the wire once', () => {
  // D9 proves the double click is stopped, but the only mechanism it exercises is
  // `disabled` on the button. Enter in a text field, or any requestSubmit(), reaches
  // the form without touching the button — handleSubmit's own in-flight guard is what
  // has to hold there, and nothing measured it.
  it('a second form submit while in flight does not send twice', async () => {
    openGate()
    const fetchMock = vi.fn(() => new Promise(() => {}))
    vi.stubGlobal('fetch', fetchMock)
    await mount()
    await fill('Ada Okafor', 'ada@okafor.ng', 'Okafor & Partners')
    await act(async () => $<HTMLInputElement>('#dc-consent').click())

    const form = $<HTMLFormElement>('form')
    const fire = () => form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await act(async () => {
      fire()
    })
    expect(fetchMock, 'control: the first submit must reach the wire').toHaveBeenCalledTimes(1)
    await act(async () => {
      fire()
    })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('X10 ([panel-padding-by-variant]): the card\'s PANELS pad to zero too, not just its form', () => {
  it('the success panel renders no padding in the card and the popup\'s 32px in the popup', async () => {
    openGate()
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, status: 200 }))
    await mount()
    await fill('Ada Okafor', 'ada@okafor.ng', 'Okafor & Partners')
    await act(async () => $<HTMLInputElement>('#dc-consent').click())
    await submit()
    await flushAsync()
    expect($('#dc-success').style.padding, "the card's panel already pads 24").toBe('0px')

    const popupHost = document.createElement('div')
    document.body.appendChild(popupHost)
    const popupRoot = createRoot(popupHost)
    await act(async () => {
      popupRoot.render(createElement(DemoModal, { onClose: noop }))
    })
    const pop = <T extends HTMLElement>(sel: string): T => popupHost.querySelector<T>(sel)!
    await act(async () => {
      typeInto(pop<HTMLInputElement>('#dm-name'), 'Ada Okafor')
      typeInto(pop<HTMLInputElement>('#dm-email'), 'ada@okafor.ng')
      typeInto(pop<HTMLInputElement>('#dm-company'), 'Okafor & Partners')
    })
    await act(async () => pop<HTMLInputElement>('#dm-consent').click())
    await act(async () => pop<HTMLButtonElement>('button[type="submit"]').click())
    await flushAsync()
    expect(pop('#dm-success').style.padding).toBe('32px 22px 24px')

    await act(async () => popupRoot.unmount())
    popupHost.remove()
  })
})

describe('X11 ([no-field-level-deviation]): the card takes the popup\'s field styling verbatim', () => {
  it('every control\'s inline style is byte-identical across the two surfaces', async () => {
    await mount()
    const popupHost = document.createElement('div')
    document.body.appendChild(popupHost)
    const popupRoot = createRoot(popupHost)
    await act(async () => {
      popupRoot.render(createElement(DemoModal, { onClose: noop }))
    })

    for (const field of ['name', 'email', 'company', 'role', 'size', 'volume', 'consent']) {
      const card = $(`#dc-${field}`).getAttribute('style')
      const popup = popupHost.querySelector(`#dm-${field}`)?.getAttribute('style')
      expect(card, `#dc-${field} must carry an inline style`).toBeTruthy()
      expect(card, `#dc-${field} deviates from #dm-${field}`).toBe(popup)
    }
    const cardSubmit = $<HTMLButtonElement>('button[type="submit"]')
    const popupSubmit = popupHost.querySelector<HTMLButtonElement>('button[type="submit"]')!
    expect(cardSubmit.getAttribute('style')).toBe(popupSubmit.getAttribute('style'))
    expect(cardSubmit.className).toBe(popupSubmit.className)

    await act(async () => popupRoot.unmount())
    popupHost.remove()
  })
})

describe('X8: both surfaces can be submitted in one session', () => {
  it('a card lead then a popup lead send two distinct payloads, and the card keeps its thank-you', async () => {
    openGate()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)

    const popupHost = document.createElement('div')
    document.body.appendChild(popupHost)
    const popupRoot = createRoot(popupHost)
    await mount()
    await act(async () => {
      popupRoot.render(createElement(DemoModal, { onClose: noop }))
    })

    await fill('Ada Okafor', 'ada@okafor.ng', 'Okafor & Partners')
    await act(async () => $<HTMLInputElement>('#dc-consent').click())
    await submit()
    await flushAsync()
    expect(container.textContent).toContain("You're booked")

    const pop = <T extends HTMLElement>(sel: string): T => popupHost.querySelector<T>(sel)!
    await act(async () => {
      typeInto(pop<HTMLInputElement>('#dm-name'), 'Bola Adeyemi')
      typeInto(pop<HTMLInputElement>('#dm-email'), 'bola@adeyemi.ng')
      typeInto(pop<HTMLInputElement>('#dm-company'), 'Adeyemi Ltd')
    })
    await act(async () => pop<HTMLInputElement>('#dm-consent').click())
    await act(async () => pop<HTMLButtonElement>('button[type="submit"]').click())
    await flushAsync()

    expect(fetchMock).toHaveBeenCalledTimes(2)
    const bodies = fetchMock.mock.calls.map((c) => JSON.parse((c[1] as RequestInit).body as string))
    expect(bodies[0]).not.toEqual(bodies[1])
    expect(JSON.stringify(bodies[0])).toContain('ada@okafor.ng')
    expect(JSON.stringify(bodies[1])).toContain('bola@adeyemi.ng')
    // The card's own panel is unaffected by the popup's submit.
    expect(container.textContent).toContain("You're booked")
    expect(popupHost.textContent).toContain("You're booked")

    await act(async () => popupRoot.unmount())
    popupHost.remove()
    expect(consoleError).not.toHaveBeenCalled()
  })
})
