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

import { invoiceStatusStyle } from './invoices'
import { severityStyle, type Violation } from './validationApi'
import type { ImportBatch, RowError } from './importApi'
import {
  channelTiles,
  filterToQuery,
  formatReviewHash,
  pagerLabels,
  parseReviewHash,
  reviewQuery,
  unreadableRows,
  verdictPill,
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
