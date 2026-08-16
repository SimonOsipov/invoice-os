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

import {
  invoiceStatusStyle,
  pruneSelection,
  skipReasonLabel,
  type BatchSubmitResultItem,
  type EditFieldKey,
  type InvoiceApproval,
  type InvoiceRecord,
  type InvoiceStatus,
  type RuleCount,
} from './invoices'
import { severityStyle, type Violation } from './validationApi'
import type { ImportBatch, ImportReport, RowError } from './importApi'
import { MAX_RUN_FILES, type ImportRun } from './importRun'
import { fmtDateTime } from './format'
import {
  ALREADY_IMPORTED_CSV_HEADER_ALL,
  alreadyImportedCsvAll,
  alreadyImportedRows,
  alreadyImportedRowsAll,
  BATCH_SUBMIT_MAX_IDS,
  bulkBarView,
  bulkOutcome,
  bulkPhaseReducer,
  canKeepAsIs,
  channelTiles,
  channelTilesAll,
  EDIT_FIELD_LABELS,
  filesStrip,
  filterToQuery,
  fixCard,
  fixEditPatch,
  formatReviewHash,
  initialReviewFilter,
  isAlreadyImported,
  pagerLabels,
  pagerNav,
  parseReviewHash,
  railPills,
  REVIEW_HASH_MAX_IDS,
  reviewFilterReducer,
  reviewFooterSummary,
  reviewHash,
  reviewHeader,
  reviewHeaderAll,
  reviewPageQuery,
  reviewPageQueryAll,
  reviewPills,
  REVIEW_PAGE_SIZE,
  reviewQuery,
  reviewQueryAll,
  reviewShellState,
  reviewShellStateAll,
  reviewTabs,
  rowExpansionView,
  routeAfterImport,
  showsSourceFile,
  sourceFileLabel,
  TILE_CAPTION_VALID,
  unreadableCsv,
  unreadableCsvAll,
  unreadableRows,
  unreadableRowsAll,
  verdictPill,
  type AlreadyImportedRowAll,
  type ReviewFilterState,
  type UnreadableRow,
  type UnreadableRowAll,
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

  // KEEP-1 (INVCR-01-15, D6, task-291): pins the ONE dimension PILL-5 above (task-286)
  // leaves unasserted -- the badge's actual COLOUR. PILL-5 proves kept-invalid wins
  // the precedence fight; this proves it renders amber (severityStyle('warning')), the
  // Test Specs table's own "(amber)" parenthetical. Also pins the exact label and the
  // count (the number of BLOCKING violations kept despite, per verdictPill's own
  // count-is-errorCount contract), neither of which PILL-5 checks either.
  it('KEEP-1: a kept row renders KEPT · INVALID in the amber (severity warning) tone, with count = the blocking-violation count', () => {
    const violations: Violation[] = [
      { rule_key: 'r1', severity: 'error', message: 'e1' },
      { rule_key: 'r2', severity: 'error', message: 'e2' },
      { rule_key: 'r3', severity: 'warning', message: 'w1' },
    ]
    const input: VerdictInput = { status: 'draft', violations, kept_as_is_at: '2026-07-30T00:00:00Z' }

    const result = verdictPill(input)

    expect(result.badges[0].label).toBe('KEPT · INVALID')
    expect(result.badges[0].count).toBe(2)
    expect(result.badges[0].tone).toEqual({
      bg: severityStyle('warning').bg,
      border: severityStyle('warning').border,
      text: severityStyle('warning').text,
    })
  })
})

// The kept mark means "kept as-is" only on a draft; on a failed invoice it means
// resolved outside the system, which is not this badge's claim.
describe('verdictPill: the kept mark is a draft-only concept, not resolved-failed (PILL-9..PILL-11)', () => {
  it('PILL-9: a resolved failed row is not badged KEPT · INVALID', () => {
    const input: VerdictInput = { status: 'failed', violations: [], kept_as_is_at: '2026-08-06T00:00:00Z' }

    const result = verdictPill(input)

    expect(result.badges).toEqual([])
    expect(result.status.label).toBe('FAILED')
  })

  it('PILL-10: a resolved failed row badges its violations, never the mark', () => {
    const violations: Violation[] = [{ rule_key: 'r1', severity: 'error', message: 'e1' }]
    const input: VerdictInput = { status: 'failed', violations, kept_as_is_at: '2026-08-06T00:00:00Z' }

    const result = verdictPill(input)

    expect(result.badges).toHaveLength(1)
    expect(result.badges[0].kind).toBe('rules-failed')
    expect(result.badges[0].count).toBe(1)
    expect(result.badges.some((b) => b.kind === 'kept-invalid')).toBe(false)
  })

  // Regression guard: byte-identical to PILL-5 above -- a draft-only gate must not
  // touch the case it already handled correctly.
  it('PILL-5 still holds: a kept draft is still KEPT · INVALID', () => {
    const violations: Violation[] = [
      { rule_key: 'r1', severity: 'error', message: 'e1' },
      { rule_key: 'r2', severity: 'error', message: 'e2' },
    ]
    const input: VerdictInput = { status: 'draft', violations, kept_as_is_at: '2026-07-30T00:00:00Z' }

    const result = verdictPill(input)

    expect(result.badges).toHaveLength(1)
    expect(result.badges[0].kind).toBe('kept-invalid')
    expect(result.badges[0].count).toBe(2)
  })

  it('PILL-11: every non-draft status ignores the mark', () => {
    const nonDraftStatuses: InvoiceStatus[] = ['validated', 'queued', 'submitted', 'accepted', 'rejected', 'failed']

    for (const status of nonDraftStatuses) {
      const result = verdictPill({ status, violations: [], kept_as_is_at: '2026-08-06T00:00:00Z' })
      expect(result.badges.some((b) => b.kind === 'kept-invalid')).toBe(false)
    }
  })

  // No shipped rule is warning-severity today, so a `failed` row can't actually reach
  // here -- verdictPill is pure, so the branch is still pinned in case that changes.
  it('a resolved failed row with only advisory violations badges ADVISORY, never the mark', () => {
    const violations: Violation[] = [{ rule_key: 'r1', severity: 'warning', message: 'w1' }]
    const input: VerdictInput = { status: 'failed', violations, kept_as_is_at: '2026-08-06T00:00:00Z' }

    const result = verdictPill(input)

    expect(result.badges).toHaveLength(1)
    expect(result.badges[0].kind).toBe('advisory')
    expect(result.badges[0].count).toBe(1)
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

    expect(result).toEqual({ importBatchIds: [id], needsFix: true, ruleKey: 'k' })
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
  // Updated in place for BULK-01-06's widening (formatReviewHash now takes an array,
  // parseReviewHash now returns one) -- this spec's own target functions are exactly
  // the two this subtask changes in place (unlike reviewQuery/channelTiles/reviewHeader,
  // which get additive `...All` siblings instead because five OTHER call sites outside
  // App.tsx would not compile). A single-id array still formats byte-identically to the
  // shipped `#review/x` (AC-2), which is what keeps this a widening, not a break.
  it('HASH-1: round-trips a uuid with case preserved verbatim (never lower-cased); a foreign hash and empty string are null', () => {
    const id = 'A1B2C3D4-E5F6-47A8-89AB-CDEF01234567'

    expect(formatReviewHash([id])).toBe(`#review/${id}`)
    expect(parseReviewHash(formatReviewHash([id]))).toEqual([id])
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
// Two specs below are GREEN-BEFORE, not RED, and are labelled as such at their own
// describe block: SHELL-4 and SHELL-7 per task-285's own audit — both exercise
// channelTiles/unreadableRows, which 08 already shipped, and are still authored here
// because each pins an invariant nothing else in this suite covers: SHELL-4 is the only
// spec pinning a NON-zero unreadable count from two RowError shapes in one batch, SHELL-7
// guards the UnreadableRow shape against a future `...e` spread. UNREAD-3 originally
// re-homed RPT-03's invariant here too (a rule_key-bearing RowError stays structural,
// before importReport.ts's structuralErrorRows — RPT-03's only other home — was deleted
// in Stage 3); a later correction inverted it in place — it now pins the entry LEAVING
// unreadableRows, and is genuinely RED until the classifier ships.
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
  // Updated in place for BULK-01-06's widening: the third param is now every id in the
  // run (App.tsx's `reviewBatchIds` state), not one -- a single-id array still writes
  // byte-identically to the shipped `#review/u-1` (AC-2), and several ids join with a
  // comma (HASH-3b, new).
  it('HASH-3: the hash is written ONLY on view=create + createStep=review with a non-empty id array, and cleared on every other combination — including view=invoices (the Finish / ← Invoices exit, where a lingering hash would bounce a reload straight back into review)', () => {
    expect(reviewHash('create', 'review', ['u-1'])).toBe('#review/u-1')
    expect(reviewHash('invoices', 'review', ['u-1'])).toBeNull()
    expect(reviewHash('create', 'form', ['u-1'])).toBeNull()
    expect(reviewHash('create', 'review', [])).toBeNull()
  })

  it('HASH-3b (NEW — several ids in the run all round-trip through the mirror): a two-id array joins with a comma', () => {
    expect(reviewHash('create', 'review', ['u-1', 'u-2'])).toBe('#review/u-1,u-2')
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
    const result = reviewTabs({ invoices: 12, unreadable: 0, alreadyImported: 0 })

    expect(result).toHaveLength(1)
    expect(result[0].id).toBe('invoices')
  })

  it('SHELL-5b (NEW — nothing in the frozen table pinned the label format or that the two counts come from different sources): both tabs render, each with its own count', () => {
    const result = reviewTabs({ invoices: 500, unreadable: 4, alreadyImported: 0 })

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
    const tabs = reviewTabs({ invoices: 0, unreadable: tiles.frozen.unreadable, alreadyImported: tiles.frozen.alreadyImported })

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

    // Post-split: the first entry (rule_key-bearing) yields zero unreadable rows, so
    // this forEach runs over the second entry's two rows (7, 8) alone — still proving
    // the {row, column, message} shape, still green.
    const result = unreadableRows(errors)

    result.forEach((row) => {
      expect(Object.keys(row).sort()).toEqual(['column', 'message', 'row'])
    })
  })
})

describe('unreadableRows / alreadyImportedRows: a rule_key-bearing RowError (store-duplicate) leaves the unreadable channel (UNREAD-3, INVERTED — the invariant this pinned was itself corrected: a store-duplicate is its own already-imported channel, not structural)', () => {
  it('UNREAD-3 (INVERTED): a rule_key-bearing RowError leaves the unreadable channel', () => {
    const errors: RowError[] = [
      {
        row: 4,
        rule_key: 'no-duplicate-invoice-number',
        severity: 'error',
        field: 'invoice_number',
        message: 'An invoice with this number already exists for this entity.',
      },
    ]

    expect(unreadableRows(errors)).toEqual([])
    expect(alreadyImportedRows(errors)).toHaveLength(1)
  })
})

// --- BUG-08: the already-imported channel (AC-1/3/4/5/6) — RED specs transcribed from
// task-405's Implementation Plan Test Specs table. isAlreadyImported/the unreadableRows
// filter/alreadyImportedRows(All)'s real bodies are the executor's job; the stubs
// committed alongside these specs (alreadyImportedRows -> [], alreadyImportedCsvAll ->
// header only, ChannelTiles.frozen's three new fields -> 0/0/true, reviewTabs' third
// count inert) make every spec below fail on an assertion, never a compile error.

// Shared fixture for AIMP-1/AIMP-2/AIMP-10 — one structural entry, one store-duplicate
// entry. A function, not a module-level const, so no test can mutate another's copy.
function mixedErrors(): RowError[] {
  return [
    { rows: [2, 3], message: 'rows disagree on issue_date' },
    {
      rows: [4, 5],
      rule_key: 'no-duplicate-invoice-number',
      severity: 'error',
      field: 'invoice_number',
      invoice_id: 'inv-1',
      message: 'An invoice with this number already exists for this entity.',
    },
  ]
}

describe('unreadableRows + alreadyImportedRows: a mixed errors[] splits into the two channels (AIMP-1/AIMP-2, AC-6)', () => {
  it('AIMP-1: a mixed errors[] splits into the two channels with no row in both and none dropped', () => {
    const errors = mixedErrors()

    const unreadable = unreadableRows(errors)
    const already = alreadyImportedRows(errors)

    expect(unreadable.map((r) => r.row)).toEqual([2, 3])
    expect(already.map((r) => r.row)).toEqual([4, 5])
    expect(unreadable.length + already.length).toBe(4)
  })

  it("AIMP-2: the structural entry's rendered fields are untouched by the split", () => {
    expect(unreadableRows(mixedErrors())).toEqual([
      { row: 2, column: '—', message: 'rows disagree on issue_date' },
      { row: 3, column: '—', message: 'rows disagree on issue_date' },
    ])
  })
})

describe('alreadyImportedRows: invoiceId resolution (AIMP-3/AIMP-4, AC-5)', () => {
  it("AIMP-3: a duplicate entry with no invoice_id yields invoiceId null, never '' or undefined", () => {
    const errors: RowError[] = [
      { rows: [7], rule_key: 'no-duplicate-invoice-number', severity: 'error', message: 'racing-insert backstop' },
    ]

    expect(alreadyImportedRows(errors)).toEqual([{ row: 7, invoiceId: null }])
  })

  it('AIMP-4: rows:[5,6] on one duplicate entry yields TWO rows both pointing at the same invoice', () => {
    const errors: RowError[] = [
      { rows: [5, 6], rule_key: 'no-duplicate-invoice-number', severity: 'error', invoice_id: 'inv-9', message: 'dup' },
    ]

    expect(alreadyImportedRows(errors)).toEqual([
      { row: 5, invoiceId: 'inv-9' },
      { row: 6, invoiceId: 'inv-9' },
    ])
  })
})

describe('channelTilesAll: the already-imported tile counts ROWS and reports the distinct-entry INVOICE count separately (AIMP-5/AIMP-5b/AIMP-6/AIMP-8, AC-3/4)', () => {
  it('AIMP-5: the tiles count ROWS and report the distinct INVOICE count separately', () => {
    const errors: RowError[] = [
      { rows: [1, 2], rule_key: 'no-duplicate-invoice-number', severity: 'error', invoice_id: 'inv-1', message: 'dup' },
      { rows: [3, 4], rule_key: 'no-duplicate-invoice-number', severity: 'error', invoice_id: 'inv-2', message: 'dup' },
      { rows: [5, 6], rule_key: 'no-duplicate-invoice-number', severity: 'error', invoice_id: 'inv-3', message: 'dup' },
      { rows: [7, 8], message: 'structural' },
    ]

    const tiles = channelTilesAll([{ errors, rule_set_version: 5 }], { cleanTotal: 0, failingTotal: 0 })

    expect(tiles.frozen.alreadyImported).toBe(6)
    expect(tiles.frozen.alreadyImportedInvoices).toBe(3)
    expect(tiles.frozen.unreadable).toBe(2)
  })

  // The swapped-noun oracle: 250 duplicate errors[] entries, each covering 3 sheet rows
  // (the real 750-row / 250-invoice line-item shape the reported repro describes). Any
  // fixture with one row per invoice makes alreadyImported === alreadyImportedInvoices
  // and cannot catch a Set-based (distinct-invoice_id) miscount — this is the one spec
  // in the suite shaped so the two numbers are provably different.
  it('AIMP-5b: rows and distinct invoices are DIFFERENT numbers at the reported repro\'s own shape — THE swapped-noun oracle', () => {
    const errors: RowError[] = Array.from({ length: 250 }, (_, i) => ({
      rows: [i * 3 + 1, i * 3 + 2, i * 3 + 3],
      rule_key: 'no-duplicate-invoice-number',
      severity: 'error',
      invoice_id: `inv-${i + 1}`,
      message: 'An invoice with this number already exists for this entity.',
    }))

    const tiles = channelTilesAll([{ errors, rule_set_version: 5 }], { cleanTotal: 0, failingTotal: 0 })

    expect(tiles.frozen.alreadyImported).toBe(750)
    expect(tiles.frozen.alreadyImportedInvoices).toBe(250)
    expect(tiles.frozen.alreadyImported).not.toBe(tiles.frozen.alreadyImportedInvoices)
  })

  it('AIMP-6: rows_valid + unreadable + alreadyImported reconciles to rows_total', () => {
    const mixed: RowError[] = [
      { rows: [1, 2, 3, 4], rule_key: 'no-duplicate-invoice-number', severity: 'error', invoice_id: 'inv-1', message: 'dup' },
      { rows: [5, 6], message: 'structural' },
    ]
    const mixedTiles = channelTilesAll([{ errors: mixed, rule_set_version: 5 }], { cleanTotal: 0, failingTotal: 0 })
    expect(4 + mixedTiles.frozen.unreadable + mixedTiles.frozen.alreadyImported).toBe(10)

    const allDuplicate: RowError[] = Array.from({ length: 250 }, (_, i) => ({
      rows: [i * 3 + 1, i * 3 + 2, i * 3 + 3],
      rule_key: 'no-duplicate-invoice-number',
      severity: 'error',
      invoice_id: `inv-${i + 1}`,
      message: 'dup',
    }))
    const dupTiles = channelTilesAll([{ errors: allDuplicate, rule_set_version: 5 }], { cleanTotal: 0, failingTotal: 0 })
    expect(0 + dupTiles.frozen.unreadable + dupTiles.frozen.alreadyImported).toBe(750)
  })

  it('AIMP-8: alreadyImportedAtZero is an explicit fact, and atZero still means unreadable===0', () => {
    const errors: RowError[] = [
      { rows: [1, 2], rule_key: 'no-duplicate-invoice-number', severity: 'error', invoice_id: 'inv-1', message: 'dup' },
    ]

    const tiles = channelTilesAll([{ errors, rule_set_version: 5 }], { cleanTotal: 0, failingTotal: 0 })

    expect(tiles.atZero).toBe(true)
    expect(tiles.frozen.alreadyImportedAtZero).toBe(false)
  })
})

// QA guard (D2/GAP-3): every AIMP-5/5b fixture gives each duplicate entry a distinct
// invoice_id, so `new Set(...invoice_id).size` also lands on the right number there and
// the mutation survives undetected. This fixture puts TWO unresolved (invoice_id-less)
// entries in the same batch — a Set collapses both into one `undefined` bucket, so a
// Set-based impl reports 2 while entry-counting reports 3.
describe('channelTiles(All): alreadyImportedInvoices counts ENTRIES, not distinct invoice_id (BUG08-QA-1, D2/GAP-3 guard)', () => {
  it('BUG08-QA-1a: two unresolved duplicate entries plus one resolved one count as THREE invoices, not two', () => {
    const errors: RowError[] = [
      { rows: [1], rule_key: 'no-duplicate-invoice-number', severity: 'error', invoice_id: 'inv-1', message: 'dup' },
      { rows: [2], rule_key: 'no-duplicate-invoice-number', severity: 'error', message: 'racing-insert backstop' },
      { rows: [3], rule_key: 'no-duplicate-invoice-number', severity: 'error', message: 'racing-insert backstop' },
    ]

    const tiles = channelTiles({ errors, rule_set_version: 5 }, { cleanTotal: 0, failingTotal: 0 })

    expect(tiles.frozen.alreadyImported).toBe(3)
    expect(tiles.frozen.alreadyImportedInvoices).toBe(3)
  })

  it('BUG08-QA-1b: the same fixture through channelTilesAll (single-batch array) counts THREE, not two', () => {
    const errors: RowError[] = [
      { rows: [1], rule_key: 'no-duplicate-invoice-number', severity: 'error', invoice_id: 'inv-1', message: 'dup' },
      { rows: [2], rule_key: 'no-duplicate-invoice-number', severity: 'error', message: 'racing-insert backstop' },
      { rows: [3], rule_key: 'no-duplicate-invoice-number', severity: 'error', message: 'racing-insert backstop' },
    ]

    const tiles = channelTilesAll([{ errors, rule_set_version: 5 }], { cleanTotal: 0, failingTotal: 0 })

    expect(tiles.frozen.alreadyImportedInvoices).toBe(3)
  })
})

describe('isAlreadyImported / alreadyImportedRows: entries with no attributable row (BUG08-QA-2, no-swallow rule applies to the already-imported channel too)', () => {
  it('BUG08-QA-2a: a duplicate entry with neither row nor rows still yields ONE row:null entry, never zero', () => {
    const errors: RowError[] = [{ rule_key: 'no-duplicate-invoice-number', invoice_id: 'inv-1', message: 'dup' }]

    expect(unreadableRows(errors)).toEqual([])
    expect(alreadyImportedRows(errors)).toEqual([{ row: null, invoiceId: 'inv-1' }])
  })

  it('BUG08-QA-2b: an explicit empty rows array behaves like an absent one — ONE row:null entry, not zero', () => {
    const errors: RowError[] = [{ rows: [], rule_key: 'no-duplicate-invoice-number', message: 'dup' }]

    expect(alreadyImportedRows(errors)).toEqual([{ row: null, invoiceId: null }])
  })
})

describe('isAlreadyImported: an empty-string rule_key (BUG08-QA-3, presence vs equality)', () => {
  it('BUG08-QA-3: rule_key: "" is still non-null, so isAlreadyImported classifies it as already-imported — presence, not truthiness, matching AC-2\'s "tests rule_key presence, not equality with any literal"', () => {
    const e: RowError = { row: 4, rule_key: '', message: 'm' }

    expect(isAlreadyImported(e)).toBe(true)
    expect(unreadableRows([e])).toEqual([])
    expect(alreadyImportedRows([e])).toEqual([{ row: 4, invoiceId: null }])
  })
})

describe('unreadableRowsAll / alreadyImportedRowsAll: mixed batches across a multi-file run (BUG08-QA-4)', () => {
  it('BUG08-QA-4: a duplicate-only file, a structural-only file, and a mixed file each contribute rows to the right channel under the right file label', () => {
    const b1: Pick<ImportBatch, 'id' | 'filename' | 'errors'> = {
      id: 'b1',
      filename: 'f1.csv',
      errors: [
        { rows: [1, 2], rule_key: 'no-duplicate-invoice-number', invoice_id: 'inv-1', message: 'dup' },
      ],
    }
    const b2: Pick<ImportBatch, 'id' | 'filename' | 'errors'> = {
      id: 'b2',
      filename: 'f2.csv',
      errors: [{ row: 5, message: 'bad row' }],
    }
    const b3: Pick<ImportBatch, 'id' | 'filename' | 'errors'> = {
      id: 'b3',
      filename: 'f3.csv',
      errors: [
        { row: 9, message: 'bad row 2' },
        { rows: [10], rule_key: 'no-duplicate-invoice-number', invoice_id: 'inv-2', message: 'dup2' },
      ],
    }

    const already = alreadyImportedRowsAll([b1, b2, b3])
    const unreadable = unreadableRowsAll([b1, b2, b3])

    expect(already).toEqual([
      { row: 1, invoiceId: 'inv-1', file: 'f1.csv' },
      { row: 2, invoiceId: 'inv-1', file: 'f1.csv' },
      { row: 10, invoiceId: 'inv-2', file: 'f3.csv' },
    ])
    expect(unreadable).toEqual([
      { row: 5, column: '—', message: 'bad row', file: 'f2.csv' },
      { row: 9, column: '—', message: 'bad row 2', file: 'f3.csv' },
    ])
  })
})

describe('alreadyImportedCsvAll: RFC-4180 escaping on the fields the new export actually varies (BUG08-QA-5)', () => {
  it('BUG08-QA-5: an embedded double quote in the filename, an embedded comma in a resolved invoice id, and an embedded newline in another filename all round-trip correctly', () => {
    const rows: AlreadyImportedRowAll[] = [
      { file: 'a"b.csv', row: 1, invoiceId: 'inv-1' },
      { file: 'c.csv', row: 2, invoiceId: 'inv,2' },
      { file: 'd\ne.csv', row: 3, invoiceId: null },
    ]

    const csv = alreadyImportedCsvAll(rows)

    expect(csv).toBe(['File,Row,Invoice id', '"a""b.csv",1,inv-1', 'c.csv,2,"inv,2"', '"d\ne.csv",3,'].join('\n'))
  })
})

describe('channelTilesAll: the reconciliation property (AC-4) across a mixed MULTI-FILE run (BUG08-QA-6)', () => {
  it('BUG08-QA-6: rows_valid + unreadable + alreadyImported reconciles to rows_total summed over two differently-shaped batches', () => {
    const batchA: Pick<ImportBatch, 'errors' | 'rule_set_version'> = {
      errors: [
        { row: 3, message: 'bad' },
        { rows: [4, 5], rule_key: 'no-duplicate-invoice-number', invoice_id: 'inv-1', message: 'dup' },
      ],
      rule_set_version: 5,
    }
    const batchB: Pick<ImportBatch, 'errors' | 'rule_set_version'> = {
      errors: [
        { row: 2, message: 'bad2' },
        { row: 3, message: 'bad3' },
        { rows: [4], rule_key: 'no-duplicate-invoice-number', invoice_id: 'inv-2', message: 'dup2' },
      ],
      rule_set_version: 5,
    }
    const rowsValidA = 2 // rows_total 5 - unreadable 1 - alreadyImported 2
    const rowsTotalA = 5
    const rowsValidB = 1 // rows_total 4 - unreadable 2 - alreadyImported 1
    const rowsTotalB = 4

    const tiles = channelTilesAll([batchA, batchB], { cleanTotal: 0, failingTotal: 0 })

    expect(tiles.frozen.unreadable).toBe(3)
    expect(tiles.frozen.alreadyImported).toBe(3)
    expect(rowsValidA + rowsValidB + tiles.frozen.unreadable + tiles.frozen.alreadyImported).toBe(
      rowsTotalA + rowsTotalB,
    )
  })
})

describe('reviewTabs: the third tab (AIMP-7, AC-3)', () => {
  it('AIMP-7: the third tab appears with a true label and is omitted at zero', () => {
    expect(reviewTabs({ invoices: 0, unreadable: 0, alreadyImported: 750 }).map((t) => t.label)).toEqual([
      'Invoices (0)',
      'Already imported (750)',
    ])
    expect(reviewTabs({ invoices: 3, unreadable: 2, alreadyImported: 0 }).map((t) => t.label)).toEqual([
      'Invoices (3)',
      'Unreadable rows (2)',
    ])
  })
})

describe('alreadyImportedCsvAll (AIMP-9, AC-3)', () => {
  it('AIMP-9: the already-imported CSV quotes per RFC-4180 and renders an unresolved id as an empty cell', () => {
    const rows: AlreadyImportedRowAll[] = [
      { file: 'a,b.csv', row: 5, invoiceId: 'inv-1' },
      { file: 'c.csv', row: null, invoiceId: null },
    ]

    const csv = alreadyImportedCsvAll(rows)
    const lines = csv.split('\n')

    expect(lines[0]).toBe(ALREADY_IMPORTED_CSV_HEADER_ALL)
    expect(lines[1]).toBe('"a,b.csv",5,inv-1')
    expect(lines[2]).toBe('c.csv,,')
  })
})

describe('unreadableCsvAll: no longer carries duplicate rows (AIMP-10, AC-6)', () => {
  it('AIMP-10: the structural CSV is byte-unchanged and no longer contains duplicate rows', () => {
    const csv = unreadableCsvAll(unreadableRowsAll([{ id: 'b1', filename: 'a.csv', errors: mixedErrors() }]))

    expect(csv).toBe(
      [
        'File,Row,Field,Why it could not be read',
        'a.csv,2,—,rows disagree on issue_date',
        'a.csv,3,—,rows disagree on issue_date',
      ].join('\n'),
    )
  })
})

describe('reviewBatch.ts source: the already-imported classifier is the SOLE reader of RowError.rule_key beyond railPills/fixCard (AIMP-11, AC-1 — GAP-2 correction: a comment-immune `.rule_key` property-access scan pinned at 4, not a bare `rule_key` string pinned at 1, which already occurs 5x in comments/railPills/fixCard and could never go green)', () => {
  it('AIMP-11 (guard): `.rule_key` occurs exactly 4 times in the source — railPills x2, fixCard x1, and one inside the already-imported classifier', () => {
    const srcPath = fileURLToPath(new URL('./reviewBatch.ts', import.meta.url))
    const source = readFileSync(srcPath, 'utf8')

    const matches = source.match(/\.rule_key\b/g) ?? []

    expect(matches).toHaveLength(4)
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
      importBatchIds: ['batch-1'],
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
      importBatchIds: ['batch-1'],
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
      { id: 'ready', label: 'Validated', count: 474, active: false },
      { id: 'queued', label: 'Queued', count: 6, active: false },
    ])
  })

  it('TAB-2b: the pill labels are D3\'s, not §7.3\'s — no "Ready to approve", no "Approved"', () => {
    const totals = { allTotal: 500, cleanTotal: 474, failingTotal: 20, queuedTotal: 6 }

    const labels = reviewPills(totals, 'all').map((p) => p.label)

    expect(labels).toEqual(['All', 'Needs a fix', 'Validated', 'Queued'])
    expect(labels).not.toContain('Ready to approve')
    expect(labels).not.toContain('Approved')
  })
})

// RED spec (APPR-12-06, task-531, A06-7) — a validated invoice held by an open approval
// run is not "ready to submit" (INVOICES-06's own missing-reason gap): the ready pill's
// label over-claimed. Asserted through reviewPills (REVIEW_PILL_LABELS itself is
// module-private) — fails today against the still-pinned 'Ready to submit' string.
describe('reviewPills: the ready pill no longer over-claims (APPR-12-06, AC #3)', () => {
  it("A06-7: REVIEW_PILL_LABELS.ready is 'Validated', not 'Ready to submit' — a validated row held by an approval run is not ready to submit", () => {
    const totals = { allTotal: 10, cleanTotal: 4, failingTotal: 3, queuedTotal: 3 }

    const readyPill = reviewPills(totals, 'all').find((p) => p.id === 'ready')

    expect(readyPill?.label).toBe('Validated')
  })
})

// RED specs (APPR-16-01, task-534, AC-1) — A06-7 above fixed the ready PILL; two sibling
// strings on the same screen (ReviewBatch.tsx:286 tile caption, :414 footer clause) still
// claim entitlement the pill no longer claims. TILE_CAPTION_VALID/reviewFooterSummary
// don't exist as exports yet, so the named imports above resolve to `undefined` here
// (verified: an ESM named import of a missing export does not throw under this project's
// esbuild-transformed vitest run — it is simply `undefined`) — every assertion below fails
// on that value, not on an import/collection error.
describe('ReviewBatch captions name validation, not entitlement (APPR-16-01, AC-1)', () => {
  it('A16-1: the tile caption names validation, not entitlement', () => {
    expect(typeof TILE_CAPTION_VALID, 'TILE_CAPTION_VALID must be an exported string').toBe('string')
    expect((TILE_CAPTION_VALID as unknown as string).toLowerCase()).not.toContain('ready to submit')
    expect((TILE_CAPTION_VALID as unknown as string).toLowerCase()).toContain('passed every rule')
  })

  it('A16-1b: the footer counter line names the validated count without entitling it', () => {
    expect(typeof reviewFooterSummary, 'reviewFooterSummary must be an exported function').toBe('function')
    const totals = { allTotal: 500, cleanTotal: 474, queuedTotal: 6, failingTotal: 20, keptTotal: 3 }
    const line = (reviewFooterSummary as unknown as (t: typeof totals) => string)(totals)

    expect(line.toLowerCase()).not.toContain('ready to submit')
    // AC-6: the other four clauses stay byte for byte -- only cleanTotal's wording changes.
    expect(line).toContain('500 invoices stored')
    expect(line).toContain('6 queued for transmission')
    expect(line).toContain('20 awaiting a fix')
    expect(line).toContain('3 kept as-is')
  })

  it('A16-1f: reviewFooterSummary stays grammatical at all-zero totals', () => {
    const zero = { allTotal: 0, cleanTotal: 0, queuedTotal: 0, failingTotal: 0, keptTotal: 0 }
    const line = (reviewFooterSummary as unknown as (t: typeof zero) => string)(zero)

    expect(line).toBe('0 invoices stored · 0 validated · 0 queued for transmission · 0 awaiting a fix · 0 kept as-is')
  })

  it('A16-1g: reviewFooterSummary keeps the five-clause · separator at large totals', () => {
    const big = { allTotal: 1_234_567, cleanTotal: 999_999, queuedTotal: 42, failingTotal: 100_000, keptTotal: 7 }
    const line = (reviewFooterSummary as unknown as (t: typeof big) => string)(big)

    expect(line).toBe('1234567 invoices stored · 999999 validated · 42 queued for transmission · 100000 awaiting a fix · 7 kept as-is')
    expect(line.split(' · ')).toHaveLength(5)
  })

  it("A16-1c: ReviewBatch.tsx source contains no 'ready to submit', in any case", () => {
    const srcPath = fileURLToPath(new URL('../components/ReviewBatch.tsx', import.meta.url))
    const source = readFileSync(srcPath, 'utf8')

    expect(source.toLowerCase()).not.toContain('ready to submit')
  })

  it('A16-1c control: the scan read the real file and a known needle is present', () => {
    const srcPath = fileURLToPath(new URL('../components/ReviewBatch.tsx', import.meta.url))
    const source = readFileSync(srcPath, 'utf8')

    // NOT 'cleanTotal' -- it occurs 9x inside lib/reviewBatch.ts too and cannot tell this
    // scan apart from an accidental read of the wrong file (LIB-SCAN-1's own URL).
    expect(source.length).toBeGreaterThan(0)
    expect(source, 'lost anchor on ReviewBatch.tsx').toContain('export function ReviewBatch(')
  })

  // AC-1's "not authored inline" is a DIFFERENT claim than "no forbidden substring":
  // a hand-typed inline literal with the SAME wording as the export passes A16-1c/1d
  // and every runtime render check, because the rendered text is identical either way.
  // Only a source-level check on the call site itself can tell "imported" from
  // "retyped by hand".
  it('A16-1e: both call sites are wired to the exports, not retyped inline', () => {
    const srcPath = fileURLToPath(new URL('../components/ReviewBatch.tsx', import.meta.url))
    const source = readFileSync(srcPath, 'utf8')

    expect(source).toContain('caption={TILE_CAPTION_VALID}')
    expect(source).not.toMatch(/caption=["']Passed every rule\.?["']/)

    expect(source).toContain('{reviewFooterSummary(')
    expect(source).not.toContain('invoices stored')
  })

  it('A16-1d: the pill and the captions no longer contradict each other', () => {
    const totals = { allTotal: 10, cleanTotal: 4, failingTotal: 3, queuedTotal: 3 }
    const readyPill = reviewPills(totals, 'all').find((p) => p.id === 'ready')
    expect(readyPill?.label.toLowerCase()).not.toContain('ready to submit')

    expect(typeof TILE_CAPTION_VALID, 'TILE_CAPTION_VALID must be an exported string').toBe('string')
    expect((TILE_CAPTION_VALID as unknown as string).toLowerCase()).not.toContain('ready to submit')

    expect(typeof reviewFooterSummary, 'reviewFooterSummary must be an exported function').toBe('function')
    const line = (reviewFooterSummary as unknown as (t: typeof totals & { keptTotal: number }) => string)({ ...totals, keptTotal: 0 })
    expect(line.toLowerCase()).not.toContain('ready to submit')
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

// --- QA Stage 4 (task-286) adversarial coverage — the reducer's `rule` arm and pagerNav's
// single-page boundary. Both are genuine gaps in the frozen/authored table above, found by
// mutation testing during verification (both mutations reddened nothing until these specs
// were added): unlike `pill` and `search`, `rule` is documented as NEVER taking the
// identity shortcut (a re-click IS a change — it toggles the filter off), but TAB-1d only
// pins identity for `pill`/`search`; and PAGE-1/2/3 never exercise a batch that fits in one
// page (total <= limit), which is the boundary a `total > 0` guard could get wrong in
// either direction.
describe('reviewFilterReducer: the `rule` arm never takes the identity shortcut (RULE-ID, task-286 QA Stage 4)', () => {
  it('a repeated click on the SAME rule key (the clear-to-null transition) returns a NEW object, unlike pill/search — toggling off is a change, not a no-op', () => {
    const withRule: ReviewFilterState = { pill: 'all', ruleKey: 'vat-standard-rate', q: '', offset: 0 }

    const cleared = reviewFilterReducer(withRule, { type: 'rule', ruleKey: 'vat-standard-rate' })

    expect(cleared.ruleKey).toBeNull()
    // NOT `.toBe(withRule)` — a `rule` arm that took the identity shortcut here would
    // still be functionally correct on THIS assertion alone (ruleKey does become null via
    // a mutating shortcut only if one existed, which it structurally cannot: `s` is never
    // mutated in place), so the discriminator is reference inequality, not the value.
    expect(cleared).not.toBe(withRule)
  })

  it('setting a rule key from null also returns a NEW object', () => {
    const state: ReviewFilterState = { pill: 'all', ruleKey: null, q: '', offset: 0 }

    const next = reviewFilterReducer(state, { type: 'rule', ruleKey: 'vat-standard-rate' })

    expect(next).not.toBe(state)
    expect(next.ruleKey).toBe('vat-standard-rate')
  })
})

// TAB-7b (above) scans ONLY ReviewInvoicesTab.tsx, per task-286 Stage 1's own plan
// ("TAB-7b scans the source" — singular). But reviewBatch.ts's task-286-added section
// (everything from its "INVCR-01-10 (task-286)" header down: the filter reducer, the
// pill/rail/pager view-models) is equally NEW code from this subtask, and AC-7 says the
// D2-forbidden names "appear nowhere, including in comments" — not "nowhere in the
// component file". This guard closes that gap. Reads reviewBatch.ts BY PATH, matching
// TAB-7b's own self-matching precaution (PILL-3b's idiom) — this describe block's
// comments legitimately name all three forbidden words.
//
// SCOPE (QA Stage 4 re-verify, task-286): scans the WHOLE file, not just the
// task-286-added section below the marker. A marker-to-EOF slice left the ~426 lines
// ABOVE the marker (pre-existing `channelTiles`/`reviewHeader`/`reviewShellState`/
// `reviewTabs`/`unreadableRows`, shipped by earlier subtasks) covered by no guard at
// all — clean today, but silently so, and a future edit up there would go unchecked.
// Whole-file is strictly safer and costs nothing: `source` is already read in full.
// The marker-existence check is kept as a structural sanity check, decoupled from the
// scan itself.
describe('reviewBatch.ts source (whole file): none of the three D2-forbidden lifecycle names appear (AC-7, LIB-SCAN-1)', () => {
  it('LIB-SCAN-1: the whole file contains no Pending/Approved/Transmitted, in any case, including in comments', () => {
    const srcPath = fileURLToPath(new URL('./reviewBatch.ts', import.meta.url))
    const source = readFileSync(srcPath, 'utf8')
    const marker = '--- INVCR-01-10 (task-286)'
    expect(source.indexOf(marker), 'the task-286 marker comment must exist in reviewBatch.ts').toBeGreaterThanOrEqual(0)

    for (const forbidden of [/\bpending\b/i, /\bapproved\b/i, /\btransmitted\b/i]) {
      expect(source).not.toMatch(forbidden)
    }
  })
})

describe('pagerNav: single-page boundary — a batch that fits in one page (PAGE-4, task-286 QA Stage 4)', () => {
  it('total <= limit at offset 0 disables BOTH buttons, not just canNext', () => {
    // 30 invoices, limit 50: everything is already on screen. Neither PAGE-1 (500-row
    // batch) nor PAGE-3 (total:0) exercises "some results, but fewer than one page".
    expect(pagerNav({ limit: 50, offset: 0, total: 30 })).toMatchObject({ canPrev: false, canNext: false })
  })

  it('total exactly equal to limit at offset 0 also disables canNext (the boundary itself, not just under it)', () => {
    expect(pagerNav({ limit: 50, offset: 0, total: 50 })).toMatchObject({ canPrev: false, canNext: false })
  })
})

// --- INVCR-01-11 (task-287, Stage 2.5/Mode A) — RED specs for the bulk-submit bar's pure
// model: BATCH_SUBMIT_MAX_IDS, bulkPhaseReducer, bulkBarView, bulkOutcome. All three
// functions throw `new Error('not implemented')` today (reviewBatch.ts's "11 STUB"
// comment), so every spec below fails on that throw — the correct RED reason — except
// BULK-14, which is GREEN-BEFORE by design (BATCH_SUBMIT_MAX_IDS is a real constant, not
// a stub; see its own describe block).
//
// task-287 Stage 1 audited §7.3's frozen 8-row table and authored this 16-row one in its
// place. THREE of the frozen rows are deliberately NOT re-authored here:
//   - "two sequential submits -> two distinct keys" is DROPPED as a duplicate of
//     already-green newIdempotencyKey distinctness coverage (invoices.test.ts's I-key-1,
//     :704-712, and KEY-1, :1199-1210) AND as unimplementable under environment:'node'
//     (it needs the component's click handlers, which have no node oracle — see BULK-15
//     below for the reachable, structural substitute: a source scan pinning WHERE the key
//     is minted).
//   - "an unknown skip reason passes through verbatim" is DROPPED as a duplicate of
//     already-green invoices.test.ts I-skip-1 (`skipReasonLabel('wat') === 'wat'`).
//     BULK-10 below covers the genuinely new claim built on top of it (the *result row*
//     built from that label), which I-skip-1 does not touch.
// lib/invoices.ts and lib/invoices.test.ts are NOT touched by this subtask — both rows
// above are dropped specifically because they already have oracles there.

// Local fixtures only — lib/invoices.test.ts's own `draftInvoice` is not imported or
// reused (that file stays untouched by this subtask). Mirrors its shape.
function mkRow(id: string, status: InvoiceStatus, overrides: Partial<InvoiceRecord> = {}): InvoiceRecord {
  return {
    id,
    entity_id: 'e1',
    import_batch_id: 'batch-1',
    invoice_number: `INV-${id}`,
    status,
    issue_date: '2026-07-01T00:00:00Z',
    supplier_tin: '00000000001',
    supplier_name: 'Acme Ltd',
    buyer_tin: '00000000002',
    buyer_name: 'Beta Ltd',
    currency: 'NGN',
    subtotal: '1000.00',
    vat: '75.00',
    total: '1075.00',
    violations: [],
    rule_set_version_id: null,
    created_at: '2026-07-01T00:00:00Z',
    irn: null,
    csid: null,
    qr_payload: null,
    rejection_reasons: [],
    kept_as_is_at: null,
    kept_as_is_by: null,
    kept_as_is_reason: null,
    failure_kind: null,
    approval: null,
    rule_set_version: null,
    can_approve: false,
    approve_blocked_reason: null,
    ...overrides,
  }
}

function buildRows(count: number, status: InvoiceStatus): InvoiceRecord[] {
  return Array.from({ length: count }, (_, i) => mkRow(`inv-${status}-${i}`, status))
}

// BULK-2/BULK-8's shared 50-row page: 12 validated, 20 draft+error, 8 draft clean, 6
// queued, 4 rejected — every one of the 5 non-validated groups counted, not just the
// failing ones. notReady = 50 - 12 = 38.
function buildMixedReviewPage(): InvoiceRecord[] {
  const validated = buildRows(12, 'validated')
  const draftWithError = Array.from({ length: 20 }, (_, i) =>
    mkRow(`inv-draft-err-${i}`, 'draft', { violations: [{ rule_key: 'r', severity: 'error', message: 'e' }] }),
  )
  const draftClean = buildRows(8, 'draft')
  const queued = buildRows(6, 'queued')
  const rejected = buildRows(4, 'rejected')
  return [...validated, ...draftWithError, ...draftClean, ...queued, ...rejected]
}

describe('bulkBarView: eligible IS pruneSelection, not the raw selection (BULK-1, AC-2)', () => {
  it('BULK-1: 5 selected ids, 2 of which left the page or are no longer validated, prune to the 3 survivors, in order', () => {
    const rows = [mkRow('a', 'validated'), mkRow('b', 'validated'), mkRow('c', 'validated'), mkRow('d', 'queued')]
    // 'e' is absent from `rows` entirely — it left the page. 'd' is present but no
    // longer validated.
    const selected = ['a', 'b', 'c', 'd', 'e']

    const view = bulkBarView(selected, rows, 'idle', false)

    expect(view.eligible).toEqual(pruneSelection(selected, rows))
    expect(view.eligible).toEqual(['a', 'b', 'c'])
  })
})

describe('bulkBarView: notReady counts every non-selectable row, not just the failing ones (BULK-2, AC-2/8)', () => {
  it('BULK-2: a page of 50 with 12 validated and 38 non-validated (draft+error, draft clean, queued, rejected) reports notReady === 38', () => {
    const rows = buildMixedReviewPage()

    const view = bulkBarView([], rows, 'idle', false)

    expect(view.notReady).toBe(38)
  })
})

describe('bulkPhaseReducer: a confirm that was never armed changes nothing (BULK-3, AC-3)', () => {
  it("BULK-3: bulkPhaseReducer('idle', {confirm}) returns 'idle' — the component's `if (next === phase) return` fires zero requests", () => {
    expect(bulkPhaseReducer('idle', { type: 'confirm' })).toBe('idle')
  })
})

describe('bulkPhaseReducer: a second confirm cannot re-enter (BULK-4, AC-3/4)', () => {
  it("BULK-4: bulkPhaseReducer('submitting', {confirm}) and ('submitting', {cancel}) both return identity — the double-click guard and \"the server already has it\", both structural", () => {
    expect(bulkPhaseReducer('submitting', { type: 'confirm' })).toBe('submitting')
    expect(bulkPhaseReducer('submitting', { type: 'cancel' })).toBe('submitting')
  })
})

describe('bulkPhaseReducer: the rest of the phase table (BULK-5, AC-3)', () => {
  it('BULK-5: arm idle->armed, armed->identity, submitting->identity; cancel armed->idle; settled from all three -> idle', () => {
    expect(bulkPhaseReducer('idle', { type: 'arm' })).toBe('armed')
    expect(bulkPhaseReducer('armed', { type: 'arm' })).toBe('armed')
    expect(bulkPhaseReducer('submitting', { type: 'arm' })).toBe('submitting')
    expect(bulkPhaseReducer('armed', { type: 'cancel' })).toBe('idle')
    expect(bulkPhaseReducer('idle', { type: 'settled' })).toBe('idle')
    expect(bulkPhaseReducer('armed', { type: 'settled' })).toBe('idle')
    expect(bulkPhaseReducer('submitting', { type: 'settled' })).toBe('idle')
  })
})

describe('bulkBarView: the stale-page gate — this subtask\'s primary correctness requirement (BULK-6, AC-1/2)', () => {
  it('BULK-6: canSubmit and canSubmitAll are both false while the page is loading, both true once it settles, and both false with nothing eligible', () => {
    const rows = buildRows(12, 'validated')
    const selected = rows.map((r) => r.id)

    // Select 5 (well, 12) -> Next -> submit before the response lands: canSubmit/
    // canSubmitAll must close this window, not just the row checkboxes.
    const whileLoading = bulkBarView(selected, rows, 'armed', true)
    expect(whileLoading.canSubmit).toBe(false)
    expect(whileLoading.canSubmitAll).toBe(false)

    const settled = bulkBarView(selected, rows, 'armed', false)
    expect(settled.canSubmit).toBe(true)
    expect(settled.canSubmitAll).toBe(true)

    // Zero eligible: a page with no validated rows at all — nothing to submit, loading
    // or not.
    const nothingEligible = bulkBarView([], buildRows(5, 'queued'), 'armed', false)
    expect(nothingEligible.canSubmit).toBe(false)
    expect(nothingEligible.canSubmitAll).toBe(false)
  })
})

describe('bulkBarView: the count label carries its own scope (BULK-7, AC-1)', () => {
  it("BULK-7: 12 eligible -> countLabel === '12 selected on this page' — the scope is IN the string, not implied", () => {
    const rows = buildRows(12, 'validated')
    const view = bulkBarView(rows.map((r) => r.id), rows, 'idle', false)

    expect(view.countLabel).toBe('12 selected on this page')
  })
})

// REWRITTEN (APPR-08-10, task-502). The note used to name a status word, and that made it
// false the moment a row this bar cannot send IS validated -- an open approval run holds a
// validated row out of `eligible` while its own Verdict pill still reads VALIDATED. The
// note's own doc comment in reviewBatch.ts already required it to name no cause; the
// shipped string broke that rule, so removing the clause corrects a false claim rather
// than making a new product decision. BULK-A3 below is the shape that falsified it.
describe('bulkBarView: the note is absent at zero and names no cause, so it is true for every non-selectable row (BULK-8, AC-1)', () => {
  it('BULK-8: notReady:0 -> note is null; the BULK-2 page -> the exact page-scoped count, naming no cause', () => {
    const allValidated = buildRows(5, 'validated')
    expect(bulkBarView([], allValidated, 'idle', false).note).toBeNull()

    const mixed = buildMixedReviewPage()
    const view = bulkBarView([], mixed, 'idle', false)

    expect(view.note).toBe('38 of the 50 rows on this page cannot be sent.')
  })

  it('BULK-A3: two validated rows, one held by an open run -> the same sentence, still true', () => {
    // The old copy read "Only VALIDATED rows can be sent. 1 of the 2 on this page cannot."
    // on exactly this input -- both rows ARE validated, so it contradicted itself on
    // screen. OPEN_RUN is declared at module scope further down; it is initialized during
    // module evaluation, before any it() body runs.
    const gated = [mkRow('a', 'validated'), mkRow('b', 'validated', { approval: OPEN_RUN })]

    expect(bulkBarView([], gated, 'idle', false).note).toBe('1 of the 2 rows on this page cannot be sent.')
  })

  it('BULK-A4: a single-row page whose one row cannot be sent reads "row", not "rows"', () => {
    // Reachable from a search or a rule filter that narrows the page to one hit.
    expect(bulkBarView([], [mkRow('a', 'queued')], 'idle', false).note).toBe(
      '1 of the 1 row on this page cannot be sent.',
    )
  })
})

describe('bulkBarView: the confirm names the count, the action, the outcome and the irreversibility (BULK-9, AC-3)', () => {
  it('BULK-9: 12 eligible -> the exact confirm copy, including QUEUED sourced from invoiceStatusStyle; 1 eligible -> the singular form', () => {
    const rows12 = buildRows(12, 'validated')
    const view12 = bulkBarView(rows12.map((r) => r.id), rows12, 'idle', false)

    expect(view12.confirmPrompt).toBe('Send 12 invoices for transmission?')
    expect(view12.confirmLabel).toBe('Yes, send 12 now')
    expect(view12.confirmDetail).toContain(invoiceStatusStyle('queued').label)
    expect(view12.confirmDetail).toContain('pull them back')

    const rows1 = buildRows(1, 'validated')
    const view1 = bulkBarView(rows1.map((r) => r.id), rows1, 'idle', false)
    expect(view1.confirmPrompt).toBe('Send 1 invoice for transmission?')
  })
})

describe('bulkOutcome: each result row is built from enqueued + reason, never from status (BULK-10, AC-5)', () => {
  it('BULK-10: an enqueued item, a skipped item with a known reason, and a skipped item with no reason resolve to distinct labels; an id absent from numbersById falls back to the raw id', () => {
    const items: BatchSubmitResultItem[] = [
      { invoice_id: 'a', enqueued: true, status: 'queued' },
      { invoice_id: 'b', enqueued: false, status: 'validated', reason: 'not_validated' },
      { invoice_id: 'c', enqueued: false, status: 'validated' },
    ]
    const numbersById = new Map([
      ['a', 'INV-A'],
      ['b', 'INV-B'],
      // 'c' deliberately absent.
    ])

    const outcome = bulkOutcome({ ok: true, items }, numbersById)

    expect(outcome.results).toEqual([
      { invoiceNumber: 'INV-A', label: 'Queued', enqueued: true },
      { invoiceNumber: 'INV-B', label: skipReasonLabel('not_validated'), enqueued: false },
      { invoiceNumber: 'c', label: 'Not queued', enqueued: false },
    ])
    expect(outcome.clearSelection).toBe(true)
  })
})

describe('bulkOutcome: a response claiming queued for a SKIPPED item cannot reach the panel (BULK-11, AC-5)', () => {
  it("BULK-11: M5-11's exact shape (enqueued:false, status:'queued', reason:'not_validated') resolves to the SKIP label, and the row carries no `status` key at all", () => {
    const items: BatchSubmitResultItem[] = [{ invoice_id: 'x', enqueued: false, status: 'queued', reason: 'not_validated' }]

    const outcome = bulkOutcome({ ok: true, items }, new Map())

    expect(outcome.results).not.toBeNull()
    const row = outcome.results![0]
    expect(row.label).toBe(skipReasonLabel('not_validated'))
    // Structural, not remembered: `status` is not a key on SubmitResultRow at all, so
    // batch_submit.go's M5-11 hard-coded value is unrepresentable in the output.
    expect(Object.keys(row).sort()).toEqual(['enqueued', 'invoiceNumber', 'label'])
  })
})

describe('bulkOutcome: a malformed 2xx resolves to [], never a throw (BULK-12, AC-5)', () => {
  it("BULK-12: SUB-3's pinned shape ({ok:true, items: undefined}) resolves to results:[], clearSelection:true", () => {
    const outcome = bulkOutcome({ ok: true, items: undefined }, new Map())

    expect(outcome.results).toEqual([])
    expect(outcome.clearSelection).toBe(true)
  })
})

describe('bulkOutcome: a request-level failure keeps the selection and shows no panel (BULK-13, AC-6)', () => {
  it('BULK-13: {ok:false} -> {results: null, clearSelection: false}', () => {
    expect(bulkOutcome({ ok: false }, new Map())).toEqual({ results: null, clearSelection: false })
  })
})

describe('BATCH_SUBMIT_MAX_IDS: the page size can never exceed the server\'s id cap (BULK-14, AC-8, GREEN-BEFORE — a drift guard, not coverage)', () => {
  it('BULK-14: BATCH_SUBMIT_MAX_IDS mirrors handlers.go:718\'s 200, and REVIEW_PAGE_SIZE never exceeds it', () => {
    expect(BATCH_SUBMIT_MAX_IDS).toBe(200)
    expect(REVIEW_PAGE_SIZE).toBeLessThanOrEqual(BATCH_SUBMIT_MAX_IDS)
  })
})

// Source scan, matching TAB-7b/LIB-SCAN-1's own by-path idiom above (never imports the
// component — Stage 3 hasn't built the bulk bar yet, and a node environment cannot
// render it anyway). ReviewInvoicesTab.tsx already exists (task-286 shipped it) but
// contains no `submitInvoices(` call yet — that is Stage 3's job — so this is a genuine
// value mismatch (0 occurrences today, not 1), not an ENOENT the way TAB-7b's first run
// was.
describe('ReviewInvoicesTab.tsx source: the idempotency key is minted inline in the confirm handler, never held (BULK-15, AC-4)', () => {
  it('BULK-15: exactly one submitInvoices( call, with newIdempotencyKey() inside its own argument list, and no useState/useRef initialiser holds the key', () => {
    const srcPath = fileURLToPath(new URL('../components/ReviewInvoicesTab.tsx', import.meta.url))
    const source = readFileSync(srcPath, 'utf8')

    const callSites = source.match(/submitInvoices\(/g) ?? []
    expect(callSites, 'exactly one submitInvoices( call site').toHaveLength(1)

    const call = /submitInvoices\(([^)]*newIdempotencyKey\(\)[^)]*)\)/.exec(source)
    expect(call, 'newIdempotencyKey() must appear inside submitInvoices(...)\'s own argument list').not.toBeNull()

    expect(source).not.toMatch(/use(?:State|Ref)\([^)]*\bnewIdempotencyKey\b/)
  })
})

describe('bulkBarView: the page-scoped submit-all disables at zero eligible (BULK-16, AC-7)', () => {
  it('BULK-16: a page with no validated rows disables canSubmitAll; 12 validated on the page enables it and names the count, independent of the current selection', () => {
    const noneValidated = buildRows(5, 'queued')
    expect(bulkBarView([], noneValidated, 'idle', false).canSubmitAll).toBe(false)

    const rows = buildRows(12, 'validated')
    // Nothing currently selected — canSubmitAll/submitAllLabel are page-scoped off
    // `rows`, not off `selected`: clicking "Submit all" is what selects them.
    const view = bulkBarView([], rows, 'idle', false)

    expect(view.submitAllLabel).toBe('Submit all 12 on this page for transmission')
    expect(view.canSubmitAll).toBe(true)
  })
})

// APPR-08-09 (task-500): bulkBarView reaches the approval predicate through pruneSelection
// and selectableIds, so these two specs prove the ripple lands on the bulk bar from the
// invoices.ts edit alone — reviewBatch.ts is not modified by that subtask.
//
// `view.note` is asserted by BULK-8/BULK-A3 above, not here. It was left unasserted at 09's
// altitude because the shipped sentence was about to become false and pinning it would have
// made the defect permanent; APPR-08-10 (task-502) rewrote the copy and those two specs
// now own it.
const OPEN_RUN: InvoiceApproval = {
  run_state: 'open',
  pending_ord: 1,
  pending_role_title: 'Reviewer',
  pending_holder_warn: false,
  due_at: null,
  overdue: false,
}

describe('bulkBarView inherits the approval gate without reviewBatch.ts changing (APPR-08-09)', () => {
  it('BULK-A1: an awaiting-approval row leaves eligible and counts into notReady', () => {
    const rows = [mkRow('a', 'validated'), mkRow('b', 'validated', { approval: OPEN_RUN })]

    const view = bulkBarView(['a', 'b'], rows, 'idle', false)

    expect(view.eligible).toEqual(['a'])
    expect(view.notReady).toBe(1)
    expect(view.submitAllLabel).toBe('Submit all 1 on this page for transmission')
    expect(view.canSubmitAll).toBe(true)
  })

  it('BULK-A2: a page where every validated row has an open run disables submit-all entirely', () => {
    const rows = Array.from({ length: 4 }, (_, i) => mkRow(`inv-${i}`, 'validated', { approval: OPEN_RUN }))

    const view = bulkBarView([], rows, 'idle', false)

    expect(view.canSubmitAll).toBe(false)
    expect(view.notReady).toBe(4)
    expect(view.eligible).toEqual([])
  })
})

// --- QA Stage 4 (task-287) adversarial coverage — four component-wiring claims that have
// no unit oracle under environment:'node' (no jsdom, no render) but ARE reachable as
// static source scans, matching TAB-7b/BULK-15's own by-path idiom above (never imports
// the component). Each one pins a decision the executor made beyond the plan's own §9-§11
// (recorded in task-287's Implementation Notes, "four decisions the plan did not
// specify") and would regress silently — passing every other spec in this file — if a
// future "simplification" touched the wrong line.
describe('ReviewInvoicesTab.tsx source: disarm() is wired to EVERY selection-changing action, not just a rows/page change (QA-1, Stage 4 — pins the executor\'s unplanned decision #1)', () => {
  it('QA-1: disarm() is called from toggleAll, the Clear button and the row checkbox\'s onToggle — three call sites plus its own definition, four occurrences total. Losing any one reopens: arm 3 -> untick all 3 -> tick 5 others leaves the bar armed on a set nobody confirmed', () => {
    const srcPath = fileURLToPath(new URL('../components/ReviewInvoicesTab.tsx', import.meta.url))
    const source = readFileSync(srcPath, 'utf8')

    const allOccurrences = source.match(/disarm\(\)/g) ?? []
    expect(allOccurrences, 'exactly one definition + three call sites').toHaveLength(4)

    expect(source, 'toggleAll must disarm on every select-all/clear-all click').toMatch(
      /function toggleAll\(\)\s*\{[^}]*disarm\(\)[^}]*\}/,
    )
    expect(source, 'the bulk bar\'s own Clear button must disarm').toMatch(
      /data-testid="review-bulk-clear"[\s\S]{0,200}?disarm\(\)[\s\S]{0,350}?data-testid="review-bulk-submit"/,
    )
    expect(source, 'a single row\'s checkbox toggle must disarm — the exact scenario an unplanned re-tick would otherwise leave armed').toMatch(
      /onToggle=\{\(\) => \{[\s\S]{0,120}?disarm\(\)/,
    )
  })
})

describe('ReviewInvoicesTab.tsx source: the post-await "settled" dispatch always reads the FUNCTIONAL setPhase form, never the stale click-closure `phase` (QA-2, Stage 4 — pins the bug the executor caught and fixed itself)', () => {
  it('QA-2: both the success leg and the catch leg dispatch settled as `setPhase((p) => bulkPhaseReducer(p, ...))` — never `setPhase(bulkPhaseReducer(phase, ...))`, which would fire off the closure captured at click time rather than the phase React holds after the await', () => {
    const srcPath = fileURLToPath(new URL('../components/ReviewInvoicesTab.tsx', import.meta.url))
    const source = readFileSync(srcPath, 'utf8')

    const functionalSettled = source.match(/setPhase\(\s*\(\s*p\s*\)\s*=>\s*bulkPhaseReducer\(\s*p\s*,\s*\{\s*type:\s*'settled'\s*\}\s*\)\s*\)/g) ?? []
    expect(functionalSettled, 'one functional settled dispatch on the success leg, one on the catch leg').toHaveLength(2)

    // The bug pattern: bulkPhaseReducer read the click-time `phase` variable directly and
    // handed setPhase a plain value, which is what a stale closure looks like after an
    // `await` re-enters the handler with a phase React has since moved on from.
    expect(source).not.toMatch(/setPhase\(\s*bulkPhaseReducer\(\s*phase\s*,/)
  })
})

describe('ReviewInvoicesTab.tsx source: the results panel is replaced ONLY by the next submit outcome, never cleared by a selection change (QA-3, Stage 4 — pins the "survives a new arm" deliberate non-change)', () => {
  it('QA-3: setResults is called exactly twice, both inside submit()\'s own try/catch — never from disarm/toggleAll/Clear/a row toggle, which is what lets an operator re-pick rows off a stale receipt\'s skip labels without losing them', () => {
    const srcPath = fileURLToPath(new URL('../components/ReviewInvoicesTab.tsx', import.meta.url))
    const source = readFileSync(srcPath, 'utf8')

    const callSites = source.match(/setResults\(/g) ?? []
    expect(callSites, 'exactly two setResults( call sites, one per submit() outcome leg').toHaveLength(2)

    const submitBody = /async function submit\(\)[\s\S]*?\n  \}\n/.exec(source)?.[0] ?? ''
    const setResultsInSubmit = submitBody.match(/setResults\(/g) ?? []
    expect(setResultsInSubmit, 'both call sites must be inside submit() itself').toHaveLength(2)
  })
})

describe('ReviewInvoicesTab.tsx source: the results panel renders no count derived from the selection (QA-4, Stage 4 — pins the "no headline count" structural absence)', () => {
  it("QA-4: the results-panel JSX block (between its own testid and the error panel's) never references `selected` — a `{selected.length} submitted` headline is the other route to reporting a server-skipped invoice as sent, and this block has no oracle for it once it exists because bulkOutcome's own signature already forbids reading `selected`", () => {
    const srcPath = fileURLToPath(new URL('../components/ReviewInvoicesTab.tsx', import.meta.url))
    const source = readFileSync(srcPath, 'utf8')

    const start = source.indexOf('data-testid="review-submit-results"')
    const end = source.indexOf('data-testid="review-submit-error"')
    expect(start, 'the results panel testid must exist').toBeGreaterThan(0)
    expect(end, 'the error panel testid must exist').toBeGreaterThan(start)

    const panelBlock = source.slice(start, end)
    expect(panelBlock).not.toMatch(/\bselected\b/)
    expect(panelBlock).not.toMatch(/\bsubmitted\b/i)
  })
})

describe('ReviewInvoicesTab.tsx source: a synchronous double-click guard exists around the submit request (QA-5, Stage 4 — existence only; the race itself has no unit oracle under environment:\'node\', see task-287 §13)', () => {
  it("QA-5: submitInFlight is declared, checked, set and reset around the await — losing any of the four would reopen the window `disabled` alone cannot close (React batches state updates, so a fast double-click re-enters the handler before `disabled` re-renders). This does NOT prove the race is closed; it only prevents the ref itself from being silently deleted", () => {
    const srcPath = fileURLToPath(new URL('../components/ReviewInvoicesTab.tsx', import.meta.url))
    const source = readFileSync(srcPath, 'utf8')

    expect(source).toMatch(/const submitInFlight = useRef\(false\)/)
    expect(source).toMatch(/if \(submitInFlight\.current\) return/)
    expect(source).toMatch(/submitInFlight\.current = true/)
    expect(source).toMatch(/submitInFlight\.current = false/)
  })
})

// --- INVCR-01-14 (task-290, Stage 2.5/Mode A) — RED specs for the row-expansion fix
// editor's pure model: fixCard, rowExpansionView, fixEditPatch, EDIT_FIELD_LABELS. All
// three functions throw `new Error('not implemented')` today (reviewBatch.ts's own STUB
// header), so every spec below fails on that throw — the correct RED reason — except
// ROW-7b, which fails on ENOENT for a DIFFERENT, legitimate reason (TAB-7b's own
// precedent above: ReviewRow.tsx does not exist until Stage 3 creates it).
//
// D8/D9 recap, this subtask's whole reason for existing: field targeting is
// mbsPathToEditField(violation.path) ALONE — no rule-key map is authored here or
// anywhere in this file. `line_items`/`invoice_number`/any APP-only path resolves to
// `null`, and the card's own `message` still carries the reason in full — `null` means
// "no field to flag", never "drop the reason".
function mkViolation(overrides: Partial<Violation> = {}): Violation {
  return { rule_key: 'r', severity: 'error', message: 'm', ...overrides }
}

describe('fixCard: field targeting is mbsPathToEditField alone — no rule-key map (AC-2, FIX-1..4)', () => {
  it("FIX-1: vat-standard-rate (path 'vat', expected '84375') resolves to the vat field and carries the expectation", () => {
    const v = mkViolation({
      rule_key: 'vat-standard-rate',
      path: 'vat',
      expected: '84375',
      message: 'VAT must equal 7.5% of the taxable base.',
    })

    const card = fixCard(v)

    expect(card.field).toBe('vat')
    expect(card.hint).toBe('Expected 84375')
    expect(card.message).toBe('VAT must equal 7.5% of the taxable base.')
    expect(card.blocking).toBe(true)
  })

  it("FIX-2: line-items-sum-subtotal (path 'subtotal', expected '1120000') resolves to the subtotal field", () => {
    const v = mkViolation({ rule_key: 'line-items-sum-subtotal', path: 'subtotal', expected: '1120000' })

    const card = fixCard(v)

    expect(card.field).toBe('subtotal')
    expect(card.hint).toBe('Expected 1120000')
  })

  it('FIX-3: an unmappable path (line_items) renders field:null and the message unabridged — never dropped, never an invented field name', () => {
    const v = mkViolation({
      rule_key: 'line-cost-non-negative',
      path: 'line_items',
      message: 'A line item cannot carry a negative amount.',
    })

    const card = fixCard(v)

    expect(card.field).toBeNull()
    expect(card.message).toBe('A line item cannot carry a negative amount.')
  })

  it('FIX-4: a path-less violation still renders — no crash, field:null, message intact', () => {
    const v = mkViolation({ rule_key: 'x', path: undefined, message: 'Something failed.' })

    expect(() => fixCard(v)).not.toThrow()
    const card = fixCard(v)
    expect(card.field).toBeNull()
    expect(card.message).toBe('Something failed.')
  })

  it('FIX-10: a warning-severity violation is non-blocking — the advisory path (§10.12), synthetic since all 19 shipped rules are error today ([warning-pill-unreachable-today])', () => {
    const v = mkViolation({ severity: 'warning', message: 'Advisory only.' })

    const card = fixCard(v)

    expect(card.severity).toBe('warning')
    expect(card.blocking).toBe(false)
  })

  it('FIX-10b: a WARNING-ONLY invoice is not labelled failing at the row-expansion level either (AC-8, §10.12) — rowExpansionView carries a distinct advisory sectionLabel, never the "Failed rules" one', () => {
    const view = rowExpansionView(
      { violations: [mkViolation({ severity: 'warning' })], rule_set_version: null },
      { can_revalidate: true, revalidate_blocked_reason: null },
    )

    expect(view.passing).toBe(false)
    expect(view.blocking).toBe(false)
    expect(view.sectionLabel).not.toContain('Failed')
    expect(view.cards).toHaveLength(1)
    expect(view.cards[0].blocking).toBe(false)
  })

  it('FIX-10c: an invoice mixing a blocking error with an advisory warning is treated as blocking overall — sectionLabel is the "Failed rules" one', () => {
    const view = rowExpansionView(
      { violations: [mkViolation({ severity: 'warning' }), mkViolation({ severity: 'error' })], rule_set_version: null },
      { can_revalidate: true, revalidate_blocked_reason: null },
    )

    expect(view.blocking).toBe(true)
    expect(view.sectionLabel).toBe('Failed rules · fix here, then re-validate this invoice only')
  })

  it('a violation carrying neither expected nor actual has hint:null, never an empty string', () => {
    const v = mkViolation({ path: 'currency' })

    expect(fixCard(v).hint).toBeNull()
  })

  it('a violation carrying BOTH expected and actual composes them — decimal strings passed through raw, never re-parsed/re-formatted ([D13])', () => {
    const v = mkViolation({ expected: '84375.00', actual: '80000.00' })

    expect(fixCard(v).hint).toBe('Expected 84375.00 · got 80000.00')
  })
})

describe('EDIT_FIELD_LABELS: every one of the 9 EDIT_FIELD_KEYS has a human label (AC-2)', () => {
  it('all nine keys are present, matching InvoiceDetail.tsx\'s own edit-form labels verbatim', () => {
    const expected: Record<EditFieldKey, string> = {
      issue_date: 'Issue date',
      supplier_tin: 'Supplier TIN',
      supplier_name: 'Supplier name',
      buyer_tin: 'Buyer TIN',
      buyer_name: 'Buyer name',
      currency: 'Currency',
      subtotal: 'Subtotal',
      vat: 'VAT',
      total: 'Total',
    }

    expect(EDIT_FIELD_LABELS).toEqual(expected)
  })
})

describe('rowExpansionView: a clean invoice (zero violations) is the passing line, never a card list (AC-6, FIX-9)', () => {
  it('FIX-9: rule_set_version 3 -> passing, summary is "Every rule in NG-MBS v3 passed.", zero cards', () => {
    const view = rowExpansionView(
      { violations: [], rule_set_version: 3 },
      { can_revalidate: false, revalidate_blocked_reason: null },
    )

    expect(view.passing).toBe(true)
    expect(view.summary).toBe('Every rule in NG-MBS v3 passed.')
    expect(view.cards).toEqual([])
  })

  it('a null rule_set_version (never evaluated) still renders honestly, not v0', () => {
    const view = rowExpansionView(
      { violations: [], rule_set_version: null },
      { can_revalidate: true, revalidate_blocked_reason: null },
    )

    expect(view.summary).toBe('Every rule in not evaluated passed.')
  })
})

// FIX-9's own "the string v8 appears nowhere in the module" -- scoped to the literal
// FULL fake label ("NG-MBS v8"), not the bare substring "v8": reviewBatch.ts's own
// INVCR-01-08-era comment (RULE_SET_NAME's own doc, above) legitimately says `v8` in
// prose (explaining why lib/rules.ts's GOLDEN_SET is NOT imported here) and a bare-"v8"
// scan would red on that pre-existing, correct line for the wrong reason. The full label
// is what a hardcoded fallback would actually emit, and it is genuinely absent today.
describe('reviewBatch.ts source (whole file): the fake "NG-MBS v8" label is never hardcoded (FIX-9b guard)', () => {
  it('FIX-9b: the literal string "NG-MBS v8" appears nowhere in reviewBatch.ts -- the passing line must derive the version from the server, never repeat D1\'s deleted fake', () => {
    const srcPath = fileURLToPath(new URL('./reviewBatch.ts', import.meta.url))
    const source = readFileSync(srcPath, 'utf8')

    expect(source).not.toMatch(/NG-MBS v8/i)
  })
})

describe('rowExpansionView: a failing invoice carries one card per violation, in server order (AC-1)', () => {
  it('two violations -> not passing, two cards, in the order the server sent them', () => {
    const violations: Violation[] = [
      mkViolation({ rule_key: 'buyer-tin-format', path: 'buyer.tin' }),
      mkViolation({ rule_key: 'vat-standard-rate', path: 'vat' }),
    ]

    const view = rowExpansionView(
      { violations, rule_set_version: null },
      { can_revalidate: true, revalidate_blocked_reason: null },
    )

    expect(view.passing).toBe(false)
    expect(view.summary).toBeNull()
    expect(view.cards.map((c) => c.ruleKey)).toEqual(['buyer-tin-format', 'vat-standard-rate'])
    expect(view.cards.map((c) => c.field)).toEqual(['buyer_tin', 'vat'])
  })
})

describe('rowExpansionView: the re-validate gate is read off the wire, byte-identical, never mirrored (AC-4, FIX-6)', () => {
  it('FIX-6: can_revalidate:false with a reason -> disabled:true, reason rendered byte-identically', () => {
    const reason = 'invoice is validated, the gate is draft-only'
    const view = rowExpansionView(
      { violations: [], rule_set_version: 3 },
      { can_revalidate: false, revalidate_blocked_reason: reason },
    )

    expect(view.revalidateDisabled).toBe(true)
    expect(view.revalidateReason).toBe(reason)
  })

  it("can_revalidate:true -> disabled:false and no reason, matching the wire's own null exactly (never an authored fallback)", () => {
    const view = rowExpansionView(
      { violations: [mkViolation()], rule_set_version: null },
      { can_revalidate: true, revalidate_blocked_reason: null },
    )

    expect(view.revalidateDisabled).toBe(false)
    expect(view.revalidateReason).toBeNull()
  })
})

describe('rowExpansionView: §7.3\'s provenance/scope note always renders while expanded (AC-7)', () => {
  it('the note is the exact §7.3 copy, on both the passing and failing branches', () => {
    const passing = rowExpansionView(
      { violations: [], rule_set_version: 3 },
      { can_revalidate: false, revalidate_blocked_reason: null },
    )
    const failing = rowExpansionView(
      { violations: [mkViolation()], rule_set_version: null },
      { can_revalidate: true, revalidate_blocked_reason: null },
    )

    expect(passing.note).toBe('Re-validating touches this invoice only — the rest of the import stays as it is.')
    expect(failing.note).toBe(passing.note)
  })
})

// KEEP-3 (INVCR-01-15, D6, task-291 -- beyond the architect's named KEEP-1/KEEP-2
// pair, added because "the reason text is the point" (this story's own founder
// ruling) requires it to actually be RENDERED, verbatim, somewhere -- ReviewRow.tsx
// reads it off `keptReason` alone, so this is the one spec proving that field is
// correct rather than merely present).
describe('rowExpansionView: the kept-as-is reason surfaces verbatim, never a fabricated fallback (KEEP-3)', () => {
  it('KEEP-3a: kept_as_is_at set carries kept_as_is_reason through to keptReason byte-identically', () => {
    const reason = 'Buyer confirmed the discrepancy is intentional; will not recur.'
    const view = rowExpansionView(
      { violations: [mkViolation({ severity: 'error' })], rule_set_version: null, kept_as_is_at: '2026-07-30T00:00:00Z', kept_as_is_reason: reason },
      { can_revalidate: false, revalidate_blocked_reason: null },
    )

    expect(view.keptReason).toBe(reason)
  })

  it('KEEP-3b: an un-kept row (kept_as_is_at absent/null) always carries keptReason: null, even if a reason were somehow present', () => {
    const view = rowExpansionView(
      { violations: [mkViolation({ severity: 'error' })], rule_set_version: null, kept_as_is_reason: 'should never render' },
      { can_revalidate: true, revalidate_blocked_reason: null },
    )

    expect(view.keptReason).toBeNull()
  })

  it('KEEP-3c: kept_as_is_at set but no reason on the wire yields null, never a client-authored fallback string', () => {
    const view = rowExpansionView(
      { violations: [mkViolation({ severity: 'error' })], rule_set_version: null, kept_as_is_at: '2026-07-30T00:00:00Z', kept_as_is_reason: null },
      { can_revalidate: false, revalidate_blocked_reason: null },
    )

    expect(view.keptReason).toBeNull()
  })
})

// KEEP-2 (INVCR-01-15, D6, task-291): the WHOLE client-side "a reason is required"
// gate. ReviewRow.tsx consults this alone before ever calling keepInvoiceAsIs, so this
// is what proves "no arm => no request" for the keep action, mirroring
// bulkPhaseReducer's own identity-return idiom (INVCR-01-11) for the bulk bar.
describe('canKeepAsIs: a reason is required (KEEP-2)', () => {
  it('KEEP-2: an empty or whitespace-only reason is rejected; any real text is accepted', () => {
    expect(canKeepAsIs('')).toBe(false)
    expect(canKeepAsIs('   ')).toBe(false)
    expect(canKeepAsIs('\t\n')).toBe(false)
    expect(canKeepAsIs('Approved by finance, one-off exception.')).toBe(true)
    // Leading/trailing whitespace around real content is still a real reason -- the
    // gate trims to CHECK, it does not require the caller to have trimmed already.
    expect(canKeepAsIs('  a real reason  ')).toBe(true)
  })
})

describe("fixEditPatch: only genuinely-changed fields reach the wire — mirrors InvoiceDetail.tsx's diffEditInput (AC-5)", () => {
  const original: Pick<InvoiceRecord, EditFieldKey> = {
    issue_date: '2026-07-01T00:00:00Z',
    supplier_tin: '00000000001',
    supplier_name: 'Acme Ltd',
    buyer_tin: '00000000002',
    buyer_name: 'Beta Ltd',
    currency: 'NGN',
    subtotal: '1000.00',
    vat: '75.00',
    total: '1075.00',
  }

  it('FIXPATCH-1: a draft equal to the original produces an empty patch', () => {
    expect(fixEditPatch(original, { vat: '75.00' })).toEqual({})
  })

  it('FIXPATCH-2: a genuinely changed field is the only key in the patch', () => {
    expect(fixEditPatch(original, { vat: '84375.00' })).toEqual({ vat: '84375.00' })
  })

  it('FIXPATCH-3: only the touched fields are considered — two touched fields both land, and touching one never drags in another', () => {
    expect(fixEditPatch(original, { vat: '84375.00', subtotal: '1120000.00' })).toEqual({
      vat: '84375.00',
      subtotal: '1120000.00',
    })
    expect(Object.keys(fixEditPatch(original, { subtotal: '1120000.00' }))).toEqual(['subtotal'])
  })

  it("FIXPATCH-4: a bare YYYY-MM-DD issue_date normalizes to midnight UTC RFC3339, mirroring InvoiceDetail.tsx's own normalization", () => {
    expect(fixEditPatch(original, { issue_date: '2026-08-15' })).toEqual({ issue_date: '2026-08-15T00:00:00Z' })
  })

  it('FIXPATCH-5: a cleared issue_date is OMITTED, never sent as "" — the PATCH cannot represent an explicit clear (JSON "null" and an absent key both mean "leave unchanged")', () => {
    expect(fixEditPatch(original, { issue_date: '' })).toEqual({})
    expect(fixEditPatch(original, { issue_date: '   ' })).toEqual({})
  })

  it('FIXPATCH-6: a null original value compares against "" — a field stored NULL and left untouched (the draft seeds "") never appears', () => {
    const withNulls: Pick<InvoiceRecord, EditFieldKey> = { ...original, buyer_tin: null }
    expect(fixEditPatch(withNulls, { buyer_tin: '' })).toEqual({})
  })
})

// ROW-7b (AC-7, GUARD — red-before ONLY because ReviewRow.tsx does not exist yet; this
// subtask's own Stage 1 states this is NOT coverage of any behaviour, just a fact of
// Stage 2.5's timing relative to Stage 3, matching TAB-7b's own precedent above.
// ReviewRow.tsx is NEW in this subtask and is scanned by NEITHER TAB-7b (scoped to
// ReviewInvoicesTab.tsx only) NOR LIB-SCAN-1 (scoped to reviewBatch.ts only) — this
// closes that gap the moment the file exists, the same way TAB-7b closed it for
// ReviewInvoicesTab.tsx in task-286.
describe('ReviewRow.tsx source: none of the three D2-forbidden lifecycle names appear (AC-7, ROW-7b)', () => {
  it('ROW-7b: the component file contains no Pending/Approved/Transmitted, in any case, including in comments', () => {
    const srcPath = fileURLToPath(new URL('../components/ReviewRow.tsx', import.meta.url))
    const source = readFileSync(srcPath, 'utf8')

    for (const forbidden of [/\bpending\b/i, /\bapproved\b/i, /\btransmitted\b/i]) {
      expect(source).not.toMatch(forbidden)
    }
  })
})

// BATCH-7b (INVCR-01-16, task-292, AC-11) -- the scanner-coverage gap the story's own
// Implementation Notes tracked from task-287 QA Stage 4 onward: ReviewBatch.tsx sits
// under NEITHER of the two file-scoped scanners above (TAB-7b watches only
// ReviewInvoicesTab.tsx, LIB-SCAN-1 only reviewBatch.ts) nor ROW-7b (ReviewRow.tsx only)
// -- so a D2-forbidden name introduced into ReviewBatch.tsx ships silently, the exact
// failure mode that bounced INVCR-01-10 (subtask 10), in the one review-surface file no
// scanner watched. Mirrors ROW-7b's own by-path, whole-file idiom byte-for-byte (reads
// the component BY PATH, never this test file, to avoid self-matching -- PILL-3b's
// shipped idiom) -- this describe block's own comments legitimately name all three
// forbidden words.
//
// A pre-existing forbidden word already sat in this exact file's doc comments (from
// INVCR-01-10, commit 4fa8874: "...could render while the other three were still
// pending.") -- reworded to "unresolved" (this subtask, INVCR-01-16) specifically so
// this new scanner does not red on day-one inherited debt, per the task's own
// Implementation Notes warning.
describe('ReviewBatch.tsx source: none of the three D2-forbidden lifecycle names appear (AC-11, BATCH-7b)', () => {
  it('BATCH-7b: the component file contains no Pending/Approved/Transmitted, in any case, including in comments', () => {
    const srcPath = fileURLToPath(new URL('../components/ReviewBatch.tsx', import.meta.url))
    const source = readFileSync(srcPath, 'utf8')

    for (const forbidden of [/\bpending\b/i, /\bapproved\b/i, /\btransmitted\b/i]) {
      expect(source).not.toMatch(forbidden)
    }
  })
})

// BUG08-BATCH-8 (task-407, AC-3) — a source scan, mirroring BATCH-7b/AIMP-11's own
// by-path idiom, because no test in the suite owns the caption's noun binding: AIMP-5b
// (reviewBatch.test.ts:743-757) calls channelTilesAll and never imports or renders this
// component, so it proves the two counts are distinguishable but is blind to which one
// the JSX actually interpolates. A swapped noun (caption reads the row count, tile value
// reads the invoice count) leaves AIMP-5b green. Also pins the zero-state boolean
// (`alreadyImportedAtZero`, not the sibling tile's `atZero`, which is the wrong
// channel's boolean) and that the shipped unreadable-tile captions survive verbatim
// ([structural-untouched]) while the executor edits the lines directly above them.
describe('ReviewBatch.tsx source: the already-imported tile caption carries the INVOICE count, the tile value carries the ROW count (AC-3, BUG08-BATCH-8)', () => {
  it('BUG08-BATCH-8: the already-imported tile caption carries the INVOICE count, the tile value carries the ROW count', () => {
    const srcPath = fileURLToPath(new URL('../components/ReviewBatch.tsx', import.meta.url))
    const source = readFileSync(srcPath, 'utf8')

    // Sanity: the path resolved and read a real file, not an empty/missing one.
    expect(source.length).toBeGreaterThan(1000)

    // Noun binding: tile value is ROW-denominated, caption is INVOICE-denominated. The
    // literal `}` immediately after the field name means these two patterns cannot
    // cross-match each other's field (`alreadyImported` vs `alreadyImportedInvoices`).
    expect(source).toMatch(/\$\{tiles\.frozen\.alreadyImported\} already imported/)
    expect(source).toMatch(/\$\{tiles\.frozen\.alreadyImportedInvoices\} invoices already in your ledger\. Nothing to fix\./)

    // At-zero caption text and the boolean it must switch on — NOT the sibling
    // unreadable tile's `atZero` (that would mean copy-pasting the wrong ternary).
    expect(source).toContain('Nothing in this file was already in your ledger.')
    expect(source).toContain('alreadyImportedAtZero')

    // [structural-untouched]: the shipped unreadable-tile captions, byte-unchanged.
    expect(source).toContain('No invoice exists for them.')
    expect(source).toContain('A structural failure, not a compliance one: no rule was ever run. Nothing was stored.')
  })
})

// BUG08-BATCH-9 (task-408, folds task-409, AC-3) -- RED-first source scan pinning the
// unreadable tile's at-zero caption fix. Same by-path idiom as BUG08-BATCH-8: reads
// ReviewBatch.tsx by path, never this test file, so it cannot self-match. The old
// caption ('Every row in the file became part of an invoice.') overclaims for the
// all-duplicate shape BUG08-QA-7 pins below (0 unreadable, N already-imported, 0 new
// invoices) -- atZero means only "unreadable === 0", never "nothing was a duplicate".
// Genuinely RED today: the forbidden substring still sits at ReviewBatch.tsx:328.
describe("ReviewBatch.tsx source: the unreadable tile's at-zero caption claims only readability, never invoice creation (AC-3, BUG08-BATCH-9)", () => {
  it('BUG08-BATCH-9: the at-zero caption is replaced and the non-zero caption survives untouched', () => {
    const srcPath = fileURLToPath(new URL('../components/ReviewBatch.tsx', import.meta.url))
    const source = readFileSync(srcPath, 'utf8')

    // Sanity: the path resolved and read a real file, not an empty/missing one.
    expect(source.length).toBeGreaterThan(1000)

    // The old caption is GONE -- it claimed every row became an invoice, false whenever
    // alreadyImported > 0 at this at-zero shape.
    expect(source).not.toContain('became part of an invoice')
    // The new caption states only readability -- true in both at-zero sub-cases.
    expect(source).toContain('Every row in the file could be read.')
    // [structural-untouched]: the non-zero arm is byte-unchanged.
    expect(source).toContain('No invoice exists for them.')
  })
})

// --- QA adversarial coverage (task-407 verification), new tests only ---

// BUG08-QA-7 (task-407 QA): pins the Objective's 750-row all-duplicate repro -- atZero
// (unreadable===0) is TRUE while alreadyImported is 750; BUG08-BATCH-9 (task-408) is
// what fixed the at-zero caption this shape exposed.
describe('channelTilesAll: atZero ignores alreadyImported even at the Objective\'s own all-duplicate shape (BUG08-QA-7)', () => {
  it('BUG08-QA-7: the 750-row all-duplicate repro reports atZero true while alreadyImported is 750', () => {
    const errors: RowError[] = Array.from({ length: 250 }, (_, i) => ({
      rows: [i * 3 + 1, i * 3 + 2, i * 3 + 3],
      rule_key: 'no-duplicate-invoice-number',
      severity: 'error',
      invoice_id: `inv-${i + 1}`,
      message: 'An invoice with this number already exists for this entity.',
    }))

    const tiles = channelTilesAll([{ errors, rule_set_version: 5 }], { cleanTotal: 0, failingTotal: 0 })

    expect(tiles.atZero).toBe(true)
    expect(tiles.frozen.alreadyImported).toBe(750)
    expect(tiles.frozen.alreadyImportedAtZero).toBe(false)
  })
})

// --- BULK-01-06 (task-311, Stage 2.5/Mode A) — RED specs for the run-widened review
// derivations. Every function marked STUB in reviewBatch.ts throws `new Error('not
// implemented')`, so specs against them fail on that throw — the correct RED reason.
// parseReviewHash/formatReviewHash/reviewHash are widened IN PLACE (their only consumer
// is App.tsx's boot/mirror, updated in the same commit; HASH-1/HASH-3 above were
// updated to the new array signature for exactly this reason). reviewQuery/
// reviewPageQuery/channelTiles/reviewHeader/reviewShellState/unreadableRows/
// unreadableCsv/UNREADABLE_CSV_HEADER stay UNTOUCHED and exported — five component call
// sites (ReviewBatch.tsx x4 counting channelTiles/reviewHeader, ReviewInvoicesTab.tsx
// x1) plus ReviewUnreadableTab.tsx's unreadableCsv call would not compile if widened in
// place; BULK-01-07 owns switching those over before the singular exports can be
// deleted (task-307's own recorded obligation).
//
// BULK-06-9 (the wire repeats the param) is DELIBERATELY NOT authored here: BULK-01-02
// already fully widened ListInvoicesOptions.importBatchIds/listInvoices/
// violationSummary, and invoices.test.ts:439-450 ("LIST-6 (BULK-02-14, AC-1)") already
// pins the repeated `import_batch_id` `append` (never `set`) behaviour, green today.
// invoices.ts/invoices.test.ts are not touched by this subtask.
//
// BULK-06-12b (NEW, not in the frozen 23-row table): AC-12 ("reviewPageQuery takes an
// id array and still routes through reviewQuery — no second composer") names a real
// acceptance criterion with no numbered spec of its own; without it, reviewPageQueryAll
// would be the one widened export in this subtask with zero RED coverage. Named "12b"
// to sit next to the header spec it neighbours in the AC list, not because it derives
// from BULK-06-12's own assertion.

// 00000000-0000-4000-8000-00000000000<n> — a valid REVIEW_UUID shape, parameterised so
// hash specs needing several distinct ids never hand-roll one and risk a regex-invalid
// typo (a stray non-hex char would make the "poisons the whole hash" specs pass for the
// wrong reason: rejected because malformed, not because of the multi-id policy).
function mkUuid(n: number): string {
  return `00000000-0000-4000-8000-00000000000${n}`
}

// Local fixture — mirrors mkRow's convention above (own copy, lib/invoices.test.ts's
// draftInvoice is not reused). Defaults to a clean, fully-recorded batch; every BULK-06
// spec overrides only the fields it cares about.
function mkBatch(id: string, filename: string | null, overrides: Partial<ImportBatch> = {}): ImportBatch {
  return {
    id,
    entity_id: 'e1',
    filename,
    status: 'completed',
    rows_total: 10,
    rows_valid: 10,
    rows_invalid: 0,
    errors: [],
    rule_set_version: 5,
    created_at: '2026-07-30T00:00:00Z',
    ...overrides,
  }
}

// QA-311-4's fixture only: a minimal 'imported' outcome report, fields beyond `id` are
// unread by the scenario under test (the batch never lands in `batches`).
function mkReport(id: string, overrides: Partial<ImportReport> = {}): ImportReport {
  return {
    id,
    status: 'completed',
    format: 'csv',
    delimiter: ',',
    encoding: 'utf-8',
    rows_total: 1,
    rows_valid: 1,
    rows_invalid: 0,
    ready_invoices: 1,
    quarantined_invoices: 0,
    errors: [],
    rule_set_version: 5,
    invoices_clean: 1,
    invoices_with_violations: 0,
    invoice_violations: [],
    ...overrides,
  }
}

describe('parseReviewHash: widened to a run (BULK-01-06, AC-1)', () => {
  it('BULK-06-1: a shipped single-uuid deep link still parses — parseReviewHash(\'#review/<uuid>\') is a one-element array', () => {
    const id = mkUuid(1)

    expect(parseReviewHash(`#review/${id}`)).toEqual([id])
  })

  it('BULK-06-2: several ids parse IN ORDER — #review/a,b,c -> [a,b,c]', () => {
    const a = mkUuid(1)
    const b = mkUuid(2)
    const c = mkUuid(3)

    expect(parseReviewHash(`#review/${a},${b},${c}`)).toEqual([a, b, c])
  })

  it('BULK-06-3: one bad segment poisons the WHOLE hash — #review/<uuid>,notauuid is null, never a partial [uuid]', () => {
    const id = mkUuid(1)

    expect(parseReviewHash(`#review/${id},notauuid`)).toBeNull()
  })

  it('BULK-06-4: traversal and suffixes stay refused', () => {
    const id = mkUuid(1)

    expect(parseReviewHash('#review/../../etc')).toBeNull()
    expect(parseReviewHash(`#review/${id}/extra`)).toBeNull()
    expect(parseReviewHash('#review/')).toBeNull()
  })

  it('BULK-06-5: the hash is bounded at REVIEW_HASH_MAX_IDS — six ids is null, never a truncated five', () => {
    const ids = Array.from({ length: 6 }, (_, i) => mkUuid(i))

    expect(parseReviewHash(`#review/${ids.join(',')}`)).toBeNull()
  })

  it("BULK-06-6: parseReviewHash(formatReviewHash([a,b])) round-trips; a single-id format stays byte-identical to today's #review/x", () => {
    const a = mkUuid(1)
    const b = mkUuid(2)

    expect(formatReviewHash([a])).toBe(`#review/${a}`)
    expect(parseReviewHash(formatReviewHash([a, b]))).toEqual([a, b])
  })
})

describe('REVIEW_HASH_MAX_IDS: mirrors importRun.ts\'s MAX_RUN_FILES (drift guard, BULK-01-06 — NOT a runtime clamp, same idiom as BATCH_SUBMIT_MAX_IDS)', () => {
  it('BULK-06-DRIFT: REVIEW_HASH_MAX_IDS equals MAX_RUN_FILES — a documented mirror, not an import, avoids the reviewBatch.ts <-> importRun.ts cycle importRun.ts:74 would otherwise create', () => {
    expect(REVIEW_HASH_MAX_IDS).toBe(MAX_RUN_FILES)
  })
})

describe('reviewQueryAll: the tenant-wide-leak argument survives widened to an array (BULK-01-06, AC-4)', () => {
  // QA Mode B (task-311): re-verified against the real implementation, not the Mode A
  // stub. A `.toThrow()`-only assertion cannot discriminate "throws because the real
  // empty/blank-id validation caught it" from "throws because a stub always throws
  // `not implemented`, unconditionally, for every input" -- so this spec now also
  // asserts the positive case (a real `['batch-1','batch-2']` must NOT throw, and must
  // produce the exact composed query) in the SAME `it`, so it can never again pass
  // against an unconditionally-throwing implementation.
  it("BULK-06-7: an empty query cannot leak the tenant — reviewQueryAll([]), (['']) and ([id,'']) all throw; a valid array does not", () => {
    expect(() => reviewQueryAll([], 'all')).toThrow()
    expect(() => reviewQueryAll([''], 'all')).toThrow()
    expect(() => reviewQueryAll(['batch-1', ''], 'all')).toThrow()

    expect(() => reviewQueryAll(['batch-1', 'batch-2'], 'all')).not.toThrow()
    expect(reviewQueryAll(['batch-1', 'batch-2'], 'all')).toEqual({ importBatchIds: ['batch-1', 'batch-2'] })
  })

  it('BULK-06-8: reviewQueryAll carries EVERY id, and the pill filter still applies', () => {
    const result = reviewQueryAll(['a', 'b'], 'ready')

    expect(result).toEqual({ importBatchIds: ['a', 'b'], status: 'validated' })
  })

  it('BULK-06-12b (AC-12, not in the frozen 23-row table): reviewPageQueryAll composes through reviewQueryAll, never a second composer', () => {
    const state: ReviewFilterState = { pill: 'needs-fix', ruleKey: 'vat-standard-rate', q: 'ACME', offset: 100 }

    const query = reviewPageQueryAll(['a', 'b'], state)

    expect(query).toEqual({
      importBatchIds: ['a', 'b'],
      needsFix: true,
      ruleKey: 'vat-standard-rate',
      q: 'ACME',
      limit: REVIEW_PAGE_SIZE,
      offset: 100,
    })
  })
})

describe('reviewShellStateAll (BULK-01-06, AC-4)', () => {
  it("BULK-06-10: reviewShellStateAll is 'batch' iff ANY batch is 'completed'; over one batch it equals the shipped reviewShellState", () => {
    const completed: Pick<ImportBatch, 'status'> = { status: 'completed' }
    const failed: Pick<ImportBatch, 'status'> = { status: 'failed' }

    expect(reviewShellStateAll([completed, failed])).toBe('batch')
    expect(reviewShellStateAll([failed, failed])).toBe('rejected')
    expect(reviewShellStateAll([completed])).toBe(reviewShellState(completed))
  })
})

describe('channelTilesAll: sums the frozen channel and labels the min non-null rule-set version (BULK-01-06, AC-5)', () => {
  it('BULK-06-11: two batches with 2 and 1 unreadable rows sum to 3; versions [null,3] label NG-MBS v3; [null,null] labels "not evaluated", never v0', () => {
    const twoUnreadable: Pick<ImportBatch, 'errors' | 'rule_set_version'> = {
      errors: [
        { row: 1, message: 'm1' },
        { row: 2, message: 'm2' },
      ],
      rule_set_version: null,
    }
    const oneUnreadable: Pick<ImportBatch, 'errors' | 'rule_set_version'> = {
      errors: [{ row: 3, message: 'm3' }],
      rule_set_version: 3,
    }

    const result = channelTilesAll([twoUnreadable, oneUnreadable], { cleanTotal: 0, failingTotal: 0 })
    expect(result.frozen.unreadable).toBe(3)
    expect(result.frozen.ruleSetLabel).toBe('NG-MBS v3')

    const bothNull: Pick<ImportBatch, 'errors' | 'rule_set_version'> = { errors: [], rule_set_version: null }
    expect(channelTilesAll([bothNull, bothNull], { cleanTotal: 0, failingTotal: 0 }).frozen.ruleSetLabel).toBe(
      'not evaluated',
    )
  })
})

describe('reviewHeaderAll: states the files, sums the rows, never prints a null (BULK-01-06, AC-6)', () => {
  it('BULK-06-12: one batch -> filesLine "from a.csv"; three -> "from 3 files"; rows read is the sum', () => {
    const one = reviewHeaderAll([mkBatch('b1', 'a.csv', { rows_total: 1500 })], { allTotal: 500 })
    expect(one.filesLine).toBe('from a.csv')
    expect(one.subline).toContain('1500 ROWS READ')

    const three = reviewHeaderAll(
      [
        mkBatch('b1', 'a.csv', { rows_total: 100 }),
        mkBatch('b2', 'b.csv', { rows_total: 200 }),
        mkBatch('b3', 'c.csv', { rows_total: 300 }),
      ],
      { allTotal: 500 },
    )
    expect(three.filesLine).toBe('from 3 files')
    expect(three.subline).toContain('600 ROWS READ')
  })

  it('BULK-06-22: a single batch with filename:null renders filesLine "source not recorded" — never "from null", never a bare "from "', () => {
    const header = reviewHeaderAll([mkBatch('b1', null, { rows_total: 10 })], { allTotal: 5 })

    expect(header.filesLine).toBe('source not recorded')
    expect(header.filesLine).not.toContain('null')
    expect(header.filesLine).not.toBe('from ')
  })
})

describe('unreadableRowsAll: attributes each entry to its file (BULK-01-06, AC-7)', () => {
  it('BULK-06-13: a rows:[5,6] error in b1 and a row:2 in b2 yield THREE entries, each carrying its own filename', () => {
    const b1: Pick<ImportBatch, 'id' | 'filename' | 'errors'> = {
      id: 'b1',
      filename: 'a.csv',
      errors: [{ rows: [5, 6], message: 'quarantined: duplicate invoice_number' }],
    }
    const b2: Pick<ImportBatch, 'id' | 'filename' | 'errors'> = {
      id: 'b2',
      filename: 'b.csv',
      errors: [{ row: 2, message: 'bad row' }],
    }

    const result = unreadableRowsAll([b1, b2])

    expect(result).toHaveLength(3)
    expect(result.filter((r) => r.file === 'a.csv')).toHaveLength(2)
    expect(result.filter((r) => r.file === 'b.csv')).toHaveLength(1)
  })

  it('BULK-06-14: an error with neither row nor rows still yields ONE row:null entry, carrying its filename — the no-swallow rule survives widening', () => {
    const b1: Pick<ImportBatch, 'id' | 'filename' | 'errors'> = {
      id: 'b1',
      filename: 'a.csv',
      errors: [{ message: 'unreadable file structure' }],
    }

    const result = unreadableRowsAll([b1])

    expect(result).toEqual([{ row: null, column: '—', message: 'unreadable file structure', file: 'a.csv' }])
  })

  it('BULK-06-23: an unreadable row belonging to a filename:null batch renders "source not recorded" in the File column and the CSV cell, never the literal null', () => {
    const b1: Pick<ImportBatch, 'id' | 'filename' | 'errors'> = {
      id: 'b1',
      filename: null,
      errors: [{ row: 1, message: 'm1' }],
    }

    const rows = unreadableRowsAll([b1])
    expect(rows[0].file).toBe('source not recorded')

    const csv = unreadableCsvAll(rows)
    expect(csv.split('\n')[1]).toBe('source not recorded,1,—,m1')
    expect(csv).not.toContain('null')
  })
})

describe('unreadableCsvAll (BULK-01-06, AC-7): the CSV gains a File column', () => {
  it('BULK-06-15: header is File,Row,Field,Why it could not be read; a null row renders an empty cell; a comma-bearing filename is RFC-4180 quoted', () => {
    const rows: UnreadableRowAll[] = [
      { row: 5, column: '—', message: 'bad row', file: 'a,b.csv' },
      { row: null, column: '—', message: 'unreadable file structure', file: 'source not recorded' },
    ]

    const csv = unreadableCsvAll(rows)
    const lines = csv.split('\n')

    expect(lines[0]).toBe('File,Row,Field,Why it could not be read')
    expect(lines[1]).toBe('"a,b.csv",5,—,bad row')
    expect(lines[2]).toBe('source not recorded,,—,unreadable file structure')
  })
})

describe('filesStrip: the whole per-file report, batch GETs merged with the in-session run (BULK-01-06, AC-8/9/10/11)', () => {
  it('BULK-06-16: filesStrip reports a run-only failure — a file that never produced a batch — carrying its filename and the server message', () => {
    const run: ImportRun = {
      files: [{ id: 'f1', name: 'broken.csv', groupId: 'g1', outcome: { kind: 'failed', message: '422: unreadable file structure' } }],
      cursor: 1,
      status: 'finished',
    }

    const rows = filesStrip([], run)

    expect(rows).toHaveLength(1)
    expect(rows[0].filename).toBe('broken.csv')
    expect(rows[0].reason).toBe('422: unreadable file structure')
  })

  it('BULK-06-17: filesStrip(batches, null) survives a deep link — one row per batch, and no run-only failure rows', () => {
    const batches: ImportBatch[] = [mkBatch('b1', 'a.csv'), mkBatch('b2', 'b.csv')]

    const rows = filesStrip(batches, null)

    expect(rows).toHaveLength(2)
    expect(rows.map((r) => r.id).sort()).toEqual(['b1', 'b2'])
  })

  it('BULK-06-18: a batch with filename:null renders "source not recorded" in filesStrip — never \'\' and never the literal null', () => {
    const rows = filesStrip([mkBatch('b1', null)], null)

    expect(rows[0].filename).toBe('source not recorded')
  })

  // BULK-06-21 (the QA-debate addition, and the reason this subtask exists in this
  // shape): a rejected-by-verdict file states its reason, read from `batch.errors`,
  // NEVER `batch.status` ([reason-comes-from-errors-not-status]). Verified in Go:
  // service.go:787 finalizes 'failed' only when rowsTotal === 0; a fully-quarantined
  // file with rows falls through to service.go:923 and finalizes 'completed' with
  // rows_valid:0, its reason entering errorsList at service.go:838-849 and reaching the
  // wire at handlers.go:371. Asserted TWICE — a status-keyed implementation fails BOTH
  // halves, not just one: once with a live run whose outcome for this file is
  // kind:'imported' with ready_invoices:0 (the report itself, matching the batch), and
  // once with run:null (a reload), where `errors` is the only surviving source.
  it('BULK-06-21: a rejected-by-verdict file (status:completed, rows_total:1, rows_valid:0, a duplicate RowError in errors) states its reason from errors, not status — both with a live run and with run:null', () => {
    const message = 'duplicate invoice_number: INV-1'
    const batch = mkBatch('b1', 'dupes.csv', {
      rows_total: 1,
      rows_valid: 0,
      rows_invalid: 1,
      errors: [{ row: 1, rule_key: 'duplicate_invoice_number', severity: 'error', message }],
    })
    const report: ImportReport = {
      id: 'b1',
      status: 'completed',
      format: 'csv',
      delimiter: ',',
      encoding: 'utf-8',
      rows_total: 1,
      rows_valid: 0,
      rows_invalid: 1,
      ready_invoices: 0,
      quarantined_invoices: 1,
      errors: batch.errors,
      rule_set_version: 5,
      invoices_clean: 0,
      invoices_with_violations: 0,
      invoice_violations: [],
    }
    const liveRun: ImportRun = {
      files: [{ id: 'f1', name: 'dupes.csv', groupId: 'g1', outcome: { kind: 'imported', batchId: 'b1', report } }],
      cursor: 1,
      status: 'finished',
    }

    const liveRows = filesStrip([batch], liveRun)
    expect(liveRows.find((r) => r.id === 'b1')?.reason).toBe(message)

    const deepLinkRows = filesStrip([batch], null)
    expect(deepLinkRows.find((r) => r.id === 'b1')?.reason).toBe(message)
  })
})

describe('sourceFileLabel / showsSourceFile: per-row source attribution (BULK-01-06, AC-11)', () => {
  it("BULK-06-19: sourceFileLabel resolves a row's import_batch_id to its batch's filename; an unknown id and a null filename both return null", () => {
    const batches: Pick<ImportBatch, 'id' | 'filename'>[] = [
      { id: 'b1', filename: 'a.csv' },
      { id: 'b2', filename: null },
    ]

    expect(sourceFileLabel('b1', batches)).toBe('a.csv')
    expect(sourceFileLabel('b2', batches)).toBeNull()
    expect(sourceFileLabel('unknown-id', batches)).toBeNull()
  })

  it('BULK-06-20: showsSourceFile is the sole owner of the "only when >1 batch" rule — false at one batch, true at two', () => {
    expect(showsSourceFile([mkBatch('b1', 'a.csv')])).toBe(false)
    expect(showsSourceFile([mkBatch('b1', 'a.csv'), mkBatch('b2', 'b.csv')])).toBe(true)
  })
})

// QA Mode B adversarial coverage (task-311). BULK-06-1..23 above are the architect's own
// Test Specs table (authored RED in Mode A, now re-verified green); everything below is
// QA-authored edge/negative/ordering coverage the table did not ask for.
describe('QA-311-1: filesStrip over the zero-row early-"failed" batch (service.go:787 — no spec constructs this fixture)', () => {
  it('a header-only file (rows_total:0, rows_valid:0, status:"failed", errors:[]) still gets exactly one row, and the current fallback reads "0 of 0 rows produced an invoice"', () => {
    const zeroRowBatch = mkBatch('b1', 'headers-only.csv', {
      status: 'failed',
      rows_total: 0,
      rows_valid: 0,
      rows_invalid: 0,
      errors: [],
    })

    const rows = filesStrip([zeroRowBatch], null)

    expect(rows).toHaveLength(1)
    expect(rows[0].filename).toBe('headers-only.csv')
    // Pinning CURRENT behaviour, not endorsing it: "0 of 0 rows produced an invoice" is
    // technically true but reads as if nothing was wrong (QA finding, not an AC — no
    // spec pins this literal string). If batchReason's fallback branch is ever reworded
    // for this zero-row case, this is the spec to update alongside it.
    expect(rows[0].reason).toBe('0 of 0 rows produced an invoice')
  })
})

describe('QA-311-2: reviewHeaderAll pins its timestamp choice at N>1 batches (no spec constrains this — executor used batches[0]?.created_at, run order)', () => {
  it('the subline date is the FIRST batch\'s created_at, not the last, not the max/min, when batches carry different timestamps', () => {
    const first = mkBatch('b1', 'a.csv', { created_at: '2026-01-01T00:00:00Z' })
    const second = mkBatch('b2', 'b.csv', { created_at: '2026-06-15T12:00:00Z' })

    const header = reviewHeaderAll([first, second], { allTotal: 10 })

    expect(header.subline).toContain(fmtDateTime(first.created_at))
    expect(header.subline).not.toContain(fmtDateTime(second.created_at))

    // Reversing array order reverses which timestamp wins — confirms the rule is
    // genuinely "array position 0", not e.g. "earliest" or "latest" by value.
    const reversed = reviewHeaderAll([second, first], { allTotal: 10 })
    expect(reversed.subline).toContain(fmtDateTime(second.created_at))
    expect(reversed.subline).not.toContain(fmtDateTime(first.created_at))
  })
})

describe('QA-311-3: filesStrip row order — batches array order wins, run-only failures always trail', () => {
  it('rows follow the `batches` array\'s own order (not created_at, not id), with run-only failures appended after in run.files order', () => {
    const bZ = mkBatch('bZ', 'z-file.csv', { created_at: '2026-01-01T00:00:00Z' })
    const bA = mkBatch('bA', 'a-file.csv', { created_at: '2026-06-01T00:00:00Z' })
    const run: ImportRun = {
      files: [
        { id: 'f1', name: 'first-refused.csv', groupId: 'g1', outcome: { kind: 'failed', message: 'refused: bad header' } },
        { id: 'f2', name: 'second-refused.csv', groupId: 'g2', outcome: { kind: 'failed', message: 'refused: no rows' } },
      ],
      cursor: 2,
      status: 'finished',
    }

    // `batches` deliberately passed in z-before-a order (neither alphabetical nor
    // created_at order) to prove filesStrip does not silently re-sort.
    const rows = filesStrip([bZ, bA], run)

    expect(rows.map((r) => r.filename)).toEqual(['z-file.csv', 'a-file.csv', 'first-refused.csv', 'second-refused.csv'])
  })
})

describe('QA-311-4: a batch known to `run` but absent from `batches` (in-flight GET), and the reverse, neither throws', () => {
  it('an "imported" run outcome whose batch has not yet landed in `batches` produces no crash — its row is simply absent until the batch GET resolves', () => {
    const otherBatch = mkBatch('b-other', 'other.csv')
    const run: ImportRun = {
      files: [{ id: 'f1', name: 'in-flight.csv', groupId: 'g1', outcome: { kind: 'imported', batchId: 'b-missing', report: mkReport('b-missing') } }],
      cursor: 1,
      status: 'finished',
    }

    // `b-missing` is referenced by the run's outcome but `batches` (the GET results)
    // does not contain it yet -- filesStrip must not throw, and (documenting current
    // behaviour, not endorsing it) the file is silently absent from the strip rather
    // than rendering a placeholder row, because batchRows is sourced from `batches`
    // alone and runOnlyFailures only reads 'failed' outcomes.
    expect(() => filesStrip([otherBatch], run)).not.toThrow()
    const rows = filesStrip([otherBatch], run)
    expect(rows.map((r) => r.id)).toEqual(['b-other'])
    expect(rows.find((r) => r.filename === 'in-flight.csv')).toBeUndefined()
  })

  it('a batch present in `batches` but never mentioned by `run` at all renders normally — no throw, no drop', () => {
    const staleBatch = mkBatch('b-stale', 'stale.csv')
    const run: ImportRun = {
      files: [{ id: 'f1', name: 'unrelated.csv', groupId: 'g1', outcome: { kind: 'failed', message: 'refused' } }],
      cursor: 1,
      status: 'finished',
    }

    expect(() => filesStrip([staleBatch], run)).not.toThrow()
    const rows = filesStrip([staleBatch], run)
    expect(rows.map((r) => r.filename).sort()).toEqual(['stale.csv', 'unrelated.csv'])
  })
})

describe('QA-311-5: unreadableRowsAll across two files erroring on the SAME row number — both survive, each attributed to its own file', () => {
  it('row 3 in file A and row 3 in file B both appear, not deduped, not merged, each carrying its own file label', () => {
    const fileA: Pick<ImportBatch, 'id' | 'filename' | 'errors'> = {
      id: 'bA',
      filename: 'a.csv',
      errors: [{ row: 3, message: 'bad date in A' }],
    }
    const fileB: Pick<ImportBatch, 'id' | 'filename' | 'errors'> = {
      id: 'bB',
      filename: 'b.csv',
      errors: [{ row: 3, message: 'bad date in B' }],
    }

    const result = unreadableRowsAll([fileA, fileB])

    expect(result).toHaveLength(2)
    expect(result).toContainEqual({ row: 3, column: '—', message: 'bad date in A', file: 'a.csv' })
    expect(result).toContainEqual({ row: 3, column: '—', message: 'bad date in B', file: 'b.csv' })
  })
})
