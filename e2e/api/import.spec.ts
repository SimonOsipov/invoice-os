// M4-03-06 (task-107): the DEPLOYED half of the joint M4 gate #2 (AC#1) — the
// literal wall-clock/counts assertion the story cares about. Drives a 500
// invoice / 1,500-row CSV upload through the REAL HTTP/multipart/gateway path
// (POST /api/invoice/v1/imports, per cmd/invoice/main.go's mount comment —
// there is no standalone "importer" service; internal/importer.CreateHandler
// is wired onto the invoice service's mux and reached via the gateway's
// "invoice" route prefix), timing the round trip and asserting the < 60s
// budget plus the known-clean split (1500/1500/0, 500 ready/0 quarantined).
//
// This is NOT the cheap regression guard — that's the LOCAL Go perf test
// (internal/importer/perf_test.go's TestServiceImport_500InvoicePerfBudget,
// IMP-PERF-01/02), which drives the same fixture shape in-process against a
// generous 30s LOCAL budget so an N+1/O(n^2) regression in [reuse-create]
// (one invoice.Store.Create round trip per ready invoice, no batching) is
// caught BEFORE this expensive shared deploy-gate spec ever runs. This file
// is the literal AC#1 verification: dev-env.yml's `e2e` job runs
// `pnpm --filter @invoice-os/e2e test:api` (playwright.api.config.ts,
// testDir: './api'), which picks up every e2e/api/*.spec.ts including this
// one — so marking the PR ready fires this spec automatically in CI, no
// separate manual Phase 3.5 run required.
//
// Uses Node's built-in FormData/Blob/fetch directly (NOT api/client.ts's
// apiFetch, which unconditionally JSON-serializes its body — unusable for a
// multipart upload) — mirroring client.ts's own rawFetch() precedent of
// dropping to a raw fetch seam when the typed wrapper's assumptions don't
// fit. Timed with performance.now() (not Date.now() — this codebase's test
// code convention, per fixtures.ts's freshTin() comment about avoiding
// Date.now()/Math.random() in test fixtures).
import { test, expect } from '@playwright/test'
import { login, createEntity, apiBase, PERSONAS } from './client'
import { freshTin } from './fixtures'

// buildPerfCsv(): a 500-invoice / 1,500-row CSV string for ONE entity — 3
// clean line rows per invoice (header fields issue_date/buyer_tin/buyer_name/
// currency/subtotal/vat/total repeat identically within each invoice's group,
// exactly like a real CSV export), ISO dates (no [issue-date-format]
// quarantine risk), distinct invoice numbers INV-PERF-00001..00500. Mirrors
// internal/importer/perf_test.go's in-process fixture shape byte-for-byte
// (same header-field values, same buyer TIN) so both halves of this gate
// exercise the identical known-clean dataset.
function buildPerfCsv(): string {
  const header = 'Invoice No,Issue Date,Buyer TIN,Buyer,Currency,Subtotal,VAT,Total,Item,Qty,Unit Price'
  const lines: string[] = [header]
  for (let i = 1; i <= 500; i++) {
    const invNo = `INV-PERF-${String(i).padStart(5, '0')}`
    for (let line = 1; line <= 3; line++) {
      lines.push(
        [invNo, '2026-01-15', '87654321-0002', 'M4-03 Perf Buyer Co', 'NGN', '1000.00', '75.00', '1075.00', `Item ${line}`, '1', '100.00'].join(
          ',',
        ),
      )
    }
  }
  return lines.join('\n')
}

// PERF_MAPPING: canonical importer field -> this fixture's header column,
// matching the exact 11-key contract internal/importer/service.go's mapping
// step expects (see the story's Test Specs table).
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

// importResponse: the subset of POST /v1/imports's success body
// (internal/importer/handlers.go's importResponse) this spec asserts on —
// exact wire field names (rows_total/rows_valid/rows_invalid/
// ready_invoices/quarantined_invoices), confirmed against handlers.go.
interface ImportResponse {
  rows_total: number
  rows_valid: number
  rows_invalid: number
  ready_invoices: number
  quarantined_invoices: number
}

test.describe('bulk import — 500-invoice/60s perf gate (API E2E, over the deployed gateway)', () => {
  test('AC1/AC2: a 500-invoice CSV imports in < 60s with the full known-clean split (1500/1500/0, 500 ready/0 quarantined)', async () => {
    // Generous Playwright-level timeout: uploading + processing 500 invoices
    // through the real gateway/DB path is expected to take single-digit
    // seconds but this is a shared, possibly cold, dev fleet — 120s leaves
    // ample headroom above the 60s budget this test itself asserts.
    test.setTimeout(120_000)

    const token = await login(PERSONAS.A)
    const entity = await createEntity(token, { name: 'M4-03 Perf Co', tin: freshTin() })

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
    console.log(`IMP-PERF (deployed): POST /api/invoice/v1/imports (500 invoices / 1500 rows) took ${elapsed.toFixed(0)}ms`)

    expect(res.ok, `expected a 2xx response, got ${res.status}`).toBe(true)
    expect(res.status).toBe(201)

    // AC1: the literal wall-clock budget this story's gate exists to prove.
    expect(elapsed, `import took ${elapsed.toFixed(0)}ms, want < 60000ms`).toBeLessThan(60_000)

    // AC2: the batch record's counts match the fixture's known-clean split.
    const body = (await res.json()) as ImportResponse
    expect(body.rows_total).toBe(1500)
    expect(body.rows_valid).toBe(1500)
    expect(body.rows_invalid).toBe(0)
    expect(body.ready_invoices).toBe(500)
    expect(body.quarantined_invoices).toBe(0)
  })
})
