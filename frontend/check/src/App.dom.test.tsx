// @vitest-environment jsdom
// @vitest-environment-options { "url": "https://www.ascomply.com/check/" }
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from './App'
import { CONSENT_TEXT } from './analytics'
import { CHECK_FIELD_KEYS, fieldLabel } from './fields'
import { READING_TEXT, SENT_NOTE, SENDING_TEXT, TYPE_SENTENCE } from './copy'
import { ARMOR_SENTENCE, COLUMNS_SENTENCE, IP_SENTENCE, MAPPING_SENTENCE, UNAVAILABLE_SENTENCE, UNREADABLE_SENTENCE } from './messages'

;(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true

// Node's own localStorage global shadows jsdom's on some versions; install a known store.
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

const STORY_CONSENT = 'Email me the results of this check. I agree that ASComply Africa keeps my email address as a contact and may email me product news and offers. I can unsubscribe at any time.'
const MAPPING = '/readiness-check/mapping'
const RUN = '/readiness-check/run'
const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const suggestion = (columns: string[], mapping: Record<string, string>, headerRow = 1) => ({
  columns,
  sample_rows: columns.length ? [columns.map((_, i) => `v${i}`)] : [],
  header_row: headerRow,
  rows_total: 1,
  mapping,
  source: 'heuristic',
})
const SENT = () => json(200, { status: 'confirm' })

let container: HTMLDivElement
let root: Root
let fetchMock: ReturnType<typeof vi.fn<typeof fetch>>

// answers maps a route to the response (or a function returning one) for each call.
function stubFetch(answers: Record<string, () => Response | Promise<Response>>) {
  fetchMock = vi.fn<typeof fetch>(async (input) => {
    const url = String(input)
    const route = Object.keys(answers).find((r) => url.endsWith(r))
    if (!route) throw new Error(`unexpected request ${url}`)
    return answers[route]()
  })
  vi.stubGlobal('fetch', fetchMock)
}

async function mount() {
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
const text = () => container.querySelector('main')?.textContent ?? ''
const fileInput = () => container.querySelector<HTMLInputElement>('input[type="file"]')!
const byLabel = <T extends HTMLElement>(label: string) => {
  const l = [...container.querySelectorAll('label')].find((x) => x.textContent === label)
  if (!l) throw new Error(`no label ${label}`)
  return container.querySelector<T>(`#${l.htmlFor}`)!
}
const button = (name: string) => [...container.querySelectorAll('button')].find((b) => b.textContent === name)!
const file = (name: string, size = 4) => new File(['a'.repeat(size)], name)

async function pick(f: File) {
  Object.defineProperty(fileInput(), 'files', { value: [f], configurable: true })
  await act(async () => void fileInput().dispatchEvent(new Event('change', { bubbles: true })))
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
  const el = byLabel<HTMLSelectElement>(label)
  const set = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value')!.set!
  await act(async () => {
    set.call(el, value)
    el.dispatchEvent(new Event('change', { bubbles: true }))
  })
}
const click = (el: Element) => act(async () => void el.dispatchEvent(new MouseEvent('click', { bubbles: true })))
async function fillAndSend(email = 'a@example.com') {
  await type(byLabel<HTMLInputElement>('Email address'), email)
  await click(container.querySelector('input[type="checkbox"]')!)
  await click(button('Send'))
  await settle()
}
const runBody = () => {
  const call = fetchMock.mock.calls.find(([u]) => String(u).endsWith(RUN))!
  return call[1]!.body as FormData
}
const calls = (route: string) => fetchMock.mock.calls.filter(([u]) => String(u).endsWith(route)).length

const SHEET = suggestion(['Inv', 'Seller', 'STIN', 'Net'], { invoice_number: 'Inv', supplier_name: 'Seller' }, 3)
const sheetStub = () => stubFetch({ [MAPPING]: () => json(200, SHEET), [RUN]: SENT })

describe('upload step', () => {
  it('upload_refusesATxtWithNoRequest', async () => {
    stubFetch({})
    await mount()
    await pick(file('notes.txt'))
    expect(text()).toContain(TYPE_SENTENCE)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('upload_saysTheFirstTwoInvoicesAreChecked', async () => {
    stubFetch({})
    await mount()
    expect(text()).toContain('first 2 invoices')
    expect(text()).toContain('15 MiB')
    expect(fileInput().accept).toBe('.csv,.xlsx,.pdf,.docx')
    expect(fileInput().multiple).toBe(false)
  })

  it('upload_aNewFileClearsTheRefusal', async () => {
    stubFetch({})
    await mount()
    await pick(file('notes.txt'))
    expect(text()).toContain(TYPE_SENTENCE)
    await pick(file('a.pdf'))
    expect(text()).not.toContain(TYPE_SENTENCE)
    expect(byLabel('Email address')).toBeTruthy()
  })

  it('upload_theZoneGivesWayWhileReadingColumns', async () => {
    let release!: (r: Response) => void
    stubFetch({ [MAPPING]: () => new Promise<Response>((r) => (release = r)) })
    await mount()
    await pick(file('a.csv'))
    expect(container.querySelector('input[type="file"]')).toBeNull()
    expect(text()).toContain(READING_TEXT)
    await act(async () => release(json(422, { error: UNREADABLE_SENTENCE })))
    await settle()
    expect(fileInput().disabled).toBe(false)
  })

  it('document_skipsTheMapStep', async () => {
    stubFetch({})
    await mount()
    await pick(file('a.pdf'))
    expect(byLabel('Email address')).toBeTruthy()
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('map step', () => {
  it('map_showsTheColumnsAndTheSuggestion', async () => {
    sheetStub()
    await mount()
    await pick(file('a.csv'))
    const headers = [...container.querySelectorAll('.check-columns tbody td:nth-child(2)')].map((c) => c.textContent)
    expect(headers).toEqual(['Inv', 'Seller', 'STIN', 'Net'])
    expect(byLabel<HTMLSelectElement>('Invoice number').value).toBe('Inv')
    expect(byLabel<HTMLSelectElement>('Supplier name').value).toBe('Seller')
    const tin = byLabel<HTMLSelectElement>('Supplier TIN')
    expect(tin.value).toBe('')
    expect(tin.selectedOptions[0].textContent).toBe('Not in this file')
  })

  it('map_theVisitorPlacesTheSupplierTin', async () => {
    sheetStub()
    await mount()
    await pick(file('a.csv'))
    await choose('Supplier TIN', 'STIN')
    await click(button('Continue'))
    await fillAndSend()
    const body = runBody()
    expect(JSON.parse(body.get('mapping') as string)).toMatchObject({ invoice_number: 'Inv', supplier_name: 'Seller', supplier_tin: 'STIN' })
    expect(body.get('header_row')).toBe('3')
  })

  it('map_continueNeedsTheInvoiceNumber', async () => {
    sheetStub()
    await mount()
    await pick(file('a.csv'))
    expect(button('Continue').disabled).toBe(false)
    await choose('Invoice number', '')
    expect(button('Continue').disabled).toBe(true)
  })

  it('map_aMappingRefusalStaysOnUpload', async () => {
    stubFetch({ [MAPPING]: () => json(422, { error: UNREADABLE_SENTENCE }) })
    await mount()
    await pick(file('a.csv'))
    expect(text()).toContain(UNREADABLE_SENTENCE)
    expect(container.querySelector('.check-columns')).toBeNull()
    expect(fileInput()).toBeTruthy()
  })

  it('map_aSuggestionWithNoColumnsStaysOnUpload', async () => {
    stubFetch({ [MAPPING]: () => json(200, suggestion([], {})) })
    await mount()
    await pick(file('a.csv'))
    expect(text()).toContain(COLUMNS_SENTENCE)
    expect(container.querySelector('.check-columns')).toBeNull()
  })

  it('upload_anotherFileReplacesTheMapping', async () => {
    const answers = [suggestion(['Inv', 'Seller'], { invoice_number: 'Inv', supplier_name: 'Seller' }), suggestion(['No', 'Vendor'], { invoice_number: 'No' })]
    stubFetch({ [MAPPING]: () => json(200, answers.shift()), [RUN]: SENT })
    await mount()
    await pick(file('a.csv'))
    await click(button('Choose another file'))
    await pick(file('b.csv'))
    await click(button('Continue'))
    await fillAndSend()
    const body = runBody()
    expect(JSON.parse(body.get('mapping') as string)).toEqual({ invoice_number: 'No' })
    expect((body.get('file') as File).name).toBe('b.csv')
  })
})

describe('map and details extras', () => {
  it('map_offersExactlyTheCheckFieldsEachWithItsOwnLabel', async () => {
    sheetStub()
    await mount()
    await pick(file('a.csv'))
    const rows = [...container.querySelectorAll('.check-fields .check-field')]
    expect(CHECK_FIELD_KEYS.length).toBeGreaterThan(0)
    expect(rows.map((r) => r.querySelector('select')!.id)).toEqual(CHECK_FIELD_KEYS.map((k) => `check-field-${k}`))
    const labels = rows.map((r) => r.querySelector('label')!.textContent)
    expect(labels).toEqual(CHECK_FIELD_KEYS.map(fieldLabel))
    expect(new Set(labels).size).toBe(labels.length)
    for (const k of CHECK_FIELD_KEYS) expect(labels, 'a raw key shown as a label').not.toContain(k)
  })

  it('details_backFromASpreadsheetReturnsToTheMapWithTheChoicesKept', async () => {
    sheetStub()
    await mount()
    await pick(file('a.csv'))
    await choose('Supplier TIN', 'STIN')
    await click(button('Continue'))
    await click(button('Back'))
    expect(container.querySelector('.check-columns')).not.toBeNull()
    expect(byLabel<HTMLSelectElement>('Supplier TIN').value).toBe('STIN')
  })

  it('details_backFromADocumentReturnsToUploadAndForgetsTheFile', async () => {
    stubFetch({})
    await mount()
    await pick(file('a.pdf'))
    await click(button('Back'))
    expect(fileInput()).toBeTruthy()
    expect(text()).not.toContain('a.pdf')
    expect(container.querySelector('form')).toBeNull()
  })

  it('upload_aDocumentAfterASpreadsheetSendsNoMapping', async () => {
    stubFetch({ [MAPPING]: () => json(200, SHEET), [RUN]: SENT })
    await mount()
    await pick(file('a.csv'))
    await click(button('Choose another file'))
    await pick(file('b.pdf'))
    await fillAndSend()
    expect([...runBody().keys()]).toEqual(['email', 'consent_text', 'file'])
  })

  it('upload_aNewFileClearsAMappingRefusal', async () => {
    stubFetch({ [MAPPING]: () => json(422, { error: UNREADABLE_SENTENCE }) })
    await mount()
    await pick(file('a.csv'))
    expect(text()).toContain(UNREADABLE_SENTENCE)
    await pick(file('b.pdf'))
    expect(byLabel('Email address')).toBeTruthy()
    expect(text()).not.toContain(UNREADABLE_SENTENCE)
  })

  it('details_theConsentBoxShowsAndSendsTheOneConstant', async () => {
    stubFetch({ [RUN]: SENT })
    await mount()
    await pick(file('a.pdf'))
    expect(CONSENT_TEXT).toBe(STORY_CONSENT)
    expect(container.querySelector('.check-consent')!.textContent).toBe(STORY_CONSENT)
    expect(container.querySelector<HTMLInputElement>('.check-consent input')!.required).toBe(true)
    expect(byLabel<HTMLInputElement>('Email address').required).toBe(true)
    expect(byLabel<HTMLInputElement>('Email address').type).toBe('email')
    await fillAndSend()
    expect(runBody().get('consent_text')).toBe(STORY_CONSENT)
  })
})

describe('details and sent', () => {
  it('details_backAndANewFileArriveUnticked', async () => {
    stubFetch({})
    await mount()
    await pick(file('a.pdf'))
    await type(byLabel<HTMLInputElement>('Email address'), 'a@example.com')
    await click(container.querySelector('input[type="checkbox"]')!)
    await click(button('Back'))
    await pick(file('b.pdf'))
    expect(container.querySelector<HTMLInputElement>('input[type="checkbox"]')!.checked).toBe(false)
    expect(byLabel<HTMLInputElement>('Email address').value).toBe('a@example.com')
    expect(button('Send').disabled).toBe(true)
  })

  it('send_aDocument400IsNotTheMappingSentence', async () => {
    stubFetch({ [RUN]: () => json(400, { error: 'invalid request body' }) })
    await mount()
    await pick(file('a.pdf'))
    await fillAndSend()
    expect(text()).toContain(UNREADABLE_SENTENCE)
    expect(text()).not.toContain(MAPPING_SENTENCE)
  })

  it('send_aTooLongDocumentShowsTheTooMuchTextSentence', async () => {
    const sentence = 'This document has too much text for the free check. Try a shorter invoice.' // readinesscheck.DocumentTooLongSentence
    stubFetch({ [RUN]: () => json(400, { error: sentence }) })
    await mount()
    await pick(file('a.pdf'))
    await fillAndSend()
    expect(text()).toContain(sentence)
    expect(text()).not.toContain(UNREADABLE_SENTENCE)
  })

  it('send_aCeilingRefusalShowsTheDailyLimitSentence', async () => {
    const sentence = 'The free check has reached its limit for today. Try again tomorrow.' // gateway checkCeilingSentence
    stubFetch({ [RUN]: () => json(503, { error: sentence }) })
    await mount()
    await pick(file('a.pdf'))
    await fillAndSend()
    expect(text()).toContain(sentence)
    expect(text()).not.toContain(UNAVAILABLE_SENTENCE)
  })

  it('suggest_aCeilingRefusalShowsTheDailyLimitSentence', async () => {
    const sentence = 'The free check has reached its limit for today. Try again tomorrow.' // gateway checkCeilingSentence
    stubFetch({ [MAPPING]: () => json(503, { error: sentence }) })
    await mount()
    await pick(file('a.csv'))
    expect(text()).toContain(sentence)
    expect(text()).not.toContain(UNAVAILABLE_SENTENCE)
  })

  it('send_aSpreadsheet400IsTheMappingSentence', async () => {
    stubFetch({ [MAPPING]: () => json(200, SHEET), [RUN]: () => json(400, { error: 'invalid request body' }) })
    await mount()
    await pick(file('a.csv'))
    await click(button('Continue'))
    await fillAndSend()
    expect(text()).toContain(MAPPING_SENTENCE)
  })

  it('send_isDisabledUntilEmailAndTick', async () => {
    stubFetch({})
    await mount()
    await pick(file('a.pdf'))
    expect(button('Send').disabled).toBe(true)
    await type(byLabel<HTMLInputElement>('Email address'), 'a@example.com')
    expect(button('Send').disabled).toBe(true)
    await click(container.querySelector('input[type="checkbox"]')!)
    expect(button('Send').disabled).toBe(false)
  })

  it('send_200ShowsTheConfirmTextWithTheTrimmedAddress', async () => {
    stubFetch({ [RUN]: SENT })
    await mount()
    await pick(file('a.pdf'))
    await fillAndSend('  a@x.io ')
    const status = container.querySelector('.check-tool [role="status"]')!.textContent!
    expect(status).toContain('Check your inbox at a@x.io.')
    expect(status).toContain(SENT_NOTE)
    expect(status).not.toMatch(/24 hours|button|send your results/i)
    expect(status).toContain('no-reply@ascomply.com')
    expect(status).not.toMatch(/on their\s+way|checks_left|rule/i)
    expect(container.querySelector('form')).toBeNull()
  })

  it('send_showsTheSendingSentenceWhileWaiting', async () => {
    stubFetch({ [RUN]: () => new Promise<Response>(() => {}) })
    await mount()
    await pick(file('a.pdf'))
    await fillAndSend()
    expect(text()).toContain(SENDING_TEXT)
    expect(button('Send').disabled).toBe(true)
  })

  it('send_409IsTheUnavailableSentenceWithoutSignUp', async () => {
    stubFetch({ [RUN]: () => json(409, { error: 'x' }) })
    await mount()
    await pick(file('a.pdf'))
    await fillAndSend()
    const alert = container.querySelector('[role="alert"]')!
    expect(alert.textContent).toBe(UNAVAILABLE_SENTENCE)
    expect(alert.querySelector('a')).toBeNull()
  })

  it('send_429FromTheGatewayShowsItsSentence', async () => {
    stubFetch({ [RUN]: () => json(429, { error: IP_SENTENCE }) })
    await mount()
    await pick(file('a.pdf'))
    await fillAndSend()
    expect(text()).toContain(IP_SENTENCE)
  })

  it('send_429FromArmorShowsTheHourSentence', async () => {
    stubFetch({ [RUN]: () => new Response('<html>429</html>', { status: 429, headers: { 'Content-Type': 'text/html' } }) })
    await mount()
    await pick(file('a.pdf'))
    await fillAndSend()
    expect(text()).toContain(ARMOR_SENTENCE)
  })

  it('send_networkFailureKeepsTheFileAndAllowsRetry', async () => {
    stubFetch({ [RUN]: () => Promise.reject(new TypeError('Failed to fetch')) })
    await mount()
    await pick(file('a.pdf'))
    await fillAndSend()
    expect(text()).toContain(UNAVAILABLE_SENTENCE)
    expect(text()).toContain('a.pdf')
    expect(button('Send').disabled).toBe(false)
    expect(calls(RUN)).toBe(1)
  })
})
