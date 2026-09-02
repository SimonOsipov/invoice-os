// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// EXTR-11-06. The rows down to the AC-9 block were written RED (`6e205fb4`) against a
// throwaway reference build and mutated once per row there; `74cf37d6` then shipped the real
// component and they went green unchanged. The Mode B block at the foot was written against
// the SHIPPED component and mutated against it — the two claims are different, which is why
// the blocks are kept apart.
//
// MEASURED jsdom 27.4.0 serialization — read back off a rendered probe, not assumed:
//   `flex: 'none'` reads `0 0 auto`; `flex: 1` reads `1 1 0%`
//   `minHeight: 0` and `minWidth: 0` read `0`, but `margin: 0` reads `0px`
//   `minWidth: 470` reads `470px`; `flex: '1 1 620px'` and `boxShadow` with `var()` round-trip raw
//   `fontSize: 8.5` reads `8.5px`; `borderRadius: 999` reads `999px`
//   `gap: '14px 16px'` round-trips raw; `gap: 6` reads `6px`
//   `gridTemplateColumns: '1fr 1fr'` round-trips raw
//   setting `overflowY` alone leaves `style.overflow` EMPTY — which is what makes the
//   body's y-only assertion a real oracle rather than a restatement
//   a `border` shorthand carrying `var()` round-trips raw (PersonaFooter.test.tsx:294)
//
// This file installs NO `Element.prototype.scrollTo` shim, and does not need one.
// `scrollRegionIntoView` (extractionReview.ts:151) is reached from exactly one call site,
// `ExtractionCanvas.tsx`'s selection effect; the fields pane owns no ground and no ref, and
// reports the field name upward instead. System Design §5 says "EXTR-11-06 and -07 must
// carry it too" — that is true of -07, which mounts both panes, and false here.
//
// `getComputedStyle` appears in no app unit spec and none is used below: every style oracle
// reads `el.style.*`, the inline declaration the component wrote.

import type { ReactNode } from 'react'

import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { crossGlyph, crosshairGlyph } from '../glyphs'
import { fieldLabel } from '../lib/extractionReview'
import { LINE_ROLES, lineFieldName } from '../lib/lineItems'
import type {
  DraftEntries,
  ExtractionCandidate,
  ExtractionCorrected,
  ExtractionFieldState,
  ExtractionRegion,
} from '../lib/extractionReview'
import { ExtractionFields } from './ExtractionFields'

// The artboard's final title (`Recognition Review.dc.html:226`). A literal, never a regex:
// the sentence is the design's own and a paraphrase is a different product.
const TITLE = 'The invoice as it will be filed'

// The pill repurposes the artboard's per-field reason slot (`:296`). `NO REGION` is this
// story's own copy — the artboard's slot carries a reason code, which EXTR-12 owns.
const PILL = 'NO REGION'

// D-15. `internal/invoice/store.go:205-221` overwrites SupplierTIN and SupplierName from the
// signed-in entity on EVERY Store.Create, so the two values this pane renders beside the page
// image are not what gets filed. Prose, no verdict, no comparison — a pill is EXTR-12's.
const SENTENCE = 'The supplier filed on this invoice comes from your client record, not from this document.'

// EXTR-12's Invented-copy table, verbatim. Literals, never an import of the constant under
// test: a test that imports the string it asserts asserts the component against itself.
const PILL_UNREADABLE = "COULDN'T READ THIS CLEARLY"
const PILL_AMBIGUOUS = 'FOUND TWO POSSIBLE VALUES'
const PILL_INCONSISTENT = "DOESN'T ADD UP"
const PILL_MISSING = 'NOT FOUND'
const NOTE_SUBTOTAL = 'The line items we read do not add up to this subtotal.'
const NOTE_SUPPLIER =
  "This document's supplier doesn't match the client you picked. It is filed from your client record either way."
const NOTE_GENERIC = 'This value disagrees with the other numbers on the document.'
const MARKER_TYPED = 'YOU CHANGED THIS'
const MARKER_POINTED = 'YOU POINTED THIS OUT'
const MARKER_CHOSEN = 'YOU CHOSE THIS'
const WAS_CHOSEN = 'We found more than one candidate'
const UNDO = 'Undo'
const INVOICE_NUMBER_LOCKED = "The invoice number is this invoice's identity and cannot be changed here."

// EXTR-13-07's own copy table (LineItemGrid), verbatim -- the grid mounts inside this pane's
// body, so the residue sweep below has to know it.
const LINE_ITEM_EMPTY_1 = 'We found no line items on this document.'
const LINE_ITEM_EMPTY_2 = 'An invoice cannot be filed until it has at least one line, so add one here.'
const LINE_ITEM_ADD = 'Add a line'

// EXTR-12's Invented-copy table again, with the lead's two Stage-1 corrections:
// `POINT_PAGELESS` is the artboard's isDocx branch (`:663`), transcribed into a row the table
// omitted; `POINT_ARMED` replaces the artboard's click-arm string, which names a gesture this
// build refuses under the 24x12 floor.
const POINT_IDLE = 'Not found — point at it on the document'
const POINT_ARMED = 'Waiting — drag a box around it on the document'
const POINT_CANCEL = 'Stop pointing'
const POINT_PAGELESS = 'Not found — type it in'

// internal/extraction/handlers_correction.go, lockedFields. invoice_number is what the invoice
// is filed under; updateContentTx re-derives the two supplier fields from the client entity and
// never reads the input, so a correction on any of the three is a 422.
const LOCKED_FIELDS = ['invoice_number', 'supplier_tin', 'supplier_name']

// internal/extraction/vocabulary.go, HeaderFields.
const HEADER_FIELDS = [
  'invoice_number',
  'issue_date',
  'supplier_tin',
  'supplier_name',
  'buyer_tin',
  'buyer_name',
  'currency',
  'subtotal',
  'vat',
  'total',
]

// SourceDocumentPages.test.tsx:31's list, verbatim: every class here forces `border-radius`
// with `!important`, from app-layer.css:193-197 and :275.
const RADIUS_FORCING = ['pf-btn', 'pf-chip', 'v2-btn', 'ops-btn', 'dev-btn', 'ops-chip', 'dev-chip']

// RulePills.tsx:7-22 is the near-miss: the amber triple is identical, the two font metrics
// are not. C-3 — picked, not averaged.
const RULE_PILL_FONT_SIZE = '9px'
const RULE_PILL_LETTER_SPACING = '0.04em'

function mkRegion(o: Partial<ExtractionRegion> = {}): ExtractionRegion {
  return { page: 1, x0: 0.62, y0: 0.08, x1: 0.9, y1: 0.13, ...o }
}

function mkField(o: Partial<ExtractionFieldState> = {}): ExtractionFieldState {
  return {
    name: 'invoice_number',
    value: 'INV-2026-0037',
    region: mkRegion(),
    reason: '',
    alternatives: [],
    corrected: null,
    ...o,
  }
}

// Wire order is neither the vocabulary's (internal/extraction/vocabulary.go:6-9 puts
// invoice_number first and total last) nor alphabetical (which agrees with the vocabulary
// on exactly these three). A pane that sorts by either fails the order row below.
const THREE_FIELDS: ExtractionFieldState[] = [
  mkField({ name: 'total', value: '1,250,000.00', region: mkRegion({ page: 3, y0: 0.7, y1: 0.76 }) }),
  mkField({ name: 'invoice_number', value: 'INV-2026-0037' }),
  mkField({ name: 'issue_date', value: '2026-08-12', region: mkRegion({ page: 1, x0: 0.1, x1: 0.38 }) }),
]

const WIRE_ORDER = ['extraction-field-total', 'extraction-field-invoice_number', 'extraction-field-issue_date']

/** The same three, with the middle one pointing nowhere. D-21: still a row, still selectable. */
const ONE_REGIONLESS: ExtractionFieldState[] = [
  THREE_FIELDS[0],
  mkField({ name: 'invoice_number', value: 'INV-2026-0037', region: null }),
  THREE_FIELDS[2],
]

interface FieldsProps {
  fields: ExtractionFieldState[]
  selected: string | null
  draft: DraftEntries
  /** The field waiting for a box. Null unless a row is arming one. */
  armed: string | null
  /** True unless a row is asking for the pageless cell. */
  canPoint: boolean
  onSelect: (name: string) => void
  onType: (name: string, value: string) => void
  onChoose: (name: string, candidate: ExtractionCandidate) => void
  onUndo: (name: string) => void
  onArm: (name: string) => void
  onDisarm: () => void
}

function fieldsPane(over: Partial<FieldsProps> = {}) {
  const props: FieldsProps = {
    fields: THREE_FIELDS,
    selected: null,
    draft: {},
    armed: null,
    canPoint: true,
    onSelect: () => {},
    onType: () => {},
    onChoose: () => {},
    onUndo: () => {},
    onArm: () => {},
    onDisarm: () => {},
    ...over,
  }
  return <ExtractionFields {...props} />
}

/** Every field the vocabulary names, so the editable/locked split has all ten to sort. */
function tenFields(): ExtractionFieldState[] {
  return HEADER_FIELDS.map((name, i) => mkField({ name, value: `v-${i}` }))
}

// -- DOM readers -------------------------------------------------------------------------

function pane(): HTMLElement {
  return screen.getByTestId('extraction-fields')
}

/**
 * The pane's two children, in the artboard's order (`:225` header over `:230` body). A third
 * would be chrome this story deferred — the header's mono meta and the summary block both
 * live in the artboard and both are EXTR-12's.
 */
function paneParts(): { header: HTMLElement; body: HTMLElement } {
  const kids = Array.from(pane().children) as HTMLElement[]
  expect(kids.length, 'the pane is a header over a body, and nothing else').toBe(2)
  return { header: kids[0], body: kids[1] }
}

/** The one inline grid inside the body. Resolved by declaration, not by a testid §4 lacks. */
function grid(): HTMLElement {
  const { body } = paneParts()
  const found = Array.from(body.querySelectorAll<HTMLElement>('*')).filter((el) => el.style.display === 'grid')
  expect(found.length, 'the body holds exactly one inline grid').toBe(1)
  return found[0]
}

// The trailing hyphen matters: `extraction-fields` is itself prefixed by `extraction-field`.
function rows(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>('[data-testid^="extraction-field-"]'))
}

function rowIds(): string[] {
  return rows().map((el) => el.dataset.testid ?? '')
}

function row(name: string): HTMLElement {
  return screen.getByTestId(`extraction-field-${name}`)
}

// `aria-current`, not `aria-pressed`: the cell holds an input and buttons, so it can no longer
// take a button role, and `aria-pressed` is only valid on one.
function selectedIds(): string[] {
  return rows()
    .filter((el) => el.getAttribute('aria-current') === 'true')
    .map((el) => el.dataset.testid ?? '')
}

function inputOf(name: string): HTMLInputElement | null {
  return screen.queryByTestId(`extraction-input-${name}`) as HTMLInputElement | null
}

/** The value the cell shows for a field. `null` when the cell renders no input at all. */
function valueOf(name: string): string | null {
  return inputOf(name)?.value ?? null
}

function chipsOf(name: string): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>(`[data-testid^="extraction-chip-${name}-"]`))
}

function undoOf(name: string): HTMLElement | null {
  return screen.queryByTestId(`extraction-undo-${name}`)
}

// Both OUTSIDE the `extraction-field-` prefix: three row counts key on it.
function pointOf(name: string): HTMLElement | null {
  return screen.queryByTestId(`extraction-point-${name}`)
}

function cancelOf(name: string): HTMLElement | null {
  return screen.queryByTestId(`extraction-point-cancel-${name}`)
}

/** Every natively focusable control inside one cell. */
function controlsOf(name: string): HTMLElement[] {
  return Array.from(row(name).querySelectorAll<HTMLElement>('input, button'))
}

function mkCandidate(value: string, page: number): ExtractionCandidate {
  return { value, region: mkRegion({ page }) }
}

/** The one dashed panel in the pane. The app's empty idiom, MemberParts.tsx:55-57's rule. */
function dashedPanel(): HTMLElement {
  const found = Array.from(pane().querySelectorAll<HTMLElement>('*')).filter((el) => /^1px dashed /.test(el.style.border))
  expect(found.length, 'the empty pane draws exactly one dashed panel').toBe(1)
  return found[0]
}

function classesOf(el: HTMLElement): string[] {
  return (el.getAttribute('class') ?? '').split(/\s+/).filter(Boolean)
}

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

// ==========================================================================================
// The pane header and its scrollable body (AC-1)
// ==========================================================================================

describe('the pane chrome', () => {
  it('carries the title the artboard settled on, at its metrics', () => {
    render(fieldsPane())

    const title = screen.getByText(TITLE)
    expect(title.style.fontSize).toBe('15px')
    expect(title.style.fontWeight).toBe('600')
    expect(title.style.letterSpacing).toBe('-0.02em')

    // `:225`. `flex: 'none'` serialises to the long form; the header must not scroll away
    // with the fields under it.
    const { header } = paneParts()
    expect(header.contains(title), 'the title is not in the first child of the pane').toBe(true)
    expect(header.style.flex).toBe('0 0 auto')
    expect(header.style.padding).toBe('13px 20px')
    expect(header.style.background).toBe('var(--bg-2)')
    expect(header.style.borderBottom).toBe('1px solid var(--line-1)')
  })

  it('scrolls the body on one axis, not the header and not both', () => {
    render(fieldsPane())

    const { header, body } = paneParts()

    // `:230`. The pair `flex: 1` + `minHeight: 0` is what stops a flex item taking a
    // content-based automatic minimum and growing past its column (C-2).
    expect(body.style.flex).toBe('1 1 0%')
    expect(body.style.minHeight).toBe('0')
    expect(body.style.padding).toBe('16px 20px 24px')

    // y-only, never the shorthand. jsdom leaves `style.overflow` empty when only the y axis
    // is set, so this pair is a real discriminator rather than a restatement — and the axis
    // is load-bearing: an `overflow: auto` would hide a row spilling its column behind a
    // scrollbar, which is exactly what EXTR11-E2E-02a measures on the deployed build.
    expect(body.style.overflowY).toBe('auto')
    expect(body.style.overflow, 'the body takes the shorthand, so a horizontal spill scrolls instead of failing').toBe('')

    expect(header.style.overflowY, 'the header scrolls').toBe('')
  })
})

// ==========================================================================================
// One cell per HEADER wire field, in wire order (AC-2, AC-3)
// ==========================================================================================

describe('the field cells', () => {
  it('renders one row per HEADER wire field, in wire order', () => {
    render(fieldsPane())

    // The fixture is deliberately unsorted: a pane that sorts by the vocabulary or
    // alphabetically produces invoice_number, issue_date, total and fails here.
    expect(
      THREE_FIELDS.map((f) => f.name),
      'the fixture is already sorted — this row proves nothing',
    ).toEqual(['total', 'invoice_number', 'issue_date'])
    expect(rowIds()).toEqual(WIRE_ORDER)
  })

  it('lays the cells out on the two columns the artboard declares', () => {
    render(fieldsPane())

    // `:290`.
    const g = grid()
    expect(g.style.gridTemplateColumns).toBe('1fr 1fr')
    expect(g.style.gap).toBe('14px 16px')
    expect(rows().every((r) => g.contains(r)), 'a row rendered outside the grid').toBe(true)

    // `:292`. Every cell occupies ONE column: the artboard's `grid-column: span 2` on
    // Supplier name is keyed to its own fixed nine-field list, and this pane renders
    // whatever the wire carries. A span map would be an invented lookup.
    for (const r of rows()) {
      expect(r.style.display).toBe('flex')
      expect(r.style.flexDirection).toBe('column')
      expect(r.style.gap).toBe('6px')
      expect(r.style.gridColumn, `${r.dataset.testid} spans more than its own column`).toBe('')
    }
  })

  it('gives the label the artboard metrics, in a row that reserves height for the pill', () => {
    render(fieldsPane())

    // `:294`. The label is `fieldLabel(f.name)` — the shipped EDIT_FIELD_LABELS plus this
    // module's one overlay entry, not the raw wire name and not a mechanical humaniser.
    const label = within(row('invoice_number')).queryByText(fieldLabel('invoice_number'))
    expect(label, 'the cell renders something other than the curated label').toBeTruthy()
    expect(
      within(row('invoice_number')).queryByText('invoice_number'),
      'the raw wire name still renders beside its label',
    ).toBeNull()
    expect(label!.style.fontSize).toBe('12px')
    expect(label!.style.fontWeight).toBe('500')
    expect(label!.style.color).toBe('var(--fg-2)')

    // The `min-height` is what stops a row without a pill sitting 18px shorter than its
    // neighbour with one — the reason the artboard declares it at all.
    const strip = label!.parentElement as HTMLElement
    expect(strip.style.gap).toBe('8px')
    expect(strip.style.minHeight).toBe('18px')
  })

  it('wraps the label strip, so a pill drops to its own line instead of spilling its column', () => {
    // W-6, measured in Chromium at the pane's 470px floor: 191px of cell content against a
    // 199px strip on `issue_date` and 197px on `vat`. The label is already at min-content and
    // the pill declares `flex: none`, so a nowrap row has nowhere to put the remainder.
    // Declaration-level only — the deployed oracle is EXTR12-E2E-07's floor walk, which needs
    // real fonts and therefore first executes on the gate.
    render(fieldsPane({ fields: ONE_REGIONLESS }))

    const strip = within(row('invoice_number')).getByText(PILL).parentElement as HTMLElement
    expect(strip.style.display, 'the pill does not sit in the label strip').toBe('flex')
    expect(strip.style.flexWrap).toBe('wrap')
  })

  it('renders an empty input for a null value, and no em-dash placeholder', () => {
    // The em-dash was the read-only cell's stand-in for "nothing here". An editable field says
    // it by being empty, and a typed em-dash would be a value the person has to delete first.
    render(fieldsPane({ fields: [mkField({ name: 'buyer_tin', value: null })] }))

    const r = row('buyer_tin')
    expect(inputOf('buyer_tin'), 'the missing field renders no input to type into').toBeTruthy()
    expect(valueOf('buyer_tin'), 'a null value reached the input as text').toBe('')
    expect(r.textContent, 'a null value leaked a guess').not.toContain('null')
    expect(within(r).queryByText('—'), 'the input carries an em-dash the person must delete').toBeNull()
  })

  it('renders an empty input for an empty string too', () => {
    // `value` is `string | null` on the wire and `''` is a legal string; both are "no value".
    render(fieldsPane({ fields: [mkField({ name: 'buyer_tin', value: '' })] }))

    expect(inputOf('buyer_tin'), 'the empty field renders no input').toBeTruthy()
    expect(valueOf('buyer_tin')).toBe('')
  })

  it('renders a present value verbatim in the input, never as text beside it', () => {
    // The control needle for the two rows above: an input that always reads '' passes both.
    render(fieldsPane())

    expect(valueOf('total'), 'the wire value did not reach the input').toBe('1,250,000.00')
    expect(
      within(row('total')).queryByText('1,250,000.00'),
      'the value renders as TEXT beside the input, so the cell shows it twice',
    ).toBeNull()
  })
})

// ==========================================================================================
// Selection (AC-4, AC-6)
// ==========================================================================================

describe('selection', () => {
  it('reports the clicked field name upward, once', () => {
    const onSelect = vi.fn()
    render(fieldsPane({ onSelect }))

    fireEvent.click(row('issue_date'))

    expect(onSelect.mock.calls).toEqual([['issue_date']])
  })

  it('reports again when the already-selected row is clicked', () => {
    // AC-4 makes no exception for the selected row. `ExtractionCanvas.tsx`'s scroll effect
    // keys on `[selected, jobId]`, so a second report of the same name re-scrolls nothing
    // once EXTR-11-07 wires the two — that is -07's gap, and this row is the pane's half.
    const onSelect = vi.fn()
    render(fieldsPane({ selected: 'total', onSelect }))

    fireEvent.click(row('total'))

    expect(onSelect.mock.calls, 'the selected row swallowed its own click').toEqual([['total']])
  })

  it('keeps a region-less row selectable', () => {
    // D-21. Round 2 made this row `disabled` and round 3 followed the brief instead:
    // "Focusing a field we could not find highlights nothing and says so." A disabled
    // <button> swallows fireEvent.click, so the call count below is that decision's oracle.
    const onSelect = vi.fn()
    render(fieldsPane({ fields: ONE_REGIONLESS, onSelect }))

    const r = row('invoice_number')
    expect(r.style.pointerEvents, 'the region-less row is not clickable').not.toBe('none')
    expect(r.getAttribute('aria-disabled')).not.toBe('true')

    fireEvent.click(r)

    expect(onSelect.mock.calls).toEqual([['invoice_number']])
  })

  it('marks exactly one row selected, and moves the mark', () => {
    const { rerender } = render(fieldsPane({ selected: 'invoice_number' }))

    expect(rows(), 'no row rendered — every count below would be vacuous').toHaveLength(3)
    expect(selectedIds()).toEqual(['extraction-field-invoice_number'])

    rerender(fieldsPane({ selected: 'total' }))
    expect(selectedIds()).toEqual(['extraction-field-total'])
  })

  it('makes the selection visible, not merely semantic', () => {
    // AC-6 says the selected row carries a "treatment". The artboard's own focus value
    // (`var(--accent)` on `f.border`, `:614`/`:656`) is declared on an <input> this story
    // does not build — editing is EXTR-12's — so no colour is pinned here. What IS pinned:
    // two rows of identical shape must not render identical style once one is selected.
    render(fieldsPane({ selected: 'issue_date' }))

    const selected = row('issue_date')
    const other = row('total')
    expect(selected.getAttribute('aria-current')).toBe('true')
    expect(other.getAttribute('aria-current')).toBe('false')
    expect(
      selected.getAttribute('style'),
      'the selected row renders exactly like an unselected one — aria-pressed alone is not a treatment',
    ).not.toBe(other.getAttribute('style'))
  })

  it('marks nothing when the selection names no rendered field', () => {
    // The window between two valid states: EXTR-11-07 clears `selected` on a jobId change,
    // and for one render the old name is held against the new wire. Zero is the honest
    // answer; an index- or position-based match lights the wrong row here.
    render(fieldsPane({ selected: 'buyer_name' }))

    expect(rows(), 'no row rendered — the zero below would be vacuous').toHaveLength(3)
    expect(selectedIds()).toEqual([])
  })

  it('carries no radius-forcing class on any element in the pane', () => {
    // EVERY_CELL_PART, not THREE_FIELDS: the old fixture carried no ambiguous field and no
    // corrected one, so the walk never saw a chip row or an Undo button — the two elements the
    // artboard writes as `.pf-chip` and `.pf-btn`, which is the most likely thing to be copied
    // in. A guard whose fixture cannot produce the thing it forbids reports clear either way.
    render(fieldsPane({ fields: EVERY_CELL_PART, selected: 'total', armed: 'buyer_tin', canPoint: true }))
    expectEveryPartRendered()

    const all = [pane(), ...Array.from(pane().querySelectorAll<HTMLElement>('*'))]
    expect(all.length, 'the walk read an empty tree').toBeGreaterThan(10)

    for (const el of all) {
      for (const forced of RADIUS_FORCING) {
        expect(classesOf(el), `${el.dataset.testid ?? el.tagName} carries ${forced}`).not.toContain(forced)
      }
    }
  })
})

// ==========================================================================================
// The NO REGION pill (AC-5)
// ==========================================================================================

describe('the region-less pill', () => {
  it('renders the pill on the region-less row, and no title anywhere in the pane', () => {
    render(fieldsPane({ fields: ONE_REGIONLESS }))

    expect(within(row('invoice_number')).getByText(PILL), 'the region-less row carries no pill').toBeTruthy()

    // A `title=` never fires for a keyboard or screen-reader user and is invisible to
    // Chromium's accessibility tree — two QA passes missed exactly that on APPR-16.
    expect(pane().querySelectorAll('[title]'), 'the pane hides a reason in a tooltip').toHaveLength(0)

    // The control needle: the same query finds a planted node, so the zero above is a real
    // absence and not a selector that matches nothing.
    const probe = document.createElement('span')
    probe.setAttribute('title', 'probe')
    pane().appendChild(probe)
    expect(pane().querySelectorAll('[title]'), 'the [title] selector is inert').toHaveLength(1)
    probe.remove()
  })

  it('gives the pill the resolved artboard values, not the RulePills ones', () => {
    render(fieldsPane({ fields: ONE_REGIONLESS }))

    const pill = within(row('invoice_number')).getByText(PILL)
    expect(classesOf(pill)).toContain('mono')

    // `:296`, every declaration.
    expect(pill.style.fontSize).toBe('8.5px')
    expect(pill.style.fontWeight).toBe('700')
    expect(pill.style.letterSpacing).toBe('0.07em')
    expect(pill.style.color).toBe('var(--status-amber-text)')
    expect(pill.style.background).toBe('var(--status-amber-bg)')
    expect(pill.style.border).toBe('1px solid var(--status-amber-border)')
    expect(pill.style.borderRadius).toBe('999px')
    expect(pill.style.padding).toBe('2px 8px')
    expect(pill.style.whiteSpace).toBe('nowrap')

    // C-3. RulePills.tsx:7-22 carries the identical amber triple and two different font
    // metrics, so a copy of that idiom fails on exactly these two lines and nothing else.
    expect(pill.style.fontSize, 'the pill copied RulePills.tsx:19').not.toBe(RULE_PILL_FONT_SIZE)
    expect(pill.style.letterSpacing, 'the pill copied RulePills.tsx:19').not.toBe(RULE_PILL_LETTER_SPACING)
  })

  it('leaves a row that has a region without a pill', () => {
    // The control needle for both rows above: a pill rendered unconditionally passes them.
    render(fieldsPane({ fields: ONE_REGIONLESS }))

    expect(within(row('invoice_number')).getByText(PILL), 'the floor: this fixture really has a pill').toBeTruthy()
    expect(within(row('total')).queryByText(PILL)).toBeNull()
    expect(within(row('issue_date')).queryByText(PILL)).toBeNull()
  })
})

// ==========================================================================================
// The empty pane (AC-7)
// ==========================================================================================

describe('an empty fields array', () => {
  it('draws the dashed panel over a transparent ground', () => {
    render(fieldsPane({ fields: [] }))

    expect(rows(), 'a row rendered for an empty wire').toHaveLength(0)

    // MemberParts.tsx:55-57 states the rule the copy must satisfy: `1px dashed var(--line-3)`
    // over a TRANSPARENT ground, never over a fill.
    const panel = dashedPanel()
    expect(panel.style.border).toBe('1px dashed var(--line-3)')
    expect(panel.style.background).toBe('transparent')

    // CreateForm.tsx:132 and ValidationView.tsx:148 both carry `pf-chip`, whose
    // `border-radius: var(--radius-pill) !important` (app-layer.css:275) overrides their own
    // inline radius. Copying either brings the override with it.
    for (const forced of RADIUS_FORCING) {
      expect(classesOf(panel), `the empty panel carries ${forced}`).not.toContain(forced)
    }
  })

  it('keeps the pane header and its scrollable body', () => {
    // The transition an early return drops: `if (fields.length === 0) return <Panel/>` loses
    // the header and the scroll container for the one state where the pane says least.
    render(fieldsPane({ fields: [] }))

    const { header, body } = paneParts()
    expect(within(header).getByText(TITLE), 'the empty pane lost its header').toBeTruthy()
    expect(body.style.overflowY).toBe('auto')
    expect(body.contains(dashedPanel()), 'the dashed panel is outside the scrollable body').toBe(true)
  })
})

// ==========================================================================================
// The supplier sentence (AC-8)
// ==========================================================================================

describe('the client-record sentence', () => {
  it('renders once under the pair, as prose', () => {
    render(fieldsPane({ fields: [mkField({ name: 'supplier_tin', value: '20184412-0001', region: null }), ...THREE_FIELDS] }))

    const note = screen.getByText(SENTENCE)

    // Prose, not a pill. The row's own NO REGION pill is a different element — this fixture
    // gives supplier_tin no region precisely so the two cannot be confused.
    expect(within(row('supplier_tin')).getByText(PILL), 'the floor: a pill really is on this row').toBeTruthy()
    expect(note, 'the sentence resolved to the pill').not.toBe(within(row('supplier_tin')).getByText(PILL))
    expect(note.style.background).toBe('')
    expect(note.style.border).toBe('')
    expect(note.style.borderRadius).toBe('')
    expect(classesOf(note), 'a mono note is a pill wearing prose').not.toContain('mono')

    // The artboard's own prose slot, `:330` — the same 11.5px / --fg-2 its two other
    // paragraphs use (`:166`, `:183`).
    expect(note.style.fontSize).toBe('11.5px')
    expect(note.style.color).toBe('var(--fg-2)')

    // It names no verdict and compares no values: the two supplier strings must not appear
    // inside it.
    expect(note.textContent).not.toContain('20184412-0001')

    // UNDER the pair, in document order. A sentence pinned to the top of the pane explains
    // fields the reader has not reached yet, and this fixture puts supplier_tin first so a
    // top-of-pane note would otherwise pass every assertion above.
    expect(
      row('supplier_tin').compareDocumentPosition(note) & Node.DOCUMENT_POSITION_FOLLOWING,
      'the sentence renders above the pair it explains',
    ).toBeTruthy()
  })

  it('renders for supplier_name alone', () => {
    // AC-8 is "supplier_tin OR supplier_name". A pane keyed on the TIN alone passes the row
    // above and fails here.
    render(fieldsPane({ fields: [mkField({ name: 'supplier_name', value: 'Sahel Freight Systems Limited' }), ...THREE_FIELDS] }))

    expect(screen.getAllByText(SENTENCE)).toHaveLength(1)
  })

  it('renders once for the pair, not once per field', () => {
    render(
      fieldsPane({
        fields: [
          mkField({ name: 'supplier_tin', value: '20184412-0001' }),
          mkField({ name: 'supplier_name', value: 'Sahel Freight Systems Limited' }),
          ...THREE_FIELDS,
        ],
      }),
    )

    expect(rows(), 'no row rendered — the count below would be vacuous').toHaveLength(5)
    expect(screen.getAllByText(SENTENCE), 'the sentence renders per supplier field, not per pair').toHaveLength(1)

    // The position row above runs with supplier_tin alone. With BOTH present the sentence has
    // two rows to sit under, and must still follow the later of them.
    const note = screen.getByText(SENTENCE)
    for (const name of ['supplier_tin', 'supplier_name']) {
      expect(
        row(name).compareDocumentPosition(note) & Node.DOCUMENT_POSITION_FOLLOWING,
        `the sentence renders above ${name}`,
      ).toBeTruthy()
    }
  })

  it('stays away when the wire carries neither supplier field', () => {
    // The control needle: a sentence rendered unconditionally passes all three rows above.
    render(fieldsPane())

    expect(rowIds(), 'no row rendered — the zero below would be vacuous').toEqual(WIRE_ORDER)
    expect(screen.queryAllByText(SENTENCE)).toHaveLength(0)
    expect(pane().textContent, 'a paraphrase of the sentence survived').not.toContain('client record')
  })
})

// ==========================================================================================
// What this story does not render (AC-9)
// ==========================================================================================

describe('the vocabulary EXTR-12 owns', () => {
  it('renders no reason, no alternative and no percentage', () => {
    // The wire object carries the keys EXTR-12 will add. A pane that spreads its field onto
    // the DOM, or renders a confidence, fails here; nothing else can.
    // The alternatives are REAL ExtractionCandidate objects, not bare strings behind a cast: a
    // chip rendering `{a.value}` renders nothing for a string, so the absence below would have
    // passed whatever the pane did with them. The reason is `unreadable`, NOT `ambiguous`, so a
    // gate keyed on the reason renders no chip here and the candidate value must stay off the
    // screen — which makes this sweep a second oracle for that gate.
    const leaky = THREE_FIELDS.map((f) => ({
      ...f,
      reason: 'unreadable',
      reason_code: 'LOW_CONFIDENCE',
      alternatives: [mkCandidate('SFS-2026-0418', 3)],
      confidence: 0.62,
    })) as unknown as ExtractionFieldState[]

    render(fieldsPane({ fields: leaky }))

    // The positive floor FIRST: without it this row passes on a render that produced nothing.
    // Read through `fieldLabel` — the raw name stops rendering once the cell is relabelled,
    // and this floor, not the needle list below, is what a relabelling breaks.
    for (const f of THREE_FIELDS) {
      expect(
        screen.queryByText(fieldLabel(f.name)),
        `${f.name} did not render its label — every absence below is vacuous`,
      ).toBeTruthy()
      expect(valueOf(f.name), `${f.name}'s value did not reach its input`).toBe(f.value)
    }

    const text = pane().textContent ?? ''
    for (const leaked of ['unreadable', 'LOW_CONFIDENCE', 'SFS-2026-0418', '0.62', '62', '%']) {
      expect(text, `the pane rendered "${leaked}"`).not.toContain(leaked)
    }
  })
})

// ==========================================================================================
// T11 (task-813). line_items[N].role belongs to LineItemGrid, not this pane -- a line-item
// name must never reach `extraction-field-*`, the same prefix EXTR11-E2E-02a counts on the
// deploy gate.
// ==========================================================================================

describe('the line-item field filter (EXTR-13-07)', () => {
  it('renders no line-item cell once the wire carries them', () => {
    const header = tenFields()
    const blockRow = mkField({ name: 'line_items', value: null, region: null })
    const lineCells = [1, 2].flatMap((i) =>
      LINE_ROLES.map((role) => mkField({ name: lineFieldName(i, role), value: '1.00' })),
    )

    render(fieldsPane({ fields: [...header, blockRow, ...lineCells] }))

    // The floor: the fixture really carries line-item fields, so the exclusion below excludes
    // something rather than nothing.
    expect(lineCells.length, 'the fixture carries no line cell to exclude').toBeGreaterThan(0)

    expect(rowIds().slice().sort(), 'a line-item name reached the header prefix').toEqual(
      HEADER_FIELDS.map((name) => `extraction-field-${name}`).sort(),
    )
  })
})

// ==========================================================================================
// Mode B. The rows above were proven against a throwaway reference build; every row below
// was proven against the SHIPPED component, and each names the mutant it kills.
// ==========================================================================================

// The pane's own empty-state copy.
const NO_FIELDS = 'Nothing was extracted from this document.'

describe('the cell is reachable by keyboard', () => {
  it('holds at least one natively focusable control, and focusing it selects that field', () => {
    // This replaces the deleted `tagName === 'BUTTON'` row and is strictly stronger: that one
    // asserted a tag and nothing a key did. The cell is now a <div> holding an input and,
    // depending on state, buttons — so the keyboard contract has to be carried by the controls,
    // and each of them must select its own field when focus reaches it.
    //
    // ARMED, over a fixture carrying a `missing` field. `tenFields()` builds every cell with
    // the default reason, so the point button never rendered here and the `disabled` clause
    // below iterated controls that could not include it — the guard could not produce the
    // thing it forbids. Measured: adding `disabled` to the point button passed this walk.
    const onSelect = vi.fn()
    const fields: ExtractionFieldState[] = tenFields().map((f) =>
      f.name === 'buyer_tin' ? mkField({ name: f.name, value: null, region: null, reason: 'missing' }) : f,
    )
    render(fieldsPane({ fields, armed: 'buyer_tin', canPoint: true, onSelect }))

    expect(rows(), 'no row rendered — every check below is vacuous').toHaveLength(HEADER_FIELDS.length)
    // The floor the walk was missing: both new controls are on screen, so the sweep below
    // really did reach them.
    expect(pointOf('buyer_tin'), 'no point button rendered — the disabled clause cannot see it').toBeTruthy()
    expect(cancelOf('buyer_tin'), 'no Stop pointing button rendered — the disabled clause cannot see it').toBeTruthy()
    for (const name of HEADER_FIELDS) {
      const controls = controlsOf(name)
      expect(controls.length, `${name} holds nothing a keyboard can reach`).toBeGreaterThan(0)
      for (const c of controls) {
        expect((c as HTMLInputElement | HTMLButtonElement).disabled, `${name}'s control leaves the tab order`).toBe(false)
      }

      onSelect.mockClear()
      fireEvent.focus(controls[0])
      expect(onSelect.mock.calls, `focusing ${name}'s control did not select it`).toEqual([[name]])
    }
  })

  it('renders the cell as a plain container, not a button wrapping other buttons', () => {
    // A <button> may not contain interactive content, so the chip row and Undo would be invalid
    // markup inside one — and `aria-pressed` is only valid on a button role, which is why the
    // selection moved to `aria-current`.
    render(fieldsPane({ fields: EVERY_CELL_PART, selected: 'total', armed: 'buyer_tin', canPoint: true }))
    expectEveryPartRendered()

    for (const r of rows()) {
      expect(r.tagName, `${r.dataset.testid} is still a button and now wraps interactive content`).not.toBe('BUTTON')
    }
  })

  it('keeps each cell announcing its own label beside its own control', () => {
    // What the deleted accessible-name row protected: the label and the value belong to the
    // same cell. `getAllByRole('button')` cannot state it any more — the cell is not a button,
    // and an <input>'s value never joins an ancestor's accessible name.
    render(fieldsPane())

    for (const f of THREE_FIELDS) {
      const r = row(f.name)
      expect(within(r).queryByText(fieldLabel(f.name)), `${f.name} does not carry its own label`).toBeTruthy()
      expect(valueOf(f.name), `${f.name}'s value is not in its own cell`).toBe(f.value)
      expect(r.contains(inputOf(f.name)), `${f.name}'s input rendered outside its cell`).toBe(true)
    }
  })
})

describe('a long or hostile value', () => {
  it('declares both halves of the defence against a value widening its column', () => {
    // A grid item's automatic minimum is CONTENT-based: without `min-width: 0` on the cell
    // AND a wrap rule on the value, one unbroken string pushes the track past the pane.
    // jsdom computes no layout, so this row pins the two declarations and nothing more —
    // EXTR11-E2E-02a measures the relationship itself, on the deployed build.
    const long = 'A'.repeat(400)
    render(fieldsPane({ fields: [mkField({ name: 'buyer_name', value: long })] }))

    const r = row('buyer_name')
    expect(r.style.minWidth, 'the cell takes a content-based automatic minimum').toBe('0')

    // An <input> cannot `overflow-wrap`, so the claim changes shape: the control fills its cell
    // through `.pf-input`'s own `width: 100%` (platform.css) and declares no width of its own,
    // which is what keeps the grid track free to shrink (`AA-11`).
    const input = inputOf('buyer_name')
    expect(input, 'the long value renders in no input').toBeTruthy()
    expect(classesOf(input as HTMLElement), 'the input does not fill its cell').toContain('pf-input')
    expect(input!.style.width, 'an inline width overrides the cell’s own minimum').toBe('')
    expect(input!.value, 'the value was truncated or ellipsised').toBe(long)
  })

  it('renders a value as text, never as markup', () => {
    // The value is read out of a document someone uploaded. A `dangerouslySetInnerHTML` here
    // passes every row above and turns that document into a script host.
    const hostile = '<img src=x onerror="alert(1)"> & <b>bold</b>'
    render(fieldsPane({ fields: [mkField({ name: 'buyer_name', value: hostile })] }))

    const r = row('buyer_name')
    expect(valueOf('buyer_name'), 'the value did not reach the input verbatim').toBe(hostile)
    expect(r.querySelectorAll('img, b'), 'the value was parsed as markup').toHaveLength(0)
  })
})

describe('the pane and the sentence, pinned', () => {
  it('keys the supplier sentence on the two field names, never on a prefix', () => {
    // `f.name.includes('supplier')` passes all four supplier rows above, because THREE_FIELDS
    // carries no supplier-shaped name at all. AC-8 names two fields; a third `supplier_*`
    // field is not the pair the sentence explains.
    render(fieldsPane({ fields: [mkField({ name: 'supplier_address', value: '14 Marina, Lagos' }), ...THREE_FIELDS] }))

    expect(row('supplier_address'), 'the floor: the supplier-shaped field really rendered').toBeTruthy()
    expect(screen.queryAllByText(SENTENCE), 'a prefix match leaked the sentence').toHaveLength(0)
  })

  it('paints the selected row in the app’s marked-row amber', () => {
    // `SourceDocumentSheet.tsx:317`, in the hue the document pane paints the region with —
    // `--accent` is `oklch(72% .15 65)` (colors.css:18), `highlightStyle` the same triple at
    // .32 alpha. The style-inequality row above passes on ANY difference, `opacity: 0.99`
    // included, so it pins the fact of a treatment and this row pins which one.
    render(fieldsPane({ selected: 'issue_date' }))

    const selected = row('issue_date')
    expect(selected.style.background).toBe('var(--accent-10)')
    expect(selected.style.boxShadow).toBe('inset 2px 0 0 var(--accent)')

    const other = row('total')
    expect(other.style.background, 'an unselected row carries the marked treatment').toBe('transparent')
    expect(other.style.boxShadow, 'an unselected row carries the marked rail').toBe('')
  })

  it('sizes the pane the way the artboard’s right column does', () => {
    // `:224`. EXTR-11-07 builds the flex row around this pane and sets nothing on it, so the
    // basis and the floor live here — and `min-width: 470px` is the relationship
    // EXTR11-E2E-02a's spill sweep exists to protect.
    render(fieldsPane())

    const p = pane()
    expect(p.style.width).toBe('620px')
    expect(p.style.flex).toBe('1 1 620px')
    expect(p.style.minWidth).toBe('470px')
    expect(p.style.display).toBe('flex')
    expect(p.style.flexDirection).toBe('column')
    expect(p.style.background).toBe('var(--bg-1)')
  })

  it('says why the empty panel is empty, at the sibling panel’s declarations', () => {
    // The panel's border and ground are pinned above, its COPY is not — so a panel rendering
    // nothing passes both empty-state rows. This is the one state where the sentence is the
    // pane's whole content.
    render(fieldsPane({ fields: [] }))

    const panel = dashedPanel()
    expect(panel.textContent).toBe(NO_FIELDS)

    // The story's "match it": `ExtractionCanvas.tsx`'s EMPTY_PANEL, shipped on this same
    // screen, declares these six. The two are byte-identical copies with no shared source,
    // and only `border` and `background` were pinned on either side — so the other four
    // could drift apart silently. Pinned by value, not by import: the panes share no edge.
    expect(panel.style.padding).toBe('14px 16px')
    expect(panel.style.borderRadius).toBe('var(--radius-md)')
    expect(panel.style.fontSize).toBe('12.5px')
    expect(panel.style.color).toBe('var(--fg-3)')
  })
})

// ==========================================================================================
// EXTR-12-06. The reason pill, the per-field note and the corrected marker. Written RED
// against stubs that return a fixed wrong value, so every row below fails on its own
// assertion rather than on a missing export.
// ==========================================================================================

const CORRECTED_TOTAL: ExtractionCorrected = { method: 'typed', was: '1,000,000.00', where: null }

describe('the reason pill', () => {
  it('renders each reason code as its own copy-table string, in its own cell', () => {
    // Every field here carries a region (mkField's default), so NO REGION cannot supply a
    // false positive in the one slot both pills compete for.
    const fields = [
      mkField({ name: 'vat', value: '271,950.00', reason: 'unreadable' }),
      mkField({ name: 'issue_date', value: '2026-08-12', reason: 'ambiguous' }),
      mkField({ name: 'subtotal', value: '3,626,000.00', reason: 'inconsistent' }),
      mkField({ name: 'buyer_tin', value: '31775208-0003', reason: 'missing' }),
    ]
    const pills: Record<string, string> = {
      vat: PILL_UNREADABLE,
      issue_date: PILL_AMBIGUOUS,
      subtotal: PILL_INCONSISTENT,
      buyer_tin: PILL_MISSING,
    }
    render(fieldsPane({ fields }))

    for (const f of fields) {
      const r = row(f.name)
      expect(valueOf(f.name), `${f.name} did not render — its pill row is vacuous`).toBe(f.value)
      expect(within(r).queryByText(pills[f.name]), `${f.name} renders no "${pills[f.name]}" pill`).toBeTruthy()

      // within(row) is load-bearing: a pane that renders all four pills once, anywhere, passes
      // a bare getByText and fails here.
      for (const other of Object.values(pills)) {
        if (other === pills[f.name]) continue
        expect(within(r).queryByText(other), `${f.name} also renders "${other}"`).toBeNull()
      }
    }

    const text = pane().textContent ?? ''
    for (const code of ['unreadable', 'ambiguous', 'inconsistent', 'missing']) {
      expect(text, `the pane rendered the raw reason code "${code}"`).not.toContain(code)
    }
  })

  it('gives the slot to the reason, and keeps NO REGION for a field with no reason', () => {
    // `Y-1`. Both pills declare `white-space: nowrap`; two of them in a strip inside a 470px
    // pane is how that floor gets broken, so the reason wins and NO REGION is the fallback.
    const fields = [
      mkField({ name: 'buyer_tin', value: null, region: null, reason: 'missing' }),
      mkField({ name: 'invoice_number', value: 'INV-2026-0037', region: null }),
    ]
    render(fieldsPane({ fields }))

    const flagged = row('buyer_tin')
    expect(within(flagged).queryByText(PILL_MISSING), 'a region-less flagged row renders no reason pill').toBeTruthy()
    expect(within(flagged).queryByText(PILL), 'the label strip carries both pills at once').toBeNull()

    // The fallback survives where it is the only thing the slot can carry — the shipped
    // EXTR-11 cue, kept rather than removed.
    expect(within(row('invoice_number')).queryByText(PILL), 'a clean region-less row lost NO REGION').toBeTruthy()
  })
})

describe('the per-field note', () => {
  it('renders three different notes for three inconsistent fields, each in its own cell', () => {
    // `Reconcile` reuses one `inconsistent` code for the line-sum check and the entity match,
    // so the NOTE is what tells AC-4 and AC-8 apart.
    const fields = [
      mkField({ name: 'subtotal', value: '3,726,000.00', reason: 'inconsistent' }),
      mkField({ name: 'supplier_tin', value: '20184412-0001', reason: 'inconsistent' }),
      mkField({ name: 'total', value: '4,005,450.00', reason: 'inconsistent' }),
    ]
    const notes: [string, string][] = [
      ['subtotal', NOTE_SUBTOTAL],
      ['supplier_tin', NOTE_SUPPLIER],
      ['total', NOTE_GENERIC],
    ]
    render(fieldsPane({ fields }))

    for (const [name, note] of notes) {
      const r = row(name)
      const value = fields.find((f) => f.name === name)!.value as string
      expect(valueOf(name), `${name} did not render — its note row is vacuous`).toBe(value)
      expect(within(r).queryByText(note), `${name} renders no note of its own`).toBeTruthy()

      // A pane that renders one note below the grid — the shipped SUPPLIER_NOTE shape — puts
      // all three outside the rows and fails here.
      for (const [, other] of notes) {
        if (other === note) continue
        expect(within(r).queryByText(other), `${name} also renders another field's note`).toBeNull()
      }
    }

    const note = within(row('subtotal')).queryByText(NOTE_SUBTOTAL)!
    // A block <span>, not the artboard's <p>: flow content is invalid inside a <button>.
    expect(note.style.display, 'the note is not block-level phrasing content').toBe('block')
    expect(note.style.fontSize).toBe('11.5px')
    expect(note.style.color).toBe('var(--fg-2)')

    // The copy table's "both may show at once", asserted rather than assumed.
    const sentence = screen.queryAllByText(SENTENCE)
    expect(sentence, 'the unconditional client-record sentence stopped rendering').toHaveLength(1)
    expect(
      sentence[0],
      "supplier_tin's own note resolved to the pane-level sentence",
    ).not.toBe(within(row('supplier_tin')).queryByText(NOTE_SUPPLIER))
  })
})

describe('a corrected field', () => {
  it('renders the marker, its label and its was-line — and drops the pill and the note', () => {
    // DELIBERATELY IMPOSSIBLE FIXTURE. The server empties `reason` on a corrected field, so a
    // realistic fixture carries `reason: ''` and both absence clauses below would pass against
    // a component that suppresses nothing. The contradiction IS the oracle — do not "fix" it.
    const fields = [
      mkField({
        name: 'subtotal',
        value: '3,726,000.00',
        reason: 'inconsistent',
        corrected: { method: 'chosen', was: '3,626,000.00', where: null },
      }),
    ]
    render(fieldsPane({ fields }))

    const r = row('subtotal')
    expect(valueOf('subtotal'), 'the row did not render — every clause below is vacuous').toBe('3,726,000.00')

    const marker = screen.queryByTestId('extraction-marker-subtotal')
    expect(marker, 'the corrected field renders no marker').toBeTruthy()
    expect(r.contains(marker), 'the marker sits outside the cell it settles').toBe(true)

    expect(within(r).queryByText(MARKER_CHOSEN), `the corrected field does not say "${MARKER_CHOSEN}"`).toBeTruthy()
    expect(within(r).queryByText(WAS_CHOSEN), 'the corrected field states no provenance').toBeTruthy()

    expect(within(r).queryByText(PILL_INCONSISTENT), 'a settled field still shouts its reason pill').toBeNull()
    expect(within(r).queryByText(NOTE_SUBTOTAL), 'a settled field still shouts its note').toBeNull()
  })

  it('reads the pointed provenance off the field’s own region', () => {
    // The main path, not an edge: the shipped UI sends no anchor label, so `where` is null on
    // every pointed correction and the phrase is derived from the region. Two pages, neither
    // of them 1 and neither shared — a cell handed `null` renders no was-line at all, and one
    // handed its neighbour's region renders the wrong page. Both pass every other row here.
    const pointed: ExtractionCorrected = { method: 'pointed', was: null, where: null }
    const fields = [
      mkField({ name: 'issue_date', value: '2026-08-12', region: mkRegion({ page: 3 }), corrected: pointed }),
      mkField({ name: 'total', value: '2,222.00', region: mkRegion({ page: 5 }), corrected: pointed }),
    ]
    render(fieldsPane({ fields }))

    for (const [name, page] of [
      ['issue_date', 3],
      ['total', 5],
    ] as const) {
      const r = row(name)
      expect(within(r).queryByText(MARKER_POINTED), `${name} did not render as pointed`).toBeTruthy()
      expect(within(r).queryByText(`Taken from page ${page}`), `${name} states no provenance`).toBeTruthy()
    }
    expect(
      within(row('issue_date')).queryByText('Taken from page 5'),
      'the cell read another field’s region',
    ).toBeNull()
  })

  it('drops the region cue as well as the reason pill', () => {
    // Reachable, unlike the contradictory fixture above: mock.go gives total, subtotal and
    // buyer_tin no region, and mergeCorrections leaves the region alone on a typed correction,
    // so all three settle with `region: null`. The artboard's pill slot is empty on a settled
    // field (`:646` gates it on `flagged`), and NO REGION rides the same slot.
    const fields = [
      mkField({ name: 'total', value: '2,222.00', region: null, corrected: CORRECTED_TOTAL }),
      mkField({ name: 'invoice_number', value: 'INV-2026-0037', region: null }),
    ]
    render(fieldsPane({ fields }))

    const r = row('total')
    expect(within(r).queryByText(MARKER_TYPED), 'the row did not settle — the absence below is vacuous').toBeTruthy()
    expect(valueOf('total'), 'the settled row did not render its value').toBe('2,222.00')
    expect(within(r).queryByText(PILL), 'a settled field still carries the region cue').toBeNull()

    // Unchanged where nothing is settled.
    expect(within(row('invoice_number')).queryByText(PILL), 'a clean region-less row lost NO REGION').toBeTruthy()
  })

  it('paints the marker and the changed label in --action, never --accent', () => {
    render(fieldsPane({ fields: [mkField({ name: 'total', value: '2,222.00', corrected: CORRECTED_TOTAL })] }))

    const marker = screen.queryByTestId('extraction-marker-total')
    expect(marker, 'the corrected field renders no marker').toBeTruthy()
    const label = within(row('total')).queryByText(MARKER_TYPED)
    expect(label, `the corrected field does not say "${MARKER_TYPED}"`).toBeTruthy()

    // The positive equality FIRST: a bare not.toBe('var(--accent)') is green on 'red', on ''
    // and on an element that never rendered. app-layer.css:38-43 states the translation the
    // artboard's `var(--accent)` at :307 and :335 takes here.
    expect(marker!.style.background).toBe('var(--action)')
    expect(label!.style.color).toBe('var(--action)')
    expect(marker!.style.background, 'the marker transcribed the artboard token literally').not.toBe('var(--accent)')
    expect(label!.style.color, 'the changed label transcribed the artboard token literally').not.toBe('var(--accent)')
  })

  it('seats the marker on a positioned value control, inset from its right edge', () => {
    // jsdom computes no layout, so this row pins the declarations and EXTR12-E2E-02 measures
    // the relationship on the deployed build. The wrapper is the contract with subtask 07:
    // 07 swaps the value span inside it for a full-width .pf-input and the marker's box is
    // unchanged, so AC-7's e2e never has to be retargeted.
    const fields = [mkField({ name: 'total', value: '2,222.00', corrected: CORRECTED_TOTAL }), THREE_FIELDS[1]]
    render(fieldsPane({ fields }))

    const control = screen.queryByTestId('extraction-control-total')
    expect(control, 'the cell renders no value control for the marker to sit in').toBeTruthy()
    expect(control!.style.position, 'an absolutely-positioned marker escapes an unpositioned wrapper').toBe('relative')
    expect(control!.style.display).toBe('flex')
    expect(control!.style.alignItems).toBe('center')
    expect(control!.contains(inputOf('total')), 'the value input moved out of the control').toBe(true)
    expect(valueOf('total'), 'the value moved out of the control').toBe('2,222.00')

    const marker = screen.queryByTestId('extraction-marker-total')
    expect(marker, 'the corrected field renders no marker').toBeTruthy()
    expect(control!.contains(marker), 'the marker is not inside the control the e2e measures it against').toBe(true)
    expect(marker!.style.position).toBe('absolute')
    expect(marker!.style.right).toBe('11px')
    expect(marker!.style.left, 'a left inset puts the marker on the selection rule').toBe('')
    expect(marker!.style.width).toBe('7px')
    expect(marker!.style.height).toBe('7px')
    expect(marker!.style.borderRadius).toBe('2px')
    expect(marker!.style.pointerEvents).toBe('none')

    // Unconditional: every cell gets the wrapper, so subtask 07's input has one to fill and
    // this testid does not appear and disappear with a correction.
    expect(
      screen.queryByTestId('extraction-control-invoice_number'),
      'an uncorrected cell has no value control',
    ).toBeTruthy()
    expect(screen.queryByTestId('extraction-marker-invoice_number'), 'an uncorrected cell carries a marker').toBeNull()
  })
})

// One field per pill, one per note arm, one ambiguous WITH real candidates, two locked and one
// settled: every element EXTR-12 added to a cell is on screen at once, so a walk over the cells
// reads all of them. `issue_date` carries alternatives because without them the fixture renders
// no chip row, and `total` is corrected because without it there is no Undo — the two elements
// the radius walk and the width walk exist to catch.
const EVERY_CELL_PART: ExtractionFieldState[] = [
  mkField({ name: 'vat', value: '271,950.00', reason: 'unreadable' }),
  mkField({
    name: 'issue_date',
    value: '2026-08-12',
    reason: 'ambiguous',
    alternatives: [mkCandidate('2026-08-21', 3), mkCandidate('2026-12-08', 5)],
  }),
  mkField({ name: 'subtotal', value: '3,626,000.00', reason: 'inconsistent' }),
  mkField({ name: 'supplier_tin', value: '20184412-0001', reason: 'inconsistent' }),
  mkField({ name: 'buyer_tin', value: null, region: null, reason: 'missing' }),
  mkField({ name: 'invoice_number', value: 'SFS-2026-0418', region: null }),
  mkField({ name: 'total', value: '3,897,950.00', corrected: CORRECTED_TOTAL }),
]

/** Every element in the fixture's cells: the cell itself, then its whole subtree. */
function cellParts(): HTMLElement[] {
  const cells = rows()
  expect(cells.length, 'the walk read a pane with no cells').toBe(EVERY_CELL_PART.length)
  return cells.flatMap((c) => [c, ...Array.from(c.querySelectorAll<HTMLElement>('*'))])
}

/**
 * The floor for every walk below: each part EXTR-12 added really rendered. Without the chip,
 * input and Undo clauses the walks iterate elements that were never on screen and report clear
 * — the same vacuity as a fixture that cannot produce what the guard forbids.
 */
function expectEveryPartRendered() {
  expect(within(row('vat')).queryByText(PILL_UNREADABLE), 'no reason pill rendered').toBeTruthy()
  expect(within(row('invoice_number')).queryByText(PILL), 'no region cue rendered').toBeTruthy()
  expect(within(row('subtotal')).queryByText(NOTE_SUBTOTAL), 'no per-field note rendered').toBeTruthy()
  expect(screen.queryByTestId('extraction-control-total'), 'no value control rendered').toBeTruthy()
  expect(screen.queryByTestId('extraction-marker-total'), 'no marker rendered').toBeTruthy()
  expect(within(row('total')).queryByText(MARKER_TYPED), 'no changed row rendered').toBeTruthy()
  expect(within(row('total')).queryByText('We read 1,000,000.00'), 'no was-line rendered').toBeTruthy()

  // EXTR-12-07's own three.
  expect(chipsOf('issue_date').length, 'no chip row rendered').toBeGreaterThan(1)
  expect(inputOf('subtotal'), 'no editable input rendered').toBeTruthy()
  expect(inputOf('invoice_number'), 'no locked input rendered').toBeTruthy()
  expect(undoOf('total'), 'no Undo button rendered').toBeTruthy()
  expect(within(row('invoice_number')).queryByText(INVOICE_NUMBER_LOCKED), 'no lock reason rendered').toBeTruthy()

  // EXTR-12-08's two. Without them every walk below iterates a pane that never rendered the
  // point button at all and reports clear — the same vacuity, on the one element 07 did not
  // build. The fixture is armed on `buyer_tin` for exactly this reason.
  expect(pointOf('buyer_tin'), 'no point button rendered').toBeTruthy()
  expect(cancelOf('buyer_tin'), 'no Stop pointing button rendered').toBeTruthy()
}

describe('the cell EXTR-12 grew', () => {
  it('declares no width on anything it added, so the pane keeps its own floor', () => {
    // AC-8's other half. The pane's basis and floor are pinned by the row above; this one says
    // nothing inside a cell can override them. `min-width: 0` on the cell is what lets a grid
    // track shrink below its content, and a `width` or `min-width` on any descendant undoes
    // that — silently, because jsdom computes no layout and no other row reads these.
    render(fieldsPane({ fields: EVERY_CELL_PART, selected: 'total', armed: 'buyer_tin', canPoint: true }))
    expectEveryPartRendered()

    let checked = 0
    for (const el of cellParts()) {
      const id = el.dataset.testid ?? `<${el.tagName.toLowerCase()}>`
      // Out of flow: an absolutely positioned box contributes nothing to an ancestor's
      // intrinsic width, which is why the marker may carry one. Its geometry is pinned above.
      if (el.style.position === 'absolute') continue
      checked += 1
      expect(el.style.width, `${id} declares a width`).toBe('')
      // The cell's own `min-width: 0` is the defence, not a violation of it.
      expect(el.style.minWidth, `${id} declares a width floor`).toMatch(/^(0(px)?)?$/)
      // jsdom expands the `flex` shorthand, so this reads `flex: 1 1 300px` too.
      expect(el.style.flexBasis, `${id} declares a flex basis`).toMatch(/^(auto|0(px|%)?)?$/)
    }
    expect(checked, 'every element claimed to be out of flow').toBeGreaterThan(EVERY_CELL_PART.length)
  })

  it('hides no reason in a tooltip on any control it added', () => {
    // The two shipped [title] walks render neither a chip nor an Undo: one fixture carries no
    // ambiguous field and no corrected one, the other neither. A `title` on the Undo button --
    // the control most likely to attract one -- was covered by nothing. A `title` never fires
    // for a keyboard or screen-reader user and is invisible to Chromium's accessibility tree.
    render(fieldsPane({ fields: EVERY_CELL_PART, selected: 'total', armed: 'buyer_tin', canPoint: true }))
    expectEveryPartRendered()

    expect(pane().querySelectorAll('[title]'), 'a control in the pane hides its reason in a tooltip').toHaveLength(0)

    // The control needle: the same query finds a planted node, so the zero above is a real
    // absence and not a selector that matches nothing.
    const probe = document.createElement('span')
    probe.setAttribute('title', 'probe')
    row('total').appendChild(probe)
    expect(pane().querySelectorAll('[title]'), 'the [title] selector is inert').toHaveLength(1)
    probe.remove()
  })

  it('puts no new testid inside the shipped extraction-field- prefix', () => {
    // `rows()` here, the same walk in ExtractionReview.test.tsx and EXTR11-E2E-02a on the
    // deploy gate all count `[data-testid^="extraction-field-"]` against the wire's field
    // count. A CHILD handle inside that prefix reads as an extra row on all three, and only
    // the gate would say so — which is why the new handles are `extraction-control-*` and
    // `extraction-marker-*`.
    render(fieldsPane({ fields: EVERY_CELL_PART, selected: 'total', armed: 'buyer_tin', canPoint: true }))
    expectEveryPartRendered()

    expect(rowIds().slice().sort(), 'a handle inside the prefix reads as an extra row').toEqual(
      EVERY_CELL_PART.map((f) => `extraction-field-${f.name}`).sort(),
    )
  })
})

describe('the pane renders nothing it does not declare', () => {
  it('leaves nothing over once the wire and its own copy are stripped', () => {
    // AC-9's row above hunts named needles — `%`, `0.62`, a reason code. A confidence written
    // as PROSE ("sixty two percent confident") slips past every one of them. This row inverts
    // the test: strip the wire and the pane's three declared sentences, and what is left of
    // the rendered text must be nothing at all.
    // AC-1's oracle, and the reason no `\d+%` / `0.\d\d` needle is added beside it: a needle
    // regex is weaker AND false-positives on a legitimate money value like 0.75. The fixture
    // carries one field per reason code, one region-less clean field and one corrected field,
    // so every string EXTR-12 taught this pane to say is on screen at once.
    // Three DIFFERENT pages across the chip row, so each sub-label is its own string: the strip
    // below replaces one occurrence per entry, and three chips reading `page 1` would leave two
    // behind and red this row for a reason it is not about.
    const fields = [
      mkField({ name: 'invoice_number', value: 'SFS-2026-0418', region: null }),
      mkField({ name: 'supplier_tin', value: '20184412-0001', reason: 'inconsistent' }),
      mkField({ name: 'vat', value: '271,950.00', reason: 'unreadable' }),
      mkField({
        name: 'issue_date',
        value: '2026-08-12',
        reason: 'ambiguous',
        alternatives: [mkCandidate('2026-08-21', 3), mkCandidate('2026-12-08', 5)],
      }),
      mkField({ name: 'buyer_tin', value: null, reason: 'missing' }),
      mkField({
        name: 'total',
        value: '3,897,950.00',
        corrected: { method: 'typed', was: '3,879,950.00', where: null },
      }),
    ]
    render(fieldsPane({ fields, selected: 'total' }))

    // A CHIP's value is text and stays in the sweep; an INPUT's value is not in textContent at
    // all, so it moves to the per-row floor below rather than out of the guard altogether.
    const chipText = ['2026-08-12', '2026-08-21', '2026-12-08', 'page 1', 'page 3', 'page 5']

    const known = [
      TITLE,
      SENTENCE,
      PILL,
      PILL_UNREADABLE,
      PILL_AMBIGUOUS,
      PILL_INCONSISTENT,
      PILL_MISSING,
      NOTE_SUPPLIER,
      MARKER_TYPED,
      'We read 3,879,950.00',
      UNDO,
      INVOICE_NUMBER_LOCKED,
      // This fixture is UNARMED, so the missing cell can only ever reach the idle label. The
      // armed and cancel strings are swept one cell at a time, in the armed row below.
      POINT_IDLE,
      ...chipText,
      ...fields.map((f) => fieldLabel(f.name)),
      // EXTR-13-07: LineItemGrid mounts unconditionally whenever the pane is populated
      // (fields.length > 0), so its declared copy joins this sweep too -- with no line-item
      // fields on this fixture it renders its own empty state.
      LINE_ITEM_EMPTY_1,
      LINE_ITEM_EMPTY_2,
      LINE_ITEM_ADD,
    ]

    let left = pane().textContent ?? ''
    // The floor first: every one really rendered, so the emptiness below is a real absence.
    for (const k of known) expect(left, `${k} did not render`).toContain(k)

    // The other half of the floor, at the property the browser now holds these values in. The
    // ambiguous field renders chips instead of an input, so it is read off the chip row above.
    for (const f of fields) {
      if (f.reason === 'ambiguous') continue
      expect(valueOf(f.name), `${f.name}'s value left the pane entirely`).toBe(f.value ?? '')
    }

    // Longest first, so a value that is a substring of another cannot eat it.
    for (const k of [...known].sort((a, b) => b.length - a.length)) left = left.replace(k, '')

    expect(left.replace(/\s+/g, ''), `the pane rendered copy it does not declare: ${JSON.stringify(left)}`).toBe('')
  })
})

// ==========================================================================================
// EXTR-12-07. The candidate chips, the typed-over input, the lock and Undo. Written RED
// against the SHIPPED cell, which renders a value <span> and no control at all — so every row
// below fails on the control it is about, not on a missing export.
// ==========================================================================================

describe('an ambiguous field', () => {
  it('renders one chip per candidate and no input, and its neighbour keeps its input', () => {
    // The DECIDED reading is itself a chip (`W-3`): on the deployed mock issue_date decides
    // 2026-01-01 and carries two lower-ranked alternatives, so "alternatives only" would hide
    // the reading the extractor ranked highest and leave a user who agrees with it no way to
    // say so — with no input to type it back into either.
    const fields = [
      mkField({
        name: 'issue_date',
        value: '2026-01-01',
        reason: 'ambiguous',
        alternatives: [mkCandidate('2026-01-10', 3), mkCandidate('2026-10-01', 5)],
      }),
      mkField({ name: 'vat', value: '271,950.00', reason: 'unreadable' }),
    ]
    render(fieldsPane({ fields }))

    const chips = chipsOf('issue_date')
    expect(chips.map((c) => c.textContent), 'the ambiguous field renders no chip row').toHaveLength(3)
    for (const [i, want] of ['2026-01-01', '2026-01-10', '2026-10-01'].entries()) {
      expect(chips[i].textContent, `chip ${i} does not carry candidate ${want}`).toContain(want)
    }
    expect(inputOf('issue_date'), 'the ambiguous field renders a chip row AND an input').toBeNull()

    // The `vat` clause is what stops "render no input anywhere" passing the absence above: an
    // absence asserted over an empty render proves nothing.
    expect(inputOf('vat'), 'the neighbour lost its input — the absence above is vacuous').toBeTruthy()

    // The chip's sub-label. `regionPhrase` supplies it, so the chip and the pointed was-line
    // cannot drift; the artboard renders it uppercase, which is CSS and not a second string —
    // `textContent` stays the phrase the copy sweep declares.
    for (const [i, page] of ['page 1', 'page 3', 'page 5'].entries()) {
      expect(chips[i].textContent, `chip ${i} states no page`).toContain(page)
    }
    const sub = within(chips[0]).getByText('page 1')
    expect(sub.style.textTransform, 'the chip sub-label was uppercased in JavaScript, not in CSS').toBe('uppercase')
  })

  it('renders no chip for a field carrying alternatives without the ambiguous reason', () => {
    // THE GATE. An implementation keyed on `alternatives.length > 0` passes the row above and
    // fails here — and would also red the AC-9 sweep, whose fixture is `unreadable` and carries
    // a real candidate. Both must be green together.
    const fields = [
      mkField({
        name: 'subtotal',
        value: '3,626,000.00',
        reason: 'inconsistent',
        alternatives: [mkCandidate('3,726,000.00', 3), mkCandidate('3,526,000.00', 5)],
      }),
    ]
    render(fieldsPane({ fields }))

    expect(inputOf('subtotal'), 'the floor: an inconsistent field still takes an input').toBeTruthy()
    expect(chipsOf('subtotal'), 'the chip row is gated on the array, not on the reason').toHaveLength(0)
    expect(pane().textContent, 'a lower-ranked candidate reached the screen').not.toContain('3,726,000.00')
  })

  it('marks the reading the invoice already holds when nothing is drafted', () => {
    // W-3 put the decided reading on the row as chip 0, and a chip's POSITION is not a signal a
    // reader can see. With nothing drafted the pane rendered three unmarked chips and withheld
    // which of them the invoice actually holds -- and an ambiguous cell renders no input, so
    // there is nowhere else to read it from.
    //
    // Index 0 and the value match cannot be told apart HERE, because chip 0 is built from the
    // wire's own value. The row below is the other half: a chosen draft moves the mark off
    // chip 0, so the mark is not pinned to a position.
    const fields = [
      mkField({
        name: 'issue_date',
        value: '2026-01-01',
        reason: 'ambiguous',
        alternatives: [mkCandidate('2026-01-10', 3), mkCandidate('2026-10-01', 5)],
      }),
    ]
    render(fieldsPane({ fields }))

    const chips = chipsOf('issue_date')
    expect(chips, 'no chip rendered -- every claim below is vacuous').toHaveLength(3)
    expect(
      chips.map((c) => c.getAttribute('aria-current')),
      'the pane will not say which of the three readings the invoice holds',
    ).toEqual(['true', 'false', 'false'])
    expect(
      chips[0].style.border,
      'the reading the invoice holds renders exactly like the two it was ranked above',
    ).toBe('1px solid var(--action)')
  })

  it('marks the chip the draft holds, and only when the draft chose it', () => {
    // An ambiguous cell renders NO input, so the chip's own mark is the only thing on screen
    // that says a candidate was picked — the value does not move, and Save's enabled state is
    // outside the pane. A chip that never marks itself leaves the whole gesture invisible until
    // the write lands, which is the claim W-1 was reversed on.
    const fields = [
      mkField({
        name: 'issue_date',
        value: '2026-01-01',
        reason: 'ambiguous',
        alternatives: [mkCandidate('2026-01-10', 3), mkCandidate('2026-10-01', 5)],
      }),
    ]
    const chosen: DraftEntries = { issue_date: { kind: 'chosen', value: '2026-10-01', region: null } }
    render(fieldsPane({ fields, draft: chosen }))

    const chips = chipsOf('issue_date')
    expect(chips, 'no chip rendered — every claim below is vacuous').toHaveLength(3)
    expect(
      chips.map((c) => c.getAttribute('aria-current')),
      'the mark is on a chip the draft did not choose, or on all of them',
    ).toEqual(['false', 'false', 'true'])

    // Not merely semantic: the artboard's `a.border` hook, in the app layer's own teal.
    expect(chips[2].style.border, 'the picked chip renders exactly like an unpicked one').toBe('1px solid var(--action)')
    expect(chips[0].style.border, 'every chip renders as picked').toBe('1px solid var(--line-2)')

    // The gate is the KIND, not the value: a typed draft that happens to carry a candidate's
    // text has chosen nothing, and a chip claiming otherwise would say the person picked it.
    // The mark falls back to the wire's own reading rather than disappearing -- the pane always
    // says which value is filed.
    cleanup()
    const typed: DraftEntries = { issue_date: { kind: 'typed', value: '2026-10-01', region: null } }
    render(fieldsPane({ fields, draft: typed }))
    expect(
      chipsOf('issue_date').map((c) => c.getAttribute('aria-current')),
      'a typed draft marked the chip carrying its text as though the person had chosen it',
    ).toEqual(['true', 'false', 'false'])
  })

  it('reports the chosen candidate upward, and writes no correction of its own', () => {
    const onChoose = vi.fn()
    const fields = [
      mkField({
        name: 'issue_date',
        value: '2026-01-01',
        reason: 'ambiguous',
        alternatives: [mkCandidate('2026-01-10', 3), mkCandidate('2026-10-01', 5)],
      }),
    ]
    render(fieldsPane({ fields, onChoose }))

    const chips = chipsOf('issue_date')
    expect(chips, 'no chip rendered — the click below is vacuous').toHaveLength(3)

    fireEvent.click(chips[2])

    // The candidate, not its index and not the field's own value: the shell needs the box to
    // move the highlight to, and the LAST chip is deliberately not the first alternative.
    expect(onChoose.mock.calls, 'the chip click reported nothing upward').toEqual([
      ['issue_date', fields[0].alternatives[1]],
    ])
  })
})

describe('what a field may be typed over with', () => {
  it('gives seven fields an editable input and the three locked ones a readOnly one', () => {
    // `lockedFields` refuses invoice_number, supplier_tin and supplier_name with a 422, and
    // `invoiceEditFor` writes only the other seven columns. An implementation that locks
    // invoice_number alone answers 9/1 and ships two inputs whose Save 422s — on supplier_tin,
    // which the deployed mock renders. The NAMES are asserted because a bare count of 3 passes
    // on the wrong three.
    render(fieldsPane({ fields: tenFields() }))

    const inputs = HEADER_FIELDS.map((name) => inputOf(name))
    expect(inputs.filter(Boolean), 'the pane renders no inputs at all').toHaveLength(HEADER_FIELDS.length)

    const locked = HEADER_FIELDS.filter((name) => inputOf(name)!.readOnly)
    const editable = HEADER_FIELDS.filter((name) => !inputOf(name)!.readOnly)
    expect(locked, 'the pane offers an edit the server refuses with a 422').toEqual(LOCKED_FIELDS)
    expect(editable, 'the pane locked a field the invoice accepts').toHaveLength(7)

    // readOnly, never disabled: a disabled input leaves the tab order and fires no focus, so
    // the three locked cells would become unreachable and unselectable by keyboard.
    for (const name of LOCKED_FIELDS) {
      const input = inputOf(name)!
      expect(input.disabled, `${name} left the tab order`).toBe(false)
      expect(input.getAttribute('aria-readonly'), `${name} does not announce itself read-only`).toBe('true')
      expect(classesOf(input), `${name} is not the shipped input`).toContain('pf-input')
    }
  })

  it('reports typed text upward without redrawing the value itself', () => {
    const onType = vi.fn()
    render(fieldsPane({ fields: [mkField({ name: 'total', value: '1,250,000.00' })] }))
    const input = inputOf('total')
    expect(input, 'nothing to type into').toBeTruthy()

    cleanup()
    render(fieldsPane({ fields: [mkField({ name: 'total', value: '1,250,000.00' })], onType }))
    fireEvent.change(inputOf('total') as HTMLInputElement, { target: { value: '9,999.00' } })

    // The pane holds no draft of its own: the shell owns the write, exactly as ExpandedFixPanel
    // holds the draft above FixCardView.
    expect(onType.mock.calls, 'the typed text never reached the shell').toEqual([['total', '9,999.00']])
  })

  it('states the invoice-number lock in text, and hides no reason in a tooltip', () => {
    render(fieldsPane({ fields: tenFields() }))

    const r = row('invoice_number')
    expect(within(r).queryByText(INVOICE_NUMBER_LOCKED), 'the lock states no reason').toBeTruthy()

    // A `title=` implementation reds TWICE: the text is absent, and the [title] count is 1.
    expect(pane().querySelectorAll('[title]'), 'the pane hides a reason in a tooltip').toHaveLength(0)

    // The two supplier fields take the pane-level client-record sentence instead, which already
    // ships and already says why they are locked — no second string is invented for them.
    expect(screen.getAllByText(SENTENCE), 'the supplier lock lost its explanation').toHaveLength(1)
    for (const name of ['supplier_tin', 'supplier_name']) {
      expect(
        within(row(name)).queryByText(INVOICE_NUMBER_LOCKED),
        `${name} borrowed the invoice number's reason`,
      ).toBeNull()
    }
  })
})

describe('Undo', () => {
  it('renders on a corrected field and nowhere else, and reports upward', () => {
    const onUndo = vi.fn()
    const fields = [
      mkField({ name: 'total', value: '2,222.00', corrected: CORRECTED_TOTAL }),
      mkField({ name: 'subtotal', value: '950.00' }),
    ]
    render(fieldsPane({ fields, onUndo }))

    const undo = undoOf('total')
    expect(undo, 'the corrected field offers no way back').toBeTruthy()
    expect(undo!.textContent, 'the Undo control is unlabelled').toBe(UNDO)
    expect(row('total').contains(undo), 'Undo rendered outside the cell it undoes').toBe(true)
    expect(undoOf('subtotal'), 'an uncorrected field offers an Undo').toBeNull()

    fireEvent.click(undo as HTMLElement)
    expect(onUndo.mock.calls, 'the Undo click reported nothing upward').toEqual([['total']])
  })
})

describe('selection after the restructure', () => {
  it('is aria-current, and no element in the pane carries aria-pressed', () => {
    // `aria-pressed` is valid only on a button role, and the cell now holds an input and
    // buttons. Scoped to this pane on purpose: screen-wide the negative would red
    // `extraction-zoom-100`, which is EXTR-11's shipped contract.
    render(fieldsPane({ fields: EVERY_CELL_PART, selected: 'total', armed: 'buyer_tin', canPoint: true }))
    expectEveryPartRendered()

    expect(row('total').getAttribute('aria-current')).toBe('true')
    expect(row('vat').getAttribute('aria-current')).toBe('false')
    expect(pane().querySelectorAll('[aria-pressed]'), 'a half-finished migration left aria-pressed behind').toHaveLength(0)

    // The control needle: the same query finds a planted node, so the zero above is a real
    // absence and not a selector that matches nothing.
    const probe = document.createElement('span')
    probe.setAttribute('aria-pressed', 'true')
    pane().appendChild(probe)
    expect(pane().querySelectorAll('[aria-pressed]'), 'the [aria-pressed] selector is inert').toHaveLength(1)
    probe.remove()
  })

  it('selects its OWN field from a cell click, an input focus and a chip click', () => {
    // IDENTITY, never a count. On the deployed build Playwright's centre-click lands on the
    // 38px input, so the browser fires `focus` AND a bubbled `click` and one gesture reports
    // twice; jsdom's fireEvent dispatches neither for the other, so a count oracle would be
    // green here and false on the gate. The duplicate is harmless — `scrollRegionIntoView` is
    // a direct scrollTop assignment and recomputes the same coordinates — so what matters is
    // that no control ever selects a field other than the one it belongs to.
    const onSelect = vi.fn()
    const onChoose = vi.fn()
    const fields = [
      mkField({ name: 'total', value: '2,222.00' }),
      mkField({
        name: 'issue_date',
        value: '2026-01-01',
        reason: 'ambiguous',
        alternatives: [mkCandidate('2026-01-10', 3), mkCandidate('2026-10-01', 5)],
      }),
    ]
    render(fieldsPane({ fields, onSelect, onChoose }))

    const input = inputOf('total')
    expect(input, 'the cell renders no input — the focus gesture below is vacuous').toBeTruthy()

    const named = (): string[] => Array.from(new Set(onSelect.mock.calls.map((c) => c[0] as string)))

    for (const [what, gesture] of [
      ['the cell click', () => fireEvent.click(row('total'))],
      ['the input focus', () => fireEvent.focus(input as HTMLElement)],
    ] as const) {
      onSelect.mockClear()
      gesture()
      expect(onSelect.mock.calls.length, `${what} reported nothing upward`).toBeGreaterThan(0)
      expect(named(), `${what} selected a field other than its own`).toEqual(['total'])
    }

    onSelect.mockClear()
    const chips = chipsOf('issue_date')
    expect(chips.length, 'no chip rendered — the click below is vacuous').toBeGreaterThan(0)
    fireEvent.click(chips[0])

    expect(onSelect.mock.calls.length, 'the chip click reported nothing upward').toBeGreaterThan(0)
    expect(named(), 'the chip selected another field, or the cell it sits in swallowed it').toEqual(['issue_date'])
    expect(onChoose.mock.calls, 'the chip click produced no draft write').toHaveLength(1)
  })
})

// ==========================================================================================
// EXTR-12-08. The point button, its two states and its cancel. Written RED against the
// SHIPPED cell, which renders no button at all — so every row fails on the control it is
// about. The four walks above were armed in the same commit and grew a floor for it.
// ==========================================================================================

// `buyer_tin` is the deployed mock's one `missing` field: a nil value and a nil region.
const TWO_MISSING: ExtractionFieldState[] = [
  mkField({ name: 'buyer_tin', value: null, region: null, reason: 'missing' }),
  mkField({ name: 'buyer_name', value: null, region: null, reason: 'missing' }),
]

/** The button's own three inline declarations — the ones AC-6 says all move together. */
function tripleOf(el: HTMLElement): { border: string; background: string; color: string } {
  return { border: el.style.border, background: el.style.background, color: el.style.color }
}

describe('a missing field', () => {
  it('offers both an input and the point button', () => {
    // A build following the artboard literally reds the FIRST clause: its `isInput` is
    // `st !== 'missing' && st !== 'ambiguous'`, so it renders no input on a missing field —
    // which EXTR-12-07 overruled. This row is what stops 08 undoing it, and the pane's own
    // per-row value floor reds beside it.
    render(fieldsPane({ fields: TWO_MISSING, canPoint: true, armed: null }))

    expect(inputOf('buyer_tin'), 'a missing field can no longer be typed into').toBeTruthy()
    const button = pointOf('buyer_tin')
    expect(button, 'a missing field offers no way to point at it').toBeTruthy()
    expect(button!.textContent, 'the point button is unlabelled or paraphrased').toBe(POINT_IDLE)
    expect(row('buyer_tin').contains(button), 'the point button rendered outside the cell it arms').toBe(true)
  })

  it('moves the border, the ground and the label together when it arms', () => {
    // The DIFF is the oracle, not a snapshot: a single-state assertion passes on a build that
    // never changes. "Without hover" has its oracle in the READING — every value comes off
    // `el.style.*`, the inline declaration, and the button carries no class, so no stylesheet
    // rule could supply it.
    const { rerender } = render(fieldsPane({ fields: TWO_MISSING, canPoint: true, armed: null }))
    const idleButton = pointOf('buyer_tin')
    expect(idleButton, 'no point button — the diff below is vacuous').toBeTruthy()
    const idle = tripleOf(idleButton!)
    const idleLabel = idleButton!.textContent

    rerender(fieldsPane({ fields: TWO_MISSING, canPoint: true, armed: 'buyer_tin' }))
    const armedButton = pointOf('buyer_tin')
    expect(armedButton, 'the armed cell lost its point button').toBeTruthy()
    const on = tripleOf(armedButton!)

    // `--ds-amber` does not exist in this repository; the DS amber IS `--accent`.
    expect(on.border, 'the armed border is not the amber dash').toBe('1.5px dashed var(--accent)')
    expect(on.background, 'the armed ground is not the amber fill').toBe('var(--status-amber-bg)')
    expect(on.color, 'the armed label is not the amber text').toBe('var(--status-amber-text)')
    expect(idle.border, 'the idle border is not the artboard dash').toBe('1.5px dashed var(--line-3)')
    expect(idle.background, 'the idle button paints a ground').toBe('transparent')
    expect(idle.color, 'the idle label is not the muted foreground').toBe('var(--fg-2)')

    for (const key of ['border', 'background', 'color'] as const) {
      expect(on[key], `arming left ${key} exactly where it was, so the state is invisible`).not.toBe(idle[key])
    }
    expect(idleLabel, 'the idle label is not the copy table’s').toBe(POINT_IDLE)
    expect(armedButton!.textContent, 'arming did not change what the button says').toBe(POINT_ARMED)
  })

  it('offers Stop pointing only while armed, and cancels with it', () => {
    // Always-absent reds the second half, always-present the first. The `onArm` clause catches
    // a cancel wired to the wrong callback.
    const onArm = vi.fn()
    const onDisarm = vi.fn()
    const { rerender } = render(fieldsPane({ fields: TWO_MISSING, canPoint: true, armed: null, onArm, onDisarm }))
    expect(cancelOf('buyer_tin'), 'an unarmed cell offers a way to stop pointing').toBeNull()

    rerender(fieldsPane({ fields: TWO_MISSING, canPoint: true, armed: 'buyer_tin', onArm, onDisarm }))
    const cancel = cancelOf('buyer_tin')
    expect(cancel, 'the armed cell offers no way out').toBeTruthy()
    expect(cancel!.textContent, 'the cancel control is unlabelled').toBe(POINT_CANCEL)

    fireEvent.click(cancel as HTMLElement)
    expect(onDisarm.mock.calls, 'Stop pointing disarmed nothing').toHaveLength(1)
    expect(onArm.mock.calls, 'Stop pointing armed the field it was meant to release').toEqual([])
  })

  it('offers typing, not pointing, on a job with no pages', () => {
    // BOTH clauses are needed: a build with the right label that still arms passes the first
    // alone. The `onSelect` clause pins the artboard's own docx fallback — the button still
    // selects — and the input clause is what makes "type it in" true.
    const onArm = vi.fn()
    const onSelect = vi.fn()
    render(fieldsPane({ fields: TWO_MISSING, canPoint: false, armed: null, onArm, onSelect }))

    const button = pointOf('buyer_tin')
    expect(button, 'the pageless cell renders no control at all').toBeTruthy()
    expect(button!.textContent, 'a document with no pages still asks to be pointed at').toBe(POINT_PAGELESS)
    expect(button!.textContent, 'the pageless label is the pointing one').not.toBe(POINT_IDLE)
    expect(inputOf('buyer_tin'), '"type it in" and there is nothing to type into').toBeTruthy()

    fireEvent.click(button as HTMLElement)
    expect(onArm.mock.calls, 'a document with no pages armed a gesture nothing can complete').toEqual([])
    expect(onSelect.mock.calls, 'the pageless button stopped selecting its own field').toEqual([['buyer_tin']])
  })

  it('says nothing the copy table does not carry while it is armed', () => {
    // The ONLY sweep that ever sees POINT_ARMED and POINT_CANCEL: the pane-wide residue sweep
    // renders unarmed, so it can only reach the idle label. Scoped to ONE cell so the shipped
    // fixture is untouched and no second NOT FOUND pill can defeat the one-occurrence strip.
    render(fieldsPane({ fields: TWO_MISSING, canPoint: true, armed: 'buyer_tin' }))

    const known = [fieldLabel('buyer_tin'), PILL_MISSING, POINT_ARMED, POINT_CANCEL]
    let left = row('buyer_tin').textContent ?? ''

    // The floor first, so the emptiness below is a real absence.
    for (const k of known) expect(left, `${k} did not render`).toContain(k)

    for (const k of [...known].sort((a, b) => b.length - a.length)) left = left.replace(k, '')
    expect(left.replace(/\s+/g, ''), `the armed cell rendered copy it does not declare: ${JSON.stringify(left)}`).toBe(
      '',
    )
  })
})

// ==========================================================================================
// EXTR-12-08, QA. Two affordances on the point button that nothing read: the only accessible
// signal that a field is armed, and the glyph a whole new export was added for. Both were
// found by deleting them from the shipped component and watching all 3639 tests pass.
// ==========================================================================================

/** One glyph's rendered markup, read off a probe rather than retyped as a path string. */
function glyphMarkup(node: ReactNode): string {
  const probe = render(<span data-testid="glyph-probe">{node}</span>)
  const html = probe.getByTestId('glyph-probe').innerHTML
  probe.unmount()
  return html
}

describe('the point button, as an affordance', () => {
  it('announces the armed field to a screen reader, and only that field', () => {
    // `Stop pointing` and the amber triple are both SIGHTED signals; the cell's own
    // `aria-current` names the SELECTED row, not the armed one. This attribute is the only
    // thing that tells a screen-reader user which field is waiting for a box. Deleting it
    // passed the whole suite. `aria-current`, never `aria-pressed` -- the pane-wide negative
    // forbids the latter, and the row that enforces it sits above.
    const { rerender } = render(fieldsPane({ fields: TWO_MISSING, canPoint: true, armed: null }))

    expect(pointOf('buyer_tin')?.getAttribute('aria-current'), 'an unarmed field announces itself as current').toBe(
      'false',
    )

    rerender(fieldsPane({ fields: TWO_MISSING, canPoint: true, armed: 'buyer_tin' }))

    expect(pointOf('buyer_tin')?.getAttribute('aria-current'), 'the armed field announces nothing').toBe('true')
    // The neighbour is what makes it an identity and not a flag: both cells carry the
    // attribute, so a build setting it on every point button reds here.
    expect(pointOf('buyer_name')?.getAttribute('aria-current'), 'arming one field announced its neighbour too').toBe(
      'false',
    )
  })

  it('carries the crosshair, not the dismissal X', () => {
    // The repo's `crossGlyph` is an X used to dismiss things; rendering it here would put a
    // close icon on a control that OPENS a gesture, which is why `crosshairGlyph` was added.
    // Read off a probe, so this row cannot disagree with glyphs.tsx about a path -- and the
    // negative is its pair: it is what still reds if crosshairGlyph is edited into an X.
    render(fieldsPane({ fields: TWO_MISSING, canPoint: true, armed: null }))

    const button = pointOf('buyer_tin')
    expect(button, 'no point button rendered -- the glyph claim is vacuous').toBeTruthy()
    const svg = button!.querySelector('svg')
    expect(svg, 'the point button renders no glyph at all').toBeTruthy()

    expect(svg!.outerHTML, "the point button does not carry the artboard's crosshair").toBe(
      glyphMarkup(crosshairGlyph),
    )
    expect(svg!.outerHTML, 'the point button carries the dismissal X, which reads as "close"').not.toBe(
      glyphMarkup(crossGlyph),
    )
  })
})

// ==========================================================================================
// QA additions (Stage 4). T11 asserts only that no line name reaches this pane's prefix. A
// filter that dropped the line cells entirely would pass it, so this asserts the other half.
// ==========================================================================================

describe('the line-item field filter, both directions (EXTR-13-07)', () => {
  it('hands every line cell to the grid while the header rows stay put', () => {
    const header = tenFields()
    const lineCells = [1, 2, 4].flatMap((i) =>
      LINE_ROLES.map((role) => mkField({ name: lineFieldName(i, role), value: '1.00' })),
    )
    const onSelect = vi.fn()
    render(
      fieldsPane({ fields: [...header, mkField({ name: 'line_items', value: null, region: null }), ...lineCells], onSelect }),
    )

    // Both floors: the header half is populated and the line half is not empty.
    expect(rowIds().length, 'the header pane rendered nothing to keep').toBe(HEADER_FIELDS.length)
    expect(lineCells.length, 'the fixture carries no line cell to hand over').toBeGreaterThan(0)

    const rows = document.querySelectorAll('[data-testid^="line-item-row-"]')
    expect(rows, 'the filtered line cells reached neither pane').toHaveLength(3)
    expect(screen.queryByTestId('line-item-empty'), 'the grid claims an empty extraction while holding rows').toBeNull()

    // The wire's hole at index 3 closes in ordinal terms only -- the third row keeps index 4's
    // own name, which is what the document highlight resolves through.
    fireEvent.click(screen.getByTestId('line-item-cell-3-quantity'))
    expect(onSelect.mock.calls, "the third row renumbered itself instead of keeping wire index 4's name").toEqual([
      [lineFieldName(4, 'quantity')],
    ])
    expect(
      (screen.getByTestId('line-item-input-3-quantity') as HTMLInputElement).getAttribute('aria-label'),
      'the third row is not labelled by its ordinal',
    ).toBe('Line 3 quantity')
  })
})
