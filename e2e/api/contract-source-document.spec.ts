// Contract spec for the two new source-document endpoints (DOC-02): GET
// /v1/invoices/{id}/source-document and GET /v1/documents/{id}/sheet.
//
// A new file, not folded into contract-import.spec.ts, matching this package's
// contract-<family>.spec.ts naming -- unlike that file's own GET /v1/documents/{id}
// describe, which stays in-file because a document can only be minted through the
// POST /v1/imports/preview seam and a sibling spec would need a second copy of it.
// Paying that cost here deliberately: importFetch/previewFetch/uploadDocument/buildForm
// below duplicate that seam rather than import it -- both files are *.spec.ts, and
// importing one into another registers its tests twice.
import { test, expect } from '@playwright/test'
import { login, createEntity, createInvoice, listInvoices, apiBase, rawFetch, PERSONAS } from './client'
import { freshTin } from './fixtures'
import { assertErrorEnvelope, type RawResult } from './contract-helpers'
import { buildMixedCsv, PERF_MAPPING } from '../importFixtures'

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
const SHA256_RE = /^[0-9a-f]{64}$/

async function previewFetch(token: string, csv: string, filename = 'import.csv'): Promise<RawResult> {
  const form = new FormData()
  form.set('file', new Blob([csv], { type: 'text/csv' }), filename)
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

async function uploadDocument(token: string, csv: string, filename = 'import.csv'): Promise<string> {
  const res = await previewFetch(token, csv, filename)
  const id = (res.body as Record<string, unknown> | undefined)?.document_id
  expect(typeof id, `preview should carry a document_id (status ${res.status})`).toBe('string')
  return id as string
}

function buildForm(entityId: string, documentId: string): FormData {
  const f = new FormData()
  f.set('entity_id', entityId)
  f.set('mapping', JSON.stringify(PERF_MAPPING))
  f.set('document_id', documentId)
  return f
}

async function importFetch(token: string, form: FormData): Promise<RawResult> {
  const res = await fetch(`${apiBase()}/api/invoice/v1/imports`, {
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

// Both reads are plain JSON and need neither response headers nor bytes, so they go
// through rawFetch like every other contract spec -- the hand-rolled seams above survive
// only because rawFetch cannot produce multipart. token=null omits Authorization.
function authHeaders(token: string | null): Record<string, string> {
  return token ? { Authorization: `Bearer ${token}` } : {}
}

function sourceDocumentFetch(token: string | null, invoiceId: string): Promise<RawResult> {
  return rawFetch(`/api/invoice/v1/invoices/${invoiceId}/source-document`, { headers: authHeaders(token) })
}

function sheetFetch(token: string | null, documentId: string): Promise<RawResult> {
  return rawFetch(`/api/invoice/v1/documents/${documentId}/sheet`, { headers: authHeaders(token) })
}

test.describe('invoice source-document contract (API E2E, over the deployed gateway)', () => {
  let token: string

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
  })

  test('source-document contract: imported invoice -> 200', async () => {
    const entity = await createEntity(token, { name: `DOC-02 src-doc ${freshTin()}`, tin: freshTin() })
    const documentId = await uploadDocument(token, buildMixedCsv(), 'doc02-mixed.csv')
    const imported = await importFetch(token, buildForm(entity.id, documentId))
    expect(imported.status, 'the mixed-batch import should return 201').toBe(201)

    const importedBody = imported.body as { invoice_violations: Array<{ invoice_number: string; invoice_id?: string }> }
    const violateEntry = importedBody.invoice_violations.find((iv) => iv.invoice_number === 'INV-UI-MIX-VIOLATE')
    expect(violateEntry, 'expected an invoice_violations entry for INV-UI-MIX-VIOLATE').toBeTruthy()
    expect(typeof violateEntry!.invoice_id, 'invoice_violations[].invoice_id must be populated').toBe('string')

    const res = await sourceDocumentFetch(token, violateEntry!.invoice_id as string)
    expect(res.status, 'source-document should return 200 for an imported invoice').toBe(200)
    const body = res.body as { source_rows: number[] | null; document: Record<string, unknown> | null }
    expect(body.document, 'an imported invoice must carry a document record').not.toBeNull()
    const record = body.document as Record<string, unknown>
    expect(record.id).toMatch(UUID_RE)
    expect(record.content_hash).toMatch(SHA256_RE)
    expect(record.size_bytes as number, 'size_bytes must be a real byte count').toBeGreaterThan(0)
    expect(typeof record.filename, 'filename must be recorded').toBe('string')
    expect((record.filename as string).length).toBeGreaterThan(0)
    // INV-UI-MIX-VIOLATE is buildMixedCsv's data row 3 (header + 2 STRUCT rows precede it),
    // which is sheet row 4 -- see importFixtures.ts's own doc comment.
    expect(body.source_rows).toEqual([4])
    expect(record.invoices_created as number, 'VIOLATE and CLEAN both landed; STRUCT was quarantined').toBeGreaterThanOrEqual(2)
    expect(record.other_invoice_rows).toContain(5)
  })

  test('source-document contract: manual invoice -> explicit nulls', async () => {
    const entity = await createEntity(token, { name: `DOC-02 manual ${freshTin()}`, tin: freshTin() })
    const invoice = await createInvoice(token, { entity_id: entity.id, invoice_number: `INV-DOC02-${freshTin()}` })

    const res = await sourceDocumentFetch(token, invoice.id)
    expect(res.status, 'a manually created invoice still 200s on source-document').toBe(200)
    const body = res.body as Record<string, unknown>
    // Go's nil slice/pointer with no `omitempty` marshals to an EXPLICIT null -- the previewer
    // must tell that apart from an absent key, so both are asserted as present keys, not just
    // falsy values.
    expect('document' in body && body.document === null, 'document must be an explicit null, not omitted').toBe(true)
    expect('source_rows' in body && body.source_rows === null, 'source_rows must be an explicit null, not omitted').toBe(true)
  })

  test('source-document contract: 401 and 404 envelopes', async () => {
    const entity = await createEntity(token, { name: `DOC-02 errs ${freshTin()}`, tin: freshTin() })
    const invoice = await createInvoice(token, { entity_id: entity.id, invoice_number: `INV-DOC02-ERR-${freshTin()}` })

    // Positive control: this exact id 200s with the real token, so the errors below cannot
    // come from a route that refuses everyone.
    const control = await sourceDocumentFetch(token, invoice.id)
    expect(control.status, "the owner's own read is still 200").toBe(200)

    assertErrorEnvelope(await sourceDocumentFetch(null, invoice.id), 401, 'no auth')
    assertErrorEnvelope(await sourceDocumentFetch(token, crypto.randomUUID()), 404, 'unknown invoice id')
    assertErrorEnvelope(await sourceDocumentFetch(token, 'not-a-uuid'), 400, 'malformed invoice id')
  })

  // The specs above all import a file first, so they prove the previewer works
  // on data the test itself created. This one asserts the DEPLOYED environment's
  // own seeded demo invoices carry a document -- the internal/demodocs boot step
  // in cmd/invoice/main.go. Without it every demo invoice reads "no source
  // document" and the populated states are unreachable outside a manual import.
  test('source-document contract: the deployed environment seeds documents onto its demo invoices', async () => {
    const list = await listInvoices(token, { q: 'DEMO-2026-', limit: 100 })
    const seeded = list.invoices.filter((i) => i.invoice_number.startsWith('DEMO-2026-'))
    expect(seeded.length, 'the deployed environment should carry db/seed.dev.sql demo invoices').toBeGreaterThan(0)

    const records = await Promise.all(
      seeded.map(async (invoice) => {
        const res = await sourceDocumentFetch(token, invoice.id)
        expect(res.status, `source-document for ${invoice.invoice_number}`).toBe(200)
        return { invoice, body: res.body as { source_rows: number[] | null; document: Record<string, unknown> | null } }
      }),
    )

    const withDocument = records.filter((r) => r.body.document !== null)
    expect(
      withDocument.length,
      'no seeded invoice carries a source document -- the demodocs boot step did not run',
    ).toBeGreaterThan(0)

    for (const { invoice, body } of withDocument) {
      const record = body.document as Record<string, unknown>
      expect(record.id, invoice.invoice_number).toMatch(UUID_RE)
      expect(record.content_hash, invoice.invoice_number).toMatch(SHA256_RE)
      expect(record.size_bytes as number, `${invoice.invoice_number} size_bytes`).toBeGreaterThan(0)
      // demodocs.filenameFor slugifies the supplier entity's name.
      expect(record.filename as string, `${invoice.invoice_number} filename`).toMatch(/^[a-z0-9-]+-invoices\.csv$/)
      // The seeded document is attributed to a real admin of the tenant, not to
      // a synthetic seeder subject -- this is what the rail renders as
      // "Uploaded by", so a fabricated uuid there would be a lying surface.
      expect(record.uploaded_by, `${invoice.invoice_number} uploaded_by`).toBe(PERSONAS.A.subject)
      // A linked invoice always carries the rows it occupies; the sheet-row
      // floor is 2 because row 1 is the header.
      expect(body.source_rows, `${invoice.invoice_number} source_rows`).not.toBeNull()
      expect((body.source_rows as number[]).length).toBeGreaterThan(0)
      for (const row of body.source_rows as number[]) {
        expect(row, `${invoice.invoice_number} sheet row`).toBeGreaterThanOrEqual(2)
      }
    }

    // Invoices with no line items are deliberately left unlinked (a row in an
    // import file IS a line item), so a mixed result is correct and an
    // all-or-nothing assertion would be wrong in both directions.
    const bySupplierFile = new Set(withDocument.map((r) => (r.body.document as Record<string, unknown>).filename))
    expect(bySupplierFile.size, 'demo documents should be one file per supplier entity, not one global file').toBeGreaterThan(1)
  })
})

test.describe('document sheet contract (API E2E, over the deployed gateway)', () => {
  let tokenA: string
  let tokenB: string

  test.beforeAll(async () => {
    tokenA = await login(PERSONAS.A)
    tokenB = await login(PERSONAS.B)
  })

  test('sheet contract: 200 shape', async () => {
    const documentId = await uploadDocument(tokenA, buildMixedCsv(), 'doc02-sheet-shape.csv')
    const res = await sheetFetch(tokenA, documentId)
    expect(res.status, 'the sheet endpoint should return 200 for a stored document').toBe(200)
    const body = res.body as {
      format: string
      truncated: boolean
      rows_total: number
      rows_returned: number
      rows: unknown[]
      columns: unknown[]
    }
    expect(body.format).toBe('csv')
    // rows.length === rows_total only holds below the 5,000-row cap -- assert the triple
    // instead, so this stays correct if the fixture ever grows past it.
    expect(body.truncated).toBe(false)
    expect(body.rows_returned).toBe(body.rows_total)
    expect(body.rows.length).toBe(body.rows_returned)
    expect(body.rows_total, 'buildMixedCsv has 4 data rows').toBe(4)
    expect(body.columns.length, 'PERF_HEADER has 11 columns').toBe(11)
    for (const c of body.columns) {
      expect(typeof c).toBe('string')
      expect((c as string).length).toBeGreaterThan(0)
    }
    for (const r of body.rows) {
      expect(Array.isArray(r)).toBe(true)
    }
  })

  test('sheet contract: cross-tenant is 404, and A still reads it', async () => {
    const documentId = await uploadDocument(tokenA, buildMixedCsv(), 'doc02-sheet-tenant.csv')

    // Positive control: the owner still reads it, so the 404s below cannot come from a route
    // that refuses everyone.
    const own = await sheetFetch(tokenA, documentId)
    expect(own.status, "the owner's own sheet read is still 200").toBe(200)

    const crossTenant = await sheetFetch(tokenB, documentId)
    const unknown = await sheetFetch(tokenA, crypto.randomUUID())
    assertErrorEnvelope(crossTenant, 404, "B reads A's document sheet")
    assertErrorEnvelope(unknown, 404, 'an unknown document id')
    // Byte-identical to the unknown-id envelope -- a cross-tenant id must not be
    // distinguishable from an unknown one (no existence oracle by response body).
    expect(crossTenant.body).toEqual(unknown.body)
  })
})
