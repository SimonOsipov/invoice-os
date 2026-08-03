// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.

import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import { createAuthedFetch } from '../lib/authedFetch'
import type { DocumentSheet, SourceDocumentRecord, SourceDocumentResponse } from '../lib/sourceDocument'
import type { PlatformCtx } from '../types'
import { SourceDocumentModal } from './SourceDocumentModal'
import type { SourceDocumentAsync } from './SourceDocumentStates'

const HASH = '3f9a1c02b7d4e6108a5c93f21e0d47b6c8a2f5039e1b7d4c60a8f3e2d5a86560'

function record(over: Partial<SourceDocumentRecord> = {}): SourceDocumentRecord {
  return {
    id: 'doc-1',
    filename: 'june-sales.xlsx',
    declared_content_type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
    size_bytes: 624640, // 610 KB exactly, 1024-base
    content_hash: HASH,
    uploaded_at: '2026-06-12T11:42:00Z',
    uploaded_by: 'c0000000-0000-0000-0000-000000000001',
    invoices_created: 500,
    other_invoice_rows: [],
    ...over,
  }
}

function response(over: Partial<SourceDocumentResponse> = {}): SourceDocumentResponse {
  return { invoice_id: 'inv-1', source_rows: [44, 45, 46, 47], document: record(), ...over }
}

function metaAsync(over: Partial<SourceDocumentAsync> = {}): SourceDocumentAsync {
  return { status: 'ready', data: response(), error: null, run: vi.fn(), ...over }
}

// The modal reads five ctx fields. Typed against the real PlatformCtx so a rename breaks
// the typecheck; the cast stands in for the ~90 fields it never touches.
type ModalCtx = Pick<PlatformCtx, 'authedFetch' | 'getToken' | 'mode' | 'user'> & {
  active: Pick<PlatformCtx['active'], 'name'>
}

function modalCtx(): PlatformCtx {
  const ctx: ModalCtx = {
    authedFetch: createAuthedFetch(() => 'tok', vi.fn()),
    getToken: () => 'tok',
    mode: 'firm',
    user: { name: 'Chinedu Okafor', initials: 'CO', tenantName: 'Okafor & Partners', verified: true },
    active: { name: 'Lagos Logistics Ltd' },
  }
  return ctx as unknown as PlatformCtx
}

function sheet(over: Partial<DocumentSheet> = {}): DocumentSheet {
  return {
    format: 'xlsx',
    delimiter: null,
    encoding: null,
    columns: Array.from({ length: 11 }, (_, i) => `col-${i}`),
    rows: [],
    rows_total: 1479,
    rows_returned: 1479,
    truncated: false,
    ...over,
  }
}

// Dispatched by URL suffix, never by call order: the shell can have more than one channel
// in flight. `null` body means "never resolves", which is how the sheet-loading window is
// held open for spec 5.
function mockFetch(bodyForSheet: DocumentSheet | null) {
  vi.stubGlobal(
    'fetch',
    vi.fn(() =>
      bodyForSheet === null
        ? new Promise(() => {})
        : Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(bodyForSheet) }),
    ),
  )
}

function renderModal(meta: SourceDocumentAsync) {
  return render(
    <SourceDocumentModal
      ctx={modalCtx()}
      meta={meta}
      invoiceNumber="INV-2026-0037"
      invoiceCreatedAt="2026-06-12T09:15:00Z"
      createdBy="c0000000-0000-0000-0000-000000000001"
      onClose={vi.fn()}
    />,
  )
}

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
  mockFetch(sheet())
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
})

describe('SourceDocumentModal shell', () => {
  // THE regression test for the hang. `minmax(0, 1fr)` alone constrains the ROW only: an
  // unconstrained flex item still takes a content-based automatic minimum, so the grid
  // grows to the virtualised sheet's full height and the window logic renders every row.
  // A spec asserting only the design's rule passes while the tab still hangs.
  it('modal body grid declares grid-template-rows and cannot grow', async () => {
    renderModal(metaAsync())

    const body = await screen.findByTestId('source-document-modal-body')
    expect(body.style.length).toBeGreaterThan(0) // vacuity floor: inline styles resolved at all

    expect(body.style.gridTemplateRows).toBe('minmax(0, 1fr)')
    expect(body.style.gridTemplateColumns).toBe('minmax(0, 1fr) 316px')
    expect(body.style.flex).not.toBe('')
    expect(body.style.flexGrow).toBe('1')
    expect(body.style.minHeight).not.toBe('')
    expect(parseFloat(body.style.minHeight)).toBe(0)

    // The canvas cell needs the same floor: a grid item's automatic minimum is
    // content-based, so the row rule alone does not stop the sheet growing this cell.
    const canvas = screen.getByTestId('source-document-canvas')
    expect(parseFloat(canvas.style.minHeight)).toBe(0)
    expect(parseFloat(canvas.style.minWidth)).toBe(0)
    expect(canvas.style.overflow).toBe('hidden')
  })

  it("renders each non-sheet state's own copy", async () => {
    const cases: Array<{ testid: string; meta: SourceDocumentAsync; sentence: string }> = [
      {
        testid: 'source-document-no-source',
        meta: metaAsync({ data: response({ document: null, source_rows: null }) }),
        sentence: 'There is no source document',
      },
      {
        testid: 'source-document-loading',
        meta: metaAsync({ status: 'loading', data: null }),
        sentence: 'only the bytes are still on their way',
      },
      {
        testid: 'source-document-unrenderable',
        meta: metaAsync({ data: response({ document: record({ filename: 'ledger.dat', declared_content_type: null }) }) }),
        sentence: 'This file is stored, but we cannot render it here',
      },
      {
        testid: 'source-document-failed',
        meta: metaAsync({ status: 'error', data: null, error: new ApiError('http', 'boom', 503) }),
        sentence: 'The document did not load',
      },
    ]

    for (const c of cases) {
      renderModal(c.meta)
      const canvas = await screen.findByTestId(c.testid)
      expect(canvas.textContent).toContain(c.sentence)
      for (const other of cases.filter((o) => o.testid !== c.testid)) {
        expect(screen.queryByTestId(other.testid)).toBeNull()
        expect(screen.getByTestId('source-document-modal').textContent).not.toContain(other.sentence)
      }
      cleanup()
    }
  })

  it('the unrenderable state offers no action', async () => {
    renderModal(metaAsync({ data: response({ document: record({ filename: 'ledger.dat', declared_content_type: null }) }) }))

    const canvas = await screen.findByTestId('source-document-unrenderable')
    expect((canvas.textContent ?? '').length).toBeGreaterThan(0) // vacuity floor
    expect(canvas.querySelectorAll('button').length).toBe(0)
    expect(screen.getByTestId('source-document-modal').textContent).not.toMatch(/download/i)
  })

  it('the failed state offers Try again and nothing else', async () => {
    const run = vi.fn()
    renderModal(metaAsync({ status: 'error', data: null, error: new ApiError('http', 'boom', 503), run }))

    const canvas = await screen.findByTestId('source-document-failed')
    const buttons = Array.from(canvas.querySelectorAll('button'))
    expect(buttons.length).toBe(1)
    expect(buttons[0].textContent?.trim()).toBe('Try again')

    // The real request id the design prints does not exist: the envelope is `{error}` and
    // ApiError carries only kind/status/body.
    expect(screen.getByTestId('source-document-failure-line').textContent).toBe('HTTP 503')

    await userEvent.click(screen.getByTestId('source-document-retry'))
    expect(run).toHaveBeenCalledTimes(1)

    expect(screen.getByTestId('source-document-modal').textContent).not.toMatch(/download/i)
  })

  it('row and column counts appear only once the sheet lands', async () => {
    mockFetch(null) // sheet request never resolves
    renderModal(metaAsync())

    // Floor: prove this really is the sheet-loading window and not a failed fetch, which
    // would also leave the counts off the header.
    await screen.findByTestId('source-document-loading')
    const loadingMeta = screen.getByTestId('source-document-meta')
    expect(loadingMeta.textContent).toMatch(/^XLSX · [\d.]+ [KMG]?B$/)
    expect(loadingMeta.textContent).not.toContain('ROWS')
    expect(loadingMeta.textContent).not.toContain('COLUMNS')

    cleanup()
    mockFetch(sheet())
    renderModal(metaAsync())

    const readyMeta = await screen.findByText(/1,479 ROWS/)
    expect(readyMeta.textContent).toContain('11 COLUMNS')
    expect(readyMeta.textContent).toContain('XLSX · 610 KB')
  })
})
