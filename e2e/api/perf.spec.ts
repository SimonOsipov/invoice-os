// M4-04-08 (task-115), Mode A (RALPH Stage 2.5): the DEPLOYED half of this
// story's own gate -- PERF-01..07 (PERF-08 is documented, not coded here; see
// the note at the bottom of this file). Authored BEFORE any M4-04 deployment
// exists, so this file's "red" is structural: there is no dev fleet running
// this story's code yet for it to fail against, exactly as M4-03-06
// (e2e/api/import.spec.ts) was authored red before M4-03's deploy. It has NOT
// been run -- see this subtask's return for the full "could not run" note.
//
// Mirrors import.spec.ts's shape: raw fetch()/FormData (NOT client.ts's
// apiFetch, which unconditionally JSON-serializes -- unusable for multipart)
// for the upload itself, performance.now() timing (this codebase's test-code
// convention, per fixtures.ts's freshTin() comment on avoiding Date.now()),
// and one entity created via createEntity so [dedup-boundary]'s per-entity
// invoice-number uniqueness can never collide across repeated CI runs.
//
// FIXTURE SHAPE, and why it differs from M4-03-06's: M4-03-06's 500-invoice
// fixture used THREE line rows/invoice at Qty=1 x UnitPrice=100.00 each
// (sum 300.00) against Subtotal=1000.00 -- a mismatch that line-items-
// sum-subtotal (migrations/20260715120000_line_rules.sql) did not exist to
// catch when that spec was authored. That rule exists now (a v1/v2 rule,
// error severity) and importer now evaluates every ready invoice DURING
// import ([import-validates], M4-04-07) -- so reusing M4-03-06's fixture
// verbatim would report EVERY invoice as violating line-items-sum-subtotal,
// making a "known clean/violation split" impossible. This fixture instead
// uses ONE line item per invoice, Qty=10 x UnitPrice=100.00 = 1000.00,
// reconciling EXACTLY to Subtotal=1000.00 -- so line-items-sum-subtotal never
// fires, and the ONLY designed-violating rule is vat-standard-rate (VAT must
// equal 7.5% of subtotal, tolerance 0.005 --
// migrations/20260711121327_seed_mbs_v1.sql), isolated by construction: no
// other seeded v1/v2 rule reads vat or reacts to it.
//
// Supplier TIN/name are NOT CSV columns -- canonicalFields
// (internal/importer/service.go) has no supplier_tin/supplier_name key at
// all; the importer sources both from the entity itself
// ([supplier-from-entity]), so createEntity's freshTin()-generated TIN and
// name satisfy supplier-tin-required/supplier-tin-format/
// supplier-name-required for every invoice in this batch automatically.
//
// Money values avoid the F5 leading-zero trap (task-114 Stage-1: "0100" is
// READY but not a valid JSON number, so dry-run/real disagree on it) -- every
// value here is either a bare "0.00" (a single leading zero before the
// decimal point IS valid JSON) or has no leading zero at all.
import { test, expect } from '@playwright/test'
import {
  login,
  createEntity,
  apiBase,
  rawFetch,
  PERSONAS,
  listInvoices,
  getInvoice,
  validateInvoice,
  editInvoice,
  getInvoiceHistory,
} from './client'
import { freshTin } from './fixtures'
import { ACTIVE_RULE_SET_VERSION } from '../rule-set'

const TOTAL_INVOICES = 500
// The designed split PERF-02 asserts against: the first 50 invoice numbers
// (by CSV/group order) carry a deliberately wrong VAT; the remaining 450 are
// fully clean.
const VIOLATING_COUNT = 50
const CLEAN_COUNT = TOTAL_INVOICES - VIOLATING_COUNT

function buildPerfCsv(): string {
  const header = 'Invoice No,Issue Date,Buyer TIN,Buyer,Currency,Subtotal,VAT,Total,Item,Qty,Unit Price'
  const lines: string[] = [header]
  for (let i = 1; i <= TOTAL_INVOICES; i++) {
    const invNo = `INV-M4408-${String(i).padStart(5, '0')}`
    const violating = i <= VIOLATING_COUNT
    // vat-standard-rate expects vat == subtotal*0.075 (tolerance 0.005): for
    // subtotal 1000.00 that is exactly 75.00. "0.00" breaks ONLY this rule --
    // total is kept internally consistent (subtotal+vat) purely for fixture
    // hygiene, since no shipped rule cross-checks total against subtotal+vat.
    const vat = violating ? '0.00' : '75.00'
    const total = violating ? '1000.00' : '1075.00'
    lines.push(
      [invNo, '2026-01-15', '87654321-0002', 'M4-04 Perf Buyer Co', 'NGN', '1000.00', vat, total, 'Perf line', '10', '100.00'].join(
        ',',
      ),
    )
  }
  return lines.join('\n')
}

// PERF_MAPPING: the same 11-key canonical-field -> header contract
// import.spec.ts uses (internal/importer/service.go's canonicalFields), no
// supplier_tin/supplier_name key because none exists ([supplier-from-entity]
// above).
const PERF_MAPPING: Record<string, string> = {
  invoice_number: 'Invoice No',
  issue_date: 'Issue Date',
  buyer_tin: 'Buyer TIN',
  buyer_name: 'Buyer',
  currency: 'Currency',
  subtotal: 'Subtotal',
  vat: 'VAT',
  total: 'Total',
  line_description: 'Item',
  line_quantity: 'Qty',
  line_unit_price: 'Unit Price',
}

// ImportResponse: the POST /v1/imports success body this spec asserts on --
// the five M4-03 counters (internal/importer/handlers.go's importResponse)
// plus M4-04-07's four additive rule-outcome fields
// ([import-report-shape]). rule_set_version is `number | null` because the
// wire type is a Go *int with NO omitempty -- explicit JSON null when nothing
// was evaluated, never absent and never a false 0 ([Stage-1 F2]/IMPV-16).
interface ImportResponse {
  rows_total: number
  rows_valid: number
  rows_invalid: number
  ready_invoices: number
  quarantined_invoices: number
  rule_set_version: number | null
  invoices_clean: number
  invoices_with_violations: number
  invoice_violations: {
    invoice_number: string
    invoice_id: string
    rows: number[]
    violations: { rule_key: string; severity: string; message: string; path?: string }[]
  }[]
}

// findInvoiceId(): GET /v1/invoices has NO entity/invoice_number filter
// ([D8], internal/invoice/handlers.go's ListHandler doc: "No status/entity
// filters"), only limit/offset -- so this pages through (limit=200) and
// filters CLIENT-SIDE by entity_id + invoice_number, stopping once found or
// once every listed invoice has been examined. This suite runs serial
// (playwright.api.config.ts: workers:1, fullyParallel:false, [Decision A8]),
// and this test's 500 freshly-created invoices are the MOST RECENT for this
// tenant (List orders `created_at DESC, id DESC`) -- so they surface within
// the first few pages regardless of any older, accumulated dev-DB history
// from prior runs of this same gate.
async function findInvoiceId(token: string, entityId: string, invoiceNumber: string): Promise<string> {
  const pageSize = 200
  let offset = 0
  for (;;) {
    const { invoices, pagination } = await listInvoices(token, { limit: pageSize, offset })
    const hit = invoices.find((inv) => inv.entity_id === entityId && inv.invoice_number === invoiceNumber)
    if (hit) return hit.id
    offset += pageSize
    if (offset >= pagination.total) break
  }
  throw new Error(`findInvoiceId: ${invoiceNumber} for entity ${entityId} not found within ${offset} listed invoices`)
}

test.describe('bulk import+validate — 500-invoice/60s perf gate + Day-60 stamp gate (API E2E, over the deployed gateway)', () => {
  test('PERF-01..05: 500-invoice import+validate <60s, the designed clean/violation split, and the Day-60 stamp gate', async () => {
    // Generous Playwright-level timeout, well above the 60s budget this test
    // itself asserts: the import+validate call, plus several follow-up
    // GET/list/DB round trips against a possibly-cold shared dev fleet.
    test.setTimeout(150_000)

    const token = await login(PERSONAS.A)
    const entity = await createEntity(token, { name: 'M4-04 Perf Co', tin: freshTin() })

    const csv = buildPerfCsv()
    const form = new FormData()
    form.set('entity_id', entity.id)
    form.set('mapping', JSON.stringify(PERF_MAPPING))
    form.set('file', new Blob([csv], { type: 'text/csv' }), 'perf.csv')

    const start = performance.now()
    const res = await fetch(`${apiBase()}/api/invoice/v1/imports`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
      body: form,
    })
    const elapsed = performance.now() - start
    console.log(
      `PERF-01 (deployed): POST /api/invoice/v1/imports (500 invoices, import+validate combined via [import-validates]) took ${elapsed.toFixed(0)}ms`,
    )

    expect(res.ok, `expected a 2xx response, got ${res.status}`).toBe(true)
    expect(res.status).toBe(201)

    // PERF-01: the <60s budget. The story's Test Specs "Then" ("elapsed < 60s;
    // warm and cold reported separately") does not say which measurement the
    // assertion binds to -- Stage-1 finding (e) resolves the ambiguity: it
    // binds to BOTH. This single unconditional assertion runs the same way
    // whether the fleet happens to be cold (this gate's first run) or warm
    // (any later CI re-run) -- there is no in-test way to force either state,
    // and M4-03-06's own precedent (29.8s cold, still under 60s) bound both
    // too. Warm vs cold is reported by reading this console.log line back
    // across two different CI runs, not by this spec computing both itself.
    expect(elapsed, `import+validate took ${elapsed.toFixed(0)}ms, want < 60000ms (binds to warm AND cold)`).toBeLessThan(
      60_000,
    )

    const body = (await res.json()) as ImportResponse
    expect(body.rows_total).toBe(TOTAL_INVOICES)
    expect(body.rows_valid).toBe(TOTAL_INVOICES)
    expect(body.rows_invalid).toBe(0)
    expect(body.ready_invoices).toBe(TOTAL_INVOICES)
    expect(body.quarantined_invoices).toBe(0)

    // PERF-02: rule_set_version is a *int, deref'd -- not `?? 0` -- so a
    // regression to null (nothing evaluated) fails LOUDLY here rather than
    // silently comparing 0 to ACTIVE_RULE_SET_VERSION and failing on the
    // wrong assertion.
    expect(body.rule_set_version, 'rule_set_version must not be null -- 500 ready invoices were evaluated').not.toBeNull()
    expect(body.rule_set_version).toBe(ACTIVE_RULE_SET_VERSION)
    expect(body.invoices_clean).toBe(CLEAN_COUNT)
    expect(body.invoices_with_violations).toBe(VIOLATING_COUNT)
    expect(body.invoice_violations).toHaveLength(VIOLATING_COUNT)
    for (const iv of body.invoice_violations) {
      expect(iv.invoice_id, 'invoice_violations[].invoice_id must be populated on a REAL import (Stage-1 F7)').not.toBe('')
      expect(iv.violations.map((v) => v.rule_key)).toEqual(['vat-standard-rate'])
    }

    // ---- PERF-04: a violating invoice from the run ----
    const violatingEntry = body.invoice_violations[0]
    const violatingInvoice = await getInvoice(token, violatingEntry.invoice_id)
    expect(violatingInvoice.status).toBe('draft')
    expect(violatingInvoice.violations.map((v) => v.rule_key)).toEqual(['vat-standard-rate'])

    // ---- PERF-03: a clean invoice from the run ----
    // The last invoice number (outside the 1..VIOLATING_COUNT violating
    // range) is designed clean.
    const cleanInvoiceNumber = `INV-M4408-${String(TOTAL_INVOICES).padStart(5, '0')}`
    const cleanInvoiceId = await findInvoiceId(token, entity.id, cleanInvoiceNumber)
    const cleanInvoice = await getInvoice(token, cleanInvoiceId)
    expect(cleanInvoice.status).toBe('validated')
    expect(cleanInvoice.violations).toEqual([])

    // PERF-03/04: rule_set_version is the ONE shared stamp the whole
    // 500-invoice batch was evaluated against ([batch-of-one]) -- already
    // parsed into `body` and already asserted === ACTIVE_RULE_SET_VERSION by
    // PERF-02 above, so no fresh call is needed here to reuse it. A fresh
    // POST .../validate is not an option for PERF-03 either way: cleanInvoice
    // is ALREADY `validated` (the importer promotes a clean invoice DURING
    // import, [import-validates]), and Gate.Validate is draft-only (409 on a
    // non-draft invoice). What IS new here is rule_set_version_id, the
    // live-stamped uuid on each invoice: asserted non-empty on both and
    // equal to each other, proving the clean and violating invoices were
    // stamped by the SAME rule-set-version row with no DB oracle needed to
    // compare against.
    expect(cleanInvoice.rule_set_version_id, 'a validated invoice must carry a stamped rule_set_version_id').toBeTruthy()
    expect(
      violatingInvoice.rule_set_version_id,
      'a gate-evaluated invoice must carry a stamped rule_set_version_id even when blocked ([error semantics])',
    ).toBeTruthy()
    expect(cleanInvoice.rule_set_version_id).toBe(violatingInvoice.rule_set_version_id)

    // ---- PERF-05: the Day-60 stamp gate -- the M4 "Ships when true" clause.
    // Correct the PERF-04 invoice over HTTP (PATCH /v1/invoices/{id},
    // M4-05-03), re-validate over HTTP (POST .../validate), then read
    // invoice_status_history over HTTP (GET .../history, M4-22-01) and
    // assert the FULL expected sequence, not "contains a draft->validated
    // entry" -- there is no DB-clock `since` baseline any more to scope the
    // match to a fresh transition, so exactness (exact length, exact order,
    // exact from/to on every row) is the replacement guard against a stale
    // transition false-positiving this assertion.
    const corrected = await editInvoice(token, violatingEntry.invoice_id, { vat: '75.00', total: '1075.00' })
    expect(corrected.status).toBe('draft')

    const revalidated = await validateInvoice(token, violatingEntry.invoice_id)
    expect(revalidated.status).toBe('validated')
    expect(revalidated.violations).toEqual([])
    expect(revalidated.rule_set_version).toBe(ACTIVE_RULE_SET_VERSION)

    // actor is deterministic on both rows: every call in this test -- the
    // import that wrote the genesis row, and the validate call just above
    // that wrote the second -- authenticates as the same PERSONAS.A token.
    const history = await getInvoiceHistory(token, violatingEntry.invoice_id)
    expect(history).toEqual([
      { from_status: null, to_status: 'draft', actor: PERSONAS.A.subject, changed_at: expect.any(String) },
      { from_status: 'draft', to_status: 'validated', actor: PERSONAS.A.subject, changed_at: expect.any(String) },
    ])

    // ---- PERF-05 negative: history is a real transition, not an echo ----
    // A spare violating invoice from the SAME batch (invoice_violations[1],
    // never [0] -- that one is the invoice corrected and promoted above) that
    // was never validated: proves the exact-sequence assertion above isn't
    // vacuously true. Must be a VIOLATING invoice, not a clean one -- all 450
    // clean invoices in this batch were already promoted draft->validated
    // DURING import ([import-validates]), so a clean invoice's history would
    // already contain that transition and prove nothing. Only a still-draft
    // violating invoice (a blocked verdict writes no promotion row) genuinely
    // has ONLY its genesis row.
    const neverValidatedEntry = body.invoice_violations[1]
    const neverValidatedHistory = await getInvoiceHistory(token, neverValidatedEntry.invoice_id)
    expect(neverValidatedHistory).toEqual([
      { from_status: null, to_status: 'draft', actor: PERSONAS.A.subject, changed_at: expect.any(String) },
    ])
  })

  // PERF-06: the fleet is healthy after deploy -- 04 booted with S2S_TOKEN set
  // ([env-wiring]), nothing crash-looping. GET /healthz/fleet is registered
  // OUTSIDE /api/ and outside the JWT verifier (internal/gateway/fleet.go's
  // doc: "a public, unauthenticated roll-up"), so no token here.
  test('PERF-06: GET /healthz/fleet is 200 "ok"', async () => {
    const res = await fetch(`${apiBase()}/healthz/fleet`)
    const body = (await res.json()) as { status: string; services: { name: string; status: string; error?: string }[] }
    expect(res.status, `fleet-health body: ${JSON.stringify(body)}`).toBe(200)
    expect(body.status).toBe('ok')
    for (const svc of body.services) {
      expect(svc.status, `service ${svc.name} is down: ${svc.error ?? ''}`).toBe('up')
    }
  })

  // PERF-07: a normal SPA user (valid JWT) gets 401 on the S2S peer surface --
  // the gateway strips X-S2S-Token before proxying ([s2s-gateway-strip]).
  //
  // MUST mint a TENANT-BEARING token (Stage-1 finding (d)): authorize()
  // (internal/gateway/gateway.go:93-99) 403s an identity with an EMPTY
  // TenantID BEFORE the request ever reaches the proxy -- a tenant-less token
  // would get 403 and prove nothing about the strip. PERSONAS.A resolves to a
  // real seeded persona (topology/targets.ts's TENANTS.a), so login() mints a
  // token carrying tenant_id -- clears authorize(), reaches
  // injectIdentity's Del(X-S2S-Token) (gateway.go:141), then 04's
  // S2SMiddleware (internal/validation/s2s.go:51-64) 401s on the missing
  // header. The check runs BEFORE the body is read (s2s.go's doc), so the
  // empty body here is deliberate -- what is being proved is unreachability,
  // not a validation-payload contract.
  test('PERF-07: a tenant-bearing SPA JWT gets 401 on /api/validation/v1/validate/batch', async () => {
    const token = await login(PERSONAS.A)
    const { status } = await rawFetch('/api/validation/v1/validate/batch', {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
      body: {},
    })
    expect(status).toBe(401)
  })
})

// PERF-08 is deliberately NOT a new test in this file: its Test Specs "Then"
// is "the whole e2e job (smoke -> api -> topology -> demo) is green", which is
// an outcome of dev-env.yml's one gated `e2e` job running every suite, not a
// new assertion this spec could make in isolation. It is satisfied by: (1)
// this file resolving rule_set_version through ../rule-set's
// ACTIVE_RULE_SET_VERSION (a FOURTH consumer -- added to that module's header
// list, [e2e-active-version]) rather than a literal; (2) the day30.spec.ts fix
// already committed (652a232) un-pinning the third, positional consumer
// Stage-1 found; and (3) api/validation.spec.ts + topology/targets.ts's
// existing resolution through the same module. There is no fifth thing to
// author here -- the gate either goes green as a whole, or Stage-1's BLOCKER
// finding was wrong, and that is for the deploy run (not Mode A) to show.
