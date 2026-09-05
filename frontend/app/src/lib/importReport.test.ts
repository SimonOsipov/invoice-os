// Specs for the import report step's pure view-model (M4-08-05, task-174) —
// reportSummary over the POST /v1/imports 201 body, plus the
// [detail-target-exclusive] DetailSelection constructors + detailTarget resolver.
//
// vitest environment is 'node' (vitest.config.ts) — no jsdom, no Testing Library.
// Nothing here touches a DOM or a component.
//
// INVCR-01-09 (task-285) deleted `structuralErrorRows`/`violationRows` with
// CreateReport.tsx, their only consumer, so RPT-02/03/04/05/08/12 and the four
// QA-adversarial blocks exercising those two exports went with them. Each deleted
// claim was checked against its named destination rather than assumed covered, and
// the two outcomes are deliberately NOT blurred together:
//
// MIGRATED — the invariant survives, asserted elsewhere today (though not verbatim):
//   - "a rule_key-bearing RowError stays STRUCTURAL" (RPT-03) -> UNREAD-3
//     (reviewBatch.test.ts), over `unreadableRows`. RPT-03's own claim was later
//     corrected: a store-duplicate error is its own already-imported channel, not
//     structural, and UNREAD-3 was inverted in place to match — it now asserts the
//     entry LEAVES `unreadableRows`. RPT-03's other half ("violationRows is [] on the
//     same report") has no successor and needs none: there is no content channel left
//     for it to cross into.
//   - rowLabel's numbers == rowErrorRows(e), and {row:0} not dropped as falsy
//     (RPT-02/12) -> importApi.test.ts's own `rowErrorRows` specs (the union reader
//     at :450, the row-0 boundary at :750). That is the real source both the deleted
//     helper and `unreadableRows` delegate to, so it was always the stronger home.
//   - "severity === 'error' is the ONLY blocking predicate; a warning-only invoice is
//     not failing" (RPT-05) -> PILL-4, and "an unrecognised severity is not bucketed
//     into a two-value enum" (the QA severity block) -> PILL-7/PILL-8, which pin the
//     derived tone per severity actually present.
//
// RETIRED — the SURFACE is gone, so there is nothing left to assert. Listed so the
// deletion is legible as a decision and not read later as an oversight:
//   - RPT-04 + the 150-row volume block: flattening invoice_violations[] to one row
//     per violation with parent fields repeated. Nothing flattens anything now —
//     verdictPill takes ONE invoice's own violations.
//   - RPT-08: invoice_id absent -> null, never a fallback to invoice_number. The
//     hazard was an OPTIONAL id on the import payload; the review screen addresses
//     rows by `InvoiceRecord.id`, which is not optional, so it is unrepresentable.
//   - RPT-02's rowLabel STRING ("rows 5, 9", never the range "rows 5–9"): there is no
//     label string any more. `UnreadableRow.row` is a number|null rendered directly.
//
// Spec map:
//   RPT-01  reportSummary echoes the six counters + rule_set_version field-for-field  (AC1)
//           against a DELIBERATELY arithmetically-inconsistent fixture, so no derived
//           counter (rows_valid = rows_total-rows_invalid, ready = clean+with_violations)
//           can pass by coincidence.
//   RPT-06  rule_set_version: null -> null, 3 -> 3 (toBeNull, not toBeFalsy)            (AC1)
//   RPT-07  Trap B — status:'failed' (REACHABLE path, not defensive) -> kind:'failed'   (AC10)
//           with NO counter keys spread on; status: undefined also fails safe
//   RPT-10  detailTarget resolves all three constructors' outputs correctly            (AC7,9)
//   RPT-11  each constructor returns BOTH keys via toEqual, never a partial object      (AC8)
//   RPT-13  fail-safe direction: an illegal both-set DetailSelection resolves           (AC7)
//           'imported', never the mock fallback under a real imported id
//
// Trap C (JSON-null coercion) is already closed upstream by normalizeReport
// (IMPAPI-12) — deliberately no spec for it here, see importReport.ts's module doc.
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import type { ImportReport } from './importApi'
import {
  clearSelection,
  detailTarget,
  reportSummary,
  selectImported,
  selectMock,
  type DetailSelection,
  type ReportSummary,
} from './importReport'

// Self-consistent base fixture (numbers add up) — used by specs that are not
// exercising RPT-01's inconsistency trap. Individual tests override only the fields
// their spec is about.
const BASE_REPORT: ImportReport = {
  id: 'batch-1',
  status: 'completed',
  format: 'csv',
  delimiter: ',',
  encoding: 'utf-8',
  rows_total: 10,
  rows_valid: 9,
  rows_invalid: 1,
  ready_invoices: 9,
  quarantined_invoices: 1,
  errors: [],
  rule_set_version: 5,
  invoices_clean: 8,
  invoices_with_violations: 1,
  invoice_violations: [],
}

function completed(s: ReportSummary): Extract<ReportSummary, { kind: 'completed' }> {
  expect(s.kind).toBe('completed')
  return s as Extract<ReportSummary, { kind: 'completed' }>
}

describe('reportSummary', () => {
  it('RPT-01: echoes the six counters + rule_set_version field-for-field — a self-consistent fixture would let a derived counter pass by coincidence, this one cannot', () => {
    // Deliberately inconsistent: rows_valid !== rows_total-rows_invalid (9 !== 8) and
    // ready_invoices !== invoices_clean+invoices_with_violations (6 !== 5).
    const report: ImportReport = {
      ...BASE_REPORT,
      id: 'batch-inconsistent',
      rows_total: 10,
      rows_valid: 7,
      rows_invalid: 2,
      invoices_clean: 4,
      invoices_with_violations: 1,
      ready_invoices: 6,
      quarantined_invoices: 3,
      rule_set_version: 9,
    }

    const s = reportSummary(report)

    expect(s).toEqual({
      kind: 'completed',
      id: 'batch-inconsistent',
      rows_valid: 7,
      rows_total: 10,
      ready_invoices: 6,
      quarantined_invoices: 3,
      invoices_clean: 4,
      invoices_with_violations: 1,
      rule_set_version: 9,
    })
  })
})

describe('reportSummary: rule_set_version (RPT-06)', () => {
  it('RPT-06: null stays null (never coerced to a falsy 0), a real version passes through', () => {
    const nullVersion = completed(reportSummary({ ...BASE_REPORT, rule_set_version: null }))
    const numberVersion = completed(reportSummary({ ...BASE_REPORT, rule_set_version: 3 }))

    // toBeNull, NOT toBeFalsy — `?? 0`/`|| 0` would produce 0, which toBeFalsy would
    // wrongly accept.
    expect(nullVersion.rule_set_version).toBeNull()
    expect(numberVersion.rule_set_version).toBe(3)
  })
})

describe('reportSummary: Trap B, status:"failed" is a reachable path (RPT-07)', () => {
  it('RPT-07: a header-only-CSV response (status:"failed", every counter 0, errors:[]) resolves kind:"failed" with NO counter keys spread on', () => {
    const failedReport: ImportReport = {
      id: 'batch-fail-1',
      status: 'failed',
      format: 'csv',
      delimiter: ',',
      encoding: 'utf-8',
      rows_total: 0,
      rows_valid: 0,
      rows_invalid: 0,
      ready_invoices: 0,
      quarantined_invoices: 0,
      errors: [],
      rule_set_version: null,
      invoices_clean: 0,
      invoices_with_violations: 0,
      invoice_violations: [],
    }

    const s = reportSummary(failedReport)

    // The falsification this must go RED against: an impl that sets kind:'failed' but
    // still spreads the zeroed counters, letting CreateReport show "0 rows valid, no
    // errors" for a file the server refused — toEqual catches any extra key at all.
    expect(s).toEqual({ kind: 'failed', id: 'batch-fail-1' })
    expect(s).not.toHaveProperty('rows_valid')
  })

  it('RPT-07: an unrecognised/undefined status also fails safe — status !== "completed", NOT === "failed" (an unvalidated field must never render as a successful import)', () => {
    const undefinedStatusReport = {
      ...BASE_REPORT,
      id: 'batch-fail-2',
      status: undefined,
    } as unknown as ImportReport

    const s = reportSummary(undefinedStatusReport)

    expect(s).toEqual({ kind: 'failed', id: 'batch-fail-2' })
  })
})

describe('detailTarget / DetailSelection constructors ([detail-target-exclusive], RPT-10, RPT-11, RPT-13)', () => {
  it('RPT-10: detailTarget resolves each constructor output correctly, including selectMock whose importedInvoiceId is null (not absent)', () => {
    expect(detailTarget(selectMock('INV-1'))).toEqual({ kind: 'mock', selectedId: 'INV-1' })
    expect(detailTarget(selectImported('uuid-123'))).toEqual({ kind: 'imported', invoiceId: 'uuid-123' })
    expect(detailTarget(clearSelection())).toEqual({ kind: 'mock', selectedId: null })
  })

  it('RPT-11: each constructor returns BOTH keys (toEqual, not toMatchObject — a partial object is exactly the bug this guards)', () => {
    const mock: DetailSelection = selectMock('INV-X')
    const imported: DetailSelection = selectImported('uuid-x')
    const cleared: DetailSelection = clearSelection()

    expect(mock).toEqual({ selectedId: 'INV-X', importedInvoiceId: null })
    expect(imported).toEqual({ selectedId: null, importedInvoiceId: 'uuid-x' })
    expect(cleared).toEqual({ selectedId: null, importedInvoiceId: null })
  })

  it('RPT-13: fail-safe direction — an illegal both-set state (unconstructible via the constructors, reachable only by an inline literal bypassing them) resolves "imported", never the mock fallback under a real imported id', () => {
    const illegal: DetailSelection = { selectedId: 'INV-1', importedInvoiceId: 'uuid-real' }

    expect(detailTarget(illegal)).toEqual({ kind: 'imported', invoiceId: 'uuid-real' })
  })
})

// --- QA (task-174, Stage 4 Mode B) — adversarial / edge coverage, node-testable only.
// The four blocks that exercised structuralErrorRows/violationRows (empty-array inputs,
// the neither-row-nor-rows StructuralRow, the row-0 guard, verbatim severity, the
// 150-row flatten) were deleted with those two exports in INVCR-01-09 — see the file
// header for where each invariant now lives. What remains is the one block whose
// subject, reportSummary, survives.

describe('reportSummary: an unrecognised non-"failed" status also fails safe (QA adversarial)', () => {
  it('a future/unknown status string (e.g. "processing") is NOT the same as testing only "failed" and undefined — status !== "completed" must reject it too', () => {
    // Falsifies an impl that special-cases exactly the two states RPT-07 exercises
    // (`status === 'failed' || status === undefined`) instead of the pinned
    // `status !== 'completed'`. A third, wholly unrecognised status is the case that
    // distinguishes the two implementations.
    const processingReport: ImportReport = { ...BASE_REPORT, id: 'batch-processing', status: 'processing' as ImportReport['status'] }

    const s = reportSummary(processingReport)

    expect(s).toEqual({ kind: 'failed', id: 'batch-processing' })
  })
})

// --- D-1 (task-919, ROUTE-02-04, Mode A) — static scan: the five deleted identifiers
// stay dead. Same shape as rowBlockedReasonRemoved.test.ts: SELF is excluded from the
// walk so this file's own needles (including the RPT-10/11/13 code above, pre-deletion)
// never self-trip.
const SRC = fileURLToPath(new URL('..', import.meta.url))
const SELF = fileURLToPath(import.meta.url)

function sourceFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.name === 'node_modules') continue
    const p = join(dir, entry.name)
    if (entry.isDirectory()) out.push(...sourceFiles(p))
    else if (/\.tsx?$/.test(entry.name) && p !== SELF) out.push(p)
  }
  return out
}

const FILES = sourceFiles(SRC)
const CORPUS = FILES.map((path) => [path, readFileSync(path, 'utf8')] as const)

function filesContaining(needle: string): string[] {
  return CORPUS.filter(([, text]) => text.includes(needle)).map(([path]) => path)
}

describe('ROUTE-02-04 (task-919): DetailSelection / selectInvoice deletion sweep (D-1)', () => {
  it('control: the scan reaches the SPA source tree', () => {
    expect(
      FILES.length,
      'the scan drifted off frontend/app/src -- every absence check below would be vacuous',
    ).toBeGreaterThanOrEqual(200)
  })

  it('control: importedInvoiceId is still found (the scan can see a match)', () => {
    expect(filesContaining('importedInvoiceId').length).toBeGreaterThan(0)
  })

  it('selectInvoice appears in no file', () => {
    expect(filesContaining('selectInvoice')).toEqual([])
  })

  it('selectMock appears in no file', () => {
    expect(filesContaining('selectMock')).toEqual([])
  })

  it('detailTarget appears in no file', () => {
    expect(filesContaining('detailTarget')).toEqual([])
  })

  it('DetailTarget appears in no file', () => {
    expect(filesContaining('DetailTarget')).toEqual([])
  })

  it('DetailSelection appears in no file', () => {
    expect(filesContaining('DetailSelection')).toEqual([])
  })
})
