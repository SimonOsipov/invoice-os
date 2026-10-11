// M3-15-03 (Order 3 of 5): the portfolio contract spec, over the wire —
// through the SAME typed seam (api/client.ts) every api/ spec shares. Setup
// goes through the typed wrappers (createEntity, offboardEntity) +
// freshTin() (M3-14's isolation convention: every created entity gets its
// own fresh Luhn-valid TIN, so nothing else this run created collides with it
// on business_entities' duplicate-TIN partial index). The
// assertions UNDER TEST go through rawFetch (M3-15-01), so the exact HTTP
// status + envelope shape is directly observable — unlike apiFetch, which
// normalizes a non-2xx into a thrown ApiError.
//
// Three properties are proven against the DEPLOYED gateway:
//   - Happy-path status: create -> 201; get/list/update/offboard/onboard -> 200.
//   - Error-path status + envelope: every failure mode maps to the status
//     statusForErr (internal/portfolio/portfolio.go:404-419) predicts, and
//     every error body is the shared flat {error: <string>} envelope
//     (exactly one key, string value) — same shape auth-contract.spec.ts
//     proves for 401s.
//   - Pagination + empty-state: the {limit, offset, total} envelope carries
//     numeric fields, and a no-match `q` filter returns entities: [] (never
//     null) with pagination.total: 0.
//
// CRITICAL (Disposition 3 / fact 8): business_entities.id is a Postgres uuid
// column; Store.GetByID/Update (internal/portfolio/store.go:280-296,
// :206-208) map ONLY pgx.ErrNoRows -> ErrNotFound -> 404. A non-UUID literal
// (e.g. "bogus-id") instead raises Postgres 22P02 invalid_text_representation,
// which falls through to a 500 and silently masks the intended 404. Every
// not-found case below therefore uses crypto.randomUUID() — a syntactically
// valid, RLS-invisible UUID — never a non-UUID string.
//
// Persona A only, fresh entities only: no cross-tenant writes.
import { test, expect } from '@playwright/test'
import { login, createEntity, offboardEntity, listEntities, rawFetch, PERSONAS } from './client'
import { freshTin } from './fixtures'
import { assertErrorEnvelope } from './contract-helpers'

test.describe('portfolio contract (API E2E, over the deployed gateway)', () => {
  let token: string

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
  })

  test.describe('happy-path status codes', () => {
    test('create -> 201', async () => {
      const tin = freshTin()
      const res = await rawFetch('/api/portfolio/v1/entities', {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { name: `M3-15-03 create ${tin}`, tin },
      })
      expect(res.status, 'create should return 201').toBe(201)
      // Status-code-only would under-test a "contract" spec — confirm the
      // 201 body is actually an Entity, not just any 201. Full field-by-field
      // echo (name/tin canonicalization) is M3-14-04's job (portfolio.spec.ts);
      // this is just enough to prove the body shape, not re-litigate it.
      const body = res.body as Record<string, unknown>
      expect(typeof body.id, 'created entity should echo a string id').toBe('string')
      expect(body.status, 'created entity should be active').toBe('active')
    })

    test('get -> 200', async () => {
      const tin = freshTin()
      const created = await createEntity(token, { name: `M3-15-03 get ${tin}`, tin })
      const res = await rawFetch(`/api/portfolio/v1/entities/${created.id}`, {
        headers: { Authorization: `Bearer ${token}` },
      })
      expect(res.status, 'get should return 200').toBe(200)
    })

    test('list -> 200', async () => {
      const res = await rawFetch('/api/portfolio/v1/entities', {
        headers: { Authorization: `Bearer ${token}` },
      })
      expect(res.status, 'list should return 200').toBe(200)
    })

    test('update -> 200', async () => {
      const tin = freshTin()
      const created = await createEntity(token, { name: `M3-15-03 update ${tin}`, tin })
      const res = await rawFetch(`/api/portfolio/v1/entities/${created.id}`, {
        method: 'PATCH',
        headers: { Authorization: `Bearer ${token}` },
        body: { name: `M3-15-03 update ${tin} v2` },
      })
      expect(res.status, 'update should return 200').toBe(200)
    })

    test('offboard -> 200', async () => {
      const tin = freshTin()
      const created = await createEntity(token, { name: `M3-15-03 offboard ${tin}`, tin })
      const res = await rawFetch(`/api/portfolio/v1/entities/${created.id}/offboard`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      })
      expect(res.status, 'offboard should return 200').toBe(200)
    })

    test('onboard -> 200', async () => {
      const tin = freshTin()
      const created = await createEntity(token, { name: `M3-15-03 onboard ${tin}`, tin })
      await offboardEntity(token, created.id)
      const res = await rawFetch(`/api/portfolio/v1/entities/${created.id}/onboard`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      })
      expect(res.status, 'onboard should return 200').toBe(200)
    })
  })

  test.describe('error-path status + envelope', () => {
    test('create with missing name -> 400 {error: string}', async () => {
      const res = await rawFetch('/api/portfolio/v1/entities', {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { tin: freshTin() },
      })
      assertErrorEnvelope(res, 400, 'create missing name')
    })

    test('create with invalid TIN -> 400 {error: string}', async () => {
      const res = await rawFetch('/api/portfolio/v1/entities', {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { name: 'M3-15-03 invalid TIN', tin: 'BADTIN' },
      })
      assertErrorEnvelope(res, 400, 'create invalid TIN')
    })

    // Each string copies a Go constant in internal/portfolio/tin.go; change both together.
    test('create with a TIN of the wrong shape -> 400 names the shape', async () => {
      const res = await rawFetch('/api/portfolio/v1/entities', {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { name: 'AUTH-10-01 wrong shape', tin: 'BADTIN' },
      })
      assertErrorEnvelope(res, 400, 'create TIN wrong shape')
      // portfolio.TINShapeMessage
      expect((res.body as { error: string }).error).toBe(
        'A TIN is digits only. Only the 12-digit FIRS TIN takes a hyphen, after the 8th digit: ########-####.',
      )
    })

    test('create with a TIN of the wrong length -> 400 names the length', async () => {
      const res = await rawFetch('/api/portfolio/v1/entities', {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { name: 'AUTH-10-01 wrong length', tin: '12345' },
      })
      assertErrorEnvelope(res, 400, 'create TIN wrong length')
      // portfolio.TINLengthMessage with %d = 5
      expect((res.body as { error: string }).error).toBe('A TIN has 10 digits (JTB) or 12 digits (FIRS). This one has 5.')
    })

    test('create with a TIN that failed its check digit -> 400 names the check digit', async () => {
      const res = await rawFetch('/api/portfolio/v1/entities', {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { name: 'AUTH-10-01 failed check digit', tin: '1234567890' },
      })
      assertErrorEnvelope(res, 400, 'create TIN failed check digit')
      // portfolio.TINChecksumMessage
      expect((res.body as { error: string }).error).toBe(
        "This TIN's last digit is a check digit, and it does not match the other digits. Check the number on the tax certificate.",
      )
    })

    test('create duplicate TIN -> 409 {error: string}', async () => {
      const tin = freshTin()
      await createEntity(token, { name: `M3-15-03 dup ${tin}`, tin })
      const res = await rawFetch('/api/portfolio/v1/entities', {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { name: `M3-15-03 dup ${tin} again`, tin },
      })
      assertErrorEnvelope(res, 409, 'create duplicate TIN')
    })

    // Not-found: crypto.randomUUID() only — see the file-header CRITICAL note.
    // A non-UUID literal would raise Postgres 22P02 and surface as a 500,
    // silently masking the 404 this test exists to prove.
    test('get not-found (random UUID) -> 404 {error: string}', async () => {
      const res = await rawFetch(`/api/portfolio/v1/entities/${crypto.randomUUID()}`, {
        headers: { Authorization: `Bearer ${token}` },
      })
      assertErrorEnvelope(res, 404, 'get not-found')
    })

    test('update not-found (random UUID) -> 404 {error: string}', async () => {
      const res = await rawFetch(`/api/portfolio/v1/entities/${crypto.randomUUID()}`, {
        method: 'PATCH',
        headers: { Authorization: `Bearer ${token}` },
        body: { name: 'does not matter' },
      })
      assertErrorEnvelope(res, 404, 'update not-found')
    })

    test('update with empty body -> 400 {error: string}', async () => {
      const tin = freshTin()
      const created = await createEntity(token, { name: `M3-15-03 empty update ${tin}`, tin })
      const res = await rawFetch(`/api/portfolio/v1/entities/${created.id}`, {
        method: 'PATCH',
        headers: { Authorization: `Bearer ${token}` },
        body: {},
      })
      assertErrorEnvelope(res, 400, 'update empty body')
    })

    test('list ?status=bogus -> 400 {error: string}', async () => {
      const res = await rawFetch('/api/portfolio/v1/entities?status=bogus', {
        headers: { Authorization: `Bearer ${token}` },
      })
      assertErrorEnvelope(res, 400, 'list status=bogus')
    })

    test('list ?limit=abc -> 400 {error: string}', async () => {
      const res = await rawFetch('/api/portfolio/v1/entities?limit=abc', {
        headers: { Authorization: `Bearer ${token}` },
      })
      assertErrorEnvelope(res, 400, 'list limit=abc')
    })

    test('list ?offset=-1 -> 400 {error: string}', async () => {
      const res = await rawFetch('/api/portfolio/v1/entities?offset=-1', {
        headers: { Authorization: `Bearer ${token}` },
      })
      assertErrorEnvelope(res, 400, 'list offset=-1')
    })

    test('offboard-then-offboard-again (redundant transition) -> 409 {error: string}', async () => {
      const tin = freshTin()
      const created = await createEntity(token, { name: `M3-15-03 redundant offboard ${tin}`, tin })
      await offboardEntity(token, created.id)
      const res = await rawFetch(`/api/portfolio/v1/entities/${created.id}/offboard`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      })
      assertErrorEnvelope(res, 409, 'redundant offboard')
    })
  })

  // Seeded active non-admins (db/seed.dev.sql). The 403 text copies portfolio's statusForErr.
  test.describe('403: a non-admin cannot add a company', () => {
    const rows = [
      { name: 'firm preparer', persona: PERSONAS.A, subject: 'c0000000-0000-0000-0000-000000000003' },
      { name: 'firm reviewer', persona: PERSONAS.A, subject: 'c0000000-0000-0000-0000-000000000004' },
      { name: 'in-house preparer', persona: PERSONAS.B, subject: 'c0000000-0000-0000-0000-000000000013' },
      { name: 'in-house reviewer', persona: PERSONAS.B, subject: 'c0000000-0000-0000-0000-000000000008' },
    ]
    for (const row of rows) {
      test(`403: ${row.name}`, async () => {
        const nonAdminToken = await login({ ...row.persona, subject: row.subject })
        const tin = freshTin()
        const res = await rawFetch('/api/portfolio/v1/entities', {
          method: 'POST',
          headers: { Authorization: `Bearer ${nonAdminToken}` },
          body: { name: `LOGFIX-06-01 ${row.name} ${tin}`, tin },
        })
        assertErrorEnvelope(res, 403, `create as ${row.name}`)
        expect((res.body as { error: string }).error).toBe('only an admin can add a company')
      })
    }
  })

  test.describe('pagination + empty-state', () => {
    test('normal list -> pagination envelope {limit, offset, total} with numeric fields', async () => {
      const tin = freshTin()
      await createEntity(token, { name: `M3-15-03 pagination ${tin}`, tin })

      const list = await listEntities(token, { limit: 200 })
      expect(typeof list.pagination.limit, 'pagination.limit should be numeric').toBe('number')
      expect(typeof list.pagination.offset, 'pagination.offset should be numeric').toBe('number')
      expect(typeof list.pagination.total, 'pagination.total should be numeric').toBe('number')
      expect(list.pagination.total, 'total should count at least the just-created entity').toBeGreaterThanOrEqual(1)
    })

    test('empty-state: a no-match q filter returns entities: [] and pagination.total: 0', async () => {
      // q ILIKEs both name and tin (internal/portfolio/store.go:95-97), so a
      // token that was never used as either is guaranteed no-match. freshTin()
      // already guarantees per-run uniqueness (pid-derived run seed + a
      // module-level call counter — fixtures.ts), which covers every row this
      // run can have written, so embedding it here is enough; no separate
      // random generator is needed.
      const noMatchToken = `M3-15-03-no-match-${freshTin()}`
      const list = await listEntities(token, { q: noMatchToken, limit: 200 })
      expect(list.entities).toEqual([])
      expect(list.pagination.total).toBe(0)
    })
  })
})
