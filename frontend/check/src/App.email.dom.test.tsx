// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/check/" }
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from './App'
import { CHECK_FIELD_KEYS, fieldLabel, formatBytes } from './fields'
import { MAX_INVOICES, NEXT_STEP, SENDING_NOTE, SENDING_TEXT, SENDING_TO, SENT_NOTE, SENT_TEXT, STAGE_MS, STAGES, UPSELL_TEXT } from './copy'
import {
  ARMOR_SENTENCE,
  CEILING_SENTENCE,
  CONSENT_SENTENCE,
  EMAIL_SENTENCE,
  IP_SENTENCE,
  SLOW_UPLOAD_SENTENCE,
  TOO_MUCH_TEXT_SENTENCE,
  UNAVAILABLE_SENTENCE,
  UNREADABLE_SENTENCE,
} from './messages'
import { SIZE_SENTENCE } from './copy'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const store = new Map<string, string>()
Object.defineProperty(globalThis, 'localStorage', {
  configurable: true,
  value: {
    getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
    setItem: (k: string, v: string) => void store.set(k, String(v)),
    removeItem: (k: string) => void store.delete(k),
    clear: () => store.clear(),
  },
})

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const SHEET = {
  columns: ['Inv', 'Seller', 'Tot', 'Net'],
  sample_rows: [['v0', 'v1', 'v2', 'v3']],
  header_row: 1,
  rows_total: 1,
  mapping: { invoice_number: 'Inv', supplier_name: 'Seller', total: 'Tot' },
  source: 'heuristic',
}

let container: HTMLDivElement
let root: Root

async function mount() {
  vi.stubGlobal('fetch', vi.fn<typeof fetch>(async () => json(200, SHEET)))
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => root.render(<App />))
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://api.example.test')
  store.set('asc_consent', JSON.stringify({ analytics: false, ts: '2026-01-01T00:00:00Z', v: 1 }))
})
afterEach(async () => {
  await act(async () => root.unmount())
  container.remove()
  store.clear()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

const settle = async () => {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))))
}
const q = <T extends Element>(sel: string) => container.querySelector<T>(sel)
const button = (name: string) => [...container.querySelectorAll('button')].find((b) => b.textContent === name)!
const emailInput = () => q<HTMLInputElement>('#check-email')!
const tickBox = () => q<HTMLInputElement>('.check-consent input')!
const click = (el: Element) => act(async () => void el.dispatchEvent(new MouseEvent('click', { bubbles: true })))
const blur = (el: Element) => act(async () => void el.dispatchEvent(new FocusEvent('focusout', { bubbles: true })))

async function pick(f: File) {
  const input = q<HTMLInputElement>('input[type="file"]')!
  Object.defineProperty(input, 'files', { value: [f], configurable: true })
  await act(async () => void input.dispatchEvent(new Event('change', { bubbles: true })))
  await settle()
}
async function type(el: HTMLInputElement, value: string) {
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  await act(async () => {
    set.call(el, value)
    el.dispatchEvent(new Event('input', { bubbles: true }))
  })
}
async function choose(label: string, value: string) {
  const l = [...container.querySelectorAll('label')].find((x) => x.textContent === label)!
  const el = q<HTMLSelectElement>(`#${l.htmlFor}`)!
  const set = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value')!.set!
  await act(async () => {
    set.call(el, value)
    el.dispatchEvent(new Event('change', { bubbles: true }))
  })
}
async function openAll() {
  for (const b of [...container.querySelectorAll<HTMLButtonElement>('.check-acc')]) if (b.getAttribute('aria-expanded') === 'false') await click(b)
  for (const b of [...container.querySelectorAll<HTMLButtonElement>('.check-subhead button')]) if (b.textContent === 'Review') await click(b)
}
const toEmail = async (name: string, size = 4) => {
  await mount()
  await pick(new File(['a'.repeat(size)], name))
  if (name.endsWith('.csv')) await click(button('Continue'))
}

describe('email step', () => {
  it('email_stepLabelFollowsTheFileKind', async () => {
    for (const [name, label] of [['a.csv', '03 · Email'], ['a.pdf', '02 · Email'], ['a.docx', '02 · Email']]) {
      await toEmail(name)
      expect(q('.check-step-no')!.textContent).toBe(label)
      expect(q('h2')!.textContent).toBe('Where should we send the results?')
      await act(async () => root.unmount())
      container.remove()
    }
    await mount()
  })

  it('email_fileRowCountsTheMatchedFields', async () => {
    await toEmail('a.csv')
    const n = CHECK_FIELD_KEYS.length
    expect(q('.check-upload-name')!.textContent).toBe('a.csv')
    expect(q('.check-upload-size')!.textContent).toBe(`${formatBytes(4)} · 3 of ${n} fields matched`)
    await click(button('Back'))
    await openAll()
    await choose(fieldLabel('subtotal'), 'Net')
    await click(button('Continue'))
    expect(q('.check-upload-size')!.textContent).toBe(`${formatBytes(4)} · 4 of ${n} fields matched`)
    await click(button('Back'))
    await openAll()
    await choose(fieldLabel('subtotal'), '')
    await click(button('Continue'))
    expect(q('.check-upload-size')!.textContent).toBe(`${formatBytes(4)} · 3 of ${n} fields matched`)
  })

  it('email_aDocumentNeedsNoColumnMatching', async () => {
    await toEmail('a.pdf', 2048)
    expect(q('.check-upload-size')!.textContent).toBe('2 KB · No column matching needed')
    expect(q('.check-file-ok svg')).toBeTruthy()
  })

  it('email_aMalformedAddressShowsItsMessageOnBlur', async () => {
    await toEmail('a.pdf')
    await type(emailInput(), 'ada@adebayofoods')
    expect(q('#check-email-err')).toBeNull()
    await blur(emailInput())
    const err = q('#check-email-err')!
    expect(err.getAttribute('role')).toBe('alert')
    expect(err.textContent).toBe(EMAIL_SENTENCE)
    expect(emailInput().getAttribute('aria-invalid')).toBe('true')
    expect(emailInput().classList.contains('is-err')).toBe(true)
    expect(emailInput().getAttribute('aria-describedby')).toBe('check-email-err')
  })

  it('email_anEmptyFieldBlursSilently', async () => {
    await toEmail('a.pdf')
    await blur(emailInput())
    expect(q('#check-email-err')).toBeNull()
    expect(emailInput().getAttribute('aria-invalid')).not.toBe('true')
  })

  it('email_aFixedAddressClearsTheMessageAsTyped', async () => {
    await toEmail('a.pdf')
    await type(emailInput(), 'ada@adebayofoods')
    await blur(emailInput())
    expect(q('#check-email-err')).toBeTruthy()
    await type(emailInput(), 'ada@adebayofoods.ng')
    expect(q('#check-email-err')).toBeNull()
    expect(q('.is-err')).toBeNull()
    await type(emailInput(), 'ada@adebayofoods')
    expect(q('#check-email-err')).toBeTruthy()
  })

  it('email_consentMessageShowsOnceTheBoxIsLeftEmpty', async () => {
    await toEmail('a.pdf')
    expect(q('#check-consent-err')).toBeNull()
    await click(tickBox())
    expect(q('#check-consent-err')).toBeNull()
    await click(tickBox())
    const err = q('#check-consent-err')!
    expect(err.getAttribute('role')).toBe('alert')
    expect(err.textContent).toBe(CONSENT_SENTENCE)
    expect(tickBox().getAttribute('aria-describedby')).toBe('check-consent-err')
    await click(tickBox())
    expect(q('#check-consent-err')).toBeNull()
  })

  it('email_sendWaitsForAValidAddressAndTheTick', async () => {
    await toEmail('a.pdf')
    const send = () => button('Send') as HTMLButtonElement
    await type(emailInput(), 'ada@adebayofoods')
    await click(tickBox())
    expect(send().disabled).toBe(true)
    await type(emailInput(), 'ada@adebayofoods.ng')
    await click(tickBox())
    expect(send().disabled).toBe(true)
    await click(tickBox())
    expect(send().disabled).toBe(false)
  })

  it('email_sendCarriesTheArrowAndKeepsItsName', async () => {
    await toEmail('a.pdf')
    const send = q<HTMLButtonElement>('button[type="submit"]')!
    expect(send.textContent).toBe('Send')
    expect(send.querySelectorAll('svg[aria-hidden="true"]').length).toBe(1)
    expect(button('Back').getAttribute('type')).toBe('button')
  })
})

const SENT = '.check-sent [role="status"]'

describe('sending view', () => {
  let answer: (() => Response) | null
  let release: () => void
  let run: ReturnType<typeof vi.fn<() => Promise<Response>>>
  const stubRun = () => {
    run = vi.fn(() => new Promise<Response>((r) => (release = () => r(answer!()))))
    vi.stubGlobal('fetch', vi.fn<typeof fetch>(async (input) => (String(input).endsWith('/readiness-check/run') ? run() : json(200, SHEET))))
  }
  const fill = async (email = 'a@x.io') => {
    await type(emailInput(), email)
    await click(tickBox())
  }
  const submit = () => click(q('button[type="submit"]')!)
  const classes = () => [...container.querySelectorAll('.check-stages li')].map((li) => li.className)
  const advance = (ms: number) => act(async () => void vi.advanceTimersByTime(ms))
  const toPdfEmail = async () => {
    await mount()
    stubRun()
    await pick(new File(['aaaa'], 'a.pdf'))
  }

  beforeEach(() => {
    answer = () => json(200, { status: 'confirm' })
  })
  afterEach(() => vi.useRealTimers())

  it('sending_showsTheHeadingStagesAndAddress', async () => {
    await toPdfEmail()
    await fill('  a@x.io ')
    await submit()
    expect(q('.check-sending h2')!.textContent).toBe(SENDING_TEXT)
    expect(q('.check-step-no')!.textContent).toBe('02 · Email')
    expect(q('.check-bar')).toBeTruthy()
    expect([...container.querySelectorAll('.check-stages li')].map((li) => li.textContent)).toEqual([...STAGES])
    expect(q('.check-sending-addr')!.textContent).toBe(SENDING_TO('a@x.io'))
    expect(q('.check-sending-addr')!.textContent).toBe('We email you at a@x.io')
    expect(q('.check-sending-note')!.textContent).toBe(SENDING_NOTE)
    expect(q('form')).toBeNull()
    expect(button('Back')).toBeUndefined()
    expect(button('Send')).toBeUndefined()
    expect(q('.check-progress [aria-current="step"]')!.textContent).toContain('Email')
    await act(async () => release())
  })

  it('sending_stagesAdvanceEveryStageMsAndTheLastHolds', async () => {
    await toPdfEmail()
    await fill()
    vi.useFakeTimers()
    await submit()
    expect(classes()).toEqual(['is-now', 'is-todo', 'is-todo', 'is-todo'])
    await advance(STAGE_MS - 1)
    expect(classes()).toEqual(['is-now', 'is-todo', 'is-todo', 'is-todo'])
    await advance(1)
    expect(classes()).toEqual(['is-done', 'is-now', 'is-todo', 'is-todo'])
    expect(q('.check-stages li .check-sdot svg')).toBeTruthy()
    await advance(2 * STAGE_MS)
    expect(classes()).toEqual(['is-done', 'is-done', 'is-done', 'is-now'])
    await advance(60000)
    expect(classes()).toEqual(['is-done', 'is-done', 'is-done', 'is-now'])
  })

  it('sending_theAnswerEndsTheStagesAndAResendRestarts', async () => {
    await toPdfEmail()
    await fill()
    vi.useFakeTimers()
    answer = () => json(503, { error: 'unavailable' })
    await submit()
    await advance(2 * STAGE_MS)
    await act(async () => release())
    await advance(0)
    expect(q('#check-email')).toBeTruthy()
    expect(q('.check-sending')).toBeNull()
    expect(vi.getTimerCount()).toBe(0)
    answer = () => json(200, { status: 'confirm' })
    await submit()
    expect(classes()).toEqual(['is-now', 'is-todo', 'is-todo', 'is-todo'])
    await act(async () => release())
    await advance(0)
    expect(q(SENT)).toBeTruthy()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('sending_aSecondSubmitBeforeTheViewMountsSendsOnce', async () => {
    await toPdfEmail()
    await fill()
    const form = q<HTMLFormElement>('form')!
    await act(async () => {
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    })
    expect(run).toHaveBeenCalledTimes(1)
    await act(async () => release())
    await settle()
    expect(q(SENT)).toBeTruthy()
  })

  it('sending_theHeadingTakesFocus', async () => {
    await toPdfEmail()
    await fill()
    await submit()
    const h2 = q<HTMLElement>('.check-sending h2')!
    expect(document.activeElement).toBe(h2)
    expect(h2.tabIndex).toBe(-1)
    await act(async () => release())
  })
})

describe('email alert', () => {
  let answer: () => Response | Promise<Response>
  const html429 = () => new Response('<html>429</html>', { status: 429, headers: { 'Content-Type': 'text/html' } })
  const reject = () => Promise.reject(new TypeError('Failed to fetch'))
  const ALERT = '.check-email [role="alert"]'
  const alerts = () => container.querySelectorAll('.check-email .check-alert')
  const fill = async () => {
    await type(emailInput(), 'a@x.io')
    await click(tickBox())
  }
  const submit = () => click(q('button[type="submit"]')!)
  const toRefusal = async (a: () => Response | Promise<Response>, name = 'a.pdf') => {
    answer = a
    await mount()
    vi.stubGlobal('fetch', vi.fn<typeof fetch>(async (input) => (String(input).endsWith('/readiness-check/run') ? answer() : json(200, SHEET))))
    await pick(new File(['aaaa'], name))
    await fill()
    await submit()
    await settle()
  }

  it.each<[string, () => Response | Promise<Response>, string]>([
    ['429 gateway', () => json(429, { error: IP_SENTENCE }), IP_SENTENCE],
    ['429 armor', html429, ARMOR_SENTENCE],
    ['422', () => json(422, { error: UNREADABLE_SENTENCE }), UNREADABLE_SENTENCE],
    ['408', () => json(408, { error: SLOW_UPLOAD_SENTENCE }), SLOW_UPLOAD_SENTENCE],
    ['503 ceiling', () => json(503, { error: CEILING_SENTENCE }), CEILING_SENTENCE],
    ['503 other', () => json(503, { error: 'busy' }), UNAVAILABLE_SENTENCE],
    ['413', () => json(413, { error: 'file is over 15 MiB' }), SIZE_SENTENCE],
    ['400 too much text', () => json(400, { error: TOO_MUCH_TEXT_SENTENCE }), TOO_MUCH_TEXT_SENTENCE],
    ['400 email', () => json(400, { error: 'email is invalid' }), EMAIL_SENTENCE],
    ['409', () => json(409, { error: 'x' }), UNAVAILABLE_SENTENCE],
    ['network', reject, UNAVAILABLE_SENTENCE],
  ])('email_eachServerRefusalShowsInTheOneAlert %s', async (_n, a, sentence) => {
    await toRefusal(a)
    expect(container.querySelectorAll(ALERT).length).toBe(1)
    const alert = alerts()[0]!
    expect(alert.textContent!.startsWith(sentence)).toBe(true)
    expect(alert.nextElementSibling!.className).toBe('check-actions')
    expect(alert.previousElementSibling!.className).toBe('check-consent-field')
    if (_n === '409') expect(alert.querySelector('a')).toBeNull()
  })

  it.each<[string, () => Response]>([
    ['422', () => json(422, { error: UNREADABLE_SENTENCE })],
    ['408', () => json(408, { error: SLOW_UPLOAD_SENTENCE })],
  ])('email_aFileRefusalOffersAnotherFile %s', async (_n, a) => {
    await toRefusal(a)
    const choose = [...alerts()[0]!.querySelectorAll('button')].find((b) => b.textContent === 'Choose another file')!
    expect(choose).toBeTruthy()
    await click(choose)
    expect(q('input[type="file"]')).not.toBeNull()
    expect(alerts().length).toBe(0)
    expect(container.textContent).not.toContain('a.pdf')
    expect(q('.check-progress [aria-current="step"]')!.textContent).toContain('Upload')
    await pick(new File(['bbbb'], 'b.pdf'))
    expect(emailInput().value).toBe('a@x.io')
    expect(tickBox().checked).toBe(false)
  })

  it.each<[string, () => Response | Promise<Response>]>([
    ['429', () => json(429, { error: IP_SENTENCE })],
    ['503', () => json(503, { error: 'busy' })],
    ['413', () => json(413, { error: 'x' })],
    ['400 too much text', () => json(400, { error: TOO_MUCH_TEXT_SENTENCE })],
    ['network', reject],
  ])('email_otherRefusalsOfferNoOtherFile %s', async (_n, a) => {
    await toRefusal(a)
    expect(alerts().length).toBe(1)
    expect(alerts()[0]!.querySelector('button, a')).toBeNull()
  })

  it('email_aRefusalKeepsTheFormFilled', async () => {
    await toRefusal(() => json(503, { error: 'busy' }))
    expect(emailInput().value).toBe('a@x.io')
    expect(tickBox().checked).toBe(true)
    expect(button('Send').disabled).toBe(false)
  })

  it('email_editingTheFormClearsTheAlert', async () => {
    const a = () => json(503, { error: 'busy' })
    await toRefusal(a)
    await blur(emailInput())
    expect(alerts().length).toBe(1)
    await type(emailInput(), 'a@x.ioo')
    expect(alerts().length).toBe(0)
    await submit()
    await settle()
    expect(alerts().length).toBe(1)
    await click(tickBox())
    expect(alerts().length).toBe(0)
    await click(tickBox())
    await submit()
    await settle()
    expect(alerts().length).toBe(1)
    await click(button('Back'))
    await pick(new File(['aaaa'], 'a.pdf'))
    expect(alerts().length).toBe(0)
  })

  it('email_theAlertTakesFocus', async () => {
    await toRefusal(() => json(422, { error: UNREADABLE_SENTENCE }))
    expect(document.activeElement).toBe(alerts()[0])
  })
})

describe('sent view', () => {
  const toSent = async (name = 'a.pdf') => {
    await mount()
    vi.stubGlobal('fetch', vi.fn<typeof fetch>(async (input) => (String(input).endsWith('/readiness-check/run') ? json(200, { status: 'confirm' }) : json(200, SHEET))))
    await pick(new File(['aaaa'], name))
    await type(emailInput(), '  a@x.io ')
    await click(tickBox())
    await click(q('button[type="submit"]')!)
    await settle()
  }
  const links = () => [...container.querySelectorAll<HTMLAnchorElement>('.check-upsell a')]

  it('sent_theConfirmationCarriesTheMarkLabelAndBoldSender', async () => {
    await toSent()
    expect(q(`${SENT} .check-sent-mark[aria-hidden="true"] svg`)).toBeTruthy()
    expect(q(`${SENT} .check-sent-label`)!.textContent).toBe('Sent')
    expect(q(`${SENT} h2`)!.textContent).toBe(SENT_TEXT('a@x.io'))
    expect(q(`${SENT} .check-sent-note`)!.textContent).toBe(SENT_NOTE)
    expect(q(`${SENT} .check-sent-note strong`)!.textContent).toBe('no-reply@ascomply.com')
  })

  it('sent_theOfferSitsOutsideTheStatus', async () => {
    await toSent()
    const up = q('.check-upsell')!
    expect(up.closest('[role="status"]')).toBeNull()
    expect(up.textContent).toContain(NEXT_STEP)
    expect(up.textContent).toContain('This check covered up to 2 invoices.')
    expect(MAX_INVOICES).toBe(2)
    expect(up.textContent).toContain('Check every invoice before it goes to MBS.')
    expect(up.textContent).toContain(UPSELL_TEXT)
  })

  it('sent_signUpAndDemoLinkTheLanding', async () => {
    await toSent()
    expect(links().map((a) => [a.textContent, a.getAttribute('href')])).toEqual([
      ['Sign up', 'https://www.ascomply.com/?register'],
      ['Book a demo', 'https://www.ascomply.com/?demo'],
    ])
    await act(async () => root.unmount())
    container.remove()
    vi.stubEnv('VITE_LANDING_URL', 'https://landing-pr-1.example/')
    await toSent()
    expect(links().map((a) => a.getAttribute('href'))).toEqual(['https://landing-pr-1.example/?register', 'https://landing-pr-1.example/?demo'])
  })

  it('sent_checkAnotherFileStartsOver', async () => {
    await toSent()
    await click(button('Check another file'))
    expect(q('input[type="file"]')).toBeTruthy()
    expect(container.textContent).not.toContain('a.pdf')
    expect(q('.check-progress [aria-current="step"]')!.textContent).toContain('Upload')
    await pick(new File(['bbbb'], 'b.pdf'))
    expect(emailInput().value).toBe('a@x.io')
    expect(tickBox().checked).toBe(false)
    expect(q<HTMLButtonElement>('button[type="submit"]')!.disabled).toBe(true)
  })

  it('sent_theHeadingTakesFocus', async () => {
    await toSent()
    expect(document.activeElement).toBe(q('.check-sent h2'))
  })
})
