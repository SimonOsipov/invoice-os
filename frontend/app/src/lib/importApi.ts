// SPA import API module (M4-08-02, task-171). STUB — the executor implements the
// bodies next; every export below throws so the RED specs in importApi.test.ts
// (IMPAPI-01..20) fail on a thrown/assertion mismatch, not an import or type error.
// Mirrors the portfolio.ts / validationApi.ts stub idiom (M3-08-01 / M3-09-01).
//
// Types mirror the wire shapes emitted by internal/importer/handlers.go's
// importResponse (:63-80) / previewResponse (:96-109), internal/importer/store.go's
// RowError (:33-40), internal/importer/service.go's InvoiceViolations (:66-71), and
// internal/invoice/validator.go's Violation (:77-82) — field names copied verbatim,
// no camelCasing.
//
// Two channels stay distinct (story Core AC3; D1 in task-171's Implementation Plan):
// `errors` (RowError[]) is STRUCTURAL — "couldn't read this row" — and MAY itself carry
// a rule_key/severity (a store-level duplicate is still structural: the row never
// reached rule evaluation, so it never appears in invoice_violations too).
// `invoice_violations` (InvoiceViolations[]) is CONTENT — "read fine, but a rule
// failed". The two channels NEVER MIX.
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
// makeImportAuth(session, onSignOut) mirrors makeAuthedFetch (authedFetch.ts:46) with
// the same two parameters (D3): PlatformCtx (src/types.ts:239) exposes only
// `authedFetch`, so the raw token and onSignOut are otherwise unreachable here. Must
// read `() => session.token` at CALL time, not construction time — a live re-sign-in
// swaps the Session object under React state, so a captured token snapshot would go
// stale (authedFetch.ts:38-45's rationale).
//
// `ApiError`/`Session` are referenced only as TYPES by this stub's signatures — no
// runtime import needed (mirrors the authedFetch.ts stub's rationale under this app's
// strict noUnusedLocals/noUnusedParameters tsconfig). All parameters below are
// underscore-prefixed for the same reason: a throw-only body never reads them.
import type { ApiError } from '@invoice-os/api-client'
import type { Session } from '../auth'

export interface ImportPreview {
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
  rule_key?: string // may be set (store-duplicate) and is STILL structural
  severity?: string
  message: string // always
}

export interface RuleViolation {
  rule_key: string
  severity: string
  message: string
  path?: string
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

export type XhrCtor = new () => XMLHttpRequest

export interface ImportAuth {
  getToken: () => string | null
  onUnauthorized: () => void
}

export interface CreateImportRequest {
  file: File
  entityId: string
  mapping: Record<string, string> // already null-stripped by toImportMapping (M4-08-03)
}

export type UploadPhase =
  | { kind: 'idle' }
  | { kind: 'sending'; loaded: number; total: number } // determinate
  | { kind: 'processing' } // indeterminate
  | { kind: 'done' }
  | { kind: 'error'; error: ApiError }

export function makeImportAuth(_session: Session, _onSignOut: () => void): ImportAuth {
  throw new Error('not implemented')
}

// seam first, base second — repo convention (portfolio.ts:72, validationApi.ts:46)
export async function previewImport(
  _auth: ImportAuth,
  _base: string,
  _file: File,
  _xhrCtor: XhrCtor = globalThis.XMLHttpRequest,
): Promise<ImportPreview> {
  throw new Error('not implemented')
}

export async function createImport(
  _auth: ImportAuth,
  _base: string,
  _req: CreateImportRequest,
  _onPhase: (p: UploadPhase) => void,
  _xhrCtor: XhrCtor = globalThis.XMLHttpRequest,
): Promise<ImportReport> {
  throw new Error('not implemented')
}

export function uploadPercent(_p: UploadPhase): number | null {
  throw new Error('not implemented')
}

export function rowErrorRows(_e: RowError): number[] {
  throw new Error('not implemented')
}

export function normalizeReport(_raw: unknown): ImportReport {
  throw new Error('not implemented')
}
