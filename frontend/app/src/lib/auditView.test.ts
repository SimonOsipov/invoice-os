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
    // Guards the disproved cutover class, not one byte string: "recorded/searchable from
    // <date> onward", "events before/logged before then", "older events ... other details
    // only" all express the same false claim CF-40 forbids.
    const cutoverClaim = /(before|prior to|onward|from).{0,40}(search|find|number)|only.{0,20}other details/i
    expect(AUDIT_COPY.searchHelper, 'must not re-introduce the disproved cutover sentence').not.toMatch(cutoverClaim)
  })

  it('auditCopy_statesTheDisplayGapNotASearchCutover', () => {
    // A bare "it can find an invoice number" leaves the found-but-not-shown asymmetry
    // looking like a bug -- the helper must account for older rows not listing the number.
    expect(AUDIT_COPY.searchHelper, 'must state invoice numbers are searchable').toMatch(/invoice number/i)
    const explainsTheGap = /older|earlier|do not list|does not list|own details|not (?:show|shown|listed) in/i
    expect(AUDIT_COPY.searchHelper, 'must explain older rows lack the number in their own details').toMatch(
      explainsTheGap,
    )
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
