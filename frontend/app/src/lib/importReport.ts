// Import report step — pure view-model over the POST /v1/imports 201 body (M4-08-05,
// task-174). Pure and node-testable by construction: no React, no DOM, no fetch.
//
// INVCR-01-09 (task-285) DELETED `structuralErrorRows`/`violationRows` and their row
// types along with CreateReport.tsx, their only consumer. Neither is forked, both are
// SUPERSEDED by the two live channels the review shell reads instead:
//   - structural  -> reviewBatch.ts's `unreadableRows`, over the same `errors[]` through
//                    the same `rowErrorRows`, with a strictly better contract (a
//                    `row: null` entry instead of a silently dropped error, an em-dash
//                    column instead of an invented one). The channel invariant here was
//                    later corrected: a rule_key-bearing RowError is its own
//                    already-imported channel, not structural — pinned as the inverted
//                    UNREAD-3. See importReport.test.ts's header for the per-claim audit
//                    of what migrated and what was retired.
//   - content     -> per-row verdicts off the LIVE `InvoiceRecord.violations` (D4: the
//                    review screen re-fetches rather than reading a frozen import-time
//                    payload), never this report.
// TRAP A below is retained because it still explains why `invoices_clean` and
// `invoice_violations` disagree in the report reportSummary echoes.
//
// Two counting traps (plan §B; full evidence in task-174's Implementation Notes):
//
// TRAP A — blocking vs any-severity. `invoices_clean`/`invoices_with_violations` count
// by the server's blocking predicate (internal/invoice/store.go:669-676, exactly
// `severity === 'error'`); `invoice_violations` lists ANY severity that produced at
// least one violation. A warning-only invoice is counted clean AND listed — documented,
// not a bug (importer/service.go:36-42). Nothing here may derive a blocked/clean label
// from `severity`: re-deriving `severity === 'error'` in the browser is a second copy of
// the server's predicate, free to drift — exactly the browser verdict Core AC3 forbids.
//
// TRAP B — status:"failed" is a REACHABLE user path, not a defensive branch: a
// header-only spreadsheet -> rowsTotal===0 (importer/service.go:817-826) -> the
// handler still returns 201 Created (handlers.go:231-234) -> createImport resolves ->
// routeAfterImport (reviewBatch.ts) classifies it. reportSummary keys `kind:'failed'` on
// `status !== 'completed'`, NOT `=== 'failed'`: normalizeReport deliberately does not
// validate `status` (importApi.ts:296-301), so an unrecognised/undefined status must
// also fail safe rather than render as a flawless import of nothing. KEEP — do not
// "simplify" to `=== 'failed'` (task-174 coordinator ruling #4).
//
// TRAP C — JSON-null coercion. ALREADY CLOSED upstream by normalizeReport (IMPAPI-12,
// mutation-verified); no re-coercion and no spec for it here — see plan §B.
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
