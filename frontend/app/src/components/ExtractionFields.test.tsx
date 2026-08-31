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

import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { ExtractionFieldState, ExtractionRegion } from '../lib/extractionReview'
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
const SENTENCE = 'The supplier we file comes from your client record, not from this document.'

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
  return { name: 'invoice_number', value: 'INV-2026-0037', region: mkRegion(), ...o }
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
  onSelect: (name: string) => void
}

function fieldsPane(over: Partial<FieldsProps> = {}) {
  const props: FieldsProps = {
    fields: THREE_FIELDS,
    selected: null,
    onSelect: () => {},
    ...over,
  }
  return <ExtractionFields {...props} />
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

function selectedIds(): string[] {
  return rows()
    .filter((el) => el.getAttribute('aria-pressed') === 'true')
    .map((el) => el.dataset.testid ?? '')
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
// One cell per wire field, in wire order (AC-2, AC-3)
// ==========================================================================================

describe('the field cells', () => {
  it('renders one row per wire field, in wire order', () => {
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

    // `:294`. The label IS the wire's field name: this story does not own the field
    // vocabulary (internal/extraction/vocabulary.go), and a display-name map would be the
    // same invented lookup the span map was rejected for.
    const label = within(row('invoice_number')).getByText('invoice_number')
    expect(label.style.fontSize).toBe('12px')
    expect(label.style.fontWeight).toBe('500')
    expect(label.style.color).toBe('var(--fg-2)')

    // The `min-height` is what stops a row without a pill sitting 18px shorter than its
    // neighbour with one — the reason the artboard declares it at all.
    const strip = label.parentElement as HTMLElement
    expect(strip.style.gap).toBe('8px')
    expect(strip.style.minHeight).toBe('18px')
  })

  it('renders an em-dash for a null value, never an empty cell', () => {
    render(fieldsPane({ fields: [mkField({ name: 'buyer_tin', value: null })] }))

    const r = row('buyer_tin')
    expect(within(r).getByText('—'), 'a null value rendered no em-dash').toBeTruthy()
    expect(r.textContent, 'a null value leaked a guess').not.toContain('null')
  })

  it('renders an em-dash for an empty string too', () => {
    // `value` is `string | null` on the wire, and `''` is a legal string. `{value ?? '—'}`
    // renders the em-dash for null and an EMPTY CELL for '' — which AC-3 forbids by name.
    render(fieldsPane({ fields: [mkField({ name: 'buyer_tin', value: '' })] }))

    expect(within(row('buyer_tin')).getByText('—'), 'an empty string rendered an empty cell').toBeTruthy()
  })

  it('renders a present value verbatim, with no em-dash riding along', () => {
    // The control needle for the two rows above: an unconditional em-dash passes both.
    render(fieldsPane())

    const r = row('total')
    expect(within(r).getByText('1,250,000.00'), 'the wire value did not render').toBeTruthy()
    expect(within(r).queryByText('—'), 'the em-dash is unconditional').toBeNull()
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
    expect(selected.getAttribute('aria-pressed')).toBe('true')
    expect(other.getAttribute('aria-pressed')).toBe('false')
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
    render(fieldsPane({ selected: 'total' }))

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
    const leaky = THREE_FIELDS.map((f) => ({
      ...f,
      reason: 'unreadable',
      reason_code: 'LOW_CONFIDENCE',
      alternatives: ['SFS-2026-0418'],
      confidence: 0.62,
    })) as unknown as ExtractionFieldState[]

    render(fieldsPane({ fields: leaky }))

    // The positive floor FIRST: without it this row passes on a render that produced nothing.
    for (const f of THREE_FIELDS) {
      expect(screen.getByText(f.name), `${f.name} did not render — every absence below is vacuous`).toBeTruthy()
      expect(screen.getByText(f.value as string), `${f.name}'s value did not render`).toBeTruthy()
    }

    const text = pane().textContent ?? ''
    for (const leaked of ['unreadable', 'LOW_CONFIDENCE', 'SFS-2026-0418', '0.62', '62', '%']) {
      expect(text, `the pane rendered "${leaked}"`).not.toContain(leaked)
    }
  })
})

// ==========================================================================================
// Mode B. The rows above were proven against a throwaway reference build; every row below
// was proven against the SHIPPED component, and each names the mutant it kills.
// ==========================================================================================

// The pane's own empty-state copy.
const NO_FIELDS = 'We read no fields from this document.'

describe('the row is a real button', () => {
  it('renders each cell as a <button type="button">, never a clickable div', () => {
    // `fireEvent.click` fires on a <div> too, so every selection row above passes on a cell
    // no keyboard can reach. The tag IS the keyboard contract: Tab, Enter and Space are the
    // UA's, and jsdom synthesises none of them, so there is nothing else here to assert.
    render(fieldsPane())

    expect(rows(), 'no row rendered — every check below is vacuous').toHaveLength(3)
    for (const r of rows()) {
      expect(r.tagName, `${r.dataset.testid} is not a button`).toBe('BUTTON')
      // Without `type`, a button inside a form defaults to `submit` and a row click posts it.
      expect((r as HTMLButtonElement).type, `${r.dataset.testid} takes the submit default`).toBe('button')
      expect((r as HTMLButtonElement).disabled, `${r.dataset.testid} is unreachable`).toBe(false)
    }
  })

  it('announces each row as its label and its value, not as an unnamed control', () => {
    // The accessible name is computed from the row's own contents. A value moved outside the
    // button still renders, still passes every row above, and leaves the control unnamed.
    render(fieldsPane())

    expect(screen.getAllByRole('button'), 'the rows carry no button role').toHaveLength(3)
    for (const f of THREE_FIELDS) {
      expect(
        screen.getByRole('button', { name: `${f.name} ${f.value as string}` }),
        `${f.name} does not announce its own value`,
      ).toBe(row(f.name))
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

    const value = within(r).getByText(long)
    expect(value.style.overflowWrap, 'a 400-character unbroken value cannot wrap').toBe('anywhere')
    expect(r.textContent, 'the value was truncated or ellipsised').toContain(long)
  })

  it('renders a value as text, never as markup', () => {
    // The value is read out of a document someone uploaded. A `dangerouslySetInnerHTML` here
    // passes every row above and turns that document into a script host.
    const hostile = '<img src=x onerror="alert(1)"> & <b>bold</b>'
    render(fieldsPane({ fields: [mkField({ name: 'buyer_name', value: hostile })] }))

    const r = row('buyer_name')
    expect(within(r).getByText(hostile), 'the value did not render verbatim').toBeTruthy()
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

describe('the pane renders nothing it does not declare', () => {
  it('leaves nothing over once the wire and its own copy are stripped', () => {
    // AC-9's row above hunts named needles — `%`, `0.62`, a reason code. A confidence written
    // as PROSE ("sixty two percent confident") slips past every one of them. This row inverts
    // the test: strip the wire and the pane's three declared sentences, and what is left of
    // the rendered text must be nothing at all.
    const fields = [mkField({ name: 'supplier_tin', value: '20184412-0001', region: null }), ...THREE_FIELDS]
    render(fieldsPane({ fields, selected: 'total' }))

    const known = [TITLE, SENTENCE, PILL, ...fields.map((f) => f.name), ...fields.map((f) => f.value as string)]

    let left = pane().textContent ?? ''
    // The floor first: every one really rendered, so the emptiness below is a real absence.
    for (const k of known) expect(left, `${k} did not render`).toContain(k)

    // Longest first, so a value that is a substring of another cannot eat it.
    for (const k of [...known].sort((a, b) => b.length - a.length)) left = left.replace(k, '')

    expect(left.replace(/\s+/g, ''), `the pane rendered copy it does not declare: ${JSON.stringify(left)}`).toBe('')
  })
})
