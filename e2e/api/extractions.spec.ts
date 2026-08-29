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
// id used is a per-run crypto.randomUUID() that matches nothing. That makes the file trivially
// safe in the shared run where smoke, api and topology hit ONE deployment with no reset
// between them (docs/e2e-convention.md:66-85).
import { test, expect } from '@playwright/test'
import { ApiError, getExtractions, login, rawFetch, PERSONAS } from './client'
import { assertUnauthorizedEnvelope } from './contract-helpers'

const EXTRACTIONS_PATH = '/api/submission/v1/extractions'

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
})
