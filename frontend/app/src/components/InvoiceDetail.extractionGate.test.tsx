// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// QA gap. `immediate: shouldFetchInvoices(base) && documentId != null` had no oracle: dropping
// the `documentId != null` half fires no extra REQUEST (the producer rejects locally with "no
// document to look up"), so every request-counting row stayed green -- while the card was
// handed `failed: true` for a render and told the operator the check had failed on a perfectly
// healthy invoice. The subject here is the entry PROP across every committed render, which is
// the only place that difference is visible, so the card is stubbed rather than rendered.
//
// Its own file because `vi.mock` is file-scoped: InvoiceDetail.extraction.test.tsx asserts on
// the real card's DOM.
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { ExtractionJob } from '../lib/importApi'
import type { InvoiceDetailRecord } from '../lib/invoices'
import type { SourceDocumentRecord, SourceDocumentResponse } from '../lib/sourceDocument'
import type { PlatformCtx } from '../types'
import { InvoiceDetail } from './InvoiceDetail'

type Entry = { jobId: string | null; loading: boolean; failed: boolean }

const { entries } = vi.hoisted(() => ({ entries: [] as Entry[] }))
vi.mock('./SourceDocumentCard', () => ({
  SourceDocumentCard: (p: { extraction: Entry }) => {
    entries.push({ ...p.extraction })
    return <div data-testid="card-stub" />
  },
}))

const DOCUMENT_ID = 'b2c3d4e5-f6a7-4b2c-8d3e-4f5a6b7c8d9e'
const JOB_ID = 'c3d4e5f6-a7b8-4c3d-9e4f-5a6b7c8d9e0f'
const HASH = '3f9a1c02b7d4e6108a5c93f21e0d47b6c8a2f5039e1b7d4c60a8f3e2d5a86560'

const JOBS: ExtractionJob[] = [
  {
    id: JOB_ID,
    document_id: DOCUMENT_ID,
    state: 'succeeded',
    created_at: '2026-06-12T12:00:00Z',
    last_error: null,
    failure_kind: null,
  },
]

function sourceRecord(): SourceDocumentRecord {
  return {
    id: DOCUMENT_ID,
    filename: 'june-sales.pdf',
    declared_content_type: 'application/pdf',
    size_bytes: 151_552,
    content_hash: HASH,
    uploaded_at: '2026-06-12T11:42:00Z',
    uploaded_by: 'c0000000-0000-0000-0000-000000000001',
    invoices_created: 1,
    other_invoice_rows: [],
  }
}

const WITH_DOCUMENT: SourceDocumentResponse = { invoice_id: 'inv-1', source_rows: [1], document: sourceRecord() }

function detailRecord(): InvoiceDetailRecord {
  return {
    id: 'inv-1', entity_id: 'ent-1', import_batch_id: 'batch-1', invoice_number: 'INV-2026-0037',
    status: 'validated', issue_date: '2026-06-12T00:00:00Z', supplier_tin: '00000000001',
    supplier_name: 'Acme Ltd', buyer_tin: '00000000002', buyer_name: 'Beta Ltd', currency: 'NGN',
    subtotal: '1000.00', vat: '75.00', total: '1075.00', violations: [], rule_set_version_id: null,
    created_at: '2026-06-12T09:15:00Z', irn: null, csid: null, qr_payload: null, rejection_reasons: [],
    kept_as_is_at: null, kept_as_is_by: null, kept_as_is_reason: null, failure_kind: null,
    line_items: [], rule_set_version: null, qr_png_base64: null, can_edit: false, can_revalidate: false,
    revalidate_blocked_reason: null, can_submit: true, submit_blocked_reason: null, can_view_ubl: true,
    ubl_blocked_reason: null, can_resolve_outside: false, resolve_outside_blocked_reason: null,
    can_approve: false, approve_blocked_reason: null, can_reject: false, reject_blocked_reason: null,
  } as InvoiceDetailRecord
}

function mockFetch(mode: 'healthy' | '500') {
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      if (url.includes('/v1/extractions')) {
        return mode === '500'
          ? Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({ error: 'boom' }) })
          : Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ jobs: JOBS }) })
      }
      if (url.endsWith('/source-document')) {
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(WITH_DOCUMENT) })
      }
      if (url.endsWith('/history')) return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve([]) })
      if (url.endsWith('/sheet')) return new Promise(() => {})
      if (url.endsWith('/approval')) {
        return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({ error: 'none' }) })
      }
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(detailRecord()) })
    }),
  )
}

function ctx(): PlatformCtx {
  return {
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    getToken: () => 'tok',
    mode: 'firm',
    user: { name: 'Chinedu Okafor', initials: 'CO', tenantName: 'Okafor & Partners', verified: true },
    active: { name: 'Lagos Logistics Ltd' },
    selectedId: null,
    importedInvoiceId: 'inv-1',
    nav: vi.fn(),
    openExtraction: vi.fn(),
  } as unknown as PlatformCtx
}

beforeEach(() => {
  entries.length = 0
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('QA-3: the lookup is not armed before the document id exists', () => {
  it('invoiceDetail_aHealthyInvoiceNeverReportsAFailedLookup', async () => {
    mockFetch('healthy')
    render(<InvoiceDetail ctx={ctx()} />)

    await screen.findByTestId('card-stub')
    await waitFor(() => expect(entries.some((e) => e.jobId === JOB_ID), 'the lookup never settled').toBe(true))

    // Population floor: more than one committed render, or "no failed entry" is one sample.
    expect(entries.length, 'the card rendered once -- there is no window to inspect').toBeGreaterThan(1)
    // THE claim. With `immediate` armed before documentId exists, the producer rejects at mount
    // with "no document to look up", useAsync commits status 'error', and the card is handed
    // failed: true for the renders between the record arriving and the deps effect re-firing.
    expect(
      entries.filter((e) => e.failed),
      'a healthy invoice reported a failed extraction lookup on some render',
    ).toEqual([])
  })

  it('invoiceDetail_aRealFailureStillReachesTheCard', async () => {
    // Control needle for the absence above, same stub and same assertion: a real 500 DOES put a
    // failed entry in this log, so the empty filter above is a decision and not a dead subject.
    mockFetch('500')
    render(<InvoiceDetail ctx={ctx()} />)

    await screen.findByTestId('card-stub')
    await waitFor(() =>
      expect(entries.some((e) => e.failed), 'control: a 500 never reached the card as failed').toBe(true),
    )
    expect(entries.some((e) => e.jobId !== null), 'a failed lookup must carry no job id').toBe(false)
  })
})
