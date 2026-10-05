// @vitest-environment jsdom
// AIRB-01 (Test-first, AC-1) -- the SUGGESTED badge must render exactly like its AUTO and
// RESTORED siblings. Renders CreateMapping directly with a hand-built ctx (CreateFlow.test.tsx's
// createFlowCtx precedent), not a full App boot -- no fetch/XHR needed, since the group fixture
// is built with the real applySuggestion/initMappingFromHeaders rather than a wire round trip.

import { createElement } from 'react'

import { cleanup, render } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { ImportPreview, SuggestMapping } from '../lib/importApi'
import { initMappingFromHeaders } from '../lib/mapping'
import { applySuggestion, type MappingGroup } from '../lib/mappingGroups'
import { CANON } from '../data'
import type { PlatformCtx } from '../types'
import { CreateMapping } from './CreateMapping'

afterEach(() => cleanup())

// AIRS-04/AIRF-04's exact fixture (mappingGroups.test.ts): an AI answer that omits `vat`, whose
// alias column 'VAT' is free (badges AUTO by fallback), and places `subtotal` on 'Total' (badges
// SUGGESTED) -- the one pair AIR-07-09 made renderable together in one group. Deliberately NOT
// TWO_COL/THREE_COL/FOUR_COL/ROW1_COL/ROW3_COL from App.aiSuggestion.test.tsx: those alias-match
// nothing on purpose, so they can never produce an AUTO badge to compare against.
const COLS = ['Invoice No', 'Subtotal', 'Total', 'VAT']

function mkPreview(columns: string[]): ImportPreview {
  return {
    document_id: 'aaaaaaaa-0000-4000-8000-0000000000b1',
    format: 'csv',
    delimiter: ',',
    encoding: 'utf-8',
    columns,
    sample_rows: [columns.map((_, i) => `v${i}`)],
    rows_total: 1,
  }
}

function suggestedGroup(): MappingGroup {
  const seed: MappingGroup = {
    id: 'g-airb-01',
    signature: JSON.stringify(['X']),
    fileIds: ['f1'],
    preview: mkPreview(['X']),
    headerRow: 1,
    mapping: initMappingFromHeaders(['X']),
    restored: null,
    suggested: null,
  }
  const res: SuggestMapping = {
    source: 'ai',
    header_row: 1,
    columns: COLS,
    sample_rows: [['INV-1', '10', '11', '1']],
    rows_total: 1,
    mapping: { invoice_number: 'Invoice No', subtotal: 'Total' },
    saved_at: null,
  }
  return applySuggestion(seed, res)
}

function badgeCtx(group: MappingGroup, over: Record<string, unknown> = {}): PlatformCtx {
  const ctx = {
    active: { short: 'Lagos Freight' },
    preview: group.preview,
    mapping: group.mapping,
    armedField: null,
    dragField: null,
    run: { files: [], cursor: 0, status: 'idle' },
    importError: null,
    entityId: 'aaaaaaaa-0000-4000-8000-000000000001',
    groups: [group],
    groupIndex: 0,
    pickedFiles: [{ id: 'f1', file: new File(['x'], 'a.csv'), documentId: null }],
    setDrag: () => {},
    endDrag: () => {},
    armField: () => {},
    resetGroupToAutomatic: () => {},
    splitOutFile: () => {},
    dropOn: () => {},
    clickCol: () => {},
    unmap: () => {},
    backToImport: () => {},
    continueMapping: () => {},
    ...over,
  }
  return ctx as unknown as PlatformCtx
}

// The column whose header text a rendered `map-column` shows -- CreateMapping.tsx's own
// `div.mono` header cell, same idiom App.aiSuggestion.test.tsx's AIRA-03/05 use.
function columnByHeader(container: HTMLElement, header: string): HTMLElement {
  const columns = Array.from(container.querySelectorAll('[data-testid="map-column"]'))
  const col = columns.find((c) => c.querySelector('div.mono')?.textContent === header)
  expect(col, `control: a ${header} column must render`).not.toBeUndefined()
  return col as HTMLElement
}

describe('CreateMapping badges', () => {
  it('AIRB-01: the SUGGESTED badge matches its two siblings property for property', () => {
    const group = suggestedGroup()
    // Floor: both placements the fixture depends on must have survived applySuggestion,
    // or the AUTO/SUGGESTED columns below would not exist and the comparison is vacuous.
    expect(group.mapping.vat, 'control: vat must have fallen back to its free alias column').toBe('VAT')
    expect(group.mapping.subtotal, 'control: subtotal must carry the answer\'s own placement').toBe('Total')

    const { container } = render(createElement(CreateMapping, { ctx: badgeCtx(group) }))

    const vatCol = columnByHeader(container, 'VAT')
    const totalCol = columnByHeader(container, 'Total')

    // Scoped to the column grid, not the shield legend above it -- the legend also prints
    // the word AUTO, but only inside a `map-column` cell can it be the rendered badge.
    const autoBadges = Array.from(vatCol.querySelectorAll('span.mono')).filter((el) => el.textContent === 'AUTO')
    expect(autoBadges, 'control: exactly one AUTO badge must render on the VAT column').toHaveLength(1)
    const autoBadge = autoBadges[0] as HTMLElement

    // Column-scoped, not file-scoped: invoice_number ALSO badges suggested in this fixture
    // (it's part of the answer too), so a file-wide "exactly one SUGGESTED" count would be
    // wrong here -- do not "restore" that broader assertion.
    const suggestedBadges = Array.from(totalCol.querySelectorAll('[data-testid="map-suggested-badge"]'))
    expect(suggestedBadges, 'exactly one SUGGESTED badge must render on the Total column').toHaveLength(1)
    const suggestedBadge = suggestedBadges[0] as HTMLElement

    // Six properties byte-identical to AUTO/RESTORED (CreateMapping.tsx's shared badge shape).
    // Compared against AUTO's own resolved value, not a hardcoded literal, so a mutation that
    // drifts either badge's shared property is caught without depending on jsdom's own
    // normalization of a shorthand (e.g. `flex: 'none'` reads back as '0 0 auto').
    // AC-11 says the badge is a <span>: selecting it by testid cannot see the element type,
    // and only a `div.mono` swap reds AIRB-02, so compare the tag against AUTO's too.
    expect(suggestedBadge.tagName).toBe(autoBadge.tagName)
    expect(suggestedBadge.className).toBe(autoBadge.className)
    expect(suggestedBadge.style.flex).toBe(autoBadge.style.flex)
    expect(suggestedBadge.style.fontSize).toBe(autoBadge.style.fontSize)
    expect(suggestedBadge.style.fontWeight).toBe(autoBadge.style.fontWeight)
    expect(suggestedBadge.style.borderRadius).toBe(autoBadge.style.borderRadius)
    expect(suggestedBadge.style.padding).toBe(autoBadge.style.padding)

    // SUGGESTED alone: the amber token pair, and distinguishable from AUTO's green pair.
    expect(suggestedBadge.getAttribute('data-testid')).toBe('map-suggested-badge')
    expect(suggestedBadge.style.color).toBe('var(--status-amber-text)')
    expect(suggestedBadge.style.border).toBe('1px solid var(--status-amber-border)')
    expect(suggestedBadge.style.color).not.toBe(autoBadge.style.color)

    // D-7: the chip carrying the badge must also gain an amber arm, or the badge sits on a
    // chip still painted in the `--action` palette (the HAZARD's own mismatch).
    const chip = suggestedBadge.parentElement as HTMLElement
    expect(chip.style.background, 'control: the badge must sit inside its placement chip').toBe('var(--status-amber-bg)')
    expect(chip.style.border).toBe('1px solid var(--status-amber-border)')
  })
})

// RESKIN2-04-02 (D-9, D-10, D-42, D-44): the v2 look of the mapping step.
const NO_ENTITY_NOTE = 'Columns read. Filing is unavailable in this workspace'
const ARMED_NOTE = 'invoice_number is armed'
const DRAG_NOTE = 'Drag invoice_number onto a column to continue'

// Nothing placed: the palette holds every field, invoice_number unmapped.
function bareGroup(): MappingGroup {
  return {
    id: 'g-bare',
    signature: JSON.stringify(['X']),
    fileIds: ['f1'],
    preview: mkPreview(['X']),
    headerRow: 1,
    mapping: initMappingFromHeaders(['X']),
    restored: null,
    suggested: null,
  }
}

// Two files, one restored mapping: the notice and both "Map X separately" buttons render.
function restoredTwoFileGroup(): MappingGroup {
  const base = suggestedGroup()
  return { ...base, fileIds: ['f1', 'f2'], restored: { savedAt: '2026-01-02T03:04:05Z', mapping: base.mapping } }
}

const TWO_FILES = [
  { id: 'f1', file: new File(['x'], 'a.csv'), documentId: null },
  { id: 'f2', file: new File(['x'], 'b.csv'), documentId: null },
]

function failedRun() {
  return {
    files: [
      { id: 'f1', name: 'a.csv', groupId: 'g', outcome: { kind: 'failed', message: 'first failure' } },
      { id: 'f2', name: 'b.csv', groupId: 'g', outcome: { kind: 'failed', message: 'second failure' } },
    ],
    cursor: 2,
    status: 'failed',
  }
}

function el(container: HTMLElement, selector: string, text: string | RegExp): HTMLElement {
  const hit = Array.from(container.querySelectorAll<HTMLElement>(selector)).find((e) =>
    typeof text === 'string' ? e.textContent === text : text.test(e.textContent ?? ''),
  )
  expect(hit, `control: ${selector} "${String(text)}" must render`).not.toBeUndefined()
  return hit as HTMLElement
}

function noteSpan(container: HTMLElement, startsWith: string): HTMLElement {
  const hit = Array.from(container.querySelectorAll<HTMLElement>('span')).find((s) => s.textContent?.startsWith(startsWith))
  expect(hit, `control: the note "${startsWith}…" must render`).not.toBeUndefined()
  return hit as HTMLElement
}

function continueButton(container: HTMLElement): HTMLButtonElement {
  const buttons = Array.from(container.querySelectorAll<HTMLButtonElement>('button')).filter((b) =>
    /^(Import \d+ rows|Continue to next file|Map invoice number to continue|Filing needs a linked entity)$/.test(b.textContent ?? ''),
  )
  expect(buttons, 'control: exactly one Continue button').toHaveLength(1)
  return buttons[0]
}

describe('CreateMapping v2 look', () => {
  it('palette chips are 30px pf-btn buttons', () => {
    const { container } = render(createElement(CreateMapping, { ctx: badgeCtx(bareGroup()) }))
    const chips = Array.from(container.querySelectorAll<HTMLElement>('button[draggable]'))
    expect(chips, 'control: every unplaced field is a palette chip').toHaveLength(CANON.length)
    for (const chip of chips) {
      const who = chip.textContent ?? ''
      expect.soft(chip.className, who).toContain('pf-btn')
      expect.soft(chip.className, who).not.toContain('pf-chip')
      expect.soft(chip.style.height, who).toBe('30px')
      expect.soft(chip.style.padding, who).toBe('0px 11px')
      expect.soft(chip.style.borderRadius, `${who}: .pf-btn forces the corner, an inline radius is dead`).toBe('')
    }
  })

  it('an armed chip reads white on teal', () => {
    const armField = vi.fn()
    const { container } = render(createElement(CreateMapping, { ctx: badgeCtx(bareGroup(), { armedField: 'subtotal', armField }) }))
    const chips = Array.from(container.querySelectorAll<HTMLElement>('button[draggable]'))
    expect(chips.length).toBeGreaterThan(1)
    const armed = chips.find((c) => c.textContent?.startsWith('subtotal')) as HTMLElement
    expect(armed, 'control: the subtotal chip must render').not.toBeUndefined()
    expect.soft(armed.style.background).toBe('var(--action)')
    expect.soft(armed.style.color).toBe('var(--primary-foreground)')
    // Control: an unarmed chip keeps its own colour.
    const other = chips.find((c) => c.textContent?.startsWith('currency')) as HTMLElement
    expect(other, 'control: an unarmed optional chip must render').not.toBeUndefined()
    expect(other.style.color).toBe('var(--fg-1)')
    armed.click()
    expect(armField).toHaveBeenCalledWith('subtotal')
  })

  it('the title, intro box and coverage card follow the prototype', () => {
    const group = { ...suggestedGroup(), fileIds: ['f1', 'f2'] }
    const other = { ...group, id: 'g-2' }
    const { container } = render(
      createElement(CreateMapping, { ctx: badgeCtx(group, { groups: [group, other], pickedFiles: TWO_FILES }) }),
    )
    const titles = Array.from(container.querySelectorAll<HTMLElement>('.card-title'))
    const title = titles.find((t) => t.textContent?.startsWith('Map fields to columns')) as HTMLElement
    expect(title, 'control: the mapping card title must render').not.toBeUndefined()
    expect.soft(title.className).toContain('card-title')
    expect.soft(title.style.fontSize).toBe('15px')

    const intro = el(container, 'p', /^Drag each field onto the column/).parentElement as HTMLElement
    expect.soft(intro.style.borderRadius).toBe('var(--radius-md)')

    const sentence = el(container, 'p', /^This mapping applies to 2 files: a\.csv and b\.csv\./)
    const card = sentence.parentElement?.parentElement as HTMLElement
    expect.soft(card.style.padding).toBe('14px 20px')
    expect.soft(card.style.gap).toBe('10px')
    expect.soft(sentence.style.fontSize).toBe('13px')
    expect.soft(el(container, 'span', 'GROUP 1 OF 2').style.letterSpacing).toBe('0.05em')
  })

  it('the restored notice is a tinted box with ghost buttons', () => {
    const group = restoredTwoFileGroup()
    const { container } = render(createElement(CreateMapping, { ctx: badgeCtx(group, { pickedFiles: TWO_FILES }) }))
    const box = container.querySelector<HTMLElement>('[data-testid="map-restored-notice"]')
    expect(box, 'control: the restored notice must render').not.toBeNull()
    expect.soft(box!.style.display).toBe('flex')
    expect.soft(box!.style.justifyContent).toBe('space-between')
    expect.soft(box!.style.flexWrap).toBe('wrap')
    expect.soft(box!.style.padding).toBe('9px 12px')
    expect.soft(box!.style.background).toBe('var(--action-tint)')
    expect.soft(box!.style.border).toBe('1px solid var(--line-2)')
    expect.soft(box!.style.borderRadius).toBe('var(--radius-md)')

    const buttons = [
      el(container, 'button', 'Use automatic suggestions'),
      el(container, 'button', 'Map a.csv separately'),
      el(container, 'button', 'Map b.csv separately'),
    ]
    for (const b of buttons) {
      const who = b.textContent ?? ''
      expect.soft(b.className, who).toContain('v2-btn-ghost')
      expect.soft(b.className, who).toContain('pf-btn')
      expect.soft(b.style.height, who).toBe('30px')
      expect.soft(b.style.fontSize, who).toBe('12.5px')
      expect.soft(b.style.padding, who).toBe('0px 12px')
      // The class supplies all four; an inline copy would shadow the ghost recipe.
      expect.soft(b.style.background, who).toBe('')
      expect.soft(b.style.borderRadius, who).toBe('')
      expect.soft(b.style.border, who).toBe('')
      expect.soft(b.style.color, who).toBe('')
    }
  })

  it('the file line wears the green tile and a mono name', () => {
    const group = { ...suggestedGroup(), fileIds: ['f1', 'f2'] }
    const { container } = render(createElement(CreateMapping, { ctx: badgeCtx(group, { pickedFiles: TWO_FILES }) }))
    const tile = el(container, 'span', 'CSV')
    expect.soft(tile.style.background).toBe('var(--status-green-bg)')
    expect.soft(tile.style.color).toBe('var(--status-green-text)')
    const name = tile.nextElementSibling as HTMLElement
    expect(name?.textContent, 'control: the name sits beside the tile').toContain('a.csv')
    expect.soft(name.tagName).toBe('SPAN')
    expect.soft(name.classList.contains('mono')).toBe(true)
    expect.soft(name.style.fontSize).toBe('12.5px')
    expect.soft(name.style.fontWeight).toBe('600')
    const more = Array.from(name.querySelectorAll<HTMLElement>('span')).find((s) => s.textContent?.trim() === '+1 more')
    expect.soft(more, 'the "+1 more" tail is its own span').not.toBeUndefined()
    expect.soft(more?.style.color).toBe('var(--fg-3)')
    expect.soft(more?.style.fontWeight).toBe('500')
  })

  it('the footer failures follow the prototype', () => {
    const { container } = render(
      createElement(CreateMapping, { ctx: badgeCtx(suggestedGroup(), { run: failedRun(), pickedFiles: TWO_FILES }) }),
    )
    const names = ['a.csv', 'b.csv'].map((n) => el(container, 'span.mono', n))
    const list = names[0].parentElement?.parentElement as HTMLElement
    expect(list.contains(names[1]), 'control: both failures share one list').toBe(true)
    expect.soft(list.style.gap).toBe('3px')
    for (const n of names) {
      expect.soft(n.style.fontSize, n.textContent ?? '').toBe('11.5px')
      expect.soft(n.classList.contains('mono'), n.textContent ?? '').toBe(true)
    }
  })

  it('the only div.mono is the column header cell (E2E-02)', () => {
    const group = { ...restoredTwoFileGroup() }
    const { container } = render(
      createElement(CreateMapping, { ctx: badgeCtx(group, { run: failedRun(), pickedFiles: TWO_FILES, groups: [group, group] }) }),
    )
    const columns = container.querySelectorAll('[data-testid="map-column"]')
    expect(columns.length).toBeGreaterThan(0)
    expect(container.querySelectorAll('div.mono')).toHaveLength(columns.length)
  })

  it('"drop field" is enabled text', () => {
    const { container } = render(createElement(CreateMapping, { ctx: badgeCtx(suggestedGroup()) }))
    const drops = Array.from(container.querySelectorAll<HTMLElement>('span')).filter((s) => s.textContent === 'drop field')
    expect(drops.length, 'control: the Subtotal column is unplaced').toBeGreaterThan(0)
    for (const d of drops) expect.soft(d.style.color).toBe('var(--fg-3)')
    // D-6 keeps the decorative gutter letter at --fg-4.
    const letter = el(container, 'span.mono', 'A')
    expect.soft(letter.style.color).toBe('var(--fg-4)')
  })

  it('the notes take the prototype tones', () => {
    const noEntity = render(createElement(CreateMapping, { ctx: badgeCtx(suggestedGroup(), { entityId: null }) }))
    const none = noteSpan(noEntity.container, NO_ENTITY_NOTE)
    expect(none.textContent).toBe(
      'Columns read. Filing is unavailable in this workspace — it has no linked business entity to file the invoices against.',
    )
    expect.soft(none.style.color).toBe('var(--status-amber-text)')
    noEntity.unmount()

    const armed = render(createElement(CreateMapping, { ctx: badgeCtx(bareGroup(), { armedField: 'invoice_number' }) }))
    const armedNote = noteSpan(armed.container, ARMED_NOTE)
    expect(armedNote.textContent).toBe(
      'invoice_number is armed — click the column that holds it. Nothing continues until you place it by hand.',
    )
    expect.soft(armedNote.style.color).toBe('var(--status-red-text)')
    armed.unmount()

    // Pin: the unarmed unmapped note was already red.
    const idle = render(createElement(CreateMapping, { ctx: badgeCtx(bareGroup()) }))
    expect(noteSpan(idle.container, DRAG_NOTE).style.color).toBe('var(--status-red-text)')
  })

  it('Continue dims while invoice_number is unmapped (#114)', () => {
    const unmapped = render(createElement(CreateMapping, { ctx: badgeCtx(bareGroup()) }))
    const dim = continueButton(unmapped.container)
    expect(dim.textContent).toBe('Map invoice number to continue')
    // Not `disabled`: the click arms invoice_number (INVCR-01-05).
    expect(dim.disabled).toBe(false)
    expect.soft(dim.style.background).toBe('var(--action)')
    expect.soft(dim.style.color).toBe('var(--primary-foreground)')
    expect.soft(dim.style.opacity).toBe('0.45')
    expect.soft(dim.style.cursor).toBe('not-allowed')
    expect.soft(dim.style.filter).toBe('none')
    unmapped.unmount()

    const noEntity = render(createElement(CreateMapping, { ctx: badgeCtx(suggestedGroup(), { entityId: null }) }))
    const blocked = continueButton(noEntity.container)
    expect(blocked.textContent).toBe('Filing needs a linked entity')
    expect(blocked.disabled).toBe(true)
    expect.soft(blocked.style.background).toBe('var(--action)')
    expect.soft(blocked.style.opacity).toBe('0.45')
    expect.soft(blocked.style.cursor).toBe('not-allowed')
    expect.soft(blocked.style.filter).toBe('none')
    noEntity.unmount()

    const mapped = render(createElement(CreateMapping, { ctx: badgeCtx(suggestedGroup()) }))
    const live = continueButton(mapped.container)
    expect(live.textContent).toBe('Import 1 rows')
    expect(live.disabled).toBe(false)
    expect.soft(live.style.background).toBe('var(--action)')
    expect.soft(live.style.color).toBe('var(--primary-foreground)')
    expect.soft(live.style.opacity).toBe('')
    expect.soft(live.style.cursor).toBe('pointer')
    expect.soft(live.style.filter).toBe('')
  })
})
