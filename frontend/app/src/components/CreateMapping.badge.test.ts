// @vitest-environment jsdom
// AIRB-01 (Test-first, AC-1) -- the SUGGESTED badge must render exactly like its AUTO and
// RESTORED siblings. Renders CreateMapping directly with a hand-built ctx (CreateFlow.test.tsx's
// createFlowCtx precedent), not a full App boot -- no fetch/XHR needed, since the group fixture
// is built with the real applySuggestion/initMappingFromHeaders rather than a wire round trip.

import { createElement } from 'react'

import { cleanup, render } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import type { ImportPreview, SuggestMapping } from '../lib/importApi'
import { initMappingFromHeaders } from '../lib/mapping'
import { applySuggestion, type MappingGroup } from '../lib/mappingGroups'
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

function badgeCtx(group: MappingGroup): PlatformCtx {
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
