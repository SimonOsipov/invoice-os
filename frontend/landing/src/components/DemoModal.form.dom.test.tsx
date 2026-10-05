// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/" }
// Modal form behaviour on `#dm-*`. fetch is the seam; no vi.mock here.
/// <reference types="node" />
import { act, createElement } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { DemoModal } from './DemoModal'
import { CONSENT_TEXT, DEFAULT_FORM, DEMO_COMPANY_MAX, DEMO_EMAIL_MAX, DEMO_NAME_MAX, ROLE_OPTIONS, TAXPAYER_SIZE_OPTIONS, VOLUME_OPTIONS } from './demoForm'
import { MARKETING_CONSENT_TEXT } from './MarketingConsent'
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
  vi.stubEnv('VITE_GATEWAY_URL', '')
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
    root.render(createElement(DemoModal, { onClose: noop }))
  })
}

function $<T extends HTMLElement>(sel: string): T {
  const el = container.querySelector<T>(sel)
  expect(el, `expected ${sel}`).not.toBeNull()
  return el!
}

// React's value tracker swallows a plain `input.value = …` write; the native setter bypasses it.
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

const GATEWAY_URL = 'https://gw.x/contacts/demo-request'

// Routes by URL: the HubSpot Forms host and the gateway answer separately.
function routeFetch(hs: () => Promise<Response> | Response, gw: () => Promise<Response> | Response) {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.x')
  const fetchMock = vi.fn<typeof fetch>(async (url) => (String(url) === GATEWAY_URL ? gw() : hs()))
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}
const hsOk = () => new Response('{}', { status: 200 })
const gwAccepted = () => new Response(JSON.stringify({ status: 'accepted' }), { status: 202 })
const urlOf = (fetchMock: { mock: { calls: Parameters<typeof fetch>[] } }, i: number) => String(fetchMock.mock.calls[i]?.[0])
const bodyOf = (fetchMock: { mock: { calls: Parameters<typeof fetch>[] } }, i: number) => String(fetchMock.mock.calls[i]?.[1]?.body)

async function flushAsync(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

async function fill(name: string, email: string, company: string): Promise<void> {
  await act(async () => {
    typeInto($('#dm-name'), name)
    typeInto($('#dm-email'), email)
    typeInto($('#dm-company'), company)
  })
}

async function tick(sel: string): Promise<void> {
  await act(async () => $<HTMLInputElement>(sel).click())
}

async function submit(): Promise<void> {
  await act(async () => $<HTMLButtonElement>('button[type="submit"]').click())
}

// The modal focuses #dm-name on mount, so a focus-on-name assertion is vacuous unless
// focus first leaves it.
function focusAwayFromName(): void {
  $<HTMLElement>('#dm-role').focus()
  expect(document.activeElement?.id, 'control: focus has left #dm-name').toBe('dm-role')
}

describe('MF-D1 (CHARACTERIZATION): typing and selecting change the values', () => {
  it('the three inputs and the size select read back what was typed/chosen', async () => {
    await mount()
    const band = TAXPAYER_SIZE_OPTIONS[0] // 'Large ₦5bn+', not DEFAULT_FORM.size
    expect(band).not.toBe(DEFAULT_FORM.size)
    await fill('Ada Okafor', 'ada@okafor.ng', 'Okafor & Partners')
    await act(async () => choose($<HTMLSelectElement>('#dm-size'), band))

    expect($<HTMLInputElement>('#dm-name').value).toBe('Ada Okafor')
    expect($<HTMLInputElement>('#dm-email').value).toBe('ada@okafor.ng')
    expect($<HTMLInputElement>('#dm-company').value).toBe('Okafor & Partners')
    expect($<HTMLSelectElement>('#dm-size').value).toBe(band)
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('MF-D2 (CHARACTERIZATION): each missing required field names itself and takes focus', () => {
  it('walks name -> email -> company on successive submits, sending nothing', async () => {
    openGate()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    await mount()
    focusAwayFromName()

    await submit()
    expect($('#dm-name-error').getAttribute('role')).toBe('alert')
    expect($('#dm-name').getAttribute('aria-invalid')).toBe('true')
    expect(document.activeElement?.id).toBe('dm-name')

    await act(async () => typeInto($('#dm-name'), 'Ada Okafor'))
    await submit()
    expect($('#dm-email-error').getAttribute('role')).toBe('alert')
    expect(document.activeElement?.id).toBe('dm-email')

    await act(async () => typeInto($('#dm-email'), 'ada@okafor.ng'))
    await submit()
    expect($('#dm-company-error').getAttribute('role')).toBe('alert')
    expect(document.activeElement?.id).toBe('dm-company')

    expect(fetchMock).not.toHaveBeenCalled()
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('MF-D3 (CHARACTERIZATION): an unticked consent blocks the submit', () => {
  it('leaves the form up with a consent alert, no success, nothing sent', async () => {
    openGate()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    await mount()
    await fill('Ada Okafor', 'ada@okafor.ng', 'Okafor & Partners')
    await submit()

    expect($('#dm-consent-error').getAttribute('role')).toBe('alert')
    expect(document.activeElement?.id).toBe('dm-consent')
    expect(container.querySelector('#dm-name'), 'the form is still up').not.toBeNull()
    expect(container.textContent).not.toContain("You're booked")
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('MF-D6 (CHARACTERIZATION): a tripped honeypot sends nothing, still thanks', () => {
  it('bypasses the wire and reaches the success panel after the stub delay', async () => {
    openGate()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    await mount()
    await fill('Ada Okafor', 'ada@okafor.ng', 'Okafor & Partners')
    await tick('#dm-consent')
    // A direct DOM write is what a naive bot does; the field is uncontrolled.
    $<HTMLInputElement>('input[name="website"]').value = 'https://spam.example'

    vi.useFakeTimers()
    await submit()
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1400)
    })
    vi.useRealTimers()

    expect(fetchMock).not.toHaveBeenCalled()
    expect(container.textContent).toContain("You're booked")
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('a tripped honeypot sends nothing anywhere, even with the gateway set and marketing ticked', async () => {
    openGate()
    const fetchMock = routeFetch(hsOk, gwAccepted)
    await mount()
    await fill('Ada Okafor', 'ada@okafor.ng', 'Okafor & Partners')
    await tick('#dm-consent')
    await tick('#dm-marketing')
    $<HTMLInputElement>('input[name="website"]').value = 'https://spam.example'

    vi.useFakeTimers()
    await submit()
    expect(container.textContent, 'control: the submit was processed').toContain('Booking…')
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1400)
    })
    vi.useRealTimers()

    expect(fetchMock).not.toHaveBeenCalled()
    expect(container.textContent).toContain("You're booked")
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('AUTH-17-08: the marketing box and the gateway post', () => {
  it('the marketing box sits under the consent box, unticked, with its treatment', async () => {
    await mount()
    const consent = $<HTMLInputElement>('#dm-consent')
    const box = $<HTMLInputElement>('#dm-marketing')

    expect(box.type).toBe('checkbox')
    expect(box.checked).toBe(false)
    expect(consent.compareDocumentPosition(box) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    const controls = Array.from($('form').querySelectorAll('input, select'))
    expect(controls.indexOf(box), 'directly under the consent box').toBe(controls.indexOf(consent) + 1)

    expect(consent.getAttribute('style'), 'control: the consent input carries a style').toBeTruthy()
    expect(box.getAttribute('style')).toBe(consent.getAttribute('style'))
    expect(box.labels?.[0]?.getAttribute('style')).toBe(consent.labels?.[0]?.getAttribute('style'))
    expect(box.labels?.[0]?.textContent).toBe(MARKETING_CONSENT_TEXT)
    expect(consent.hasAttribute('aria-required'), 'control: the consent box is required').toBe(true)
    expect(box.hasAttribute('aria-required'), 'the marketing box is not').toBe(false)
  })

  it('the marketing box is disabled while submitting, as its siblings are', async () => {
    openGate()
    vi.stubGlobal('fetch', vi.fn(() => new Promise(() => {})))
    await mount()
    await fill('Ada Okafor', 'ada@okafor.ng', 'Okafor & Partners')
    await tick('#dm-consent')
    await submit()

    expect($<HTMLInputElement>('#dm-consent').disabled, 'control: submitting disables the consent box').toBe(true)
    expect($<HTMLInputElement>('#dm-marketing').disabled).toBe(true)
  })

  it('an unticked marketing box never blocks a booking', async () => {
    openGate()
    routeFetch(hsOk, gwAccepted)
    await mount()
    expect($<HTMLInputElement>('#dm-marketing').checked, 'control: the box exists, unticked').toBe(false)
    await fill('Ada Okafor', 'ada@okafor.ng', 'Okafor & Partners')
    await tick('#dm-consent')
    await submit()
    await flushAsync()

    expect(container.textContent).toContain("You're booked")
    expect(container.querySelector('#dm-marketing-error')).toBeNull()
  })

  it('on the production host HubSpot goes first, then the gateway', async () => {
    openGate()
    let releaseHs: () => void = () => undefined
    const hsGate = new Promise<void>((resolve) => (releaseHs = resolve))
    const fetchMock = routeFetch(() => hsGate.then(hsOk), gwAccepted)
    await mount()
    await fill('Ada Okafor', 'ada@okafor.ng', 'Okafor & Partners')
    await tick('#dm-consent')
    await submit()
    await flushAsync()

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(urlOf(fetchMock, 0)).toBe(submissionUrl(hubspotTarget()!))

    await act(async () => releaseHs())
    await flushAsync()

    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(urlOf(fetchMock, 1)).toBe(GATEWAY_URL)
    expect(JSON.parse(bodyOf(fetchMock, 1))).toEqual({ email: 'ada@okafor.ng', name: 'Ada Okafor', company: 'Okafor & Partners' })
    expect(container.textContent).toContain("You're booked")
    expect(consoleError).not.toHaveBeenCalled()
  })

  it('a ticked marketing box sends its sentence to the gateway and leaves the HubSpot body byte-identical', async () => {
    openGate()
    const fetchMock = routeFetch(hsOk, gwAccepted)
    await mount()
    await fill('Ada Okafor', 'ada@okafor.ng', 'Okafor & Partners')
    await tick('#dm-consent')
    await tick('#dm-marketing')
    await submit()
    await flushAsync()

    expect(fetchMock).toHaveBeenCalledTimes(2)
    const lead: DemoLead = {
      name: 'Ada Okafor',
      email: 'ada@okafor.ng',
      company: 'Okafor & Partners',
      role: DEFAULT_FORM.role,
      size: DEFAULT_FORM.size,
      volume: DEFAULT_FORM.volume,
      consent: true,
      marketing: false,
    }
    expect(bodyOf(fetchMock, 0), 'the Forms body is the unticked payload, byte for byte').toBe(JSON.stringify(buildSubmission(lead, CONSENT_TEXT)))
    expect(bodyOf(fetchMock, 0)).not.toMatch(/marketing/i)
    expect(JSON.parse(bodyOf(fetchMock, 1))).toEqual({
      email: 'ada@okafor.ng',
      name: 'Ada Okafor',
      company: 'Okafor & Partners',
      marketing_consent_text: MARKETING_CONSENT_TEXT,
    })
  })
})

describe('MF-X1 (CHARACTERIZATION): a blank submit names every missing field at once', () => {
  it('renders all four alerts, focuses only the first, and reaches no wire', async () => {
    openGate()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    await mount()
    focusAwayFromName()
    await submit()

    for (const key of ['name', 'email', 'company', 'consent']) {
      const err = $(`#dm-${key}-error`)
      expect(err.getAttribute('role'), `#dm-${key}-error must be an alert`).toBe('alert')
      expect(err.textContent?.trim().length, `#dm-${key}-error must carry copy`).toBeGreaterThan(0)
      expect($(`#dm-${key}`).getAttribute('aria-invalid')).toBe('true')
    }
    expect(document.activeElement?.id).toBe('dm-name')
    expect(fetchMock).not.toHaveBeenCalled()
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('MF-X2 (CHARACTERIZATION): whitespace-only and malformed answers are rejected', () => {
  it('spaces in all three required fields still block the submit and send nothing', async () => {
    openGate()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    await mount()
    await fill('   ', '  \t ', ' \n ')
    await tick('#dm-consent')
    focusAwayFromName()
    await submit()

    expect($('#dm-name-error')).not.toBeNull()
    expect($('#dm-email-error')).not.toBeNull()
    expect($('#dm-company-error')).not.toBeNull()
    expect(container.querySelector('#dm-consent-error'), 'a ticked consent must not error').toBeNull()
    expect(document.activeElement?.id).toBe('dm-name')
    expect(fetchMock).not.toHaveBeenCalled()
    expect(container.textContent).not.toContain("You're booked")
  })

  it('a malformed email is rejected even when the other fields are filled', async () => {
    openGate()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    await mount()
    await fill('Ada Okafor', 'ada-at-okafor', 'Okafor & Partners')
    await tick('#dm-consent')
    await submit()

    expect($('#dm-email-error').textContent).toContain('valid work email')
    expect(container.querySelector('#dm-name-error')).toBeNull()
    expect(container.querySelector('#dm-company-error')).toBeNull()
    expect(document.activeElement?.id).toBe('dm-email')
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('MF-X3 (CHARACTERIZATION): the option lists are the imported constants', () => {
  it('dm-role/size/volume each render Select… plus their constant, in order', async () => {
    await mount()
    const cases: [string, readonly string[]][] = [
      ['dm-role', ROLE_OPTIONS],
      ['dm-size', TAXPAYER_SIZE_OPTIONS],
      ['dm-volume', VOLUME_OPTIONS],
    ]
    for (const [id, options] of cases) {
      expect(options.length, `${id}'s constant must not be empty`).toBeGreaterThan(0)
      const rendered = Array.from($<HTMLSelectElement>(`#${id}`).options).map((o) => o.textContent)
      expect(rendered, `${id} options`).toEqual(['Select…', ...options])
    }
    expect($<HTMLSelectElement>('#dm-size').value).toBe(DEFAULT_FORM.size)
  })
})

describe('MF-X4 (CHARACTERIZATION): long answers survive intact', () => {
  it('a company at the 200-character cap reaches the wire untruncated and the thank-you names the visitor', async () => {
    openGate()
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 })
    vi.stubGlobal('fetch', fetchMock)
    const company = 'Ω'.repeat(DEMO_COMPANY_MAX)
    const name = 'Adaobi'.repeat(30) + ' Okafor'
    await mount()
    await fill(name, 'ada@okafor.ng', company)
    await tick('#dm-consent')
    await submit()
    await flushAsync()

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const body = JSON.parse((fetchMock.mock.calls[0]?.[1] as RequestInit).body as string)
    const lead: DemoLead = {
      name,
      email: 'ada@okafor.ng',
      company,
      role: DEFAULT_FORM.role,
      size: DEFAULT_FORM.size,
      volume: DEFAULT_FORM.volume,
      consent: true,
      marketing: false,
    }
    expect(body).toEqual(buildSubmission(lead, CONSENT_TEXT))
    expect(JSON.stringify(body)).toContain(company)
    expect(container.textContent).toContain("You're booked")
    expect(container.textContent).toContain('Adaobi'.repeat(30))
    expect(consoleError).not.toHaveBeenCalled()
  })
})

describe('MF-X4b: inputs carry the gateway field caps', () => {
  it('name and company cap at 200, email at 254', async () => {
    await mount()
    expect($<HTMLInputElement>('#dm-name').maxLength).toBe(200)
    expect($<HTMLInputElement>('#dm-company').maxLength).toBe(200)
    expect($<HTMLInputElement>('#dm-email').maxLength).toBe(254)
    expect([DEMO_NAME_MAX, DEMO_COMPANY_MAX, DEMO_EMAIL_MAX]).toEqual([200, 200, 254])
  })
})

describe('MF-X5 (CHARACTERIZATION): retry restores the select choices', () => {
  it('role, taxpayer size and volume all come back after a failed submit', async () => {
    openGate()
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('net')))
    // Index 3 of each list; the guards below prove none is the form's default.
    const role = ROLE_OPTIONS[3]
    const size = TAXPAYER_SIZE_OPTIONS[3]
    const volume = VOLUME_OPTIONS[3]
    expect(role).not.toBe(DEFAULT_FORM.role)
    expect(size).not.toBe(DEFAULT_FORM.size)
    expect(volume).not.toBe(DEFAULT_FORM.volume)
    await mount()
    await fill('Ada Okafor', 'ada@okafor.ng', 'Okafor & Partners')
    await act(async () => {
      choose($<HTMLSelectElement>('#dm-role'), role)
      choose($<HTMLSelectElement>('#dm-size'), size)
      choose($<HTMLSelectElement>('#dm-volume'), volume)
    })
    await tick('#dm-consent')
    await submit()
    await flushAsync()

    expect(container.textContent, 'control: the error panel replaced the form').toContain('Something went wrong')
    await act(async () => $<HTMLButtonElement>('#dm-error-retry').click())

    expect($<HTMLSelectElement>('#dm-role').value).toBe(role)
    expect($<HTMLSelectElement>('#dm-size').value).toBe(size)
    expect($<HTMLSelectElement>('#dm-volume').value).toBe(volume)
    expect($<HTMLInputElement>('#dm-consent').checked).toBe(true)
  })
})

describe('MF-X6 (CHARACTERIZATION): the form\'s Tab order is the seven answers, the marketing box, then the submit', () => {
  it('the tabbable sequence inside the <form> is fixed; the honeypot is skipped', async () => {
    await mount()
    // jsdom has no layout, so isFocusable's offsetParent clause is always false here; tabIndex
    // and disabled are the clauses this env can observe. The Close button sits outside the form.
    const form = $<HTMLFormElement>('form')
    const all = Array.from(form.querySelectorAll<HTMLElement>('input, select, button'))
    expect(all.length).toBeGreaterThan(0)
    const tabbable = all.filter((el) => el.tabIndex >= 0 && !(el as HTMLButtonElement).disabled).map((el) => el.id || el.getAttribute('type'))
    expect(tabbable).toEqual(['dm-name', 'dm-email', 'dm-company', 'dm-role', 'dm-size', 'dm-volume', 'dm-consent', 'dm-marketing', 'submit'])

    const honeypot = $<HTMLInputElement>('input[name="website"]')
    expect(form.contains(honeypot), 'control: the honeypot is inside the form').toBe(true)
    expect(honeypot.tabIndex, 'the honeypot must stay out of the tab order').toBe(-1)
  })
})
