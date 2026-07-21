// M4-16-02 (Order 2 of 3, MIDDLE): the invoice contract spec, over the wire —
// through the SAME typed seam (api/client.ts) every api/ spec shares. Setup
// (fresh entities, invoices that need to already exist) goes through the
// typed wrappers (createEntity, createInvoice, validateInvoice); the
// assertions UNDER TEST go through rawFetch (M3-15-01) so the exact HTTP
// status + envelope shape is directly observable — unlike apiFetch, which
// normalizes a non-2xx into a thrown ApiError. Mirrors
// contract-portfolio.spec.ts's shape, scoped to the invoice CRUD/transition/
// validate/history surface (M4), which had zero deployed-gateway contract
// coverage before this file even though it is exhaustively handler-tested
// in-process.
//
// Three wrinkles this surface has that the earlier contract specs don't
// ([D-transition-200], [D-list-clamp], [D-validate-200-fixture] — story
// "M4-16 API Contract Coverage — M4 Surfaces"), each honored, not "corrected":
//   - /history's success body is a BARE JSON array — no {history:[...]}
//     envelope, unlike every other endpoint in this file.
//   - /validate's blocking verdict is a 200 carrying `violations` as data,
//     never an HTTP error — a broken draft is a successful validate call.
//   - The `validated` transition target is guarded (409) even though it's a
//     syntactically well-formed Status — it's only earned via /validate.
//   - List's out-of-range clamp is ASYMMETRIC: limit>200 clamps down to 200
//     (still 200 OK); limit<1, offset<0, and non-integer values are 400s.
//
// The transitions-200 success case (validated -> queued) drives its own
// clean-invoice fixture (own copy, mirroring the deterministic
// e2e/topology/invoice-surfaces.spec.ts:cleanInvoiceFields — proven to
// promote draft->validated against this same deployed fleet) through
// validateInvoice first, asserting status==="validated" so a non-clean
// fixture fails loudly at setup, not as a confusing downstream 409.
//
// Isolation: fresh entity per file (freshTin(), no DELETE endpoint exists);
// crypto.randomUUID() for every not-found case (a syntactically valid,
// RLS-invisible UUID — never a non-UUID string, which would raise Postgres
// 22P02 and mask the intended 404, per contract-portfolio.spec.ts's CRITICAL
// note); a high `offset` for empty-state (the shared dev tenant already
// carries invoices from other specs, so there is no truly empty list).
import { test, expect } from '@playwright/test'
import { login, createEntity, createInvoice, validateInvoice, rawFetch, PERSONAS, type Entity } from './client'
import { freshTin } from './fixtures'
import { assertErrorEnvelope } from './contract-helpers'

// cleanInvoiceFields(): own copy (repo convention — no cross-suite imports
// between spec files), mirroring e2e/topology/invoice-surfaces.spec.ts's
// fixture of the same name VERBATIM: a canonical supplier TIN, VAT at the
// correct 7.5% of subtotal, one reconciling line item — fires ZERO
// violations against the seeded v1 rule set, so it deterministically
// promotes draft -> validated.
function cleanInvoiceFields(invoiceNumber: string) {
  return {
    invoice_number: invoiceNumber,
    issue_date: '2026-01-01T00:00:00Z',
    supplier_tin: freshTin(),
    supplier_name: 'Acme Nigeria Ltd',
    buyer_tin: '87654321-0002',
    buyer_name: 'Buyer Ltd',
    currency: 'NGN',
    subtotal: '1000',
    vat: '75',
    total: '1075',
    line_items: [{ description: 'Widget', quantity: '10', unit_price: '100', line_total: '1000' }],
  }
}

test.describe('invoice contract (API E2E, over the deployed gateway)', () => {
  let token: string
  let entity: Entity

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
    const tin = freshTin()
    entity = await createEntity(token, { name: `M4-16-02 invoice ${tin}`, tin })
  })

  test.describe('create', () => {
    test('create -> 201 {id, status: draft}', async () => {
      const res = await rawFetch('/api/invoice/v1/invoices', {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { entity_id: entity.id, invoice_number: `INV-C-${freshTin()}` },
      })
      expect(res.status, 'create should return 201').toBe(201)
      const body = res.body as Record<string, unknown>
      expect(typeof body.id, 'created invoice should echo a string id').toBe('string')
      expect(body.status, 'a freshly created invoice should be draft').toBe('draft')
    })

    test('create with missing invoice_number -> 400 {error: string}', async () => {
      const res = await rawFetch('/api/invoice/v1/invoices', {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { entity_id: entity.id },
      })
      assertErrorEnvelope(res, 400, 'create missing invoice_number')
    })

    test('create with missing entity_id -> 400 {error: string}', async () => {
      const res = await rawFetch('/api/invoice/v1/invoices', {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { invoice_number: `INV-C-${freshTin()}` },
      })
      assertErrorEnvelope(res, 400, 'create missing entity_id')
    })

    test('create with no request body -> 400 {error: string}', async () => {
      // Omit `body` entirely -- rawFetch only JSON-stringifies a body that is
      // PRESENT, so this sends a genuinely empty request body (the decode-error
      // branch), same technique contract-validation.spec.ts uses.
      const res = await rawFetch('/api/invoice/v1/invoices', {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      })
      assertErrorEnvelope(res, 400, 'create no body')
    })

    test('create duplicate invoice_number on the same entity -> 409 {error: string}', async () => {
      const num = `INV-C-DUP-${freshTin()}`
      await createInvoice(token, { entity_id: entity.id, invoice_number: num })
      const res = await rawFetch('/api/invoice/v1/invoices', {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { entity_id: entity.id, invoice_number: num },
      })
      assertErrorEnvelope(res, 409, 'create duplicate invoice_number')
    })

    test('create with no auth -> 401 {error: string}', async () => {
      const res = await rawFetch('/api/invoice/v1/invoices', {
        method: 'POST',
        body: { entity_id: entity.id, invoice_number: `INV-C-${freshTin()}` },
      })
      assertErrorEnvelope(res, 401, 'create no auth')
    })
  })

  test.describe('read', () => {
    test('read -> 200 {id: matches created}', async () => {
      const created = await createInvoice(token, { entity_id: entity.id, invoice_number: `INV-R-${freshTin()}` })
      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, {
        headers: { Authorization: `Bearer ${token}` },
      })
      expect(res.status, 'read should return 200').toBe(200)
      const body = res.body as Record<string, unknown>
      expect(body.id, 'read body.id should match the created invoice').toBe(created.id)
    })

    test('read not-found (random UUID) -> 404 {error: string}', async () => {
      const res = await rawFetch(`/api/invoice/v1/invoices/${crypto.randomUUID()}`, {
        headers: { Authorization: `Bearer ${token}` },
      })
      assertErrorEnvelope(res, 404, 'read not-found')
    })

    test('read with no auth -> 401 {error: string}', async () => {
      const created = await createInvoice(token, { entity_id: entity.id, invoice_number: `INV-R-${freshTin()}` })
      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}`)
      assertErrorEnvelope(res, 401, 'read no auth')
    })
  })

  test.describe('list', () => {
    test('list -> 200 {invoices: [], pagination: {limit, offset, total}}', async () => {
      const res = await rawFetch('/api/invoice/v1/invoices?limit=5', {
        headers: { Authorization: `Bearer ${token}` },
      })
      expect(res.status, 'list should return 200').toBe(200)
      const body = res.body as Record<string, unknown>
      expect(Array.isArray(body.invoices), 'invoices should be an array').toBe(true)
      const pagination = body.pagination as Record<string, unknown>
      expect(typeof pagination.limit, 'pagination.limit should be numeric').toBe('number')
      expect(typeof pagination.offset, 'pagination.offset should be numeric').toBe('number')
      expect(typeof pagination.total, 'pagination.total should be numeric').toBe('number')
      expect(pagination.limit, 'an in-range limit should be echoed unclamped').toBe(5)
    })

    test('list ?limit=500 clamps to 200 -> 200 {pagination.limit: 200}', async () => {
      // The clamp is asymmetric ([D-list-clamp]): only limit>200 clamps
      // (still 200 OK) -- limit<1/offset<0/non-integer reject with 400 below.
      const res = await rawFetch('/api/invoice/v1/invoices?limit=500', {
        headers: { Authorization: `Bearer ${token}` },
      })
      expect(res.status, 'an over-range limit should still return 200 (clamped, not rejected)').toBe(200)
      const body = res.body as Record<string, unknown>
      const pagination = body.pagination as Record<string, unknown>
      expect(pagination.limit, 'limit>200 should clamp down to 200').toBe(200)
    })

    test('list ?limit=0 -> 400 {error: string}', async () => {
      const res = await rawFetch('/api/invoice/v1/invoices?limit=0', {
        headers: { Authorization: `Bearer ${token}` },
      })
      assertErrorEnvelope(res, 400, 'list limit=0')
    })

    test('list ?offset=-1 -> 400 {error: string}', async () => {
      const res = await rawFetch('/api/invoice/v1/invoices?offset=-1', {
        headers: { Authorization: `Bearer ${token}` },
      })
      assertErrorEnvelope(res, 400, 'list offset=-1')
    })

    test('list ?limit=abc -> 400 {error: string}', async () => {
      const res = await rawFetch('/api/invoice/v1/invoices?limit=abc', {
        headers: { Authorization: `Bearer ${token}` },
      })
      assertErrorEnvelope(res, 400, 'list limit=abc')
    })

    test('empty-state via a high offset -> 200 {invoices: [], pagination.total numeric}', async () => {
      // The shared dev tenant already carries invoices from other specs, so
      // there is no truly empty list -- an offset beyond total is the
      // deterministic, order-independent way to observe the empty-state shape.
      const res = await rawFetch('/api/invoice/v1/invoices?offset=100000000', {
        headers: { Authorization: `Bearer ${token}` },
      })
      expect(res.status, 'a beyond-total offset should still return 200').toBe(200)
      const body = res.body as Record<string, unknown>
      expect(Array.isArray(body.invoices), 'invoices should be an array').toBe(true)
      expect((body.invoices as unknown[]).length, 'invoices should be empty beyond total').toBe(0)
      const pagination = body.pagination as Record<string, unknown>
      expect(typeof pagination.total, 'pagination.total should be numeric').toBe('number')
      expect(pagination.offset, 'pagination.offset should echo the requested high offset').toBe(100000000)
    })

    test('list with no auth -> 401 {error: string}', async () => {
      const res = await rawFetch('/api/invoice/v1/invoices')
      assertErrorEnvelope(res, 401, 'list no auth')
    })
  })

  test.describe('transitions', () => {
    test('validated -> queued -> 200 {status: queued} (the transitions success path)', async () => {
      // Drive a CLEAN invoice all the way to validated first (assert
      // status==="validated" so a non-clean fixture fails loudly here, not
      // as a confusing 409 below), then observe the transitions endpoint's
      // only zero-violation success path via rawFetch.
      const created = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(`INV-T-${freshTin()}`) })
      const validated = await validateInvoice(token, created.id)
      expect(validated.status, 'the clean fixture should promote draft -> validated').toBe('validated')

      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}/transitions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { target: 'queued' },
      })
      expect(res.status, 'validated -> queued should return 200').toBe(200)
      const body = res.body as Record<string, unknown>
      expect(body.status, 'the transitioned invoice should be queued').toBe('queued')
    })

    test('target=validated is guarded (only earned via /validate) -> 409 {error: string}', async () => {
      const created = await createInvoice(token, { entity_id: entity.id, invoice_number: `INV-T-${freshTin()}` })
      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}/transitions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { target: 'validated' },
      })
      assertErrorEnvelope(res, 409, 'transition target=validated guard')
    })

    test('illegal transition (draft -> submitted) -> 409 {error: string}', async () => {
      const created = await createInvoice(token, { entity_id: entity.id, invoice_number: `INV-T-${freshTin()}` })
      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}/transitions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { target: 'submitted' },
      })
      assertErrorEnvelope(res, 409, 'illegal transition draft -> submitted')
    })

    test('transition with an unknown target -> 400 {error: string}', async () => {
      const created = await createInvoice(token, { entity_id: entity.id, invoice_number: `INV-T-${freshTin()}` })
      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}/transitions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { target: 'not-a-status' },
      })
      assertErrorEnvelope(res, 400, 'transition unknown target')
    })

    test('transition with no request body -> 400 {error: string}', async () => {
      const created = await createInvoice(token, { entity_id: entity.id, invoice_number: `INV-T-${freshTin()}` })
      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}/transitions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      })
      assertErrorEnvelope(res, 400, 'transition no body')
    })

    test('transition not-found (random UUID) -> 404 {error: string}', async () => {
      const res = await rawFetch(`/api/invoice/v1/invoices/${crypto.randomUUID()}/transitions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { target: 'queued' },
      })
      assertErrorEnvelope(res, 404, 'transition not-found')
    })

    test('transition with no auth -> 401 {error: string}', async () => {
      const created = await createInvoice(token, { entity_id: entity.id, invoice_number: `INV-T-${freshTin()}` })
      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}/transitions`, {
        method: 'POST',
        body: { target: 'queued' },
      })
      assertErrorEnvelope(res, 401, 'transition no auth')
    })
  })

  test.describe('validate', () => {
    test('validate a broken draft -> 200 {violations: non-empty, status: draft} (never an HTTP error)', async () => {
      // Only entity_id/invoice_number set (all MBS content omitted) --
      // deterministically stays draft with severity:"error" violations
      // regardless of the active rule set (missing-required-content always
      // fires), the dashboard.spec.ts broken-draft pattern.
      const created = await createInvoice(token, { entity_id: entity.id, invoice_number: `INV-V-${freshTin()}` })
      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}/validate`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      })
      expect(res.status, 'a blocking verdict is still a 200, never an HTTP error').toBe(200)
      const body = res.body as Record<string, unknown>
      expect(Array.isArray(body.violations), 'violations should be an array').toBe(true)
      expect((body.violations as unknown[]).length, 'the broken draft should fire at least one violation').toBeGreaterThan(0)
      expect(body.status, 'a blocking verdict should leave the invoice draft').toBe('draft')
      expect('rule_set_version' in body, 'the validate response should carry rule_set_version').toBe(true)
    })

    test('validate not-found (random UUID) -> 404 {error: string}', async () => {
      const res = await rawFetch(`/api/invoice/v1/invoices/${crypto.randomUUID()}/validate`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      })
      assertErrorEnvelope(res, 404, 'validate not-found')
    })

    test('validate with no auth -> 401 {error: string}', async () => {
      const created = await createInvoice(token, { entity_id: entity.id, invoice_number: `INV-V-${freshTin()}` })
      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}/validate`, { method: 'POST' })
      assertErrorEnvelope(res, 401, 'validate no auth')
    })
  })

  test.describe('history', () => {
    test('history -> 200 bare array of status changes (no envelope)', async () => {
      const created = await createInvoice(token, { entity_id: entity.id, invoice_number: `INV-H-${freshTin()}` })
      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}/history`, {
        headers: { Authorization: `Bearer ${token}` },
      })
      expect(res.status, 'history should return 200').toBe(200)
      // Success body is a BARE JSON array -- no {history:[...]} envelope or
      // pagination, unlike every other success body in this file.
      expect(Array.isArray(res.body), 'history body should be a bare array, not an envelope object').toBe(true)
      const changes = res.body as Record<string, unknown>[]
      expect(changes.length, 'a created invoice should have at least its genesis history row').toBeGreaterThanOrEqual(1)
      const first = changes[0]
      expect('to_status' in first, 'a history row should carry to_status').toBe(true)
      expect('actor' in first, 'a history row should carry actor').toBe(true)
      expect('changed_at' in first, 'a history row should carry changed_at').toBe(true)
      expect('from_status' in first, 'a history row should carry from_status (nullable)').toBe(true)
    })

    test('history not-found (random UUID) -> 404 {error: string}', async () => {
      const res = await rawFetch(`/api/invoice/v1/invoices/${crypto.randomUUID()}/history`, {
        headers: { Authorization: `Bearer ${token}` },
      })
      assertErrorEnvelope(res, 404, 'history not-found')
    })

    test('history with no auth -> 401 {error: string}', async () => {
      const created = await createInvoice(token, { entity_id: entity.id, invoice_number: `INV-H-${freshTin()}` })
      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}/history`)
      assertErrorEnvelope(res, 401, 'history no auth')
    })
  })
})
