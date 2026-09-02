// EXTR-07-05: the extraction job reader over the deployed gateway. This is the SPA fleet's
// FIRST /api/submission/* caller, so the gateway hop to the submission binary is genuinely
// unproven until this file runs.
//
// WHAT THIS LAYER OBSERVES — reachability, and nothing beyond it:
//   1. THE ROUTE ITSELF. cmd/submission/main.go:160 registers `GET /v1/extractions` on
//      app.Mux, and nothing in the Go tree reads that pattern — the handler tests call
//      extraction.JobsHandler directly against a synthetic httptest request. Misspell the
//      path or change the METHOD and go build, go vet and the whole internal/extraction
//      suite stay green. Here it fails: an unregistered path answers with ServeMux's
//      plain-text 404, which cannot carry the 400 + {error} envelope asserted below.
//   2. The gateway hop: /api/submission/* is stripped and forwarded (internal/gateway/
//      gateway.go:61), which nothing else in the fleet exercises.
//   3. JWT -> tenant -> RLS end to end. The Go RLS tests set app.current_tenant themselves;
//      here the tenant arrives from a real token through the real gateway.
//
// WHAT THIS FILE REFUSES TO ASSERT. It makes no claim about any stage, start, progress or
// outcome of an extraction, because it cannot: nothing in this repository enqueues extraction
// work — `extractArgs` is unexported with no caller outside internal/extraction — so no spec
// can cause a real job to exist, and `queued` is structurally unobservable anyway (the worker
// inserts the row and advances it to `extracting` inside ONE transaction). This file can
// therefore only ever see {"jobs":[]}, and an empty list is evidence of a working round trip,
// never of a job's behaviour. Every state, ordering, cap and error-surface claim lives in the
// Go DB-backed suite (internal/extraction/reader_db_test.go). Do not cite this file as
// evidence for the substance of the story's stage-reporting criterion — it proves the
// endpoint is reachable and correctly refuses bad input, and that is all it proves.
//
// NO COUNT IS ASSERTED, and every case but EXTR12-API-02 creates no row: its document id and
// job id are a per-run crypto.randomUUID() that matches nothing. That makes those cases
// trivially safe in the shared run where smoke, api and topology hit ONE deployment with no
// reset between them (docs/e2e-convention.md:66-85). EXTR-11-09 added the detail and page routes
// below under that same rule: Reader.Detail raises ErrNotFound from detailTx BEFORE the audit
// recorder runs (reader.go:164-185), so even the 404 arm writes nothing and its transaction
// rolls back.
//
// EXTR12-API-02 is the one exception and it is deliberate: a 201 from the correction POST needs
// a real job, a real invoice and a real correction row, so it uploads its own document under its
// own per-run entity, the contract-document-upload.spec.ts idiom. It asserts no count and
// touches nothing another spec seeded.
//
// EXTR-11-09 · REFUSALS ONLY, for both new routes. A 200 from either needs a settled job, which
// nothing in this layer can produce without breaking the paragraph above -- so the settled-body
// assertions live in e2e/topology/import-wizard.spec.ts as EXTR11-E2E-04/04b, off the response
// the SPA itself consumed.
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { test, expect } from '@playwright/test'
import {
  ApiError,
  apiBase,
  createEntity,
  getExtractionDetail,
  getExtractions,
  login,
  rawFetch,
  PERSONAS,
  type ExtractionJob,
} from './client'
import { assertErrorEnvelope, assertUnauthorizedEnvelope } from './contract-helpers'
import { freshTin } from './fixtures'

const EXTRACTIONS_PATH = '/api/submission/v1/extractions'
const UPLOAD_PATH = '/api/submission/v1/documents'
const IMPORT_DOCUMENT_PATH = '/api/invoice/v1/imports/document'

const detailPath = (id: string) => `${EXTRACTIONS_PATH}/${id}`
const pagePath = (id: string, n: string) => `${EXTRACTIONS_PATH}/${id}/pages/${n}`
const correctionPath = (id: string, field: string) => `${EXTRACTIONS_PATH}/${id}/fields/${field}/corrections`

// e2e/ is ESM, so import.meta.url is the only cwd-independent anchor.
const PDF_FIXTURE = join(dirname(fileURLToPath(import.meta.url)), '../fixtures/documents/native_invoice.pdf')
const PDF_BYTES = new Uint8Array(readFileSync(PDF_FIXTURE))

// The 201 body's complete key set (CorrectionResponse), sorted. Asserted as a WHOLE: an added
// omitempty drops a field from the wire without touching one value assertion.
const CORRECTION_KEYS = ['created_at', 'field_name', 'id', 'invoice_id', 'method', 'region', 'value']

const POLL_BUDGET_MS = 120_000
const POLL_INTERVAL_MS = 1_000
const TEST_TIMEOUT_MS = 180_000

// A trailing PDF comment changes the content hash without moving any byte offset, so `startxref`
// still resolves. Without a fresh hash, per-tenant dedupe reuses the old row and the permanent
// per-document enqueue key skips the enqueue, so the poll settles on a PREVIOUS run's job.
function uniquePdf(): Uint8Array<ArrayBuffer> {
  const marker = new TextEncoder().encode(`%e2e-${crypto.randomUUID()}\n`)
  const out = new Uint8Array(PDF_BYTES.length + marker.length)
  out.set(PDF_BYTES, 0)
  out.set(marker, PDF_BYTES.length)
  return out
}

async function asRawResult(res: Response): Promise<{ status: number; body: unknown }> {
  let body: unknown
  try {
    body = await res.json()
  } catch {
    body = undefined
  }
  return { status: res.status, body }
}

// Never set Content-Type: fetch derives the multipart boundary from the FormData body, so
// rawFetch (which forces application/json) cannot be used here.
async function uploadPdf(token: string, label: string): Promise<string> {
  const form = new FormData()
  form.set('file', new Blob([uniquePdf()], { type: 'application/pdf' }), 'native_invoice.pdf')
  const res = await asRawResult(
    await fetch(`${apiBase()}${UPLOAD_PATH}`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
      body: form,
    }),
  )
  expect(res.status, `${label}: a PDF upload should return 201 (body ${JSON.stringify(res.body)})`).toBe(201)
  return (res.body as Record<string, unknown>).document_id as string
}

// Polls to a `succeeded` job, or THROWS. Neither an empty jobs[] nor budget expiry may fall
// through to a pass: the loop exits by throw and the function returns a JOB, not a boolean.
async function pollUntilSucceeded(token: string, documentId: string, label: string): Promise<ExtractionJob> {
  const deadline = Date.now() + POLL_BUDGET_MS
  let seen = 'nothing observed'
  for (;;) {
    const { jobs } = await getExtractions(token, documentId)
    if (jobs.length > 0) {
      seen = jobs.map((j) => `${j.id}=${j.state}${j.last_error ? ` last_error=${j.last_error}` : ''}`).join(', ')
      const dead = jobs.find((j) => j.state === 'dead_lettered')
      if (dead) throw new Error(`${label}: extraction dead-lettered -- ${seen}`)
      const succeeded = jobs.find((j) => j.state === 'succeeded')
      if (succeeded) return succeeded
    } else {
      seen = 'jobs: [] (no extraction was ever enqueued for this document)'
    }
    if (Date.now() >= deadline) {
      throw new Error(`${label}: no extraction reached "succeeded" within ${POLL_BUDGET_MS} ms -- last seen ${seen}`)
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS))
  }
}

// refusalOf(): mirrors isolation.spec.ts's captureRejection — a call that RESOLVES (the wrong
// outcome for a refusal arm) must fail loudly here, rather than pass silently before the
// status/body assertions below ever run.
async function refusalOf(thunk: () => Promise<unknown>, label: string): Promise<ApiError> {
  try {
    await thunk()
  } catch (err) {
    expect(err, `${label}: expected an ApiError`).toBeInstanceOf(ApiError)
    return err as ApiError
  }
  throw new Error(`${label}: expected the call to reject, but it resolved with a 2xx`)
}

test.describe('extraction job reader (API E2E, over the deployed gateway)', () => {
  test('the route exists behind the gateway', async () => {
    const token = await login(PERSONAS.A)

    const err = await refusalOf(() => getExtractions(token), 'no document_id')

    // 400 is the registration oracle, and it is stronger than "the body parsed": it
    // discriminates against BOTH 404s this path could produce — the gateway's own JSON 404 for
    // an unknown first segment (gateway.go:74-80) and the submission mux's plain-text 404 for
    // an unregistered subpath — with no reasoning about which one answered.
    expect(err.status, 'a missing document_id should be refused with 400').toBe(400)
    expect(err.body, 'the 400 should carry the handler\'s own {error} envelope').toEqual({
      error: 'document_id is required',
    })
  })

  test('a malformed document id is refused', async () => {
    const token = await login(PERSONAS.A)

    const err = await refusalOf(() => getExtractions(token, 'not-a-uuid'), 'malformed document_id')

    expect(err.status, 'a malformed document_id should be refused with 400').toBe(400)
    expect(err.body, 'the 400 should name the uuid requirement, not leak a parse error').toEqual({
      error: 'document_id must be a well-formed uuid',
    })
  })

  test('an unknown document yields an empty job list', async () => {
    const token = await login(PERSONAS.A)
    // Minted per run, so this spec assumes nothing about the deployment's contents and leaves
    // nothing behind that a later run could collide with.
    const unknownDocumentId = crypto.randomUUID()

    const res = await getExtractions(token, unknownDocumentId)

    // The whole round trip is the assertion: gateway -> submission -> a real tenant transaction
    // -> RLS. A document the caller cannot see is an EMPTY LIST, not a 404 and not a 403 — and
    // `jobs` is a real empty array rather than JSON null (reader.go:42, :59-60).
    expect(res, 'an unknown document should read as an empty job list').toEqual({ jobs: [] })
    expect(Array.isArray(res.jobs), 'jobs should marshal as an array, never as null').toBe(true)
  })

  test('an unauthenticated call is refused before routing', async () => {
    // The gateway verifier's 401 body and the extraction handler's 401 body are byte-identical
    // ({"error":"unauthorized"}), and rawFetch discards the WWW-Authenticate header that would
    // tell them apart (client.ts:52). So this test does NOT claim the body shows the service was
    // never reached — it cannot. What the three paths together show is that ONE pre-routing
    // reject path produced all three answers.
    const noHeaders = { headers: {} }

    const extractions = await rawFetch(EXTRACTIONS_PATH, noHeaders)
    // A control the existing suite already proves is a gateway 401 (auth-contract.spec.ts:34-39).
    const tenancy = await rawFetch('/api/tenancy/v1/me', noHeaders)
    // The discriminator: `not-a-service` is not in the gateway's routedServices
    // (cmd/gateway/main.go:31-34), so if ROUTING had already happened this would be a 404. It is
    // a 401 instead, which places the reject strictly BEFORE the router (gateway.go:63 wraps
    // :74-80).
    const unroutedService = await rawFetch('/api/not-a-service/v1/x', noHeaders)

    assertUnauthorizedEnvelope(extractions, 'extractions with no Authorization header')
    assertUnauthorizedEnvelope(tenancy, 'tenancy control with no Authorization header')

    expect(
      unroutedService.status,
      'an unrouted service name should still answer 401, not 404 — the verifier runs before the router',
    ).toBe(401)
    expect(
      extractions.body,
      'the extractions 401 should be the same body the gateway gives the tenancy control',
    ).toEqual(tenancy.body)
    expect(
      unroutedService.body,
      'the unrouted-service 401 should be the same body, so all three came from one reject path',
    ).toEqual(tenancy.body)
  })

  // --- EXTR-11-09 · the two routes the review screen reads -------------------------------
  //
  // The REGISTRATION oracle in each row below is the 400, never the 401. The gateway verifier
  // answers 401 before the router runs (proved by the unrouted-service control above), so a
  // 401 says nothing about whether the submission mux carries the pattern. A 400 with the
  // handler's own {error} envelope can only have come from the handler, and it discriminates
  // against BOTH 404s an unregistered path produces -- the gateway's JSON 404 for an unknown
  // first segment and the submission mux's plain-text 404 for an unregistered subpath -- with
  // no reasoning about which one answered. `GET /v1/extractions` is registered without a
  // trailing slash, so Go 1.22's mux matches it on the exact path only and cannot answer for
  // either subpath here.

  test('EXTR11-API-01: the detail route exists behind the gateway', async () => {
    const token = await login(PERSONAS.A)
    // Minted per run: this spec assumes nothing about the deployment's contents and leaves
    // nothing a later run could collide with.
    const unknownJobId = crypto.randomUUID()

    const unauthenticated = await rawFetch(detailPath(unknownJobId), { headers: {} })
    assertUnauthorizedEnvelope(unauthenticated, 'the detail route with no Authorization header')

    const err = await refusalOf(() => getExtractionDetail(token, 'not-a-uuid'), 'malformed job id')

    expect(err.status, 'a malformed job id should be refused with 400').toBe(400)
    // The message names THIS route's parameter. The collection route's ("document_id must be
    // a well-formed uuid") would tell a caller the wrong field, and asserting the exact string
    // is what keeps the two apart (handlers.go:117-122).
    expect(err.body, "the 400 should carry the detail handler's own {error} envelope").toEqual({
      error: 'id must be a well-formed uuid',
    })
  })

  test('EXTR11-API-02: the page route exists behind the gateway', async () => {
    const token = await login(PERSONAS.A)
    const unknownJobId = crypto.randomUUID()

    const unauthenticated = await rawFetch(pagePath(unknownJobId, '1'), { headers: {} })
    assertUnauthorizedEnvelope(unauthenticated, 'the page route with no Authorization header')

    const authorized = { headers: { Authorization: `Bearer ${token}` } }

    const malformedId = await rawFetch(pagePath('not-a-uuid', '1'), authorized)
    assertErrorEnvelope(malformedId, 400, 'the page route with a malformed job id')
    expect(malformedId.body, 'both routes bind the same {id} and share its message').toEqual({
      error: 'id must be a well-formed uuid',
    })

    // The sharpest oracle in this file for `/pages/{n}` SPECIFICALLY. `GET /v1/extractions/{id}`
    // binds a single path segment, so it cannot answer a three-segment subpath at all, and
    // "page must be a positive integer" is a string only PageImageHandler produces
    // (handlers.go:171-177). A 400 carrying it therefore places the answer inside that handler
    // rather than anywhere else in the fleet.
    const nonPositivePage = await rawFetch(pagePath(unknownJobId, '0'), authorized)
    assertErrorEnvelope(nonPositivePage, 400, 'the page route with page 0')
    expect(nonPositivePage.body, 'page 0 should be refused by the page handler, by name').toEqual({
      error: 'page must be a positive integer',
    })
  })

  test('EXTR11-API-03: an unknown job is 404, not 500', async () => {
    const token = await login(PERSONAS.A)
    const unknownJobId = crypto.randomUUID()

    // statusForErr's `default` arm is 500 (handlers.go:49-51), so 404 is only reachable through
    // the ErrNotFound arm -- which is also the one answer an absent job and another tenant's
    // job share, so a refusal never confirms that an id exists (reader.go:101-105).
    const detail = await refusalOf(() => getExtractionDetail(token, unknownJobId), 'unknown job, detail route')
    expect(detail.status, 'an unknown job should read as 404, never 500').toBe(404)
    expect(detail.body, 'the 404 should carry the {error} envelope, not an internal').toEqual({ error: 'not found' })

    // The page route reaches the same arm through PageImageKey, and it matters independently:
    // a 500 here would be an internal leaking out of the object-store branch.
    const pageImage = await rawFetch(pagePath(unknownJobId, '1'), { headers: { Authorization: `Bearer ${token}` } })
    assertErrorEnvelope(pageImage, 404, 'unknown job, page route')
    expect(pageImage.body, 'the page route shares the detail route not-found body').toEqual({ error: 'not found' })
  })

  // --- EXTR-12-04 - the correction POST ---------------------------------------------------
  //
  // The registration oracle is the 400, never the 401: the gateway verifier answers 401 before
  // the router runs, so a 401 says nothing about whether the submission mux carries this pattern
  // under POST. Nothing in Go reads the mux pattern, so go build, go vet and the whole
  // internal/extraction suite stay green on a misspelled path or a wrong METHOD.

  test('EXTR12-API-01: the correction route exists behind the gateway', async () => {
    const token = await login(PERSONAS.A)
    const unknownJobId = crypto.randomUUID()
    const authorized = { Authorization: `Bearer ${token}` }
    const body = { value: '1500.00', method: 'typed' }

    const unauthenticated = await rawFetch(correctionPath(unknownJobId, 'total'), { method: 'POST', headers: {}, body })
    assertUnauthorizedEnvelope(unauthenticated, 'the correction route with no Authorization header')

    const malformedId = await rawFetch(correctionPath('not-a-uuid', 'total'), { method: 'POST', headers: authorized, body })
    assertErrorEnvelope(malformedId, 400, 'the correction route with a malformed job id')
    expect(malformedId.body, 'every {id} route in this service shares one message').toEqual({
      error: 'id must be a well-formed uuid',
    })

    // The sharpest oracle for THIS pattern: `POST /v1/extractions/{id}` is not registered at all
    // and `GET /v1/extractions/{id}` binds a single segment, so neither can answer a
    // four-segment subpath. A 422 naming the identity fence can only have come from this handler.
    const locked = await rawFetch(correctionPath(unknownJobId, 'invoice_number'), {
      method: 'POST',
      headers: authorized,
      body,
    })
    assertErrorEnvelope(locked, 422, 'a correction on invoice_number')
    expect(locked.body, 'the locked-field refusal names the identity fence, by name').toEqual({
      error: 'invoice_number identifies the invoice and is not corrected here',
    })
  })

  test('EXTR12-API-02: a correction on a settled job is appended and returned', async () => {
    test.setTimeout(TEST_TIMEOUT_MS)
    const token = await login(PERSONAS.A)

    // Its own entity, so the mock extractor's fixed MOCK-INV-0001 cannot collide with another
    // run and quarantine instead of filing an invoice.
    const entity = await createEntity(token, { name: `EXTR-12 corr ${freshTin()}`, tin: freshTin() })
    const documentId = await uploadPdf(token, 'EXTR12-API-02')
    const job = await pollUntilSucceeded(token, documentId, 'EXTR12-API-02')

    const imported = await rawFetch(IMPORT_DOCUMENT_PATH, {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
      body: { entity_id: entity.id, document_id: documentId },
    })
    expect(imported.status, 'EXTR12-API-02: the settled document should import as one batch').toBe(201)
    expect(
      (imported.body as Record<string, unknown>).ready_invoices,
      'EXTR12-API-02: the import must FILE an invoice -- a quarantined row has none to correct',
    ).toBe(1)

    const res = await rawFetch(correctionPath(job.id, 'total'), {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
      body: { value: '1500.00', method: 'typed' },
    })

    expect(res.status, `EXTR12-API-02: a correction should return 201 (body ${JSON.stringify(res.body)})`).toBe(201)
    const body = res.body as Record<string, unknown>
    expect(Object.keys(body).sort(), 'EXTR12-API-02: the 201 body is the whole CorrectionResponse').toEqual(
      CORRECTION_KEYS,
    )
    expect(body.field_name, 'EXTR12-API-02: field_name echoes the path segment').toBe('total')
    expect(body.value, 'EXTR12-API-02: value is what was sent').toBe('1500.00')
    expect(body.method, 'EXTR12-API-02: method is what was sent').toBe('typed')
    expect(body.region, 'EXTR12-API-02: a typed correction carries an explicit null region').toBeNull()
    expect(
      body.invoice_id,
      'EXTR12-API-02: invoice_id names the invoice the correction reached, which is the claim the audit row makes',
    ).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/)
  })
})
