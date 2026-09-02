// @vitest-environment jsdom
//
// task-813 (EXTR-13-07), Stage 2.5 Mode A. Written RED against the STUB in LineItemGrid.tsx,
// which renders null -- every row below fails on its own target assertion (a `getByTestId`
// miss or a value mismatch), never on an import or a collection error. T1-T10, T12-T15 are
// the component's own contract; T11 (the header pane renders no line cell) lives in
// ExtractionFields.test.tsx, because it asserts through that pane's own `rowIds()`.
//
// T3/T4/T8 drive Add/remove/remap through a controlled RTL wrapper applying the shipped
// `addRow`/`removeRow`/`remapRoles` (lineItems.ts) -- the component itself holds no draft, so
// there is nothing else to assert the DOM effect of a click against.

import { useState } from 'react'

import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { ExtractionRegion } from '../lib/extractionReview'
import { LINE_ROLES, addRow, lineFieldName, remapRoles, removeRow } from '../lib/lineItems'
import type { LineCell, LineRole, LineRow } from '../lib/lineItems'
import { LineItemGrid } from './LineItemGrid'

const REGION_A: ExtractionRegion = { page: 1, x0: 0.1, y0: 0.1, x1: 0.3, y1: 0.15 }
const REGION_B: ExtractionRegion = { page: 2, x0: 0.4, y0: 0.4, x1: 0.6, y1: 0.45 }

function mkCell(name: string | null, value: string, region: ExtractionRegion | null = null): LineCell {
  return { name, value, region, reason: '' }
}

/** wireIndex 2, quantity 2 * unit_price 100.00 = line_total 200.00 -- clean by default. */
function mkRow(
  wireIndex: number,
  values: Partial<Record<LineRole, string>> = {},
  regions: Partial<Record<LineRole, ExtractionRegion | null>> = {},
): LineRow {
  const v: Record<LineRole, string> = {
    description: 'Widget',
    quantity: '2',
    unit_price: '100.00',
    line_total: '200.00',
    ...values,
  }
  return {
    key: `i${wireIndex}`,
    cells: {
      description: mkCell(lineFieldName(wireIndex, 'description'), v.description, regions.description ?? null),
      quantity: mkCell(lineFieldName(wireIndex, 'quantity'), v.quantity, regions.quantity ?? null),
      unit_price: mkCell(lineFieldName(wireIndex, 'unit_price'), v.unit_price, regions.unit_price ?? null),
      line_total: mkCell(lineFieldName(wireIndex, 'line_total'), v.line_total, regions.line_total ?? null),
    },
  }
}

interface GridProps {
  rows: LineRow[]
  wireRows: LineRow[]
  subtotal: string | null
  selected: string | null
  onSelectCell: (name: string) => void
  onEditCell: (at: number, role: LineRole, value: string) => void
  onAddRow: () => void
  onRemoveRow: (at: number) => void
  onRemapRoles: (from: LineRole, to: LineRole) => void
}

function itemGrid(over: Partial<GridProps> = {}) {
  const rows = over.rows ?? [mkRow(1), mkRow(2, { quantity: '3', unit_price: '50.00', line_total: '150.00' })]
  const props: GridProps = {
    rows,
    wireRows: rows,
    subtotal: null,
    selected: null,
    onSelectCell: () => {},
    onEditCell: () => {},
    onAddRow: () => {},
    onRemoveRow: () => {},
    onRemapRoles: () => {},
    ...over,
  }
  return <LineItemGrid {...props} />
}

function editCellAt(rows: LineRow[], at: number, role: LineRole, value: string): LineRow[] {
  return rows.map((row, i) => (i === at ? { ...row, cells: { ...row.cells, [role]: { ...row.cells[role], value } } } : row))
}

interface HarnessSpies {
  onSelectCell?: (name: string) => void
  onAddRow?: () => void
  onRemoveRow?: (at: number) => void
  onRemapRoles?: (from: LineRole, to: LineRole) => void
}

/** The controlled wrapper T2/T3/T4/T8 need: 08's draft, minimal, applying the shipped edits. */
function Harness({
  initial,
  wireRows,
  subtotal = null,
  selected = null,
  spies = {},
}: {
  initial: LineRow[]
  wireRows?: LineRow[]
  subtotal?: string | null
  selected?: string | null
  spies?: HarnessSpies
}) {
  const [rows, setRows] = useState(initial)
  return (
    <LineItemGrid
      rows={rows}
      wireRows={wireRows ?? initial}
      subtotal={subtotal}
      selected={selected}
      onSelectCell={(name) => spies.onSelectCell?.(name)}
      onEditCell={(at, role, value) => setRows((r) => editCellAt(r, at, role, value))}
      onAddRow={() => {
        spies.onAddRow?.()
        setRows((r) => addRow(r))
      }}
      onRemoveRow={(at) => {
        spies.onRemoveRow?.(at)
        setRows((r) => removeRow(r, at))
      }}
      onRemapRoles={(from, to) => {
        spies.onRemapRoles?.(from, to)
        setRows((r) => remapRoles(r, from, to))
      }}
    />
  )
}

// -- DOM readers -----------------------------------------------------------------------------

function gridEl(): HTMLElement {
  return screen.getByTestId('line-item-grid')
}

function scrollEl(): HTMLElement {
  return screen.getByTestId('line-item-scroll')
}

function rowsOf(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>('[data-testid^="line-item-row-"]'))
}

function rowAt(n: number): HTMLElement {
  return screen.getByTestId(`line-item-row-${n}`)
}

function cellAt(n: number, role: LineRole): HTMLElement {
  return screen.getByTestId(`line-item-cell-${n}-${role}`)
}

function inputAt(n: number, role: LineRole): HTMLInputElement {
  return screen.getByTestId(`line-item-input-${n}-${role}`) as HTMLInputElement
}

function markerAt(n: number, role: LineRole): HTMLElement | null {
  return screen.queryByTestId(`line-item-marker-${n}-${role}`)
}

function flagAt(n: number): HTMLElement | null {
  return screen.queryByTestId(`line-item-flag-${n}`)
}

function addBtn(): HTMLElement {
  return screen.getByTestId('line-item-add')
}

function removeBtn(n: number): HTMLElement {
  return screen.getByTestId(`line-item-remove-${n}`)
}

function roleSelect(role: LineRole): HTMLElement {
  return screen.getByTestId(`line-item-role-${role}`)
}

afterEach(cleanup)

// ==========================================================================================

describe('T1 one cell per parsed row', () => {
  it('renders four cells for every parsed row', () => {
    const rows = [mkRow(1), mkRow(2, { quantity: '3', unit_price: '50.00', line_total: '150.00' })]
    render(itemGrid({ rows, wireRows: rows }))

    expect(rowsOf().length, 'two rows in the fixture -- every check below is vacuous otherwise').toBe(2)

    for (const n of [1, 2]) {
      for (const role of LINE_ROLES) {
        const input = inputAt(n, role)
        expect(input, `row ${n}'s ${role} input did not render`).toBeTruthy()
        expect(input.getAttribute('aria-label'), `row ${n}'s ${role} input carries no accessible label`).toBeTruthy()
      }
    }
  })
})

describe('T2 the row flag', () => {
  it('typing a quantity moves the row flag on that keystroke', () => {
    const row1 = mkRow(1) // clean: 2 * 100.00 = 200.00
    const row2 = mkRow(2, { quantity: '3', unit_price: '50.00', line_total: '150.00' }) // clean: 3 * 50.00 = 150.00
    render(<Harness initial={[row1, row2]} />)

    expect(rowsOf().length, 'two rows must render before the before/after arms mean anything').toBe(2)
    expect(flagAt(1), 'row 1 already carries a flag -- the sibling check below would be vacuous').toBeNull()
    expect(flagAt(2), 'the BEFORE arm: row 2 must start clean').toBeNull()

    fireEvent.change(inputAt(2, 'quantity'), { target: { value: '9' } }) // 9 * 50.00 != 150.00

    expect(flagAt(2), 'the AFTER arm: the keystroke did not recompute the flag').toBeTruthy()
    expect(flagAt(1), 'the unchanged sibling picked up a flag too').toBeNull()
  })
})

describe('T3 Add a line', () => {
  it('appends a blank row and reports the click once', () => {
    const rows = [mkRow(1), mkRow(2, { quantity: '3', unit_price: '50.00', line_total: '150.00' })]
    const onAddRow = vi.fn()
    render(<Harness initial={rows} spies={{ onAddRow }} />)

    expect(rowsOf().length, 'two rows must render before Add means anything').toBe(2)
    fireEvent.click(addBtn())

    expect(onAddRow, 'Add did not report its click').toHaveBeenCalledTimes(1)
    expect(rowsOf().length, 'Add did not append a row').toBe(3)
    for (const role of LINE_ROLES) {
      expect(inputAt(3, role).value, `the new row's ${role} is not blank`).toBe('')
    }
  })
})

describe('T4 removing a row', () => {
  it('closes the ordinal gap but never renumbers the wire name that survives', () => {
    // The story's original row said removing a row renumbers field names 1,2,3 -- that
    // CONTRADICTS shipped removeRow (lineItems.ts:191-193), which is purely positional and
    // never touches a cell's own name. This asserts the inverse.
    const rows = [mkRow(1), mkRow(2), mkRow(4)]
    const onSelectCell = vi.fn()
    render(<Harness initial={rows} spies={{ onSelectCell }} />)

    expect(rowsOf().length, 'three rows must render before the removal means anything').toBe(3)
    fireEvent.click(removeBtn(2))

    expect(rowsOf().length, 'removing a row left the row count unchanged').toBe(2)
    expect(screen.queryByTestId('line-item-row-3'), 'a third ordinal survived the removal').toBeNull()

    fireEvent.click(cellAt(2, 'quantity'))
    expect(
      onSelectCell.mock.calls,
      "the ordinal-2 row renumbered its wire name instead of keeping wire index 4's",
    ).toEqual([[lineFieldName(4, 'quantity')]])
  })
})

describe('T5 one flag per broken row', () => {
  it('flags one row and leaves its clean sibling alone', () => {
    const clean = mkRow(1)
    const broken = mkRow(2, { quantity: '9' }) // 9 * 100.00 != 200.00
    render(itemGrid({ rows: [clean, broken], wireRows: [clean, broken] }))

    expect(rowsOf().length, 'two rows must render before the flag count means anything').toBe(2)
    const flags = Array.from(document.querySelectorAll<HTMLElement>('[data-testid^="line-item-flag-"]'))
    expect(flags.length, 'exactly one row is broken in this fixture').toBe(1)
    expect(flags[0].dataset.testid).toBe('line-item-flag-2')
  })
})

describe('T6 the table-level sum line', () => {
  const rows = [mkRow(1, { line_total: '100.00' }), mkRow(2, { quantity: '3', unit_price: '50.00', line_total: '150.00' })] // sum 250.00

  it('shows both numbers when the lines disagree with the printed subtotal', () => {
    render(itemGrid({ rows, wireRows: rows, subtotal: '260.00' }))

    const sum = screen.queryByTestId('line-item-sum')
    expect(sum, 'the disagreeing arm renders no sum line').toBeTruthy()
    expect(sum!.textContent, 'the drafted sum is missing').toContain('250.00')
    expect(sum!.textContent, 'the printed subtotal is missing').toContain('260.00')
  })

  it('says nothing when the lines agree with the printed subtotal', () => {
    // The control needle for the other two arms: a sum line rendered unconditionally passes
    // them. The floor is load-bearing here too -- without it, a component that renders
    // nothing at all (this file's stub) passes this absence vacuously.
    render(itemGrid({ rows, wireRows: rows, subtotal: '250.00' }))
    expect(rowsOf().length, 'the rows did not render -- the absence below is vacuous').toBe(2)
    expect(screen.queryByTestId('line-item-sum'), 'a settled sum still shouts').toBeNull()
  })

  it('states there is no printed subtotal to check the lines against', () => {
    render(itemGrid({ rows, wireRows: rows, subtotal: null }))

    const sum = screen.queryByTestId('line-item-sum')
    expect(sum, 'the no-subtotal arm renders no sum line').toBeTruthy()
    expect(sum!.textContent, 'the drafted sum is missing').toContain('250.00')
    expect(sum!.textContent, 'the no-subtotal sentence is missing').toMatch(/no subtotal/i)
  })
})

describe('T7 cell selection', () => {
  it('selects the clicked cell wire name, and an added row selects nothing', () => {
    const rows = [mkRow(1), mkRow(2, { quantity: '3', unit_price: '50.00', line_total: '150.00' })]
    const onSelectCell = vi.fn()
    render(<Harness initial={rows} spies={{ onSelectCell }} />)

    fireEvent.click(cellAt(2, 'quantity'))
    expect(onSelectCell.mock.calls, 'clicking a parsed cell did not report its wire name').toEqual([
      [lineFieldName(2, 'quantity')],
    ])

    onSelectCell.mockClear()
    fireEvent.click(addBtn())
    expect(rowsOf().length, 'Add did not append a row -- the click below has nothing to select').toBe(3)
    fireEvent.click(cellAt(3, 'quantity'))
    expect(onSelectCell.mock.calls, 'a nameless added cell reported a selection').toEqual([])
  })
})

describe('T8 remapping roles', () => {
  it('moves the whole cell, region and all, in both directions', () => {
    const row = mkRow(5, { quantity: '7', unit_price: '100.00' }, { quantity: REGION_A, unit_price: REGION_B })
    const onSelectCell = vi.fn()
    render(<Harness initial={[row]} spies={{ onSelectCell }} />)

    expect(inputAt(1, 'quantity').value, 'the floor: quantity starts at its own value').toBe('7')
    expect(inputAt(1, 'unit_price').value, 'the floor: unit_price starts at its own value').toBe('100.00')

    fireEvent.change(roleSelect('quantity'), { target: { value: 'unit_price' } })

    expect(inputAt(1, 'quantity').value, 'unit_price did not move into the quantity column').toBe('100.00')
    expect(inputAt(1, 'unit_price').value, 'quantity did not move into the unit_price column').toBe('7')

    fireEvent.click(cellAt(1, 'quantity'))
    expect(
      onSelectCell.mock.calls,
      "the cell now sitting in the quantity column still reports its OLD (unit_price) wire name",
    ).toEqual([[lineFieldName(5, 'unit_price')]])
  })
})

describe('T9 the empty extraction', () => {
  it('says so and offers Add a line, with no silent empty table', () => {
    render(itemGrid({ rows: [], wireRows: [] }))

    const empty = screen.getByTestId('line-item-empty')
    expect(empty.textContent, 'the first sentence is missing').toContain('We found no line items on this document.')
    expect(empty.textContent, 'the second sentence is missing').toContain(
      'An invoice cannot be filed until it has at least one line, so add one here.',
    )

    expect(addBtn(), 'the empty state offers no Add control').toBeTruthy()
    expect(gridEl().querySelectorAll('table'), 'the empty state still renders a table').toHaveLength(0)
    expect(gridEl().querySelectorAll('[disabled]'), 'the empty state disables a control').toHaveLength(0)
    expect(gridEl().querySelectorAll('[title]'), 'the empty state hides a reason in a tooltip').toHaveLength(0)
  })
})

describe('T10 the scroll container', () => {
  it('declares overflow-x auto and contains the rows', () => {
    // Declaration-level only -- jsdom computes no layout. The real oracle is
    // EXTR13-LAYOUT-01 (containment) and -02 (it actually scrolls), authored and run by
    // subtask 09 against the deployed build.
    const rows = [mkRow(1)]
    render(itemGrid({ rows, wireRows: rows }))

    const scroll = scrollEl()
    expect(scroll.style.overflowX).toBe('auto')
    expect(scroll.contains(rowAt(1)), 'the scroll node does not contain the rows it is meant to scroll').toBe(true)
  })
})

describe('T12 the corrected-cell marker', () => {
  it('marks a cell that differs from the wire, and leaves an unchanged sibling bare', () => {
    // lineItems.ts's LineCell carries no `corrected` -- linesFromFields drops it (`:93`) -- so
    // the marker has no wire signal of its own. This is why the component takes a `wireRows`
    // prop: a cell is marked when it differs from its positional wire counterpart.
    const wireRows = [mkRow(1), mkRow(2, { quantity: '3', unit_price: '50.00', line_total: '150.00' })]
    const draftRows = [
      wireRows[0],
      { ...wireRows[1], cells: { ...wireRows[1].cells, quantity: { ...wireRows[1].cells.quantity, value: '9' } } },
    ]
    render(itemGrid({ rows: draftRows, wireRows }))

    const marker = markerAt(2, 'quantity')
    expect(marker, 'the edited cell renders no marker').toBeTruthy()
    expect(marker!.style.background).toBe('var(--action)')
    expect(marker!.style.width).toBe('7px')
    expect(marker!.style.height).toBe('7px')

    expect(markerAt(2, 'description'), "the same row's untouched cell carries a marker").toBeNull()
    expect(markerAt(1, 'quantity'), 'the unchanged sibling row carries a marker').toBeNull()
  })
})

describe('T13 the flag pill strip', () => {
  it('sits in a wrapping label strip, and declares flex: none on itself', () => {
    const rows = [mkRow(1), mkRow(2, { quantity: '9' })]
    render(itemGrid({ rows, wireRows: rows }))

    const pill = flagAt(2)
    expect(pill, 'the floor: row 2 really is flagged').toBeTruthy()
    const strip = pill!.parentElement as HTMLElement
    expect(strip.style.flexWrap, "the pill's strip does not wrap").toBe('wrap')
    expect(pill!.style.flex, 'the pill can be squeezed by its own strip').toBe('none')
  })
})

describe('T14 the selected cell', () => {
  it("wears the pane's marked-row amber, and its sibling does not", () => {
    const rows = [mkRow(1), mkRow(2, { quantity: '3', unit_price: '50.00', line_total: '150.00' })]
    render(itemGrid({ rows, wireRows: rows, selected: lineFieldName(2, 'quantity') }))

    const selected = cellAt(2, 'quantity')
    expect(selected.style.background).toBe('var(--accent-10)')
    expect(selected.style.boxShadow).toBe('inset 2px 0 0 var(--accent)')

    const other = cellAt(2, 'description')
    expect(other.style.background, 'an unselected cell wears the selected treatment').not.toBe('var(--accent-10)')
    expect(other.style.boxShadow, 'an unselected cell wears the selected rail').not.toBe('inset 2px 0 0 var(--accent)')
  })
})

describe('T15 no hidden reasons, no disabled controls', () => {
  it('renders no [title] and no [disabled], populated or empty', () => {
    const rows = [mkRow(1), mkRow(2, { quantity: '9' })]
    const { unmount } = render(itemGrid({ rows, wireRows: rows }))
    expect(gridEl().querySelectorAll('[disabled]'), 'the populated grid disables a control').toHaveLength(0)
    expect(gridEl().querySelectorAll('[title]'), 'the populated grid hides a reason in a tooltip').toHaveLength(0)

    // The control needle: the same query finds a planted node, so the zero above is a real
    // absence and not a selector that matches nothing.
    const probe = document.createElement('span')
    probe.setAttribute('title', 'probe')
    gridEl().appendChild(probe)
    expect(gridEl().querySelectorAll('[title]'), 'the [title] selector is inert').toHaveLength(1)
    probe.remove()
    unmount()

    render(itemGrid({ rows: [], wireRows: [] }))
    expect(gridEl().querySelectorAll('[disabled]'), 'the empty grid disables a control').toHaveLength(0)
    expect(gridEl().querySelectorAll('[title]'), 'the empty grid hides a reason in a tooltip').toHaveLength(0)
  })
})
