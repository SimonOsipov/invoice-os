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
import {
  login,
  me,
  memberships,
  createEntity,
  createInvoice,
  validateInvoice,
  rawFetch,
  listInvoices,
  createApprovalPolicy,
  putApprovalPolicyDraft,
  publishApprovalPolicy,
  deleteApprovalPolicy,
  getInvoiceApproval,
  decideInvoiceApproval,
  getInvoiceHistory,
  PERSONAS,
  type Entity,
} from './client'
import { freshTin, freshPolicyName } from './fixtures'
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

// createFailedInvoice(): the draft->validated->queued->failed chain the
// rejection_reasons test below drives inline, extracted here since the
// resolve-outside tests need a fresh failed fixture several times.
async function createFailedInvoice(tok: string, entityId: string, invoiceNumber: string): Promise<string> {
  const created = await createInvoice(tok, { entity_id: entityId, ...cleanInvoiceFields(invoiceNumber) })
  await validateInvoice(tok, created.id)
  await rawFetch(`/api/invoice/v1/invoices/${created.id}/transitions`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${tok}` },
    body: { target: 'queued' },
  })
  await rawFetch(`/api/invoice/v1/invoices/${created.id}/transitions`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${tok}` },
    body: { target: 'failed' },
  })
  return created.id
}

// The seeded active PREPARER of tenant a (db/seed.dev.sql): a member, so reads succeed,
// but not an approver. Own copy — repo convention is no cross-spec imports.
const PREPARER_SUBJECT = 'c0000000-0000-0000-0000-000000000003'

// internal/invoice/handlers.go's notApproverTransmitReason, byte for byte (the separator
// is an em dash, U+2014). This is the only layer where that Go const meets a TS literal on
// bytes a running server emitted — assertErrorEnvelope never looks at the message.
const NOT_APPROVER_REASON = 'Only an admin or a reviewer can submit an invoice to NRS/MBS — ask an approver on your team.'

// APPR-08-06: approvalGate's sentences (internal/invoice/handlers.go), restated here so a
// silent edit to the server cannot rewrite its own oracle. Deliberately distinct from
// NOT_APPROVER_REASON above -- that one names the transmit door, this one the decision door.
const APPROVE_NOT_APPROVER_REASON = 'Only an admin or a reviewer can approve or reject an invoice — ask an approver on your team.'
const APPROVE_NOT_VALIDATED_REASON = 'Only a validated invoice can be approved or rejected.'
const APPROVE_RUN_CLOSED_REASON = "This invoice's approval run is already closed."
const APPROVE_NOT_ROLE_HOLDER_REASON =
  "Only an approver staffed to this step's workflow role can approve or reject it — ask whoever holds that role."

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

    // OPEN DISCREPANCY (task-332, BUG-01-06): the story recorded a seeded failed invoice
    // (DEMO-2026-1004) as rejection_reasons: null off the LIVE API, even though its seed
    // row (db/seed.dev.sql) literally writes '[]' and the column is `jsonb NOT NULL
    // DEFAULT '[]'`. Both can be true if internal/invoice/invoice.go's Violations/
    // RejectionReasons (json.RawMessage) ever reach marshal as a Go nil -- a nil
    // json.RawMessage encodes to JSON `null`, not `[]`. rawFetch's parsed body (not the
    // typed client, which never normalizes either key) is the only way to see the wire
    // honestly. Allowed to fail at the deploy gate -- that would be a real finding in the
    // Go serialization, not a broken test (the underlying cause is Out of Scope here).
    test('a failed invoice\'s rejection_reasons and violations come back as arrays, never null', async () => {
      const created = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(`INV-R-FAILED-${freshTin()}`) })
      const validated = await validateInvoice(token, created.id)
      expect(validated.status, 'the clean fixture should promote draft -> validated').toBe('validated')

      await rawFetch(`/api/invoice/v1/invoices/${created.id}/transitions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { target: 'queued' },
      })
      const failedRes = await rawFetch(`/api/invoice/v1/invoices/${created.id}/transitions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { target: 'failed' },
      })
      expect(failedRes.status, 'queued -> failed should return 200').toBe(200)

      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, {
        headers: { Authorization: `Bearer ${token}` },
      })
      expect(res.status).toBe(200)
      const body = res.body as Record<string, unknown>
      expect(body.status, 'the fixture should now be failed').toBe('failed')
      expect(Array.isArray(body.rejection_reasons), `rejection_reasons was ${JSON.stringify(body.rejection_reasons)}, not an array`).toBe(true)
      expect(Array.isArray(body.violations), `violations was ${JSON.stringify(body.violations)}, not an array`).toBe(true)
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

    // APPR-08-10: the awaiting_approval filter's reject half, alongside the other
    // list-filter 400s rather than with the approval specs -- this is a query-param
    // contract, and it needs no policy, no run and no armed invoice.
    test('list ?awaiting_approval=maybe -> 400 {error: string}', async () => {
      const res = await rawFetch('/api/invoice/v1/invoices?awaiting_approval=maybe', {
        headers: { Authorization: `Bearer ${token}` },
      })
      assertErrorEnvelope(res, 400, 'list awaiting_approval=maybe')
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

      // [supplier-from-entity] (internal/invoice/store.go's Store.Create, INVCR-01-17):
      // supplier_tin is ALWAYS derived from the entity, overriding whatever the caller
      // sends -- so a caller-supplied supplier_tin is never actually stored. This half
      // needs its own fresh entity (one invoice under it, for a clean total) and must
      // search the STORED value, read back off the create response.
      const supplierEntityTin = freshTin()
      const supplierEntity = await createEntity(token, { name: `M4-16-02 invoice supplier-tin ${supplierEntityTin}`, tin: supplierEntityTin })
      const supplierMatch = await createInvoice(token, {
        entity_id: supplierEntity.id,
        invoice_number: `INV-L-${freshTin()}`,
      })
      const storedSupplierTin = supplierMatch.supplier_tin as string
      const supplierRes = await listInvoices(token, { q: storedSupplierTin })
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

    // BUG-05-05 (task-414): buyer_tin omitted entirely -- SQL NULL, not '' (handlers.go's
    // *string stays untouched through to store.go's bind, [buyer-tin-null-not-empty]).
    // Reuses cleanInvoiceFields minus buyer_tin so buyer-tin-required is the only rule
    // this fixture can fire.
    test('validate: an invoice with no buyer TIN stays draft and is not submittable', async () => {
      const created = await createInvoice(token, {
        entity_id: entity.id,
        ...cleanInvoiceFields(`INV-BUYERTIN-${freshTin()}`),
        buyer_tin: undefined,
      })
      expect(created.buyer_tin, 'an omitted buyer_tin must stay null, not an empty string').toBeNull()

      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}/validate`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
      })
      expect(res.status, 'a blocking verdict is still a 200, never an HTTP error').toBe(200)
      const body = res.body as Record<string, unknown>
      expect(body.status, 'the missing buyer TIN should leave the invoice draft').toBe('draft')
      const violations = body.violations as { rule_key: string; severity: string }[]
      const buyerTinViolation = violations.find((v) => v.rule_key === 'buyer-tin-required')
      expect(buyerTinViolation, 'buyer-tin-required should fire for an omitted buyer_tin').toBeTruthy()
      expect(buyerTinViolation?.severity, 'buyer-tin-required is a blocking (error) rule').toBe('error')

      const getRes = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, { headers: { Authorization: `Bearer ${token}` } })
      const getBody = getRes.body as Record<string, unknown>
      expect(getBody.can_submit, 'a draft blocked by buyer-tin-required cannot be submitted').toBe(false)
      expect(typeof getBody.submit_blocked_reason, 'the submit-blocked reason should be a string').toBe('string')
      expect((getBody.submit_blocked_reason as string).length, 'the submit-blocked reason should be non-empty').toBeGreaterThan(0)
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
      // can_submit is independent of can_revalidate ([gates-on-the-wire]): both are false
      // on a draft, but for different rules (validated-only vs draft-only), and can_edit
      // stays true throughout -- proven here on the same body.
      expect(body.can_submit, 'a demoted draft cannot be submitted').toBe(false)
      expect(typeof body.submit_blocked_reason, 'the submit-blocked reason should be a string').toBe('string')
      expect((body.submit_blocked_reason as string).length, 'the submit-blocked reason should be non-empty').toBeGreaterThan(0)
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
      expect(body.can_submit, 'a validated invoice should be submittable').toBe(true)
      expect(body.submit_blocked_reason, 'a submittable invoice carries no blocked reason').toBeNull()
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

  test.describe('resolve-outside', () => {
    test('POST resolved-outside on a failed invoice with a reason -> 200 {kept_as_is_at/by/reason}; whitespace-only reason -> 400', async () => {
      const id = await createFailedInvoice(token, entity.id, `INV-RO-${freshTin()}`)
      const reason = `resolved outside the system ${freshTin()}`

      const res = await rawFetch(`/api/invoice/v1/invoices/${id}/resolved-outside`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { reason },
      })
      expect(res.status, 'resolved-outside with a real reason should return 200').toBe(200)
      const body = res.body as Record<string, unknown>
      expect(body.kept_as_is_at, 'kept_as_is_at should be stamped').not.toBeNull()
      expect(body.kept_as_is_by, 'kept_as_is_by should be stamped').not.toBeNull()
      expect(body.kept_as_is_reason, 'kept_as_is_reason should echo the reason').toBe(reason)

      const blankRes = await rawFetch(`/api/invoice/v1/invoices/${id}/resolved-outside`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { reason: '  ' },
      })
      assertErrorEnvelope(blankRes, 400, 'resolved-outside whitespace-only reason')
    })

    test('POST resolved-outside on a draft invoice -> 409 {error: string}', async () => {
      const created = await createInvoice(token, { entity_id: entity.id, invoice_number: `INV-RO-${freshTin()}` })
      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}/resolved-outside`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { reason: 'not applicable, still a draft' },
      })
      assertErrorEnvelope(res, 409, 'resolved-outside on a draft')
    })

    test('POST resolved-outside on a random UUID -> 404 {error: string}', async () => {
      const res = await rawFetch(`/api/invoice/v1/invoices/${crypto.randomUUID()}/resolved-outside`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { reason: 'does not matter' },
      })
      assertErrorEnvelope(res, 404, 'resolved-outside not-found')
    })

    test('DELETE resolved-outside on a resolved failed invoice -> 200 with the triple nulled, and again idempotently', async () => {
      const id = await createFailedInvoice(token, entity.id, `INV-RO-${freshTin()}`)
      const resolveRes = await rawFetch(`/api/invoice/v1/invoices/${id}/resolved-outside`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { reason: `resolved ${freshTin()}` },
      })
      expect(resolveRes.status, 'setup: resolving should return 200').toBe(200)

      const firstDelete = await rawFetch(`/api/invoice/v1/invoices/${id}/resolved-outside`, {
        method: 'DELETE',
        headers: { Authorization: `Bearer ${token}` },
      })
      expect(firstDelete.status, 'un-resolving should return 200').toBe(200)
      const firstBody = firstDelete.body as Record<string, unknown>
      expect(firstBody.kept_as_is_at, 'kept_as_is_at should be nulled').toBeNull()
      expect(firstBody.kept_as_is_by, 'kept_as_is_by should be nulled').toBeNull()
      expect(firstBody.kept_as_is_reason, 'kept_as_is_reason should be nulled').toBeNull()

      // Un-resolving an already-unresolved invoice is a no-op, not an error.
      const secondDelete = await rawFetch(`/api/invoice/v1/invoices/${id}/resolved-outside`, {
        method: 'DELETE',
        headers: { Authorization: `Bearer ${token}` },
      })
      expect(secondDelete.status, 'a second un-resolve should still return 200').toBe(200)
      const secondBody = secondDelete.body as Record<string, unknown>
      expect(secondBody.kept_as_is_at, 'kept_as_is_at should stay null').toBeNull()
    })

    test('GET can_resolve_outside is present on both a draft and a failed invoice; the draft is blocked with a reason', async () => {
      const draft = await createInvoice(token, { entity_id: entity.id, invoice_number: `INV-RO-${freshTin()}` })
      const failedId = await createFailedInvoice(token, entity.id, `INV-RO-${freshTin()}`)

      const draftRes = await rawFetch(`/api/invoice/v1/invoices/${draft.id}`, { headers: { Authorization: `Bearer ${token}` } })
      const draftBody = draftRes.body as Record<string, unknown>
      expect('can_resolve_outside' in draftBody, 'GET should carry can_resolve_outside on a draft').toBe(true)
      expect(draftBody.can_resolve_outside, 'a draft cannot be resolved outside the system').toBe(false)
      expect(draftBody.resolve_outside_blocked_reason, 'a blocked draft must carry a reason').not.toBeNull()

      const failedRes = await rawFetch(`/api/invoice/v1/invoices/${failedId}`, { headers: { Authorization: `Bearer ${token}` } })
      const failedBody = failedRes.body as Record<string, unknown>
      expect('can_resolve_outside' in failedBody, 'GET should carry can_resolve_outside on a failed invoice').toBe(true)
    })

    test('resolving a failed invoice never re-drives it: status and history are unchanged', async () => {
      const id = await createFailedInvoice(token, entity.id, `INV-RO-${freshTin()}`)
      const beforeHistory = await rawFetch(`/api/invoice/v1/invoices/${id}/history`, { headers: { Authorization: `Bearer ${token}` } })
      const beforeCount = (beforeHistory.body as Record<string, unknown>[]).length

      const resolveRes = await rawFetch(`/api/invoice/v1/invoices/${id}/resolved-outside`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { reason: `resolved ${freshTin()}` },
      })
      expect(resolveRes.status, 'resolving should return 200').toBe(200)

      const getRes = await rawFetch(`/api/invoice/v1/invoices/${id}`, { headers: { Authorization: `Bearer ${token}` } })
      expect((getRes.body as Record<string, unknown>).status, 'resolving must not change status').toBe('failed')

      const afterHistory = await rawFetch(`/api/invoice/v1/invoices/${id}/history`, { headers: { Authorization: `Bearer ${token}` } })
      const afterCount = (afterHistory.body as Record<string, unknown>[]).length
      expect(afterCount, 'resolving must not add a history row').toBe(beforeCount)
    })
  })

  // The transmission gate: only an admin or a reviewer may drive an invoice to NRS/MBS.
  // Only a deployed gateway proves it — the domain role resolves server-side from
  // `memberships`, which every in-process handler test stubs. The preparer token is used
  // for assertions ONLY; all setup runs as the admin, who is the only one who can do it.
  test.describe('transmission RBAC', () => {
    test('anchor: the preparer token really resolves an active preparer of this tenant', async () => {
      // ANCHOR, not new coverage (the isolation.spec.ts idiom). Every refusal below would
      // read the same for a stranger's token -- callerRole answers "" for a non-member and
      // for a suspended one, and "" is refused by the same arm. Without this, the whole
      // block would stay green if …0003 stopped being an active preparer.
      const preparerToken = await login({ ...PERSONAS.A, subject: PREPARER_SUBJECT })
      const identity = await me(preparerToken)
      expect(identity.tenant.id, 'the preparer must resolve tenant a, the one the fixtures live in').toBe(PERSONAS.A.tenantId)
      expect(identity.user.id, 'the token must resolve the seeded preparer subject').toBe(PREPARER_SUBJECT)
      expect(identity.user.role, 'the refusals below must be caused by THIS role, not by absent membership').toBe('preparer')

      // /v1/me's role query carries no status filter (tenancy/store.go), so it reads
      // 'preparer' for a SUSPENDED one too -- the other input callerRole answers "" for.
      // Only the roster row's status tells the two apart.
      const roster = await memberships(preparerToken)
      const self = roster.memberships.find((m) => m.user_id === PREPARER_SUBJECT)
      expect(self?.role, 'the roster must agree with /v1/me on the role').toBe('preparer')
      expect(self?.status, 'a suspended preparer would 403 below for the wrong reason').toBe('active')
    })

    test('a preparer cannot batch-submit a validated invoice; an admin submitting the same invoice enqueues it', async () => {
      // Every fixture is built INSIDE the test body: CI retries once, and the admin
      // submit below really enqueues, so a hoisted invoice would come back not_validated
      // on the retry and report a false failure.
      const preparerToken = await login({ ...PERSONAS.A, subject: PREPARER_SUBJECT })
      const created = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(`INV-RBAC-${freshTin()}`) })
      const validated = await validateInvoice(token, created.id)
      expect(validated.status, 'the clean fixture should promote draft -> validated').toBe('validated')

      const refused = await rawFetch('/api/invoice/v1/invoices/submissions', {
        method: 'POST',
        headers: { Authorization: `Bearer ${preparerToken}` },
        body: { invoice_ids: [created.id], idempotency_key: crypto.randomUUID() },
      })
      assertErrorEnvelope(refused, 403, 'preparer batch-submit')
      expect((refused.body as { error: string }).error, 'the 403 should carry the server sentence verbatim').toBe(NOT_APPROVER_REASON)

      // Read the status back BEFORE the admin call -- that submit is not inert.
      const after = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, { headers: { Authorization: `Bearer ${token}` } })
      expect((after.body as Record<string, unknown>).status, 'a refused submit must not move the invoice').toBe('validated')

      // The positive control: same endpoint, same body shape, same invoice, admin token.
      // Without it a mis-shaped request could 403 for reasons that have nothing to do with role.
      const allowed = await rawFetch('/api/invoice/v1/invoices/submissions', {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { invoice_ids: [created.id], idempotency_key: crypto.randomUUID() },
      })
      expect(allowed.status, 'an admin submitting the same invoice should return 200').toBe(200)
      const results = (allowed.body as Record<string, unknown>).results as Record<string, unknown>[]
      expect(results.length, 'exactly one result for one requested invoice').toBe(1)
      expect(results[0].invoice_id, 'the result should name the requested invoice').toBe(created.id)
      expect(results[0].enqueued, 'an admin submit of a validated invoice should enqueue it').toBe(true)
      expect(results[0].status, 'the enqueued invoice should now be queued').toBe('queued')
    })

    test('a preparer cannot drive a transition, and the refused invoice is unmoved', async () => {
      // Its own invoice, never the batch test's -- that one is genuinely enqueued and the
      // worker drives it onward, so a shared fixture's status readback would be a race.
      const preparerToken = await login({ ...PERSONAS.A, subject: PREPARER_SUBJECT })
      const created = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(`INV-RBAC-${freshTin()}`) })
      const validated = await validateInvoice(token, created.id)
      expect(validated.status, 'the clean fixture should promote draft -> validated').toBe('validated')

      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}/transitions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${preparerToken}` },
        body: { target: 'queued' },
      })
      assertErrorEnvelope(res, 403, 'preparer transition')
      expect((res.body as { error: string }).error, 'the 403 should carry the server sentence verbatim').toBe(NOT_APPROVER_REASON)

      const after = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, { headers: { Authorization: `Bearer ${token}` } })
      expect((after.body as Record<string, unknown>).status, 'a refused transition must not move the invoice').toBe('validated')
    })

    test("a preparer's 403 is identical for a real invoice and a random UUID (no existence oracle)", async () => {
      const preparerToken = await login({ ...PERSONAS.A, subject: PREPARER_SUBJECT })
      const created = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(`INV-RBAC-${freshTin()}`) })
      const validated = await validateInvoice(token, created.id)
      expect(validated.status, 'the clean fixture should promote draft -> validated').toBe('validated')

      const real = await rawFetch(`/api/invoice/v1/invoices/${created.id}/transitions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${preparerToken}` },
        body: { target: 'queued' },
      })
      const unknown = await rawFetch(`/api/invoice/v1/invoices/${crypto.randomUUID()}/transitions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${preparerToken}` },
        body: { target: 'queued' },
      })

      // Pin the real id at 403 FIRST: two matching 404s (or two matching 401s) would
      // satisfy the equality below without the gate existing at all.
      assertErrorEnvelope(real, 403, 'preparer transition on a real invoice')
      expect(unknown.status, 'an unknown id must answer with the same status').toBe(real.status)
      expect(unknown.body, 'an unknown id must answer with an identical body').toEqual(real.body)
    })

    test('both transmit doors refuse a preparer with the same 403 body', async () => {
      // No fixture: a random id is enough precisely because BOTH doors refuse before
      // reading a row. One shared writeError feeds them, so any per-path variation is a defect.
      const preparerToken = await login({ ...PERSONAS.A, subject: PREPARER_SUBJECT })
      const id = crypto.randomUUID()

      const transitionRes = await rawFetch(`/api/invoice/v1/invoices/${id}/transitions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${preparerToken}` },
        body: { target: 'queued' },
      })
      const batchRes = await rawFetch('/api/invoice/v1/invoices/submissions', {
        method: 'POST',
        headers: { Authorization: `Bearer ${preparerToken}` },
        body: { invoice_ids: [id], idempotency_key: crypto.randomUUID() },
      })

      assertErrorEnvelope(transitionRes, 403, 'preparer transition door')
      assertErrorEnvelope(batchRes, 403, 'preparer batch-submit door')
      expect((transitionRes.body as { error: string }).error, 'the transition door should carry the server sentence verbatim').toBe(NOT_APPROVER_REASON)
      expect(batchRes.body, 'both doors must answer with an identical body').toEqual(transitionRes.body)
    })

    test('GET can_submit forks by role on ONE validated invoice: true for an admin, false + the role reason for a preparer', async () => {
      const preparerToken = await login({ ...PERSONAS.A, subject: PREPARER_SUBJECT })
      const created = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(`INV-RBAC-${freshTin()}`) })
      const validated = await validateInvoice(token, created.id)
      expect(validated.status, 'the clean fixture should promote draft -> validated').toBe('validated')

      // The admin leg is what makes the preparer leg non-vacuous: same row, same status,
      // so the only thing that can explain the difference is the role.
      const adminRes = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, { headers: { Authorization: `Bearer ${token}` } })
      expect(adminRes.status, 'GET on a validated invoice should return 200').toBe(200)
      const adminBody = adminRes.body as Record<string, unknown>
      expect(adminBody.can_submit, 'an admin can submit a validated invoice').toBe(true)
      expect(adminBody.submit_blocked_reason, 'a submittable invoice carries no blocked reason').toBeNull()

      const preparerRes = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, { headers: { Authorization: `Bearer ${preparerToken}` } })
      expect(preparerRes.status, 'a preparer can still READ the invoice').toBe(200)
      const preparerBody = preparerRes.body as Record<string, unknown>
      expect(preparerBody.can_submit, 'a preparer cannot submit any invoice').toBe(false)
      expect(typeof preparerBody.submit_blocked_reason, 'the blocked reason should be a string').toBe('string')
      expect(preparerBody.submit_blocked_reason, 'the read flag carries the same sentence as the 403').toBe(NOT_APPROVER_REASON)
    })

    test("a preparer's submit_blocked_reason survives a queued invoice, where an admin's is null", async () => {
      const preparerToken = await login({ ...PERSONAS.A, subject: PREPARER_SUBJECT })
      const created = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(`INV-RBAC-${freshTin()}`) })
      const validated = await validateInvoice(token, created.id)
      expect(validated.status, 'the clean fixture should promote draft -> validated').toBe('validated')

      // A plain transition, not a batch submit: it enqueues no job, so nothing moves this
      // fixture off `queued` mid-test (reconciliation's queued_never_sent drift is audit-only).
      const queued = await rawFetch(`/api/invoice/v1/invoices/${created.id}/transitions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { target: 'queued' },
      })
      expect(queued.status, 'setup: an admin transition to queued should return 200').toBe(200)

      const adminRes = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, { headers: { Authorization: `Bearer ${token}` } })
      const adminBody = adminRes.body as Record<string, unknown>
      expect(adminBody.status, 'setup: the invoice should be queued').toBe('queued')
      expect(adminBody.can_submit, 'a queued invoice is not submittable').toBe(false)
      // Null for an admin because queued is not editable -- so on THIS status the role arm
      // is the only thing that can put a sentence in the field.
      expect(adminBody.submit_blocked_reason, 'an admin gets no reason on a queued invoice').toBeNull()

      const preparerRes = await rawFetch(`/api/invoice/v1/invoices/${created.id}`, { headers: { Authorization: `Bearer ${preparerToken}` } })
      const preparerBody = preparerRes.body as Record<string, unknown>
      expect(preparerBody.can_submit, 'a preparer cannot submit any invoice').toBe(false)
      expect(preparerBody.submit_blocked_reason, 'the role refusal is emitted on every status, not only the editable ones').toBe(NOT_APPROVER_REASON)
    })
  })

  // APPR-07-08 (D38): db/seed.dev.sql has NO seeded approval policy, and publishing takes
  // the tenant's SINGLE active slot tenant-wide, arming every invoice this shared tenant
  // validates. Every test below creates its own one-step policy and deletes it in a
  // `finally`, so the tenant returns to zero active versions before the next spec runs.
  test.describe('approval decision', () => {
    // armedInvoice(): a fresh one-step policy naming roleKey, published, then a clean
    // invoice validated against it -- ApplyValidation's promotion arms exactly one open
    // run with one pending step (engine.go's ArmTx). Caller must delete policyId (D38).
    async function armedInvoice(roleKey: string): Promise<{ invoiceId: string; policyId: string }> {
      const policy = await createApprovalPolicy(token, { name: freshPolicyName() })
      await putApprovalPolicyDraft(token, policy.id, { steps: [{ kind: 'approval', workflow_role_key: roleKey }] })
      await publishApprovalPolicy(token, policy.id)

      const created = await createInvoice(token, { entity_id: entity.id, ...cleanInvoiceFields(`INV-APPR-${freshTin()}`) })
      const validated = await validateInvoice(token, created.id)
      expect(validated.status, 'the clean fixture should promote draft -> validated, arming a run').toBe('validated')

      return { invoiceId: created.id, policyId: policy.id }
    }

    test('approve: 200, a single-step run closes approved', async () => {
      const { invoiceId, policyId } = await armedInvoice('cfo')
      try {
        const res = await rawFetch(`/api/invoice/v1/invoices/${invoiceId}/approvals`, {
          method: 'POST',
          headers: { Authorization: `Bearer ${token}` },
          body: { decision: 'approved' },
        })
        expect(res.status, "an admin staffed to the pending step's role can approve").toBe(200)
        const body = res.body as Record<string, unknown>
        expect(body.state, 'a single-step policy closes on its first decision').toBe('approved')
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }
    })

    test('reject: 200, the run closes rejected', async () => {
      const { invoiceId, policyId } = await armedInvoice('cfo')
      try {
        const res = await rawFetch(`/api/invoice/v1/invoices/${invoiceId}/approvals`, {
          method: 'POST',
          headers: { Authorization: `Bearer ${token}` },
          body: { decision: 'rejected', reason: 'missing purchase order' },
        })
        expect(res.status, "an admin staffed to the pending step's role can reject").toBe(200)
        const body = res.body as Record<string, unknown>
        expect(body.state, 'reject closes the run').toBe('rejected')
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }
    })

    test('400: an unrecognised decision value', async () => {
      const { invoiceId, policyId } = await armedInvoice('cfo')
      try {
        const res = await rawFetch(`/api/invoice/v1/invoices/${invoiceId}/approvals`, {
          method: 'POST',
          headers: { Authorization: `Bearer ${token}` },
          body: { decision: 'maybe' },
        })
        assertErrorEnvelope(res, 400, 'unrecognised decision value')
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }
    })

    test('400: a blank reject reason', async () => {
      const { invoiceId, policyId } = await armedInvoice('cfo')
      try {
        const res = await rawFetch(`/api/invoice/v1/invoices/${invoiceId}/approvals`, {
          method: 'POST',
          headers: { Authorization: `Bearer ${token}` },
          body: { decision: 'rejected', reason: '   ' },
        })
        assertErrorEnvelope(res, 400, 'blank reject reason')
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }
    })

    test('403: a preparer cannot decide (AXIS 1)', async () => {
      const { invoiceId, policyId } = await armedInvoice('cfo')
      try {
        const preparerToken = await login({ ...PERSONAS.A, subject: PREPARER_SUBJECT })
        const res = await rawFetch(`/api/invoice/v1/invoices/${invoiceId}/approvals`, {
          method: 'POST',
          headers: { Authorization: `Bearer ${preparerToken}` },
          body: { decision: 'approved' },
        })
        assertErrorEnvelope(res, 403, 'preparer cannot decide')
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }
    })

    // --- APPR-08-06: the READ flags mirror this endpoint's own refusal ladder ---
    //
    // Each test below drives the wire flag AND the door it advertises on the same armed
    // invoice, so the two can never drift apart unobserved -- the whole point of
    // [gates-on-the-wire]. approveFlags() also asserts key PRESENCE, which is the e2e half
    // of AC #2: a Go struct tag typo would drop the key on the deployed fleet only.
    async function approveFlags(id: string, asToken: string): Promise<Record<string, unknown>> {
      const res = await rawFetch(`/api/invoice/v1/invoices/${id}`, { headers: { Authorization: `Bearer ${asToken}` } })
      expect(res.status, 'GET on an armed invoice should return 200').toBe(200)
      const body = res.body as Record<string, unknown>
      for (const k of ['can_approve', 'approve_blocked_reason', 'can_reject', 'reject_blocked_reason']) {
        expect(k in body, `GET should carry ${k}`).toBe(true)
      }
      // ONE approvalGate call feeds both pairs, so they can never diverge on the wire.
      expect(body.can_approve, 'can_approve and can_reject must agree').toBe(body.can_reject)
      expect(body.approve_blocked_reason, 'both reasons must be the same sentence').toBe(body.reject_blocked_reason)
      return body
    }

    test('GET approve flags open on an armed run, then close once the run is approved', async () => {
      const { invoiceId, policyId } = await armedInvoice('cfo')
      try {
        const armed = await approveFlags(invoiceId, token)
        expect(armed.can_approve, 'a staffed admin on an open run with a pending step can decide').toBe(true)
        expect(armed.approve_blocked_reason, 'an allowed gate names no refusal').toBeNull()

        const res = await rawFetch(`/api/invoice/v1/invoices/${invoiceId}/approvals`, {
          method: 'POST',
          headers: { Authorization: `Bearer ${token}` },
          body: { decision: 'approved' },
        })
        expect(res.status, 'the flag advertised an approve, so the door must accept it').toBe(200)

        // Approve closes the RUN and leaves the invoice validated, so the status rung
        // still passes and the run rung is what refuses -- the loop the flags close.
        const closed = await approveFlags(invoiceId, token)
        expect(closed.can_approve, 'an approved run cannot be decided again').toBe(false)
        expect(closed.approve_blocked_reason, 'the closed-run sentence, verbatim').toBe(APPROVE_RUN_CLOSED_REASON)
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }
    })

    test('GET approve flags follow a reject through the demotion to draft', async () => {
      const { invoiceId, policyId } = await armedInvoice('cfo')
      try {
        const res = await rawFetch(`/api/invoice/v1/invoices/${invoiceId}/approvals`, {
          method: 'POST',
          headers: { Authorization: `Bearer ${token}` },
          body: { decision: 'rejected', reason: 'missing purchase order' },
        })
        expect(res.status, 'setup: the reject should return 200').toBe(200)

        // Reject's demoter walks the invoice back to draft while the run stays rejected,
        // so the STATUS rung refuses here, ahead of the run rung. This is the production
        // shape TestApprovalGate_RejectedRunDemotedToDraft pins in unit form.
        const after = await approveFlags(invoiceId, token)
        expect(after.status, 'reject demotes the invoice to draft').toBe('draft')
        expect(after.can_approve, 'a draft invoice cannot be approved').toBe(false)
        expect(after.approve_blocked_reason, 'the status sentence wins over the closed-run one').toBe(APPROVE_NOT_VALIDATED_REASON)
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }
    })

    test("GET approve flags refuse a preparer (AXIS 1) with the decision door's own sentence", async () => {
      const { invoiceId, policyId } = await armedInvoice('cfo')
      try {
        const preparerToken = await login({ ...PERSONAS.A, subject: PREPARER_SUBJECT })
        const body = await approveFlags(invoiceId, preparerToken)
        expect(body.can_approve, 'a preparer cannot decide any invoice').toBe(false)
        expect(body.approve_blocked_reason, 'the role rung is first, ahead of the run rungs').toBe(APPROVE_NOT_APPROVER_REASON)
        // Distinct from the transmit door's sentence: same shape, different door.
        expect(body.approve_blocked_reason, 'the decision door has its own copy').not.toBe(NOT_APPROVER_REASON)
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }
    })

    test('GET approve flags refuse an unstaffed approver (AXIS 2), matching the 403', async () => {
      // quality_reviewer is seeded UNSTAFFED, the same fixture the AXIS-2 403 test uses.
      const { invoiceId, policyId } = await armedInvoice('quality_reviewer')
      try {
        const body = await approveFlags(invoiceId, token)
        expect(body.can_approve, 'an admin staffed to no workflow role cannot decide').toBe(false)
        expect(body.approve_blocked_reason, 'the role-holder sentence, verbatim').toBe(APPROVE_NOT_ROLE_HOLDER_REASON)
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }
    })

    test('403: an approver not staffed to the pending role (AXIS 2)', async () => {
      // quality_reviewer is seeded UNSTAFFED (db/seed.dev.sql) -- token (…0001) is an
      // active admin, so it passes AXIS 1 but fails AXIS 2 on this policy.
      const { invoiceId, policyId } = await armedInvoice('quality_reviewer')
      try {
        const res = await rawFetch(`/api/invoice/v1/invoices/${invoiceId}/approvals`, {
          method: 'POST',
          headers: { Authorization: `Bearer ${token}` },
          body: { decision: 'approved' },
        })
        assertErrorEnvelope(res, 403, 'admin not staffed to the pending role')
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }
    })

    test('404: a random uuid has no approval run', async () => {
      const res = await rawFetch(`/api/invoice/v1/invoices/${crypto.randomUUID()}/approvals`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}` },
        body: { decision: 'approved' },
      })
      assertErrorEnvelope(res, 404, 'random uuid has no run')
    })

    test('409: a second decision on an already-closed run', async () => {
      const { invoiceId, policyId } = await armedInvoice('cfo')
      try {
        const first = await rawFetch(`/api/invoice/v1/invoices/${invoiceId}/approvals`, {
          method: 'POST',
          headers: { Authorization: `Bearer ${token}` },
          body: { decision: 'approved' },
        })
        expect(first.status, 'setup: the first decision should close the single-step run').toBe(200)

        const second = await rawFetch(`/api/invoice/v1/invoices/${invoiceId}/approvals`, {
          method: 'POST',
          headers: { Authorization: `Bearer ${token}` },
          body: { decision: 'approved' },
        })
        assertErrorEnvelope(second, 409, 'second decision on a closed run')
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }
    })

    test('journey: validate -> approve -> submit, in either flag state', async () => {
      const { invoiceId, policyId } = await armedInvoice('cfo')
      try {
        const run = await getInvoiceApproval(token, invoiceId)
        expect(run.state, 'validating armed an open run').toBe('open')
        expect(run.steps, 'the one-step cfo policy arms exactly one step').toHaveLength(1)
        expect(run.steps[0].state, 'the arm-time step is pending').toBe('pending')

        const decided = await decideInvoiceApproval(token, invoiceId, { decision: 'approved' })
        expect(decided.state, 'approving the only step closes the run').toBe('approved')

        // Asserted on an APPROVED invoice specifically, which is the half true in BOTH
        // flag states: flag off, ApprovalFacts folds TransmitClear to true; flag on, the
        // approved run satisfies the gate's second disjunct. The deployed fleet runs the
        // flag OFF, so nothing here may assert a flag-ON-only fact -- an undecided
        // invoice's can_submit:false, the transitions 409 and the batch skip are all
        // unobservable here and belong to APPR-14's deployed proof.
        const detail = await rawFetch(`/api/invoice/v1/invoices/${invoiceId}`, {
          headers: { Authorization: `Bearer ${token}` },
        })
        expect(detail.status, 'GET on the approved invoice should return 200').toBe(200)
        const detailBody = detail.body as Record<string, unknown>
        expect(detailBody.can_submit, 'an approved invoice is submittable for an admin').toBe(true)
        expect(detailBody.submit_blocked_reason, 'an allowed gate names no refusal').toBeNull()

        const submitted = await rawFetch(`/api/invoice/v1/invoices/${invoiceId}/transitions`, {
          method: 'POST',
          headers: { Authorization: `Bearer ${token}` },
          body: { target: 'queued' },
        })
        expect(submitted.status, 'the wire advertised can_submit, so the door it advertises must accept').toBe(200)
        const submittedBody = submitted.body as Record<string, unknown>
        expect(submittedBody.status, 'the invoice reaches queued').toBe('queued')
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }
    })

    // APPR-13-07: the reject sibling of the approve journey above, driven on one
    // response through the typed client -- distinct from the split reject/GET pair
    // already covered separately at :1061-1075 and :1163-1183.
    test('journey: validate -> reject -> demoted to draft', async () => {
      const { invoiceId, policyId } = await armedInvoice('cfo')
      try {
        const decided = await decideInvoiceApproval(token, invoiceId, { decision: 'rejected', reason: 'missing purchase order' })
        expect(decided.state, 'reject closes the run').toBe('rejected')

        const detail = await rawFetch(`/api/invoice/v1/invoices/${invoiceId}`, {
          headers: { Authorization: `Bearer ${token}` },
        })
        expect(detail.status, 'GET on the demoted invoice should return 200').toBe(200)
        const detailBody = detail.body as Record<string, unknown>
        expect(detailBody.status, 'reject demotes the invoice to draft').toBe('draft')
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }
    })

    test('the rejection adds a status-history row', async () => {
      const { invoiceId, policyId } = await armedInvoice('cfo')
      try {
        const before = await getInvoiceHistory(token, invoiceId)

        await decideInvoiceApproval(token, invoiceId, { decision: 'rejected', reason: 'missing purchase order' })

        const after = await getInvoiceHistory(token, invoiceId)
        expect(after.length, 'the demotion writes exactly one more history row').toBe(before.length + 1)
        expect(after[after.length - 1].to_status, 'the newest row lands on draft (changed_at ASC)').toBe('draft')
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }
    })

    test('the ledger carries the reject reason verbatim', async () => {
      const { invoiceId, policyId } = await armedInvoice('cfo')
      try {
        const reason = 'buyer TIN does not match the register'
        await decideInvoiceApproval(token, invoiceId, { decision: 'rejected', reason })

        const run = await getInvoiceApproval(token, invoiceId)
        expect(run.decisions, 'a single-step run closes on exactly one decision').toHaveLength(1)
        expect(run.decisions[0].decision, 'the recorded decision is the reject').toBe('rejected')
        expect(run.decisions[0].reason, 'the reason survives to the ledger byte-identical').toBe(reason)
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }
    })

    test("a rejected run's steps stay readable after the close", async () => {
      const { invoiceId, policyId } = await armedInvoice('cfo')
      try {
        await decideInvoiceApproval(token, invoiceId, { decision: 'rejected', reason: 'missing purchase order' })

        const run = await getInvoiceApproval(token, invoiceId)
        expect(run.steps, 'the one-step run is still readable after the close').toHaveLength(1)
        expect(run.steps[0].state, 'the decided step reads rejected, not stuck pending').toBe('rejected')
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }
    })

    test('400: a blank reject reason names the field and leaves the run open', async () => {
      // :1091-1103 already pins the status; assertErrorEnvelope never inspects the
      // message, so this adds the exact sentence and confirms the run is still open --
      // not the 400 itself.
      const { invoiceId, policyId } = await armedInvoice('cfo')
      try {
        const res = await rawFetch(`/api/invoice/v1/invoices/${invoiceId}/approvals`, {
          method: 'POST',
          headers: { Authorization: `Bearer ${token}` },
          body: { decision: 'rejected', reason: '   ' },
        })
        assertErrorEnvelope(res, 400, 'blank reject reason')
        const body = res.body as Record<string, unknown>
        expect(body.error, 'the field name is in the message, verbatim').toBe('reason is required')

        const run = await getInvoiceApproval(token, invoiceId)
        expect(run.state, 'a refused decision leaves the run open').toBe('open')
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }
    })

    test('404: a validated invoice with no armed run answers 404, not an empty run', async () => {
      // Distinct from :1227-1234 (a nonexistent invoice id on POST): this is a real,
      // validated invoice with no policy active, reaching ErrRunNotFound through a real
      // row lookup on the GET endpoint -- the contract the SPA's empty state depends on
      // (isNoApprovalRun, APPR-13-01). No policy is created, so no finally is needed.
      const created = await createInvoice(token, {
        entity_id: entity.id,
        ...cleanInvoiceFields(`INV-APPR-NORUN-${freshTin()}`),
      })
      const validated = await validateInvoice(token, created.id)
      expect(validated.status, 'setup: with no active policy, validate does not arm a run').toBe('validated')

      const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}/approval`, {
        headers: { Authorization: `Bearer ${token}` },
      })
      assertErrorEnvelope(res, 404, 'validated invoice with no armed run')
    })

    // --- APPR-08-10: the flag-OFF observable surface -------------------------
    //
    // Everything below is true with APPROVALS_ENFORCED unset, which is what the fleet
    // runs. The four detail keys, the per-row approval envelope and the awaiting_approval
    // filter all ship UNFLAGGED by design (docs/approvals.md, "Not gated") -- the flag
    // gates ENFORCEMENT, not visibility.

    test('contract: the four approval flags are present and typed', async () => {
      // approveFlags() already asserts PRESENCE and the approve/reject agreement; this
      // adds only the types, so the presence loop is not duplicated. Driven over TWO
      // shapes so both arms of `string | null` are actually observed: an armed invoice a
      // staffed admin can decide (booleans true, reasons null) and a bare draft the status
      // rung refuses (booleans false, reasons strings). One shape alone would leave half
      // the declared type unexercised.
      const assertTypes = (body: Record<string, unknown>, label: string) => {
        for (const k of ['can_approve', 'can_reject']) {
          expect(typeof body[k], `${label}: ${k} should be a boolean`).toBe('boolean')
        }
        for (const k of ['approve_blocked_reason', 'reject_blocked_reason']) {
          const v = body[k]
          expect(v === null || typeof v === 'string', `${label}: ${k} should be string | null, was ${JSON.stringify(v)}`).toBe(true)
        }
      }

      const { invoiceId, policyId } = await armedInvoice('cfo')
      try {
        const armed = await approveFlags(invoiceId, token)
        assertTypes(armed, 'armed invoice')
        expect(armed.can_approve, 'a staffed admin on an open run can decide').toBe(true)
        expect(armed.approve_blocked_reason, 'an allowed gate names no refusal').toBeNull()
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }

      const draft = await createInvoice(token, {
        entity_id: entity.id,
        ...cleanInvoiceFields(`INV-APPR-TYPES-${freshTin()}`),
      })
      const bare = await approveFlags(draft.id, token)
      assertTypes(bare, 'bare draft')
      expect(bare.can_approve, 'a draft with no run cannot be decided').toBe(false)
      expect(typeof bare.approve_blocked_reason, 'a refused gate names a sentence, not null').toBe('string')
    })

    test('contract: list rows carry the approval key', async () => {
      const { invoiceId, policyId } = await armedInvoice('cfo')
      try {
        // A DRAFT created after the publish sweep is never armed (arming is
        // ApplyValidation's promoting branch), so it is the deterministic source of an
        // explicit null -- the shared dev tenant's older rows are not.
        const unarmed = await createInvoice(token, {
          entity_id: entity.id,
          ...cleanInvoiceFields(`INV-APPR-ROW-${freshTin()}`),
        })

        const res = await rawFetch('/api/invoice/v1/invoices?limit=50', {
          headers: { Authorization: `Bearer ${token}` },
        })
        expect(res.status, 'list should return 200').toBe(200)
        const body = res.body as Record<string, unknown>
        expect(Object.keys(body).sort(), 'the envelope still has exactly two keys').toEqual(['invoices', 'pagination'])

        const rows = body.invoices as Record<string, unknown>[]
        for (const row of rows) {
          expect('approval' in row, `row ${row.id} should carry the approval key`).toBe(true)
          // APPR-12-09 (A09-13): the approve pair rides every row too, and the REJECT
          // pair must NOT (U5a). `in`, not a truthiness read: an omitted key and an
          // explicit false are different answers on a permission flag.
          expect('can_approve' in row, `row ${row.id} should carry can_approve`).toBe(true)
          expect('approve_blocked_reason' in row, `row ${row.id} should carry approve_blocked_reason`).toBe(true)
          expect('can_reject' in row, `row ${row.id} must NOT carry can_reject`).toBe(false)
          expect('reject_blocked_reason' in row, `row ${row.id} must NOT carry reject_blocked_reason`).toBe(false)
        }

        const armedRow = rows.find((r) => r.id === invoiceId)
        expect(armedRow, 'the freshly armed invoice should be on page 1 (created_at DESC)').toBeDefined()
        const approval = armedRow?.approval as Record<string, unknown> | null
        expect(approval, 'an armed invoice carries an approval object').not.toBeNull()
        expect(approval?.run_state, 'the newest run of an armed invoice is open').toBe('open')

        // The deployed wire agrees with itself: ONE approvalGate call feeds both, so the
        // same invoice read two ways cannot answer differently. An all-false pair would
        // agree vacuously, so the allowed value is asserted too.
        const detail = await approveFlags(invoiceId, token)
        expect(armedRow?.can_approve, 'list and detail must agree on can_approve').toBe(detail.can_approve)
        expect(armedRow?.approve_blocked_reason, 'list and detail must agree on the refusal').toBe(
          detail.approve_blocked_reason,
        )
        expect(armedRow?.can_approve, 'a staffed admin on an open run can decide from the list too').toBe(true)
        expect(armedRow?.approve_blocked_reason, 'an allowed gate names no refusal').toBeNull()

        const unarmedRow = rows.find((r) => r.id === unarmed.id)
        expect(unarmedRow, 'the fresh draft should be on page 1').toBeDefined()
        expect(unarmedRow?.approval, 'a row with no run carries an explicit null, never an omitted key').toBeNull()
        // The polarity control for the armed row above.
        expect(unarmedRow?.can_approve, 'a draft cannot be approved').toBe(false)
        expect(typeof unarmedRow?.approve_blocked_reason, 'a refused gate names a sentence, not null').toBe('string')
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }
    })

    test('contract: awaiting_approval filter answers 200 and finds the armed invoice', async () => {
      const { invoiceId, policyId } = await armedInvoice('cfo')
      try {
        // INSIDE the try, deliberately: deleteApprovalPolicy in the finally deactivates
        // the version, and the filter's own EXISTS(is_active) conjunct then matches ZERO
        // rows for the whole tenant -- the same assertion after the finally is a
        // guaranteed false.
        const res = await listInvoices(token, { awaiting_approval: true, limit: 50 })
        expect(
          res.invoices.some((i) => i.id === invoiceId),
          'the armed, undecided invoice should be in the filtered page',
        ).toBe(true)
        // Never an exact total: publishing arms the WHOLE validated backlog, and this
        // environment inherits whatever earlier specs left validated.
        expect(res.pagination.total, 'the filtered total counts at least the armed invoice').toBeGreaterThanOrEqual(1)
      } finally {
        await deleteApprovalPolicy(token, policyId)
      }
    })
  })
})
