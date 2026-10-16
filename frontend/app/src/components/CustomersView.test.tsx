// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
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

// Routes by path, never by call order: the screen fires both reads at mount.
function mockApi(handlers: { customers?: Handler; invoices?: Handler }) {
  const fetchMock = vi.fn((url: string) => {
    const path = new URL(url).pathname
    const h = path.endsWith('/v1/customers') ? handlers.customers : path.endsWith('/v1/invoices') ? handlers.invoices : undefined
    if (!h) throw new Error(`unrouted ${url}`)
    return Promise.resolve(h(url))
  })
  vi.stubGlobal('fetch', fetchMock)
  const calls = (suffix: string) => fetchMock.mock.calls.map((c) => c[0] as string).filter((u) => new URL(u).pathname.endsWith(suffix))
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
  it('CU-09 one row per record, keyed by id, six columns', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    mockApi({
      customers: () => recordsPage([record({ id: 'a', name: 'Alpha' }), record({ id: 'b', tin: '20000000-0002', name: null }), record({ id: 'c', tin: '20000000-0003', name: 'Alpha' })]),
      invoices: () => invoicesPage([]),
    })
    render(<CustomersView ctx={ctxFor()} />)
    await screen.findByText('Customer')

    const head = document.querySelector('.pf-list-head') as HTMLElement
    expect(cells(head)).toEqual(['Customer', 'TIN', 'Type', 'Invoices', 'Total billed', 'Tax status'])
    expect(rows()).toHaveLength(3)
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
    expect(head.style.gridTemplateColumns).toBe('minmax(160px, 1.6fr) 140px 70px 70px 140px 104px')
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
