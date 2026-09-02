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
// a domain 201, not a 500. Independent parallel success needs a non-mock extractor (EXTR-17).
//
// Multipart lives in this file, not client.ts: apiFetch and rawFetch both force
// Content-Type: application/json (packages/api-client/src/client.ts:47-50, client.ts:40-45), the
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
import { apiBase, createEntity, getExtractions, login, PERSONAS, type ExtractionJob } from './client'
import { freshTin } from './fixtures'
import { assertErrorEnvelope, type RawResult } from './contract-helpers'

// e2e/ is ESM, so there is no __dirname, and a relative path would resolve against
// process.cwd() -- e2e/ under CI's `pnpm --filter`, the repo root for a developer.
// import.meta.url is the only cwd-independent anchor (topology/import-wizard.spec.ts:95).
const PDF_FIXTURE = join(dirname(fileURLToPath(import.meta.url)), '../fixtures/documents/native_invoice.pdf')
const PDF_BYTES = new Uint8Array(readFileSync(PDF_FIXTURE))

const UPLOAD_PATH = '/api/submission/v1/documents'
const IMPORT_DOCUMENT_PATH = '/api/invoice/v1/imports/document'
const PREVIEW_PATH = '/api/invoice/v1/imports/preview'

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
function uniquePdf(): Uint8Array<ArrayBuffer> {
  const marker = new TextEncoder().encode(`%e2e-${crypto.randomUUID()}\n`)
  const out = new Uint8Array(PDF_BYTES.length + marker.length)
  out.set(PDF_BYTES, 0)
  out.set(marker, PDF_BYTES.length)
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
  // No gate runs here, so RuleSetVersion stays nil. toBeNull() rejects a false `0` stamp too.
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
    expect(body.invoice_violations, 'DOCUP-02: no gate ran, so there is nothing to violate').toEqual([])
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
      // Both stay 0: ImportDocument runs no rule gate, so nothing was ever evaluated as clean.
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
})
