// M4-07-05 (task-159, Order 5 of 5, FINAL): the dashboard rollup contract, over the
// wire -- through the SAME typed seam (api/client.ts) every api/ spec shares. Setup
// goes through the typed wrappers (createEntity, createInvoice, validateInvoice) +
// freshTin() (M3-14's isolation convention), exactly like contract-portfolio.spec.ts.
//
// The deployed seed (db/seed.dev.sql) creates ONLY tenants + memberships -- zero
// entities, zero invoices -- so top_violations/needs_attention are EMPTY on a cold
// DB. Every assertion below is scoped to entities THIS FILE creates (Decision
// [e2e-relative-assertions]): no absolute firm-wide count is ever asserted, since the
// deployed DB is shared with every other spec run against this fleet.
//
// POST /v1/invoices with only entity_id + invoice_number (every other field
// omitted), then validateInvoice, leaves the invoice `draft` and stamps it with
// severity:"error" violations (internal/invoice/gate_test.go's
// TestGate_ValidateZeroLineItemsStaysDraftWithLineItemsRequired proves the
// stays-draft-with-violations shape; Stage 1 confirms omitting ALL optional fields,
// not just line_items, is exactly "missing required MBS content" for this fixture).
// internal/dashboard/store.go's TopViolations query has no LIMIT, so a containment
// assertion can't be pushed out by other specs' concurrent fixture data.
//
// Six tests (DASH-40..45), one test.describe, no helper framework beyond
// assertErrorEnvelope(), imported from ./contract-helpers, shared across the
// api/ contract specs (contract-portfolio.spec.ts, contract-validation.spec.ts,
// auth-contract.spec.ts).
import { test, expect } from '@playwright/test'
import {
  login,
  createEntity,
  createInvoice,
  validateInvoice,
  rollup,
  rawFetch,
  apiBase,
  PERSONAS,
  type Entity,
} from './client'
import { freshTin } from './fixtures'
import { assertErrorEnvelope } from './contract-helpers'

// brokenInvoice(): create one invoice on `entity` with only entity_id/invoice_number
// set, then validate it -- stays draft, stamped with severity:"error" violations
// (see file header). Returns the fired severity:"error" rule keys.
async function brokenInvoice(token: string, entity: Entity, label: string): Promise<string[]> {
  const created = await createInvoice(token, {
    entity_id: entity.id,
    invoice_number: `M4-07-05-${label}-${freshTin()}`,
  })
  const validated = await validateInvoice(token, created.id)
  return validated.violations.filter((v) => v.severity === 'error').map((v) => v.rule_key)
}

test.describe('dashboard rollup contract (API E2E, over the deployed gateway)', () => {
  let tokenA: string
  let entityFewer: Entity
  let errorRuleKeys: string[]

  test.beforeAll(async () => {
    tokenA = await login(PERSONAS.A)

    // entityFewer: the DASH-42/43/44 fixture -- one broken invoice, needs_attention:1.
    const tin = freshTin()
    entityFewer = await createEntity(tokenA, { name: `M4-07-05 fewer ${tin}`, tin })
    errorRuleKeys = await brokenInvoice(tokenA, entityFewer, 'fewer')
  })

  test('DASH-40: reachable through the gateway with 200, JSON content-type, and all three top-level keys present', async () => {
    const res = await fetch(`${apiBase()}/api/dashboard/v1/rollup`, {
      headers: { Authorization: `Bearer ${tokenA}` },
    })
    expect(res.status, 'rollup should return 200').toBe(200)
    expect(res.headers.get('content-type'), 'rollup should respond with JSON').toContain('application/json')

    const body = (await res.json()) as Record<string, unknown>
    expect(body).toHaveProperty('totals')
    expect(body).toHaveProperty('clients')
    expect(body).toHaveProperty('top_violations')
    // Never-null shape: dashboard.go pre-declares clients/top_violations as
    // []Client{}/[]RuleCount{}, so an empty tenant still marshals real arrays.
    expect(Array.isArray(body.clients), 'clients should be an array, never null').toBe(true)
    expect(Array.isArray(body.top_violations), 'top_violations should be an array, never null').toBe(true)
  })

  test('DASH-41: totals.counts carries exactly the 7 state keys, all numbers', async () => {
    const data = await rollup(tokenA)
    const counts = data.totals.counts as unknown as Record<string, unknown>
    // Key presence (not value) is the point here: no `omitempty` on Counts (Go side),
    // so rejected/failed must appear even when their current tenant-wide value is 0 --
    // and since this is the shared dev DB, we assert presence, never a specific value.
    expect(Object.keys(counts).sort()).toEqual(
      ['accepted', 'draft', 'failed', 'queued', 'rejected', 'submitted', 'validated'].sort(),
    )
    for (const [key, value] of Object.entries(counts)) {
      expect(typeof value, `totals.counts.${key} should be a number`).toBe('number')
    }
  })

  test('DASH-42: fresh entity appears with its own counts, and needs_attention drives exceptions-first ordering', async () => {
    // entityMore: a second fresh entity with TWO broken invoices (needs_attention:2),
    // strictly greater than entityFewer's 1 -- local to this test, used only to prove
    // exceptions-first ordering (store.go: ORDER BY needs_attention DESC) is
    // observable over the wire. Index comparison, not adjacency, so concurrent
    // fixtures from other specs can't break this.
    const tin = freshTin()
    const entityMore = await createEntity(tokenA, { name: `M4-07-05 more ${tin}`, tin })
    await brokenInvoice(tokenA, entityMore, 'more-a')
    await brokenInvoice(tokenA, entityMore, 'more-b')

    const data = await rollup(tokenA)

    const rowFewer = data.clients.find((c) => c.entity_id === entityFewer.id)
    expect(rowFewer, 'entityFewer should appear in clients').toBeDefined()
    expect(rowFewer!.entity_name).toBe(entityFewer.name)
    expect(rowFewer!.counts.draft).toBe(1)
    expect(rowFewer!.needs_attention).toBe(1)

    const rowMore = data.clients.find((c) => c.entity_id === entityMore.id)
    expect(rowMore, 'entityMore should appear in clients').toBeDefined()
    expect(rowMore!.entity_name).toBe(entityMore.name)
    expect(rowMore!.counts.draft).toBe(2)
    expect(rowMore!.needs_attention).toBe(2)

    const idxMore = data.clients.findIndex((c) => c.entity_id === entityMore.id)
    const idxFewer = data.clients.findIndex((c) => c.entity_id === entityFewer.id)
    expect(
      idxMore,
      'entityMore (needs_attention:2) should sort before entityFewer (needs_attention:1)',
    ).toBeLessThan(idxFewer)
  })

  test('DASH-43: top_violations contains the rule that fired on the broken draft', async () => {
    expect(errorRuleKeys.length, 'the broken-draft fixture should have fired at least one severity:error rule').toBeGreaterThan(0)

    const data = await rollup(tokenA)
    const hit = data.top_violations.find((rc) => errorRuleKeys.includes(rc.rule_key))
    expect(hit, `top_violations should contain one of ${JSON.stringify(errorRuleKeys)}`).toBeDefined()
    expect(hit!.invoices).toBeGreaterThanOrEqual(1)
  })

  test('DASH-44: cross-tenant isolation is symmetric -- neither tenant sees the other in rollup', async () => {
    const tokenB = await login(PERSONAS.B)

    // B's own fixture, symmetric to A's entityFewer -- a positive control (mirrors
    // isolation.spec.ts's convention): proves this isn't a "B sees nothing" false
    // pass, where the negative assertions below would hold even if RLS 404'd/
    // filtered unconditionally rather than scoping by tenant.
    const tin = freshTin()
    const entityB = await createEntity(tokenB, { name: `M4-07-05 tenant-B ${tin}`, tin })
    await brokenInvoice(tokenB, entityB, 'tenant-b')

    const rollupA = await rollup(tokenA)
    const rollupB = await rollup(tokenB)

    const aEntityIds = new Set(rollupA.clients.map((c) => c.entity_id))
    const bEntityIds = new Set(rollupB.clients.map((c) => c.entity_id))

    // Positive control: each tenant sees its own just-created entity.
    expect(aEntityIds.has(entityFewer.id), 'A should see its own entityFewer').toBe(true)
    expect(bEntityIds.has(entityB.id), 'B should see its own entityB').toBe(true)

    // The isolation proof: neither tenant's rollup contains the other's entity_id.
    expect(bEntityIds.has(entityFewer.id), 'B must not see A entityFewer').toBe(false)
    expect(aEntityIds.has(entityB.id), 'A must not see B entityB').toBe(false)
  })

  test('DASH-45: unauthenticated request is refused at the gateway edge -- 401 shared envelope', async () => {
    // No headers at all -- rawFetch applies init.headers verbatim, so omitting it
    // entirely sends no Authorization header. This is the GATEWAY's pre-routing 401
    // (internal/gateway/gateway.go's Verifier.Middleware wraps the whole router
    // before any service is reached), the same path auth-contract.spec.ts proves for
    // tenancy/portfolio/validation.
    const res = await rawFetch('/api/dashboard/v1/rollup')
    assertErrorEnvelope(res, 401, 'unauthenticated rollup')
  })
})
