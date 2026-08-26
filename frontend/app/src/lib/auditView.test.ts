// RED specs (AUDIT-11-08, Stage 2.5/Mode A) -- pin AUDIT_COPY.searchHelper's replacement
// wording before the executor edits it. .ralph/AUDIT-11-08-arch.md supersedes the story's
// subtask spec: the drafted "cutover" sentence is FALSE (audit_log.invoice_id is a
// generated column read at ADD COLUMN time, so historical rows carry it and the number
// arm reaches them). The real gap is DISPLAY -- an old row's own payload lacks the number.

import { describe, expect, it } from 'vitest'

import { AUDIT_COPY } from './auditView'

// Three failure classes reintroduce the false belief CF-40 forbids: an explicit
// exclusion ("other details only"), a reliability hedge ("older ... less reliably
// matched"), and a plain temporal denial ("events before X cannot be found by
// number"). Scoped so "before the number was recorded" (temporal narration, not
// an exclusion claim) does not trip it. CUTOVER_CORPUS below pins the discrimination.
const CUTOVER_EXCLUSION = /other details.{0,20}only|only.{0,20}other details/i
const RELIABILITY_HEDGE =
  /(older|earlier|past|historical).{0,40}(less reliably|less likely|not always|may not|might not|harder to find|inconsistently|unreliably|not as reliably|worse matched)/i
const TEMPORAL_EXCLUSION =
  /\b(older|earlier|before|prior to|past|historical)\b[^.]{0,80}\b(cannot|can't|could not|are not|is not|aren't|isn't|never|unsearchable|excluded)\b[^.]{0,40}(search|find|found|match|reach)/i

const claimsOlderEventsAreUnsearchable = (s: string): boolean =>
  CUTOVER_EXCLUSION.test(s) || RELIABILITY_HEDGE.test(s) || TEMPORAL_EXCLUSION.test(s)

describe('AUDIT_COPY.searchHelper', () => {
  it('auditCopy_doesNotDenyInvoiceNumberSearch', () => {
    expect(AUDIT_COPY.searchHelper, 'must not deny invoice-number search').not.toMatch(
      /cannot find an invoice number/i,
    )
  })

  it('auditCopy_doesNotClaimOlderEventsAreUnsearchableByNumber', () => {
    expect(
      claimsOlderEventsAreUnsearchable(AUDIT_COPY.searchHelper),
      'must not claim older events are excluded from number search',
    ).toBe(false)
  })

  const CUTOVER_CORPUS: ReadonlyArray<readonly [string, boolean]> = [
    [
      'Invoice numbers are recorded from August 2026 onward; events logged before then are searchable by their other details only.',
      true,
    ],
    ['Older invoice numbers logged in the past are less reliably matched than current ones.', true],
    [
      'Invoice numbers are only recorded from August 2026 onward, so earlier events cannot be found by number.',
      true,
    ],
    ['Events logged before August 2026 are not searchable by invoice number.', true],
    ['Historical events are excluded from invoice-number search.', true],
    ['Earlier events are never matched by an invoice number.', true],
    [
      'Invoice numbers are searchable for every event, including ones logged before the number was recorded in their own payload.',
      false,
    ],
    [
      "An invoice number matches that invoice's events, including older ones that do not list the number in their own details.",
      false,
    ],
    ['Older events are found by invoice number even though they do not carry it.', false],
    ['It cannot find a member shown by their email address.', false],
    ['Events recorded before this release are still found by number, but do not show it in their own details.', false],
  ]

  it.each(CUTOVER_CORPUS)('auditCopy_cutoverGuardDiscriminates: %s', (sentence, isFalseClaim) => {
    expect(claimsOlderEventsAreUnsearchable(sentence)).toBe(isFalseClaim)
  })

  it('auditCopy_statesTheDisplayGapNotASearchCutover', () => {
    // Both properties must hold in the SAME sentence -- otherwise "invoice numbers"
    // in one clause and the gap explanation in an unrelated one satisfy this by
    // coincidence (proved by a mutation that drops the number from the search-term
    // sentence and keeps it only in the unrelated features list).
    const explainsTheGap = /older|earlier|do not list|does not list|own details|not (?:show|shown|listed) in/i
    const sentences = AUDIT_COPY.searchHelper.split(/(?<=[.!?])\s+/).map((s) => s.trim())
    const gapSentence = sentences.find((s) => /invoice number/i.test(s) && explainsTheGap.test(s))
    expect(
      gapSentence,
      'one sentence must state the invoice number is searchable AND explain older rows lack it in their own details',
    ).toBeDefined()
  })

  it('auditCopy_stillDeniesEmailActorSearch', () => {
    // Assert the denial, not the old byte string -- the replacement re-splits the sentence.
    expect(AUDIT_COPY.searchHelper, 'must still mention email').toMatch(/email/i)
    const deniesEmail = /cannot find.{0,60}email|email address.{0,20}(cannot|can't|not)/i
    expect(AUDIT_COPY.searchHelper, 'must still deny actor-by-email search').toMatch(deniesEmail)
  })

  it('auditCopy_doesNotTellTheUserToNarrowByCompany', () => {
    // BQ-3 was answered (a): the search works in every company mode. An instruction to
    // select/choose/pick a company first would be false under that decision (D-25).
    expect(AUDIT_COPY.searchHelper, 'must not instruct the user to narrow by company').not.toMatch(
      /(select|choose|pick).{0,20}(a |the )?company/i,
    )
  })
})
