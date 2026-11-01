// M4-07-05 (task-159, Order 5 of 5, FINAL): the dashboard rollup contract, over the
// wire -- through the SAME typed seam (api/client.ts) every api/ spec shares. Setup
// goes through the typed wrappers (createEntity, createInvoice, validateInvoice) +
// freshTin() (M3-14's isolation convention), exactly like contract-portfolio.spec.ts.
//
// The deployed seed (db/seed.dev.sql) now curates 10 business_entities, 6 of them with
// their own seeded invoices too (persona-handoff-fix step 4, [demo-invoice-seed]) -- so
// top_violations/needs_attention are NOT empty on a fresh DB. Every assertion below is
// still scoped to entities THIS FILE creates (Decision [e2e-relative-assertions]): no
// absolute firm-wide count is ever asserted, since the deployed DB is shared with every
// other spec run against this fleet, the seeded demo fixture now included.
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
// No helper framework beyond assertErrorEnvelope(), imported from ./contract-helpers, shared across the
// api/ contract specs (contract-portfolio.spec.ts, contract-validation.spec.ts,
// auth-contract.spec.ts).
import { test, expect } from '@playwright/test'
import {
  login,
  createEntity,
  createInvoice,
  validateInvoice,
  rollup,
  rollupHistory,
  approveUntilClosed,
  firmApproverTokens,
  getEntity,
  rawFetch,
  apiBase,
  PERSONAS,
  type Entity,
  type RollupHistoryRow,
} from './client'
import { freshTin } from './fixtures'
import { assertErrorEnvelope, ensureFirmPolicyActive } from './contract-helpers'

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

// Own copies of contract-invoice.spec.ts's fixtures (no cross-spec imports).
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

async function createFailedInvoice(tok: string, entityId: string, invoiceNumber: string): Promise<string> {
  const created = await createInvoice(tok, { entity_id: entityId, ...cleanInvoiceFields(invoiceNumber) })
  await validateInvoice(tok, created.id)
  await approveUntilClosed(created.id, await firmApproverTokens())
  for (const target of ['queued', 'failed']) {
    const res = await rawFetch(`/api/invoice/v1/invoices/${created.id}/transitions`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${tok}` },
      body: { target },
    })
    expect(res.status, `createFailedInvoice setup: -> ${target} should return 200`).toBe(200)
  }
  return created.id
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

  test('DASH-46: totals.awaiting_approval is a number and counts is still exactly seven keys', async () => {
    const data = await rollup(tokenA)

    // A shared deployed DB, so the VALUE is never asserted -- only that the key is
    // on the wire, is a number, and never went negative. DASH-41's seven-key set is
    // re-read here because awaiting_approval is a Bucket SIBLING of needs_attention:
    // folding it into counts would make it an eighth state and double-count the same
    // invoice in the donut.
    expect(typeof data.totals.awaiting_approval, 'totals.awaiting_approval should be a number').toBe('number')
    expect(data.totals.awaiting_approval).toBeGreaterThanOrEqual(0)

    const counts = data.totals.counts as unknown as Record<string, unknown>
    expect(Object.keys(counts).sort()).toEqual(
      ['accepted', 'draft', 'failed', 'queued', 'rejected', 'submitted', 'validated'].sort(),
    )

    // Bucket is embedded anonymously in Client, so the key promotes onto every row.
    // entityFewer guarantees clients is non-empty -- without it the loop below would
    // pass vacuously on a tenant with no invoices.
    expect(data.clients.length, 'clients should carry at least the beforeAll fixture entity').toBeGreaterThan(0)
    for (const client of data.clients) {
      expect(
        typeof client.awaiting_approval,
        `clients[${client.entity_id}].awaiting_approval should be a number`,
      ).toBe('number')
      expect(client.awaiting_approval).toBeGreaterThanOrEqual(0)
      const clientCounts = client.counts as unknown as Record<string, unknown>
      expect(Object.keys(clientCounts).sort()).toEqual(
        ['accepted', 'draft', 'failed', 'queued', 'rejected', 'submitted', 'validated'].sort(),
      )
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

// WAT is UTC+1 with no DST; the snapshotter keys rows by the WAT day (dashboard.WATDay).
function watDay(offsetDays = 0): string {
  return new Date(Date.now() + 3600_000 + offsetDays * 86_400_000).toISOString().slice(0, 10)
}

// The boot snapshot runs at reconciliation start; poll until persona A has a row.
async function nonEmptyHistory(token: string, days?: number): Promise<RollupHistoryRow[]> {
  let rows: RollupHistoryRow[] = []
  await expect
    .poll(
      async () => {
        rows = (await rollupHistory(token, days)).history
        return rows.length
      },
      { message: 'history holds at least one snapshot row', timeout: 30_000 },
    )
    .toBeGreaterThanOrEqual(1)
  return rows
}

const HISTORY_KEYS = ['day', 'entity_id', 'needs_attention', 'not_submitted', 'readiness_num', 'total', 'vat_kobo']

test.describe('dashboard rollup history contract (API E2E, over the deployed gateway)', () => {
  let tokenA: string

  test.beforeAll(async () => {
    tokenA = await login(PERSONAS.A)
  })

  test('DASH-47: history is reachable through the gateway and holds the boot snapshot', async () => {
    await nonEmptyHistory(tokenA)
    const res = await fetch(`${apiBase()}/api/dashboard/v1/rollup/history`, {
      headers: { Authorization: `Bearer ${tokenA}` },
    })
    expect(res.status, 'history should return 200').toBe(200)
    expect(res.headers.get('content-type'), 'history should respond with JSON').toContain('application/json')
    const body = (await res.json()) as { history: unknown[] }
    expect(Object.keys(body)).toEqual(['history'])
    expect(Array.isArray(body.history)).toBe(true)
    expect(body.history.length).toBeGreaterThanOrEqual(1)
  })

  test('DASH-48: every row carries exactly the seven keys, integers bounded by total', async () => {
    const rows = await nonEmptyHistory(tokenA)
    for (const row of rows) {
      expect(Object.keys(row).sort()).toEqual(HISTORY_KEYS)
      expect(row.day).toMatch(/^\d{4}-\d{2}-\d{2}$/)
      expect(typeof row.entity_id).toBe('string')
      for (const n of [row.total, row.readiness_num, row.needs_attention, row.not_submitted]) {
        expect(Number.isInteger(n), `${n} should be an integer`).toBe(true)
        expect(n).toBeGreaterThanOrEqual(0)
      }
      // A net of credit notes can be negative.
      expect(Number.isInteger(row.vat_kobo), `${row.vat_kobo} should be an integer`).toBe(true)
      expect(row.readiness_num).toBeLessThanOrEqual(row.total)
      expect(row.needs_attention).toBeLessThanOrEqual(row.total)
      expect(row.not_submitted).toBeLessThanOrEqual(row.total)
    }
  })

  test('DASH-49: rows are oldest first and the latest day is today or yesterday in WAT', async () => {
    const before = watDay(-1)
    const rows = await nonEmptyHistory(tokenA)
    const after = watDay()
    for (let i = 1; i < rows.length; i++) {
      const prev = rows[i - 1]!
      const cur = rows[i]!
      const ordered = prev.day < cur.day || (prev.day === cur.day && prev.entity_id <= cur.entity_id)
      expect(ordered, `row ${i} (${cur.day}, ${cur.entity_id}) must not precede row ${i - 1}`).toBe(true)
    }
    const latest = rows.reduce((max, r) => (r.day > max ? r.day : max), '')
    // The window brackets the request, so a run across WAT midnight cannot flip the bound.
    expect(latest >= before && latest <= after, `latest ${latest} should lie in [${before}, ${after}]`).toBe(true)
  })

  test('DASH-50: history stays inside the caller\'s tenant', async () => {
    const tokenB = await login(PERSONAS.B)
    const rowsA = await nonEmptyHistory(tokenA)
    const idsA = new Set(rowsA.map((r) => r.entity_id))
    const idsB = new Set((await nonEmptyHistory(tokenB)).map((r) => r.entity_id))
    expect(idsA.size).toBeGreaterThan(0)
    for (const id of idsA) {
      const entity = await getEntity(tokenA, id)
      expect(entity.id).toBe(id)
    }
    for (const id of idsA) expect(idsB.has(id), `B must not see A entity ${id}`).toBe(false)
  })

  test('DASH-51: days is validated and bounds the window', async () => {
    const auth = { Authorization: `Bearer ${tokenA}` }
    for (const bad of ['0', 'abc']) {
      const res = await rawFetch(`/api/dashboard/v1/rollup/history?days=${bad}`, { headers: auth })
      assertErrorEnvelope(res, 400, `history days=${bad}`)
    }

    const full = await nonEmptyHistory(tokenA)
    // The server picks its own WAT today during the call, so bracket the request with the client's.
    const todayBefore = watDay()
    const one = (await rollupHistory(tokenA, 1)).history
    const todayAfter = watDay()
    for (const row of one) expect(row.day >= todayBefore, `days=1 row ${row.day} must be ${todayBefore} or later`).toBe(true)
    const fullKeys = new Set(full.map((r) => `${r.day}|${r.entity_id}`))
    for (const row of one) expect(fullKeys.has(`${row.day}|${row.entity_id}`), 'days=1 rows are a subset of the default read').toBe(true)
    if (one.length === 0) {
      const latest = full.reduce((max, r) => (r.day > max ? r.day : max), '')
      expect(latest < todayAfter, 'an empty days=1 read is allowed only when the newest snapshot day is before today').toBe(true)
    }

    // 500 clamps to dashboard.HistoryWindowDays rather than failing.
    const big = await rawFetch('/api/dashboard/v1/rollup/history?days=500', { headers: auth })
    expect(big.status).toBe(200)
  })

  test('DASH-53: only a filed document moves vat_tracked, and resolve and undo move it', async () => {
    await ensureFirmPolicyActive(tokenA)
    const tin = freshTin()
    const entity = await createEntity(tokenA, { name: `DASH-53 ${tin}`, tin })
    await createInvoice(tokenA, { entity_id: entity.id, ...cleanInvoiceFields(`DASH53-D-${tin}`) })
    const failedId = await createFailedInvoice(tokenA, entity.id, `DASH53-F-${tin}`)
    const vatTracked = async () => {
      const client = (await rollup(tokenA)).clients.find((c) => c.entity_id === entity.id)
      expect(client, 'the fresh entity should have a rollup row').toBeDefined()
      return client!.metrics.vat_tracked
    }
    const resolved = `/api/invoice/v1/invoices/${failedId}/resolved-outside`
    const auth = { Authorization: `Bearer ${tokenA}` }

    expect(await vatTracked(), 'a draft and an unresolved failed invoice are not filed').toEqual({ num: 0, den: 2 })
    const post = await rawFetch(resolved, { method: 'POST', headers: auth, body: { reason: `resolved ${tin}` } })
    expect(post.status, 'resolving should return 200').toBe(200)
    expect(await vatTracked(), 'a resolved failed invoice is filed').toEqual({ num: 7500, den: 2 })
    const del = await rawFetch(resolved, { method: 'DELETE', headers: auth })
    expect(del.status, 'un-resolving should return 200').toBe(200)
    expect(await vatTracked(), 'undoing removes its VAT').toEqual({ num: 0, den: 2 })
  })

  test('DASH-52: unauthenticated request is refused at the gateway edge', async () => {
    const res = await rawFetch('/api/dashboard/v1/rollup/history')
    assertErrorEnvelope(res, 401, 'unauthenticated rollup history')
  })
})
