// Import report step — pure view-model for the report step (M4-08-05, task-174).
// Every derivation the UI reads lives here so CreateReport.tsx is presentation only
// (plan §C). Pinned by importReport.test.ts (RPT-01..08, 10..13). Plan §C/§D are
// authoritative.
//
// Stage 2.5 (Mode A): every FUNCTION below is a signature-only skeleton whose body
// throws 'not implemented' until the executor fills it in (same throw-skeleton
// pattern as importApi.ts@d2ed19e / importFlow.ts@86fa0be). Interfaces/types carry
// their real, plan-pinned shapes now — only the logic is stubbed.
//
// Three counting traps (plan §B; full evidence in task-174's Implementation Notes):
//
// TRAP A — blocking vs any-severity. `invoices_clean`/`invoices_with_violations` count
// by the server's blocking predicate (internal/invoice/store.go:669-676, exactly
// `severity === 'error'`); `invoice_violations` lists ANY severity that produced at
// least one violation. A warning-only invoice is counted clean AND listed — documented,
// not a bug (importer/service.go:36-42). violationRows must render `severity` VERBATIM
// and derive NO blocked/clean label: re-deriving `severity === 'error'` in the browser
// is a second copy of the server's predicate, free to drift — exactly the browser
// verdict Core AC3 forbids.
//
// TRAP B — status:"failed" is a REACHABLE user path, not a defensive branch: a
// header-only spreadsheet -> rowsTotal===0 (importer/service.go:817-826) -> the
// handler still returns 201 Created (handlers.go:231-234) -> createImport resolves ->
// the app advances to the report step. reportSummary keys `kind:'failed'` on
// `status !== 'completed'`, NOT `=== 'failed'`: normalizeReport deliberately does not
// validate `status` (importApi.ts:296-301), so an unrecognised/undefined status must
// also fail safe rather than render as a flawless import of nothing. KEEP — do not
// "simplify" to `=== 'failed'` (task-174 coordinator ruling #4).
//
// TRAP C — JSON-null coercion. ALREADY CLOSED upstream by normalizeReport (IMPAPI-12,
// mutation-verified); no re-coercion and no spec for it here — see plan §B.
//
// [detail-target-exclusive] / F6 supersession (plan §C4): selectedId and
// importedInvoiceId are replaced by one DetailSelection atom written ONLY through the
// three constructors below (clearSelection/selectMock/selectImported), so "forgot to
// clear the other field" becomes a type error rather than a runtime bug. detailTarget
// resolves the atom to a render target; RPT-13 pins its fail-safe direction — an
// illegal both-set state (unconstructible via the constructors, reachable only by an
// inline literal bypassing them) resolves 'imported', never a mock invoice rendered
// under a real UUID.
import type { ImportReport } from './importApi'

export type ReportSummary =
  | { kind: 'failed'; id: string }
  | {
      kind: 'completed'
      id: string
      rows_valid: number
      rows_total: number
      ready_invoices: number
      quarantined_invoices: number
      invoices_clean: number
      invoices_with_violations: number
      rule_set_version: number | null
    }

export interface StructuralRow {
  // from errors[] — "couldn't read this row"
  rowLabel: string // '' when neither row nor rows is present; delegates to rowErrorRows
  field: string | null
  rule_key: string | null // may be set (store-duplicate) and is STILL structural
  severity: string | null
  message: string
}

export interface ViolationRow {
  // from invoice_violations[] — "read fine, rule failed"; one row per violation
  invoice_number: string
  invoice_id: string | null // null => NOT clickable — never a fallback to invoice_number
  rows: number[]
  rule_key: string
  severity: string // rendered VERBATIM — no derived blocked/clean label (Trap A)
  message: string
  path: string | null
}

// kind:'failed' iff r.status !== 'completed' (Trap B; KEEP — see module doc comment).
// On 'completed', echoes the six counters plus rule_set_version field-for-field — no
// derived counter is ever computed here (RPT-01).
export function reportSummary(_r: ImportReport): ReportSummary {
  throw new Error('not implemented')
}

// One row per errors[] entry. rowLabel delegates to rowErrorRows (importApi.ts:284)
// rather than re-reading the row/rows union locally — a local re-read could silently
// drift from the shipped union reader, e.g. dropping row 0 (RPT-12).
export function structuralErrorRows(_r: ImportReport): StructuralRow[] {
  throw new Error('not implemented')
}

// Flattens invoice_violations[] to one ViolationRow per violation, repeating the
// parent's invoice_number/rows/invoice_id on each (RPT-04). invoice_id undefined ->
// null, never a fallback to invoice_number (RPT-08).
export function violationRows(_r: ImportReport): ViolationRow[] {
  throw new Error('not implemented')
}

// --- detail-target selection ([detail-target-exclusive], debate F6, plan §C4) ---

export interface DetailSelection {
  selectedId: string | null
  importedInvoiceId: string | null
}

export function clearSelection(): DetailSelection {
  throw new Error('not implemented')
}

export function selectMock(_number: string): DetailSelection {
  throw new Error('not implemented')
}

export function selectImported(_id: string): DetailSelection {
  throw new Error('not implemented')
}

export type DetailTarget = { kind: 'imported'; invoiceId: string } | { kind: 'mock'; selectedId: string | null }

// Fail-safe direction (RPT-13): an illegal both-set DetailSelection — unconstructible
// via the three constructors above, reachable only by an inline literal bypassing them
// in App.tsx — resolves 'imported', so the failure degrades to an honest placeholder
// rather than a mock invoice rendered under a real UUID.
export function detailTarget(_sel: DetailSelection): DetailTarget {
  throw new Error('not implemented')
}
