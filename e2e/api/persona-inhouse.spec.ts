// PERSONA-01-06 (Order 6 of 7): the seeded IN-HOUSE tenant as the SUBJECT of an api/ spec
// for the first time — through the SAME typed seam (api/client.ts) every api/ spec shares.
//
// WHY THIS FILE EXISTS. `PERSONAS.B` appears 8 times across e2e/api/'s spec files, and
// **no test in this suite has that tenant as its SUBJECT** — every appearance is a control
// propping up a NEGATIVE claim:
//   - 5 are bare `login(PERSONAS.B)` setup calls (isolation.spec.ts:57, :79, :98, :127;
//     dashboard.spec.ts:141) — neither positive nor foil on their own;
//   - 1 is already a positive identity assertion (isolation.spec.ts:67-70 — id, name,
//     kind, role), owned by that file's AC1 test;
//   - 1 is the cross-tenant foil (isolation.spec.ts:92);
//   - 1 never mints a token for this tenant at all (contract-tenancy.spec.ts:104 borrows
//     its SUBJECT id into another tenant's login, for a non-member 403 probe).
// The enclosing tests carry positive controls for it too (isolation.spec.ts:89, :101-108,
// :120-122; dashboard.spec.ts:159) — and both files say in their own comments that those
// controls exist only to keep the negative claim non-vacuous (isolation.spec.ts:104-106,
// dashboard.spec.ts:143-146). This file is the first where this tenant's own data IS the
// claim. (The parent story's "all 8 uses are isolation foils" is falsifiable by reading
// isolation.spec.ts:67-70; the statement above is the accurate one.)
//
// WHAT THIS FILE IS *NOT* ABOUT: the tenant's KIND. Nothing server-side branches on
// `tenants.kind` — it is SELECTed once (internal/tenancy/store.go:39) and echoed into the
// /me response (internal/tenancy/tenancy.go:89); no handler, query or RLS policy reads it.
// Every firm/in-house behavioural difference lives in the frontend. So these tests are
// about this tenant being EMPTY and SEPARATE, not about it being in-house, and nothing
// below asserts or implies kind-dependent server behaviour. The one legitimate `kind`
// assertion is /me echoing the seeded column straight back (IH-1).
//
// The one genuinely in-house-SHAPED risk is IH-4. frontend/app/src/components/
// InvoicesList.tsx:71 — `ctx.mode === 'inhouse' ? undefined : (ctx.active.entityId ?? …)`
// — means this persona's screen calls GET /v1/invoices with `entity_id` OMITTED ENTIRELY,
// while the firm's screen always sends it. The UNFILTERED branch of ListHandler is
// therefore the production path for THIS persona, and no api/ spec had ever driven it with
// this tenant's token. Not because the server branches on kind (it does not), but because
// the frontend calls the route differently.
//
// NAMING. Named for the SUBJECT rather than a capability: docs/e2e-convention.md:7-25
// forbids DATED files ("no dayN.spec.ts files") and fixes the organizing AXIS, explicitly
// leaving the file/directory layout to the implementation (:24-25). A persona-named api
// spec is what makes the asymmetry above visibly closed.
//
// ASSERTION DISCIPLINE. The deployed dev DB is shared and never reset
// (docs/e2e-convention.md "One browser, serial"), and this tenant's counts grow every run
// (topology/persona-surfaces.spec.ts:296-304 leaves >=2 validated invoices behind per run).
// So every assertion here is containment by an id/invoice_number THIS FILE created, a
// `>=`, a single row's own fields, or — for the rollup — a before/after DELTA measured
// inside one test. Never `toHaveLength`, never `pagination.total === n`, never
// `counts.<state> === n`.
//
// HONEST SCOPING. topology/persona-surfaces.spec.ts:296-304 already drives this tenant to
// `validated` in the BROWSER layer; IH-3 is the first to do it in the API layer, and the
// first `getInvoiceHistory` call with this tenant's token anywhere. `listEntities` and
// `listInvoices` had never been called with this tenant's token at all.
//
// Serial by INHERITANCE: playwright.api.config.ts sets fullyParallel:false / workers:1
// because the kill-switch spec mutates the GLOBAL `rules` table. There is deliberately no
// test.describe.configure override here — that setting is load-bearing for the suite.
import { test, expect } from '@playwright/test'
import {
  login,
  me,
  createEntity,
  getEntity,
  listEntities,
  createInvoice,
  validateInvoice,
  getInvoiceHistory,
  listInvoices,
  rollup,
  PERSONAS,
  type Entity,
  type ValidateInvoiceResult,
} from './client'
import { canonicalTin, freshTin } from './fixtures'

// cleanInvoiceFields(): own copy (repo convention — no cross-suite imports between spec
// files, stated at contract-invoice.spec.ts:43-48; four copies already exist), mirroring
// topology/invoice-surfaces.spec.ts's fixture of the same name VERBATIM: a canonical
// supplier TIN, VAT at the correct 7.5% of subtotal, one reconciling line item — fires
// ZERO violations against the seeded rule set, so it deterministically promotes
// draft -> validated.
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

// createValidatedInvoice(): create + validate, carrying the fixture guard both IH-3 and
// IH-5 rest on. POST .../validate is THE gate that earns `validated`, and a zero-violation
// invoice auto-promotes inside it; a BLOCKING verdict comes back as a 200 carrying
// violations as DATA, never an HTTP error — so without this assertion a rule-set change
// would silently leave the fixture `draft`, and IH-5's delta would fail much later with an
// unreadable "expected 2, received 0".
async function createValidatedInvoice(
  token: string,
  entityId: string,
  invoiceNumber: string,
): Promise<ValidateInvoiceResult> {
  const created = await createInvoice(token, { entity_id: entityId, ...cleanInvoiceFields(invoiceNumber) })
  const validated = await validateInvoice(token, created.id)
  expect(
    validated.status,
    'the clean fixture must promote draft->validated; if this fails the rule set moved under cleanInvoiceFields()',
  ).toBe('validated')
  return validated
}

// findEntityById(): pages the entity list to the end rather than assuming page 1. Unlike
// the invoice list, GET /v1/entities is ordered `name ASC, id ASC`
// (internal/portfolio/store.go:115) — NOT by recency — so a tenant that accrues a few
// entities per run against the never-reset dev DB will eventually push this file's rows
// off the default 50-row page (internal/portfolio/portfolio.go:224-234), and a
// non-containment would then read as an RLS/list defect rather than as paging. Same shape
// as perf.spec.ts:138-149's findInvoiceId; limit 200 is the server's clamp maximum.
async function findEntityById(token: string, id: string): Promise<Entity | undefined> {
  const pageSize = 200
  let offset = 0
  for (;;) {
    const { entities, pagination } = await listEntities(token, { limit: pageSize, offset })
    const hit = entities.find((e) => e.id === id)
    if (hit) return hit
    offset += pageSize
    if (offset >= pagination.total) return undefined
  }
}

test.describe('the in-house tenant as a first-class API subject (API E2E, over the deployed gateway)', () => {
  let token: string
  let entityOne: Entity
  let entityTwo: Entity
  let tinOne: string
  let invoiceNumberOne: string
  let invoiceNumberTwo: string

  // Two entities, each carrying one draft invoice, built once for the whole file: IH-4
  // needs invoices under TWO entities to prove the unfiltered list is not entity-scoped,
  // and IH-5 needs each entity to HAVE an invoice at all before it can appear in `clients`
  // (the rollup's per-entity query is `FROM invoices i JOIN business_entities e`,
  // internal/dashboard/store.go:59-60 — an entity with zero invoices produces no row).
  // Building them here rather than inside either test is what keeps IH-4 and IH-5
  // independent of each other's execution order. Every create is asserted here so a setup
  // failure reports AS setup, not as a confusing containment miss three tests later.
  test.beforeAll(async () => {
    token = await login(PERSONAS.B)

    tinOne = freshTin()
    entityOne = await createEntity(token, { name: `PERSONA-01-06 one ${tinOne}`, tin: tinOne })
    expect(entityOne.status, 'setup: entityOne should be created active').toBe('active')

    const tinTwo = freshTin()
    entityTwo = await createEntity(token, { name: `PERSONA-01-06 two ${tinTwo}`, tin: tinTwo })
    expect(entityTwo.status, 'setup: entityTwo should be created active').toBe('active')

    // Minimal drafts (entity_id + invoice_number only): these exist to be LISTED and to
    // give each entity a rollup row — not to be validated. The validated fixtures belong
    // to IH-5, created INSIDE it so its before/after delta brackets them.
    invoiceNumberOne = `INV-P01-06-ONE-${freshTin()}`
    invoiceNumberTwo = `INV-P01-06-TWO-${freshTin()}`
    const draftOne = await createInvoice(token, { entity_id: entityOne.id, invoice_number: invoiceNumberOne })
    const draftTwo = await createInvoice(token, { entity_id: entityTwo.id, invoice_number: invoiceNumberTwo })
    expect(draftOne.status, 'setup: a freshly created invoice should be draft').toBe('draft')
    expect(draftTwo.status, 'setup: a freshly created invoice should be draft').toBe('draft')
  })

  test('IH-1 (anchor): /me resolves this tenant, so nothing below can pass vacuously', async () => {
    // ANCHOR, not new coverage. The identity CONTRACT is owned by isolation.spec.ts's AC1
    // test, whose four assertions for this tenant sit at :67-70. It is repeated here as a
    // PRECONDITION — against the PERSONAS registry rather than restated literals, so this
    // can never become a second, drifting copy of that contract — because a mis-seeded or
    // mis-scoped token would otherwise make every positive assertion below vacuous.
    // `kind` is asserted only as the seeded column echoed back: no server behaviour
    // depends on it (see the file header).
    const identity = await me(token)
    expect(identity.tenant.id, 'the token must resolve the seeded in-house tenant').toBe(PERSONAS.B.tenantId)
    expect(identity.tenant.name).toBe(PERSONAS.B.name)
    expect(identity.tenant.kind).toBe(PERSONAS.B.kind)
    expect(identity.user.id, 'the caller subject is what IH-3 asserts history rows are attributed to').toBe(
      PERSONAS.B.subject,
    )
    expect(identity.user.role).toBe(PERSONAS.B.role)
  })

  test('IH-2: its own entity round-trips create -> get -> list, with the canonical TIN echoed back', async () => {
    // `listEntities` had never been called with this tenant's token anywhere in the repo.
    // The risk that protects: the LIST route's RLS-scoped SELECT and its pagination
    // envelope have only ever run against a tenant carrying dozens of seeded rows, so a
    // defect that manifests only on a near-empty portfolio (a mis-computed total, or a
    // fallback to an unscoped read) is invisible today.
    const fetched = await getEntity(token, entityOne.id)
    expect(fetched.id).toBe(entityOne.id)
    expect(fetched.name).toBe(entityOne.name)
    expect(fetched.status).toBe('active')
    // The service canonicalizes an accepted TIN to its digits-only form on write and echoes
    // THAT form back (internal/portfolio/tin.go's ValidateTIN, fixtures.ts:160-167), so the
    // hyphenated input is never what comes out.
    expect(fetched.tin, 'the echoed TIN should be the canonical digits-only form').toBe(canonicalTin(tinOne))

    // The envelope, from the caller's default request — populated, never an absolute total
    // (this tenant accumulates entities from every prior run against the un-reset dev DB).
    const { pagination } = await listEntities(token)
    expect(pagination.offset, 'the default offset should be 0').toBe(0)
    expect(pagination.limit, 'the default page size should be a positive integer').toBeGreaterThan(0)
    expect(pagination.total, "the envelope should count at least this file's two entities").toBeGreaterThanOrEqual(2)

    // Containment (paged — see findEntityById): both of this file's entities are visible in
    // this tenant's own list, and the listed row carries the same canonical TIN as the GET.
    const listedOne = await findEntityById(token, entityOne.id)
    const listedTwo = await findEntityById(token, entityTwo.id)
    expect(listedOne, 'entityOne should appear in its own tenant list').toBeDefined()
    expect(listedTwo, 'entityTwo should appear in its own tenant list').toBeDefined()
    expect(listedOne!.name).toBe(entityOne.name)
    expect(listedOne!.tin, 'the listed row should carry the same canonical TIN as the GET').toBe(canonicalTin(tinOne))
    expect(listedTwo!.name).toBe(entityTwo.name)
  })

  test('IH-3: a clean invoice under its own entity reaches validated, and history records the promotion', async () => {
    // The highest-value cell. Every invoice this suite has ever written for this tenant is
    // deliberately broken and stays `draft` (dashboard.spec.ts:44-51 is the only writer),
    // so the validate gate's promotion path and the invoice_status_history write +
    // RLS-scoped read (internal/invoice/store.go:404-427) had only ever been proven for the
    // seeded firm. `getInvoiceHistory` had never been called with this tenant's token at
    // all. Honest scoping: topology/persona-surfaces.spec.ts:296-304 already drives this
    // tenant to `validated` in the BROWSER layer — this is the first in the API layer, not
    // the first in the repo.
    const validated = await createValidatedInvoice(token, entityOne.id, `INV-P01-06-HIST-${freshTin()}`)
    expect(validated.entity_id, "the invoice should belong to this tenant's own entity").toBe(entityOne.id)

    // The success body is a BARE array, not an envelope ([history-endpoint-scope],
    // client.ts:409-415), ordered `changed_at ASC, id ASC` (internal/invoice/store.go:427)
    // — so the genesis row is first.
    const history = await getInvoiceHistory(token, validated.id)
    expect(history.length, 'history should carry the genesis row plus the promotion').toBeGreaterThanOrEqual(2)
    expect(history[0], 'the genesis row has no predecessor state').toMatchObject({
      from_status: null,
      to_status: 'draft',
    })
    const promotion = history.find((c) => c.from_status === 'draft' && c.to_status === 'validated')
    expect(promotion, 'history should record the draft -> validated promotion').toBeDefined()
    // actor is the caller's own JWT subject (internal/invoice/store.go:1105-1107, reached
    // from the gate's promote step) — IH-1 pins that subject to this tenant's persona.
    expect(promotion!.actor, "the promotion should be attributed to this tenant's own caller").toBe(
      PERSONAS.B.subject,
    )
  })

  test('IH-4: the unfiltered invoice list returns invoices across two of its entities in one list', async () => {
    // THE in-house-shaped cell. InvoicesList.tsx:71 omits `entity_id` entirely for this
    // persona (the firm's screen always sends it), so the UNFILTERED branch of ListHandler
    // is the branch this persona actually takes — hence no query at all here. This is the
    // API-layer mirror of Core AC 7, "the in-house Invoices list is deliberately not
    // entity-scoped". The entity-scoped variant is deliberately NOT added: client.ts's
    // ListInvoicesQuery does not expose entity_id (client.ts:326-331), that param is the
    // firm screen's path, and it is already asserted at
    // topology/persona-surfaces.spec.ts:450-456.
    const { invoices, pagination } = await listInvoices(token)

    // Page 1 is sufficient HERE (unlike listEntities in IH-2, which pages): this list is
    // ordered `created_at DESC, id DESC` (internal/invoice/store.go:525), this file has
    // created exactly 3 invoices for this tenant by the time this test runs, and the suite
    // is serial (workers:1) so nothing creates newer ones mid-file — accumulation only ever
    // adds OLDER rows, which sort behind them, so rotting this would take >47 NEWER rows
    // appearing mid-file. Same reasoning perf.spec.ts:132-137 documents.
    const listedOne = invoices.find((inv) => inv.invoice_number === invoiceNumberOne)
    const listedTwo = invoices.find((inv) => inv.invoice_number === invoiceNumberTwo)
    expect(listedOne, `${invoiceNumberOne} should appear in the unfiltered list`).toBeDefined()
    expect(listedTwo, `${invoiceNumberTwo} should appear in the unfiltered list`).toBeDefined()

    // The point of the cell: ONE unfiltered list carries invoices belonging to DIFFERENT
    // entities of this tenant. Asserted as each row's OWN entity_id — a "two distinct
    // values" check would pass vacuously if the field were absent on both rows.
    expect(listedOne!.entity_id).toBe(entityOne.id)
    expect(listedTwo!.entity_id).toBe(entityTwo.id)

    // Envelope populated — never an absolute total (this tenant accumulates).
    expect(pagination.offset, 'the default offset should be 0').toBe(0)
    expect(pagination.limit, 'the default page size should be a positive integer').toBeGreaterThan(0)
    expect(pagination.total, "the envelope should count at least this file's two invoices").toBeGreaterThanOrEqual(2)
  })

  test('IH-5: the rollup validated count rises by exactly the number of validated fixtures created', async () => {
    // The DELTA is the whole point of this cell. [PERSONA-01-03] asserts the rendered
    // Approvals badge EQUALS a live rollup() read (topology/persona-surfaces.spec.ts:
    // 316-317) — same route, same field on both sides — so if counts.validated were wrong,
    // both sides would be wrong TOGETHER and that test would still pass. Pinning the number
    // to fixture reality is the half the browser layer structurally cannot supply.
    //
    // An ABSOLUTE assertion here would rot: this tenant's validated count is already
    // non-zero and grows every run (persona-surfaces.spec.ts:296-304), and the dev DB is
    // never reset. The delta is sound because the api suite runs serial (workers:1) and
    // dev-env.yml runs it BEFORE the topology suite, so nothing mutates this tenant between
    // the two reads — and it stays sound on a CI retry, which re-measures its own bracket.
    const before = await rollup(token)
    // Key-set guard, presence only (never a value — the shape contract itself is
    // dashboard.spec.ts:84-96): a renamed or typo'd key would make both reads `undefined`
    // and the delta NaN, failing with a message that names nothing.
    expect(Object.keys(before.totals.counts).sort()).toEqual(
      ['accepted', 'draft', 'failed', 'queued', 'rejected', 'submitted', 'validated'].sort(),
    )

    const validatedNumbers = [`INV-P01-06-VAL-1-${freshTin()}`, `INV-P01-06-VAL-2-${freshTin()}`]
    for (const invoiceNumber of validatedNumbers) {
      await createValidatedInvoice(token, entityOne.id, invoiceNumber)
    }

    const after = await rollup(token)
    expect(
      after.totals.counts.validated - before.totals.counts.validated,
      'the tenant-wide validated count should rise by exactly the number of validated fixtures created between the two reads',
    ).toBe(validatedNumbers.length)

    // Each of this file's entities has its OWN clients row, with its own name and its own
    // counts — containment, never a clients length.
    const rowOne = after.clients.find((c) => c.entity_id === entityOne.id)
    const rowTwo = after.clients.find((c) => c.entity_id === entityTwo.id)
    expect(rowOne, 'entityOne should have its own clients row').toBeDefined()
    expect(rowTwo, 'entityTwo should have its own clients row').toBeDefined()
    expect(rowOne!.entity_name).toBe(entityOne.name)
    expect(rowTwo!.entity_name).toBe(entityTwo.name)
    // entityOne is created by this file and no other spec can reach it, but IH-3 also
    // validated one invoice under it — hence >=, not an equality.
    expect(
      rowOne!.counts.validated,
      "entityOne's own validated count should include this test's fixtures",
    ).toBeGreaterThanOrEqual(validatedNumbers.length)
  })
})
