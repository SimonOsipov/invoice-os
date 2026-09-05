// @vitest-environment jsdom
// RED specs (EXTR-15-09, task-835, Mode A / test-first) — AC-4: both review tabs take the
// review unit as a REQUIRED prop.
//
// AN OPTIONAL PROP IS THE WHOLE DEFECT. `unit?: ReviewUnit` compiles at every call site,
// falls back to 'spreadsheet' inside, and a caller that forgot it ships a document run
// still saying "rows" — with a green suite, because no render can tell "the caller passed
// spreadsheet" from "the caller passed nothing". Only the compiler can.
//
// SW-4 IS THEREFORE TWO SPECS AT TWO LAYERS, and neither one alone is the oracle:
//   SW-4a  the compile-time half. `pnpm --filter @invoice-os/app typecheck` is its ONLY
//          oracle — vitest strips types, so it runs green under vitest either way. The
//          two `@ts-expect-error` directives below are USED: omitting `unit` is a tsc
//          error, so tsc is quiet. Make the prop optional and they go unused, which tsc
//          reports as TS2578. That inversion is the spec.
//   SW-4b  the source-scan half, so the vitest leg is not blind to AC-4 entirely. It sees
//          `unit?:` and a default value, which is what an executor reaches for when the
//          existing render call sites start failing to compile.
import { readFileSync } from 'node:fs'
import path from 'node:path'

import { cleanup, fireEvent, render } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { ReviewUnit, UnreadableRowAll } from '../lib/reviewBatch'
import type { PlatformCtx } from '../types'
import { ReviewAlreadyImportedTab } from './ReviewAlreadyImportedTab'
import { ReviewUnreadableTab } from './ReviewUnreadableTab'

function readSrc(rel: string): string {
  return readFileSync(path.join(process.cwd(), rel), 'utf8')
}

describe('EXTR-15-09 SW-4 (AC-4): the unit is a required prop on both review tabs', () => {
  it('SW-4a (GREEN under `typecheck` since EXTR-15-09; always green under vitest): omitting `unit` must not compile', () => {
    const missingOnUnreadable = (
      // @ts-expect-error AC-4: `unit` is required. While this directive is UNUSED, tsc
      // reports TS2578 here and that IS this spec's red.
      <ReviewUnreadableTab rows={[]} rowsTotal={0} batchIds={['b1']} onImportCorrected={() => {}} />
    )
    const missingOnAlreadyImported = (
      // @ts-expect-error AC-4: `unit` is required — see the note above.
      <ReviewAlreadyImportedTab rows={[]} rowsTotal={0} batchIds={['b1']} onOpenInvoice={() => {}} />
    )

    // Runtime is not the oracle here; these two keep the fixtures from being dead code.
    expect(missingOnUnreadable).toBeTruthy()
    expect(missingOnAlreadyImported).toBeTruthy()
  })

  it('SW-4b (GREEN since EXTR-15-09): each tab declares `unit: ReviewUnit`, neither optional nor defaulted', () => {
    const files = ['src/components/ReviewUnreadableTab.tsx', 'src/components/ReviewAlreadyImportedTab.tsx']
    const wrong: string[] = []

    for (const file of files) {
      const src = readSrc(file)
      // Control, paired with the two absence claims below: a moved or emptied file would
      // otherwise satisfy them by scanning nothing.
      expect(src, `${file} was not read`).toContain('export function Review')

      if (!src.includes('unit: ReviewUnit')) wrong.push(`${file}: no \`unit: ReviewUnit\` in the props type`)
      if (src.includes('unit?:')) wrong.push(`${file}: the unit prop is optional`)
      if (/unit(: ReviewUnit)?\s*=\s*'(spreadsheet|document)'/.test(src)) wrong.push(`${file}: the unit prop is defaulted`)
    }

    expect(wrong).toEqual([])
  })
})

// ============================================================================
// RED specs (EXTR-15-11, task-856, Mode A) — "Enter it by hand", from the Unreadable tab.
//
// A document too poorly scanned to read mints a quarantined batch and NO invoice, so the
// extraction review screen (whose only entry point renders on an invoice detail) can never
// reach it. Subtask 05 gave this tab a sentence saying the invoice can be typed by hand and
// nothing to click. This is the control.
//
// CONTRACT THESE SPECS PIN, and which the executor owes:
//   - ReviewUnreadableTab gains `ctx: PlatformCtx`, exactly as ReviewInvoicesTab already
//     takes it from the same shell (ReviewBatch.tsx:405-406). One prop, not two: it carries
//     BOTH the shipped `ctx.enterByHand` (types.ts:490, EXTR-15-07) and `ctx.activeEntity`,
//     the resolved-entity predicate every filing gate reads. A separate `onEnterByHand`
//     callback would be a second entry into the same handler, which the brief forbids.
//   - the control renders only for `unit === 'document'`, once per row that HAS a
//     documentId, inside the existing "why" cell.
//   If subtask 11 wires it under another prop name, THESE specs are what change — the
//   choice is stated, not assumed.
//
// TYPECHECK IS RED UNTIL SUBTASK 11 LANDS. `ctx` is not on the props type yet, so
// `pnpm --filter @invoice-os/app typecheck` reports TS2322 on renderTab below. That is the
// compile-layer half of this pass's red, and it goes green when the prop is added. There is
// deliberately no suppression directive on it: an expect-error would invert into TS2578 the
// moment the executor is correct. (Never open a comment LINE with the directive text
// either — tsc reads it as a real directive wherever it starts a line.)
//
// Copy: 'Enter it by hand' is fixed by AC-1 and pinned literally — the same label
// CreateFlow.tsx:186 already ships. The no-entity REASON copy is deliberately NOT pinned:
// no exported constant for it exists, and importing one that does not exist would make this
// file fail to COLLECT rather than fail an assertion. The four-layer disabled recipe is
// asserted structurally instead (UT-3) and byte-compared against the shipped sibling (UT-7).

const HAND_OFF_LABEL = 'Enter it by hand'

// Both halves of AC-5's "no layout constant changes": the source literal, and the
// serialized inline style it produces. UT-5 reads both.
const UNREADABLE_GRID_SOURCE = "const UNREADABLE_GRID = '150px 90px 170px 1fr'"
const UNREADABLE_GRID_STYLE = 'grid-template-columns: 150px 90px 170px 1fr'

const DOC_A = '9a4c1e77-2f80-4c53-b6d2-51e0a3f8cc19'
const DOC_B = 'a0000000-0000-4000-8000-00000000000b'
const DOC_C = 'c1111111-1111-4111-8111-11111111111c'

// Three rows from three different documents with three different filenames. A fixture whose
// items are naturally equal cannot tell a per-row read from a tab-level scalar (UT-4).
const THREE_ROWS: UnreadableRowAll[] = [
  { row: null, column: '—', message: 'The scan was too poor to read.', file: 'march-invoice.pdf', documentId: DOC_A },
  { row: null, column: '—', message: 'No invoice number was found.', file: 'april-invoice.pdf', documentId: DOC_C },
  { row: null, column: '—', message: 'The document had no readable text.', file: 'may-invoice.pdf', documentId: DOC_B },
]

// One row that CAN be handed off and one that cannot, in one render (UT-6). A quarantined
// batch whose source document was never recorded has nothing to hand off.
const NO_DOCUMENT: UnreadableRowAll = {
  row: null,
  column: '—',
  message: 'The upload never reached storage.',
  file: 'lost.pdf',
  documentId: null,
}
const MIXED_ROWS: UnreadableRowAll[] = [THREE_ROWS[0], NO_DOCUMENT]

/**
 * The rendered text of THREE_ROWS at unit='spreadsheet', captured on a2855587 BEFORE this
 * subtask. UT-2's regression guard: the spreadsheet unit's DOM must not move by one
 * character. The rows carry documentIds deliberately — a fixture whose spreadsheet rows had
 * `documentId: null` would be satisfied by AC-6's null gate and could not see the unit gate
 * at all.
 */
const SPREADSHEET_TEXT_BEFORE =
  '3 rows never became invoicesThe importer could not read them, so no rule was ever run against them and nothing was stored. They cannot be fixed here: correct the rows in your file and import again.FileRowFieldWhy it could not be readmarch-invoice.pdf——The scan was too poor to read.april-invoice.pdf——No invoice number was found.may-invoice.pdf——The document had no readable text.Field names are the importer’s own, not your spreadsheet’s headings, and are a best guess on numeric errors.Download this list (CSV)Import a corrected file3 of 12 rows. The invoices that did import are unaffected.'

function unreadableCtx(over: Record<string, unknown> = {}): PlatformCtx {
  return {
    activeEntity: { id: 'e-1', name: 'Lagos Freight Ltd', tin: '12345678-0001' },
    enterByHand: () => {},
    ...over,
  } as unknown as PlatformCtx
}

function renderTab(rows: UnreadableRowAll[], unit: ReviewUnit, ctxOver: Record<string, unknown> = {}) {
  return render(
    <ReviewUnreadableTab
      ctx={unreadableCtx(ctxOver)}
      rows={rows}
      rowsTotal={12}
      batchIds={['b1']}
      onImportCorrected={() => {}}
      unit={unit}
    />,
  )
}

function handOffButtons(root: HTMLElement): HTMLButtonElement[] {
  return Array.from(root.querySelectorAll('button')).filter((b) => (b.textContent ?? '').trim() === HAND_OFF_LABEL)
}

// A data row is a grid div that is not the header; the header alone carries className
// "label". Matching the serialized style attribute rather than a testid keeps these specs
// from demanding a DOM marker AC-2 does not ask for.
function gridDivs(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>('div')).filter((d) =>
    (d.getAttribute('style') ?? '').includes(UNREADABLE_GRID_STYLE),
  )
}

function dataRows(container: HTMLElement): HTMLElement[] {
  return gridDivs(container).filter((d) => !d.classList.contains('label'))
}

function headerCells(container: HTMLElement): HTMLElement[] {
  const header = gridDivs(container).find((d) => d.classList.contains('label'))
  return header === undefined ? [] : (Array.from(header.children) as HTMLElement[])
}

/**
 * ceiling: jsdom loads no stylesheets, so this reads the inline/attribute layer only. It
 * catches the defect APPR-16 shipped — a `title=` with no rendered text beside it — but a
 * class-driven `display:none` would slip past. Same helper, same limit, as
 * CreateFlow.test.tsx's.
 */
function visibleText(el: HTMLElement | null): string {
  if (el === null) return ''
  if (el.hidden) return ''
  const s = el.style
  if (s.display === 'none' || s.visibility === 'hidden' || s.opacity === '0') return ''
  return (el.textContent ?? '').trim()
}

describe('EXTR-15-11: the Unreadable tab offers manual entry (AC-1/AC-2)', () => {
  afterEach(() => cleanup())

  it('UT-1 (AC-1): every document row offers "Enter it by hand", enabled when an entity is resolved', () => {
    const { container } = renderTab(THREE_ROWS, 'document')

    // Population floor FIRST: "three buttons" would also be satisfied by a surface that
    // rendered three rows of nothing.
    const rows = dataRows(container)
    expect(rows, 'all three unreadable rows must render before any button is counted').toHaveLength(3)
    expect(rows.map((r) => r.textContent)).toEqual([
      expect.stringContaining('march-invoice.pdf'),
      expect.stringContaining('april-invoice.pdf'),
      expect.stringContaining('may-invoice.pdf'),
    ])

    const buttons = handOffButtons(container)
    expect(buttons).toHaveLength(3)
    // The enabled-control leg. Without it UT-3 would also pass on a button wired
    // permanently disabled.
    expect(buttons.map((b) => b.disabled)).toEqual([false, false, false])
    expect(buttons.map((b) => b.getAttribute('title'))).toEqual([null, null, null])
  })

  it('UT-2 (AC-2): the spreadsheet unit renders ZERO buttons and text byte-equal to the pre-change render', () => {
    // Differential, not an absence: the document leg is the control needle. Asserting only
    // "spreadsheet has no button" passes today, and passes on a component that throws.
    const documentRender = renderTab(THREE_ROWS, 'document')
    expect(handOffButtons(documentRender.container), 'the document unit must offer the control').toHaveLength(3)
    cleanup()

    const { container } = renderTab(THREE_ROWS, 'spreadsheet')
    expect(dataRows(container), 'the spreadsheet render must be populated').toHaveLength(3)
    expect(handOffButtons(container)).toHaveLength(0)
    expect(container.textContent, 'the spreadsheet unit’s DOM moved').toBe(SPREADSHEET_TEXT_BEFORE)
  })
})

describe('EXTR-15-11: the entity gate refuses with a visible reason (AC-3)', () => {
  afterEach(() => cleanup())

  it('UT-3 (AC-3): with no resolved entity the button is DISABLED with a visible reason, never hidden', () => {
    // `ctx.activeEntity === null` is the predicate every filing gate reads
    // (CreateUpload.tsx:98, CreateFlow.tsx's DocumentFailureRow). Not entitiesState, not a
    // client id, either of which can be non-null before the entity is fetched.
    const { container } = renderTab([THREE_ROWS[0]], 'document', { activeEntity: null })

    const buttons = handOffButtons(container)
    expect(buttons, 'the button must still RENDER without an entity — disabled-with-reason, never hidden').toHaveLength(1)
    const button = buttons[0]
    expect(button.disabled).toBe(true)

    const describedBy = button.getAttribute('aria-describedby')
    expect(describedBy, 'a disabled control must point at its reason').toBeTruthy()

    const reason = container.ownerDocument.getElementById(String(describedBy)) as HTMLElement | null
    expect(reason, `aria-describedby="${describedBy}" resolves to no node in the document`).not.toBeNull()

    // APPR-16: a title= alone is invisible in Chromium, and a disabled button is out of the
    // tab order, so the visible sibling is the only layer a keyboard or SR user reaches.
    const text = visibleText(reason)
    expect(text.length, 'the reason node is empty or hidden — a title= alone is invisible').toBeGreaterThan(0)
    expect(button.getAttribute('title'), 'the title must say what the visible reason says').toBe(text)

    // Scoped to the row it refuses, not a stray sentence elsewhere in the tab.
    const rows = dataRows(container)
    expect(rows).toHaveLength(1)
    expect(rows[0].contains(reason), 'the reason must sit inside the row it refuses').toBe(true)
  })
})

describe('EXTR-15-11: the hand-off carries THIS row’s document (AC-4)', () => {
  afterEach(() => cleanup())

  it('UT-4 (AC-4): two different rows in ONE render dispatch two different document ids', () => {
    // The sole oracle for per-row attribution. A tab-level scalar `documentId` passes every
    // other spec in this file; only two clicks with two different expected values see it.
    expect(DOC_A, 'the fixture cannot discriminate if its two rows agree').not.toBe(DOC_B)

    const enterByHand = vi.fn()
    const { container } = renderTab(THREE_ROWS, 'document', { enterByHand })

    const buttons = handOffButtons(container)
    expect(buttons).toHaveLength(3)
    fireEvent.click(buttons[0])
    fireEvent.click(buttons[2])

    expect(enterByHand).toHaveBeenCalledTimes(2)
    expect(enterByHand.mock.calls).toEqual([[DOC_A], [DOC_B]])
  })
})

describe('EXTR-15-11: the control lands in the existing "why" cell (AC-5)', () => {
  afterEach(() => cleanup())

  it('UT-5 (AC-5): the grid literal is untouched, the header keeps its four cells, and the button sits in cell 4', () => {
    const src = readSrc('src/components/ReviewUnreadableTab.tsx')
    // Control, paired with the literal claim below: a moved or renamed file scans as clean.
    expect(src, 'ReviewUnreadableTab.tsx was not read').toContain('export function ReviewUnreadableTab')
    expect(src, 'the unreadable grid changed — AC-5 forbids a new column').toContain(UNREADABLE_GRID_SOURCE)

    const documentRender = renderTab(THREE_ROWS, 'document')
    const documentHeader = headerCells(documentRender.container).length
    const rows = dataRows(documentRender.container)
    expect(rows).toHaveLength(3)

    // Four direct children, matching the four grid tracks. A fifth would be a new column.
    expect(rows.map((r) => r.children.length)).toEqual([4, 4, 4])

    const buttons = handOffButtons(documentRender.container)
    expect(buttons).toHaveLength(3)
    expect(
      rows.map((r, i) => r.children[3].contains(buttons[i])),
      'the button must land inside the existing "why" cell, not beside it',
    ).toEqual([true, true, true])
    cleanup()

    const spreadsheetRender = renderTab(THREE_ROWS, 'spreadsheet')
    expect(documentHeader, 'the header must be populated').toBe(4)
    expect(headerCells(spreadsheetRender.container).length, 'the header cell count diverged between units').toBe(documentHeader)
  })
})

describe('EXTR-15-11: a row with no stored document offers nothing (AC-6)', () => {
  afterEach(() => cleanup())

  it('UT-6 (AC-6): the null-document row renders its file and reason but no control, beside a row that has one', () => {
    const { container } = renderTab(MIXED_ROWS, 'document')

    const rows = dataRows(container)
    expect(rows, 'both rows must render').toHaveLength(2)

    // Positive needle: the row is populated. An absence-only claim would pass on a blank
    // screen, which is the very defect this story exists to close.
    expect(rows[1].textContent).toContain(NO_DOCUMENT.file)
    expect(rows[1].textContent).toContain(NO_DOCUMENT.message)

    // No button, not a disabled one: there is nothing to hand off.
    expect(handOffButtons(rows[1]), 'a row with no stored document must offer no control at all').toHaveLength(0)
    // And the sibling that DOES have one still offers it, so the absence above is not
    // satisfied by a tab that dropped the control entirely.
    expect(handOffButtons(rows[0])).toHaveLength(1)
    expect(handOffButtons(container)).toHaveLength(1)
  })
})

describe('EXTR-15-11: the disabled recipe is the shipped sibling’s, not a second one (AC-3)', () => {
  it('UT-7: the disabled-branch style literal is byte-equal to ReviewAlreadyImportedTab’s', () => {
    // Read at test time, not transcribed: a token change in the sibling must not be able to
    // leave the two surfaces silently disagreeing.
    const siblingSrc = readSrc('src/components/ReviewAlreadyImportedTab.tsx')
    const unreadableSrc = readSrc('src/components/ReviewUnreadableTab.tsx')
    expect(siblingSrc, 'ReviewAlreadyImportedTab.tsx was not read').toContain('export function ReviewAlreadyImportedTab')
    expect(unreadableSrc, 'ReviewUnreadableTab.tsx was not read').toContain('export function ReviewUnreadableTab')

    const match = /\.\.\.\([^?]*\?\s*(\{[^{}]*\})\s*:\s*null\)/.exec(siblingSrc)
    expect(match, 'the sibling’s disabled-only style spread was not found — this spec is scanning nothing').not.toBeNull()
    const literal = match![1]
    // Control on the extraction itself: the right capture, not an empty object.
    expect(literal, 'the captured literal is not the disabled recipe').toContain("cursor: 'not-allowed'")

    expect(
      unreadableSrc.includes(literal),
      `the hand-off button must reuse the shipped disabled recipe verbatim:\n  ${literal}`,
    ).toBe(true)
  })
})
