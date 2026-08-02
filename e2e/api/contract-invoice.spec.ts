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
import { login, createEntity, createInvoice, validateInvoice, rawFetch, listInvoices, PERSONAS, type Entity } from './client'
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

// twoLineInvoiceFields(): a two-line variant of cleanInvoiceFields (INVED-01-08), needed
// so T10's 3-line PATCH genuinely REPLACES the set rather than merely appending to a
// single starting line, and so T11's fingerprint check has real line ids to compare.
// Same header numbers as cleanInvoiceFields (fires zero violations), just split across two
// reconciling lines instead of one.
function twoLineInvoiceFields(invoiceNumber: string) {
  return {
    ...cleanInvoiceFields(invoiceNumber),
    line_items: [
      { description: 'Line One', quantity: '2', unit_price: '250', line_total: '500' },
      { description: 'Line Two', quantity: '5', unit_price: '100', line_total: '500' },
    ],
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

    test('search-contract: q matches buyer and supplier TIN through the gateway', async () => {
      // Uses the typed listInvoices() helper deliberately (not rawFetch like the rest
      // of this block) -- widening ListInvoicesQuery only proves anything if something
      // calls it through the gateway.
      const buyerTin = freshTin()
      const buyerMatch = await createInvoice(token, {
        entity_id: entity.id,
        invoice_number: `INV-L-${freshTin()}`,
        buyer_tin: buyerTin,
      })
      const buyerRes = await listInvoices(token, { q: buyerTin })
      expect(buyerRes.invoices.some((inv) => inv.id === buyerMatch.id), 'q should match the invoice by buyer_tin').toBe(true)
      expect(buyerRes.pagination.total, 'pagination.total should be the filtered total, not the tenant-wide total').toBe(1)

      const supplierTin = freshTin()
      const supplierMatch = await createInvoice(token, {
        entity_id: entity.id,
        invoice_number: `INV-L-${freshTin()}`,
        supplier_tin: supplierTin,
      })
      const supplierRes = await listInvoices(token, { q: supplierTin })
      expect(supplierRes.invoices.some((inv) => inv.id === supplierMatch.id), 'q should match the invoice by supplier_tin').toBe(true)
      expect(supplierRes.pagination.total, 'pagination.total should be the filtered total, not the tenant-wide total').toBe(1)
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

  // INVED-01-08: the PATCH-line_items and GET-action-flag contracts (Core AC 2/3/4), plus
  // the one additive transitions/submissions door Core AC 2 needs a second proof of. This
  // file asserts individual keys, never an exact key set on a success body (verified) --
  // so the three additive GET keys below are safe, and batch-submit's result item is
  // asserted field-by-field rather than via a whole-object toEqual for the same reason.
  test.describe('edit (PATCH)', () => {
    test('PATCH line_items[3] -> 200, replaces the whole set with line_no reassigned 1..3', async () => {
      const created = await createInvoice(token, { entity_id: entity.id, ...twoLineInvoiceFields(`INV-E-${freshTin()}`) })

      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, {
        method: 'PATCH',
        headers: { Authorization: `Bearer ${token}` },
        body: {
          line_items: [
            { description: 'Alpha', quantity: '1', unit_price: '100', line_total: '100' },
            { description: 'Beta', quantity: '2', unit_price: '100', line_total: '200' },
            { description: 'Gamma', quantity: '3', unit_price: '100', line_total: '300' },
          ],
        },
      })
      expect(res.status, 'PATCH with a full line_items array should return 200').toBe(200)
      const body = res.body as Record<string, unknown>
      const lines = body.line_items as Record<string, unknown>[]
      expect(Array.isArray(lines), 'line_items should be an array').toBe(true)
      expect(lines.length, 'PATCH should REPLACE the whole line set, not append to it').toBe(3)
      expect(lines.map((l) => l.line_no), 'line_no should be reassigned 1..N by array position').toEqual([1, 2, 3])

      const getRes = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, { headers: { Authorization: `Bearer ${token}` } })
      const getLines = (getRes.body as Record<string, unknown>).line_items as Record<string, unknown>[]
      expect(getLines.length, 'GET should reflect the replaced 3-line set').toBe(3)
      expect(getLines.map((l) => l.line_no)).toEqual([1, 2, 3])
    })

    test('PATCH line_items:[] -> 200, and the response OMITS the key entirely, never []', async () => {
      const created = await createInvoice(token, { entity_id: entity.id, ...twoLineInvoiceFields(`INV-E-${freshTin()}`) })

      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, {
        method: 'PATCH',
        headers: { Authorization: `Bearer ${token}` },
        body: { line_items: [] },
      })
      expect(res.status, 'PATCH line_items:[] should return 200').toBe(200)
      // Invoice.LineItems is `json:"line_items,omitempty"` (invoice.go:105) and both
      // replaceLinesTx and hydrateLinesTx return nil (never []LineItem{}) for zero rows --
      // the wire carries NO key at all for a fully-emptied invoice. A naive
      // `toEqual([])` would fail here; only absence is correct.
      expect('line_items' in (res.body as Record<string, unknown>), 'a zero-line invoice must omit line_items entirely, not carry []').toBe(false)

      const getRes = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, { headers: { Authorization: `Bearer ${token}` } })
      expect('line_items' in (getRes.body as Record<string, unknown>), 'GET must agree: no key, not []').toBe(false)
    })

    test('PATCH header-only change leaves line ids and content byte-identical [fingerprint-excludes-line-ids]', async () => {
      const created = await createInvoice(token, { entity_id: entity.id, ...twoLineInvoiceFields(`INV-E-${freshTin()}`) })
      const before = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, { headers: { Authorization: `Bearer ${token}` } })
      const beforeLines = (before.body as Record<string, unknown>).line_items

      // buyer_name, NOT supplier_name (INVCR-01-18, C7 fix, edit path): Store.Update/Edit
      // now ALWAYS re-derive supplier_tin/supplier_name from the invoice's entity,
      // discarding whatever the PATCH body sends for those two fields (mirroring
      // Store.Create's own [supplier-from-entity] override, INVCR-01-17) -- a PATCHed
      // supplier_name would silently NOT take effect, which is exactly the fix working as
      // intended, not a bug this "header-only change" vehicle should trip over. buyer_name
      // is untouched by the derivation (scope fence) and remains ordinary caller-controlled
      // content, so it still proves the thing this test actually cares about: a header-only
      // PATCH leaves the stored line ids/content byte-identical.
      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, {
        method: 'PATCH',
        headers: { Authorization: `Bearer ${token}` },
        body: { buyer_name: 'Renamed Buyer Ltd' },
      })
      expect(res.status, 'a header-only PATCH should return 200').toBe(200)

      const after = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, { headers: { Authorization: `Bearer ${token}` } })
      const afterBody = after.body as Record<string, unknown>
      expect(afterBody.buyer_name, 'the header field should be updated').toBe('Renamed Buyer Ltd')
      expect(afterBody.line_items, 'a header-only edit must not touch the stored lines (ids incl.)').toEqual(beforeLines)
    })

    test('PATCH a line change on a validated invoice demotes it to draft with can_revalidate:true and a null reason', async () => {
      const created = await createInvoice(token, { entity_id: entity.id, ...twoLineInvoiceFields(`INV-E-${freshTin()}`) })
      const validated = await validateInvoice(token, created.id)
      expect(validated.status, 'the clean fixture should promote draft -> validated').toBe('validated')

      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, {
        method: 'PATCH',
        headers: { Authorization: `Bearer ${token}` },
        body: { line_items: [{ description: 'Changed', quantity: '1', unit_price: '1000', line_total: '1000' }] },
      })
      expect(res.status, 'a line change on a validated invoice should still return 200').toBe(200)

      const getRes = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, { headers: { Authorization: `Bearer ${token}` } })
      const body = getRes.body as Record<string, unknown>
      expect(body.status, 'the edit should demote validated -> draft').toBe('draft')
      expect(body.can_edit, 'a demoted draft should still be editable').toBe(true)
      expect(body.can_revalidate, 'a demoted draft should be re-validatable').toBe(true)
      expect(body.revalidate_blocked_reason, 'the reason must be exactly null, not merely falsy').toBeNull()
    })

    test('GET on an untouched validated invoice -> can_edit:true, can_revalidate:false, and a non-empty reason', async () => {
      const created = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(`INV-E-${freshTin()}`) })
      const validated = await validateInvoice(token, created.id)
      expect(validated.status, 'the clean fixture should promote draft -> validated').toBe('validated')

      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, { headers: { Authorization: `Bearer ${token}` } })
      expect(res.status, 'GET on a validated invoice should return 200').toBe(200)
      const body = res.body as Record<string, unknown>
      expect(body.can_edit, 'a validated invoice should still be editable').toBe(true)
      expect(body.can_revalidate, 'a validated invoice should not be re-validatable until edited').toBe(false)
      expect(typeof body.revalidate_blocked_reason, 'the reason should be a string').toBe('string')
      expect((body.revalidate_blocked_reason as string).length, 'the reason should be non-empty').toBeGreaterThan(0)
    })

    test('POST /invoices/submissions on a line-mutated (demoted) invoice skips it as not_validated', async () => {
      const created = await createInvoice(token, { entity_id: entity.id, ...twoLineInvoiceFields(`INV-E-${freshTin()}`) })
      const validated = await validateInvoice(token, created.id)
      expect(validated.status, 'the clean fixture should promote draft -> validated').toBe('validated')

      const editRes = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, {
        method: 'PATCH',
        headers: { Authorization: `Bearer ${token}` },
        body: { line_items: [{ description: 'Changed', quantity: '1', unit_price: '1000', line_total: '1000' }] },
      })
      expect(editRes.status, 'the demoting edit should succeed').toBe(200)

      const res = await rawFetch('/api/invoice/v1/invoices/submissions', {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { invoice_ids: [created.id], idempotency_key: crypto.randomUUID() },
      })
      expect(res.status, 'batch-submit should still return 200 even when every invoice is skipped').toBe(200)
      const body = res.body as Record<string, unknown>
      const results = body.results as Record<string, unknown>[]
      expect(results.length, 'exactly one result for one requested invoice').toBe(1)
      expect(results[0].invoice_id, 'the result should name the requested invoice').toBe(created.id)
      expect(results[0].enqueued, 'a draft (demoted) invoice must not be enqueued').toBe(false)
      expect(results[0].reason, 'the skip reason must be not_validated').toBe('not_validated')
    })

    test('POST /transitions {"target":"queued"} on a draft (demoted-from-validated) invoice is refused with 409', async () => {
      // The API-only second door onto the same refusal Core AC 2 names: the existing
      // `illegal transition (draft -> submitted)` case above pins draft -> submitted; this
      // pins draft -> queued specifically, reached via the validated -> edit -> demoted
      // path, not a never-validated draft.
      const created = await createInvoice(token, { entity_id: entity.id, ...twoLineInvoiceFields(`INV-E-${freshTin()}`) })
      const validated = await validateInvoice(token, created.id)
      expect(validated.status, 'the clean fixture should promote draft -> validated').toBe('validated')

      const editRes = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, {
        method: 'PATCH',
        headers: { Authorization: `Bearer ${token}` },
        body: { line_items: [{ description: 'Changed', quantity: '1', unit_price: '1000', line_total: '1000' }] },
      })
      expect(editRes.status, 'the demoting edit should succeed').toBe(200)

      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}/transitions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { target: 'queued' },
      })
      assertErrorEnvelope(res, 409, 'draft (demoted-from-validated) -> queued transition')
    })

    test('PATCH a malformed line numeric -> 400, never a 500', async () => {
      const created = await createInvoice(token, { entity_id: entity.id, invoice_number: `INV-E-${freshTin()}` })

      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, {
        method: 'PATCH',
        headers: { Authorization: `Bearer ${token}` },
        body: { line_items: [{ description: 'x', unit_price: 'not-a-number' }] },
      })
      assertErrorEnvelope(res, 400, 'malformed line numeric')
    })
  })
})
