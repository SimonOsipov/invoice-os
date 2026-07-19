// Import report step — pure view-model for the report step (M4-08-05, task-174).
// Every derivation the UI reads lives here so CreateReport.tsx is presentation only
// (plan §C). Pinned by importReport.test.ts (RPT-01..08, 10..13). Plan §C/§D are
// authoritative.
//
// Pure and node-testable by construction: no React, no DOM, no fetch. CreateReport.tsx
// renders what these return and derives nothing of its own.
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
import { rowErrorRows, type ImportReport } from './importApi'

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
export function reportSummary(r: ImportReport): ReportSummary {
  // `!== 'completed'`, NOT `=== 'failed'` — KEEP, do not "simplify" (task-174 ruling #4).
  // normalizeReport explicitly does not validate `status` (importApi.ts:296-301), so an
  // undefined or unrecognised status is representable here. `=== 'failed'` would be a
  // whitelist of one against an unvalidated field, and everything outside that whitelist
  // would render as a successful import — the "flawless import of nothing" Trap B
  // describes, on a path the server answers with 201 Created. `!== 'completed'` fails safe.
  if (r.status !== 'completed') return { kind: 'failed', id: r.id }
  // Field-for-field echo. No counter is ever DERIVED from another (no
  // `rows_total - rows_invalid`, no `invoices_clean + invoices_with_violations`): the
  // server's numbers are the only verdict, and a second copy of its arithmetic here is
  // free to drift from it (RPT-01).
  return {
    kind: 'completed',
    id: r.id,
    rows_valid: r.rows_valid,
    rows_total: r.rows_total,
    ready_invoices: r.ready_invoices,
    quarantined_invoices: r.quarantined_invoices,
    invoices_clean: r.invoices_clean,
    invoices_with_violations: r.invoices_with_violations,
    // null means "nothing was evaluated" and must stay null — `?? 0` would render as a
    // rule set numbered 0, a false zero (RPT-06).
    rule_set_version: r.rule_set_version,
  }
}

// One row per errors[] entry. rowLabel delegates to rowErrorRows (importApi.ts:284)
// rather than re-reading the row/rows union locally — a local re-read could silently
// drift from the shipped union reader, e.g. dropping row 0 (RPT-12).
export function structuralErrorRows(r: ImportReport): StructuralRow[] {
  return r.errors.map((e) => {
    const nums = rowErrorRows(e)
    return {
      // Joined with ', ', never a `min–max` range: [5, 9] is two reported rows, and
      // "rows 5–9" would assert five the server never reported (RPT-02).
      rowLabel: nums.length === 0 ? '' : nums.length === 1 ? `row ${nums[0]}` : `rows ${nums.join(', ')}`,
      field: e.field ?? null,
      // Carried, and STILL structural — a rule_key-bearing RowError (the store-duplicate
      // case) never crosses into the violations channel (RPT-03).
      rule_key: e.rule_key ?? null,
      severity: e.severity ?? null,
      message: e.message,
    }
  })
}

// Flattens invoice_violations[] to one ViolationRow per violation, repeating the
// parent's invoice_number/rows/invoice_id on each (RPT-04). invoice_id undefined ->
// null, never a fallback to invoice_number (RPT-08).
export function violationRows(r: ImportReport): ViolationRow[] {
  return r.invoice_violations.flatMap((iv) =>
    iv.violations.map((v) => ({
      invoice_number: iv.invoice_number,
      // Absent -> null, NEVER a fallback to invoice_number: the id is what the
      // click-through carries, and a non-UUID cited as one is worse than not clickable
      // at all (RPT-08).
      invoice_id: iv.invoice_id ?? null,
      rows: iv.rows,
      rule_key: v.rule_key,
      // Verbatim. No derived `blocked` flag — that would be a second copy of the
      // server's blocking predicate (Trap A) living in the browser (RPT-05).
      severity: v.severity,
      message: v.message,
      path: v.path ?? null,
    })),
  )
}

// --- detail-target selection ([detail-target-exclusive], debate F6, plan §C4) ---

export interface DetailSelection {
  selectedId: string | null
  importedInvoiceId: string | null
}

// The three constructors are TOTAL — each returns BOTH members, so there is no way to
// set one target and leave the other stale. That is the whole point of the atom: a
// partial write is not something an author has to remember to avoid, it is a type error.
export function clearSelection(): DetailSelection {
  return { selectedId: null, importedInvoiceId: null }
}

export function selectMock(number: string): DetailSelection {
  return { selectedId: number, importedInvoiceId: null }
}

export function selectImported(id: string): DetailSelection {
  return { selectedId: null, importedInvoiceId: id }
}

export type DetailTarget = { kind: 'imported'; invoiceId: string } | { kind: 'mock'; selectedId: string | null }

// Fail-safe direction (RPT-13): an illegal both-set DetailSelection — unconstructible
// via the three constructors above, reachable only by an inline literal bypassing them
// in App.tsx — resolves 'imported', so the failure degrades to an honest placeholder
// rather than a mock invoice rendered under a real UUID.
export function detailTarget(sel: DetailSelection): DetailTarget {
  // importedInvoiceId is tested FIRST on purpose. If both are somehow set, resolving
  // 'imported' shows an honest placeholder; resolving 'mock' would render an unrelated
  // mock invoice under a real invoice's UUID (RPT-13).
  if (sel.importedInvoiceId !== null) return { kind: 'imported', invoiceId: sel.importedInvoiceId }
  return { kind: 'mock', selectedId: sel.selectedId }
}
