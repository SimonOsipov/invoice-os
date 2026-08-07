// @vitest-environment jsdom
// Component tests for Row; mirrors InvoiceDetail.test.tsx's fetch-mock + ctx-cast idiom.
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { InvoiceDetailRecord, InvoiceRecord } from '../lib/invoices'
import { ROW_EXPANSION_COPY } from '../lib/reviewBatch'
import type { PlatformCtx } from '../types'
import { Row } from './ReviewRow'

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

function row(over: Partial<InvoiceRecord> = {}): InvoiceRecord {
  return {
    id: 'inv-x',
    entity_id: 'ent-1',
    import_batch_id: null,
    invoice_number: 'INV-X',
    status: 'draft',
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
    failure_kind: null,
    rule_set_version: null,
    ...over,
  }
}

function listRow(over: Partial<InvoiceRecord> = {}): InvoiceRecord {
  return row({ id: 'inv-1', invoice_number: 'INV-1', status: 'failed', ...over })
}

function detailFixture(over: Partial<InvoiceDetailRecord> = {}): InvoiceDetailRecord {
  return {
    ...listRow(),
    qr_png_base64: null,
    can_edit: false,
    can_revalidate: true,
    revalidate_blocked_reason: null,
    can_submit: false,
    submit_blocked_reason: null,
    can_view_ubl: true,
    ubl_blocked_reason: null,
    can_resolve_outside: false,
    resolve_outside_blocked_reason: null,
    ...over,
  }
}

function reviewRowCtx(): PlatformCtx {
  const ctx = {
    mode: 'firm',
    active: { entityId: 'ent-1' },
    user: { tenantName: 'Acme Co' },
    authedFetch: createAuthedFetch(() => 'tok', () => {}),
    openCreate: () => {},
    openImportedInvoice: () => {},
    invoiceQuery: '',
  }
  return ctx as unknown as PlatformCtx
}

function rowCtx(): PlatformCtx {
  return { authedFetch: createAuthedFetch(() => 'tok', vi.fn()) } as unknown as PlatformCtx
}

function mockGetInvoice(detail: InvoiceDetailRecord) {
  const fetchMock = vi.fn(() =>
    Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve(detail) }),
  )
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function renderRow(over: Partial<InvoiceRecord> = {}) {
  render(
    <Row
      r={row(over)}
      batches={[]}
      checked={false}
      expanded={false}
      onToggleExpand={() => {}}
      onToggle={() => {}}
      ctx={reviewRowCtx()}
      base="https://gw"
      onChanged={() => {}}
    />,
  )
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

// QA gap-fill (task-413, BUG-05-04): the buyer-tin testid/colour on this surface was
// verified only by lib unit tests and code inspection (mutation-verify: deleting
// data-testid="buyer-tin" from ReviewRow.tsx reddened nothing). These render with
// `expanded=false` so the ExpandedFixPanel's own getInvoice fetch never engages.
describe('ReviewRow buyer TIN signal (task-413, BUG-05-04, AC-4)', () => {
  it('AC-4: null, empty and whitespace-only buyer TIN all read TIN MISSING in red', () => {
    const cases: Array<{ label: string; buyer_tin: string | null }> = [
      { label: 'null', buyer_tin: null },
      { label: 'empty string', buyer_tin: '' },
      { label: 'whitespace-only', buyer_tin: '   ' },
    ]

    for (const { label, buyer_tin } of cases) {
      renderRow({ buyer_tin })

      const tin = screen.getByTestId('buyer-tin')
      expect(tin.textContent, label).toBe('TIN MISSING')
      expect(tin.style.color, label).toBe('var(--status-red-text)')

      cleanup()
    }
  })

  it('AC-4/AC-5: a present buyer TIN, malformed or well-formed, renders the value in grey', () => {
    const cases: Array<{ label: string; buyer_tin: string }> = [
      { label: 'malformed', buyer_tin: 'BADTIN' },
      { label: 'well-formed', buyer_tin: '87654321-0002' },
    ]

    for (const { label, buyer_tin } of cases) {
      renderRow({ buyer_tin })

      const tin = screen.getByTestId('buyer-tin')
      expect(tin.textContent, label).toBe(buyer_tin)
      expect(tin.style.color, label).toBe('var(--fg-3)')

      cleanup()
    }
  })
})

// rowExpansionView (lib/reviewBatch.ts) sets keptReason from kept_as_is_at presence alone
// -- it structurally cannot gate on status (no status in its input) -- so the CONSUMER
// (ReviewRow.tsx) must gate the banner render itself.
describe('ReviewRow row-expansion: the kept banner is a draft-only concept, not resolved-failed', () => {
  it('T6-7: a resolved failed row, expanded, never shows review-kept-banner', async () => {
    mockGetInvoice(detailFixture({
      status: 'failed',
      kept_as_is_at: '2026-08-06T00:00:00Z',
      kept_as_is_by: 'someone',
      kept_as_is_reason: 'Filed manually with the tax authority.',
    }))

    render(
      <Row
        r={listRow({ status: 'failed' })}
        batches={[]}
        checked={false}
        expanded
        onToggleExpand={() => {}}
        onToggle={() => {}}
        ctx={rowCtx()}
        base="https://gw"
        onChanged={() => {}}
      />,
    )

    await screen.findByTestId('review-revalidate') // wait for the record to load before asserting absence
    expect(screen.queryAllByTestId('review-kept-banner')).toHaveLength(0)
  })

  it('T6-8: a kept blocked draft row, expanded, still shows review-kept-banner', async () => {
    mockGetInvoice(detailFixture({
      status: 'draft',
      violations: [{ rule_key: 'vat-standard-rate', severity: 'error', message: 'bad rate' }],
      kept_as_is_at: '2026-07-30T00:00:00Z',
      kept_as_is_by: 'someone',
      kept_as_is_reason: 'Buyer confirmed the discrepancy is intentional.',
    }))

    render(
      <Row
        r={listRow({ status: 'draft' })}
        batches={[]}
        checked={false}
        expanded
        onToggleExpand={() => {}}
        onToggle={() => {}}
        ctx={rowCtx()}
        base="https://gw"
        onChanged={() => {}}
      />,
    )

    const banner = await screen.findByTestId('review-kept-banner')
    expect(banner.textContent).toContain(ROW_EXPANSION_COPY.keptPrefix)
    expect(banner.textContent).toContain('Buyer confirmed the discrepancy is intentional.')
  })
})
