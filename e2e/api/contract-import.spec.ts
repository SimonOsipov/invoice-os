// M4-16-03 (Order 3 of 3, FINAL): the import contract spec, over the wire.
// The import surface is `multipart/form-data`, which api/client.ts's
// rawFetch CANNOT produce ([D-multipart]: rawFetch always JSON-serializes a
// present body) -- so, mirroring import.spec.ts's own precedent, this file
// drops to Node's built-in FormData/Blob/fetch directly against apiBase()
// and adapts the response into the shared RawResult shape ({status, body})
// so contract-helpers.ts's assertErrorEnvelope still applies to every
// failure case.
//
// Success bodies (201 real / 200 dry-run) are asserted SHAPE-ONLY
// ([D-success-shape-only]: errors is an array, the five counter fields are
// numeric, format is "csv") -- exact counts are import.spec.ts's job (the
// M4-03 perf/counts gate), not this contract suite's.
//
// Dedup ([D-dedup]) is the AGAINST-STORE precheck (ExistingNumbers,
// service.go), not an in-file duplicate -- a real import seeds one row, then
// a dry-run reimport of the identical (entity, invoice_number) observes the
// precheck hit. The precheck runs before the dry-run/real split (service.go:
// existing check precedes the `if dryRun` branch), so the dry-run reimport
// needs no self-heal: it never writes.
//
// Handler flow verified against internal/importer/handlers.go/service.go
// directly (not guessed from the story): identity-first-401 fires before
// ParseMultipartForm ever runs, so a missing Authorization header 401s
// regardless of form validity -- the 401 case below still sends a fully
// valid multipart body, isolating auth as the only broken input. Entity
// lookup (EntitySupplier) happens AFTER the against-store dedup precheck but
// BEFORE the dry-run/real split, so entity-not-found 404s even under
// ?dry_run=true (service.go's Import doc, point 4).
import { test, expect } from '@playwright/test'
import { login, createEntity, apiBase, PERSONAS } from './client'
import { freshTin } from './fixtures'
import { assertErrorEnvelope, type RawResult } from './contract-helpers'

// importFetch(): the multipart request seam, adapting fetch's Response into
// RawResult. Never set Content-Type manually -- fetch derives the multipart
// boundary from the FormData body itself; overriding it here would send a
// boundary-less header and break parsing server-side. token=null omits
// Authorization entirely (the 401 case).
async function importFetch(token: string | null, form: FormData, query = ''): Promise<RawResult> {
  const headers: Record<string, string> = {}
  if (token) headers.Authorization = `Bearer ${token}`
  const res = await fetch(`${apiBase()}/api/invoice/v1/imports${query}`, {
    method: 'POST',
    headers,
    body: form,
  })
  let body: unknown
  try {
    body = await res.json()
  } catch {
    body = undefined
  }
  return { status: res.status, body }
}

// IMPORT_HEADER / IMPORT_MAPPING / buildCleanCsv: own local copy (repo
// convention -- no cross-suite imports between spec files), mirroring
// import.spec.ts's proven-ready fixture cell values (single invoice, one
// line row is sufficient here -- the seed only needs to INSERT).
const IMPORT_HEADER = 'Invoice No,Issue Date,Buyer TIN,Buyer,Currency,Subtotal,VAT,Total,Item,Qty,Unit Price'
const IMPORT_MAPPING: Record<string, string> = {
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

function buildCleanCsv(num: string): string {
  const row = [num, '2026-01-15', '87654321-0002', 'M4-16 Import Buyer', 'NGN', '1000.00', '75.00', '1075.00', 'Item 1', '1', '100.00'].join(',')
  return `${IMPORT_HEADER}\n${row}`
}

function buildForm(entityId: string, num: string): FormData {
  const f = new FormData()
  f.set('entity_id', entityId)
  f.set('mapping', JSON.stringify(IMPORT_MAPPING))
  f.set('file', new Blob([buildCleanCsv(num)], { type: 'text/csv' }), 'import.csv')
  return f
}

test.describe('import contract (API E2E, over the deployed gateway)', () => {
  let token: string

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
  })

  test('real import -> 201 {errors: null|[], numeric counters, format: "csv"}', async () => {
    const entity = await createEntity(token, { name: `M4-16 imp ${freshTin()}`, tin: freshTin() })
    const res = await importFetch(token, buildForm(entity.id, `INV-${freshTin()}`))
    expect(res.status, 'a real clean-CSV import should return 201').toBe(201)
    const body = res.body as Record<string, unknown>
    expect(
      body.errors === null || Array.isArray(body.errors),
      'errors is the []RowError batch-report field: null when there are no row errors, an array otherwise',
    ).toBe(true)
    expect(typeof body.rows_total, 'rows_total should be numeric').toBe('number')
    expect(typeof body.rows_valid, 'rows_valid should be numeric').toBe('number')
    expect(typeof body.rows_invalid, 'rows_invalid should be numeric').toBe('number')
    expect(typeof body.ready_invoices, 'ready_invoices should be numeric').toBe('number')
    expect(typeof body.quarantined_invoices, 'quarantined_invoices should be numeric').toBe('number')
    expect(body.format, 'format should echo csv').toBe('csv')
  })

  test('dry-run import -> 200 {errors: null|[], id omitted}', async () => {
    const entity = await createEntity(token, { name: `M4-16 imp ${freshTin()}`, tin: freshTin() })
    const res = await importFetch(token, buildForm(entity.id, `INV-${freshTin()}`), '?dry_run=true')
    expect(res.status, 'a dry-run clean-CSV import should return 200').toBe(200)
    const body = res.body as Record<string, unknown>
    expect(
      body.errors === null || Array.isArray(body.errors),
      'errors is the []RowError batch-report field: null when there are no row errors, an array otherwise',
    ).toBe(true)
    expect(body.id, 'a dry-run import writes nothing, so id is omitted').toBeUndefined()
  })

  test('mapping missing invoice_number -> 400 {error: string}', async () => {
    const entity = await createEntity(token, { name: `M4-16 imp ${freshTin()}`, tin: freshTin() })
    const badMapping = { ...IMPORT_MAPPING }
    delete badMapping.invoice_number
    const form = new FormData()
    form.set('entity_id', entity.id)
    form.set('mapping', JSON.stringify(badMapping))
    form.set('file', new Blob([buildCleanCsv(`INV-${freshTin()}`)], { type: 'text/csv' }), 'import.csv')
    const res = await importFetch(token, form)
    assertErrorEnvelope(res, 400, 'mapping missing invoice_number')
  })

  test('unrecognized file format -> 400 {error: string}', async () => {
    const entity = await createEntity(token, { name: `M4-16 imp ${freshTin()}`, tin: freshTin() })
    const form = new FormData()
    form.set('entity_id', entity.id)
    form.set('mapping', JSON.stringify(IMPORT_MAPPING))
    form.set('file', new Blob([buildCleanCsv(`INV-${freshTin()}`)], { type: 'application/octet-stream' }), 'import.dat')
    const res = await importFetch(token, form)
    assertErrorEnvelope(res, 400, 'unrecognized file format')
  })

  test('entity not in tenant, even on dry_run -> 404 {error: string}', async () => {
    const res = await importFetch(token, buildForm(crypto.randomUUID(), `INV-${freshTin()}`), '?dry_run=true')
    assertErrorEnvelope(res, 404, 'entity not in tenant (dry_run)')
  })

  test('oversized upload (> 10 MiB) -> 413 {error: string}', async () => {
    const entity = await createEntity(token, { name: `M4-16 imp ${freshTin()}`, tin: freshTin() })
    const form = new FormData()
    form.set('entity_id', entity.id)
    form.set('mapping', JSON.stringify(IMPORT_MAPPING))
    // 11 MiB > the 10 MiB maxUploadBytes cap (handlers.go) -- unambiguously over.
    form.set('file', new Blob(['a'.repeat(11 * 1024 * 1024)], { type: 'text/csv' }), 'big.csv')
    const res = await importFetch(token, form)
    assertErrorEnvelope(res, 413, 'oversized upload')
  })

  test('no auth -> 401 {error: string}', async () => {
    const entity = await createEntity(token, { name: `M4-16 imp ${freshTin()}`, tin: freshTin() })
    const res = await importFetch(null, buildForm(entity.id, `INV-${freshTin()}`))
    assertErrorEnvelope(res, 401, 'no auth')
  })

  test('dedup: against-store, not in-file -- a seeded real import is caught by a dry-run reimport', async () => {
    const entity = await createEntity(token, { name: `M4-16 imp ${freshTin()}`, tin: freshTin() })
    const num = `INV-DUP-${freshTin()}`

    const first = await importFetch(token, buildForm(entity.id, num))
    expect(first.status, 'the seeding import should succeed').toBe(201)

    const second = await importFetch(token, buildForm(entity.id, num), '?dry_run=true')
    expect(second.status, 'the dry-run reimport of the same (entity, number) should still return 200').toBe(200)
    const body = second.body as Record<string, unknown>
    expect(body.errors, 'errors should contain the against-store duplicate hit').toContainEqual(
      expect.objectContaining({ rule_key: 'no-duplicate-invoice-number', severity: 'error' }),
    )
  })
})
