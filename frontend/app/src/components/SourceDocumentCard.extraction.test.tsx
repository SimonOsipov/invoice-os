// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// EXTR-11-08, Mode A. The entry control on SourceDocumentCard, rendered against the card
// alone -- the lookup that feeds it is InvoiceDetail's and is pinned in
// InvoiceDetail.extraction.test.tsx. SourceDocumentCard.test.tsx keeps the whole-detail
// rows; this file is the props contract, so a hand-built pair is the right subject.
//
// `tsc --noEmit` stays green: the two new props are a tsc error against today's
// `({ meta, onOpen })` signature, so the component is cast through `CardProps` and
// `card_theSignatureCarriesTheExtractionProps` is the fence that the widening lands.

import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import type { ReactElement } from 'react'

import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { SourceDocumentRecord, SourceDocumentResponse } from '../lib/sourceDocument'
import type { SourceDocumentAsync } from './SourceDocumentStates'
import { SourceDocumentCard } from './SourceDocumentCard'

const HERE = dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = join(HERE, '..', '..', '..', '..')

// THE testid. EXTR-11-05's four deployed specs reach the review screen through it and have no
// other handle; a synonym fails at the deploy gate in EXTR-11-09, four subtasks downstream.
// `card_theTestidMatchesTheDeployedLocator` reads the e2e source and proves this literal is
// the one those specs click, rather than trusting that two files were written in step.
const TESTID = 'open-extraction-review'

// Story `## Decisions -> Invented copy`, both rows final. The table is the record
// (commit 5c108824); a reword moves the table and these literals together.
const CONTROL_LABEL = 'Check the extraction'
const NO_JOB_REASON = 'This document has no extraction to check.'
// The fourth row, added in QA: the shape `{ jobId, loading }` could not represent a failed
// lookup, so an error mapped onto `loading` and left the control disabled with nothing said.
const LOOKUP_FAILED_REASON = 'We could not check this document for an extraction.'

const JOB_ID = 'c3d4e5f6-a7b8-4c3d-9e4f-5a6b7c8d9e0f'
const HASH = '3f9a1c02b7d4e6108a5c93f21e0d47b6c8a2f5039e1b7d4c60a8f3e2d5a86560'

type ExtractionEntry = { jobId: string | null; loading: boolean; failed: boolean }

const READY = (jobId: string | null): ExtractionEntry => ({ jobId, loading: false, failed: false })
const IN_FLIGHT: ExtractionEntry = { jobId: null, loading: true, failed: false }
const FAILED: ExtractionEntry = { jobId: null, loading: false, failed: true }

type CardProps = {
  meta: SourceDocumentAsync
  onOpen: () => void
  extraction: ExtractionEntry
  onOpenExtraction: (jobId: string) => void
}

const Card = SourceDocumentCard as unknown as (p: CardProps) => ReactElement

function sourceRecord(over: Partial<SourceDocumentRecord> = {}): SourceDocumentRecord {
  return {
    id: 'doc-1',
    filename: 'june-sales.pdf',
    declared_content_type: 'application/pdf',
    size_bytes: 151_552,
    content_hash: HASH,
    uploaded_at: '2026-06-12T11:42:00Z',
    uploaded_by: 'c0000000-0000-0000-0000-000000000001',
    invoices_created: 1,
    other_invoice_rows: [],
    ...over,
  }
}

function meta(document: SourceDocumentRecord | null): SourceDocumentAsync {
  const data: SourceDocumentResponse = { invoice_id: 'inv-1', source_rows: [1], document }
  return { status: 'ready', data, error: null, run: vi.fn() }
}

// The card plus a sibling that DOES carry a title. The absence assertion then reads "the only
// [title] in this tree is the probe", so an empty query result can never be what passes it.
function renderCard(props: Partial<CardProps> = {}) {
  const onOpenExtraction = props.onOpenExtraction ?? vi.fn()
  const view = render(
    <div>
      <span data-testid="title-probe" title="control needle" />
      <Card
        meta={props.meta ?? meta(sourceRecord())}
        onOpen={props.onOpen ?? vi.fn()}
        extraction={props.extraction ?? READY(JOB_ID)}
        onOpenExtraction={onOpenExtraction}
      />
    </div>,
  )
  return { ...view, onOpenExtraction }
}

const control = () => screen.getByTestId(TESTID) as HTMLButtonElement

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe('AC-5: with a job the control is enabled and reports the job id', () => {
  it('card_withAJobTheControlIsEnabledAndReportsTheJobId', async () => {
    const { onOpenExtraction } = renderCard({ extraction: READY(JOB_ID) })

    const btn = control()
    expect(btn.disabled, 'a job exists, so the control must be enabled').toBe(false)
    expect(btn.textContent?.trim(), "the label must be the story table's string").toBe(CONTROL_LABEL)

    await userEvent.click(btn)

    expect(onOpenExtraction, 'one click must produce exactly one hand-off').toHaveBeenCalledTimes(1)
    expect(onOpenExtraction, 'the hand-off must carry the job id').toHaveBeenCalledWith(JOB_ID)
  })
})

describe('AC-6: without a job the control is disabled with visible text', () => {
  it('card_withoutAJobTheControlIsDisabledWithVisibleText', async () => {
    const { container, onOpenExtraction } = renderCard({ extraction: READY(null) })

    const btn = control()
    expect(btn.disabled, 'no job, so the control must carry the real disabled attribute').toBe(true)

    // Visible text, never a tooltip: a `title=` on a DISABLED button never fires in Chromium
    // and two QA passes on APPR-16 missed it. getByText throws if the sentence is absent.
    const reason = screen.getByText(NO_JOB_REASON)
    expect(screen.getByTestId('source-document-card').contains(reason), 'the reason must sit on the card').toBe(true)
    // jsdom computes no layout, so "visible" is only as strong as the declarations that can
    // hide a node outright. This is the reachable half; the painted half is EXTR11-E2E-07's.
    expect(reason.hidden, 'the reason must not be hidden').toBe(false)
    expect(reason.style.display, 'the reason must not be display:none').not.toBe('none')
    expect(reason.style.visibility, 'the reason must not be visibility:hidden').not.toBe('hidden')

    // The probe is the population floor for this absence: without it, a query that matched
    // nothing at all would read the same as a card that carries no title.
    const titled = [...container.querySelectorAll('[title]')].map((el) => el.getAttribute('data-testid'))
    expect(titled, 'the card must carry no title attribute -- only the control needle may').toEqual(['title-probe'])

    // pointerEventsCheck off: user-event otherwise refuses to dispatch at a disabled control,
    // which would satisfy the assertion below without ever exercising the click.
    await userEvent.setup({ pointerEventsCheck: 0 }).click(btn)
    expect(onOpenExtraction, 'a disabled control must swallow the click').not.toHaveBeenCalled()
  })
})

describe('AC-7: in flight the control is disabled with no reason', () => {
  it('card_inFlightTheControlIsDisabledWithNoReason', () => {
    renderCard({ extraction: IN_FLIGHT })

    expect(control().disabled, 'the lookup has not settled, so the control must be disabled').toBe(true)
    expect(
      screen.queryByText(NO_JOB_REASON),
      'a lookup still in flight has not established that there is no job -- the reason must not show yet',
    ).toBeNull()
  })

  it('card_theReasonIsWhatSeparatesTheTwoDisabledStates', () => {
    // Control needle for the absence above, on the same query: the settled no-job state DOES
    // render the sentence, so `queryByText` returning null there is a real difference.
    renderCard({ extraction: READY(null) })
    expect(screen.queryByText(NO_JOB_REASON), 'control: the settled no-job state must render the reason').not.toBeNull()
  })
})

// QA gap, closed here. `{ jobId, loading }` was two booleans for three states, so a failing
// GET /v1/extractions mapped onto `loading: true` -- disabled forever, nothing said. The
// sibling lookup's own failure renders <ErrorState/> (SourceDocumentCard.tsx:29-30) and the
// two can fail independently, so the card body renders while this control sits mute.
describe('QA-1: a failed lookup is disabled with its OWN reason', () => {
  it('card_aFailedLookupIsDisabledWithItsOwnVisibleReason', async () => {
    const { container, onOpenExtraction } = renderCard({ extraction: FAILED })

    const btn = control()
    expect(btn.disabled, 'a failed lookup found no job, so the control must be disabled').toBe(true)

    const reason = screen.getByText(LOOKUP_FAILED_REASON)
    expect(screen.getByTestId('source-document-card').contains(reason), 'the reason must sit on the card').toBe(true)
    expect(reason.hidden, 'the reason must not be hidden').toBe(false)
    expect(reason.style.display, 'the reason must not be display:none').not.toBe('none')

    // Never the no-job sentence: nothing about a failed lookup establishes that no job exists.
    expect(
      screen.queryByText(NO_JOB_REASON),
      'a failed lookup must not claim the document has no extraction',
    ).toBeNull()

    const titled = [...container.querySelectorAll('[title]')].map((el) => el.getAttribute('data-testid'))
    expect(titled, 'the reason must be visible text -- only the control needle may carry a title').toEqual(['title-probe'])

    await userEvent.setup({ pointerEventsCheck: 0 }).click(btn)
    expect(onOpenExtraction, 'a disabled control must swallow the click').not.toHaveBeenCalled()
  })

  it('card_theThreeDisabledArmsAreDistinguishable', () => {
    // All three states rendered through the same query, so "distinguishable" is measured and
    // not asserted twice from two directions. A shape that folded failed into loading would
    // put the same value in two cells here.
    const seen: Record<string, string | null> = {}
    for (const [arm, extraction] of [
      ['inFlight', IN_FLIGHT],
      ['noJob', READY(null)],
      ['failed', FAILED],
    ] as const) {
      cleanup()
      renderCard({ extraction })
      expect(control().disabled, `${arm}: every one of the three must be disabled`).toBe(true)
      const p = screen.getByTestId('source-document-card').querySelector('p:not([data-testid])')
      seen[arm] = p?.textContent ?? null
    }

    expect(seen.inFlight, 'in flight must show no reason at all').toBeNull()
    expect(seen.noJob, 'the settled no-job arm must show its own sentence').toBe(NO_JOB_REASON)
    expect(seen.failed, 'the failed arm must show its own sentence').toBe(LOOKUP_FAILED_REASON)
    expect(new Set(Object.values(seen)).size, 'the three disabled arms are not distinguishable').toBe(3)
  })

  it('card_theEnabledArmShowsNoReason', () => {
    // The fourth cell of the same table: a control that is enabled explains nothing.
    renderCard({ extraction: READY(JOB_ID) })
    expect(control().disabled).toBe(false)
    expect(screen.queryByText(NO_JOB_REASON)).toBeNull()
    expect(screen.queryByText(LOOKUP_FAILED_REASON)).toBeNull()
  })
})

describe('AC-8: the disabled control pins its own background and border', () => {
  it('card_theDisabledControlSetsBackgroundAndBorderColorInline', () => {
    renderCard({ extraction: READY(null) })
    const btn = control()

    // `.v2-btn-ghost:hover` carries no `!important`, so an inline declaration outranks it --
    // which is the whole reason these two are set rather than left to the class.
    expect(btn.style.background, 'the disabled control must pin its own background').not.toBe('')
    expect(btn.style.borderColor, 'the disabled control must pin its own border colour').not.toBe('')
  })

  it('card_theControlMatchesItsSiblingsRecipe', () => {
    renderCard({ extraction: READY(JOB_ID) })
    const btn = control()
    const sibling = screen.getByTestId('view-source-document') as HTMLButtonElement

    expect(btn.className, 'the control must use the rail button recipe').toBe('v2-btn v2-btn-ghost pf-btn')
    expect(btn.className, "and the same one as the sibling it sits under").toBe(sibling.className)
    // jsdom serialisation, measured: 34 -> '34px', 13 -> '13px', 12 -> '12px', '100%' raw.
    expect(btn.style.width, 'full width, like its sibling').toBe('100%')
    expect(btn.style.height).toBe('34px')
    expect(btn.style.fontSize).toBe('13px')
    expect(btn.style.marginTop).toBe('12px')
    // Order is the claim EXTR11-E2E-07 measures on the deployed build; here it is DOM order.
    expect(
      sibling.compareDocumentPosition(btn) & Node.DOCUMENT_POSITION_FOLLOWING,
      'the control must sit beneath view-source-document, not above it',
    ).toBeTruthy()
  })
})

describe('AC-4: the control needs a document record', () => {
  it('card_noControlWithoutADocumentRecord', () => {
    renderCard({ meta: meta(null), extraction: READY(JOB_ID) })

    // Floor: the no-record arm really rendered, so the absence below is a decision and not
    // an empty document.
    expect(screen.getByTestId('why-no-source-document'), 'the no-record arm did not render').toBeTruthy()
    expect(screen.queryByTestId(TESTID), 'a manually typed invoice has no extraction to offer').toBeNull()
    expect(screen.queryByText(NO_JOB_REASON), 'and no reason sentence either').toBeNull()
  })
})

describe('AC-9: the card still fetches nothing', () => {
  it('card_theCardStillFetchesNothing', async () => {
    const fetchMock = vi.fn(() => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({}) }))
    vi.stubGlobal('fetch', fetchMock)
    try {
      for (const extraction of [READY(JOB_ID), READY(null), IN_FLIGHT, FAILED]) {
        cleanup()
        renderCard({ extraction })
        // Floor: the card actually mounted in this iteration.
        expect(screen.getByTestId('source-document-card'), 'the card did not render').toBeTruthy()
      }

      expect(fetchMock, 'SourceDocumentCard.tsx:1-5 says the card never fetches').not.toHaveBeenCalled()

      // Control needle for that absence: the stub IS the global fetch, so zero calls above
      // was a real zero and not an unwired mock.
      await (globalThis.fetch as unknown as (u: string) => Promise<unknown>)('/probe')
      expect(fetchMock, 'control: the stub was never wired, so the zero above proved nothing').toHaveBeenCalledTimes(1)
    } finally {
      vi.unstubAllGlobals()
    }
  })
})

describe('the declarations this control depends on', () => {
  it('card_theSignatureCarriesTheExtractionProps', () => {
    const src = readFileSync(join(HERE, 'SourceDocumentCard.tsx'), 'utf8')
    const start = src.indexOf('export function SourceDocumentCard(')
    expect(start, 'no SourceDocumentCard declaration -- the scan anchor is broken').toBeGreaterThan(-1)
    const signature = src.slice(start, src.indexOf('{\n', start))
    // Control needle: proves the slice caught the signature and not an empty span.
    expect(signature, 'the slice missed the signature').toContain('meta')

    expect(signature, 'the card must take the extraction entry as a prop, not fetch it').toContain('extraction')
    expect(signature, 'the card must take the hand-off as a prop').toContain('onOpenExtraction')
  })

  it('card_theExtractionEntryCarriesTheFailedArm', () => {
    const src = readFileSync(join(HERE, 'SourceDocumentCard.tsx'), 'utf8')
    const match = src.match(/^export type ExtractionEntry = (.+)$/m)
    expect(match, 'no ExtractionEntry declaration -- the scan anchor is broken').not.toBeNull()
    // Control needle: proves the line read is the type and not a truncated span.
    expect(match![1], 'the slice missed the entry type').toContain('jobId: string | null')
    expect(match![1], 'the entry must be able to represent a failed lookup, not fold it into loading').toContain('failed: boolean')
  })

  // AC-11, closed across the two files that must agree. EXTR-11-05 wrote the e2e helper
  // before this control existed; if the executor picks a synonym, that mismatch would
  // otherwise surface only at EXTR-11-09's deploy gate.
  it('card_theTestidMatchesTheDeployedLocator', () => {
    const spec = readFileSync(join(REPO_ROOT, 'e2e', 'topology', 'import-wizard.spec.ts'), 'utf8')
    const start = spec.indexOf('async function openExtractionReview(')
    expect(start, 'openExtractionReview not found -- EXTR-11-05\'s helper moved or was renamed').toBeGreaterThan(-1)
    const body = spec.slice(start, spec.indexOf('\n}\n', start))
    const match = body.match(/getByTestId\('([^']+)'\)\s*\.click\(\)/)
    expect(match, 'the helper no longer clicks a testid -- this pin is reading the wrong thing').not.toBeNull()
    expect(match![1], 'the card must carry the exact testid the deployed specs click').toBe(TESTID)
  })

  // AC-12. InvoiceDetail.test.tsx keeps a CLOSED-WORLD testid inventory over this page and
  // errors on any undeclared id; the control ships onto that page, so the id belongs in
  // UNTOUCHED_TESTIDS beside its two siblings. That guard is the behavioural oracle -- it
  // reds the moment the control ships undeclared; this row is what makes the declaration
  // itself red TODAY, before there is anything to render.
  it('card_theTestidIsDeclaredInTheInvoiceDetailInventory', () => {
    const src = readFileSync(join(HERE, 'InvoiceDetail.test.tsx'), 'utf8')
    const start = src.indexOf('const UNTOUCHED_TESTIDS')
    expect(start, 'UNTOUCHED_TESTIDS not found -- the closed-world inventory moved').toBeGreaterThan(-1)
    const list = src.slice(start, src.indexOf('\n  ]', start))
    // Control needle: the two siblings the new id sits beside are already in this slice.
    expect(list, 'the slice missed the list body').toContain("'view-source-document'")
    expect(list, 'the slice missed the list body').toContain("'why-no-source-document'")

    expect(list, `'${TESTID}' must be declared in UNTOUCHED_TESTIDS`).toContain(`'${TESTID}'`)
  })
})
