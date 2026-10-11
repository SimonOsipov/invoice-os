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
    line_items: [],
    rule_set_version: null,
    qr_png_base64: null,
    verdict_stale: false,
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
    can_correct_invoice_number: false,
    invoice_number_blocked_reason: null,
  }
}

const HISTORY: StatusChange[] = [
  { from_status: null, to_status: 'draft', actor: 'c0000000-0000-0000-0000-000000000001', actor_name: 'Chinedu Okafor', actor_kind: 'person', changed_at: '2026-06-12T09:15:00Z', cause: null },
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
  return { invoice_id: 'inv-1', source_rows: [44, 45, 46, 47], header_row: null, document: sourceRecord() }
}

function withoutDocument(): SourceDocumentResponse {
  return { invoice_id: 'inv-1', source_rows: null, header_row: null, document: null }
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
    // The card's extraction entry control (EXTR-11-08) -- its own file owns the assertions;
    // an empty list here leaves this file's subject unchanged. Without this arm the lookup
    // falls through to the invoice-record fallback below, whose missing `jobs` throws.
    if (url.includes('/v1/extractions')) {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ jobs: [] }) })
    }
    // LiveInvoiceDetail fires a fourth request for the approval card. Default 404 so
    // this file's assertions (about SourceDocumentCard) are unaffected.
    if (url.endsWith('/approval')) {
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({ error: 'no approval run for this invoice' }) })
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(detailRecord()) })
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

type DetailCtx = Pick<PlatformCtx, 'authedFetch' | 'getToken' | 'mode' | 'user' | 'importedInvoiceId' | 'nav'> & {
  active: Pick<PlatformCtx['active'], 'name'>
}

function detailCtx(): PlatformCtx {
  const ctx: DetailCtx = {
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    getToken: () => 'tok',
    mode: 'firm',
    user: { name: 'Chinedu Okafor', initials: 'CO', tenantName: 'Okafor & Partners', verified: true },
    active: { name: 'Lagos Logistics Ltd' },
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
  // import-wizard.spec.ts:576 asserts that string has zero matches on this screen.
  it('the card sits in the rail below the state strip, and no card is titled "Audit trail"', async () => {
    mockFetch(withDocument())
    render(<InvoiceDetail ctx={detailCtx()} />)

    const card = await screen.findByTestId('source-document-card')
    const strip = await screen.findByTestId('status-strip')
    expect(strip.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(screen.queryByTestId('status-history')).toBeNull()
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
    mockFetch({ invoice_id: 'inv-1', source_rows: [44], header_row: null, document: sourceRecord({ filename: null }) })
    render(<InvoiceDetail ctx={detailCtx()} />)

    const card = await screen.findByTestId('source-document-card')
    expect(card.textContent).toContain('Filename not recorded')
  })

  it('a long filename wraps with word-break: break-all rather than overflowing', async () => {
    const longName = `${'a'.repeat(120)}.xlsx`
    mockFetch({ invoice_id: 'inv-1', source_rows: [44], header_row: null, document: sourceRecord({ filename: longName }) })
    render(<InvoiceDetail ctx={detailCtx()} />)

    const filenameEl = await screen.findByText(longName)
    expect(filenameEl.style.wordBreak).toBe('break-all')
  })

  // `source_rows: null` with a document present is every invoice imported before this
  // story shipped -- distinct from the manual-invoice `document: null` case above.
  it('a pre-story invoice (document present, rows never recorded) shows the honest fallback', async () => {
    mockFetch({ invoice_id: 'inv-1', source_rows: null, header_row: null, document: sourceRecord() })
    render(<InvoiceDetail ctx={detailCtx()} />)

    const range = await screen.findByTestId('source-document-range')
    expect(range.textContent).toBe('The rows of this file that became this invoice were not recorded.')
  })
})

describe('SourceDocumentCard takes the v2 look (RESKIN2-03-02)', () => {
  it('rail card buttons centre their labels', async () => {
    mockFetch(withDocument())
    const { unmount } = render(<InvoiceDetail ctx={detailCtx()} />)
    const view = await screen.findByTestId('view-source-document')
    const check = screen.getByTestId('open-extraction-review')
    for (const [name, btn] of [['view-source-document', view], ['open-extraction-review', check]] as const) {
      expect.soft(btn.style.justifyContent, `${name} centres its label`).toBe('center')
      expect.soft(btn.style.marginTop, `${name} has no own top margin`).toBe('')
    }
    const column = view.parentElement as HTMLElement
    expect(column, 'the two buttons share one column').toBe(check.parentElement)
    expect.soft(column.style.display).toBe('flex')
    expect.soft(column.style.flexDirection).toBe('column')
    expect.soft(column.style.gap).toBe('8px')
    unmount()

    mockFetch(withoutDocument())
    render(<InvoiceDetail ctx={detailCtx()} />)
    const why = await screen.findByTestId('why-no-source-document')
    expect.soft(why.style.justifyContent, 'why-no-source-document centres its label').toBe('center')
  })

  it('the record card body follows table B', async () => {
    mockFetch(withDocument())
    render(<InvoiceDetail ctx={detailCtx()} />)

    const body = await screen.findByTestId('source-document-card')
    expect.soft(body.style.padding).toBe('15px 18px 16px')
    const row = body.firstElementChild as HTMLElement
    expect.soft((row.firstElementChild as HTMLElement).style.borderRadius, 'tile corner').toBe('var(--radius-md)')
    expect.soft(row.style.marginBottom, 'identity row bottom margin').toBe('13px')
    const name = screen.getByText('june-sales.xlsx')
    expect.soft(name.style.fontSize, 'name size').toBe('13px')
    expect.soft(name.style.fontWeight, 'name weight').toBe('600')
    expect.soft(name.style.color, 'name inherits its colour').toBe('')
    expect.soft(name.style.lineHeight, 'name line height').toBe('1.35')
    const meta = screen.getByTestId('source-document-card-meta')
    expect.soft(meta.style.fontSize, 'meta size').toBe('9.5px')
    expect.soft(meta.style.marginTop, 'meta top margin').toBe('3px')
    expect.soft(meta.style.marginBottom, 'meta has no bottom margin').toBe('')
    const rows = screen.getByTestId('source-document-range')
    expect.soft(rows.style.fontSize, 'rows text size').toBe('12px')
    expect.soft(rows.style.lineHeight, 'rows text line height').toBe('1.5')
    expect.soft(rows.style.marginTop, 'rows text top margin').toBe('0px')
    expect.soft(rows.style.marginBottom, 'rows text bottom margin').toBe('13px')
    const sha = screen.getByText(/^SHA-256 /)
    expect.soft(sha.style.marginTop, 'SHA marginTop').toBe('12px')
    expect.soft(sha.style.paddingTop, 'SHA paddingTop').toBe('11px')
    expect.soft(sha.style.letterSpacing, 'SHA has no tracking').toBe('')
    expect.soft(sha.style.whiteSpace, 'SHA stays on one line').toBe('nowrap')
    expect.soft(sha.style.textOverflow, 'SHA ends in an ellipsis').toBe('ellipsis')
  })

  it('the no-source box follows table B', async () => {
    mockFetch(withoutDocument())
    render(<InvoiceDetail ctx={detailCtx()} />)

    const body = await screen.findByTestId('source-document-card')
    expect.soft(body.style.padding).toBe('15px 18px 16px')
    const box = screen.getByText('No source document').parentElement as HTMLElement
    expect.soft(box.style.padding, 'box padding').toBe('13px 14px')
    expect.soft(box.style.marginBottom, 'box marginBottom').toBe('12px')
    const title = screen.getByText('No source document')
    expect.soft(title.style.fontSize, 'title size').toBe('12.5px')
    expect.soft(title.style.fontWeight, 'title weight').toBe('600')
    expect.soft(title.style.color, 'title inherits its colour').toBe('')
    expect.soft(title.style.marginBottom, 'title marginBottom').toBe('4px')
    const text = screen.getByText(/There is no uploaded file behind it/)
    expect.soft(text.style.fontSize, 'body size').toBe('12px')
    expect.soft(text.style.color, 'body colour').toBe('var(--fg-2)')
    expect.soft(text.style.lineHeight, 'body line height').toBe('1.5')
  })
})

