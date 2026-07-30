// RED specs (INVCR-01-08, task-284, Stage 2.5/Mode A) — pin lib/reviewBatch.ts's pure
// review view-model before the executor implements the bodies. Every export currently
// throws `new Error('not implemented')` (reviewBatch.ts's own header comment), so every
// spec below fails on that thrown error — the correct RED reason (assertion /
// not-implemented), not an import/compile error. PILL-3b is the one deliberate
// exception: it is a static source-scan GUARD, not a discriminator (see its own comment
// below), and is expected to already be green.
//
// Spec map (task-284 Implementation Plan §8, 26-row table; this file covers the 18 rows
// scoped to reviewBatch.ts — the 8 listInvoices/getImportBatch/violationSummary rows
// live in invoices.test.ts / importApi.test.ts instead):
//   PILL-1     a clean invoice is VALIDATED, not a green DRAFT               (AC-5)
//   PILL-2     an erroring draft: N RULES FAILED counts errors only          (AC-5)
//   PILL-3a/b  queued is QUEUED, never APPROVED (+ source-scan guard)        (AC-5)
//   PILL-4     a warning-only invoice is not shown as failing (§10.12 trap)  (AC-6)
//   PILL-5     a kept row is KEPT · INVALID and does not stack               (AC-5)
//   FILTER-1   each pill maps to server params, All emits zero keys          (AC-7)
//   QUERY-1    reviewQuery always carries the batch id                      (AC-4)
//   RULESET-1  label is NG-MBS v<n> from the server, never v0                (AC-8)
//   CHANNEL-1  the not-imported channel renders at zero, not omitted         (AC-6)
//   CHANNEL-2  left-channel numbers are the supplied totals, not the page    (AC-6)
//   PAGER-1..4 pager label arithmetic, incl. the limit<1 guard               (AC-9)
//   HASH-1/2   parseReviewHash/formatReviewHash round-trip + rejection       (AC-4)
//   UNREAD-1/2 row/rows union reader, incl. the rowless-error trap           (AC-4)
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import { invoiceStatusStyle, type InvoiceStatus, type RuleCount } from './invoices'
import { severityStyle, type Violation } from './validationApi'
import type { ImportBatch, ImportReport, RowError } from './importApi'
import {
  channelTiles,
  filterToQuery,
  formatReviewHash,
  initialReviewFilter,
  pagerLabels,
  pagerNav,
  parseReviewHash,
  railPills,
  reviewFilterReducer,
  reviewHash,
  reviewHeader,
  reviewPageQuery,
  reviewPills,
  REVIEW_PAGE_SIZE,
  reviewQuery,
  reviewShellState,
  reviewTabs,
  routeAfterImport,
  unreadableCsv,
  unreadableRows,
  verdictPill,
  type ReviewFilterState,
  type UnreadableRow,
  type VerdictInput,
} from './reviewBatch'

describe('verdictPill (AC-5)', () => {
  it('PILL-1: a clean validated invoice is VALIDATED, not a green DRAFT — colours come from invoiceStatusStyle verbatim, never re-authored', () => {
    const input: VerdictInput = { status: 'validated', violations: [] }

    const result = verdictPill(input)

    expect(result.status).toEqual(invoiceStatusStyle('validated'))
    expect(result.status.label).toBe('VALIDATED')
    expect(result.badges).toEqual([])
  })

  it('PILL-2: an erroring draft gets DRAFT plus N RULES FAILED, counting errors only — 3 errors + 2 warnings must badge 3, not 5 (was weak in the story: an errors-only fixture cannot tell a severity-blind impl from a correct one)', () => {
    const violations: Violation[] = [
      { rule_key: 'r1', severity: 'error', message: 'e1' },
      { rule_key: 'r2', severity: 'error', message: 'e2' },
      { rule_key: 'r3', severity: 'error', message: 'e3' },
      { rule_key: 'r4', severity: 'warning', message: 'w1' },
      { rule_key: 'r5', severity: 'warning', message: 'w2' },
    ]
    const input: VerdictInput = { status: 'draft', violations }

    const result = verdictPill(input)

    expect(result.status).toEqual(invoiceStatusStyle('draft'))
    const failedBadge = result.badges.find((b) => b.kind === 'rules-failed')
    expect(failedBadge).toBeDefined()
    expect(failedBadge?.count).toBe(3)
    expect(failedBadge?.tone).toEqual({
      bg: severityStyle('error').bg,
      border: severityStyle('error').border,
      text: severityStyle('error').text,
    })
  })
})

describe('verdictPill: queued is QUEUED, never APPROVED (PILL-3)', () => {
  it('PILL-3a: a queued invoice renders QUEUED', () => {
    const input: VerdictInput = { status: 'queued', violations: [] }

    const result = verdictPill(input)

    expect(result.status).toEqual(invoiceStatusStyle('queued'))
    expect(result.status.label).toBe('QUEUED')
  })

  // Guard, not a discriminator (task-284 Implementation Plan §8): a string-concat check
  // can't see 'APPROV' + 'ED' assembled at runtime, so this can only prove the literal
  // is absent from the SOURCE, not that the label is never rendered. Must read
  // reviewBatch.ts itself — reading this test file instead would self-match, since this
  // spec's own name and comments legitimately mention the word.
  it("PILL-3b (guard, expected green today): reviewBatch.ts's own source contains no 'APPROVED' literal", () => {
    const srcPath = fileURLToPath(new URL('./reviewBatch.ts', import.meta.url))
    const source = readFileSync(srcPath, 'utf8')

    expect(source).not.toContain('APPROVED')
  })
})

describe('verdictPill: warning-only stays passing, never failing (PILL-4, §10.12 trap)', () => {
  it('PILL-4: a validated invoice with only a warning violation renders VALIDATED plus an advisory badge, never rules-failed — the strongest spec in this subtask, since violations.length > 0 passes every other spec today and only breaks here', () => {
    const violations: Violation[] = [{ rule_key: 'r1', severity: 'warning', message: 'w1' }]
    const input: VerdictInput = { status: 'validated', violations }

    const result = verdictPill(input)

    expect(result.status).toEqual(invoiceStatusStyle('validated'))
    expect(result.status.label).toBe('VALIDATED')
    expect(result.badges.some((b) => b.kind === 'rules-failed')).toBe(false)
    const advisory = result.badges.find((b) => b.kind === 'advisory')
    expect(advisory).toBeDefined()
    expect(advisory?.count).toBe(1)
    expect(advisory?.tone).toEqual({
      bg: severityStyle('warning').bg,
      border: severityStyle('warning').border,
      text: severityStyle('warning').text,
    })
  })
})

describe('verdictPill: the advisory arm is status-agnostic and its tone is derived (QA Stage 4, task-284 — pins the two judgment calls Stage 3 made but left unpinned)', () => {
  it('PILL-6: a draft with only warning violations still gets an advisory badge, never suppressed just because the row has not been validated yet', () => {
    const violations: Violation[] = [{ rule_key: 'r1', severity: 'warning', message: 'w1' }]
    const input: VerdictInput = { status: 'draft', violations }

    const result = verdictPill(input)

    expect(result.status).toEqual(invoiceStatusStyle('draft'))
    expect(result.badges.some((b) => b.kind === 'rules-failed')).toBe(false)
    const advisory = result.badges.find((b) => b.kind === 'advisory')
    expect(advisory).toBeDefined()
    expect(advisory?.count).toBe(1)
  })

  it('PILL-7: an advisory set with only info-severity violations renders the muted info tone, not amber — proves the tone is derived from what is actually present, not hard-coded to warning', () => {
    const violations: Violation[] = [{ rule_key: 'r1', severity: 'info', message: 'i1' }]
    const input: VerdictInput = { status: 'validated', violations }

    const result = verdictPill(input)

    const advisory = result.badges.find((b) => b.kind === 'advisory')
    expect(advisory).toBeDefined()
    expect(advisory?.tone).toEqual({
      bg: severityStyle('info').bg,
      border: severityStyle('info').border,
      text: severityStyle('info').text,
    })
  })

  it('PILL-8: an advisory set mixing info and warning violations renders the warning tone, not info — warning dominates the derivation so an info-only-looking advisory never under-signals a real warning in the mix', () => {
    const violations: Violation[] = [
      { rule_key: 'r1', severity: 'info', message: 'i1' },
      { rule_key: 'r2', severity: 'warning', message: 'w1' },
    ]
    const input: VerdictInput = { status: 'validated', violations }

    const result = verdictPill(input)

    const advisory = result.badges.find((b) => b.kind === 'advisory')
    expect(advisory).toBeDefined()
    expect(advisory?.count).toBe(2)
    expect(advisory?.tone).toEqual({
      bg: severityStyle('warning').bg,
      border: severityStyle('warning').border,
      text: severityStyle('warning').text,
    })
  })
})

describe('verdictPill: a kept row is KEPT · INVALID and does not stack (PILL-5)', () => {
  it('PILL-5: kept_as_is_at set REPLACES rules-failed with exactly one kept-invalid badge, never both', () => {
    const violations: Violation[] = [
      { rule_key: 'r1', severity: 'error', message: 'e1' },
      { rule_key: 'r2', severity: 'error', message: 'e2' },
    ]
    const input: VerdictInput = { status: 'draft', violations, kept_as_is_at: '2026-07-30T00:00:00Z' }

    const result = verdictPill(input)

    expect(result.badges).toHaveLength(1)
    expect(result.badges[0].kind).toBe('kept-invalid')
  })
})

describe('filterToQuery (AC-7)', () => {
  it('FILTER-1: each pill maps to server params; All emits an object with ZERO keys, never an explicit needsFix:false/status:undefined', () => {
    const all = filterToQuery('all')

    expect(all).toEqual({})
    expect(Object.keys(all)).toHaveLength(0)
    expect(filterToQuery('needs-fix')).toEqual({ needsFix: true })
    expect(filterToQuery('ready')).toEqual({ status: 'validated' })
    expect(filterToQuery('queued')).toEqual({ status: 'queued' })
  })
})

describe('reviewQuery (AC-4, cashing the un-cashed #review/<uuid> safety argument)', () => {
  it('QUERY-1: reviewQuery always carries the batch id, merges filterToQuery with the extras, and never emits an empty q', () => {
    const id = 'batch-1'

    const result = reviewQuery(id, 'needs-fix', { q: '', ruleKey: 'k' })

    expect(result).toEqual({ importBatchId: id, needsFix: true, ruleKey: 'k' })
    expect('q' in result).toBe(false)
  })
})

describe('channelTiles: rule-set label (AC-8, RULESET-1)', () => {
  it('RULESET-1: label is NG-MBS v<n> from the server; null AND undefined both read "not evaluated", never v0', () => {
    const live = { cleanTotal: 0, failingTotal: 0 }

    expect(channelTiles({ errors: [], rule_set_version: 3 }, live).frozen.ruleSetLabel).toBe('NG-MBS v3')
    expect(channelTiles({ errors: [], rule_set_version: null }, live).frozen.ruleSetLabel).toBe('not evaluated')

    // `undefined` here is deliberate, not a typo: ImportBatch types rule_set_version as
    // always-present (`number | null`), but the recorded shipped trap (InvoiceRecord's
    // own rule_set_version) is that a field typed present can still read `undefined` at
    // runtime — this pins the same fail-safe against that class of bug.
    const undefinedVersion = { errors: [], rule_set_version: undefined } as unknown as Pick<
      ImportBatch,
      'errors' | 'rule_set_version'
    >
    expect(channelTiles(undefinedVersion, live).frozen.ruleSetLabel).toBe('not evaluated')
  })
})

describe('channelTiles: LIVE vs FROZEN channels (AC-6, CHANNEL-1/CHANNEL-2 — no spec existed for this export before Stage 1)', () => {
  it('CHANNEL-1: the not-imported (frozen) channel renders at zero as an explicit fact, not null/omitted', () => {
    const result = channelTiles({ errors: [], rule_set_version: 5 }, { cleanTotal: 0, failingTotal: 0 })

    expect(result.frozen.unreadable).toBe(0)
    expect(result.atZero).toBe(true)
  })

  it('CHANNEL-2: left-channel (live) numbers are the SUPPLIED totals, never derived by counting the page handed in', () => {
    const errors: RowError[] = [
      { row: 1, message: 'bad row' },
      { row: 2, message: 'bad row' },
      { row: 3, message: 'bad row' },
    ]

    const result = channelTiles({ errors, rule_set_version: 5 }, { cleanTotal: 474, failingTotal: 26 })

    expect(result.live).toEqual({ cleanTotal: 474, failingTotal: 26 })
  })
})

describe('pagerLabels (AC-9)', () => {
  it('PAGER-1 (medium — 500/50 is exact, cannot discriminate ceil vs floor on its own): first page of 500', () => {
    const result = pagerLabels({ limit: 50, offset: 0, total: 500 })

    expect(result.showing).toBe('SHOWING 1–50 OF 500')
    expect(result.page).toBe('PAGE 1 / 10')
  })

  it('PAGER-2 (the spec that actually discriminates ceil-vs-floor and the missing Math.min on `last`): short final page', () => {
    const result = pagerLabels({ limit: 50, offset: 450, total: 474 })

    expect(result.showing).toBe('SHOWING 451–474 OF 474')
    expect(result.page).toBe('PAGE 10 / 10')
  })

  it('PAGER-3: zero total renders SHOWING 0 OF 0 / PAGE 1 / 1, not "1-0 of 0" or an unguarded ceil(0/50)=0', () => {
    const result = pagerLabels({ limit: 50, offset: 0, total: 0 })

    expect(result.showing).toBe('SHOWING 0 OF 0')
    expect(result.page).toBe('PAGE 1 / 1')
  })

  it('PAGER-4 (AC-9 gap — no shipped spec exercised limit<1 before Stage 1): limit:0 produces no NaN and no Infinity', () => {
    const result = pagerLabels({ limit: 0, offset: 0, total: 10 })

    expect(result.showing).not.toContain('NaN')
    expect(result.showing).not.toContain('Infinity')
    expect(result.page).not.toContain('NaN')
    expect(result.page).not.toContain('Infinity')
  })

  it('PAGER-5 (QA Stage 4 gap — the `last < first` arm is reached by ANY offset >= total, not only limit<1): a stale page after a filter narrows the result set shows 0, not an inverted "51–10"', () => {
    const result = pagerLabels({ limit: 50, offset: 50, total: 10 })

    expect(result.showing).toBe('SHOWING 0 OF 10')
    expect(result.page).toBe('PAGE 1 / 1')
  })
})

describe('parseReviewHash / formatReviewHash (AC-4)', () => {
  it('HASH-1: round-trips a uuid with case preserved verbatim (never lower-cased); a foreign hash and empty string are null', () => {
    const id = 'A1B2C3D4-E5F6-47A8-89AB-CDEF01234567'

    expect(formatReviewHash(id)).toBe(`#review/${id}`)
    expect(parseReviewHash(formatReviewHash(id))).toBe(id)
    expect(parseReviewHash('#somewhere-else')).toBeNull()
    expect(parseReviewHash('')).toBeNull()
  })

  it('HASH-2 (widened beyond the shipped table): a malformed or non-uuid fragment is rejected — never a startsWith+slice that would accept a path-traversal-shaped tail', () => {
    const id = 'a1b2c3d4-e5f6-47a8-89ab-cdef01234567'

    expect(parseReviewHash('#review/../../etc')).toBeNull()
    expect(parseReviewHash('#review/')).toBeNull()
    expect(parseReviewHash(`#review/${id}/extra`)).toBeNull()
    expect(parseReviewHash(`#review/${id}?x=1`)).toBeNull()
    expect(parseReviewHash(`#REVIEW/${id}`)).toBeNull()
  })
})

describe('unreadableRows (AC-4)', () => {
  it('UNREAD-1: the row/rows union reads through the shipped rowErrorRows reader, absent field renders —, message is verbatim; rows:[5,6] yields TWO entries', () => {
    const errors: RowError[] = [{ rows: [5, 6], message: 'quarantined: duplicate invoice_number' }]

    const result = unreadableRows(errors)

    expect(result).toHaveLength(2)
    expect(result[0]).toEqual({ row: 5, column: '—', message: 'quarantined: duplicate invoice_number' })
    expect(result[1]).toEqual({ row: 6, column: '—', message: 'quarantined: duplicate invoice_number' })
  })

  it('UNREAD-2: an error with neither row nor rows is kept as ONE row:null entry — a naive flatMap(rowErrorRows) would silently drop it', () => {
    const errors: RowError[] = [{ message: 'unreadable file structure' }]

    const result = unreadableRows(errors)

    expect(result).toHaveLength(1)
    expect(result[0]).toEqual({ row: null, column: '—', message: 'unreadable file structure' })
  })
})

// --- INVCR-01-09 (task-285, Stage 2.5/Mode A) — RED specs for the six new exports
// (routeAfterImport, reviewShellState, reviewHeader, reviewTabs, reviewHash,
// unreadableCsv), added by 09 on top of the exports 08 already shipped above. Every one
// of the six throws `new Error('not implemented')` (see reviewBatch.ts's "09 STUB"
// comment), so specs against them fail on that throw — the correct RED reason.
//
// Three specs below are GREEN-BEFORE, not RED, and are labelled as such at their own
// describe block: SHELL-4 and SHELL-7 per task-285's own audit, and UNREAD-3 (an
// independent finding made while authoring this file, not called out in the plan) —
// all three exercise channelTiles/unreadableRows, which 08 already shipped. They are
// still authored here because each pins an invariant nothing else in this suite covers:
// SHELL-4 is the only spec pinning a NON-zero unreadable count from two RowError shapes
// in one batch, SHELL-7 guards the UnreadableRow shape against a future `...e` spread,
// and UNREAD-3 re-homes RPT-03's invariant (a rule_key-bearing RowError stays
// structural) before importReport.ts's structuralErrorRows — RPT-03's only other home —
// is deleted in Stage 3.
//
// task-285's own table retired SHELL-1 (unimplementable under environment:'node' — no
// `location`, no React; replaced by HASH-3 below), SHELL-2 (green-before AND a literal
// duplicate of CHANNEL-2, already above), and SHELL-3 (would smuggle a styling literal —
// `dashed`/`muted` as constants — into a data structure); none of the three are
// re-authored here. See this task's QA report for the reasoning on each.

const BASE_ROUTE_REPORT: ImportReport = {
  id: 'batch-route-base',
  status: 'completed',
  format: 'csv',
  delimiter: ',',
  encoding: 'utf-8',
  rows_total: 10,
  rows_valid: 10,
  rows_invalid: 0,
  ready_invoices: 1,
  quarantined_invoices: 0,
  errors: [],
  rule_set_version: 5,
  invoices_clean: 1,
  invoices_with_violations: 0,
  invoice_violations: [],
}

describe('routeAfterImport (AC-9, task-285 Implementation Plan §3 — order is load-bearing: status, then count, then id)', () => {
  it('ROUTE-1: zero ready invoices is rejected, even on a completed import — nothing was created', () => {
    const report: ImportReport = { ...BASE_ROUTE_REPORT, id: 'batch-r1', ready_invoices: 0 }

    const result = routeAfterImport(report, null)

    expect(result).toEqual({ kind: 'rejected', batchId: 'batch-r1' })
  })

  it('ROUTE-2: exactly one ready invoice with a resolved id opens that invoice', () => {
    const report: ImportReport = { ...BASE_ROUTE_REPORT, id: 'batch-r2', ready_invoices: 1 }

    const result = routeAfterImport(report, 'u-7')

    expect(result).toEqual({ kind: 'single', invoiceId: 'u-7' })
  })

  it('ROUTE-3: more than one ready invoice routes to review', () => {
    const report: ImportReport = { ...BASE_ROUTE_REPORT, id: 'batch-r3', ready_invoices: 12 }

    const result = routeAfterImport(report, null)

    expect(result).toEqual({ kind: 'review', batchId: 'batch-r3' })
  })

  it('ROUTE-4: a failed import is rejected regardless of count — catches an impl that checks count before status', () => {
    const report: ImportReport = { ...BASE_ROUTE_REPORT, id: 'batch-r4', status: 'failed', ready_invoices: 3 }

    const result = routeAfterImport(report, 'u-9')

    expect(result).toEqual({ kind: 'rejected', batchId: 'batch-r4' })
  })

  it('ROUTE-5: a non-completed, non-"failed" status is still rejected with a NON-zero ready_invoices — catches an `=== \'failed\'` whitelist that would let any other status fall through to the count check and pass by coincidence', () => {
    const report: ImportReport = {
      ...BASE_ROUTE_REPORT,
      id: 'batch-r5',
      status: 'pending' as ImportReport['status'],
      ready_invoices: 5,
    }

    const result = routeAfterImport(report, null)

    expect(result).toEqual({ kind: 'rejected', batchId: 'batch-r5' })
  })

  it('ROUTE-6: one ready invoice with NO resolvable id (null) degrades to review, never single with a missing invoiceId', () => {
    const report: ImportReport = { ...BASE_ROUTE_REPORT, id: 'batch-r6', ready_invoices: 1 }

    const result = routeAfterImport(report, null)

    expect(result).toEqual({ kind: 'review', batchId: 'batch-r6' })
  })

  it("ROUTE-7 (NEW — closes a gap ROUTE-6 leaves open): one ready invoice with an EMPTY STRING id also degrades to review — '' is falsy but still a string, so a `resolvedInvoiceId != null` guard would wrongly let it through into 'single'", () => {
    const report: ImportReport = { ...BASE_ROUTE_REPORT, id: 'batch-r7', ready_invoices: 1 }

    const result = routeAfterImport(report, '')

    expect(result).toEqual({ kind: 'review', batchId: 'batch-r7' })
    expect(result.kind).not.toBe('single')
  })

  it("ROUTE-8 (NEW — closes a gap nothing else in this table reaches): a resolved id never overrides the count — 5 ready invoices with a resolvable id still routes to review, catching an impl shaped `if (resolvedInvoiceId) return single`", () => {
    const report: ImportReport = { ...BASE_ROUTE_REPORT, id: 'batch-r8', ready_invoices: 5 }

    const result = routeAfterImport(report, 'u-7')

    expect(result).toEqual({ kind: 'review', batchId: 'batch-r8' })
  })

  it('ROUTE-9 (QA Stage 4, mutation-found — closes a gap NOTHING in ROUTE-1..8 reaches): a non-completed status with EXACTLY one ready invoice and a resolved id is still rejected, never single — this is the one fixture combination that actually makes "status before count before id" observable. ROUTE-4/5 use counts other than 1, so reordering the status check to run AFTER the count===1-and-id branch (promoting `single` above `failed`) passes every other spec in this file and is only caught here', () => {
    const report: ImportReport = { ...BASE_ROUTE_REPORT, id: 'batch-r9', status: 'failed', ready_invoices: 1 }

    const result = routeAfterImport(report, 'u-7')

    expect(result).toEqual({ kind: 'rejected', batchId: 'batch-r9' })
    expect(result.kind).not.toBe('single')
  })
})

describe('reviewHash (AC-1, HASH-3 — replaces SHELL-1, which cannot exist under environment:\'node\': no `location`, no React)', () => {
  it('HASH-3: the hash is written ONLY on view=create + createStep=review with a non-empty batch id, and cleared on every other combination — including view=invoices (the Finish / ← Invoices exit, where a lingering hash would bounce a reload straight back into review)', () => {
    expect(reviewHash('create', 'review', 'u-1')).toBe('#review/u-1')
    expect(reviewHash('invoices', 'review', 'u-1')).toBeNull()
    expect(reviewHash('create', 'form', 'u-1')).toBeNull()
    expect(reviewHash('create', 'review', null)).toBeNull()
    expect(reviewHash('create', 'review', '')).toBeNull()
  })
})

describe('channelTiles: unreadable sums BOTH RowError shapes in one batch (SHELL-4, task-285 §8 — MEDIUM, GREEN-BEFORE: channelTiles shipped in 08/task-284; kept here because it is the only spec in this suite pinning tile=tab=footer at a NON-zero value)', () => {
  it('SHELL-4: a row-scalar error and a rows-array error in the same batch sum to 4 unreadable rows, not 2 (one entry per array) or 1 (collapsed)', () => {
    const errors: RowError[] = [
      { row: 5, message: 'm1' },
      { rows: [8, 9, 10], message: 'm2' },
    ]

    const result = channelTiles({ errors, rule_set_version: 5 }, { cleanTotal: 0, failingTotal: 0 })

    expect(result.frozen.unreadable).toBe(4)
  })
})

describe('reviewTabs (AC-4, §7.2)', () => {
  it('SHELL-5: the Unreadable rows tab is OMITTED from the array at zero, not merely hidden — length 1, the remaining tab is "invoices"', () => {
    const result = reviewTabs({ invoices: 12, unreadable: 0 })

    expect(result).toHaveLength(1)
    expect(result[0].id).toBe('invoices')
  })

  it('SHELL-5b (NEW — nothing in the frozen table pinned the label format or that the two counts come from different sources): both tabs render, each with its own count', () => {
    const result = reviewTabs({ invoices: 500, unreadable: 4 })

    expect(result.map((t) => t.label)).toEqual(['Invoices (500)', 'Unreadable rows (4)'])
  })
})

describe('reviewShellState (AC-7, resolves the AC-7/AC-9 divergence — task-285 Implementation Plan §4)', () => {
  it('SHELL-6: a 201 response with a non-completed status ("pending") is still the rejected-file surface', () => {
    const batch: Pick<ImportBatch, 'status'> = { status: 'pending' }

    expect(reviewShellState(batch)).toBe('rejected')
  })

  it('SHELL-9 (NEW — the resolution of the AC-7/AC-9 contradiction): an all-quarantined batch (completed, ready_invoices 0, rows_invalid > 0) renders the BATCH surface, not §7.5 — even though routeAfterImport classifies the SAME import "rejected", because the two functions answer different questions and are not required to agree', () => {
    const batch: Pick<ImportBatch, 'status'> = { status: 'completed' }
    const report: ImportReport = {
      ...BASE_ROUTE_REPORT,
      id: 'batch-quarantined',
      status: 'completed',
      ready_invoices: 0,
      rows_total: 10,
      rows_valid: 0,
      rows_invalid: 10,
    }

    expect(reviewShellState(batch)).toBe('batch')
    expect(routeAfterImport(report, null)).toEqual({ kind: 'rejected', batchId: 'batch-quarantined' })
  })
})

describe('reviewTabs + channelTiles on an all-quarantined batch (QA Stage 4 — SHELL-9 only proves reviewShellState/routeAfterImport; this proves the CLAIM §4 makes about what SHELL-9\'s "batch surface" actually renders: an empty Invoices tab alongside a fully populated Unreadable tab, not just that the shell picked "batch" over "rejected")', () => {
  it('an all-quarantined batch (0 live invoices, 10 structural errors) renders Invoices (0) and Unreadable rows (10) — proving the batch surface is not merely selected but genuinely non-empty on the channel that matters', () => {
    const batch: Pick<ImportBatch, 'errors' | 'rule_set_version'> = {
      errors: Array.from({ length: 10 }, (_, i) => ({ row: i + 2, message: `row ${i + 2} failed` })),
      rule_set_version: 5,
    }

    const tiles = channelTiles(batch, { cleanTotal: 0, failingTotal: 0 })
    const tabs = reviewTabs({ invoices: 0, unreadable: tiles.frozen.unreadable })

    expect(tiles.frozen.unreadable).toBe(10)
    expect(tabs.map((t) => t.label)).toEqual(['Invoices (0)', 'Unreadable rows (10)'])
  })
})

describe('unreadableRows: shape guard (SHELL-7, task-285 §8 — GREEN-BEFORE structural guard, not new coverage: UnreadableRow is {row, column, message} BY TYPE, so no entry can carry an id or a status today; kept cheaply in case a future `...e` spread widens the shape)', () => {
  it('SHELL-7: no unreadable row carries an invoiceId, id, or status key, however the source errors are shaped', () => {
    const errors: RowError[] = [
      { row: 4, rule_key: 'duplicate_invoice_number', severity: 'error', field: 'invoice_number', message: 'm1' },
      { rows: [7, 8], message: 'm2' },
    ]

    const result = unreadableRows(errors)

    result.forEach((row) => {
      expect(Object.keys(row).sort()).toEqual(['column', 'message', 'row'])
    })
  })
})

describe('unreadableRows: a rule_key-bearing RowError (store-duplicate) stays structural (UNREAD-3, task-285 §8 — re-homes RPT-03 before importReport.ts\'s structuralErrorRows, RPT-03\'s only other home, is deleted in Stage 3. ALSO GREEN-BEFORE, an independent finding: unreadableRows already ships and already drops rule_key/severity by construction — but the INVARIANT itself has no home left once structuralErrorRows is gone, which is why this is authored rather than skipped)', () => {
  it('UNREAD-3: a RowError carrying rule_key + severity (the store-level duplicate shape) still yields one unreadable row per row, with no rule_key/severity/invoiceId key on it', () => {
    const errors: RowError[] = [{ row: 4, rule_key: 'duplicate_invoice_number', severity: 'error', message: 'duplicate invoice number' }]

    const result = unreadableRows(errors)

    expect(result).toEqual([{ row: 4, column: '—', message: 'duplicate invoice number' }])
    expect(result[0]).not.toHaveProperty('rule_key')
    expect(result[0]).not.toHaveProperty('severity')
    expect(result[0]).not.toHaveProperty('invoiceId')
  })
})

describe('reviewHeader (AC-2, SHELL-8 — the model takes NO file parameter, so the "from {{file}}" clause is unconstructible here, not merely dropped by a conditional)', () => {
  it('SHELL-8: title is "{allTotal} invoices imported" always, the batch id is exposed verbatim, and the subline carries ROWS READ plus the rule-set label', () => {
    const batch: Pick<ImportBatch, 'id' | 'rows_total' | 'rule_set_version' | 'created_at'> = {
      id: 'a1b2c3d4-e5f6-47a8-89ab-cdef01234567',
      rows_total: 1500,
      rule_set_version: 3,
      created_at: '2026-07-30T00:00:00Z',
    }

    const result = reviewHeader(batch, { allTotal: 500 })

    expect(result.title).toBe('500 invoices imported')
    expect(result.batchId).toBe('a1b2c3d4-e5f6-47a8-89ab-cdef01234567')
    expect(result.subline).toContain('1500 ROWS READ')
    expect(result.subline).toContain('NG-MBS v3')
  })
})

describe('unreadableCsv (AC-5, CSV-1 — the §7.4 "Download this list (CSV)" serialization)', () => {
  it('CSV-1 (NEW — a naive join(\',\') corrupts every row after the first comma): a message containing a comma AND a double quote is RFC-4180 escaped; the em-dash column survives unescaped and a null row never renders the string "null"', () => {
    const rows: UnreadableRow[] = [
      { row: 5, column: '—', message: 'rows disagree, and say "why"' },
      { row: null, column: '—', message: 'unreadable file structure' },
    ]

    const csv = unreadableCsv(rows)
    const lines = csv.split('\n')

    expect(lines[0]).toBe('Row,Field,Why it could not be read')
    expect(lines[1]).toBe('5,—,"rows disagree, and say ""why"""')
    expect(lines[2]).not.toContain('null')
  })
})

// --- INVCR-01-10 (task-286, Stage 2.5/Mode A) — RED specs for the 5 new exports
// (reviewFilterReducer, reviewPageQuery, reviewPills, railPills, pagerNav), added by 10
// on top of everything 08/09 already shipped above. Every one of the five throws
// `new Error('not implemented')` (reviewBatch.ts's "10 STUB" comment), so specs against
// them fail on that throw — the correct RED reason (assertion / not-implemented, never
// an import/compile error).
//
// task-286's Stage 1 audited its own frozen 7-row table and retired two of them, neither
// re-authored here:
//   - TAB-4 ("the rail is batch-wide, not page-derived") is VACUOUS as written: its
//     fixture supplied a page AND a summary so a wrong impl could be caught reading the
//     page, but `railPills` takes NO page parameter at all — a function that cannot
//     accept a page cannot be tested for ignoring one. Replaced by RAIL-1/RAIL-2/RAIL-3
//     below, which catch the real wrong impls (a re-sort, a synthesised active pill, a
//     non-empty rail from an empty summary).
//   - TAB-6 ("paging prunes a departed selection") is a DUPLICATE and GREEN-BEFORE:
//     `pruneSelection` ships at invoices.ts:802 and invoices.test.ts:772 + :1110-1134
//     already pin this exactly, including the `→ []` leg. The new fact this subtask adds
//     — that the component calls it on every `rows` change — has no node oracle (no
//     jsdom, no renders); declared as a gap in §7, not re-tested here.
//
// TAB-7 was WEAK in the frozen table (its stated form duplicates shipped PILL-1/PILL-3a).
// Kept as two legs below — TAB-7a (green-before) and TAB-7b (the only leg that can
// actually fail) — per Stage 1's narrowing.
describe('reviewFilterReducer (AC-2/3/4/10, task-286 §3) — offset:0 is structural, not remembered', () => {
  it('TAB-1a: a pill change resets the offset — asserted end-to-end through the real reviewPageQuery composer, not just the reducer output', () => {
    const state: ReviewFilterState = { pill: 'all', ruleKey: null, q: '', offset: 450 }

    const next = reviewFilterReducer(state, { type: 'pill', pill: 'needs-fix' })

    expect(reviewPageQuery('batch-1', next)).toEqual({
      importBatchId: 'batch-1',
      needsFix: true,
      limit: REVIEW_PAGE_SIZE,
      offset: 0,
    })
  })

  it('TAB-1b: a rail click resets the offset — closes the gap the frozen table left open (it only covered the pill arm)', () => {
    const state: ReviewFilterState = { pill: 'all', ruleKey: null, q: '', offset: 450 }

    const next = reviewFilterReducer(state, { type: 'rule', ruleKey: 'vat-standard-rate' })

    expect(next.offset).toBe(0)
    expect(next.ruleKey).toBe('vat-standard-rate')
  })

  it('TAB-1c: a search commit resets the offset — same gap, the search arm', () => {
    const state: ReviewFilterState = { pill: 'all', ruleKey: null, q: '', offset: 450 }

    const next = reviewFilterReducer(state, { type: 'search', q: 'ACME' })

    expect(next.offset).toBe(0)
    expect(next.q).toBe('ACME')
  })

  it("TAB-1d: a no-op action preserves state IDENTITY — catches a `{...s}` spread that would refetch on every redundant click and re-fire the debounce effect's mount-time dispatch", () => {
    const activePill: ReviewFilterState = { pill: 'needs-fix', ruleKey: null, q: '', offset: 0 }
    expect(reviewFilterReducer(activePill, { type: 'pill', pill: 'needs-fix' })).toBe(activePill)

    const committedSearch: ReviewFilterState = { pill: 'all', ruleKey: null, q: 'ACME', offset: 0 }
    expect(reviewFilterReducer(committedSearch, { type: 'search', q: 'ACME' })).toBe(committedSearch)

    // The debounce effect's own mount-time dispatch: q is already '' and the effect
    // commits '' again on mount. Must be a no-op, or every tab mount would refetch once
    // for free before the user has typed anything.
    expect(reviewFilterReducer(initialReviewFilter, { type: 'search', q: '' })).toBe(initialReviewFilter)
  })
})

describe('reviewFilterReducer + reviewPageQuery: whitespace-only search is ABSENT, not empty (AC-3/10, QUERY-2)', () => {
  it("QUERY-2: a whitespace-only search commit trims q to '' and the resulting query carries NO q key at all — ' ' is truthy in JS and non-empty in Go, and would reach ILIKE '% %'", () => {
    const state: ReviewFilterState = { pill: 'all', ruleKey: null, q: 'ACME', offset: 450 }

    const next = reviewFilterReducer(state, { type: 'search', q: '   ' })

    expect(next.q).toBe('')
    expect(next.offset).toBe(0)
    const query = reviewPageQuery('batch-1', next)
    expect('q' in query).toBe(false)
  })
})

describe('reviewPageQuery: all four filters compose into ONE ANDed options object (AC-2/3/4, QUERY-3)', () => {
  it('QUERY-3: pill + rule + search + page 3 compose without dropping any of the four or the batch id — catches a client-side filter and a dropped batch id', () => {
    const state: ReviewFilterState = { pill: 'needs-fix', ruleKey: 'vat-standard-rate', q: 'ACME', offset: 100 }

    const query = reviewPageQuery('batch-1', state)

    expect(query).toEqual({
      importBatchId: 'batch-1',
      needsFix: true,
      ruleKey: 'vat-standard-rate',
      q: 'ACME',
      limit: REVIEW_PAGE_SIZE,
      offset: 100,
    })
  })
})

describe('reviewPills (AC-2, D3) — takes the four totals only, no rows parameter', () => {
  it('TAB-2: pill counts come from the four supplied totals, not a row length — 20 vs 474 catches a clean/failing swap', () => {
    const totals = { allTotal: 500, cleanTotal: 474, failingTotal: 20, queuedTotal: 6 }

    const pills = reviewPills(totals, 'all')

    expect(pills).toEqual([
      { id: 'all', label: 'All', count: 500, active: true },
      { id: 'needs-fix', label: 'Needs a fix', count: 20, active: false },
      { id: 'ready', label: 'Ready to submit', count: 474, active: false },
      { id: 'queued', label: 'Queued', count: 6, active: false },
    ])
  })

  it('TAB-2b: the pill labels are D3\'s, not §7.3\'s — no "Ready to approve", no "Approved"', () => {
    const totals = { allTotal: 500, cleanTotal: 474, failingTotal: 20, queuedTotal: 6 }

    const labels = reviewPills(totals, 'all').map((p) => p.label)

    expect(labels).toEqual(['All', 'Needs a fix', 'Ready to submit', 'Queued'])
    expect(labels).not.toContain('Ready to approve')
    expect(labels).not.toContain('Approved')
  })
})

describe('reviewFilterReducer: a rule pill TOGGLES (AC-4, TAB-3)', () => {
  it('TAB-3: clicking a rule key sets it; clicking the SAME key again clears it — the param is present on the query, then absent', () => {
    const on = reviewFilterReducer(initialReviewFilter, { type: 'rule', ruleKey: 'vat-standard-rate' })
    expect(on.ruleKey).toBe('vat-standard-rate')
    expect(reviewPageQuery('batch-1', on)).toHaveProperty('ruleKey', 'vat-standard-rate')

    const off = reviewFilterReducer(on, { type: 'rule', ruleKey: 'vat-standard-rate' })
    expect(off.ruleKey).toBeNull()
    expect('ruleKey' in reviewPageQuery('batch-1', off)).toBe(false)
  })
})

describe('railPills (AC-4, store.go:661-671) — takes NO page and NO invoice array; replaces the retired TAB-4', () => {
  it('RAIL-1: server order and counts pass through UNTOUCHED — catches a client re-sort or a severity filter, either of which would make the rail disagree with the query it fires', () => {
    const summary: RuleCount[] = [
      { rule_key: 'b-rule', invoices: 5 },
      { rule_key: 'a-rule', invoices: 5 },
      { rule_key: 'c-rule', invoices: 1 },
    ]

    const pills = railPills(summary, null)

    expect(pills).toEqual([
      { ruleKey: 'b-rule', count: 5, active: false },
      { ruleKey: 'a-rule', count: 5, active: false },
      { ruleKey: 'c-rule', count: 1, active: false },
    ])
  })

  it('RAIL-2: the active key marks EXACTLY one pill and is NEVER synthesised — an active key absent from the summary strands the user filtered with no rail pill to click off, which this helper must not paper over', () => {
    const summary: RuleCount[] = [
      { rule_key: 'a-rule', invoices: 5 },
      { rule_key: 'b-rule', invoices: 3 },
    ]

    expect(railPills(summary, 'a-rule').map((p) => p.active)).toEqual([true, false])
    expect(railPills(summary, null).map((p) => p.active)).toEqual([false, false])

    const absent = railPills(summary, 'zzz-absent-key')
    expect(absent.map((p) => p.active)).toEqual([false, false])
    expect(absent).toHaveLength(2)
  })

  it("RAIL-3: an empty summary is an empty rail — pairs with the component's isEmpty:()=>false ruling (§4), which keeps \"no rules failed\" from reading as null/loading", () => {
    expect(railPills([], null)).toEqual([])
    expect(railPills([], 'some-key')).toEqual([])
  })
})

describe("pagerNav (AC-5) — fed the RESPONSE's echoed pagination, never REVIEW_PAGE_SIZE", () => {
  it('PAGE-1: disables at both ends', () => {
    expect(pagerNav({ limit: 50, offset: 0, total: 500 })).toMatchObject({ canPrev: false, canNext: true })
    expect(pagerNav({ limit: 50, offset: 450, total: 500 })).toMatchObject({ canPrev: true, canNext: false })
  })

  it("PAGE-2: offsets step by the RESPONSE's limit — catches offset-1/offset+1 and an unclamped prev going negative", () => {
    expect(pagerNav({ limit: 50, offset: 100, total: 500 })).toMatchObject({ prevOffset: 50, nextOffset: 150 })
    expect(pagerNav({ limit: 50, offset: 0, total: 500 }).prevOffset).toBe(0)
  })

  it('PAGE-3: a zero total AND a stale offset both disable Next — pairs with the shipped PAGER-5, which proves the LABELS stay honest in the same state', () => {
    expect(pagerNav({ limit: 50, offset: 0, total: 0 }).canNext).toBe(false)
    expect(pagerNav({ limit: 50, offset: 50, total: 10 }).canNext).toBe(false)
  })
})

describe("verdictPill: every renderable status label is one of invoiceStatusStyle's 7 (AC-7, TAB-7a — GREEN-BEFORE: verdictPill and invoiceStatusStyle both shipped in 08; kept here because nothing else in this file iterates the full InvoiceStatus union against verdictPill specifically, which is the form this subtask's own AC-7 states)", () => {
  it('TAB-7a: all 7 canonical statuses, through verdictPill, render a label drawn from the shipped 7 — never an invented lifecycle name', () => {
    const ALL_STATUSES: InvoiceStatus[] = ['draft', 'validated', 'queued', 'submitted', 'accepted', 'rejected', 'failed']
    const SHIPPED_LABELS = new Set(['DRAFT', 'VALIDATED', 'QUEUED', 'SUBMITTED', 'ACCEPTED', 'REJECTED', 'FAILED'])

    for (const status of ALL_STATUSES) {
      const label = verdictPill({ status, violations: [] }).status.label
      expect(SHIPPED_LABELS.has(label)).toBe(true)
    }
  })
})

// TAB-7b (AC-7, GUARD — red-before ONLY because ReviewInvoicesTab.tsx does not exist yet;
// task-286 Stage 1 states this is NOT coverage of any behaviour, just a fact of Stage
// 2.5's timing relative to Stage 3. `readFileSync` throws ENOENT today, which is a
// legitimate red for a DIFFERENT reason than every other spec in this file (a thrown
// not-implemented, or a genuine value mismatch) — this failure does not mean "the
// assertion caught a bug", only "the file to scan doesn't exist yet". Once Stage 3
// creates the component, this becomes the ONLY leg in this entire suite that can catch
// this subtask's own wrong impl writing one of the three D2-forbidden lifecycle names
// (`Pending`/`Approved`/`Transmitted`, story decision D2) into the new file. Reads the
// component BY PATH, never this test file, to avoid self-matching (PILL-3b's shipped
// idiom, reviewBatch.ts:104) — this describe block's own comments legitimately mention
// all three forbidden names.
describe('ReviewInvoicesTab.tsx source: none of the three D2-forbidden lifecycle names appear (AC-7, TAB-7b)', () => {
  it('TAB-7b: the component file contains no Pending/Approved/Transmitted, in any case, including in comments', () => {
    const srcPath = fileURLToPath(new URL('../components/ReviewInvoicesTab.tsx', import.meta.url))
    const source = readFileSync(srcPath, 'utf8')

    for (const forbidden of [/\bpending\b/i, /\bapproved\b/i, /\btransmitted\b/i]) {
      expect(source).not.toMatch(forbidden)
    }
  })
})
