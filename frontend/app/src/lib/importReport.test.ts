// RED specs (M4-08-05, task-174, RPT-01..08, 10..13) — pin the import report step's
// pure view-model (reportSummary/structuralErrorRows/violationRows, and the
// [detail-target-exclusive] DetailSelection constructors + detailTarget resolver)
// against a throw-skeleton lib/importReport.ts before the executor implements the
// bodies and CreateReport.tsx. Plan §D is authoritative.
//
// vitest environment is 'node' (vitest.config.ts) — no jsdom, no Testing Library.
// Nothing here touches a DOM or a component; CreateReport.tsx's rendering (the two
// visually separate sections, row clickability, the failure panel) is Playwright-only
// and is declared OUT of this node suite — see the plan's "declared gaps" list and
// M4-08-07 (task-176), which owns AC#9 end-to-end, RPT-09, and both renderings.
//
// Spec map (AC coverage complete — plan §D):
//   RPT-01  reportSummary echoes the six counters + rule_set_version field-for-field  (AC1)
//           against a DELIBERATELY arithmetically-inconsistent fixture, so no derived
//           counter (rows_valid = rows_total-rows_invalid, ready = clean+with_violations)
//           can pass by coincidence.
//   RPT-02  structuralErrorRows rowLabel: row/rows(2)/rows(non-contiguous)/neither      (AC2)
//   RPT-03  a rule_key-bearing RowError (store-duplicate) stays structural-only —       (AC3)
//           appears in structuralErrorRows, violationRows(r) is []
//   RPT-04  violationRows flattens 2+1 violations -> 3 rows, parent fields repeated     (AC2)
//   RPT-05  warning-only invoice: listed despite invoices_with_violations:0, severity   (AC4)
//           verbatim, no derived 'blocked' key, invoices_clean unaffected
//   RPT-06  rule_set_version: null -> null, 3 -> 3 (toBeNull, not toBeFalsy)            (AC1)
//   RPT-07  Trap B — status:'failed' (REACHABLE path, not defensive) -> kind:'failed'   (AC10)
//           with NO counter keys spread on; status: undefined also fails safe
//   RPT-08  invoice_id absent -> null (toBeNull); present -> the exact UUID             (AC6)
//   RPT-10  detailTarget resolves all three constructors' outputs correctly            (AC7,9)
//   RPT-11  each constructor returns BOTH keys via toEqual, never a partial object      (AC8)
//   RPT-12  rowLabel's numbers equal rowErrorRows(e) for every union shape incl. row:0  (AC2)
//   RPT-13  fail-safe direction: an illegal both-set DetailSelection resolves           (AC7)
//           'imported', never the mock fallback under a real imported id
//
// Declared gaps — no node oracle, NOT specced here (M4-08-07 owns): AC#9 end-to-end
// (click through, then a normal InvoicesList click shows the real detail — the field
// hazard is unrepresentable once §C4's atom exists; only the handler wiring is
// Playwright-only); RPT-09 (two channels as two visually separate sections); row
// clickability; the failure panel's rendering. Trap C (JSON-null coercion) is already
// closed upstream by normalizeReport (IMPAPI-12) — deliberately no spec for it here,
// see importReport.ts's module doc comment.
//
// Every spec below currently fails because reportSummary/structuralErrorRows/
// violationRows/clearSelection/selectMock/selectImported/detailTarget's stub bodies
// throw `new Error('not implemented')` before ever returning anything — that IS the
// correct RED reason (assertion / not-implemented), not an import/compile error.
import { describe, expect, it } from 'vitest'

import type { ImportReport, RowError } from './importApi'
import { rowErrorRows } from './importApi'
import {
  clearSelection,
  detailTarget,
  reportSummary,
  selectImported,
  selectMock,
  structuralErrorRows,
  violationRows,
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

describe('structuralErrorRows: rowLabel (RPT-02, RPT-12)', () => {
  it('RPT-02: row -> "row N"; rows[2] -> "rows a, b"; non-contiguous rows -> joined, never a range; neither -> ""', () => {
    const report: ImportReport = {
      ...BASE_REPORT,
      errors: [
        { row: 12, message: 'bad numeric' },
        { rows: [5, 6], message: 'quarantined group' },
        { rows: [5, 9], message: 'quarantined group, non-contiguous' },
        { message: 'no row info at all' },
      ],
    }

    const labels = structuralErrorRows(report).map((r) => r.rowLabel)

    expect(labels).toEqual([
      'row 12',
      'rows 5, 6',
      'rows 5, 9', // NOT 'rows 5–9' — a range would assert 4 rows the server never reported
      '',
    ])
  })

  it('RPT-12: the numbers in rowLabel always equal rowErrorRows(e), including {row:0} -> "row 0" (row 0 must not be dropped as falsy)', () => {
    const cases: RowError[] = [{ row: 0, message: 'm0' }, { row: 12, message: 'm1' }, { rows: [5, 9], message: 'm2' }, { message: 'm3' }]
    const report: ImportReport = { ...BASE_REPORT, errors: cases }

    const rows = structuralErrorRows(report)

    cases.forEach((e, i) => {
      const nums = rowErrorRows(e)
      const expected = nums.length === 0 ? '' : nums.length === 1 ? `row ${nums[0]}` : `rows ${nums.join(', ')}`
      expect(rows[i].rowLabel).toBe(expected)
    })
    // Pin row 0 explicitly — the exact falsification (`if (e.row)` truthy-check drops it).
    expect(rows[0].rowLabel).toBe('row 0')
  })
})

describe('structuralErrorRows / violationRows: the two channels never mix (RPT-03)', () => {
  it('RPT-03: a RowError carrying rule_key/severity (store-duplicate) stays structural — both fields carried there, and violationRows is empty on the same report', () => {
    const report: ImportReport = {
      ...BASE_REPORT,
      errors: [{ row: 4, rule_key: 'duplicate_invoice_number', severity: 'error', message: 'duplicate invoice number' }],
      invoice_violations: [],
    }

    const structural = structuralErrorRows(report)
    const violations = violationRows(report)

    expect(structural).toEqual([
      {
        rowLabel: 'row 4',
        field: null,
        rule_key: 'duplicate_invoice_number',
        severity: 'error',
        message: 'duplicate invoice number',
      },
    ])
    expect(violations).toEqual([])
  })
})

describe('violationRows: flattening (RPT-04)', () => {
  it('RPT-04: two invoices with 2 and 1 violations -> 3 rows; each carries its parent invoice_number/rows/invoice_id', () => {
    const report: ImportReport = {
      ...BASE_REPORT,
      invoice_violations: [
        {
          invoice_number: 'INV-1',
          invoice_id: 'uuid-1',
          rows: [2, 3],
          violations: [
            { rule_key: 'no-negative-total', severity: 'error', message: 'total is negative' },
            { rule_key: 'soft-check-vat', severity: 'warning', message: 'vat looks off' },
          ],
        },
        {
          invoice_number: 'INV-2',
          invoice_id: 'uuid-2',
          rows: [9],
          violations: [{ rule_key: 'missing-po', severity: 'error', message: 'po reference missing' }],
        },
      ],
    }

    const rows = violationRows(report)

    expect(rows).toHaveLength(3)
    expect(rows[0]).toEqual({
      invoice_number: 'INV-1',
      invoice_id: 'uuid-1',
      rows: [2, 3],
      rule_key: 'no-negative-total',
      severity: 'error',
      message: 'total is negative',
      path: null,
    })
    expect(rows[1]).toEqual({
      invoice_number: 'INV-1',
      invoice_id: 'uuid-1',
      rows: [2, 3],
      rule_key: 'soft-check-vat',
      severity: 'warning',
      message: 'vat looks off',
      path: null,
    })
    expect(rows[2]).toEqual({
      invoice_number: 'INV-2',
      invoice_id: 'uuid-2',
      rows: [9],
      rule_key: 'missing-po',
      severity: 'error',
      message: 'po reference missing',
      path: null,
    })
  })
})

describe('violationRows / reportSummary: warning-only invoices (Trap A, RPT-05)', () => {
  it('RPT-05: listed despite invoices_with_violations:0, severity verbatim, no derived "blocked" key, invoices_clean unaffected', () => {
    const report: ImportReport = {
      ...BASE_REPORT,
      invoices_clean: 3,
      invoices_with_violations: 0,
      ready_invoices: 3,
      invoice_violations: [
        {
          invoice_number: 'INV-9',
          invoice_id: 'uuid-9',
          rows: [1],
          violations: [{ rule_key: 'soft-check', severity: 'warning', message: 'looks unusual but not blocked' }],
        },
      ],
    }

    const rows = violationRows(report)
    const s = completed(reportSummary(report))

    expect(rows).toHaveLength(1)
    expect(rows[0].severity).toBe('warning')
    expect(Object.keys(rows[0])).not.toContain('blocked')
    expect(s.invoices_clean).toBe(3)
    expect(s.invoices_with_violations).toBe(0)
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

describe('violationRows: invoice_id (RPT-08)', () => {
  it('RPT-08: absent invoice_id -> null (toBeNull, never a fallback to invoice_number); present -> the exact UUID', () => {
    const report: ImportReport = {
      ...BASE_REPORT,
      invoice_violations: [
        {
          invoice_number: 'INV-A',
          rows: [1],
          violations: [{ rule_key: 'r1', severity: 'error', message: 'm1' }],
        },
        {
          invoice_number: 'INV-B',
          invoice_id: 'a1b2c3d4-uuid',
          rows: [2],
          violations: [{ rule_key: 'r2', severity: 'error', message: 'm2' }],
        },
      ],
    }

    const rows = violationRows(report)

    expect(rows[0].invoice_id).toBeNull()
    expect(rows[1].invoice_id).toBe('a1b2c3d4-uuid')
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
// Everything below is genuinely new falsification surface, not a restatement of
// RPT-01..13: Playwright-only gaps (AC#9 end-to-end, RPT-09's two-section rendering, row
// clickability, the failure panel's rendering) belong to M4-08-07 and are NOT
// manufactured here. Judged-and-skipped as already covered, not duplicated: invoice_id
// absent -> null (RPT-08 already pins toBeNull, not toBeFalsy) and the detailTarget
// truth table (RPT-10 covers the three legal states, RPT-13 the illegal one).

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

describe('structuralErrorRows / violationRows: empty-array inputs (QA adversarial)', () => {
  it('both channels return [] on an empty errors[]/invoice_violations[] — no crash on the boundary RPT-01..13 never exercises directly', () => {
    const report: ImportReport = { ...BASE_REPORT, errors: [], invoice_violations: [] }

    expect(structuralErrorRows(report)).toEqual([])
    expect(violationRows(report)).toEqual([])
  })

  it('a RowError carrying neither row nor rows produces a full StructuralRow: rowLabel "", and field/rule_key/severity all null (not undefined)', () => {
    const report: ImportReport = { ...BASE_REPORT, errors: [{ message: 'unparseable issue date' }] }

    // toEqual, not toMatchObject: an impl that leaves field/rule_key/severity undefined
    // (`e.field` instead of `e.field ?? null`) must go RED here, not just on rowLabel.
    expect(structuralErrorRows(report)).toEqual([{ rowLabel: '', field: null, rule_key: null, severity: null, message: 'unparseable issue date' }])
  })

  it('a RowError carrying row: 0 produces a full StructuralRow with rowLabel "row 0" (row 0 not dropped as falsy) and its field carried', () => {
    const report: ImportReport = { ...BASE_REPORT, errors: [{ row: 0, field: 'invoice_number', message: 'blank invoice number' }] }

    expect(structuralErrorRows(report)).toEqual([{ rowLabel: 'row 0', field: 'invoice_number', rule_key: null, severity: null, message: 'blank invoice number' }])
  })
})

describe('violationRows: severity outside error/warning renders verbatim, never bucketed (QA adversarial, Trap A)', () => {
  it('an unrecognised severity string (neither "error" nor "warning") passes through unchanged', () => {
    // Falsifies an impl that collapses severity to a two-value enum, e.g.
    // `severity: v.severity === 'error' ? 'error' : 'warning'` — RPT-05's 'warning' input
    // would pass that broken impl by coincidence (warning -> warning); an unrecognised
    // third value is the case that actually distinguishes verbatim passthrough from
    // bucketing.
    const report: ImportReport = {
      ...BASE_REPORT,
      invoice_violations: [
        {
          invoice_number: 'INV-7',
          invoice_id: 'uuid-7',
          rows: [3],
          violations: [{ rule_key: 'future-rule', severity: 'info', message: 'a severity level the browser has never seen' }],
        },
      ],
    }

    const rows = violationRows(report)

    expect(rows[0].severity).toBe('info')
  })
})

describe('violationRows: large volume does not misalign the flatten (QA adversarial)', () => {
  it('50 invoices x 3 violations each -> 150 rows, each still paired with its own parent invoice_number/invoice_id/rule_key', () => {
    const invoice_violations = Array.from({ length: 50 }, (_, i) => ({
      invoice_number: `INV-${i}`,
      invoice_id: `uuid-${i}`,
      rows: [i],
      violations: Array.from({ length: 3 }, (_, j) => ({
        rule_key: `rule-${j}`,
        severity: j === 0 ? 'error' : 'warning',
        message: `violation ${j} on invoice ${i}`,
      })),
    }))
    const report: ImportReport = { ...BASE_REPORT, invoice_violations }

    const rows = violationRows(report)

    expect(rows).toHaveLength(150)
    expect(rows[0]).toMatchObject({ invoice_number: 'INV-0', invoice_id: 'uuid-0', rule_key: 'rule-0' })
    expect(rows[149]).toMatchObject({ invoice_number: 'INV-49', invoice_id: 'uuid-49', rule_key: 'rule-2' })
    // Every row belongs to the invoice its index-range implies (3 rows per invoice) — an
    // off-by-one in the flatMap would misalign a row with the wrong parent's fields.
    rows.forEach((row, idx) => {
      expect(row.invoice_number).toBe(`INV-${Math.floor(idx / 3)}`)
    })
  })
})
