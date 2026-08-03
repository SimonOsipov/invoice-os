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
//
// [upload-once]: the file no longer crosses the POST /v1/imports wire. Every
// case here previews first -- which STORES the bytes and returns a
// document_id -- and then imports that id. A dry run needs the document too:
// it decodes the bytes, it just writes nothing.
//
// The second describe covers GET /v1/documents/{id}, the download half of
// [upload-once]. It is hosted in THIS file rather than a sibling because a
// document can only be minted by POST /v1/imports/preview -- the multipart seam
// above -- and a separate spec would need a third copy of it.
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

// previewFetch(): POST /v1/imports/preview -- since [upload-once] the ONLY
// route by which a file reaches the server. Returns the whole RawResult
// because its two POST-STORE 4xx bodies carry document_id alongside error,
// which the unrecognized-format case below relies on.
async function previewFetch(token: string, csv: string, filename = 'import.csv', type = 'text/csv'): Promise<RawResult> {
  const form = new FormData()
  form.set('file', new Blob([csv], { type }), filename)
  const res = await fetch(`${apiBase()}/api/invoice/v1/imports/preview`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${token}` },
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

// uploadDocument(): preview a file and hand back the id of the document it
// stored, whatever the preview's own status was.
async function uploadDocument(token: string, csv: string, filename = 'import.csv', type = 'text/csv'): Promise<string> {
  const res = await previewFetch(token, csv, filename, type)
  const id = (res.body as Record<string, unknown> | undefined)?.document_id
  expect(typeof id, `preview should carry a document_id (status ${res.status}, body ${JSON.stringify(res.body)})`).toBe('string')
  return id as string
}

// buildForm(): the POST /v1/imports body -- three text fields, no file.
function buildForm(entityId: string, documentId: string): FormData {
  const f = new FormData()
  f.set('entity_id', entityId)
  f.set('mapping', JSON.stringify(IMPORT_MAPPING))
  f.set('document_id', documentId)
  return f
}

// downloadFetch(): GET /v1/documents/{id}. A bare fetch, never rawFetch --
// rawFetch always json()s the body and hands back neither headers nor bytes
// (client.ts:36-53). dashboard.spec.ts:68-72 is the header-reading precedent.
function downloadFetch(token: string | null, id: string, range?: string): Promise<Response> {
  const headers: Record<string, string> = {}
  if (token) headers.Authorization = `Bearer ${token}`
  if (range) headers.Range = range
  return fetch(`${apiBase()}/api/invoice/v1/documents/${id}`, { headers })
}

// asRawResult(): adapts an error Response into the shape assertErrorEnvelope reads.
async function asRawResult(res: Response): Promise<RawResult> {
  let body: unknown
  try {
    body = await res.json()
  } catch {
    body = undefined
  }
  return { status: res.status, body }
}

// expectBytes(): byte equality against the uploaded text. arrayBuffer(), never
// text() -- decoding both sides would hide an encoding change. The length check
// runs first because a bare Buffer.equals() failure prints nothing useful.
function expectBytes(actual: ArrayBuffer, want: string, label: string): void {
  const got = Buffer.from(actual)
  const expected = Buffer.from(want, 'utf8')
  expect(got.length, `${label}: byte length`).toBe(expected.length)
  expect(got.equals(expected), `${label}: bytes differ from what was uploaded`).toBe(true)
}

test.describe('import contract (API E2E, over the deployed gateway)', () => {
  let token: string

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
  })

  test('real import -> 201 {errors: null|[], numeric counters, format: "csv"}', async () => {
    const entity = await createEntity(token, { name: `M4-16 imp ${freshTin()}`, tin: freshTin() })
    const documentId = await uploadDocument(token, buildCleanCsv(`INV-${freshTin()}`))
    const res = await importFetch(token, buildForm(entity.id, documentId))
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
    // A dry run decodes the bytes, so it needs the document too -- it just
    // persists nothing, because it creates no batch.
    const documentId = await uploadDocument(token, buildCleanCsv(`INV-${freshTin()}`))
    const res = await importFetch(token, buildForm(entity.id, documentId), '?dry_run=true')
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
    const documentId = await uploadDocument(token, buildCleanCsv(`INV-${freshTin()}`))
    const form = new FormData()
    form.set('entity_id', entity.id)
    form.set('mapping', JSON.stringify(badMapping))
    form.set('document_id', documentId)
    const res = await importFetch(token, form)
    assertErrorEnvelope(res, 400, 'mapping missing invoice_number')
  })

  test('unrecognized file format -> 400 {error: string}', async () => {
    const entity = await createEntity(token, { name: `M4-16 imp ${freshTin()}`, tin: freshTin() })
    // Preview 400s on this file too, and STILL names the document it stored
    // ([error-body-carries-document-id]) -- that is what makes an unparseable
    // upload retrievable rather than only theoretically stored. Import then
    // resolves the same unrecognized format off the same row.
    const preview = await previewFetch(token, buildCleanCsv(`INV-${freshTin()}`), 'import.dat', 'application/octet-stream')
    expect(preview.status, 'preview should reject the format but still store the bytes').toBe(400)
    const documentId = (preview.body as Record<string, unknown>).document_id
    expect(typeof documentId, 'a post-store 4xx body must carry document_id').toBe('string')
    const res = await importFetch(token, buildForm(entity.id, documentId as string))
    assertErrorEnvelope(res, 400, 'unrecognized file format')
  })

  test('entity not in tenant, even on dry_run -> 404 {error: string}', async () => {
    const documentId = await uploadDocument(token, buildCleanCsv(`INV-${freshTin()}`))
    const res = await importFetch(token, buildForm(crypto.randomUUID(), documentId), '?dry_run=true')
    assertErrorEnvelope(res, 404, 'entity not in tenant (dry_run)')
  })

  test('oversized request (> 15 MiB) -> 413 {error: string}', async () => {
    const entity = await createEntity(token, { name: `M4-16 imp ${freshTin()}`, tin: freshTin() })
    const documentId = await uploadDocument(token, buildCleanCsv(`INV-${freshTin()}`))
    const form = new FormData()
    form.set('entity_id', entity.id)
    form.set('mapping', JSON.stringify(IMPORT_MAPPING))
    form.set('document_id', documentId)
    // 16 MiB > the 15 MiB maxUploadBytes cap (handlers.go) -- unambiguously
    // over. The padding rides in an unrelated part, which the handler ignores:
    // no file crosses this wire any more, and the cap bounds the WHOLE request.
    form.set('pad', new Blob(['a'.repeat(16 * 1024 * 1024)], { type: 'application/octet-stream' }), 'big.bin')
    const res = await importFetch(token, form)
    assertErrorEnvelope(res, 413, 'oversized request')
  })

  test('DOC-E2E-04: a request UNDER the 15 MiB cap still imports -> 201', async () => {
    test.setTimeout(120_000)
    const entity = await createEntity(token, { name: `DOC-01 cap ${freshTin()}`, tin: freshTin() })
    const documentId = await uploadDocument(token, buildCleanCsv(`INV-${freshTin()}`))
    const form = new FormData()
    form.set('entity_id', entity.id)
    form.set('mapping', JSON.stringify(IMPORT_MAPPING))
    form.set('document_id', documentId)
    // The mirror of the 413 above, same ignored part: 12 MiB probes the cap through
    // the real gateway without making the server decode 12 MiB of CSV. A REAL import
    // is 201 -- 200 is the dry run.
    form.set('pad', new Blob(['a'.repeat(12 * 1024 * 1024)], { type: 'application/octet-stream' }), 'big.bin')
    const res = await importFetch(token, form)
    expect(res.status, 'a 12 MiB request is under the 15 MiB cap and must still import').toBe(201)
  })

  test('no auth -> 401 {error: string}', async () => {
    const entity = await createEntity(token, { name: `M4-16 imp ${freshTin()}`, tin: freshTin() })
    const documentId = await uploadDocument(token, buildCleanCsv(`INV-${freshTin()}`))
    const res = await importFetch(null, buildForm(entity.id, documentId))
    assertErrorEnvelope(res, 401, 'no auth')
  })

  test('dedup: against-store, not in-file -- a seeded real import is caught by a dry-run reimport', async () => {
    const entity = await createEntity(token, { name: `M4-16 imp ${freshTin()}`, tin: freshTin() })
    const num = `INV-DUP-${freshTin()}`
    // ONE document, imported twice ([reupload-new-batch]): a re-import of the
    // same stored bytes is a new batch, not an error -- the duplicate is caught
    // by the against-store precheck, exactly as before.
    const documentId = await uploadDocument(token, buildCleanCsv(num))

    const first = await importFetch(token, buildForm(entity.id, documentId))
    expect(first.status, 'the seeding import should succeed').toBe(201)

    const second = await importFetch(token, buildForm(entity.id, documentId), '?dry_run=true')
    expect(second.status, 'the dry-run reimport of the same (entity, number) should still return 200').toBe(200)
    const body = second.body as Record<string, unknown>
    expect(body.errors, 'errors should contain the against-store duplicate hit').toContainEqual(
      expect.objectContaining({ rule_key: 'no-duplicate-invoice-number', severity: 'error' }),
    )
  })
})

// GET /v1/documents/{id} over the deployed gateway. internal/gateway installs only a
// Rewrite hook -- no ModifyResponse, no Transport -- so status, Range, Content-Range,
// Accept-Ranges, Content-Disposition and X-Content-Type-Options all reach these
// assertions as the handler wrote them. A divergence here IS a gateway finding.
test.describe('document download contract (API E2E, over the deployed gateway)', () => {
  let tokenA: string
  let tokenB: string

  test.beforeAll(async () => {
    tokenA = await login(PERSONAS.A)
    tokenB = await login(PERSONAS.B)
  })

  test('DOC-E2E-02: download returns the stored bytes verbatim -- including a file preview could not parse', async () => {
    const clean = buildCleanCsv(`INV-${freshTin()}`)
    const cleanId = await uploadDocument(tokenA, clean)
    const cleanRes = await downloadFetch(tokenA, cleanId)
    expect(cleanRes.status, 'a stored document downloads 200').toBe(200)
    expectBytes(await cleanRes.arrayBuffer(), clean, 'clean csv round trip')

    // [store-before-decode]: the bytes are written before the format is judged, so the
    // id the 400 names still downloads the original file.
    const unparseable = buildCleanCsv(`INV-${freshTin()}`)
    const rejected = await previewFetch(tokenA, unparseable, 'evidence.dat', 'application/octet-stream')
    expect(rejected.status, 'an unrecognized format is refused').toBe(400)
    const rejectedId = (rejected.body as Record<string, unknown>).document_id
    expect(typeof rejectedId, 'the post-store 400 names the document it stored').toBe('string')
    const rejectedRes = await downloadFetch(tokenA, rejectedId as string)
    expect(rejectedRes.status, 'an unparseable upload is still retrievable').toBe(200)
    expectBytes(await rejectedRes.arrayBuffer(), unparseable, 'unparseable upload round trip')
  })

  test('DOC-E2E-03: the download is an opaque attachment, never inline', async () => {
    const id = await uploadDocument(tokenA, buildCleanCsv(`INV-${freshTin()}`))
    const res = await downloadFetch(tokenA, id)
    expect(res.status, 'the header probe needs a 200 to read headers off').toBe(200)
    expect(res.headers.get('content-type'), 'fixed, never the row declared_content_type').toBe('application/octet-stream')
    expect(res.headers.get('x-content-type-options')).toBe('nosniff')
    expect(res.headers.get('accept-ranges')).toBe('bytes')
    // Prefix only, never the exact filename: dedupe keeps the FIRST upload's name and
    // this DB is never reset between runs.
    expect(res.headers.get('content-disposition') ?? '', 'the disposition must be an attachment').toMatch(/^attachment/)
    await res.arrayBuffer()
  })

  test('DOC-E2E-05: a ranged download returns 206 with exactly the requested slice', async () => {
    const csv = buildCleanCsv(`INV-${freshTin()}`)
    const id = await uploadDocument(tokenA, csv)
    const res = await downloadFetch(tokenA, id, 'bytes=0-9')
    expect(res.status, 'a satisfiable Range must survive the gateway hop as a 206').toBe(206)
    expect(res.headers.get('content-range'), 'the /N suffix is the only place the full size appears on a 206').toBe(
      `bytes 0-9/${Buffer.byteLength(csv, 'utf8')}`,
    )
    // The fixture is ASCII, so a 10-character slice is the first 10 BYTES.
    expectBytes(await res.arrayBuffer(), csv.slice(0, 10), 'the first ten bytes')
  })

  test('DOC-E2E-06: a cross-tenant document id is 404 on the deployed fleet', async () => {
    const id = await uploadDocument(tokenA, buildCleanCsv(`INV-${freshTin()}`))

    // Positive control: the owner still downloads it, so the 404s below cannot come
    // from a route that refuses everyone.
    const own = await downloadFetch(tokenA, id)
    expect(own.status, "the owner's own download is still 200").toBe(200)
    await own.arrayBuffer()

    assertErrorEnvelope(await asRawResult(await downloadFetch(tokenB, id)), 404, "B downloads A's document")
    assertErrorEnvelope(await asRawResult(await downloadFetch(tokenB, crypto.randomUUID())), 404, 'an unknown id')
  })

  test('DOC-E2E-08: the document outlives the import that consumed it', async () => {
    const entity = await createEntity(tokenA, { name: `DOC-01 evidence ${freshTin()}`, tin: freshTin() })
    const csv = buildCleanCsv(`INV-${freshTin()}`)
    const documentId = await uploadDocument(tokenA, csv)
    const imported = await importFetch(tokenA, buildForm(entity.id, documentId))
    expect(imported.status, 'the consuming import should succeed').toBe(201)
    const res = await downloadFetch(tokenA, documentId)
    expect(res.status, 'the evidence is still retrievable after the import read it').toBe(200)
    expectBytes(await res.arrayBuffer(), csv, 'evidence after the import')
  })

  test('DOC-E2E-09: identical bytes uploaded twice resolve to ONE document', async () => {
    const csv = buildCleanCsv(`INV-${freshTin()}`)
    const first = await uploadDocument(tokenA, csv)
    const second = await uploadDocument(tokenA, csv)
    expect(second, 'per-tenant dedupe resolves one content hash to one row').toBe(first)
    const res = await downloadFetch(tokenA, second)
    expect(res.status, 'the reused row still serves its bytes').toBe(200)
    expectBytes(await res.arrayBuffer(), csv, 'the deduped document')
  })
})
