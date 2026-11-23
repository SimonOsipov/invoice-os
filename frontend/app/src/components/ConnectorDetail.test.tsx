// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import { CONNECTORS_BAD_CONSENT_URL, type ErpConnectionWire } from '../lib/erpConnections'
import type { ErpOverview, FieldView, LookupsView, MappingView, SourceDocWire } from '../lib/erpConnectionDetail'
import { fmtDateTime } from '../lib/format'
import type { PlatformCtx } from '../types'
import { ConnectorDetail } from './ConnectorDetail'

const BASE = 'https://gw.test/api/portfolio/v1/entities/e1'
const OV = BASE + '/erp-connections/c1/overview'
const MAP = BASE + '/erp-mappings/fake'
const LOOK = BASE + '/erp-lookups/fake?external_key=fake-co-3'
const CONSENT = 'https://gw/erp/fake/consent?state=s'
const AT = '2026-03-04T10:00:00Z'

const conn = (o: Partial<ErpConnectionWire> = {}): ErpConnectionWire => ({
  id: 'c1', entity_id: 'e1', system: 'fake', external_key: 'fake-co-3', status: 'active', last_sync_at: AT, created_at: '', updated_at: '', ...o,
})
const doc = (o: Partial<SourceDocWire> = {}): SourceDocWire => ({
  id: 'd1', source_doc_id: 'fake-co-3-inv-1', source_revision: '1', state: 'drafted', failure: null, invoice_id: null, invoice_number: null,
  differs_from_erp: false, changed_after_clearance_at: null, deleted_in_erp_at: null, updated_at: AT, ...o,
})
const ov = (o: Partial<ErpOverview> = {}): ErpOverview => ({
  connection: conn(),
  activity: { since: AT, counts: { pulled: 0, pending: 0, drafted: 0, linked: 0, held: 0, failed: 0, deleted: 0 }, documents: [] },
  writebacks: [],
  held: [],
  ...o,
})
const field = (target: string, o: Partial<FieldView> = {}): FieldView => ({
  target, label: target.toUpperCase(), line: false, source_path: 'a.' + target, default_path: 'a.' + target, origin: 'default', ...o,
})
const mapping = (): MappingView => ({ system: 'fake', fields: [field('invoice_number'), field('issue_date')] })
const lookups = (): LookupsView => ({ system: 'fake', external_key: 'fake-co-3', lookups: [] })

function deferred<T>() {
  let resolve!: (v: T) => void
  const promise = new Promise<T>((a) => {
    resolve = a
  })
  return { promise, resolve }
}

type Init = { method?: string; body?: unknown }
type Opts = {
  role?: string
  overview?: () => unknown
  mapping?: () => unknown
  lookups?: () => unknown
  post?: (body: unknown) => unknown
  put?: (url: string, body: unknown) => unknown
}
function mount(o: Opts = {}) {
  const f = vi.fn(async (url: string, init?: Init) => {
    if (init?.method === 'POST') return o.post!(init.body)
    if (init?.method === 'PUT') return o.put!(url, init.body)
    if (url === OV) return (o.overview ?? (() => ov()))()
    if (url === MAP) return (o.mapping ?? mapping)()
    if (url.startsWith(BASE + '/erp-lookups/')) return (o.lookups ?? lookups)()
    throw new Error('unexpected ' + url)
  })
  const ctx = {
    activeEntity: { id: 'e1', name: 'Acme Ltd' },
    members: [{ id: 'u1', name: 'Ada', initials: 'A', email: null, role: o.role ?? 'admin', status: 'active', isYou: true }],
    membersState: 'ready',
    refetchMembers: vi.fn(),
    openImportedInvoice: vi.fn(),
    authedFetch: f,
  } as unknown as PlatformCtx
  const onBack = vi.fn()
  const r = render(<ConnectorDetail ctx={ctx} entityId="e1" connectionId="c1" onBack={onBack} />)
  return { ...r, f, ctx, onBack }
}
type M = ReturnType<typeof mount>
const ovGets = (m: M) => m.f.mock.calls.filter((c) => c[0] === OV).length
const gets = (m: M, url: string) => m.f.mock.calls.filter((c) => c[0] === url && c[1] === undefined).length
const posts = (m: M) => m.f.mock.calls.filter((c) => c[1]?.method === 'POST')

const mkTab = () => ({ opener: {} as unknown, location: { href: '' }, close: vi.fn() })
const spyOpen = () => {
  const tab = mkTab()
  return { tab, open: vi.spyOn(window, 'open').mockReturnValue(tab as unknown as Window) }
}
const flush = () => act(async () => { await vi.advanceTimersByTimeAsync(0) })
const advance = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })
const click = (el: HTMLElement) => act(async () => { fireEvent.click(el) })
const reconnectBtn = () => screen.getByTestId('erp-detail-reconnect') as HTMLButtonElement
const reconsent = () => ov({ connection: conn({ status: 'reconsent' }) })
const pendingAnswer = () => ({ ...conn({ status: 'pending' }), consent_url: CONSENT })

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw.test')
})
afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllEnvs()
  vi.restoreAllMocks()
})

describe('header', () => {
  it('connectorDetail_headerShowsTheConnection', async () => {
    mount()
    const head = await screen.findByTestId('erp-detail-header')
    expect(head.textContent).toContain('Fake ERP')
    expect(head.textContent).toContain('Acme Ltd · ERP company fake-co-3')
    expect(head.textContent).toContain('Synced ' + fmtDateTime(AT))
    expect(screen.getByTestId('erp-detail-status').textContent).toBe('CONNECTED')
  })

  it('connectorDetail_reconnectOnlyForAnAdminOnReconsent', async () => {
    const a = mount({ overview: reconsent })
    await screen.findByTestId('erp-detail-header')
    expect(reconnectBtn()).toBeTruthy()
    a.unmount()
    const b = mount()
    await screen.findByTestId('erp-detail-header')
    expect(screen.queryByTestId('erp-detail-reconnect')).toBeNull()
    b.unmount()
    mount({ overview: reconsent, role: 'preparer' })
    await screen.findByTestId('erp-detail-header')
    expect(screen.queryByTestId('erp-detail-reconnect')).toBeNull()
  })
})

describe('reconnect', () => {
  it('connectorDetail_reconnectOpensConsentAndPolls', async () => {
    vi.useFakeTimers()
    const { tab } = spyOpen()
    let n = 0
    const m = mount({
      overview: () => (++n === 1 ? reconsent() : n <= 3 ? ov({ connection: conn({ status: 'pending' }) }) : ov()),
      post: () => pendingAnswer(),
    })
    await flush()
    await click(reconnectBtn())
    await flush()
    expect(posts(m)).toHaveLength(1)
    expect(posts(m)[0][1]).toEqual({ method: 'POST', body: { system: 'fake', connection_id: 'c1' } })
    expect(tab.location.href).toBe(CONSENT)
    expect(tab.opener).toBeNull()
    expect(ovGets(m)).toBe(1)
    await advance(3000)
    expect(ovGets(m)).toBe(2)
    await advance(3000)
    await advance(3000)
    expect(ovGets(m)).toBe(4)
    expect(screen.getByTestId('erp-detail-status').textContent).toBe('CONNECTED')
    await advance(30000)
    expect(ovGets(m)).toBe(4)
  })

  it('connectorDetail_pollStopsAfter200Ticks', async () => {
    vi.useFakeTimers()
    const m = mount({ overview: () => ov({ connection: conn({ status: 'pending' }) }) })
    await flush()
    await advance(201 * 3000)
    expect(ovGets(m)).toBe(201)
    await advance(30000)
    expect(ovGets(m)).toBe(201)
  })

  it('connectorDetail_reconnectIgnoresASecondClick', async () => {
    const { open } = spyOpen()
    const held = deferred<unknown>()
    const m = mount({ overview: reconsent, post: () => held.promise })
    await screen.findByTestId('erp-detail-header')
    await click(reconnectBtn())
    await click(reconnectBtn())
    expect(posts(m)).toHaveLength(1)
    expect(open).toHaveBeenCalledTimes(1)
    expect(reconnectBtn().disabled).toBe(true)
    await act(async () => held.resolve(pendingAnswer()))
  })

  it('connectorDetail_unmountMidReconnectAppliesNothing', async () => {
    const { tab } = spyOpen()
    const err = vi.spyOn(console, 'error').mockImplementation(() => {})
    const held = deferred<unknown>()
    const m = mount({ overview: reconsent, post: () => held.promise })
    await screen.findByTestId('erp-detail-header')
    await click(reconnectBtn())
    m.unmount()
    await act(async () => held.resolve(pendingAnswer()))
    expect(tab.location.href).toBe(CONSENT)
    expect(ovGets(m)).toBe(1)
    expect(err).not.toHaveBeenCalled()
  })

  it('connectorDetail_unmountMidReconnectFailureRefetchesNothing', async () => {
    const { tab } = spyOpen()
    const held = deferred<unknown>()
    const m = mount({ overview: reconsent, post: () => held.promise })
    await screen.findByTestId('erp-detail-header')
    await click(reconnectBtn())
    m.unmount()
    await act(async () => {
      held.resolve(Promise.reject(new ApiError('http', 'boom', 500)))
      await Promise.resolve()
    })
    expect(tab.close).toHaveBeenCalled()
    expect(ovGets(m)).toBe(1)
  })

  it('connectorDetail_unmountStopsThePoll', async () => {
    vi.useFakeTimers()
    const m = mount({ overview: () => ov({ connection: conn({ status: 'pending' }) }) })
    await flush()
    await advance(3000)
    expect(ovGets(m)).toBe(2)
    m.unmount()
    await advance(30000)
    expect(ovGets(m)).toBe(2)
  })

  it('connectorDetail_reconnectBadConsentUrlClosesTheTab', async () => {
    const { tab } = spyOpen()
    let answer: unknown = { ...conn({ status: 'pending' }) }
    const m = mount({ overview: reconsent, post: () => answer })
    await screen.findByTestId('erp-detail-header')
    await click(reconnectBtn())
    await waitFor(() => expect(tab.close).toHaveBeenCalledTimes(1))
    expect(screen.getByTestId('erp-detail-flash').textContent).toBe(CONNECTORS_BAD_CONSENT_URL)
    await waitFor(() => expect(ovGets(m)).toBe(2))
    expect(tab.location.href).toBe('')
    answer = { ...conn({ status: 'pending' }), consent_url: 'javascript:x' }
    await waitFor(() => expect(reconnectBtn().disabled).toBe(false))
    await click(reconnectBtn())
    await waitFor(() => expect(tab.close).toHaveBeenCalledTimes(2))
    expect(tab.location.href).toBe('')
    await waitFor(() => expect(ovGets(m)).toBe(3))
  })

  it('connectorDetail_reconnectFailureFlashesAndRefetches', async () => {
    const { tab } = spyOpen()
    let status = 500
    const m = mount({ overview: reconsent, post: () => { throw new ApiError('http', 'it broke ' + status, status) } })
    await screen.findByTestId('erp-detail-header')
    await click(reconnectBtn())
    await waitFor(() => expect(ovGets(m)).toBe(2))
    expect(tab.close).toHaveBeenCalledTimes(1)
    expect(screen.getByTestId('erp-detail-flash').textContent).toBe('it broke 500')
    expect(m.ctx.refetchMembers).not.toHaveBeenCalled()

    status = 403
    await waitFor(() => expect(reconnectBtn().disabled).toBe(false))
    await click(reconnectBtn())
    await waitFor(() => expect(tab.close).toHaveBeenCalledTimes(2))
    expect(screen.getByTestId('erp-detail-flash').textContent).toBe('it broke 403')
    expect(m.ctx.refetchMembers).toHaveBeenCalledTimes(1)
    expect(ovGets(m)).toBe(2)

    status = 401
    await waitFor(() => expect(reconnectBtn().disabled).toBe(false))
    await click(reconnectBtn())
    await waitFor(() => expect(tab.close).toHaveBeenCalledTimes(3))
    expect(m.ctx.refetchMembers).toHaveBeenCalledTimes(1)
    expect(ovGets(m)).toBe(2)
  })
})

describe('sync activity', () => {
  it('connectorDetail_activityListsTheDocuments', async () => {
    mount({
      overview: () =>
        ov({
          activity: {
            since: AT,
            counts: { pulled: 3, pending: 0, drafted: 1, linked: 0, held: 0, failed: 1, deleted: 1 },
            documents: [
              doc({ id: 'a', source_doc_id: 'erp-1', state: 'drafted', invoice_id: 'i1', invoice_number: 'INV-0001' }),
              doc({ id: 'b', source_doc_id: 'erp-2', state: 'failed', failure: 'refused' }),
              doc({ id: 'c', source_doc_id: 'erp-3', state: 'deleted' }),
            ],
          },
        }),
    })
    const card = await screen.findByTestId('erp-detail-activity')
    expect(card.textContent).toContain('3 pulled · 1 new drafts · 0 linked · 0 held · 1 failed · 1 deleted in the ERP · 0 in progress')
    const rows = within(card).getAllByTestId('erp-activity-row')
    expect(rows).toHaveLength(3)
    expect(rows[0].textContent).toContain('erp-1')
    expect(rows[0].textContent).toContain('NEW DRAFT')
    expect(rows[0].textContent).toContain('INV-0001')
    expect(rows[0].textContent).toContain(fmtDateTime(AT))
    expect(rows[1].textContent).toContain('FAILED')
    expect(rows[2].textContent).toContain('DELETED IN ERP')
  })

  it('connectorDetail_activityEmpty', async () => {
    mount()
    const card = await screen.findByTestId('erp-detail-activity')
    expect(card.textContent).toContain('No documents pulled yet.')
    expect(within(card).queryAllByTestId('erp-activity-row')).toHaveLength(0)
  })
})

describe('field mapping', () => {
  it('connectorDetail_mappingShowsPathsAndCustom', async () => {
    mount({ mapping: () => ({ system: 'fake', fields: [field('invoice_number', { source_path: 'mine.no', origin: 'client' }), field('issue_date', { source_path: null, default_path: null, origin: 'none' })] }) })
    await screen.findByText('mine.no')
    const rows = screen.getAllByTestId('erp-mapping-row')
    expect(rows[0].textContent).toContain('mine.no')
    expect(rows[0].textContent).toContain('CUSTOM')
    expect(rows[1].textContent).toContain('Not mapped')
    expect(rows[1].textContent).not.toContain('CUSTOM')
  })

  it('connectorDetail_editIsAdminOnly', async () => {
    const a = mount({ role: 'preparer' })
    await screen.findByText('a.invoice_number')
    expect(screen.queryByTestId('erp-detail-mapping-edit')).toBeNull()
    expect(screen.getByTestId('erp-detail-mapping-admin-only').textContent).toBe('Only an admin can edit the mapping.')
    a.unmount()
    mount()
    await screen.findByText('a.invoice_number')
    await waitFor(() => expect(screen.getByTestId('erp-detail-mapping-edit')).toBeTruthy())
    expect(screen.queryByTestId('erp-detail-mapping-admin-only')).toBeNull()
    await click(screen.getByTestId('erp-detail-mapping-edit'))
    expect(screen.getByTestId('field-mapping-modal')).toBeTruthy()
  })

  it('connectorDetail_noCompanyNoLookupsRequest', async () => {
    const m = mount({ overview: () => ov({ connection: conn({ external_key: null, status: 'pending' }) }) })
    await screen.findByText('Lookups open once the connection is signed in.')
    expect(m.f.mock.calls.filter((c) => c[0].includes('erp-lookups'))).toHaveLength(0)
    expect(screen.getByTestId('erp-detail-header').textContent).toContain('ERP company not chosen yet')
  })

  it('connectorDetail_savedMappingUpdatesThePanel', async () => {
    const m = mount({
      put: () => ({ system: 'fake', fields: [field('invoice_number', { source_path: 'new.no', origin: 'client' }), field('issue_date')] }),
    })
    await screen.findByText('a.invoice_number')
    await waitFor(() => expect(screen.getByTestId('erp-detail-mapping-edit')).toBeTruthy())
    await click(screen.getByTestId('erp-detail-mapping-edit'))
    fireEvent.change(screen.getByLabelText('INVOICE_NUMBER path'), { target: { value: 'new.no' } })
    await click(screen.getByTestId('field-mapping-save'))
    await waitFor(() => expect(screen.queryByTestId('field-mapping-modal')).toBeNull())
    const row = screen.getAllByTestId('erp-mapping-row')[0]
    expect(row.textContent).toContain('new.no')
    expect(row.textContent).toContain('CUSTOM')
    expect(gets(m, MAP)).toBe(1)
    expect(gets(m, LOOK)).toBe(1)
  })

  it('connectorDetail_mappingShowsLookups', async () => {
    const lk = (o: object) => ({ kind: 'item', erp_key: 'k', tax_category: null, exemption_code: null, hsn_code: null, service_code: null, tin: null, state: null, lga: null, ...o })
    mount({ lookups: () => ({ system: 'fake', external_key: 'fake-co-3', lookups: [lk({ erp_key: 'sku-1', hsn_code: '1234' }), lk({ kind: 'customer', erp_key: 'acme', tin: 'T-9' })] }) })
    const rows = await screen.findAllByTestId('erp-lookup-row')
    expect(rows).toHaveLength(2)
    expect(rows[0].textContent).toContain('Item')
    expect(rows[0].textContent).toContain('sku-1')
    expect(rows[0].textContent).toContain('HSN code 1234')
    expect(rows[1].textContent).toContain('Customer')
    expect(rows[1].textContent).toContain('TIN T-9')
    expect(screen.queryByText('No lookups yet.')).toBeNull()
  })

  it('connectorDetail_savedLookupsUpdateThePanel', async () => {
    const lk = (o: object) => ({ kind: 'customer', erp_key: 'acme', tax_category: null, exemption_code: null, hsn_code: null, service_code: null, tin: null, state: null, lga: null, ...o })
    const m = mount({
      lookups: () => ({ system: 'fake', external_key: 'fake-co-3', lookups: [lk({ tin: 'OLD' })] }),
      put: () => ({ system: 'fake', external_key: 'fake-co-3', lookups: [lk({ tin: 'NEWTIN' })] }),
    })
    await screen.findByText(/TIN OLD/)
    await waitFor(() => expect(screen.getByTestId('erp-detail-mapping-edit')).toBeTruthy())
    await click(screen.getByTestId('erp-detail-mapping-edit'))
    fireEvent.change(screen.getByLabelText('TIN'), { target: { value: 'NEWTIN' } })
    await click(screen.getByTestId('field-mapping-save'))
    await waitFor(() => expect(screen.queryByTestId('field-mapping-modal')).toBeNull())
    expect(screen.getByTestId('erp-lookup-row').textContent).toContain('TIN NEWTIN')
    expect(gets(m, LOOK)).toBe(1)
  })

  it('connectorDetail_mappingFailureStaysInItsCard', async () => {
    mount({ mapping: () => { throw new ApiError('http', 'mapping broke', 500) } })
    const card = await screen.findByTestId('erp-detail-mapping')
    await within(card).findByText('mapping broke')
    expect(screen.getByTestId('erp-detail-activity')).toBeTruthy()
    expect(screen.getByTestId('erp-detail-header')).toBeTruthy()
  })
})

describe('write-back and held', () => {
  it('connectorDetail_writebacksShowOutcomeAndReason', async () => {
    mount({
      overview: () =>
        ov({
          writebacks: [
            { id: 'w1', invoice_id: 'i1', invoice_number: 'INV-1', outcome: 'accepted', state: 'succeeded', reason: null, attempt: 1, created_at: AT },
            { id: 'w2', invoice_id: 'i2', invoice_number: 'INV-2', outcome: 'failed', state: 'failed', reason: 'vendor', attempt: 2, created_at: AT },
          ],
        }),
    })
    const card = await screen.findByTestId('erp-detail-writebacks')
    const rows = within(card).getAllByTestId('erp-writeback-row')
    expect(rows[0].textContent).toContain('INV-1')
    expect(rows[0].textContent).toContain('ACCEPTED · Written back')
    expect(rows[1].textContent).toContain('FAILED · Not written: the ERP answered with an error')
  })

  it('connectorDetail_heldRowsAndOpenInvoice', async () => {
    const m = mount({
      overview: () =>
        ov({
          held: [
            { ...doc({ id: 'h1', source_doc_id: 'erp-9', state: 'linked', differs_from_erp: true, invoice_id: 'inv-9', invoice_number: 'INV-9' }), reasons: ['differs_from_erp'] },
            { ...doc({ id: 'h2', source_doc_id: 'erp-10', state: 'deleted', deleted_in_erp_at: AT }), reasons: ['deleted_in_erp'] },
          ],
        }),
    })
    const card = await screen.findByTestId('erp-detail-held')
    const rows = within(card).getAllByTestId('erp-held-row')
    expect(rows.map((r) => r.dataset.sourceDocId)).toEqual(['erp-9', 'erp-10'])
    expect(rows[0].textContent).toContain('Differs from the ERP')
    expect(rows[1].textContent).toContain('Deleted or voided in the ERP')
    expect(within(rows[0]).getByTestId('erp-held-open')).toBeTruthy()
    expect(within(rows[1]).queryByTestId('erp-held-open')).toBeNull()
    await click(within(rows[0]).getByTestId('erp-held-open'))
    expect(m.ctx.openImportedInvoice).toHaveBeenCalledWith('inv-9')
  })
})

describe('states', () => {
  it('connectorDetail_notFoundAndError', async () => {
    const a = mount({ overview: () => { throw new ApiError('http', 'not found', 404) } })
    const nf = await screen.findByTestId('erp-detail-not-found')
    expect(nf.textContent).toBe('This connection is not in Acme Ltd.')
    await click(screen.getByTestId('erp-detail-back'))
    expect(a.onBack).toHaveBeenCalledTimes(1)
    a.unmount()

    let fail = true
    const b = mount({ overview: () => { if (fail) throw new ApiError('http', 'server broke', 500); return ov() } })
    await screen.findByText('server broke')
    expect(screen.queryByTestId('erp-detail-not-found')).toBeNull()
    fail = false
    await click(screen.getByText('Retry'))
    await screen.findByTestId('erp-detail-header')
    expect(ovGets(b)).toBe(2)
  })

  it('connectorDetail_noMockPanels', async () => {
    mount()
    await screen.findByText('Sync activity')
    for (const t of ['Reconciliation', 'Documents pulled', 'Master data mirror', 'Token expires', 'Sync now']) expect(screen.queryByText(new RegExp(t))).toBeNull()
    expect(screen.queryByTestId('connector-env-pill')).toBeNull()
  })
})
