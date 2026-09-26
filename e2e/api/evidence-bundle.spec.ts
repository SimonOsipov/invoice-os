// AUDIT-05-10: the evidence bundle over the deployed gateway -- GET /v1/evidence-bundle
// and its /preview sibling (cmd/invoice/main.go), reached through the real gateway/JWT/RLS
// path no httptest request or pgx.Tx test can exercise together.
//
// Test-first: no (see the story). The route already exists by the time this file is
// written, so it cannot be authored red.
//
// fflate's unzipSync proves the ZIP opens honestly (real central-directory parsing, real
// entry names) rather than by grepping raw bytes for filenames -- this repo has no other
// ZIP-reading capability anywhere (verified exhaustively before adding the dependency).
//
// The entity name carries a `Zz AUDIT-05 ` prefix, matching audit.spec.ts's own trap note:
// GET /v1/entities sorts by name ASC and the SPA's resolveActiveClient falls back to
// clients[0], so an entity that sorts before "Honeywell Group" silently becomes the
// topology suite's default active client. There is no delete endpoint, so this row and its
// invoice outlive the run.
//
// Scrubbed request/response headers and verbatim body bytes (AC 8 and AC 9 of the parent
// story) are proven ONLY in internal/archive/exchange_db_test.go. The first describe's
// invoice is never submitted. The second describe submits one through the mock adapter and
// proves only that its body files exist and the request body carries the invoice number.
// Departure from docs/e2e-convention.md's "containment, never a literal count": the bundle
// is scoped to an entity this file creates in beforeAll and nothing else ever touches, so
// the counts are deterministic. The exact count is also STRICTLY STRONGER than containment
// here -- it proves no OTHER entity's invoice leaked into the bundle, which a toContain
// assertion cannot see.
import { test, expect } from '@playwright/test'
import { unzipSync } from 'fflate'
import {
  approveUntilClosed,
  apiBase,
  createEntity,
  createInvoice,
  firmApproverTokens,
  getInvoice,
  login,
  rawFetch,
  validateInvoice,
  PERSONAS,
} from './client'
import { freshTin } from './fixtures'
import { assertErrorEnvelope, ensureFirmPolicyActive, type RawResult } from './contract-helpers'

// bundleFetch(): a bare fetch, never apiFetch/rawFetch -- both always res.json() the body
// and hand back neither headers nor bytes (client.ts:36-53), the same reasoning
// contract-import.spec.ts's downloadFetch and contract-ubl.spec.ts's ublFetch document for
// their own routes. Own copy, per this package's no-cross-suite-imports convention.
function bundleFetch(token: string, path: string, entityId: string, period: { from: string; to: string }): Promise<Response> {
  const params = new URLSearchParams({ entity_id: entityId, from: period.from, to: period.to })
  return fetch(`${apiBase()}/api/invoice/${path}?${params.toString()}`, {
    headers: { Authorization: `Bearer ${token}` },
  })
}

function downloadFetch(token: string, entityId: string, period: { from: string; to: string }): Promise<Response> {
  return bundleFetch(token, 'v1/evidence-bundle', entityId, period)
}

function previewFetch(token: string, entityId: string, period: { from: string; to: string }): Promise<Response> {
  return bundleFetch(token, 'v1/evidence-bundle/preview', entityId, period)
}

async function asRaw(res: Response): Promise<RawResult> {
  let body: unknown
  try {
    body = await res.json()
  } catch {
    body = undefined
  }
  return { status: res.status, body }
}

// parseDispositionFilename(): the WHOLE header must be "attachment; filename=<token>" with
// <token> matching this character class -- not just a prefix check. A quoted value
// (mime.FormatMediaType wraps in '"' when it has to) or a trailing parameter both fail this
// regex outright, so a single match IS the unquoted assertion (D-44), not a separate check
// bolted on afterward.
const DISPOSITION_RE = /^attachment; filename=([A-Za-z0-9._-]+)$/

function parseDispositionFilename(header: string | null): string {
  expect(header, 'Content-Disposition header must be present').toBeTruthy()
  const match = header!.match(DISPOSITION_RE)
  expect(
    match,
    `Content-Disposition = ${JSON.stringify(header)}, want the unquoted "attachment; filename=<token>" (D-44)`,
  ).not.toBeNull()
  return match![1]
}

// The wire shape manifest.json and the preview body share (D-49, manifest.go) -- declared
// once here so both can be compared with a single deep-equal instead of field by field.
interface BundleEntity {
  id: string
  name: string
  tin: string | null
}
interface BundlePeriod {
  from: string
  to: string
  bounds: string
  basis: string
}
interface BundleCounts {
  invoices: number
  status_transitions: number
  submissions: number
  exchange_attempts: number
  body_files: number
}
interface BundleManifest {
  entity: BundleEntity
  period: BundlePeriod
  counts: BundleCounts
}
interface PreviewBody {
  entity: BundleEntity
  period: BundlePeriod
  filename: string
  counts: BundleCounts
  over_limit: boolean
}

// fetchBundle(): downloads once, asserts the envelope (status/Content-Type/Content-Disposition),
// opens the ZIP, and parses manifest.json out of it -- the one place every download-side
// assertion in this file reads from, so a test needing more than one of these facts does not
// re-fetch or re-unzip.
async function fetchBundle(
  token: string,
  entityId: string,
  period: { from: string; to: string },
): Promise<{ zip: Record<string, Uint8Array>; manifest: BundleManifest; filename: string }> {
  const res = await downloadFetch(token, entityId, period)
  expect(res.status, 'download status').toBe(200)
  expect(res.headers.get('Content-Type'), 'Content-Type').toBe('application/zip')
  const filename = parseDispositionFilename(res.headers.get('Content-Disposition'))

  const bytes = new Uint8Array(await res.arrayBuffer())
  let zip: Record<string, Uint8Array> = {}
  expect(() => {
    zip = unzipSync(bytes)
  }, 'the response body must open as a ZIP').not.toThrow()

  const manifest = JSON.parse(new TextDecoder().decode(zip['manifest.json'])) as BundleManifest
  return { zip, manifest, filename }
}

// A wide, fixed period rather than one centered on "now": invoices.go's WHERE clause
// scopes on entity_id AND created_at, and entity_id already isolates this query to only
// what this spec creates -- so widening the window buys immunity to clock skew between
// this runner and the deployed gateway at no cost in precision. parseRequest enforces no
// max-window or future-`to` bound, so a 15-year span is not rejected.
const period = { from: '2020-01-01T00:00:00Z', to: '2035-01-01T00:00:00Z' }

// Honours quoted fields and doubled quotes: the header columns carry JSON.
function parseCsv(text: string): Record<string, string>[] {
  const records: string[][] = []
  let record: string[] = []
  let field = ''
  let quoted = false
  for (let i = 0; i < text.length; i++) {
    const c = text[i]
    if (quoted) {
      if (c === '"' && text[i + 1] === '"') {
        field += '"'
        i++
      } else if (c === '"') {
        quoted = false
      } else {
        field += c
      }
    } else if (c === '"') {
      quoted = true
    } else if (c === ',') {
      record.push(field)
      field = ''
    } else if (c === '\n' || c === '\r') {
      if (c === '\r' && text[i + 1] === '\n') i++
      record.push(field)
      records.push(record)
      record = []
      field = ''
    } else {
      field += c
    }
  }
  if (field !== '' || record.length > 0) {
    record.push(field)
    records.push(record)
  }
  const [header, ...rows] = records
  return rows.map((r) => Object.fromEntries(header.map((h, i) => [h, r[i] ?? ''])))
}

// Own copy of invoice-surfaces.spec.ts's submittable fixture (no cross-suite imports).
// 99999999-0001 is the mock adapter's explicit accept trigger (docs/mock-app-adapter.md).
function acceptInvoiceFields(invoiceNumber: string) {
  return {
    invoice_number: invoiceNumber,
    issue_date: '2026-01-01T00:00:00Z',
    supplier_tin: freshTin(),
    supplier_name: 'Acme Nigeria Ltd',
    buyer_tin: '99999999-0001',
    buyer_name: 'Buyer Ltd',
    currency: 'NGN',
    subtotal: '1000',
    vat: '75',
    total: '1075',
    line_items: [{ description: 'Widget', quantity: '10', unit_price: '100', line_total: '1000' }],
  }
}

// internal/submission jobstore.go markJobAccepted writes this submission_jobs.state.
const JOB_STATE_ACCEPTED = 'accepted'
// internal/submission exchange_bridge.go OutcomeSent: ExchangeFor's outcome once the bytes reached the wire.
const OUTCOME_SENT = 'sent'
// internal/submission exchange_bridge.go OpSubmit: the accept trigger answers on Submit, with no poll.
const OP_SUBMIT = 'submit'
// internal/submission mock_adapter.go MockAdapter.Submit: http.StatusOK on the accept branch.
const MOCK_ACCEPT_HTTP_STATUS = '200'

test.describe('evidence bundle (API E2E, over the deployed gateway)', () => {
  let token: string
  let entityId: string
  let invoiceNumber: string

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
    const entity = await createEntity(token, { name: `Zz AUDIT-05 ${freshTin()}`, tin: freshTin() })
    entityId = entity.id
    invoiceNumber = `INV-AUDIT-05-${freshTin()}`
    await createInvoice(token, { entity_id: entityId, invoice_number: invoiceNumber })
  })

  test('download: zip, opens, carries the four CSVs + manifest; invoices.csv contains this invoice; the never-submitted invoice leaves submissions/exchange header-only', async () => {
    const { zip, manifest, filename } = await fetchBundle(token, entityId, period)

    const names = Object.keys(zip)
    for (const want of ['invoices.csv', 'status_history.csv', 'submissions.csv', 'exchange.csv', 'manifest.json']) {
      expect(names, `zip entries: ${names.join(', ')}`).toContain(want)
    }

    const decoder = new TextDecoder()
    const invoicesCsv = decoder.decode(zip['invoices.csv'])
    expect(invoicesCsv, 'invoices.csv must contain the invoice number this spec created (containment, not count)').toContain(
      invoiceNumber,
    )

    // This entity and invoice are exclusive to this spec run -- history.go/submissions.go/
    // exchange.go all scope on `invoice_id = ANY($1)` over exactly the ids this entity+period
    // resolved to, never tenant-wide -- so "no data row" is a deterministic property of a
    // private fixture, not a shared-state count. The "no count" rule (docs/e2e-convention.md
    // :83-85, audit.spec.ts:14-18) governs counts over state this spec does not own, which
    // does not apply here.
    const submissionsRows = decoder.decode(zip['submissions.csv']).trim().split('\n')
    const exchangeRows = decoder.decode(zip['exchange.csv']).trim().split('\n')
    expect(submissionsRows, 'submissions.csv must be header-only -- the invoice was never submitted').toHaveLength(1)
    expect(exchangeRows, 'exchange.csv must be header-only -- the invoice was never submitted').toHaveLength(1)

    // manifest.json's own declared counts are a second, independent signal for the same
    // private fixture -- both must agree with the CSVs above (D-49).
    expect(manifest.entity.id, 'manifest entity id').toBe(entityId)
    expect(manifest.counts.invoices, 'manifest invoices count').toBe(1)
    expect(manifest.counts.submissions, 'manifest submissions count').toBe(0)
    expect(manifest.counts.exchange_attempts, 'manifest exchange_attempts count').toBe(0)

    expect(filename, 'download filename').toMatch(/^ASComply_evidence_Zz-AUDIT-05-[A-Za-z0-9-]+_\d{8}_\d{8}\.zip$/)
  })

  test('preview: invoices count is at least 1, its filename equals the download\'s, and it agrees with manifest.json', async () => {
    const { manifest, filename: downloadFilename } = await fetchBundle(token, entityId, period)

    const previewRes = await previewFetch(token, entityId, period)
    expect(previewRes.status, 'preview status').toBe(200)
    const preview = (await previewRes.json()) as PreviewBody

    expect(preview.counts.invoices, 'preview invoices count').toBeGreaterThanOrEqual(1)
    // The preview's filename is the BARE name; the download's is wrapped in the
    // Content-Disposition header -- parseDispositionFilename already stripped that inside
    // fetchBundle, so this is a direct string comparison rather than a substring/prefix check.
    expect(preview.filename, "preview filename must equal the download's").toBe(downloadFilename)

    // Preview.Entity/Period/Counts are manifest.go's own types (D-49): the two descriptions
    // of one bundle cannot drift apart, so this is a direct cross-check rather than trusting
    // each response in isolation.
    expect(preview.entity, 'preview entity must match manifest.json entity').toEqual(manifest.entity)
    expect(preview.period, 'preview period must match manifest.json period').toEqual(manifest.period)
    expect(preview.counts, 'preview counts must match manifest.json counts').toEqual(manifest.counts)
  })

  test('an entity_id belonging to no visible entity is 404 from both preview and download', async () => {
    // A syntactically valid, RLS-invisible UUID -- never a non-UUID string, which would
    // raise Postgres 22P02 and mask the intended 404 (contract-portfolio.spec.ts's note).
    const unknownEntityId = crypto.randomUUID()

    const downloadRes = await downloadFetch(token, unknownEntityId, period)
    const previewRes = await previewFetch(token, unknownEntityId, period)

    assertErrorEnvelope(await asRaw(downloadRes), 404, 'download of an unknown entity')
    assertErrorEnvelope(await asRaw(previewRes), 404, 'preview of an unknown entity')
  })
})

test.describe('evidence bundle for a submitted invoice (API E2E)', () => {
  let token: string

  test.beforeAll(async () => {
    token = await login(PERSONAS.A)
    await ensureFirmPolicyActive(token)
  })

  test('an invoice accepted through the mock adapter carries its submission, exchange rows, body files and fiscal outcome into the bundle', async () => {
    test.setTimeout(240_000)

    const entity = await createEntity(token, { name: `Zz AUDIT-05 submitted ${freshTin()}`, tin: freshTin() })
    const invoiceNumber = `INV-AUDIT-05-S-${freshTin()}`
    const created = await createInvoice(token, { entity_id: entity.id, ...acceptInvoiceFields(invoiceNumber) })

    const validated = await validateInvoice(token, created.id)
    expect(validated.status, 'the clean fixture should promote draft -> validated').toBe('validated')

    await approveUntilClosed(created.id, await firmApproverTokens())

    const submitted = await rawFetch('/api/invoice/v1/invoices/submissions', {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
      body: { invoice_ids: [created.id], idempotency_key: crypto.randomUUID() },
    })
    expect(submitted.status, 'batch-submit status').toBe(200)
    const results = (submitted.body as { results: { enqueued: boolean }[] }).results
    expect(results[0].enqueued, 'the approved, validated invoice should be enqueued').toBe(true)

    await expect
      .poll(async () => (await getInvoice(token, created.id)).status, {
        message: 'the worker should drive the invoice to accepted',
        timeout: 120_000,
        intervals: [1_000],
      })
      .toBe('accepted')
    const invoice = await getInvoice(token, created.id)
    expect(invoice.irn, 'GET irn').toBeTruthy()
    expect(invoice.csid, 'GET csid').toBeTruthy()

    const { zip, manifest } = await fetchBundle(token, entity.id, period)
    const decoder = new TextDecoder()

    const invoiceRow = parseCsv(decoder.decode(zip['invoices.csv'])).find((r) => r.invoice_id === created.id)
    expect(invoiceRow, 'invoices.csv must carry the submitted invoice').toBeDefined()
    expect(invoiceRow!.status, 'invoices.csv status').toBe(invoice.status)
    expect(invoiceRow!.irn, "invoices.csv irn must equal the GET's").toBe(invoice.irn)
    expect(invoiceRow!.csid, "invoices.csv csid must equal the GET's").toBe(invoice.csid)

    const submissions = parseCsv(decoder.decode(zip['submissions.csv']))
    expect(submissions, 'submissions.csv must hold exactly one job row').toHaveLength(1)
    expect(submissions[0].invoice_id, 'submissions.csv invoice_id').toBe(created.id)
    expect(submissions[0].state, 'submissions.csv state').toBe(JOB_STATE_ACCEPTED)

    const exchange = parseCsv(decoder.decode(zip['exchange.csv']))
    const attempts = exchange.filter((r) => r.invoice_id === created.id)
    expect(attempts.length, 'exchange.csv must hold at least one attempt for this invoice').toBeGreaterThanOrEqual(1)
    // exchange.go orders rows by occurred_at, so the last row is the attempt that got the verdict.
    const last = attempts[attempts.length - 1]
    expect(last.operation, 'last attempt operation').toBe(OP_SUBMIT)
    expect(last.outcome, 'last attempt outcome').toBe(OUTCOME_SENT)
    expect(last.http_status, 'last attempt http_status').toBe(MOCK_ACCEPT_HTTP_STATUS)
    expect(Object.keys(zip), 'the request body file must be in the ZIP').toContain(last.request_body_file)
    expect(Object.keys(zip), 'the response body file must be in the ZIP').toContain(last.response_body_file)
    expect(decoder.decode(zip[last.request_body_file]), 'the request body must carry the invoice number').toContain(invoiceNumber)

    expect(manifest.counts.submissions, 'manifest submissions count').toBe(1)
    expect(manifest.counts.exchange_attempts, 'manifest exchange_attempts must equal the exchange.csv rows').toBe(exchange.length)
  })
})
