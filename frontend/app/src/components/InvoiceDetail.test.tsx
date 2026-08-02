// @vitest-environment jsdom
// RED specs (task-332, BUG-01-06, Mode A) -- the failed-dead-end card (InvoiceDetail.tsx
// :533-542) says nothing about rejection_reasons being empty yet, so the first test below
// fails on the card's actual textContent, not an import/compile error. First render test
// for this component; mirrors InvoicesList.test.tsx's fetch-mock + ctx-cast idiom.
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { InvoiceDetailRecord, StatusChange } from '../lib/invoices'
import type { PlatformCtx } from '../types'
import { InvoiceDetail } from './InvoiceDetail'

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

function detailRecord(over: Partial<InvoiceDetailRecord> = {}): InvoiceDetailRecord {
  return {
    id: 'inv-failed-1',
    entity_id: 'ent-1',
    import_batch_id: null,
    invoice_number: 'INV-FAILED-1',
    status: 'failed',
    issue_date: '2026-07-01T00:00:00Z',
    supplier_tin: '00000000001',
    supplier_name: 'Acme Ltd',
    buyer_tin: '00000000002',
    buyer_name: 'Beta Ltd',
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
    line_items: [],
    // null (never validated) sidesteps ViolationsTable entirely -- irrelevant to this
    // story's honest-line assertion, which only depends on status/rejection_reasons.
    rule_set_version: null,
    qr_png_base64: null,
    can_edit: false,
    can_revalidate: false,
    revalidate_blocked_reason: null,
    ...over,
  }
}

function detailCtx(importedInvoiceId: string): PlatformCtx {
  const ctx = {
    mode: 'firm',
    active: { entityId: 'ent-1' },
    user: { tenantName: 'Acme Co' },
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    selectedId: null,
    importedInvoiceId,
    nav: () => {},
  }
  return ctx as unknown as PlatformCtx
}

// getInvoice and getInvoiceHistory fire concurrently (two independent useAsync effects) --
// dispatched by URL suffix rather than call order, unlike invoices.test.ts's
// mockFetchOnce/mockFetchSequence which only ever mock one endpoint at a time.
function mockDetailFetch(detail: InvoiceDetailRecord, history: StatusChange[] = []) {
  const fetchMock = vi.fn((url: string) => {
    const body: MockResponse = url.endsWith('/history')
      ? { ok: true, status: 200, json: () => Promise.resolve(history) }
      : { ok: true, status: 200, json: () => Promise.resolve(detail) }
    return Promise.resolve(body)
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('InvoiceDetail failed-dead-end card (task-332, BUG-01-06, [failed-no-reason-lands-on-the-detail])', () => {
  it('AC-3: a failed invoice with no recorded reason renders an explicit "no reason recorded" line', async () => {
    mockDetailFetch(detailRecord({ rejection_reasons: [] }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const card = await screen.findByTestId('failed-dead-end')
    expect(card.textContent?.toLowerCase()).toContain('no reason recorded')
  })

  it('AC-4: a failed invoice WITH reasons still renders the rejection card, and gets no "no reason" line', async () => {
    mockDetailFetch(detailRecord({ rejection_reasons: [{ code: 'invalid_tin', message: 'bad tin' }] }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    const card = await screen.findByTestId('failed-dead-end')
    expect(card.textContent?.toLowerCase()).not.toContain('no reason recorded')
    expect(await screen.findByTestId('rejection-reasons')).toBeDefined()
  })

  // QA Mode B adversarial (task-332, BUG-01-06, point e): the honest line is nested
  // inside the `status === 'failed'` gate (failed-dead-end itself), so a non-failed
  // invoice must never render either the card or the line -- even with rejection_reasons
  // empty, the shape that triggers the line on a FAILED invoice.
  it('a non-failed invoice (rejected, empty rejection_reasons) never renders failed-dead-end or the "no reason" line', async () => {
    mockDetailFetch(detailRecord({ status: 'rejected', rejection_reasons: [] }))

    render(<InvoiceDetail ctx={detailCtx('inv-failed-1')} />)

    await screen.findByText('INV-FAILED-1') // wait for the record to render before asserting absence
    expect(screen.queryByTestId('failed-dead-end')).toBeNull()
    expect(screen.queryByText(/no reason recorded/i)).toBeNull()
  })
})
