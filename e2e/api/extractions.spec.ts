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
// NO ROW IS CREATED AND NO COUNT IS ASSERTED. Every request here is a GET; the only document
// id and the only job id used are a per-run crypto.randomUUID() that matches nothing. That
// makes the file trivially safe in the shared run where smoke, api and topology hit ONE
// deployment with no reset between them (docs/e2e-convention.md:66-85). EXTR-11-09 added the
// detail and page routes below under that same rule: Reader.Detail raises ErrNotFound from
// detailTx BEFORE the audit recorder runs (reader.go:164-185), so even the 404 arm writes
// nothing and its transaction rolls back.
//
// EXTR-11-09 · REFUSALS ONLY, for both new routes. A 200 from either needs a settled job, which
// nothing in this layer can produce without breaking the paragraph above -- so the settled-body
// assertions live in e2e/topology/import-wizard.spec.ts as EXTR11-E2E-04/04b, off the response
// the SPA itself consumed.
import { test, expect } from '@playwright/test'
import { ApiError, getExtractionDetail, getExtractions, login, rawFetch, PERSONAS } from './client'
import { assertErrorEnvelope, assertUnauthorizedEnvelope } from './contract-helpers'

const EXTRACTIONS_PATH = '/api/submission/v1/extractions'

const detailPath = (id: string) => `${EXTRACTIONS_PATH}/${id}`
const pagePath = (id: string, n: string) => `${EXTRACTIONS_PATH}/${id}/pages/${n}`

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
})
