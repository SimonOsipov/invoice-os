// @vitest-environment jsdom
// APPR-16-01, task-534 -- runtime render coverage the Mode A red phase didn't provide.
// LIB-SCAN-1/A16-1c only see SOURCE text; they cannot see a string composed at runtime
// (e.g. a caption reassembled from parts, or a stray literal reintroduced beside the
// exported constant without the source scan noticing). This renders the real
// ReviewBatch component end to end and asserts on what actually lands in the DOM.
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createAuthedFetch } from '../lib/authedFetch'
import type { ImportBatch } from '../lib/importApi'
import { reviewFooterSummary, TILE_CAPTION_VALID } from '../lib/reviewBatch'
import type { PlatformCtx } from '../types'
import { ReviewBatch } from './ReviewBatch'

afterEach(cleanup)

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
})

interface MockResponse {
  ok: boolean
  status: number
  json: () => Promise<unknown>
}

function listResponse(total: number): MockResponse {
  return { ok: true, status: 200, json: () => Promise.resolve({ invoices: [], pagination: { limit: 1, offset: 0, total } }) }
}

const TOTALS = { allTotal: 500, cleanTotal: 474, queuedTotal: 6, failingTotal: 20, keptTotal: 3 }

function batch(over: Partial<ImportBatch> = {}): ImportBatch {
  return {
    id: 'b1',
    entity_id: 'ent-1',
    filename: 'june.csv',
    status: 'completed',
    rows_total: 500,
    rows_valid: 480,
    rows_invalid: 20,
    errors: [],
    rule_set_version: 3,
    created_at: '2026-08-01T00:00:00Z',
    ...over,
  }
}

// The shell fires six concurrent GETs (batch + four pill counts + kept-as-is), and the
// invoices tab fires two more (its own paginated list + violation summary) -- dispatched
// by URL/param, not call order, mirroring InvoiceDetail.test.tsx's mockDetailFetch idiom.
function mockReviewFetch(b: ImportBatch, totals: typeof TOTALS) {
  const fetchMock = vi.fn((url: string) => {
    if (url.includes('/imports/')) {
      return Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve(b) })
    }
    if (url.includes('/violation-summary')) {
      return Promise.resolve<MockResponse>({ ok: true, status: 200, json: () => Promise.resolve({ rules: [] }) })
    }
    if (url.includes('/invoices')) {
      const params = new URL(url).searchParams
      if (params.get('kept_as_is') === 'true') return Promise.resolve(listResponse(totals.keptTotal))
      if (params.get('needs_fix') === 'true') return Promise.resolve(listResponse(totals.failingTotal))
      if (params.get('status') === 'validated') return Promise.resolve(listResponse(totals.cleanTotal))
      if (params.get('status') === 'queued') return Promise.resolve(listResponse(totals.queuedTotal))
      if (params.get('limit') === '1') return Promise.resolve(listResponse(totals.allTotal))
      // The invoices tab's own paginated list (not one of the shell's limit:1 pill counts).
      return Promise.resolve(listResponse(0))
    }
    return Promise.reject(new Error(`unmocked url: ${url}`))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function reviewCtx(batchIds: string[]): PlatformCtx {
  const ctx = {
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    reviewBatchIds: batchIds,
    run: null,
    restartImport: vi.fn(),
    openImportedInvoice: vi.fn(),
    closeCreate: vi.fn(),
    skipUpload: vi.fn(),
  }
  return ctx as unknown as PlatformCtx
}

describe('ReviewBatch: rendered captions name validation, not entitlement (APPR-16-01)', () => {
  it('R16-1: the tile caption rendered in the DOM is exactly TILE_CAPTION_VALID, not an inline literal', async () => {
    mockReviewFetch(batch(), TOTALS)

    render(<ReviewBatch ctx={reviewCtx(['b1'])} />)

    await waitFor(() => expect(screen.getByText(TILE_CAPTION_VALID)).toBeTruthy())
    // A wired-up assertion, not just an exported-constant check: nothing on the page
    // says the old inline literal, and the ONLY occurrence of the caption text matches
    // the export byte for byte (getByText throws on more than one match).
    expect(document.body.textContent).toContain('Passed every rule.')
  })

  it('R16-2: the rendered footer line is exactly reviewFooterSummary(totals)', async () => {
    mockReviewFetch(batch(), TOTALS)

    render(<ReviewBatch ctx={reviewCtx(['b1'])} />)

    const expected = reviewFooterSummary(TOTALS)
    await waitFor(() => expect(screen.getByText(expected)).toBeTruthy())
  })

  it('R16-3: no rendered text on the screen contains "ready to submit", in any case', async () => {
    mockReviewFetch(batch(), TOTALS)

    render(<ReviewBatch ctx={reviewCtx(['b1'])} />)

    await waitFor(() => expect(screen.getByText(TILE_CAPTION_VALID)).toBeTruthy())
    // The source scan (A16-1c) can only see ReviewBatch.tsx's own text; this catches a
    // string composed at runtime (e.g. concatenated from parts) that a source scan
    // structurally cannot.
    expect(document.body.textContent?.toLowerCase()).not.toContain('ready to submit')
  })
})
