// SPA import API module (M4-08-02, task-171) — previewImport + createImport over a
// single private XHR transport, the two-phase upload progress contract, and the
// report normalization D1 requires. Pinned by importApi.test.ts (IMPAPI-01..20).
//
// Types mirror the wire shapes emitted by internal/importer/handlers.go's
// importResponse (:63-80) / previewResponse (:96-109), internal/importer/store.go's
// RowError (:33-40), internal/importer/service.go's InvoiceViolations (:66-71), and
// internal/invoice/validator.go's Violation (:77-82) — field names copied verbatim,
// no camelCasing.
//
// Two wire channels stay distinct (story Core AC3; D1 in task-171's Implementation Plan):
// `errors` (RowError[]) is "the row never reached rule evaluation" — covers both
// unreadable rows and store-level duplicates (rule_key/invoice_id may be set; the
// browser routes those to their own already-imported tab, see reviewBatch.ts's
// isAlreadyImported). `invoice_violations` (InvoiceViolations[]) is CONTENT — "read
// fine, but a rule failed". The two wire channels NEVER MIX.
//
// Why normalizeReport exists (D1 — do not delete as defensive noise). The PREVIEW
// endpoint explicitly guards the nil-slice trap (handlers.go:320-334, PRV-08) so
// columns/sample_rows are always arrays; the IMPORT endpoint does NOT — service.go:667
// `var errorsList []RowError` and service.go:907 `var invoiceViolations
// []InvoiceViolations` stay nil when there is nothing to report, and handlers.go:74,79
// tag both fields with no `omitempty`, so a fully clean import (the commonest outcome,
// and the story's AC7 500-invoice demo) returns `"errors": null, "invoice_violations":
// null`. normalizeReport is the client-side counterpart of the guard the import
// endpoint is missing — it coerces both nulls to `[]` so `report.errors.map(...)` never
// throws. `rule_set_version` is the same class of hazard (Go `*int`, no omitempty ->
// `number | null`) but is NOT coerced: a null there means "nothing was evaluated" and
// must stay null, never a false `0`.
//
// Ragged sample_rows hazard ([preview-samples], PRV-09): rows are verbatim and
// unpadded, and may be SHORTER than `columns` — read cells by bounds-checked index; an
// unguarded `row[i]` past a short row's end yields `undefined`, not `''`.
//
// previewImport/createImport cannot use apiFetch (D2): apiFetch JSON-serializes any
// body and always sets Content-Type: application/json, with no FormData branch — a
// FormData body would be sent as "{}" and the server 400s "invalid multipart form".
// Both multipart calls go through ONE private XHR transport instead (not exported here
// — xhrJson), so there is exactly one non-apiFetch code path rather than two that could
// silently disagree on auth/error shaping (IMPAPI-20 is the anti-fork guard, mirroring
// PRV-16's role on the backend).
//
// makeImportAuth(session, onSignOut, onSuspended) mirrors makeAuthedFetch with the same
// three parameters (D3, AUDIT-10-07): PlatformCtx (src/types.ts:239) exposes only
// `authedFetch`, so the raw token and onSignOut are otherwise unreachable here. Must
// read `() => session.token` at CALL time, not construction time — a live re-sign-in
// swaps the Session object under React state, so a captured token snapshot would go
// stale (authedFetch.ts:38-45's rationale).
//
// Progress contract ([progress-two-phase], AC4/AC6): xhrJson reports RAW loaded/total
// BYTES, never a fraction — the arithmetic lives in the pure `uploadPercent` so it has a
// node oracle and the UI holds none. `lengthComputable === false` forces `total: 0`,
// which `uploadPercent` maps to null (indeterminate); there is deliberately NO fallback
// to `file.size`, which would invent a denominator the transport never confirmed. The
// flip to indeterminate is `upload.onload` (last byte handed to the socket): everything
// after it — server parse, decode, DB writes, rule evaluation, response travel — is
// unobservable, because the endpoint is synchronous with no job to poll, so any stage
// label there would be invented. ZERO `sending` events is legal (a ~100 KB CSV on a fast
// link may fire none, IMPAPI-08), so nothing may require a determinate phase to have
// occurred. `idle` is never emitted — it is the caller's initial state only. The
// terminal phase (done/error) is emitted BEFORE the promise settles, so a caller
// rendering from onPhase and one awaiting observe the same order. previewImport emits no
// phases at all — it is a header read, not the upload AC6 is about.
//
// `ApiError` is imported as a VALUE (xhrJson constructs it); `Session` stays a type-only
// import under this app's `verbatimModuleSyntax`.
import { ApiError } from '@invoice-os/api-client'
import type { Session } from '../auth'
import { isSuspended, isUnauthorized } from './authedFetch'
import type { AuthedFetch } from './portfolio'

export interface ImportPreview {
  document_id: string // the stored source document; createImport sends this instead of the bytes
  format: string
  delimiter: string | null // JSON null for xlsx
  encoding: string | null // JSON null for xlsx
  columns: string[] // non-null, guaranteed by PRV-08
  sample_rows: string[][] // non-null (PRV-08); ROWS ARE RAGGED
  rows_total: number // data rows, excludes header
}

export interface RowError {
  // STRUCTURAL channel — "couldn't read this row"
  row?: number // iff an ungroupable single row
  rows?: number[] // iff a whole quarantined invoice group
  field?: string
  rule_key?: string // set on a store-level duplicate; UI splits those into their own channel, see reviewBatch.ts's isAlreadyImported
  severity?: string
  invoice_id?: string // resolved id of the colliding invoice; absent when unresolved (racing-INSERT backstop)
  message: string // always
}

// `expected`/`actual` (INVCR-01-08 AC-3) mirror validationApi.ts's Violation field for
// field -- same optionality, same decimal-string type, same reason (see that type's own
// comment): absent means absent, never `''`.
export interface RuleViolation {
  rule_key: string
  severity: string
  message: string
  path?: string
  expected?: string
  actual?: string
}

export interface InvoiceViolations {
  // CONTENT channel — "read fine, rule failed"
  invoice_number: string
  invoice_id?: string // real invoice UUID on the real path; absent on dry-run
  rows: number[]
  violations: RuleViolation[]
}

export interface ImportReport {
  id: string
  status: 'completed' | 'failed'
  format: string
  delimiter: string | null
  encoding: string | null
  rows_total: number
  rows_valid: number
  rows_invalid: number
  ready_invoices: number
  quarantined_invoices: number
  errors: RowError[] // NORMALIZED: wire null -> []
  rule_set_version: number | null // null when nothing evaluated — never read as 0
  invoices_clean: number
  invoices_with_violations: number
  invoice_violations: InvoiceViolations[] // NORMALIZED: wire null -> []
}

// GET /v1/imports/{id} response (the import_batches row; 08/task-284 AC-2, per 07's own
// R1/R2). Exactly 10 fields -- `filename` (BULK-01-01: the uploaded part's name, or null
// when not recorded), no `ready_invoices`/`invoices_clean`/`invoices_with_violations` (07
// R2 -- those are live off the list endpoint's own totals, never stored on the batch).
//
// `errors` is always an ARRAY on GET, never JSON null (07 spec 4). That is NOT the same
// claim as "always EMPTY", and reading it that way would make the review screen's
// unreadable-rows tile a permanent zero. It carries the batch's real structural errors:
// Store.Finalize persists them into import_batches.errors on the success path
// (service.go:958), Store.GetBatch reads the column back (store.go:235-280), and the
// nil-guards in both GetBatch and GetHandler exist solely so a jsonb null cannot marshal
// back out as JSON `null`. That never-null guarantee is exactly why getImportBatch does
// NOT run the body through normalizeReport -- the POST's wire null->[] coercion is a
// documented quirk of THAT endpoint alone, and re-coercing here would imply the GET might
// lie too.
export interface ImportBatch {
  id: string
  entity_id: string
  filename: string | null
  status: 'pending' | 'processing' | 'completed' | 'failed'
  rows_total: number
  rows_valid: number
  rows_invalid: number
  errors: RowError[]
  rule_set_version: number | null
  created_at: string
}

// GET /api/submission/v1/extractions. Mirrors extraction.JobState / extraction.JobsResponse
// (internal/extraction/reader.go:23-35) and e2e/api/client.ts's own copy field for field --
// `state` stays a bare string on both sides because the column is one (EXTR-09-07).
export interface ExtractionJob {
  id: string
  document_id: string
  state: string
  created_at: string
  last_error: string | null
}

export interface ExtractionJobsResponse {
  jobs: ExtractionJob[]
}

// The 201 body of POST /api/submission/v1/documents, field for field
// (e2e/api/contract-document-upload.spec.ts pins the whole key set). `reused` is the
// per-tenant content-hash dedupe verdict, not an error.
export interface SourceDocumentUpload {
  document_id: string
  filename: string
  content_type: string
  size_bytes: number
  reused: boolean
}

export type XhrCtor = new () => XMLHttpRequest

export interface ImportAuth {
  getToken: () => string | null
  onUnauthorized: () => void
  // Optional for the same reason createAuthedFetch's third parameter is (authedFetch.ts).
  onSuspended?: () => void
}

export interface CreateImportRequest {
  documentId: string
  entityId: string
  mapping: Record<string, string> // already null-stripped by toImportMapping (M4-08-03)
}

export type UploadPhase =
  | { kind: 'idle' }
  | { kind: 'sending'; loaded: number; total: number } // determinate
  | { kind: 'processing' } // indeterminate
  | { kind: 'done' }
  | { kind: 'error'; error: ApiError }

// Mirrors makeAuthedFetch parameter for parameter so M4-08-06 can instantiate both from the
// SAME arguments in the SAME useMemo (App.tsx) — identical inputs at one construction site
// make divergence structurally impossible (D3).
// `() => session.token` is read at CALL time, never captured (authedFetch.ts:38-45).
export function makeImportAuth(session: Session, onSignOut: () => void, onSuspended?: () => void): ImportAuth {
  return {
    getToken: () => session.token,
    onUnauthorized: onSignOut,
    onSuspended,
  }
}

// The ONE transport (D2). Private on purpose: previewImport and createImport are thin
// wrappers over it rather than two peers, which is what makes them incapable of drifting
// apart on the Authorization header or the ApiError shaping (IMPAPI-20 is the guard).
// Error kinds/messages/body mirror apiFetch (client.ts:60-73) field for field so callers
// cannot tell the two transports apart.
function xhrJson(
  auth: ImportAuth,
  method: string,
  url: string,
  form: FormData,
  onPhase: ((p: UploadPhase) => void) | null,
  xhrCtor: XhrCtor,
): Promise<unknown> {
  return new Promise<unknown>((resolve, reject) => {
    const xhr = new xhrCtor()
    let settled = false

    const fail = (error: ApiError): void => {
      if (settled) return
      settled = true
      onPhase?.({ kind: 'error', error })
      reject(error)
    }

    xhr.upload.onprogress = (e) => {
      // Raw bytes, not a fraction. lengthComputable:false => total 0 => indeterminate.
      onPhase?.({ kind: 'sending', loaded: e.loaded, total: e.lengthComputable ? e.total : 0 })
    }

    // Last byte handed to the socket — everything after this point is unobservable.
    xhr.upload.onload = () => {
      onPhase?.({ kind: 'processing' })
    }

    xhr.onload = () => {
      if (settled) return
      const status = xhr.status

      let body: unknown
      let parsed = false
      try {
        body = JSON.parse(xhr.responseText)
        parsed = true
      } catch {
        // best-effort — a non-JSON body is normal on 4xx/5xx (e.g. a 413 from the proxy).
      }

      if (status >= 200 && status < 300) {
        if (!parsed) {
          fail(new ApiError('malformed', 'malformed response body', status))
          return
        }
        settled = true
        onPhase?.({ kind: 'done' }) // terminal phase precedes the settle, always
        resolve(body)
        return
      }

      let msg = xhr.statusText
      if (parsed && body && typeof body === 'object' && 'error' in body) {
        msg = String((body as { error: unknown }).error)
      }
      const error = new ApiError('http', msg, status, parsed ? body : undefined)

      // Both seams run the apiFetch path's own predicates (authedFetch.ts) rather than a
      // status comparison of their own, so the two transports cannot disagree on which
      // refusal is which — IMPAPI-19..23.
      if (isUnauthorized(error)) auth.onUnauthorized()
      else if (isSuspended(error)) auth.onSuspended?.()

      fail(error)
    }

    xhr.onerror = () => fail(new ApiError('network', 'network error', null))
    // NOTE (task-177): `xhr.timeout` is deliberately unset (0 = infinite) — this handler
    // is unreachable in production and is pinned only by IMPAPI-17b's FakeXhr. Do NOT
    // "fix" it by picking a value: none is both safe and useful (the 60s budget in
    // e2e/api/import.spec.ts binds 500 invoices, not a maximal upload). See task-179.
    xhr.ontimeout = () => fail(new ApiError('network', 'request timed out', null))

    xhr.open(method, url)
    const token = auth.getToken()
    if (token) xhr.setRequestHeader('Authorization', 'Bearer ' + token)
    // Content-Type is deliberately NEVER set: the browser writes the multipart boundary
    // itself, and Go's ParseMultipartForm rejects a hand-set header that omits it.
    xhr.send(form)
  })
}

// seam first, base second — repo convention (portfolio.ts:72, validationApi.ts:46)
export async function previewImport(
  auth: ImportAuth,
  base: string,
  file: File,
  xhrCtor: XhrCtor = globalThis.XMLHttpRequest,
): Promise<ImportPreview> {
  const form = new FormData()
  form.append('file', file)
  // Preview emits no phases and needs no normalization — PRV-08 guarantees its
  // columns/sample_rows are arrays, and delimiter/encoding are genuinely nullable.
  const raw = (await xhrJson(auth, 'POST', base + '/api/invoice/v1/imports/preview', form, null, xhrCtor)) as ImportPreview | null
  // The one validated field: an absent id would reach createImport's FormData as the
  // string "undefined" and 400 at the END of a run instead of naming this file now.
  if (!raw || !raw.document_id) {
    throw new ApiError('malformed', 'preview response is missing document_id', null, raw)
  }
  return raw
}

export async function createImport(
  auth: ImportAuth,
  base: string,
  req: CreateImportRequest,
  onPhase: (p: UploadPhase) => void,
  xhrCtor: XhrCtor = globalThis.XMLHttpRequest,
): Promise<ImportReport> {
  const form = new FormData()
  form.append('entity_id', req.entityId)
  form.append('mapping', JSON.stringify(req.mapping))
  form.append('document_id', req.documentId)
  // No query string is ever appended — dry_run is never sent ([no-dry-run]).
  const raw = await xhrJson(auth, 'POST', base + '/api/invoice/v1/imports', form, onPhase, xhrCtor)
  return normalizeReport(raw)
}

// The document path's upload (EXTR-09-07). Multipart, so it shares previewImport's XHR
// transport rather than apiFetch (D2) — one part named `file`, exactly as
// internal/extraction/handlers_upload.go reads it. `onPhase` is optional: a document run
// dispatches no per-file phase events (its N pipelines overlap, and runReducer's 'phase'
// writes onto one cursor).
export async function uploadSourceDocument(
  auth: ImportAuth,
  base: string,
  file: File,
  onPhase: ((p: UploadPhase) => void) | null = null,
  xhrCtor: XhrCtor = globalThis.XMLHttpRequest,
): Promise<SourceDocumentUpload> {
  const form = new FormData()
  form.append('file', file)
  const raw = (await xhrJson(auth, 'POST', base + '/api/submission/v1/documents', form, onPhase, xhrCtor)) as SourceDocumentUpload | null
  // The one validated field, same reason previewImport validates its own: an absent id
  // would reach the poll as the string "undefined" and 400 there instead of here.
  if (!raw || !raw.document_id) {
    throw new ApiError('malformed', 'upload response is missing document_id', null, raw)
  }
  return raw
}

// GET /api/submission/v1/extractions?document_id=… — the poll's one read. Plain JSON, so
// it goes through authedFetch, and `jobs` is resolved verbatim: reader.go guarantees an
// array, never null.
export function getExtractions(authedFetch: AuthedFetch, base: string, documentId: string): Promise<ExtractionJobsResponse> {
  return authedFetch<ExtractionJobsResponse>(
    `${base}/api/submission/v1/extractions?document_id=${encodeURIComponent(documentId)}`,
  )
}

// POST /api/invoice/v1/imports/document — JSON, not multipart: the bytes are already
// stored. Returns the SAME ImportReport shape the spreadsheet import does, so it takes the
// same normalizeReport pass and every downstream run/route view reads one type.
export async function importDocument(
  authedFetch: AuthedFetch,
  base: string,
  req: { entityId: string; documentId: string },
): Promise<ImportReport> {
  const raw = await authedFetch<unknown>(`${base}/api/invoice/v1/imports/document`, {
    method: 'POST',
    body: { entity_id: req.entityId, document_id: req.documentId },
  })
  return normalizeReport(raw)
}

// A plain-JSON GET, unlike previewImport/createImport's multipart POSTs -- goes through
// the typed authedFetch wrapper, not xhrJson (08/task-284 AC-2). `authedFetch` seam
// first, `base` second -- repo convention. Non-2xx rejects with the underlying ApiError
// unchanged; `rule_set_version` is never coerced to 0 (a null there means "nothing was
// evaluated" and must stay null), which is also why the body is resolved verbatim with no
// normalizeReport pass -- see ImportBatch's own comment.
export async function getImportBatch(authedFetch: AuthedFetch, base: string, id: string): Promise<ImportBatch> {
  // `id` is encoded even though it is a uuid by the time it gets here (the route is
  // `#review/<uuid>` and parseReviewHash rejects anything else): it is still a path segment
  // built from a caller-supplied string, and the encode costs nothing. NOTE this differs
  // from getInvoice/getInvoiceHistory/editInvoice, which interpolate their `id` raw --
  // flagged rather than silently "fixed" across all four, which is not this subtask's call.
  return authedFetch<ImportBatch>(`${base}/api/invoice/v1/imports/${encodeURIComponent(id)}`)
}

export function uploadPercent(p: UploadPhase): number | null {
  if (p.kind === 'done') return 100
  // total === 0 is the lengthComputable:false case — guarding it is also what keeps this
  // free of NaN/Infinity.
  if (p.kind === 'sending') return p.total > 0 ? Math.round((p.loaded / p.total) * 100) : null
  return null
}

// Union reader for the two RowError shapes (plural `rows` for a quarantined invoice
// group, scalar `row` for an ungroupable single row). Exported so M4-08-04 and -05 read
// the union one way instead of hand-rolling it two ways.
export function rowErrorRows(e: RowError): number[] {
  if (e.rows) return e.rows
  if (e.row !== undefined) return [e.row]
  return []
}

// Coerces the two nil-slice fields the import endpoint leaves as JSON null (D1 — see the
// module doc comment for why this is load-bearing, not defensive noise). Never throws: a
// body that will not parse at all is xhrJson's `malformed` branch, not this function's
// problem, and spreading a non-object raw (string/number/null) yields {} rather than an
// error. rule_set_version is passed through as null on purpose — "nothing was evaluated"
// must never read as a rule set numbered 0.
//
// SCOPE: this normalizes the three fields D1 identified and nothing else. It is NOT a
// validator — a structurally wrong 200 still yields an undefined `id`/`status`/count,
// deliberately. Defaulting those to 0/'' would fabricate data and reintroduce the exact
// false-zero hazard D1 flags for rule_set_version; a body that breaches the server
// contract should surface as an obvious undefined, not as a plausible-looking zero.
export function normalizeReport(raw: unknown): ImportReport {
  const r = (raw ?? {}) as ImportReport
  return {
    ...r,
    errors: Array.isArray(r.errors) ? r.errors : [],
    invoice_violations: Array.isArray(r.invoice_violations) ? r.invoice_violations : [],
    rule_set_version: r.rule_set_version ?? null,
  }
}
