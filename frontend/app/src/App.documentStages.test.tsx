// @vitest-environment jsdom
// task-786 (EXTR-10-04) QA, Mode B adversarial coverage -- Core AC 6, "the stage map is
// empty at the start of every run". CARD-1..8 (ImportProgress.test.tsx) and STAGE-3/4/ADV-4
// (documentRun.test.ts) all pin documentRunRows' JOIN, never the RESET: nothing proved
// `setDocumentStages({})` (App.tsx, startDocumentRun) actually fires. It matters because the
// one call site (CreateUpload.tsx:315) is reachable a second time on the SAME `pickedFiles` --
// ImportProgress.tsx's own header comment says a whole-run failure routes back to the step
// router with the run intact, not through resetImport -- so a second click reuses the same
// crypto.randomUUID() file ids `documentRunRows` joins on. Two runs with DIFFERENT ids could
// never collide regardless of the reset; this is the one path where they don't.
//
// Harness is App.auditPrefilter.test.tsx's ("the App -> AuditView seam"): the real <App/>, a
// stubbed session/localStorage, a stubbed gateway, and ctx captured through a mocked Sidebar.
// The upload leg is XHR, not fetch (importApi.ts's uploadSourceDocument/xhrJson), so a local
// FakeXhr drives it deterministically -- same idiom as importApi.test.ts's FakeXhr, duplicated
// rather than imported (ImportProgress.test.tsx's own stated reason: independently owned test
// fixtures).
//
// The landing/exit block below drives the run all the way through poll + import + the
// invoice lookup, so routeFetch grew a small book of fixtures keyed by document/job/batch id
// instead of the one-shot onerror this file used to need.

import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Session } from './auth'
import type { DocumentRowState } from './lib/documentRun'
import { SESSION_KEY, serializeSession } from './lib/session'
import type { PlatformCtx } from './types'

// Minimal fake: only what xhrJson (importApi.ts) touches for one POST /documents round
// trip. `fireError()` leaves a stage entry behind without settling it; `respond()` (copied
// from App.routeReviewHash.test.tsx:110-114) settles it, which the landing/exit rows need.
class FakeXhr {
  static instances: FakeXhr[] = []
  static last(): FakeXhr | undefined {
    return FakeXhr.instances[FakeXhr.instances.length - 1]
  }

  status = 0
  statusText = ''
  responseText = ''
  upload: { onprogress: (() => void) | null; onload: (() => void) | null } = { onprogress: null, onload: null }
  onload: (() => void) | null = null
  onerror: (() => void) | null = null
  ontimeout: (() => void) | null = null

  constructor() {
    FakeXhr.instances.push(this)
  }

  open(): void {}
  setRequestHeader(): void {}
  send(): void {}

  fireError(): void {
    this.onerror?.()
  }

  respond(status: number, body: unknown): void {
    this.status = status
    this.responseText = JSON.stringify(body)
    this.onload?.()
  }
}

// Node v25's native localStorage collides with jsdom's (App.standIn.test.tsx:74-75).
function createMemoryStorage() {
  const store = new Map<string, string>()
  return {
    getItem: vi.fn((key: string) => (store.has(key) ? (store.get(key) as string) : null)),
    setItem: vi.fn((key: string, value: string) => {
      store.set(key, value)
    }),
    removeItem: vi.fn((key: string) => {
      store.delete(key)
    }),
    clear: vi.fn(() => {
      store.clear()
    }),
  }
}

const SEAT_SESSION: Session = { persona: APP_PERSONAS.firm, token: null, me: null, verified: true }
const GATEWAY = 'https://gw.test'
const ENTITY_ID = 'aaaaaaaa-0000-4000-8000-000000000001'
const ENTITY_B = 'aaaaaaaa-0000-4000-8000-000000000002'

const DOC_ID = 'dddddddd-1111-4111-8111-111111111111'
const JOB_ID = 'eeeeeeee-1111-4111-8111-111111111111'
const BATCH_ID = 'bbbbbbbb-1111-4111-8111-111111111111'
const INVOICE_ID = 'ffffffff-1111-4111-8111-111111111111'
const OTHER_JOB_ID = 'eeeeeeee-2222-4222-8222-222222222222'

const DOC_A = 'dddddddd-3333-4333-8333-333333333333'
const JOB_A = 'eeeeeeee-3333-4333-8333-333333333333'
const BATCH_A = 'bbbbbbbb-3333-4333-8333-333333333333'
const INV_A = 'ffffffff-3333-4333-8333-333333333333'
const DOC_B = 'dddddddd-4444-4444-8444-444444444444'
const JOB_B = 'eeeeeeee-4444-4444-8444-444444444444'
const BATCH_B = 'bbbbbbbb-4444-4444-8444-444444444444'
const INV_B = 'ffffffff-4444-4444-8444-444444444444'

let capturedCtx: PlatformCtx | undefined
vi.mock('./components/Sidebar', () => ({
  Sidebar: (p: { ctx: PlatformCtx }) => {
    capturedCtx = p.ctx
    return null
  },
}))

beforeEach(() => {
  capturedCtx = undefined
  FakeXhr.instances = []
  window.history.replaceState(null, '', '/')
  vi.stubGlobal('localStorage', createMemoryStorage())
  vi.stubGlobal('XMLHttpRequest', FakeXhr)
})

afterEach(() => {
  cleanup()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

interface RouteBook {
  jobsByDocument?: Record<string, unknown[]>
  detailByJob?: Record<string, unknown>
  importReplies?: unknown[]
  invoicesByBatch?: Record<string, { id: string }[]>
}

// One real entity (portfolio/v1/entities) so `active.entityId` resolves to something
// startDocumentRun will accept; everything else answers well enough not to crash a
// mounting Workspace (App.auditPrefilter.test.tsx's routeFetch, same fallback shape).
// `book` extends the table with the poll/import/invoice-lookup legs a landed run needs,
// keyed so each test supplies only the ids it drives.
function routeFetch(book: RouteBook = {}) {
  let importCall = 0
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      const jobsMatch = /\/extractions\?document_id=([^&]+)/.exec(url)
      if (jobsMatch) {
        const docId = decodeURIComponent(jobsMatch[1])
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({ jobs: book.jobsByDocument?.[docId] ?? [] }),
        })
      }
      const detailMatch = /\/extractions\/([^/?]+)$/.exec(url)
      const detail = detailMatch ? book.detailByJob?.[decodeURIComponent(detailMatch[1])] : undefined
      if (detail) {
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(detail) })
      }
      if (url.includes('/api/invoice/v1/imports/document')) {
        const replies = book.importReplies ?? []
        const reply = replies[Math.min(importCall, replies.length - 1)]
        importCall += 1
        return Promise.resolve({ ok: true, status: 201, json: () => Promise.resolve(reply) })
      }
      if (url.includes('/api/invoice/v1/invoices?') && url.includes('import_batch_id=')) {
        const m = /import_batch_id=([^&]+)/.exec(url)
        const batchId = m ? decodeURIComponent(m[1]) : ''
        const invoices = book.invoicesByBatch?.[batchId] ?? []
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({ invoices, pagination: { limit: 1, offset: 0, total: invoices.length } }),
        })
      }
      // getInvoiceApprovalRun (lib/approvals.ts) reads a 404 as "no run" -- the generic
      // fallback below is a truthy object, which renders as a malformed run instead.
      if (/\/api\/invoice\/v1\/invoices\/[^/?]+\/approval$/.test(url)) {
        return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({ error: 'not found' }) })
      }
      // The exit's own landing (getInvoice) -- a well-formed row so InvoiceDetail's
      // render (rejection_reasons.length et al.) does not throw on a fallback shape.
      const invoiceMatch = /\/api\/invoice\/v1\/invoices\/([^/?]+)$/.exec(url)
      if (invoiceMatch) {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve(invoiceDetailReply(decodeURIComponent(invoiceMatch[1]))),
        })
      }
      if (url.includes('/portfolio/v1/entities')) {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () =>
            Promise.resolve({
              entities: [
                {
                  id: ENTITY_ID,
                  name: 'Stale Map Test Co',
                  tin: '12345678-0001',
                  registration: null,
                  sector: null,
                  address: null,
                  status: 'active',
                  created_at: '2026-01-01T00:00:00Z',
                },
                {
                  id: ENTITY_B,
                  name: 'Second Entity Co',
                  tin: '87654321-0001',
                  registration: null,
                  sector: null,
                  address: null,
                  status: 'active',
                  created_at: '2026-01-01T00:00:00Z',
                },
              ],
              pagination: { limit: 200, offset: 0, total: 2 },
            }),
        })
      }
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ entities: [], policies: [], members: [], roles: [], invoices: [], total: 0 }),
      })
    }),
  )
}

async function renderApp(book: RouteBook = {}) {
  routeFetch(book)
  localStorage.setItem(SESSION_KEY, serializeSession(SEAT_SESSION))
  vi.stubEnv('VITE_GATEWAY_URL', GATEWAY)
  vi.resetModules()
  const { default: App } = await import('./App')
  return render(<App />)
}

function requireCtx(): PlatformCtx {
  expect(capturedCtx, 'Sidebar never rendered -- ctx was not captured').toBeDefined()
  return capturedCtx!
}

// jsdom runs no real history stack for back()/forward() -- move the URL the way the
// browser would, then fire the event the browser fires (App.routePopstate.test.tsx:118-123).
async function popTo(path: string) {
  window.history.replaceState(null, '', path)
  await act(async () => {
    window.dispatchEvent(new PopStateEvent('popstate'))
  })
}

function extractionJob(id: string, documentId: string) {
  return { id, document_id: documentId, state: 'succeeded', created_at: '2026-01-01T00:00:00Z', last_error: null, failure_kind: null }
}

function extractionDetail(id: string, documentId: string) {
  return {
    id,
    document_id: documentId,
    state: 'succeeded',
    failure_kind: null,
    document: { filename: 'invoice.pdf', content_type: 'application/pdf', size_bytes: 14, stored_at: '2026-01-01T00:00:00Z' },
    pages: [],
    fields: [],
  }
}

// A minimally valid InvoiceRecord (lib/invoices.ts) -- every required field present, so
// InvoiceDetail's render (rejection_reasons.length, etc.) never sees an undefined one.
function invoiceDetailReply(id: string) {
  return {
    id,
    entity_id: ENTITY_ID,
    import_batch_id: null,
    invoice_number: 'INV-0001',
    status: 'accepted',
    issue_date: null,
    supplier_tin: null,
    supplier_name: null,
    buyer_tin: null,
    buyer_name: null,
    currency: null,
    subtotal: null,
    vat: null,
    total: null,
    violations: [],
    rule_set_version_id: null,
    created_at: '2026-01-01T00:00:00Z',
    irn: null,
    csid: null,
    qr_payload: null,
    rejection_reasons: [],
    kept_as_is_at: null,
    kept_as_is_by: null,
    kept_as_is_reason: null,
    failure_kind: null,
    rule_set_version: null,
    approval: null,
    can_approve: false,
    approve_blocked_reason: null,
    can_submit: false,
    submit_blocked_reason: null,
  }
}

function uploadReply(documentId: string) {
  return { document_id: documentId, filename: 'invoice.pdf', content_type: 'application/pdf', size_bytes: 14, reused: false }
}

function importReply(batchId: string) {
  return {
    id: batchId,
    status: 'completed',
    format: 'pdf',
    delimiter: '',
    encoding: '',
    rows_total: 1,
    rows_valid: 1,
    rows_invalid: 0,
    ready_invoices: 1,
    quarantined_invoices: 0,
    errors: [],
    rule_set_version: null,
    invoices_clean: 1,
    invoices_with_violations: 0,
    invoice_violations: [],
  }
}

describe('Core AC 6: the stage map is empty at the start of every run', () => {
  it('documentStagesResetsOnASecondRunOverTheSamePickedFiles', async () => {
    await renderApp()
    let ctx = requireCtx()

    await act(async () => {
      ctx.openCreate()
    })
    await waitFor(() => expect(requireCtx().entityId, 'active.entityId never backfilled entityId').toBe(ENTITY_ID))

    const file = new File(['%PDF-1.4 dummy'], 'invoice.pdf', { type: 'application/pdf' })
    await act(async () => {
      requireCtx().addPickedFiles([file])
    })
    ctx = requireCtx()
    expect(ctx.pickedFiles, 'the file was not picked').toHaveLength(1)
    expect(ctx.runKind, 'the picked file did not classify as a document run').toBe('document')
    const fileId = ctx.pickedFiles[0]!.id

    // Vacuity floor: documentStages must start empty (resetImport's own initial state),
    // or run 1's failure below proves nothing about a RESET.
    expect(ctx.documentStages, 'documentStages was not empty before run 1').toEqual({})

    // Run 1: fail it, deterministically, via the upload transport's own onerror -- no
    // wire shape needed for poll/import, since a rejected upload never reaches either.
    act(() => {
      requireCtx().startDocumentRun()
    })
    expect(FakeXhr.instances, 'control: run 1 never reached the upload transport').toHaveLength(1)
    act(() => {
      FakeXhr.last()!.fireError()
    })
    await waitFor(() =>
      expect(requireCtx().documentStages[fileId]?.kind, 'run 1 must leave a stage entry behind to go stale').toBe(
        'failed',
      ),
    )
    const staleReason = (requireCtx().documentStages[fileId] as Extract<DocumentRowState, { kind: 'failed' }>).reason
    expect(staleReason, 'run 1 must carry a real reason, not an empty one').not.toBe('')

    // Run 2, same pickedFiles (same file id) -- the one path where a leftover key COULD
    // leak: CreateUpload's button is the sole call site (grep) and a whole-run failure
    // routes back without resetImport, so this is the reachable second click, not a
    // fixture-only replay. Retried through waitFor: run 1's own Promise.all/finally must
    // clear `reqInFlight` first, and a call while it is still set is a silent no-op
    // (App.tsx's startDocumentRun) -- caught here because that no-op would never create
    // FakeXhr instance #2.
    await waitFor(() => {
      act(() => {
        requireCtx().startDocumentRun()
      })
      expect(FakeXhr.instances, 'control: run 2 never reached the upload transport').toHaveLength(2)
    })

    // The assertion: instance #2 exists but has not been resolved or rejected yet, so
    // nothing but the reset itself could have touched documentStages since run 2 started.
    expect(requireCtx().documentStages, 'a stale entry from run 1 survived into run 2').toEqual({})
  })
})

// One document, one ready invoice, through to a settled outcome: renders the create view,
// picks a file, starts the run and settles the upload leg. Returns the history length
// captured right before the run starts, so callers can pin how many entries the landing
// itself pushed.
async function landOneDocumentRun(book: RouteBook, docId: string): Promise<number> {
  await renderApp(book)
  await act(async () => {
    requireCtx().openCreate()
  })
  await waitFor(() => expect(requireCtx().entityId, 'active.entityId never backfilled entityId').toBe(ENTITY_ID))

  const file = new File(['%PDF-1.4 dummy'], 'invoice.pdf', { type: 'application/pdf' })
  await act(async () => {
    requireCtx().addPickedFiles([file])
  })
  const before = window.history.length
  act(() => {
    requireCtx().startDocumentRun()
  })
  expect(FakeXhr.instances, 'control: the run never reached the upload transport').toHaveLength(1)
  act(() => {
    FakeXhr.last()!.respond(201, uploadReply(docId))
  })
  return before
}

describe('EXTR32-A1: a one-document run that imports one invoice lands on /extraction/<jobId> in one pushed entry', () => {
  it('EXTR32-A1: a one-document run that imports one invoice lands on /extraction/<jobId> in one pushed entry', async () => {
    const before = await landOneDocumentRun(
      {
        jobsByDocument: { [DOC_ID]: [extractionJob(JOB_ID, DOC_ID)] },
        detailByJob: { [JOB_ID]: extractionDetail(JOB_ID, DOC_ID) },
        importReplies: [importReply(BATCH_ID)],
        invoicesByBatch: { [BATCH_ID]: [{ id: INVOICE_ID }] },
      },
      DOC_ID,
    )

    await waitFor(() =>
      expect(window.location.pathname, 'the landing must be addressed at its own job path').toBe(`/extraction/${JOB_ID}`),
    )
    expect(requireCtx().view, 'the view must land on extraction').toBe('extraction')
    expect(requireCtx().extractionJobId, 'ctx.extractionJobId must carry the landed job').toBe(JOB_ID)
    expect(window.history.length, 'the landing must push exactly one entry').toBe(before + 1)
  })
})

describe('EXTR32-A2: the landing drains the run, so Back to /create renders no progress card', () => {
  it('EXTR32-A2: the landing drains the run, so Back to /create renders no progress card', async () => {
    await landOneDocumentRun(
      {
        jobsByDocument: { [DOC_ID]: [extractionJob(JOB_ID, DOC_ID)] },
        detailByJob: { [JOB_ID]: extractionDetail(JOB_ID, DOC_ID) },
        importReplies: [importReply(BATCH_ID)],
        invoicesByBatch: { [BATCH_ID]: [{ id: INVOICE_ID }] },
      },
      DOC_ID,
    )
    await waitFor(() =>
      expect(window.location.pathname, 'sanity: the run must land on its job path').toBe(`/extraction/${JOB_ID}`),
    )

    expect(requireCtx().run, 'the landing must drain the run to a literal idle state').toEqual({
      files: [],
      cursor: 0,
      status: 'idle',
    })

    await popTo('/create')

    // Render floor before the absence check: Back must actually land on the documents
    // step, or the import-progress absence below would pass on a blank screen too.
    expect(requireCtx().createStep, 'Back must land on the documents step').toBe('documents')
    expect(requireCtx().view, 'Back must restore the create view').toBe('create')
    expect(screen.queryByTestId('import-progress'), 'AC-6: Back must not reopen the finished run').toBeNull()
  })
})

describe('EXTR32-A3: the exit reaches the invoice in one action', () => {
  it('EXTR32-A3: the exit reaches the invoice in one action', async () => {
    await landOneDocumentRun(
      {
        jobsByDocument: { [DOC_ID]: [extractionJob(JOB_ID, DOC_ID)] },
        detailByJob: { [JOB_ID]: extractionDetail(JOB_ID, DOC_ID) },
        importReplies: [importReply(BATCH_ID)],
        invoicesByBatch: { [BATCH_ID]: [{ id: INVOICE_ID }] },
      },
      DOC_ID,
    )

    await waitFor(() =>
      expect(screen.queryByTestId('extraction-open-invoice'), 'the review must offer its exit').not.toBeNull(),
    )

    const before = window.history.length
    fireEvent.click(screen.getByTestId('extraction-open-invoice'))

    await waitFor(() =>
      expect(window.location.pathname, 'the exit must reach the real invoice detail').toBe(`/invoices/${INVOICE_ID}`),
    )
    expect(requireCtx().importedInvoiceId, 'ctx.importedInvoiceId must carry the invoice the exit opened').toBe(INVOICE_ID)
    expect(window.history.length, 'the exit must push exactly one entry').toBe(before + 1)
  })
})

describe('EXTR32-A4: a job the wire cannot identify falls back to the invoice detail', () => {
  it('EXTR32-A4: a job the wire cannot identify falls back to the invoice detail', async () => {
    await landOneDocumentRun(
      {
        jobsByDocument: {
          [DOC_ID]: [
            { id: '', document_id: DOC_ID, state: 'succeeded', created_at: '2026-01-01T00:00:00Z', last_error: null, failure_kind: null },
          ],
        },
        importReplies: [importReply(BATCH_ID)],
        invoicesByBatch: { [BATCH_ID]: [{ id: INVOICE_ID }] },
      },
      DOC_ID,
    )

    await waitFor(() =>
      expect(
        window.location.pathname,
        'a job the wire cannot identify must still land on the invoice detail',
      ).toBe(`/invoices/${INVOICE_ID}`),
    )
    expect(requireCtx().view, 'control: the fallback must render the detail view').toBe('detail')
    expect(screen.queryByTestId('extraction-review'), 'no extraction review must render for an unidentified job').toBeNull()
    expect(requireCtx().run.status, 'the run must still drain to idle, the single-invoice reset').toBe('idle')
  })
})

describe('EXTR32-A5: the exit belongs to its job and its company', () => {
  it('EXTR32-A5: the exit belongs to its job and its company', async () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    try {
      await landOneDocumentRun(
        {
          jobsByDocument: { [DOC_ID]: [extractionJob(JOB_ID, DOC_ID)] },
          detailByJob: {
            [JOB_ID]: extractionDetail(JOB_ID, DOC_ID),
            [OTHER_JOB_ID]: extractionDetail(OTHER_JOB_ID, DOC_ID),
            constructor: extractionDetail('constructor', DOC_ID),
          },
          importReplies: [importReply(BATCH_ID)],
          invoicesByBatch: { [BATCH_ID]: [{ id: INVOICE_ID }] },
        },
        DOC_ID,
      )
      await waitFor(() =>
        expect(window.location.pathname, 'sanity: the run must land on its own job path first').toBe(`/extraction/${JOB_ID}`),
      )

      // (a) another job's review: the exit belongs to JOB_ID alone.
      act(() => {
        requireCtx().openExtraction(OTHER_JOB_ID)
      })
      await waitFor(() =>
        expect(screen.queryByTestId('extraction-save'), '(a) control: the settled footer rendered').not.toBeNull(),
      )
      expect(screen.queryByTestId('extraction-open-invoice'), '(a) another job must offer no exit').toBeNull()

      // (b) back to the job the run actually landed on: the exit returns.
      act(() => {
        requireCtx().openExtraction(JOB_ID)
      })
      await waitFor(() =>
        expect(screen.queryByTestId('extraction-save'), '(b) control: the settled footer rendered').not.toBeNull(),
      )
      expect(screen.queryByTestId('extraction-open-invoice'), '(b) the landed job must still offer its exit').not.toBeNull()

      // (c) a company switch clears the map: the same job renders no exit under the new company.
      act(() => {
        requireCtx().switchClient(ENTITY_B)
      })
      act(() => {
        requireCtx().openExtraction(JOB_ID)
      })
      await waitFor(() =>
        expect(screen.queryByTestId('extraction-save'), '(c) control: the settled footer rendered').not.toBeNull(),
      )
      expect(
        screen.queryByTestId('extraction-open-invoice'),
        '(c) a switched company must offer no exit for the old job',
      ).toBeNull()

      // (d) a job id shaped like a JS prototype key must not resolve through it.
      act(() => {
        requireCtx().openExtraction('constructor')
      })
      await waitFor(() =>
        expect(screen.queryByTestId('extraction-save'), '(d) control: the settled footer rendered').not.toBeNull(),
      )
      expect(screen.queryByTestId('extraction-open-invoice'), '(d) a "constructor" job id must offer no exit').toBeNull()
      expect(errorSpy, '(d) nothing thrown or logged for a prototype-shaped id').not.toHaveBeenCalled()
    } finally {
      errorSpy.mockRestore()
    }
  })
})

describe("EXTR32-A7: a second import keeps the first job's exit", () => {
  it("EXTR32-A7: a second import keeps the first job's exit", async () => {
    await landOneDocumentRun(
      {
        jobsByDocument: {
          [DOC_A]: [extractionJob(JOB_A, DOC_A)],
          [DOC_B]: [extractionJob(JOB_B, DOC_B)],
        },
        detailByJob: {
          [JOB_A]: extractionDetail(JOB_A, DOC_A),
          [JOB_B]: extractionDetail(JOB_B, DOC_B),
        },
        importReplies: [importReply(BATCH_A), importReply(BATCH_B)],
        invoicesByBatch: {
          [BATCH_A]: [{ id: INV_A }],
          [BATCH_B]: [{ id: INV_B }],
        },
      },
      DOC_A,
    )
    await waitFor(() =>
      expect(window.location.pathname, 'sanity: run 1 must land on its own job path').toBe(`/extraction/${JOB_A}`),
    )

    act(() => {
      requireCtx().openCreate()
    })
    const fileB = new File(['%PDF-1.4 dummy b'], 'b.pdf', { type: 'application/pdf' })
    await act(async () => {
      requireCtx().addPickedFiles([fileB])
    })

    // reqInFlight releases in run 1's own finally -- retry until instance #2 exists, the
    // same idiom the Core AC 6 test above uses for its own second run.
    await waitFor(() => {
      act(() => {
        requireCtx().startDocumentRun()
      })
      expect(FakeXhr.instances, 'control: run 2 never reached the upload transport').toHaveLength(2)
    })
    act(() => {
      FakeXhr.instances[1]!.respond(201, uploadReply(DOC_B))
    })

    await waitFor(() =>
      expect(window.location.pathname, 'run 2 must land on its own job path').toBe(`/extraction/${JOB_B}`),
    )

    await popTo(`/extraction/${JOB_A}`)

    await waitFor(() =>
      expect(
        screen.queryByTestId('extraction-open-invoice'),
        "the first job's exit must survive a second import",
      ).not.toBeNull(),
    )
    const before = window.history.length
    fireEvent.click(screen.getByTestId('extraction-open-invoice'))
    await waitFor(() =>
      expect(window.location.pathname, "the exit must reach the FIRST job's own invoice").toBe(`/invoices/${INV_A}`),
    )
    expect(window.history.length, 'the exit must push exactly one entry').toBe(before + 1)
  })
})

describe('EXTR32-A8: a reload of the wired landing reopens the review cleanly with no exit', () => {
  it('EXTR32-A8: a reload of the wired landing reopens the review cleanly with no exit', async () => {
    const book: RouteBook = {
      jobsByDocument: { [DOC_ID]: [extractionJob(JOB_ID, DOC_ID)] },
      detailByJob: { [JOB_ID]: extractionDetail(JOB_ID, DOC_ID) },
      importReplies: [importReply(BATCH_ID)],
      invoicesByBatch: { [BATCH_ID]: [{ id: INVOICE_ID }] },
    }
    await landOneDocumentRun(book, DOC_ID)
    await waitFor(() =>
      expect(window.location.pathname, 'sanity: the run must land on its own job path').toBe(`/extraction/${JOB_ID}`),
    )

    cleanup()
    capturedCtx = undefined
    // This file's afterEach carries no vi.restoreAllMocks() -- restore locally.
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    try {
      await renderApp(book)
      await waitFor(() => expect(requireCtx().extractionJobId, 'a reload must reopen the same job').toBe(JOB_ID))
      await waitFor(() =>
        expect(screen.queryByTestId('extraction-save'), 'control: the settled footer rendered').not.toBeNull(),
      )
      expect(screen.queryByTestId('extraction-open-invoice'), 'a cold reload holds no invoice id, so no exit').toBeNull()
      expect(errorSpy, 'a reload must not throw or log').not.toHaveBeenCalled()
    } finally {
      errorSpy.mockRestore()
    }
  })
})
