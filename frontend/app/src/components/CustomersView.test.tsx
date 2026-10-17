// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { Customer, CustomerListResponse } from '../lib/buyerDirectory'
import { fmt } from '../lib/format'
import { AGGREGATE_PAGE_SIZE } from '../lib/invoices'
import type { InvoiceRecord } from '../lib/invoices'
import type { PlatformCtx } from '../types'
import { CustomersView } from './CustomersView'

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

const ok = (body: unknown): MockResponse => ({ ok: true, status: 200, json: () => Promise.resolve(body) })
const down: MockResponse = { ok: false, status: 503, json: () => Promise.resolve({ error: 'down' }) }

function record(over: Partial<Customer> = {}): Customer {
  return {
    id: 'c1',
    tin: '20000000-0001',
    name: 'Alpha Traders',
    contact_person: null,
    buyer_type: 'B2B',
    email: null,
    telephone: null,
    street: null,
    city: null,
    postal_zone: null,
    country: null,
    state: null,
    lga: null,
    status: 'active',
    created_at: '2026-07-01T00:00:00Z',
    ...over,
  }
}

function recordsPage(customers: Customer[], total = customers.length, offset = 0): MockResponse {
  const body: CustomerListResponse = { customers, pagination: { limit: 50, offset, total } }
  return ok(body)
}

function row(over: Partial<InvoiceRecord> = {}): InvoiceRecord {
  return {
    id: 'inv-x',
    entity_id: 'ent-1',
    import_batch_id: null,
    invoice_number: 'INV-X',
    status: 'draft',
    issue_date: '2026-07-01',
    supplier_tin: '10000000-0001',
    supplier_name: 'Acme Ltd',
    buyer_tin: '20000000-0001',
    buyer_name: 'Beta Traders',
    currency: 'NGN',
    subtotal: '1000.00',
    vat: '75.00',
    total: '1075.00',
    violations: [],
    rule_set_version_id: null,
    created_at: '2026-07-01T00:00:00Z',
    irn: null,
    csid: null,
    qr_payload: null,
    rejection_reasons: [],
    kept_as_is_at: null,
    kept_as_is_by: null,
    kept_as_is_reason: null,
    failure_kind: null,
    approval: null,
    rule_set_version: null,
    can_approve: false,
    approve_blocked_reason: null,
    can_submit: false,
    submit_blocked_reason: null,
    demotion: null,
    ...over,
  }
}

function invoicesPage(invoices: InvoiceRecord[], total = invoices.length, offset = 0): MockResponse {
  return ok({ invoices, pagination: { limit: AGGREGATE_PAGE_SIZE, offset, total } })
}

type Handler = (url: string) => MockResponse | Promise<MockResponse>

// Routes by method and path, never by call order: the screen fires both reads at mount.
function mockApi(handlers: { customers?: Handler; invoices?: Handler; createCustomer?: Handler; transition?: Handler }) {
  const fetchMock = vi.fn((url: string, init?: { method?: string }) => {
    const path = new URL(url).pathname
    const method = init?.method ?? 'GET'
    const h =
      method === 'POST' && /\/(archive|restore)$/.test(path)
        ? handlers.transition
        : method === 'POST' && path.endsWith('/v1/customers')
        ? handlers.createCustomer
        : method === 'GET' && path.endsWith('/v1/customers')
          ? handlers.customers
          : method === 'GET' && path.endsWith('/v1/invoices')
            ? handlers.invoices
            : undefined
    if (!h) throw new Error(`unrouted ${method} ${url}`)
    return Promise.resolve(h(url))
  })
  vi.stubGlobal('fetch', fetchMock)
  const calls = (suffix: string) => fetchMock.mock.calls.filter((c) => c[1]?.method !== 'POST').map((c) => c[0] as string).filter((u) => new URL(u).pathname.endsWith(suffix))
  return { fetchMock, customerCalls: () => calls('/v1/customers'), invoiceCalls: () => calls('/v1/invoices') }
}

function ctxFor(mode: 'firm' | 'inhouse' = 'firm', entityId = 'ent-1', tenantName: string | null = null): PlatformCtx {
  return {
    mode,
    active: { name: 'Acme Co', short: 'Acme', entityId },
    user: { tenantName },
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    openCreate: () => {},
  } as unknown as PlatformCtx
}

const paramsOf = (url: string) => new URL(url).searchParams
const rows = () => document.querySelectorAll('.pf-list-row')
const cells = (r: Element) => Array.from(r.children).map((c) => c.textContent)

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('CustomersView: no KPI card row', () => {
  it('renders no .pf-grid-4 wrapper and none of the four card labels', async () => {
    mockApi({ customers: () => recordsPage([record()]), invoices: () => invoicesPage([]) })

    const { container } = render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')

    expect(container.querySelector('.pf-grid-4'), 'no KPI card row wrapper').toBeNull()
    expect(screen.queryAllByText('Valid TINs')).toHaveLength(0)
    expect(screen.queryAllByText('Flagged')).toHaveLength(0)
    expect(screen.queryAllByText('Total billed'), 'only the table header').toHaveLength(1)
  })
})

describe('CustomersView: the record list', () => {
  it('CU-09 one row per record, keyed by id, seven columns', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    mockApi({
      customers: () => recordsPage([record({ id: 'a', name: 'Alpha' }), record({ id: 'b', tin: '20000000-0002', name: null }), record({ id: 'c', tin: '20000000-0003', name: 'Alpha' })]),
      invoices: () => invoicesPage([]),
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Customer')

    const head = document.querySelector('.pf-list-head') as HTMLElement
    expect(cells(head)).toEqual(['Customer', 'TIN', 'Type', 'Invoices', 'Total billed', 'Tax status', 'Action'])
    expect(rows()).toHaveLength(3)
    expect(Array.from(rows()).map((r) => r.children.length)).toEqual([7, 7, 7])
    expect(cells(rows()[0])[0]).toBe('AAlpha')
    expect(cells(rows()[1])[0], 'a nameless record reads a dash').toBe('——')
    expect(cells(rows()[2])[0], 'two records sharing a name render two rows').toBe('AAlpha')
    expect(Array.from(rows()).map((r) => cells(r)[1]), 'TIN comes from the record').toEqual(['20000000-0001', '20000000-0002', '20000000-0003'])
    expect(consoleError.mock.calls.map((c) => String(c[0])).filter((m) => m.includes('same key')), 'no duplicate React key').toEqual([])
    consoleError.mockRestore()
  })

  it('CU-10 the record name wins over the invoice name', async () => {
    mockApi({
      customers: () => recordsPage([record({ name: 'Record Ltd' })]),
      invoices: () => invoicesPage([row({ buyer_tin: '20000000-0001', buyer_name: 'Invoice Ltd' })]),
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Record Ltd')

    expect(screen.queryByText('Invoice Ltd')).toBeNull()
  })

  it('CU-11 a record with no name takes the invoice name', async () => {
    mockApi({
      customers: () => recordsPage([record({ name: null })]),
      invoices: () => invoicesPage([row({ buyer_tin: '20000000-0001', buyer_name: 'Invoice Ltd' })]),
    })
    render(<CustomersView ctx={ctxFor()} />)

    expect(await screen.findByText('Invoice Ltd')).toBeTruthy()
  })

  it('CU-12 type comes from the record, a dash when null', async () => {
    mockApi({
      customers: () => recordsPage([record({ id: 'a', buyer_type: 'B2G' }), record({ id: 'b', tin: '20000000-0002', buyer_type: null })]),
      invoices: () => invoicesPage([]),
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('B2G')

    expect(cells(rows()[0])[2]).toBe('B2G')
    expect(cells(rows()[1])[2]).toBe('—')
  })

  it('CU-13 counts and totals join by TIN over the whole set', async () => {
    const filler = Array.from({ length: AGGREGATE_PAGE_SIZE }, (_, i) => row({ id: `f${i}`, buyer_tin: `9${String(i).padStart(7, '0')}-0001`, total: '1.00' }))
    const mine = row({ id: 'mine', buyer_tin: '20000000-0001', total: '500.00' })
    const { invoiceCalls } = mockApi({
      customers: () => recordsPage([record()]),
      invoices: (url) => (paramsOf(url).get('offset') === '0' ? invoicesPage(filler, 201, 0) : invoicesPage([mine], 201, AGGREGATE_PAGE_SIZE)),
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')

    expect(cells(rows()[0]).slice(3, 5)).toEqual(['1', fmt(500)])
    expect(invoiceCalls().map((u) => paramsOf(u).get('offset')).sort()).toEqual(['0', String(AGGREGATE_PAGE_SIZE)])
  })

  it('CU-14 a record with no invoice shows 0 and a zero total', async () => {
    mockApi({
      customers: () => recordsPage([record()]),
      invoices: () => invoicesPage([row({ buyer_tin: '30000000-0001' })]),
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')

    expect(cells(rows()[0]).slice(3, 5)).toEqual(['0', fmt(0)])
  })

  it('CU-15 invoice buyers with no record add no row', async () => {
    mockApi({
      customers: () => recordsPage([record()]),
      invoices: () => invoicesPage([row({ id: 'i1' }), row({ id: 'i2', buyer_tin: null, buyer_name: 'No Tin Co' }), row({ id: 'i3', buyer_tin: '12345', buyer_name: 'Bad Tin Co' })]),
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')

    expect(rows()).toHaveLength(1)
    expect(screen.queryByText('No Tin Co')).toBeNull()
    expect(screen.queryByText('Bad Tin Co')).toBeNull()
  })

  it('CU-16 the invoice fetch is workspace-wide for both personas', async () => {
    for (const mode of ['firm', 'inhouse'] as const) {
      const { invoiceCalls } = mockApi({ customers: () => recordsPage([record()]), invoices: () => invoicesPage([]) })
      const { unmount } = render(<CustomersView ctx={ctxFor(mode, 'ent-1')} />)
      await screen.findByText('Alpha Traders')

      expect(invoiceCalls().length, `${mode} fetched invoices`).toBeGreaterThan(0)
      for (const u of invoiceCalls()) expect(paramsOf(u).has('entity_id'), `${mode} ${u}`).toBe(false)
      unmount()
    }
  })

  it('CU-17 a client switch neither refetches nor blanks', async () => {
    const { fetchMock } = mockApi({ customers: () => recordsPage([record()]), invoices: () => invoicesPage([]) })
    const { rerender } = render(<CustomersView ctx={ctxFor('firm', 'ent-1')} />)
    await screen.findByText('Alpha Traders')
    const before = fetchMock.mock.calls.length

    rerender(<CustomersView ctx={ctxFor('firm', 'ent-2')} />)

    expect(screen.getByText('Alpha Traders'), 'rows stay').toBeTruthy()
    await waitFor(() => expect(fetchMock.mock.calls.length).toBe(before))
  })

  it('CU-18 the pager shows only above one page and Next requests offset 50', async () => {
    const { customerCalls } = mockApi({
      customers: (url) => recordsPage([record({ id: `c${paramsOf(url).get('offset')}` })], 51, Number(paramsOf(url).get('offset'))),
      invoices: () => invoicesPage([]),
    })
    render(<CustomersView ctx={ctxFor()} />)
    fireEvent.click(await screen.findByRole('button', { name: /Next/ }))

    await waitFor(() => expect(customerCalls()).toHaveLength(2))
    expect(paramsOf(customerCalls()[1]).get('offset')).toBe('50')
    expect(screen.getByTestId('customers-pager')).toBeTruthy()

    fireEvent.click(await screen.findByRole('button', { name: /Previous/ }))
    await waitFor(() => expect(customerCalls()).toHaveLength(3))
    expect(paramsOf(customerCalls()[2]).get('offset')).toBe('0')

    cleanup()
    mockApi({ customers: () => recordsPage([record()], 50), invoices: () => invoicesPage([]) })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')
    expect(screen.queryByTestId('customers-pager')).toBeNull()
  })

  it('CU-23 records with zero invoices still render the table', async () => {
    mockApi({
      customers: () => recordsPage([record({ id: 'a' }), record({ id: 'b', tin: '20000000-0002', name: 'Beta' })]),
      invoices: () => invoicesPage([], 0),
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Beta')

    expect(rows()).toHaveLength(2)
    for (const r of Array.from(rows())) expect(cells(r).slice(3, 5)).toEqual(['0', fmt(0)])
    expect(screen.queryByText('No customers yet')).toBeNull()
  })

  it("CU-24 a nameless row's avatar reads a dash", async () => {
    mockApi({ customers: () => recordsPage([record({ name: null })]), invoices: () => invoicesPage([]) })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Customer')

    expect((rows()[0].children[0].children[0] as HTMLElement).textContent).toBe('—')
  })

  it('shows the tenant in the subtitle when known', async () => {
    mockApi({ customers: () => recordsPage([record()]), invoices: () => invoicesPage([]) })
    render(<CustomersView ctx={ctxFor('firm', 'ent-1', 'Acme Advisory')} />)

    expect(await screen.findByText('Acme Advisory · Customer records, tax IDs & billing history')).toBeTruthy()
  })
})

describe('CustomersView: edit', () => {
  it('CU-21 a row click opens the editor for that row, not another', async () => {
    mockApi({
      customers: () => recordsPage([record({ id: 'a', name: 'First Co' }), record({ id: 'b', tin: '20000000-0002', name: 'Second Co' })]),
      invoices: () => invoicesPage([]),
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Second Co')

    fireEvent.click(rows()[1])

    const dialog = within(screen.getByRole('dialog', { name: 'Edit customer' }))
    expect(dialog.getByText('20000000-0002')).toBeTruthy()
    expect((dialog.getByLabelText('Name') as HTMLInputElement).value).toBe('Second Co')
  })

  it('CU-22 a preparer can open and save; the records page refetches', async () => {
    const patches: unknown[] = []
    const { customerCalls, invoiceCalls, fetchMock } = mockApi({ customers: () => recordsPage([record({ id: 'a' })]), invoices: () => invoicesPage([]) })
    const routed = fetchMock.getMockImplementation()!
    fetchMock.mockImplementation(((url: string, init?: { method?: string; body?: unknown }) => {
      if (init?.method === 'PATCH') {
        patches.push({ url, body: JSON.parse(String(init.body)) })
        return Promise.resolve(ok(record({ id: 'a', email: 'new@x.ng' })))
      }
      return routed(url)
    }) as typeof routed)
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')
    const [recordsBefore, invoicesBefore] = [customerCalls().length, invoiceCalls().length]

    fireEvent.click(rows()[0])
    const dialog = within(screen.getByRole('dialog', { name: 'Edit customer' }))
    fireEvent.change(dialog.getByLabelText('Email'), { target: { value: 'new@x.ng' } })
    fireEvent.click(dialog.getByRole('button', { name: 'Save changes' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(patches).toEqual([{ url: 'https://gw/api/invoice/v1/customers/a', body: { email: 'new@x.ng' } }])
    await waitFor(() => expect(customerCalls().length).toBe(recordsBefore + 1))
    expect(invoiceCalls().length, 'the invoice fetch does not rerun').toBe(invoicesBefore)
  })
})

describe('CustomersView: add', () => {
  const addButton = () => screen.getByRole('button', { name: 'Add customer' }) as HTMLButtonElement

  it('CU-25 Add customer opens an empty create form', async () => {
    const { fetchMock } = mockApi({
      customers: () => recordsPage([record({ id: 'a' }), record({ id: 'b', tin: '20000000-0002', name: 'Beta' })]),
      invoices: () => invoicesPage([]),
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Beta')
    const before = fetchMock.mock.calls.length

    fireEvent.click(addButton())

    const dialog = within(screen.getByRole('dialog', { name: 'Add customer' }))
    expect((dialog.getByRole('textbox', { name: 'TIN' }) as HTMLInputElement).value).toBe('')
    const inputs = Array.from(document.querySelectorAll('[role="dialog"] input')) as HTMLInputElement[]
    expect(inputs).toHaveLength(11)
    for (const i of inputs) expect(i.value, i.getAttribute('aria-label') ?? '').toBe('')
    expect((dialog.getByLabelText('Buyer type') as HTMLSelectElement).value).toBe('')
    expect(fetchMock.mock.calls.length, 'no request beyond the two reads').toBe(before)
  })

  it('CU-26 Add customer shows for every member and is disabled without a gateway', async () => {
    mockApi({ customers: () => recordsPage([record()]), invoices: () => invoicesPage([]) })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')
    expect(addButton().disabled).toBe(false)
    cleanup()

    vi.stubEnv('VITE_GATEWAY_URL', '')
    const { fetchMock } = mockApi({})
    render(<CustomersView ctx={ctxFor()} />)
    expect(addButton().disabled).toBe(true)
    fireEvent.click(addButton())
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(fetchMock).not.toHaveBeenCalled()
  })

  async function openWith409() {
    const held = record({ id: 'h', tin: 'RN-77', name: 'Held Co', status: 'archived' })
    const lookups: string[] = []
    const api = mockApi({
      customers: (url) => {
        if (paramsOf(url).get('tin')) {
          lookups.push(url)
          return recordsPage([held])
        }
        return recordsPage([record()])
      },
      invoices: () => invoicesPage([]),
      createCustomer: () => ({ ok: false, status: 409, json: () => Promise.resolve({ error: 'duplicate tin' }) }),
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')
    fireEvent.click(addButton())
    fireEvent.change(screen.getByRole('textbox', { name: 'TIN' }), { target: { value: 'RN-77' } })
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Add customer' }))
    return { ...api, lookups }
  }

  it('CU-27 Open customer opens Edit customer on the held record', async () => {
    const { lookups } = await openWith409()
    fireEvent.click(await screen.findByRole('button', { name: 'Open customer' }))

    const dialog = await screen.findByRole('dialog', { name: 'Edit customer' })
    expect(within(dialog).getByText('RN-77')).toBeTruthy()
    expect((within(dialog).getByLabelText('Name') as HTMLInputElement).value, 'the edit form is filled from the held record').toBe('Held Co')
    expect(screen.queryByRole('dialog', { name: 'Add customer' })).toBeNull()
    expect(lookups).toHaveLength(1)
    const q = paramsOf(lookups[0])
    expect([q.get('limit'), q.get('offset'), q.get('tin'), q.has('status')]).toEqual(['1', '0', 'RN-77', false])
  })

  it('CU-27b a failed or empty lookup keeps the create form with the generic banner', async () => {
    const { fetchMock } = await openWith409()
    const routed = fetchMock.getMockImplementation()!
    let answer: MockResponse = down
    fetchMock.mockImplementation(((url: string, init?: { method?: string }) =>
      new URL(url).searchParams.get('tin') ? Promise.resolve(answer) : routed(url, init)) as typeof routed)

    for (const a of [down, recordsPage([])]) {
      answer = a
      fireEvent.click(await screen.findByRole('button', { name: 'Open customer' }))
      expect(await screen.findByText('Something went wrong. Please try again.')).toBeTruthy()
      expect(screen.getByRole('dialog', { name: 'Add customer' })).toBeTruthy()
      expect(screen.queryByRole('dialog', { name: 'Edit customer' })).toBeNull()
      await waitFor(() => expect((screen.getByRole('button', { name: 'Open customer' }) as HTMLButtonElement).disabled).toBe(false))
    }
  })

  it('CU-27c Cancel is blocked while Open customer is looking up, so no Edit form opens after', async () => {
    let release: (r: MockResponse) => void = () => {}
    const held = new Promise<MockResponse>((resolve) => {
      release = resolve
    })
    mockApi({
      customers: (url) => (paramsOf(url).get('tin') ? held : recordsPage([record()])),
      invoices: () => invoicesPage([]),
      createCustomer: () => ({ ok: false, status: 409, json: () => Promise.resolve({ error: 'duplicate tin' }) }),
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')
    fireEvent.click(addButton())
    fireEvent.change(screen.getByRole('textbox', { name: 'TIN' }), { target: { value: 'RN-77' } })
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Add customer' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Open customer' }))

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(screen.getByRole('dialog', { name: 'Add customer' })).toBeTruthy()

    release(recordsPage([record({ id: 'h', tin: 'RN-77', name: 'Held Co' })]))
    expect(await screen.findByRole('dialog', { name: 'Edit customer' })).toBeTruthy()
  })

  it('CU-28 Open customer on an archived record selects Archived', async () => {
    const { customerCalls } = await openWith409()
    const listCalls = () => customerCalls().filter((u) => !paramsOf(u).get('tin'))
    expect(paramsOf(listCalls()[0]).get('status')).toBe('active')
    fireEvent.click(await screen.findByRole('button', { name: 'Open customer' }))

    await screen.findByRole('dialog', { name: 'Edit customer' })
    await waitFor(() => expect(listCalls()).toHaveLength(2))
    const q = paramsOf(listCalls()[1])
    expect([q.get('status'), q.get('offset')]).toEqual(['archived', '0'])
    expect(screen.getByRole('button', { name: 'Archived' }).style.background).toBe('var(--action)')
  })

  it('CU-26b a create on the Archived chip selects Active', async () => {
    const { customerCalls } = mockApi({
      customers: () => recordsPage([record()]),
      invoices: () => invoicesPage([]),
      createCustomer: () => ({ ok: true, status: 201, json: () => Promise.resolve(record({ id: 'n', tin: 'RN-5' })) }),
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')
    fireEvent.click(screen.getByRole('button', { name: 'Archived' }))
    await waitFor(() => expect(customerCalls()).toHaveLength(2))

    fireEvent.click(addButton())
    fireEvent.change(screen.getByRole('textbox', { name: 'TIN' }), { target: { value: 'RN-5' } })
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Add customer' }))

    await waitFor(() => expect(customerCalls()).toHaveLength(3))
    const q = paramsOf(customerCalls()[2])
    expect([q.get('status'), q.get('offset')]).toEqual(['active', '0'])
    expect(screen.getByRole('button', { name: 'Active' }).style.background).toBe('var(--action)')
  })

  it('CU-29 a saved customer refetches the records page and closes the form', async () => {
    const created = record({ id: 'n', tin: 'RN-5', name: 'New Co' })
    let saved = false
    const { customerCalls, invoiceCalls } = mockApi({
      customers: () => recordsPage(saved ? [record(), created] : [record()]),
      invoices: () => invoicesPage([]),
      createCustomer: () => {
        saved = true
        return { ok: true, status: 201, json: () => Promise.resolve(created) }
      },
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')
    const [recordsBefore, invoicesBefore] = [customerCalls().length, invoiceCalls().length]

    fireEvent.click(addButton())
    fireEvent.change(screen.getByRole('textbox', { name: 'TIN' }), { target: { value: 'RN-5' } })
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Add customer' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(await screen.findByText('New Co')).toBeTruthy()
    expect(customerCalls().length).toBe(recordsBefore + 1)
    expect(invoiceCalls().length, 'the invoice fetch does not rerun').toBe(invoicesBefore)
  })
})

const NOTICE = 'Leaves the Active list and the invoice prefill. Invoices that name it still save.'
const posts = (fetchMock: ReturnType<typeof mockApi>['fetchMock']) => fetchMock.mock.calls.filter((c) => c[1]?.method === 'POST').map((c) => new URL(c[0] as string).pathname)

describe('CustomersView: archive and restore', () => {
  it('CU-34 Archive arms on the first click and posts archive on the second', async () => {
    const { fetchMock } = mockApi({ customers: () => recordsPage([record({ id: 'A' })]), invoices: () => invoicesPage([]), transition: () => ok(record({ id: 'A', status: 'archived' })) })
    render(<CustomersView ctx={ctxFor()} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Archive' }))

    const armed = screen.getByRole('button', { name: 'Confirm archive' })
    expect(armed.style.color).toBe('var(--status-red-text)')
    expect(posts(fetchMock)).toEqual([])
    fireEvent.click(armed)
    await waitFor(() => expect(posts(fetchMock)).toEqual(['/api/invoice/v1/customers/A/archive']))
  })

  it('CU-35 Restore on an archived row arms, then posts restore, never archive', async () => {
    const { fetchMock } = mockApi({ customers: () => recordsPage([record({ id: 'B', status: 'archived' })]), invoices: () => invoicesPage([]), transition: () => ok(record({ id: 'B' })) })
    render(<CustomersView ctx={ctxFor()} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Restore' }))
    expect(posts(fetchMock)).toEqual([])
    fireEvent.click(screen.getByRole('button', { name: 'Confirm restore' }))
    await waitFor(() => expect(posts(fetchMock)).toEqual(['/api/invoice/v1/customers/B/restore']))
  })

  it('CU-36 the action never opens the edit form', async () => {
    mockApi({ customers: () => recordsPage([record({ id: 'A' })]), invoices: () => invoicesPage([]) })
    render(<CustomersView ctx={ctxFor()} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Archive' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive' }))
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('CU-37a on success the records page refetches and the row leaves Active', async () => {
    let archived = false
    const { customerCalls, invoiceCalls } = mockApi({
      customers: () => recordsPage(archived ? [record({ id: 'B', tin: '20000000-0002', name: 'Beta' })] : [record({ id: 'A' }), record({ id: 'B', tin: '20000000-0002', name: 'Beta' })]),
      invoices: () => invoicesPage([]),
      transition: () => {
        archived = true
        return ok(record({ id: 'A', status: 'archived' }))
      },
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')
    const [recordsBefore, invoicesBefore] = [customerCalls().length, invoiceCalls().length]
    fireEvent.click(screen.getAllByRole('button', { name: 'Archive' })[0])
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive' }))

    await waitFor(() => expect(screen.queryByText('Alpha Traders')).toBeNull())
    expect(customerCalls().length).toBe(recordsBefore + 1)
    expect(invoiceCalls().length, 'the invoice fetch does not rerun').toBe(invoicesBefore)
    expect(screen.queryByText('redundant transition')).toBeNull()
  })

  it('CU-37b archiving the last row of a later page steps to the last page', async () => {
    let archived = false
    const { customerCalls } = mockApi({
      customers: (url) => {
        const off = Number(paramsOf(url).get('offset'))
        if (off === 100) return archived ? recordsPage([], 100, 100) : recordsPage([record({ id: 'Z', name: 'Zed' })], 101, 100)
        if (off === 50) return recordsPage([record({ id: 'Y50', tin: '20000000-0050', name: 'Yankee' })], archived ? 100 : 101, 50)
        return recordsPage([record({ id: 'F' })], archived ? 100 : 101, 0)
      },
      invoices: () => invoicesPage([]),
      transition: () => {
        archived = true
        return ok(record({ id: 'Z', status: 'archived' }))
      },
    })
    render(<CustomersView ctx={ctxFor()} />)
    fireEvent.click(await screen.findByRole('button', { name: /Next/ }))
    fireEvent.click(await screen.findByRole('button', { name: /Next/ }))
    await screen.findByText('Zed')
    fireEvent.click(screen.getByRole('button', { name: 'Archive' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive' }))

    await screen.findByText('Yankee')
    const last = customerCalls().at(-1)!
    expect(paramsOf(last).get('offset')).toBe('50')
    expect(screen.getByTestId('customers-pager')).toBeTruthy()
  })

  it('CU-37b boundary: offset 0 and total 0 shows the empty card and no further GET', async () => {
    const { customerCalls } = mockApi({ customers: () => recordsPage([], 0, 0), invoices: () => invoicesPage([]) })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('No customers yet')
    const before = customerCalls().length
    await new Promise((r) => setTimeout(r, 30))
    expect(customerCalls().length).toBe(before)
  })

  it('CU-37c restoring the last row of a later Archived page steps back', async () => {
    let restored = false
    const { customerCalls } = mockApi({
      customers: (url) => {
        const p = paramsOf(url)
        if (p.get('status') === 'archived' && Number(p.get('offset')) === 50) return restored ? recordsPage([], 50, 50) : recordsPage([record({ id: 'Y', name: 'Yank', status: 'archived' })], 51, 50)
        if (p.get('status') === 'archived') return recordsPage([record({ id: 'X', name: 'Xray', status: 'archived' })], restored ? 50 : 51, 0)
        return recordsPage([record()])
      },
      invoices: () => invoicesPage([]),
      transition: () => {
        restored = true
        return ok(record({ id: 'Y' }))
      },
    })
    render(<CustomersView ctx={ctxFor()} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Archived' }))
    fireEvent.click(await screen.findByRole('button', { name: /Next/ }))
    await screen.findByText('Yank')
    fireEvent.click(screen.getByRole('button', { name: 'Restore' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm restore' }))

    await screen.findByText('Xray')
    const last = customerCalls().at(-1)!
    expect(paramsOf(last).get('status')).toBe('archived')
    expect(paramsOf(last).get('offset')).toBe('0')
    expect(screen.queryByTestId('customers-pager')).toBeNull()
  })

  it('CU-37 a 409 shows the server message inline on that row only', async () => {
    mockApi({
      customers: () => recordsPage([record({ id: 'A' }), record({ id: 'B', tin: '20000000-0002', name: 'Beta' })]),
      invoices: () => invoicesPage([]),
      transition: () => ({ ok: false, status: 409, json: () => Promise.resolve({ error: 'redundant transition' }) }),
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')
    fireEvent.click(screen.getAllByRole('button', { name: 'Archive' })[0])
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive' }))

    const msg = await screen.findByText('redundant transition')
    expect(msg.style.color).toBe('var(--status-red-text)')
    expect(screen.getAllByText('redundant transition')).toHaveLength(1)
    expect(within(rows()[0] as HTMLElement).getByText('redundant transition')).toBe(msg)
    expect(screen.getAllByRole('button', { name: 'Archive' })).toHaveLength(2)
  })

  it('CU-37d a failed transition refetches the records page so the row shows its true status', async () => {
    let archived = false
    const { customerCalls } = mockApi({
      customers: () => recordsPage([record({ id: 'A', status: archived ? 'archived' : 'active' })]),
      invoices: () => invoicesPage([]),
      transition: () => {
        archived = true
        return { ok: false, status: 409, json: () => Promise.resolve({ error: 'redundant transition' }) }
      },
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')
    expect(customerCalls()).toHaveLength(1)
    fireEvent.click(screen.getByRole('button', { name: 'Archive' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive' }))

    expect(await screen.findByText('redundant transition')).toBeTruthy()
    await waitFor(() => expect(customerCalls()).toHaveLength(2))
    expect(await screen.findByText('ARCHIVED', { exact: true })).toBeTruthy()
  })

  it('CU-37e a 401 on archive shows nothing inline', async () => {
    const { fetchMock } = mockApi({
      customers: () => recordsPage([record({ id: 'A' })]),
      invoices: () => invoicesPage([]),
      transition: () => ({ ok: false, status: 401, json: () => Promise.resolve({ error: 'unauthorized' }) }),
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')
    fireEvent.click(screen.getByRole('button', { name: 'Archive' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive' }))

    await waitFor(() => expect(posts(fetchMock)).toHaveLength(1))
    await waitFor(() => expect((screen.getByRole('button', { name: 'Archive' }) as HTMLButtonElement).disabled).toBe(false))
    expect(screen.queryByText(/unauthorized|Something went wrong/)).toBeNull()
  })

  it('CU-38 a fast double click on confirm posts once', async () => {
    let release!: () => void
    const { fetchMock } = mockApi({
      customers: () => recordsPage([record({ id: 'A' })]),
      invoices: () => invoicesPage([]),
      transition: () => ok(record({ id: 'A', status: 'archived' })),
    })
    const routed = fetchMock.getMockImplementation()!
    const gate = new Promise<void>((r) => (release = r))
    fetchMock.mockImplementation(((url: string, init?: { method?: string }) => (init?.method === 'POST' ? gate.then(() => routed(url, init)) : routed(url, init))) as typeof routed)
    render(<CustomersView ctx={ctxFor()} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Archive' }))
    const confirm = screen.getByRole('button', { name: 'Confirm archive' })
    fireEvent.click(confirm)
    fireEvent.click(confirm)
    release()
    await waitFor(() => expect(posts(fetchMock)).toHaveLength(1))
  })

  it('CU-39 the armed archive says what archiving does; nothing else does', async () => {
    mockApi({
      customers: () => recordsPage([record({ id: 'A' }), record({ id: 'B', tin: '20000000-0002', name: 'Beta', status: 'archived' })]),
      invoices: () => invoicesPage([]),
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')
    expect(screen.queryByText(NOTICE)).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Archive' }))
    const line = screen.getByText(NOTICE)
    expect(line.style.fontSize).toBe('11.5px')
    expect(line.style.color).toBe('var(--fg-3)')
    expect(within(rows()[0] as HTMLElement).getByText(NOTICE)).toBe(line)

    fireEvent.click(screen.getByRole('button', { name: 'Restore' }))
    expect(screen.getByRole('button', { name: 'Confirm restore' })).toBeTruthy()
    expect(screen.queryByText(NOTICE)).toBeNull()
  })

  it('CU-35b a restored row that stays on All is no longer armed', async () => {
    let restored = false
    mockApi({
      customers: () => recordsPage([record({ id: 'B', status: restored ? 'active' : 'archived' })]),
      invoices: () => invoicesPage([]),
      transition: () => {
        restored = true
        return ok(record({ id: 'B' }))
      },
    })
    render(<CustomersView ctx={ctxFor()} />)
    fireEvent.click(await screen.findByRole('button', { name: 'All' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Restore' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm restore' }))

    expect(await screen.findByRole('button', { name: 'Archive' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Confirm archive' })).toBeNull()
  })

  it('CU-37 a failure that is not an ApiError shows the generic message inline', async () => {
    mockApi({
      customers: () => recordsPage([record({ id: 'A' })]),
      invoices: () => invoicesPage([]),
    })
    const base = ctxFor()
    const real = base.authedFetch
    const ctx = { ...base, authedFetch: ((url: string, opts?: { method?: string }) => (opts?.method === 'POST' ? Promise.reject(new Error('session ended')) : real(url, opts))) as typeof real }
    render(<CustomersView ctx={ctx} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Archive' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive' }))

    expect(await screen.findByText('Something went wrong. Please try again.')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Archive' })).toBeTruthy()
  })

  it('CU-37b a later page emptied to total 0 shows the empty card and never a negative offset', async () => {
    let emptied = false
    const { customerCalls } = mockApi({
      customers: (url) => {
        const off = Number(paramsOf(url).get('offset'))
        if (emptied) return recordsPage([], 0, off)
        return off === 50 ? recordsPage([record({ id: 'Z', name: 'Zed' })], 51, 50) : recordsPage([record({ id: 'F' })], 51, 0)
      },
      invoices: () => invoicesPage([]),
      transition: () => {
        emptied = true
        return ok(record({ id: 'Z', status: 'archived' }))
      },
    })
    render(<CustomersView ctx={ctxFor()} />)
    fireEvent.click(await screen.findByRole('button', { name: /Next/ }))
    await screen.findByText('Zed')
    fireEvent.click(screen.getByRole('button', { name: 'Archive' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm archive' }))

    await screen.findByText('No customers yet')
    await new Promise((r) => setTimeout(r, 30))
    const offsets = customerCalls().map((u) => Number(paramsOf(u).get('offset')))
    expect(offsets.length).toBeGreaterThan(0)
    expect(offsets.every((o) => o >= 0)).toBe(true)
  })
})

describe('CustomersView: the status chips', () => {
  const chip = (name: string) => screen.getByRole('button', { name })

  it('CU-30 Active is the default and the first request carries status=active', async () => {
    const { customerCalls } = mockApi({ customers: () => recordsPage([record()]), invoices: () => invoicesPage([]) })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')

    expect(chip('Active').style.background).toBe('var(--action)')
    expect(chip('All').style.background).not.toBe('var(--action)')
    expect(chip('Archived').style.background).not.toBe('var(--action)')
    expect(paramsOf(customerCalls()[0]).get('status')).toBe('active')
  })

  it('CU-31 each chip requests its own status and resets the page', async () => {
    const { customerCalls } = mockApi({
      customers: (url) => recordsPage([record()], 51, Number(paramsOf(url).get('offset'))),
      invoices: () => invoicesPage([]),
    })
    render(<CustomersView ctx={ctxFor()} />)
    fireEvent.click(await screen.findByRole('button', { name: /Next/ }))
    await waitFor(() => expect(customerCalls()).toHaveLength(2))
    expect(paramsOf(customerCalls()[1]).get('offset')).toBe('50')

    fireEvent.click(chip('Archived'))
    await waitFor(() => expect(customerCalls()).toHaveLength(3))
    expect([paramsOf(customerCalls()[2]).get('status'), paramsOf(customerCalls()[2]).get('offset')]).toEqual(['archived', '0'])

    fireEvent.click(chip('All'))
    await waitFor(() => expect(customerCalls()).toHaveLength(4))
    expect([paramsOf(customerCalls()[3]).has('status'), paramsOf(customerCalls()[3]).get('offset')]).toEqual([false, '0'])
  })

  it('CU-32 an archived row says ARCHIVED, an active row says nothing', async () => {
    mockApi({
      customers: () => recordsPage([record({ id: 'a', name: 'Active Co' }), record({ id: 'b', tin: '20000000-0002', name: 'Old Co', status: 'archived' })]),
      invoices: () => invoicesPage([]),
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Old Co')
    fireEvent.click(chip('All'))
    await waitFor(() => expect(screen.queryByText('Loading customers…')).toBeNull())

    const [a, b] = Array.from(rows()).map((r) => r.children[0] as HTMLElement)
    const pill = within(b).getByText('ARCHIVED', { exact: true })
    expect(pill.style.color).toBe('var(--status-muted-text)')
    expect(pill.style.borderRadius).toBe('var(--radius-sm)')
    expect(pill.parentElement).toBe(b)
    expect(within(a).queryByText('ARCHIVED')).toBeNull()
    expect(screen.queryByText('ACTIVE')).toBeNull()
  })

  const probeCalls = (urls: string[]) => urls.filter((u) => paramsOf(u).get('limit') === '1' && paramsOf(u).get('status') === 'archived')

  it('CU-33a Archived with no rows shows the filter copy; All with none shows No customers yet', async () => {
    const { customerCalls } = mockApi({
      customers: (url) => (paramsOf(url).get('status') === 'active' ? recordsPage([record()]) : recordsPage([])),
      invoices: () => invoicesPage([]),
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')

    fireEvent.click(chip('Archived'))
    expect(await screen.findByText('No customers match this filter')).toBeTruthy()
    expect(screen.getByText('Try a different status filter.')).toBeTruthy()
    expect(screen.queryByText('No customers yet')).toBeNull()

    fireEvent.click(chip('All'))
    expect(await screen.findByText('No customers yet')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'New invoice' })).toBeTruthy()
    expect(probeCalls(customerCalls()), 'no probe on Archived or All').toHaveLength(0)
  })

  it('CU-33b Active empty with archived records says No active customers', async () => {
    const { customerCalls } = mockApi({
      customers: (url) => (paramsOf(url).get('status') === 'archived' ? recordsPage([record({ status: 'archived' })], 1) : recordsPage([])),
      invoices: () => invoicesPage([]),
    })
    render(<CustomersView ctx={ctxFor()} />)

    expect(await screen.findByText('No active customers')).toBeTruthy()
    expect(screen.getByText('Archived customers are under the Archived filter.')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'New invoice' })).toBeTruthy()
    expect(screen.queryByText('No customers yet')).toBeNull()
    const probe = probeCalls(customerCalls())
    expect(probe).toHaveLength(1)
    expect(paramsOf(probe[0]).get('offset')).toBe('0')
  })

  it("CU-33c a failed probe falls back to CUST-02's card", async () => {
    mockApi({
      customers: (url) => (paramsOf(url).get('status') === 'archived' ? down : recordsPage([])),
      invoices: () => invoicesPage([]),
    })
    render(<CustomersView ctx={ctxFor()} />)

    expect(await screen.findByText('No customers yet')).toBeTruthy()
    expect(screen.getByText('Customers are added when you save an invoice with a buyer TIN.')).toBeTruthy()
    expect(screen.queryByText('Something went wrong')).toBeNull()
    expect(screen.queryByText('No active customers')).toBeNull()
  })

  it('CU-33d the probe runs only on an empty Active page (boundary)', async () => {
    const { customerCalls } = mockApi({ customers: () => recordsPage([record()]), invoices: () => invoicesPage([]) })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')
    expect(probeCalls(customerCalls())).toHaveLength(0)
    cleanup()

    vi.stubEnv('VITE_GATEWAY_URL', '')
    const { fetchMock } = mockApi({})
    render(<CustomersView ctx={ctxFor()} />)
    expect(screen.getByText('No customers yet')).toBeTruthy()
    expect(fetchMock).not.toHaveBeenCalled()

    fireEvent.click(chip('Archived'))
    expect(screen.getByText('No customers yet')).toBeTruthy()
    expect(screen.queryByText('No customers match this filter')).toBeNull()
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('CU-33e the screen shows Loading, not the empty card, while the probe is in flight', async () => {
    let release: (r: MockResponse) => void = () => {}
    const held = new Promise<MockResponse>((resolve) => {
      release = resolve
    })
    const { customerCalls } = mockApi({
      customers: (url) => (paramsOf(url).get('status') === 'archived' ? held : recordsPage([])),
      invoices: () => invoicesPage([]),
    })
    render(<CustomersView ctx={ctxFor()} />)

    await waitFor(() => expect(probeCalls(customerCalls())).toHaveLength(1))
    expect(screen.getByText('Loading customers…')).toBeTruthy()
    expect(screen.queryByText('No customers yet')).toBeNull()
    expect(screen.queryByText('No active customers')).toBeNull()

    release(recordsPage([record({ status: 'archived' })], 1))
    expect(await screen.findByText('No active customers')).toBeTruthy()
    expect(screen.queryByText('Loading customers…')).toBeNull()
  })

  it('CU-33f a stale probe result never flashes the wrong empty card when Active empties again', async () => {
    let archivedTotal = 0
    mockApi({
      customers: (url) => {
        const q = paramsOf(url)
        if (q.get('status') === 'archived') return recordsPage(archivedTotal ? [record({ status: 'archived' })] : [], archivedTotal)
        return q.get('status') === 'active' ? recordsPage([]) : recordsPage([record()])
      },
      invoices: () => invoicesPage([]),
    })
    render(<CustomersView ctx={ctxFor()} />)
    expect(await screen.findByText('No customers yet')).toBeTruthy()
    fireEvent.click(chip('All'))
    await screen.findByText('Alpha Traders')

    archivedTotal = 1
    const shown: string[] = []
    const watch = new MutationObserver((ms) => ms.forEach((m) => m.addedNodes.forEach((n) => shown.push(n.textContent ?? ''))))
    watch.observe(document.body, { childList: true, subtree: true })
    fireEvent.click(chip('Active'))
    expect(await screen.findByText('No active customers')).toBeTruthy()
    watch.disconnect()
    expect(shown.filter((t) => t.includes('No customers yet'))).toEqual([])
  })
})

describe('CustomersView: states', () => {
  it('CU-19 records total 0 renders the empty state even with invoices', async () => {
    mockApi({ customers: () => recordsPage([]), invoices: () => invoicesPage([row({ id: 'a' }), row({ id: 'b' }), row({ id: 'c' })]) })
    render(<CustomersView ctx={ctxFor()} />)

    expect(await screen.findByText('No customers yet')).toBeTruthy()
    expect(screen.getByText('Customers are added when you save an invoice with a buyer TIN.')).toBeTruthy()
    expect(rows()).toHaveLength(0)
  })

  it('CU-20 no gateway makes no request and shows the empty state', () => {
    vi.stubEnv('VITE_GATEWAY_URL', '')
    const { fetchMock } = mockApi({})

    render(<CustomersView ctx={ctxFor()} />)

    expect(fetchMock).not.toHaveBeenCalled()
    expect(screen.getByText('No customers yet')).toBeTruthy()
  })

  it('CU-08 loading and error render the shared state, never the register', async () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise(() => {})))
    const loading = render(<CustomersView ctx={ctxFor()} />)
    expect(await screen.findByText('Loading customers…')).toBeTruthy()
    expect(screen.queryByText('Customer')).toBeNull()
    expect(screen.queryByText('No customers yet')).toBeNull()
    expect(screen.getByRole('heading', { level: 1, name: 'Customers' }), 'control: the header still renders').toBeTruthy()
    loading.unmount()

    const { customerCalls, invoiceCalls } = mockApi({ customers: () => recordsPage([record()]), invoices: () => down })
    render(<CustomersView ctx={ctxFor()} />)
    expect(await screen.findByText('Something went wrong')).toBeTruthy()
    expect(screen.queryByText('Customer')).toBeNull()
    expect(screen.queryByText('No customers yet')).toBeNull()
    const [recordsBefore, invoicesBefore] = [customerCalls().length, invoiceCalls().length]

    fireEvent.click(screen.getByRole('button', { name: /retry/i }))

    await waitFor(() => expect(invoiceCalls().length).toBe(invoicesBefore + 1))
    expect(customerCalls().length, 'Retry re-runs only the errored fetch').toBe(recordsBefore)
  })

  it('an errored records fetch shows the error even when the invoices answer', async () => {
    mockApi({ customers: () => down, invoices: () => invoicesPage([]) })
    render(<CustomersView ctx={ctxFor()} />)

    expect(await screen.findByText('Something went wrong')).toBeTruthy()
    expect(screen.queryByText('No customers yet')).toBeNull()
  })

  it('CU-07 the truncation notice states the invoice cap', async () => {
    mockApi({
      customers: () => recordsPage([record()]),
      invoices: (url) => invoicesPage(Number(paramsOf(url).get('offset')) === 0 ? [row()] : [row({ id: 'x', buyer_tin: '30000000-0001' })], 2500, Number(paramsOf(url).get('offset'))),
    })
    render(<CustomersView ctx={ctxFor()} />)
    const notice = await screen.findByTestId('customers-truncated-notice')

    expect(notice.textContent).toBe('Invoices and Total billed count the newest 10 of 2500 invoices.')
    expect(notice.style.padding).toBe('12px 14px')
    expect(notice.style.marginBottom).toBe('16px')
    expect(notice.style.borderRadius).toBe('var(--radius-md)')
    const head = screen.getByText('Tax status').parentElement as HTMLElement
    expect(head.style.gridTemplateColumns).toBe('minmax(160px, 1.6fr) 120px 70px 70px 140px 104px 140px')
    expect(head.style.padding).toBe('11px 18px')
  })

  it('an untruncated set shows no notice', async () => {
    mockApi({ customers: () => recordsPage([record()]), invoices: () => invoicesPage([row()]) })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')

    expect(screen.queryByTestId('customers-truncated-notice')).toBeNull()
  })
})

describe('CustomersView: the v2 register', () => {
  function seed() {
    mockApi({
      customers: () => recordsPage([record({ id: 'a', tin: '30000000-0001', name: 'Alpha Traders' }), record({ id: 'b', tin: 'RN-12345', name: 'Beta Traders' }), record({ id: 'c', tin: '12345678', name: 'Gamma Traders' })]),
      invoices: () => invoicesPage([]),
    })
  }

  it('CU-01 the h1 is Customers and takes the heading rule\'s weight', async () => {
    seed()
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')

    const h1 = screen.getByRole('heading', { level: 1, name: 'Customers' })
    expect(h1.style.fontSize, 'control: the h1 keeps its size').toBe('26px')
    expect(h1.style.fontWeight, 'the h1 must carry no inline font-weight').toBe('')
  })

  it('CU-02 the tax-status chip is a 4px mono chip with no dot', async () => {
    seed()
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')

    const chips = [...screen.getAllByText('VALID', { exact: true }), screen.getByText('NEEDS TIN', { exact: true })]
    expect(chips).toHaveLength(3)
    for (const chip of chips) {
      const what = chip.textContent
      expect(chip.classList.contains('mono'), `${what} is a mono span`).toBe(true)
      expect(chip.style.borderRadius, `${what} corner`).toBe('var(--radius-sm)')
      expect(chip.style.padding, `${what} padding`).toBe('3px 9px')
      expect(chip.style.letterSpacing, `${what} tracking`).toBe('0.04em')
      expect(chip.childElementCount, `${what} holds no dot`).toBe(0)
      const wrapper = chip.parentElement as HTMLElement
      expect(wrapper.tagName, `${what} sits in a plain span`).toBe('SPAN')
      expect(wrapper.getAttribute('style'), `${what} wrapper takes no style`).toBeNull()
    }
  })

  it('CU-03 avatars are circles', async () => {
    seed()
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')

    const avatars = ['AT', 'BT', 'GT'].map((i) => screen.getByText(i, { exact: true }))
    for (const a of avatars) expect(a.style.borderRadius, `${a.textContent} avatar`).toBe('50%')
  })

  it('CU-04 the empty title is 700', async () => {
    mockApi({ customers: () => recordsPage([]), invoices: () => invoicesPage([]) })
    render(<CustomersView ctx={ctxFor()} />)

    const title = await screen.findByText('No customers yet')
    expect(title.style.fontSize, 'control: the title keeps its size').toBe('16px')
    expect(title.style.fontWeight).toBe('700')
  })

  it('CU-06 the pill and TIN colour follow FILEABLE_TIN', async () => {
    seed()
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')

    const valid = screen.getAllByText('VALID', { exact: true })
    expect(valid).toHaveLength(2)
    for (const chip of valid) {
      expect(chip.style.color).toBe('var(--status-green-text)')
      expect(chip.style.background).toBe('var(--status-green-bg)')
      expect(chip.style.border).toBe('1px solid var(--status-green-border)')
      expect(chip.style.display).toBe('inline-flex')
      expect(chip.style.fontSize).toBe('10px')
      expect(chip.style.fontWeight).toBe('600')
    }
    const bad = screen.getByText('NEEDS TIN', { exact: true })
    expect(bad.style.color).toBe('var(--status-red-text)')
    expect(bad.style.background).toBe('var(--status-red-bg)')
    expect(bad.style.border).toBe('1px solid var(--status-red-border)')
    expect(screen.getByText('30000000-0001').style.color, 'a fileable TIN reads muted').toBe('var(--fg-3)')
    expect(screen.getByText('RN-12345').style.color, 'an RN- TIN reads muted').toBe('var(--fg-3)')
    expect(screen.getByText('12345678').style.color, 'a non-fileable TIN reads red').toBe('var(--status-red-text)')
  })

  it('CU-05 the register carries no v1 vocabulary (boundary)', async () => {
    seed()
    const { container } = render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Alpha Traders')

    const html = container.innerHTML
    const radii = Array.from(html.matchAll(/(?<![-\w])border-radius:\s*([^;"]+)/g), (m) => m[1].trim())
    expect(radii.length).toBeGreaterThan(0)
    expect(radii.filter((r) => r === 'var(--radius-sm)').length, 'control: the chips carry --radius-sm').toBeGreaterThanOrEqual(2)
    for (const r of radii) expect(['var(--radius-sm)', 'var(--radius-md)', '50%'], `corner "${r}"`).toContain(r)
    for (const needle of ['999px', '99px', 'oklch', 'gradient', 'box-shadow']) expect(html, needle).not.toContain(needle)
  })
})
