// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// EXTR-11-08, Mode A. The extractions lookup: it lives on InvoiceDetail, never on the card
// (SourceDocumentCard.tsx:1-5 -- "the card never fetches the sheet"), it runs only when the
// source-document record names a document, and it picks the job with `newestJob()`
// (lib/documentRun.ts:24) rather than trusting the server's array order.
//
// Harness is SourceDocumentCard.test.tsx's: the whole InvoiceDetail with a URL-dispatched
// fetch stub, so the card -> detail wiring is the subject rather than a hand-built pair.

import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { ExtractionJob } from '../lib/importApi'
import type { InvoiceDetailRecord, StatusChange } from '../lib/invoices'
import type { SourceDocumentRecord, SourceDocumentResponse } from '../lib/sourceDocument'
import type { PlatformCtx } from '../types'
import { InvoiceDetail } from './InvoiceDetail'

const TESTID = 'open-extraction-review'
// Story `## Decisions -> Invented copy`, final. See SourceDocumentCard.extraction.test.tsx.
const NO_JOB_REASON = 'This document has no extraction to check.'
const LOOKUP_FAILED_REASON = 'We could not check this document for an extraction.'

const DOCUMENT_ID = 'b2c3d4e5-f6a7-4b2c-8d3e-4f5a6b7c8d9e'
const NEWER_JOB = 'c3d4e5f6-a7b8-4c3d-9e4f-5a6b7c8d9e0f'
const OLDER_JOB = 'a1b2c3d4-e5f6-4a1b-8c2d-3e4f5a6b7c8d'
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
  { from_status: null, to_status: 'draft', actor: 'c0000000-0000-0000-0000-000000000001', actor_name: 'Chinedu Okafor', actor_kind: 'person', changed_at: '2026-06-12T09:15:00Z' },
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
const WITHOUT_DOCUMENT: SourceDocumentResponse = { invoice_id: 'inv-1', source_rows: null, document: null }

function job(id: string, createdAt: string): ExtractionJob {
  return { id, document_id: DOCUMENT_ID, state: 'succeeded', created_at: createdAt, last_error: null }
}

const NEWEST_FIRST = [job(NEWER_JOB, '2026-06-12T12:00:00Z'), job(OLDER_JOB, '2026-06-12T11:00:00Z')]
const OLDEST_FIRST = [job(OLDER_JOB, '2026-06-12T11:00:00Z'), job(NEWER_JOB, '2026-06-12T12:00:00Z')]

type Wire = { calls: string[]; extractionCalls: string[]; sourceCalls: string[] }

// Dispatched by URL, never by call order: the detail fires four concurrent requests.
// `jobs: 'hang'` leaves the extractions read in flight forever, the idiom
// SourceDocumentCard.test.tsx already uses for `/sheet`; `'500'` and `'null-body'` are the two
// shapes that must NOT read as "this document has no extraction".
function mockFetch(source: SourceDocumentResponse, jobs: readonly ExtractionJob[] | 'hang' | '500' | 'null-body' | 'null-jobs' | 'no-jobs-key'): Wire {
  const wire: Wire = { calls: [], extractionCalls: [], sourceCalls: [] }
  const fetchMock = vi.fn((url: string) => {
    wire.calls.push(url)
    if (url.includes('/v1/extractions')) {
      wire.extractionCalls.push(url)
      if (jobs === 'hang') return new Promise(() => {})
      if (jobs === '500') {
        return Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({ error: 'extractions read failed' }) })
      }
      if (jobs === 'null-body') {
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(null) })
      }
      if (jobs === 'null-jobs') {
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ jobs: null }) })
      }
      if (jobs === 'no-jobs-key') {
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) })
      }
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ jobs }) })
    }
    if (url.endsWith('/source-document')) {
      wire.sourceCalls.push(url)
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(source) })
    }
    if (url.endsWith('/history')) {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(HISTORY) })
    }
    if (url.endsWith('/sheet')) return new Promise(() => {})
    if (url.endsWith('/approval')) {
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({ error: 'no approval run for this invoice' }) })
    }
    return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(detailRecord()) })
  })
  vi.stubGlobal('fetch', fetchMock)
  return wire
}

type DetailCtx = Pick<PlatformCtx, 'authedFetch' | 'getToken' | 'mode' | 'user' | 'selectedId' | 'importedInvoiceId' | 'nav'> & {
  active: Pick<PlatformCtx['active'], 'name'>
  openExtraction: (jobId: string) => void
}

function detailCtx(openExtraction: (jobId: string) => void): PlatformCtx {
  const ctx: DetailCtx = {
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    getToken: () => 'tok',
    mode: 'firm',
    user: { name: 'Chinedu Okafor', initials: 'CO', tenantName: 'Okafor & Partners', verified: true },
    active: { name: 'Lagos Logistics Ltd' },
    selectedId: null,
    importedInvoiceId: 'inv-1',
    nav: vi.fn(),
    openExtraction,
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

describe('AC-9: the lookup runs only when a document record exists', () => {
  it('invoiceDetail_aDocumentRecordFiresExactlyOneExtractionsLookup', async () => {
    const wire = mockFetch(WITH_DOCUMENT, NEWEST_FIRST)
    render(<InvoiceDetail ctx={detailCtx(vi.fn())} />)

    await screen.findByTestId(TESTID)
    await waitFor(() =>
      expect(wire.extractionCalls.length, 'the detail never read GET /v1/extractions').toBeGreaterThan(0),
    )

    expect(wire.extractionCalls, 'the record names one document, so the lookup runs once').toHaveLength(1)
    const url = new URL(wire.extractionCalls[0]!)
    expect(url.pathname, 'the lookup must use the shipped list route').toBe('/api/submission/v1/extractions')
    expect(url.searchParams.get('document_id'), 'the lookup must be scoped to this document').toBe(DOCUMENT_ID)
  })

  it('invoiceDetail_noDocumentRecordFiresNoExtractionsLookup', async () => {
    // A manually typed invoice. `document: null` is the same 200 the endpoint returns for it.
    const typed = mockFetch(WITHOUT_DOCUMENT, NEWEST_FIRST)
    render(<InvoiceDetail ctx={detailCtx(vi.fn())} />)

    // Floor: the record fetch settled and the no-record arm rendered, so "no extractions
    // call" is a decision this component made and not a page that never loaded.
    await screen.findByTestId('why-no-source-document')
    expect(typed.sourceCalls.length, 'the source-document read never happened').toBeGreaterThan(0)
    expect(typed.extractionCalls, 'a manually typed invoice must fire no extra request').toEqual([])

    // Control needle in the same spec, through the same stub: with a record the very same
    // component DOES read the list, so the emptiness above is the `document: null` arm.
    cleanup()
    const imported = mockFetch(WITH_DOCUMENT, NEWEST_FIRST)
    render(<InvoiceDetail ctx={detailCtx(vi.fn())} />)
    await waitFor(() =>
      expect(imported.extractionCalls.length, 'control: the lookup never runs at all').toBeGreaterThan(0),
    )
  })
})

describe('AC-5: the control opens the newest job', () => {
  it('invoiceDetail_theControlOpensTheNewestJobWhateverTheArrayOrder', async () => {
    // Both orders, because `jobs[0]` is right by coincidence in one of them --
    // lib/documentRun.ts:22-23 records that the server's order is not a contract.
    for (const [label, jobs] of [
      ['newest first', NEWEST_FIRST],
      ['oldest first', OLDEST_FIRST],
    ] as const) {
      cleanup()
      const openExtraction = vi.fn()
      mockFetch(WITH_DOCUMENT, jobs)
      render(<InvoiceDetail ctx={detailCtx(openExtraction)} />)

      const btn = (await screen.findByTestId(TESTID)) as HTMLButtonElement
      await waitFor(() => expect(btn.disabled, `${label}: the control never enabled`).toBe(false))
      await userEvent.click(btn)

      expect(openExtraction, `${label}: one click, one hand-off`).toHaveBeenCalledTimes(1)
      expect(openExtraction, `${label}: the newest job by created_at must win`).toHaveBeenCalledWith(NEWER_JOB)
    }
  })
})

describe('AC-6/AC-7: the two disabled states, end to end', () => {
  it('invoiceDetail_anEmptyJobsListLeavesTheControlDisabledWithAVisibleReason', async () => {
    mockFetch(WITH_DOCUMENT, [])
    // The probe is the population floor for the absence below: the invoice detail carries
    // ZERO title attributes today, so without a planted needle an empty query result reads
    // the same whether the selector works or not.
    const { container } = render(
      <div>
        <span data-testid="title-probe" title="control needle" />
        <InvoiceDetail ctx={detailCtx(vi.fn())} />
      </div>,
    )

    const btn = (await screen.findByTestId(TESTID)) as HTMLButtonElement
    await waitFor(() => expect(btn.disabled, 'no job, so the control must end up disabled').toBe(true))
    expect(screen.getByText(NO_JOB_REASON), 'the reason must be visible text on the card').toBeTruthy()

    const titled = [...container.querySelectorAll('[title]')].map((el) => el.getAttribute('data-testid'))
    expect(titled, 'the reason must be visible text -- only the control needle may carry a title').toEqual(['title-probe'])
  })

  it('invoiceDetail_whileTheLookupIsInFlightTheControlIsDisabledWithNoReason', async () => {
    mockFetch(WITH_DOCUMENT, 'hang')
    render(<InvoiceDetail ctx={detailCtx(vi.fn())} />)

    const btn = (await screen.findByTestId(TESTID)) as HTMLButtonElement
    expect(btn.disabled, 'an unsettled lookup must leave the control disabled').toBe(true)
    expect(
      screen.queryByText(NO_JOB_REASON),
      'an unsettled lookup has not established that there is no job',
    ).toBeNull()
  })
})

// QA gap, closed here. The source-document read and this one fail INDEPENDENTLY -- the card
// body renders off a 200 while the extractions lookup 500s -- so the control has to say
// something. The prop shape `{ jobId, loading }` could not: it mapped an error onto `loading`.
describe('QA-1: a failed lookup gets its own reason, never the no-job one', () => {
  for (const [label, mode] of [
    ['a 500', '500'],
    ['a 200 with a null body', 'null-body'],
  ] as const) {
    it(`invoiceDetail_${mode === '500' ? 'aFailedLookup' : 'ANullBody'}LeavesTheControlDisabledWithItsOwnReason`, async () => {
      const wire = mockFetch(WITH_DOCUMENT, mode)
      render(<InvoiceDetail ctx={detailCtx(vi.fn())} />)

      const btn = (await screen.findByTestId(TESTID)) as HTMLButtonElement
      // Floor: the source read really did succeed, so the card rendered its record body and
      // the two lookups really did settle differently.
      await screen.findByTestId('source-document-card-meta')
      await waitFor(() => expect(wire.extractionCalls.length, `${label}: the lookup never ran`).toBeGreaterThan(0))

      await waitFor(() =>
        expect(screen.queryByText(LOOKUP_FAILED_REASON), `${label}: the control explains nothing`).not.toBeNull(),
      )
      expect(btn.disabled, `${label}: a failed lookup found no job, so the control must be disabled`).toBe(true)
      expect(
        screen.queryByText(NO_JOB_REASON),
        `${label}: a failed lookup must not claim the document has no extraction`,
      ).toBeNull()
    })
  }

  it('invoiceDetail_aSettledEmptyListStillGetsTheNoJobReason', async () => {
    // Control needle for the two rows above, through the same component and the same stub: a
    // real settled 200 with no jobs DOES get the other sentence, so the fork is a fork.
    mockFetch(WITH_DOCUMENT, [])
    render(<InvoiceDetail ctx={detailCtx(vi.fn())} />)

    await screen.findByTestId(TESTID)
    await waitFor(() => expect(screen.queryByText(NO_JOB_REASON)).not.toBeNull())
    expect(screen.queryByText(LOOKUP_FAILED_REASON), 'a settled empty list is not a failed lookup').toBeNull()
  })
})

// The dep list is `[documentId]`, not the siblings' `[invoiceId]`. LiveInvoiceDetail is keyed
// by invoiceId (InvoiceDetail.tsx:102), so invoiceId is CONSTANT for a mount: with it in the
// deps the effect runs once, at mount, while documentId is still null and `immediate` is
// false -- and the lookup never fires at all. That is what this row measures.
describe('QA-2: the lookup fires on the documentId the source read resolved', () => {
  it('invoiceDetail_theLookupFiresOnlyAfterTheDocumentIdResolves', async () => {
    const wire = mockFetch(WITH_DOCUMENT, NEWEST_FIRST)
    render(<InvoiceDetail ctx={detailCtx(vi.fn())} />)

    await screen.findByTestId(TESTID)
    await waitFor(() => expect(wire.extractionCalls.length, 'the lookup never ran').toBe(1))

    // It ran AFTER the source read, never before -- a dep list that could not see documentId
    // would have to fire at mount (with no id) or not at all.
    const firstExtraction = wire.calls.findIndex((u) => u.includes('/v1/extractions'))
    const firstSource = wire.calls.findIndex((u) => u.endsWith('/source-document'))
    expect(firstSource, 'the source read never happened').toBeGreaterThan(-1)
    expect(firstExtraction, 'the extractions read never happened').toBeGreaterThan(-1)
    expect(firstExtraction, 'the lookup was issued before the record that names its document').toBeGreaterThan(firstSource)
    expect(new URL(wire.extractionCalls[0]!).searchParams.get('document_id')).toBe(DOCUMENT_ID)
  })
})

// `newestJob(jobs)` iterates its argument, so `undefined` throws -- during RENDER, not inside a
// promise, but a throw either way. `?? []` at the call site is the only thing between the
// component and a malformed body. Go's reader.go guarantees an array, so neither shape below is
// reachable through the shipped server; both are one bad deploy away.
describe('QA-4: a malformed jobs list does not take the page down', () => {
  for (const [label, mode] of [
    ['`jobs: null`', 'null-jobs'],
    ['no `jobs` key at all', 'no-jobs-key'],
  ] as const) {
    it(`invoiceDetail_${mode === 'null-jobs' ? 'ANullJobsList' : 'AMissingJobsKey'}RendersTheNoJobArm`, async () => {
      mockFetch(WITH_DOCUMENT, mode)
      render(<InvoiceDetail ctx={detailCtx(vi.fn())} />)

      // The page rendered at all: a throw out of newestJob takes the whole detail with it, so
      // finding the control IS the assertion that it did not.
      const btn = (await screen.findByTestId(TESTID)) as HTMLButtonElement
      await screen.findByTestId('source-document-card-meta')
      await waitFor(() => expect(btn.disabled, `${label}: no job, so the control must be disabled`).toBe(true))

      // A settled 200 with an unreadable list still reads as "no job", not as a failed lookup:
      // useAsync resolves a non-null object to 'ready' whatever is inside it.
      await waitFor(() => expect(screen.queryByText(NO_JOB_REASON), `${label}: no reason shown`).not.toBeNull())
      expect(screen.queryByText(LOOKUP_FAILED_REASON), `${label}: a 200 is not a failed lookup`).toBeNull()
    })
  }
})
