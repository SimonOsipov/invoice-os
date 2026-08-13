// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// Renders the whole InvoiceDetail rather than the card alone, so the right-rail insertion
// point and the card -> modal wiring are what is under test, not a hand-assembled pair.

import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { InvoiceDetailRecord, StatusChange } from '../lib/invoices'
import type { SourceDocumentRecord, SourceDocumentResponse } from '../lib/sourceDocument'
import type { PlatformCtx } from '../types'
import { InvoiceDetail } from './InvoiceDetail'

const HASH = '3f9a1c02b7d4e6108a5c93f21e0d47b6c8a2f5039e1b7d4c60a8f3e2d5a86560'

function detailRecord(): InvoiceDetailRecord {
  return {
    id: 'inv-1',
    entity_id: 'ent-1',
    import_batch_id: 'batch-1',
    invoice_number: 'INV-2026-0037',
    status: 'validated',
    issue_date: '2026-06-12T00:00:00Z',
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
    created_at: '2026-06-12T09:15:00Z',
    irn: null,
    csid: null,
    qr_payload: null,
    rejection_reasons: [],
    kept_as_is_at: null,
    kept_as_is_by: null,
    kept_as_is_reason: null,
    failure_kind: null,
    approval: null,
    line_items: [],
    rule_set_version: null,
    qr_png_base64: null,
    can_edit: false,
    can_revalidate: false,
    revalidate_blocked_reason: null,
    can_submit: true,
    submit_blocked_reason: null,
    can_view_ubl: true,
    ubl_blocked_reason: null,
    can_resolve_outside: false,
    resolve_outside_blocked_reason: null,
    can_approve: false,
    approve_blocked_reason: null,
    can_reject: false,
    reject_blocked_reason: null,
  }
}

const HISTORY: StatusChange[] = [
  { from_status: null, to_status: 'draft', actor: 'c0000000-0000-0000-0000-000000000001', changed_at: '2026-06-12T09:15:00Z' },
]

function sourceRecord(over: Partial<SourceDocumentRecord> = {}): SourceDocumentRecord {
  return {
    id: 'doc-1',
    filename: 'june-sales.xlsx',
    declared_content_type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
    size_bytes: 624640, // 610 KB, 1024-base
    content_hash: HASH,
    uploaded_at: '2026-06-12T11:42:00Z',
    uploaded_by: 'c0000000-0000-0000-0000-000000000001',
    invoices_created: 500,
    other_invoice_rows: [],
    ...over,
  }
}

function withDocument(): SourceDocumentResponse {
  return { invoice_id: 'inv-1', source_rows: [44, 45, 46, 47], document: sourceRecord() }
}

function withoutDocument(): SourceDocumentResponse {
  return { invoice_id: 'inv-1', source_rows: null, document: null }
}

// Dispatched by URL suffix, never by call order: the detail fires three concurrent
// requests. `null` for the source-document body means "answer it with a 500".
function mockFetch(source: SourceDocumentResponse | null) {
  const fetchMock = vi.fn((url: string) => {
    if (url.endsWith('/history')) {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(HISTORY) })
    }
    if (url.endsWith('/source-document')) {
      return source === null
        ? Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({ error: 'boom' }) })
        : Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(source) })
    }
    if (url.endsWith('/sheet')) {
      return new Promise(() => {}) // the sheet canvas is DOC-02-06's; hold it open here
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(detailRecord()) })
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

type DetailCtx = Pick<PlatformCtx, 'authedFetch' | 'getToken' | 'mode' | 'user' | 'selectedId' | 'importedInvoiceId' | 'nav'> & {
  active: Pick<PlatformCtx['active'], 'name'>
}

function detailCtx(): PlatformCtx {
  const ctx: DetailCtx = {
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    getToken: () => 'tok',
    mode: 'firm',
    user: { name: 'Chinedu Okafor', initials: 'CO', tenantName: 'Okafor & Partners', verified: true },
    active: { name: 'Lagos Logistics Ltd' },
    selectedId: null,
    importedInvoiceId: 'inv-1',
    nav: vi.fn(),
  }
  return ctx as unknown as PlatformCtx
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('SourceDocumentCard on the invoice detail', () => {
  // The design says "directly above Audit trail"; no card by that name exists here, and
  // import-wizard.spec.ts:538 asserts that string has zero matches on this screen.
  it('the card precedes status-history in DOM order', async () => {
    mockFetch(withDocument())
    render(<InvoiceDetail ctx={detailCtx()} />)

    const card = await screen.findByTestId('source-document-card')
    const history = screen.getByTestId('status-history')
    expect(card.compareDocumentPosition(history) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(screen.queryByText('Audit trail')).toBeNull()
  })

  it('the card states the row range before the modal opens', async () => {
    mockFetch(withDocument())
    render(<InvoiceDetail ctx={detailCtx()} />)

    const range = await screen.findByTestId('source-document-range')
    expect(range.textContent).toMatch(/^Rows 44–47 of this file became this invoice\.$/)
    expect(screen.queryByTestId('source-document-modal')).toBeNull()
  })

  it('the no-file card reads "Why there is no file"', async () => {
    mockFetch(withoutDocument())
    render(<InvoiceDetail ctx={detailCtx()} />)

    const button = await screen.findByTestId('why-no-source-document')
    expect(button.textContent?.trim()).toBe('Why there is no file')

    const card = screen.getByTestId('source-document-card')
    expect(card.textContent).toContain('No source document')
    expect(card.textContent).toContain('This invoice was typed into ASComply. There is no uploaded file behind it.')

    await userEvent.click(button)
    expect(screen.getByTestId('source-document-modal')).toBeDefined()
    expect(screen.getByTestId('source-document-no-source').textContent).toContain('There is no source document')
  })

  // The card never fetches the sheet, and neither count is stored anywhere -- so it cannot
  // know them. Divergence from design §5.
  it('the card carries no row or column count', async () => {
    mockFetch(withDocument())
    render(<InvoiceDetail ctx={detailCtx()} />)

    const meta = await screen.findByTestId('source-document-card-meta')
    expect(meta.textContent).toMatch(/^XLSX · /)

    const card = screen.getByTestId('source-document-card')
    expect((card.textContent ?? '').length).toBeGreaterThan(0) // vacuity floor
    expect(card.textContent).not.toContain('ROWS')
    expect(card.textContent).not.toContain('COLUMNS')
  })

  it('a record fetch failure degrades to an error state, not a fabricated card', async () => {
    mockFetch(null)
    render(<InvoiceDetail ctx={detailCtx()} />)

    const card = await screen.findByTestId('source-document-card')
    await screen.findByRole('button', { name: 'Retry' })

    expect(card.textContent).not.toContain('june-sales.xlsx')
    expect(card.textContent).not.toContain('SHA-256')
    expect(screen.queryByTestId('view-source-document')).toBeNull()
  })

  it('a null filename falls back to "Filename not recorded"', async () => {
    mockFetch({ invoice_id: 'inv-1', source_rows: [44], document: sourceRecord({ filename: null }) })
    render(<InvoiceDetail ctx={detailCtx()} />)

    const card = await screen.findByTestId('source-document-card')
    expect(card.textContent).toContain('Filename not recorded')
  })

  it('a long filename wraps with word-break: break-all rather than overflowing', async () => {
    const longName = `${'a'.repeat(120)}.xlsx`
    mockFetch({ invoice_id: 'inv-1', source_rows: [44], document: sourceRecord({ filename: longName }) })
    render(<InvoiceDetail ctx={detailCtx()} />)

    const filenameEl = await screen.findByText(longName)
    expect(filenameEl.style.wordBreak).toBe('break-all')
  })

  // `source_rows: null` with a document present is every invoice imported before this
  // story shipped -- distinct from the manual-invoice `document: null` case above.
  it('a pre-story invoice (document present, rows never recorded) shows the honest fallback', async () => {
    mockFetch({ invoice_id: 'inv-1', source_rows: null, document: sourceRecord() })
    render(<InvoiceDetail ctx={detailCtx()} />)

    const range = await screen.findByTestId('source-document-range')
    expect(range.textContent).toBe('The rows of this file that became this invoice were not recorded.')
  })
})
