// RED specs (AUDIT-11-08, Stage 2.5/Mode A) -- pin AUDIT_COPY.searchHelper's replacement
// wording before the executor edits it. .ralph/AUDIT-11-08-arch.md supersedes the story's
// subtask spec: the drafted "cutover" sentence is FALSE (audit_log.invoice_id is a
// generated column read at ADD COLUMN time, so historical rows carry it and the number
// arm reaches them). The real gap is DISPLAY -- an old row's own payload lacks the number.

import { describe, expect, it } from 'vitest'

import { AUDIT_COPY } from './auditView'

describe('AUDIT_COPY.searchHelper', () => {
  it('auditCopy_doesNotDenyInvoiceNumberSearch', () => {
    expect(AUDIT_COPY.searchHelper, 'must not deny invoice-number search').not.toMatch(
      /cannot find an invoice number/i,
    )
  })

  it('auditCopy_doesNotClaimOlderEventsAreUnsearchableByNumber', () => {
    // Two failure classes reintroduce the false belief CF-40 forbids: an explicit
    // exclusion ("other details only") and a reliability hedge ("older ... less
    // reliably matched"). Scoped so "before the number was recorded" (temporal
    // narration, not an exclusion claim) does not trip it.
    const cutoverExclusion = /other details.{0,20}only|only.{0,20}other details/i
    const reliabilityHedge =
      /(older|earlier|past|historical).{0,40}(less reliably|less likely|not always|may not|might not|harder to find|inconsistently|unreliably|not as reliably|worse matched)/i
    expect(AUDIT_COPY.searchHelper, 'must not claim older events are excluded from number search').not.toMatch(
      cutoverExclusion,
    )
    expect(AUDIT_COPY.searchHelper, 'must not hedge that older events are less reliably matched').not.toMatch(
      reliabilityHedge,
    )
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
