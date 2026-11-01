// The document pipeline over the deployed gateway: POST /api/submission/v1/documents ->
// GET /api/submission/v1/extractions -> POST /api/invoice/v1/imports/document. Each service has
// its own Go tests; the seam joining the three exists only here, so this file needs a deployed
// fleet and cannot pass locally.
//
// QA disposition A-2: Core AC #9 ("documents extract in parallel") is demonstrated here ONLY as
// collision-recovery. internal/extraction/mock.go stamps MOCK-INV-0001 on every document --
// `mockDefaultResult` and the `clean-invoice` fixture alike, held there by
// TestMockExtractor_InvoiceNumberIsUnchangedAndClean -- so two documents against one entity
// must collide; DOCUP-04 proves the loser quarantines as
// a domain 201, not a 500. Independent parallel success needs a non-mock extractor.
//
// EXTR-17 did NOT close this. It wired and provisioned the docling arm but left
// EXTRACTOR=mock deployed: flipping it reads native_invoice.pdf as 11 fields with zero line
// items, which breaks 12 of 36 extraction specs -- 11 of them losing coverage no fixture in
// the repo can restore (the EXTR-13 line grid, the EXTR-12 reason pills). Closing this needs
// a rich native fixture the tier-1 rules resolve into a multi-line ambiguous result, and that
// fixture is the real work. Until it exists the docling arm has no deployed oracle.
//
// Multipart lives in this file, not client.ts: apiFetch and rawFetch both force
// Content-Type: application/json (packages/api-client/src/client.ts), the
// precedent every shipped multipart spec follows (contract-import.spec.ts:5,47-50). The JSON
// seams stay local too -- the 4xx arms need the raw body, which apiFetch throws away.
//
// The 403 arm of AC-5 is deliberately absent: suspension.spec.ts owns the suspended-member wire
// and self-heals `memberships` in a finally, and a second copy of that mutation would leak into
// the topology suite that runs after this one.
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test, expect } from '@playwright/test'
import {
  apiBase,
  approveUntilClosed,
  createEntity,
  createInvoice,
  firmApproverTokens,
  getCarriedReading,
  getExtractionDetail,
  getExtractions,
  getInvoice,
  getInvoiceNotes,
  getNoteOriginals,
  listInvoices,
  login,
  PERSONAS,
  rawFetch,
  validateInvoice,
  type DocumentImportRequest,
  type ExtractionJob,
} from './client'
import { freshTin } from './fixtures'
import { assertErrorEnvelope, ensureFirmPolicyActive, type RawResult } from './contract-helpers'

// e2e/ is ESM, so there is no __dirname, and a relative path would resolve against
// process.cwd() -- e2e/ under CI's `pnpm --filter`, the repo root for a developer.
// import.meta.url is the only cwd-independent anchor (topology/import-wizard.spec.ts:95).
const PDF_FIXTURE = join(dirname(fileURLToPath(import.meta.url)), '../fixtures/documents/native_invoice.pdf')
const PDF_BYTES = new Uint8Array(readFileSync(PDF_FIXTURE))
// Reads as a credit note of INV-ORIG-3401, number CN-3401, 100.00 / 7.50 / 107.50 over 2 lines.
const NOTE_BYTES = new Uint8Array(
  readFileSync(join(dirname(fileURLToPath(import.meta.url)), '../fixtures/documents/jev_credit_note.pdf')),
)

const UPLOAD_PATH = '/api/submission/v1/documents'
const IMPORT_DOCUMENT_PATH = '/api/invoice/v1/imports/document'
const PREVIEW_PATH = '/api/invoice/v1/imports/preview'
const READING_PATH = '/api/invoice/v1/imports/document/reading'
const SUPPLY_PATH = '/api/invoice/v1/imports/document/invoice'

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/

// The mock extractor's work is sub-second; the worker's own Timeout is 10 minutes. 120 s sits
// between them: long enough that a cold fleet does not flake, short enough to fail well inside
// the River job's own deadline. TEST_TIMEOUT_MS must exceed it -- the api config's own timeout
// is 60 s (playwright.api.config.ts).
const POLL_BUDGET_MS = 120_000
const POLL_INTERVAL_MS = 1_000
const TEST_TIMEOUT_MS = 180_000

// importResponse's complete key set (internal/importer/handlers.go:71-89), sorted. Asserted as
// a WHOLE: an added `omitempty` drops a field from the wire without touching one value
// assertion. `id`/`status` carry omitempty but are always populated -- the batch is minted first.
const IMPORT_REPORT_KEYS = [
  'delimiter',
  'encoding',
  'errors',
  'format',
  'id',
  'invoice_violations',
  'invoices_clean',
  'invoices_with_violations',
  'quarantined_invoices',
  'ready_invoices',
  'rows_invalid',
  'rows_total',
  'rows_valid',
  'rule_set_version',
  'status',
]

// internal/importer/service.go:507 -- the store-level duplicate's named rule.
const DUPLICATE_RULE_KEY = 'no-duplicate-invoice-number'

// The fixture plus a trailing PDF comment: it changes the content hash without moving any byte
// offset, so `startxref` still resolves and the worker's pdfium page render (Pages.Ingest, which
// runs whatever EXTRACTOR is set to) still sees the same page. Without a fresh hash, per-tenant
// dedupe reuses the old row and the PERMANENT per-document enqueue key
// (internal/extraction/enqueue.go) skips the enqueue -- the poll would then settle on a PREVIOUS
// run's job and stay green while extraction is broken.
// <ArrayBuffer> is load-bearing: bare Uint8Array widens to ArrayBufferLike, which BlobPart rejects.
function uniquePdf(base: Uint8Array = PDF_BYTES): Uint8Array<ArrayBuffer> {
  const marker = new TextEncoder().encode(`%e2e-${crypto.randomUUID()}\n`)
  const out = new Uint8Array(base.length + marker.length)
  out.set(base, 0)
  out.set(marker, base.length)
  return out
}

// asRawResult(): adapts fetch's Response into the shape assertErrorEnvelope reads.
async function asRawResult(res: Response): Promise<RawResult> {
  let body: unknown
  try {
    body = await res.json()
  } catch {
    body = undefined
  }
  return { status: res.status, body }
}

function authHeaders(token: string | null): Record<string, string> {
  return token ? { Authorization: `Bearer ${token}` } : {}
}

// Never set Content-Type: fetch derives the multipart boundary from the FormData body.
// token=null omits Authorization entirely (the 401 arm).
async function uploadFetch(token: string | null, body: BlobPart, filename: string, type: string): Promise<RawResult> {
  const form = new FormData()
  form.set('file', new Blob([body], { type }), filename)
  return asRawResult(
    await fetch(`${apiBase()}${UPLOAD_PATH}`, { method: 'POST', headers: authHeaders(token), body: form }),
  )
}

// JSON, not multipart -- the bytes are already stored.
async function importDocumentFetch(token: string | null, body: unknown): Promise<RawResult> {
  return asRawResult(
    await fetch(`${apiBase()}${IMPORT_DOCUMENT_PATH}`, {
      method: 'POST',
      headers: { ...authHeaders(token), 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  )
}

// The spreadsheet route's upload, used ONLY as DOCUP-06's contrast: it stores before it decodes,
// so its 400 carries document_id ([error-body-carries-document-id]).
async function previewFetch(token: string, body: BlobPart, filename: string, type: string): Promise<RawResult> {
  const form = new FormData()
  form.set('file', new Blob([body], { type }), filename)
  return asRawResult(
    await fetch(`${apiBase()}${PREVIEW_PATH}`, { method: 'POST', headers: authHeaders(token), body: form }),
  )
}

// EXTR27-API-02's raw seams: the reading and supply routes are asserted on their raw body
// (400/404 error text), which apiFetch's typed wrapper throws away.
async function readingFetch(token: string | null, query: string): Promise<RawResult> {
  return asRawResult(
    await fetch(`${apiBase()}${READING_PATH}?${query}`, { headers: authHeaders(token) }),
  )
}

async function supplyFetch(token: string | null, body: unknown): Promise<RawResult> {
  return asRawResult(
    await fetch(`${apiBase()}${SUPPLY_PATH}`, {
      method: 'POST',
      headers: { ...authHeaders(token), 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }),
  )
}

// A fresh document, with the 201 body asserted on the way through.
async function uploadPdf(token: string, label: string): Promise<{ documentId: string; bytes: Uint8Array<ArrayBuffer> }> {
  const bytes = uniquePdf()
  const res = await uploadFetch(token, bytes, 'native_invoice.pdf', 'application/pdf')
  expect(res.status, `${label}: a PDF upload should return 201 (body ${JSON.stringify(res.body)})`).toBe(201)
  const body = res.body as Record<string, unknown>
  expect(Object.keys(body).sort(), `${label}: the 201 body is the whole uploadResponse`).toEqual([
    'content_type',
    'document_id',
    'filename',
    'reused',
    'size_bytes',
  ])
  expect(body.document_id as string, `${label}: document_id`).toMatch(UUID_RE)
  expect(body.filename, `${label}: the part filename survives sanitization unchanged`).toBe('native_invoice.pdf')
  expect(body.content_type, `${label}: the CANONICAL type is recorded, not the declared one`).toBe('application/pdf')
  expect(body.size_bytes, `${label}: size_bytes is the byte count actually sent`).toBe(bytes.length)
  expect(body.reused, `${label}: uniquePdf() bytes have never been stored, so this is a new row`).toBe(false)
  return { documentId: body.document_id as string, bytes }
}

// Polls to a `succeeded` job, or THROWS. Neither an empty jobs[] (vacuous for every find())
// nor budget expiry may fall through to a pass: the empty case only ever continues the loop,
// the function returns a JOB rather than a boolean, and the loop exits by throw. `failed` is
// not terminal -- River retries it (internal/extraction/worker.go:141-143) -- so only
// `dead_lettered` short-circuits.
async function pollUntilSucceeded(token: string, documentId: string, label: string): Promise<ExtractionJob> {
  const deadline = Date.now() + POLL_BUDGET_MS
  let seen = 'nothing observed'
  for (;;) {
    const { jobs } = await getExtractions(token, documentId)
    if (jobs.length === 0) {
      seen = 'jobs: [] (no extraction was ever enqueued for this document)'
    } else {
      seen = jobs.map((j) => `${j.id}=${j.state}${j.last_error ? ` last_error=${j.last_error}` : ''}`).join(', ')
      const dead = jobs.find((j) => j.state === 'dead_lettered')
      if (dead) {
        throw new Error(`${label}: extraction dead-lettered -- ${seen}`)
      }
      const succeeded = jobs.find((j) => j.state === 'succeeded')
      if (succeeded) {
        return succeeded
      }
    }
    if (Date.now() >= deadline) {
      throw new Error(`${label}: no extraction reached "succeeded" within ${POLL_BUDGET_MS} ms -- last seen ${seen}`)
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS))
  }
}

// upload -> poll -> a document ready to import.
async function settleDocument(token: string, label: string): Promise<string> {
  const { documentId } = await uploadPdf(token, label)
  await pollUntilSucceeded(token, documentId, label)
  return documentId
}

// The field-for-field ImportReport assertion, applied to every 201 here -- the quarantined one
// included, so a divergent shape on the unhappy path cannot hide.
function expectImportReportShape(body: Record<string, unknown>, label: string): void {
  expect(Object.keys(body).sort(), `${label}: every ImportReport field must be present`).toEqual(IMPORT_REPORT_KEYS)
  expect(body.format, `${label}: format`).toBe('document')
  // A nil *string without omitempty renders as an explicit null, and the previewer must tell
  // null apart from absent.
  expect(body.delimiter, `${label}: delimiter is an explicit null, never "" and never absent`).toBeNull()
  expect(body.encoding, `${label}: encoding is an explicit null, never "" and never absent`).toBeNull()
  // RuleSetVersion stays nil. toBeNull() rejects a false `0` stamp too.
  expect(body.rule_set_version, `${label}: rule_set_version is null -- not 0, not absent`).toBeNull()
  expect(Array.isArray(body.errors), `${label}: errors marshals as an array, never as null`).toBe(true)
  expect(Array.isArray(body.invoice_violations), `${label}: invoice_violations is an array, never null`).toBe(true)
  expect(body.id as string, `${label}: id is the minted batch`).toMatch(UUID_RE)
  expect(body.status, `${label}: a document import has no dry-run branch, so it always completes`).toBe('completed')
  for (const key of ['rows_total', 'rows_valid', 'rows_invalid', 'ready_invoices', 'quarantined_invoices', 'invoices_clean', 'invoices_with_violations']) {
    expect(typeof body[key], `${label}: ${key} should be numeric`).toBe('number')
  }
  expect(body.rows_total, `${label}: one document is one row (D-5)`).toBe(1)
}

test.describe('document pipeline contract (API E2E, over the deployed gateway)', () => {
  let token: string

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
  })

  test('DOCUP-01: a PDF upload settles succeeded', async () => {
    test.setTimeout(TEST_TIMEOUT_MS)
    const { documentId } = await uploadPdf(token, 'DOCUP-01')

    const job = await pollUntilSucceeded(token, documentId, 'DOCUP-01')
    expect(job.state, 'DOCUP-01: the settled job').toBe('succeeded')
    expect(job.document_id, 'DOCUP-01: the job belongs to the document just uploaded').toBe(documentId)
    expect(job.id, 'DOCUP-01: job id').toMatch(UUID_RE)
    expect(job.last_error, 'DOCUP-01: a succeeded job carries no error').toBeNull()
    // EXTR-15-01 AC-4, deployed: JobState's failure_kind tag carries no omitempty, so the key
    // is present and null on a job that never failed. The key set is the only oracle that
    // catches an absent key -- a `toBeNull()` alone reads the same on undefined.
    expect(Object.keys(job).sort(), 'DOCUP-01: the JobState key set drifted from internal/extraction/reader.go').toEqual(
      ['created_at', 'document_id', 'failure_kind', 'id', 'last_error', 'state'].sort(),
    )
    expect(job.failure_kind, 'DOCUP-01: a succeeded job reports a null failure_kind').toBeNull()

    // An independent re-read pinning the list itself as non-empty -- an empty jobs[] satisfies
    // every find() above vacuously.
    const { jobs } = await getExtractions(token, documentId)
    expect(jobs.length, 'DOCUP-01: the document must carry at least one job').toBeGreaterThan(0)
    expect(jobs.map((j) => j.state), 'DOCUP-01: the settled state is visible on a fresh read').toContain('succeeded')
  })

  test('DOCUP-02: a settled document imports as one batch', async () => {
    test.setTimeout(TEST_TIMEOUT_MS)
    const entity = await createEntity(token, { name: `EXTR-09 doc ${freshTin()}`, tin: freshTin() })
    const documentId = await settleDocument(token, 'DOCUP-02')

    const res = await importDocumentFetch(token, { entity_id: entity.id, document_id: documentId })
    expect(res.status, `DOCUP-02: a settled document imports 201 (body ${JSON.stringify(res.body)})`).toBe(201)
    const body = res.body as Record<string, unknown>
    expectImportReportShape(body, 'DOCUP-02')
    expect(body.rows_valid, 'DOCUP-02: the single row read cleanly').toBe(1)
    expect(body.rows_invalid, 'DOCUP-02: nothing was unreadable').toBe(0)
    expect(body.ready_invoices, 'DOCUP-02: one draft invoice landed').toBe(1)
    expect(body.quarantined_invoices, 'DOCUP-02: a fresh entity cannot collide').toBe(0)
    expect(body.errors, 'DOCUP-02: a clean import reports no row errors').toEqual([])
    expect(body.invoice_violations, 'DOCUP-02: the import reports no violations').toEqual([])
  })

  test('DOCUP-03: the 201 body is a whole ImportReport', async () => {
    test.setTimeout(TEST_TIMEOUT_MS)
    const entity = await createEntity(token, { name: `EXTR-09 shape ${freshTin()}`, tin: freshTin() })
    const documentId = await settleDocument(token, 'DOCUP-03')

    const res = await importDocumentFetch(token, { entity_id: entity.id, document_id: documentId })
    expect(res.status, `DOCUP-03: needs a 201 to read a body off (body ${JSON.stringify(res.body)})`).toBe(201)
    const body = res.body as Record<string, unknown>

    // What makes this the SHAPE test rather than a repeat of DOCUP-02: every counter pinned to
    // its exact value, in one object, rather than to `typeof number`.
    expectImportReportShape(body, 'DOCUP-03')
    expect(
      {
        rows_total: body.rows_total,
        rows_valid: body.rows_valid,
        rows_invalid: body.rows_invalid,
        ready_invoices: body.ready_invoices,
        quarantined_invoices: body.quarantined_invoices,
        invoices_clean: body.invoices_clean,
        invoices_with_violations: body.invoices_with_violations,
      },
      'DOCUP-03: the full counter block of a clean single-document import',
    ).toEqual({
      rows_total: 1,
      rows_valid: 1,
      rows_invalid: 0,
      ready_invoices: 1,
      quarantined_invoices: 0,
      // Both stay 0.
      invoices_clean: 0,
      invoices_with_violations: 0,
    })
  })

  test('DOCUP-04: two concurrent imports give two batches and one duplicate', async () => {
    test.setTimeout(TEST_TIMEOUT_MS)
    // ONE entity on purpose: the mock stamps MOCK-INV-0001 on both documents, so the invoices
    // (entity, number) unique constraint MUST fire.
    const entity = await createEntity(token, { name: `EXTR-09 race ${freshTin()}`, tin: freshTin() })
    const [firstDocument, secondDocument] = await Promise.all([
      settleDocument(token, 'DOCUP-04 doc A'),
      settleDocument(token, 'DOCUP-04 doc B'),
    ])
    expect(secondDocument, 'DOCUP-04: two distinct documents, not one deduped row').not.toBe(firstDocument)

    const results = await Promise.all([
      importDocumentFetch(token, { entity_id: entity.id, document_id: firstDocument }),
      importDocumentFetch(token, { entity_id: entity.id, document_id: secondDocument }),
    ])

    for (const [i, res] of results.entries()) {
      expect(
        res.status,
        `DOCUP-04: import ${i} must be a 201 -- a collision is a DOMAIN outcome, never a 500 (body ${JSON.stringify(res.body)})`,
      ).toBe(201)
    }
    const bodies = results.map((r) => r.body as Record<string, unknown>)
    bodies.forEach((b, i) => expectImportReportShape(b, `DOCUP-04 import ${i}`))

    expect(new Set(bodies.map((b) => b.id)).size, 'DOCUP-04: each import mints its OWN batch').toBe(2)

    const ready = bodies.filter((b) => b.ready_invoices === 1)
    const quarantined = bodies.filter((b) => b.quarantined_invoices === 1)
    // Asserted BEFORE either list is indexed -- an empty list satisfies nothing below.
    expect(ready.length, 'DOCUP-04: exactly one import may land the invoice').toBe(1)
    expect(quarantined.length, 'DOCUP-04: exactly one import must quarantine on the duplicate').toBe(1)

    expect(ready[0].rows_valid, 'DOCUP-04: the winner read its row').toBe(1)
    expect(ready[0].rows_invalid, 'DOCUP-04: the winner has nothing invalid').toBe(0)
    expect(ready[0].quarantined_invoices, 'DOCUP-04: the winner quarantined nothing').toBe(0)
    expect(ready[0].errors, 'DOCUP-04: the winner reports no row errors').toEqual([])

    const loser = quarantined[0]
    expect(loser.ready_invoices, 'DOCUP-04: the loser landed nothing').toBe(0)
    expect(loser.rows_valid, 'DOCUP-04: the loser read no valid row').toBe(0)
    expect(loser.rows_invalid, 'DOCUP-04: the loser quarantined its one row').toBe(1)
    expect(loser.errors, 'DOCUP-04: the duplicate is a STRUCTURAL error carrying a named rule').toContainEqual(
      expect.objectContaining({
        rule_key: DUPLICATE_RULE_KEY,
        severity: 'error',
        field: 'invoice_number',
      }),
    )
    // Core AC#5's NEVER MIX invariant. invoice_id is deliberately NOT asserted: ExistingNumbers
    // can run before the winner commits, leaving it "" and the omitempty key absent -- correct
    // behaviour in a genuine race.
    expect(loser.invoice_violations, 'DOCUP-04: a structural error never doubles as a violation').toEqual([])
  })

  test('DOCUP-05: refused type, no auth, oversized', async () => {
    test.setTimeout(TEST_TIMEOUT_MS)
    const bytes = uniquePdf()

    // Positive control FIRST: without it, the refusals below are equally satisfied by a route
    // that refuses everyone.
    const control = await uploadFetch(token, bytes, 'native_invoice.pdf', 'application/pdf')
    expect(control.status, 'DOCUP-05: the control upload must be accepted').toBe(201)

    // A spreadsheet is refused here even though the fleet reads one elsewhere: it belongs to
    // POST /v1/imports/preview. The picker's fork, asserted on the wire.
    assertErrorEnvelope(await uploadFetch(token, 'a,b\n1,2\n', 'ledger.csv', 'text/csv'), 400, 'DOCUP-05 a csv')
    assertErrorEnvelope(
      await uploadFetch(token, bytes, 'evidence.zip', 'application/zip'),
      400,
      'DOCUP-05 an unreadable type',
    )
    // Identity is checked ABOVE the size cap, so a stranger's bytes are never read.
    assertErrorEnvelope(await uploadFetch(null, bytes, 'native_invoice.pdf', 'application/pdf'), 401, 'DOCUP-05 no auth')
    // 16 MiB > the 15 MiB cap. The oversized part IS the `file` part and is named .pdf, so size
    // is the only reason left -- classification would have accepted it.
    assertErrorEnvelope(
      await uploadFetch(token, new Uint8Array(16 * 1024 * 1024), 'big.pdf', 'application/pdf'),
      413,
      'DOCUP-05 oversized',
    )
  })

  test('DOCUP-06: the refused upload is not retrievable', async () => {
    const bytes = uniquePdf()

    const refused = await uploadFetch(token, bytes, 'evidence.zip', 'application/zip')
    assertErrorEnvelope(refused, 400, 'DOCUP-06 refused upload')
    // assertErrorEnvelope already pins the key set to ['error']; this names the defect on failure.
    expect(
      'document_id' in (refused.body as Record<string, unknown>),
      'DOCUP-06: this route classifies BEFORE it stores, so a refused upload names no document',
    ).toBe(false)

    // The contrast, same bytes and same filename: preview stores before it decodes, so its 400
    // must name the row it wrote (contract-import.spec.ts:221,312-321 owns that pin).
    const preview = await previewFetch(token, bytes, 'evidence.zip', 'application/zip')
    expect(preview.status, 'DOCUP-06: preview refuses the same file').toBe(400)
    expect(
      typeof (preview.body as Record<string, unknown>).document_id,
      'DOCUP-06: preview stored first, so its 400 carries document_id -- the behaviour this route deliberately does NOT share',
    ).toBe('string')
  })

  test('EXTR27-API-02: a document with nothing to carry reads null, and a supply against it is refused', async () => {
    const entity = await createEntity(token, { name: `EXTR-27 supply ${freshTin()}`, tin: freshTin() })
    const unknown = crypto.randomUUID()

    expect(
      await getCarriedReading(token, unknown),
      'EXTR27-API-02: the typed mirror over the wire',
    ).toBeNull()

    const positive = await readingFetch(token, `document_id=${unknown}`)
    expect(positive.status, 'EXTR27-API-02: a document with no reading still answers 200').toBe(200)
    expect(Object.keys(positive.body as Record<string, unknown>)).toEqual(['reading'])
    expect((positive.body as Record<string, unknown>).reading).toBeNull()

    const malformed = await readingFetch(token, 'document_id=nope')
    assertErrorEnvelope(malformed, 400, 'EXTR27-API-02 malformed document_id')
    expect((malformed.body as Record<string, unknown>).error).toBe('document_id must be a well-formed uuid')

    assertErrorEnvelope(await readingFetch(null, `document_id=${unknown}`), 401, 'EXTR27-API-02 no auth')

    const supplyRes = await supplyFetch(token, {
      entity_id: entity.id,
      document_id: unknown,
      invoice_number: `EXTR27-${freshTin()}`,
    })
    // The importer's JSON envelope, never the mux's plain-text 404 for an unregistered route.
    assertErrorEnvelope(supplyRes, 404, 'EXTR27-API-02: a document with no succeeded extraction')
    expect((supplyRes.body as Record<string, unknown>).error).toBe('not found')

    const blankSupply = await supplyFetch(token, { entity_id: entity.id, document_id: unknown, invoice_number: '   ' })
    assertErrorEnvelope(blankSupply, 400, 'EXTR27-API-02 blank invoice_number')
    expect((blankSupply.body as Record<string, unknown>).error).toBe('invoice_number is required')
  })
})

// internal/submission/mock_script.go mockTINAccept: the buyer TIN the mock adapter accepts.
const MOCK_TIN_ACCEPT = '99999999-0001'
const NOTE_READ = { original: 'INV-ORIG-3401', number: 'CN-3401', total: 107.5, lines: 2 }
// internal/importer/handlers_document.go documentAlreadyFiledReason.
const ALREADY_FILED_REASON = 'An invoice has already been filed from this document.'
// internal/invoice/notes.go NoteRefusalMessage.
const OTHER_CLIENT_REASON = 'the original belongs to another client'
const OVER_CAP_KEY = 'credit-note-over-cap'

type Bearer = { Authorization: string }
const bearer = (tok: string): Bearer => ({ Authorization: `Bearer ${tok}` })

async function postImport(tok: string, body: DocumentImportRequest | Record<string, unknown>) {
  return rawFetch(IMPORT_DOCUMENT_PATH, { method: 'POST', headers: bearer(tok), body })
}

// The recipe in contract-invoice.spec.ts acceptedOriginal (no cross-spec imports). `small` keeps the
// total under the fixture note's 107.50.
async function acceptedOriginal(tok: string, entityId: string, number: string, small = false) {
  const created = await createInvoice(tok, {
    entity_id: entityId,
    invoice_number: number,
    issue_date: '2026-01-01T00:00:00Z',
    supplier_tin: freshTin(),
    supplier_name: 'Acme Nigeria Ltd',
    buyer_tin: MOCK_TIN_ACCEPT,
    buyer_name: 'Buyer Ltd',
    currency: 'NGN',
    ...(small
      ? { subtotal: '90', vat: '6.75', total: '96.75', line_items: [{ description: 'Widget', quantity: '1', unit_price: '90', line_total: '90' }] }
      : {
          subtotal: '1000',
          vat: '75',
          total: '1075',
          line_items: [
            { description: 'Line One', quantity: '2', unit_price: '250', line_total: '500' },
            { description: 'Line Two', quantity: '5', unit_price: '100', line_total: '500' },
          ],
        }),
  })
  expect((await validateInvoice(tok, created.id)).status, 'the original validates clean').toBe('validated')
  await approveUntilClosed(created.id, await firmApproverTokens())
  const sub = await rawFetch('/api/invoice/v1/invoices/submissions', {
    method: 'POST',
    headers: bearer(tok),
    body: { invoice_ids: [created.id], idempotency_key: crypto.randomUUID() },
  })
  expect(sub.status, 'batch submit').toBe(200)
  await expect
    .poll(async () => (await getInvoice(tok, created.id)).status, { timeout: 120_000, intervals: [1_000] })
    .toBe('accepted')
  return getInvoice(tok, created.id)
}

// Unique fixture bytes -> upload -> settled. Returns the document and the job it settled on.
async function settleNote(tok: string, label: string): Promise<{ documentId: string; job: ExtractionJob }> {
  const res = await uploadFetch(tok, uniquePdf(NOTE_BYTES), 'jev_credit_note.pdf', 'application/pdf')
  expect(res.status, `${label}: the fixture uploads 201 (body ${JSON.stringify(res.body)})`).toBe(201)
  const documentId = (res.body as { document_id: string }).document_id
  return { documentId, job: await pollUntilSucceeded(tok, documentId, label) }
}

// A fresh entity with its accepted original INV-ORIG-3401 and a settled fixture document.
async function noteFixture(tok: string, label: string, small = false) {
  const entity = await createEntity(tok, { name: `Zz CRDDBTNOTE-05 ${label} ${freshTin()}`, tin: freshTin() })
  const original = await acceptedOriginal(tok, entity.id, NOTE_READ.original, small)
  const { documentId, job } = await settleNote(tok, label)
  return { entity, original, documentId, job }
}

// The invoices of the (fresh) entity: containment is the entity, never a count of the table.
async function entityInvoices(tok: string, entityId: string) {
  return (await listInvoices(tok, { entity_id: entityId, limit: 50 })).invoices
}

test.describe('note documents', () => {
  test.describe.configure({ timeout: 300_000 })
  let token: string

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
    await ensureFirmPolicyActive(token)
  })

  test('note documents: the fixture reads as a credit note', async () => {
    const { documentId, job } = await settleNote(token, 'note-read')
    const detail = await getExtractionDetail(token, job.id)
    expect(detail.document_id, 'the detail is for the document just uploaded').toBe(documentId)
    expect(detail.document_type).toBe('credit note')
    const field = detail.fields.find((f) => f.name === 'original_invoice_number')
    expect(field?.value, 'the original is read off the document').toBe(NOTE_READ.original)
  })

  test('note documents: the first import holds and creates nothing', async () => {
    const { entity, original, documentId } = await noteFixture(token, 'hold')

    const res = await postImport(token, { entity_id: entity.id, document_id: documentId })
    expect(res.status, `the hold is a 201 (body ${JSON.stringify(res.body)})`).toBe(201)
    const body = res.body as Record<string, unknown>
    expectImportReportShape(body, 'hold')
    expect(body.ready_invoices).toBe(0)
    expect(body.errors).toContainEqual(expect.objectContaining({ field: 'original_invoice_id' }))

    const invoices = await entityInvoices(token, entity.id)
    expect(invoices.map((i) => i.id), 'the entity still holds only its original').toEqual([original.id])
    expect(invoices.filter((i) => i.import_batch_id === body.id), 'no invoice cites the hold batch').toEqual([])
  })

  test('note documents: the picker lists the accepted original first', async () => {
    const entity = await createEntity(token, { name: `Zz CRDDBTNOTE-05 picker ${freshTin()}`, tin: freshTin() })
    // The read original goes first, so newest-first alone would put the other one on top.
    const original = await acceptedOriginal(token, entity.id, NOTE_READ.original)
    const other = await acceptedOriginal(token, entity.id, `INV-OTHER-${freshTin()}`)
    const draft = await createInvoice(token, {
      entity_id: entity.id,
      invoice_number: `INV-DRAFT-${freshTin()}`,
      issue_date: '2026-01-01T00:00:00Z',
      currency: 'NGN',
    })
    const { documentId } = await settleNote(token, 'picker')
    const hold = await postImport(token, { entity_id: entity.id, document_id: documentId })
    expect(hold.status, 'the hold mints the batch the picker reads its entity from').toBe(201)

    const got = await getNoteOriginals(token, documentId)
    expect(got.entity_id).toBe(entity.id)
    expect(got.filed_invoice_id).toBeNull()
    const ids = got.originals.map((o) => o.id)
    expect(ids, 'the accepted originals, the read one first').toEqual([original.id, other.id])
    expect(ids, 'a draft is no original').not.toContain(draft.id)
    expect(got.originals[0].invoice_number).toBe(NOTE_READ.original)
  })

  test('note documents: no original and a foreign original create nothing', async () => {
    const { entity, original, documentId } = await noteFixture(token, 'refusals')
    const tokenB = await login(PERSONAS.B)
    const entityB = await createEntity(tokenB, { name: `Zz CRDDBTNOTE-05 foreign ${freshTin()}`, tin: freshTin() })
    const foreign = await createInvoice(tokenB, {
      entity_id: entityB.id,
      invoice_number: `INV-B-${freshTin()}`,
      issue_date: '2026-01-01T00:00:00Z',
      currency: 'NGN',
    })

    const base = { entity_id: entity.id, document_id: documentId, document_type: 'credit_note', invoice_number: NOTE_READ.number }
    const missing = await postImport(token, base)
    assertErrorEnvelope(missing, 400, 'no original_invoice_id')
    expect((missing.body as { error: string }).error).toBe('original_invoice_id is required')
    assertErrorEnvelope(await postImport(token, { ...base, original_invoice_id: foreign.id }), 404, 'a foreign original')

    expect((await entityInvoices(token, entity.id)).map((i) => i.id), 'neither refusal created an invoice').toEqual([original.id])
    expect((await getInvoiceNotes(token, original.id)).notes, 'nor a note').toEqual([])
  })

  test('note documents: the import creates a draft note on its original', async () => {
    const { entity, original, documentId } = await noteFixture(token, 'import')

    const res = await postImport(token, {
      entity_id: entity.id,
      document_id: documentId,
      document_type: 'credit_note',
      original_invoice_id: original.id,
      invoice_number: NOTE_READ.number,
    })
    expect(res.status, `the note POST is a 201 (body ${JSON.stringify(res.body)})`).toBe(201)
    expect((res.body as { ready_invoices: number }).ready_invoices).toBe(1)

    const note = (await entityInvoices(token, entity.id)).find((i) => i.document_type === 'credit_note')
    expect(note, 'GET /v1/invoices finds the note').toBeDefined()
    expect(note!.status).toBe('draft')
    expect(note!.original_invoice_id).toBe(original.id)
    expect(note!.invoice_number).toBe(NOTE_READ.number)
    expect(Number(note!.total), 'the total is the read total').toBe(NOTE_READ.total)
    expect((await getInvoice(token, note!.id)).line_items, 'the read lines').toHaveLength(NOTE_READ.lines)

    expect((await getInvoiceNotes(token, original.id)).notes.map((n) => n.id), 'the original lists it').toContain(note!.id)
    expect((await getNoteOriginals(token, documentId)).filed_invoice_id, 'the picker now names it').toBe(note!.id)
  })

  test('note documents: an original of another client is refused', async () => {
    const { entity, documentId } = await noteFixture(token, 'other-client')
    const entityB = await createEntity(token, { name: `Zz CRDDBTNOTE-05 client B ${freshTin()}`, tin: freshTin() })
    const originalB = await acceptedOriginal(token, entityB.id, NOTE_READ.original)

    const res = await postImport(token, {
      entity_id: entity.id,
      document_id: documentId,
      document_type: 'credit_note',
      original_invoice_id: originalB.id,
      invoice_number: NOTE_READ.number,
    })
    assertErrorEnvelope(res, 409, 'an original of another client')
    expect((res.body as { error: string }).error).toBe(OTHER_CLIENT_REASON)
    expect((await entityInvoices(token, entity.id)).filter((i) => i.document_type === 'credit_note')).toEqual([])
  })

  test('note documents: an over-cap note stays draft at validate', async () => {
    const { entity, original, documentId } = await noteFixture(token, 'over-cap', true)
    expect(Number(original.total), 'the original is smaller than the note').toBeLessThan(NOTE_READ.total)

    const res = await postImport(token, {
      entity_id: entity.id,
      document_id: documentId,
      document_type: 'credit_note',
      original_invoice_id: original.id,
      invoice_number: NOTE_READ.number,
    })
    expect(res.status, `the over-cap note still imports (body ${JSON.stringify(res.body)})`).toBe(201)
    const note = (await entityInvoices(token, entity.id)).find((i) => i.document_type === 'credit_note')
    expect(note, 'the draft note exists').toBeDefined()

    const validated = await validateInvoice(token, note!.id)
    expect(validated.status).toBe('draft')
    expect(validated.violations.map((v) => v.rule_key)).toContain(OVER_CAP_KEY)
  })

  test('note documents: this is an invoice files an invoice', async () => {
    const { entity, original, documentId } = await noteFixture(token, 'override')

    const res = await postImport(token, { entity_id: entity.id, document_id: documentId, document_type: 'commercial_invoice' })
    expect(res.status, `the override is a 201 (body ${JSON.stringify(res.body)})`).toBe(201)
    expect((res.body as { ready_invoices: number }).ready_invoices).toBe(1)

    const filed = (await entityInvoices(token, entity.id)).filter((i) => i.id !== original.id)
    expect(filed, 'exactly the one invoice was filed').toHaveLength(1)
    expect(filed[0].document_type).toBe('commercial_invoice')
    expect(filed[0].original_invoice_id).toBeNull()
  })

  test('note documents: a filed document does not hold or file again', async () => {
    const { entity, original, documentId } = await noteFixture(token, 'filed')
    const filedRes = await postImport(token, {
      entity_id: entity.id,
      document_id: documentId,
      document_type: 'credit_note',
      original_invoice_id: original.id,
      invoice_number: NOTE_READ.number,
    })
    expect(filedRes.status, 'the setup note files').toBe(201)
    const before = (await entityInvoices(token, entity.id)).map((i) => i.id).sort()
    const note = (await entityInvoices(token, entity.id)).find((i) => i.document_type === 'credit_note')
    expect(note, 'the note exists').toBeDefined()

    const bare = await postImport(token, { entity_id: entity.id, document_id: documentId })
    expect(bare.status, `a bare POST on a filed document is a 201 (body ${JSON.stringify(bare.body)})`).toBe(201)
    const body = bare.body as { ready_invoices: number; errors: Record<string, unknown>[] }
    expect(body.ready_invoices).toBe(0)
    expect(body.errors).toContainEqual(
      expect.objectContaining({
        field: 'invoice_number',
        rule_key: 'no-duplicate-invoice-number',
        invoice_id: note!.id,
        message: ALREADY_FILED_REASON,
      }),
    )

    const override = await postImport(token, { entity_id: entity.id, document_id: documentId, document_type: 'commercial_invoice' })
    assertErrorEnvelope(override, 409, 'commercial_invoice on a filed document')
    expect((override.body as { error: string }).error).toBe(ALREADY_FILED_REASON)

    expect((await entityInvoices(token, entity.id)).map((i) => i.id).sort(), 'neither call added an invoice').toEqual(before)
  })
})
