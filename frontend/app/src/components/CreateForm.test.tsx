// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { Customer } from '../lib/buyerDirectory'
import { DRAFT_NRS_BLANK, draftLine } from '../lib/clients'
import type { CarriedReading } from '../lib/importApi'
import { labelOf, type InvoiceField } from '../lib/invoiceFields'
import type { Draft, DraftLineNRSKey, PlatformCtx } from '../types'
import { CreateForm } from './CreateForm'

const HEADER_KEYS = [
  'invoice_kind', 'tax_currency_code', 'due_date', 'issue_time', 'tax_point_date', 'payment_status',
  'supplier_email', 'supplier_telephone', 'supplier_street', 'supplier_city', 'supplier_postal_zone',
  'supplier_country', 'supplier_state', 'supplier_lga',
  'buyer_email', 'buyer_telephone', 'buyer_street', 'buyer_city', 'buyer_postal_zone',
  'buyer_country', 'buyer_state', 'buyer_lga',
] as const
const LINE_KEYS = [
  'tax_category', 'tax_percent', 'hsn_code', 'isic_code', 'product_category', 'service_category',
  'sellers_item_identification', 'price_unit',
] as const

const TIN_A = '12345678-0001'
const TIN_B = '87654321-0002'

function customer(tin: string, over: Partial<Customer> = {}): Customer {
  return {
    id: `c-${tin}`, tin, name: 'Acme Buyer', contact_person: null, buyer_type: 'B2B', email: 'buy@acme.test',
    telephone: '0800', street: '1 Main', city: 'Ikeja', postal_zone: '100001', country: 'NG', state: 'NG-LA',
    lga: 'NG-LA-IK', created_at: '2026-01-01T00:00:00Z', ...over,
  }
}

function blankDraft(over: Partial<Draft> = {}): Draft {
  return {
    number: 'INV-1', buyer: '', buyerTin: '', date: '', currency: 'NGN', ...DRAFT_NRS_BLANK,
    items: [draftLine({ desc: 'Bolts', qty: 1, price: 1000 }), draftLine({ desc: 'Nuts', qty: 2, price: 5 })],
    ...over,
  }
}

type Fetch = (url: string) => Promise<unknown>
let authedFetch: ReturnType<typeof vi.fn<Fetch>>

// A stateful host so the form's own writers drive the draft, like App.tsx does.
function mount(initial: Draft, reading: CarriedReading | null = null) {
  function Host() {
    const [draft, setDraft] = useState(initial)
    const ctx = {
      active: { short: 'Acme' },
      activeEntity: { id: 'e-1', name: 'Acme Ltd', tin: TIN_A },
      draft,
      filing: false,
      filingError: null,
      handOffReading: reading,
      authedFetch,
      updateDraft: (k: keyof Draft, v: string) => setDraft((d) => ({ ...d, [k]: v })),
      updateDraftFields: (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch })),
      updateItemField: (i: number, k: DraftLineNRSKey, v: string) =>
        setDraft((d) => ({ ...d, items: d.items.map((it, idx) => (idx === i ? { ...it, [k]: v } : it)) })),
      updateItemDesc: vi.fn(),
      updateItem: vi.fn(),
      addItem: vi.fn(),
      removeItem: vi.fn(),
      fileDraft: vi.fn(),
    } as unknown as PlatformCtx
    return <CreateForm ctx={ctx} />
  }
  return render(<Host />)
}

const flush = () => act(async () => {})
const byLabel = (key: string) => screen.getByLabelText(labelOf(key as InvoiceField['key'])) as HTMLInputElement
const tinInput = () => screen.getByPlaceholderText('########-####') as HTMLInputElement
const nameInput = () => screen.getByText('Buyer name', { exact: true }).nextElementSibling as HTMLInputElement

async function blurTin(tin: string) {
  fireEvent.change(tinInput(), { target: { value: tin } })
  fireEvent.blur(tinInput())
  await flush()
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gateway.test')
  authedFetch = vi.fn<Fetch>(async () => ({ codes: [] }))
})
afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

describe('CreateForm NRS fields', () => {
  it('createForm_manualModeShowsEveryNrsField', () => {
    mount(blankDraft())
    for (const key of HEADER_KEYS) expect(byLabel(key), key).toBeTruthy()
    const rows = screen.getAllByTestId('draft-line-nrs')
    expect(rows).toHaveLength(2)
    for (const row of rows) {
      for (const key of LINE_KEYS) expect(row.querySelectorAll(`[data-line-field="${key}"]`), key).toHaveLength(1)
      const base = within(row).getByLabelText(/^Base quantity/) as HTMLInputElement
      expect(base.value).toBe('1')
      expect(base.readOnly).toBe(true)
      expect((within(row).getByLabelText(/^Line tax/) as HTMLInputElement).readOnly).toBe(true)
    }
  })

  it('createForm_carriedModeIsUnchanged', () => {
    const reading = {
      document_id: 'd', extraction_job_id: 'j', issue_date: null, buyer_tin: null, buyer_name: null, currency: 'NGN',
      subtotal: null, vat: null, total: null,
      line_items: [{ description: 'Bolts', quantity: '1', unit_price: '5', line_total: '5', line_tax: null }],
    } as CarriedReading
    mount(blankDraft(), reading)
    expect(screen.queryByTestId('draft-line-nrs')).toBeNull()
    expect(screen.queryByText('Supplier details')).toBeNull()
    expect(screen.queryByLabelText('Due date')).toBeNull()
  })

  it('createForm_buyerTypeNote', () => {
    const { unmount } = mount(blankDraft({ invoice_kind: 'B2C' }))
    expect(screen.getByTestId('buyer-type-note').textContent).toBe('Buyer details are optional for a B2C invoice.')
    expect(document.querySelectorAll('[aria-required="true"]')).toHaveLength(0)
    unmount()
    mount(blankDraft({ invoice_kind: 'B2G' }))
    expect(screen.getByTestId('buyer-type-note').textContent).toBe('Buyer TIN, name, email and address are required for a B2B or B2G invoice.')
    expect(document.querySelectorAll('input[aria-required="true"]')).toHaveLength(9)
  })
})

describe('CreateForm buyer lookup', () => {
  it('createForm_knownTinFillsTheBuyer', async () => {
    authedFetch.mockImplementation(async () => ({ customers: [customer(TIN_A)], pagination: { limit: 1, offset: 0, total: 1 } }))
    mount(blankDraft())
    await blurTin(TIN_A)
    expect(authedFetch).toHaveBeenCalledWith(`https://gateway.test/api/invoice/v1/customers?tin=${TIN_A}&limit=1`)
    expect(nameInput().value).toBe('Acme Buyer')
    expect(byLabel('invoice_kind').value).toBe('B2B')
    expect(byLabel('buyer_email').value).toBe('buy@acme.test')
    expect(byLabel('buyer_lga').value).toBe('NG-LA-IK')
    expect(tinInput().value).toBe(TIN_A)
  })

  it('createForm_unknownTinClearsThePreviousFill', async () => {
    authedFetch.mockImplementation(async (url) =>
      url.includes(TIN_A)
        ? { customers: [customer(TIN_A)], pagination: { limit: 1, offset: 0, total: 1 } }
        : { customers: [], pagination: { limit: 1, offset: 0, total: 0 } },
    )
    mount(blankDraft())
    await blurTin(TIN_A)
    await blurTin(TIN_B)
    expect(nameInput().value).toBe('')
    expect(byLabel('buyer_email').value).toBe('')
    expect(tinInput().value).toBe(TIN_B)
  })

  it('createForm_staleLookupIsIgnored', async () => {
    let resolveA!: (v: unknown) => void
    authedFetch.mockImplementation((url) =>
      url.includes(TIN_A)
        ? new Promise((r) => (resolveA = r))
        : Promise.resolve({ customers: [customer(TIN_B, { name: 'B Buyer' })], pagination: { limit: 1, offset: 0, total: 1 } }),
    )
    mount(blankDraft())
    await blurTin(TIN_A)
    await blurTin(TIN_B)
    expect(nameInput().value).toBe('B Buyer')
    await act(async () => resolveA({ customers: [customer(TIN_A, { name: 'A Buyer' })], pagination: { limit: 1, offset: 0, total: 1 } }))
    expect(nameInput().value).toBe('B Buyer')
  })

  it('createForm_answerForATinNoLongerInTheFieldIsIgnored', async () => {
    let resolveA!: (v: unknown) => void
    authedFetch.mockImplementation(() => new Promise((r) => (resolveA = r)))
    mount(blankDraft())
    await blurTin(TIN_A)
    await blurTin('1234')
    await act(async () => resolveA({ customers: [customer(TIN_A, { name: 'A Buyer' })], pagination: { limit: 1, offset: 0, total: 1 } }))
    expect(tinInput().value).toBe('1234')
    expect(nameInput().value, 'the buyer of a TIN the user replaced is not filled').toBe('')
  })

  it('createForm_failedLookupChangesNothing', async () => {
    const error = vi.spyOn(console, 'error').mockImplementation(() => {})
    authedFetch.mockRejectedValue(new Error('boom'))
    mount(blankDraft({ buyer: 'Typed Buyer', buyer_email: 'typed@x.test' }))
    await blurTin(TIN_A)
    expect(authedFetch).toHaveBeenCalledOnce()
    expect(nameInput().value).toBe('Typed Buyer')
    expect(byLabel('buyer_email').value).toBe('typed@x.test')
    expect(error).not.toHaveBeenCalled()
  })

  it('createForm_sameTinDoesNotRefetchButAFailedOneRetries', async () => {
    authedFetch.mockRejectedValueOnce(new Error('boom')).mockImplementation(async () => ({ customers: [customer(TIN_A)], pagination: { limit: 1, offset: 0, total: 1 } }))
    mount(blankDraft())
    await blurTin(TIN_A)
    await blurTin(TIN_A)
    expect(authedFetch, 'a failed lookup may be retried').toHaveBeenCalledTimes(2)
    expect(nameInput().value).toBe('Acme Buyer')
    fireEvent.blur(tinInput())
    await flush()
    expect(authedFetch, 'the same answered TIN is not fetched again').toHaveBeenCalledTimes(2)
  })

  it('createForm_lookupKeepsTheTypedTinAndTrimsIt', async () => {
    authedFetch.mockImplementation(async () => ({ customers: [customer('99999999-9999')], pagination: { limit: 1, offset: 0, total: 1 } }))
    mount(blankDraft())
    await blurTin(` ${TIN_A} `)
    expect(authedFetch).toHaveBeenCalledWith(expect.stringContaining(`tin=${TIN_A}&`))
    expect(nameInput().value).toBe('Acme Buyer')
    expect(tinInput().value, 'the record never rewrites the typed TIN').toBe(` ${TIN_A} `)
  })

  it('createForm_lookupOnlyForAFileableTin', async () => {
    mount(blankDraft())
    await blurTin('1234')
    expect(authedFetch).not.toHaveBeenCalled()
  })
})

describe('CreateForm tax', () => {
  it('createForm_eachRowShowsItsOwnLineTax', () => {
    mount(blankDraft({ items: [draftLine({ desc: 'Bolts', qty: 1, price: 1000 }), { ...draftLine({ desc: 'Nuts', qty: 2, price: 5 }), tax_category: 'STANDARD_VAT', tax_percent: '10' }] }))
    expect((screen.getByLabelText('Line tax 1') as HTMLInputElement).value).toBe('')
    expect((screen.getByLabelText('Line tax 2') as HTMLInputElement).value).toBe('1.00')
  })

  it('createForm_taxCategoryPickFillsPercentAndTax', async () => {
    authedFetch.mockImplementation(async (url) =>
      url.includes('/code-lists/tax-categories')
        ? { codes: [{ code: 'STANDARD_VAT', label: 'Standard VAT', percent: '7.5' }] }
        : { codes: [] },
    )
    mount(blankDraft({ items: [draftLine({ desc: 'Bolts', qty: 1, price: 1000 })] }))
    expect(screen.getByText('VAT · 7.5%')).toBeTruthy()
    const category = screen.getByLabelText('Tax category 1') as HTMLInputElement
    fireEvent.focus(category)
    await flush()
    fireEvent.change(category, { target: { value: 'STANDARD_VAT' } })
    await flush()
    expect((screen.getByLabelText('Tax percent 1') as HTMLInputElement).value).toBe('7.5')
    expect((screen.getByLabelText('Line tax 1') as HTMLInputElement).value).toBe('75.00')
    expect(screen.getByText('VAT', { exact: true })).toBeTruthy()
    expect(screen.getByTestId('summary-vat').textContent).toBe('75.00')
  })
})
