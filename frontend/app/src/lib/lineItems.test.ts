// lineItems.ts's spec. The rows above the "adversarial" banner were authored RED against a
// throwing stub and are now green; the rows below it were added in QA against the shipped
// body, each one pinned by a mutation that kills it.
//
// Genuinely discriminating against a plausible wrong implementation: the index-order test
// (arrival order vs numeric index — only multi-digit indices expose it), the hole test (a
// gap must not renumber neighbours), the unparseable-total test (must not fold to zero), the
// 1e18 magnitude pair (float64 vs exact decimal), removeRow's identity-preservation test, and
// the leading-zero divergence test. Weak-but-real controls, kept because a stub can pass them
// by accident otherwise: the lineSumState null/non-null pair, remapRoles' non-identity guard,
// lineSetChanged's no-op-vs-edit pair, and the no-network scan's control file.

import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import type { ExtractionFieldState, ExtractionRegion, ExtractionReason } from './extractionReview'
import {
  LINE_ROLES,
  LINE_TOLERANCE,
  addRow,
  lineSetChanged,
  lineSumState,
  linesFromFields,
  linesToPost,
  parseLineFieldName,
  remapRoles,
  removeRow,
  rowArithmetic,
  type LineCell,
  type LineRole,
  type LineRow,
} from './lineItems'

function repoFile(rel: string): string {
  return readFileSync(fileURLToPath(new URL(`../../../../${rel}`, import.meta.url)), 'utf8')
}

function mkField(
  name: string,
  value: string | null,
  region: ExtractionRegion | null = null,
  reason: ExtractionReason = '',
): ExtractionFieldState {
  return { name, value, region, reason, alternatives: [], corrected: null }
}

function cell(name: string | null, value: string | null): LineCell {
  return { name, value: value ?? '', region: null, reason: '' }
}

interface RowValues {
  description?: string | null
  quantity?: string | null
  unit_price?: string | null
  line_total?: string | null
}

function mkRow(index: number, values: RowValues = {}): LineRow {
  const name = (role: LineRole) => `line_items[${index}].${role}`
  return {
    key: `r${index}`,
    cells: {
      description: cell(name('description'), values.description ?? null),
      quantity: cell(name('quantity'), values.quantity ?? null),
      unit_price: cell(name('unit_price'), values.unit_price ?? null),
      line_total: cell(name('line_total'), values.line_total ?? null),
    },
  }
}

// -- parseLineFieldName ------------------------------------------------------------------

describe('parseLineFieldName', () => {
  it('the four roles parse, and a multi-digit index is not truncated', () => {
    expect(parseLineFieldName('line_items[3].unit_price'), 'unit_price at index 3').toEqual({
      index: 3,
      role: 'unit_price',
    })
    expect(parseLineFieldName('line_items[1].description'), 'description at index 1').toEqual({
      index: 1,
      role: 'description',
    })
    expect(parseLineFieldName('line_items[7].quantity'), 'quantity at index 7').toEqual({
      index: 7,
      role: 'quantity',
    })
    expect(parseLineFieldName('line_items[2].line_total'), 'line_total at index 2').toEqual({
      index: 2,
      role: 'line_total',
    })
    // +1 needle: a naive single-digit regex ([0-9]) would truncate this to index 1.
    expect(parseLineFieldName('line_items[12].description'), 'a multi-digit index was truncated').toEqual({
      index: 12,
      role: 'description',
    })
  })

  it('six near-misses all parse to null', () => {
    const rejects = [
      'line_items',
      'line_items[0].quantity',
      'line_items[01].quantity',
      'line_itemsx[1].quantity',
      'line_items[1].line_tax',
      'document_text_layer',
    ]
    expect(rejects.length, 'the reject fixture list itself is empty').toBeGreaterThan(0)
    for (const name of rejects) {
      expect(parseLineFieldName(name), `${name} must parse to null`).toBeNull()
    }
  })
})

// -- linesFromFields ----------------------------------------------------------------------

describe('linesFromFields', () => {
  it("rows come back in numeric index order, not the wire's lexicographic arrival order", () => {
    const regionFor2: ExtractionRegion = { page: 1, x0: 0.1, y0: 0.1, x1: 0.2, y1: 0.12 }
    const regionFor10: ExtractionRegion = { page: 1, x0: 0.1, y0: 0.5, x1: 0.2, y1: 0.52 }
    // The real wire order (reader.go's ORDER BY ties on created_at) sorts field_name as
    // text, so every line_items[10].* row precedes every line_items[2].* row.
    const fields = [
      mkField('line_items[10].description', 'Ten', regionFor10),
      mkField('line_items[10].quantity', '1'),
      mkField('line_items[10].unit_price', '5.00'),
      mkField('line_items[10].line_total', '5.00'),
      mkField('line_items[2].description', 'Two', regionFor2),
      mkField('line_items[2].quantity', '3'),
      mkField('line_items[2].unit_price', '4.00'),
      mkField('line_items[2].line_total', '12.00'),
    ]

    const rows = linesFromFields(fields)

    expect(rows.length, 'both wire rows were dropped').toBe(2)
    expect(
      rows.map((r) => r.cells.description.name),
      'an arrival-order (not index-order) implementation would return [10, 2]',
    ).toEqual(['line_items[2].description', 'line_items[10].description'])
    expect(rows[0].cells.description.region, 'row 2 lost its own region').toEqual(regionFor2)
    expect(rows[1].cells.description.region, 'row 10 lost its own region').toEqual(regionFor10)
  })

  it('a row present in one role only still appears, with the other three cells inert', () => {
    const fields = [mkField('line_items[2].description', 'Solo')]
    const rows = linesFromFields(fields)
    expect(rows.length, 'the lone-role row was dropped entirely').toBe(1)
    const row = rows[0]
    expect(row.cells.description.value, 'the one populated cell lost its value').toBe('Solo')
    for (const role of ['quantity', 'unit_price', 'line_total'] as const) {
      expect(row.cells[role], `${role} cell was not inert`).toEqual({
        name: `line_items[2].${role}`,
        value: '',
        region: null,
        reason: '',
      })
    }
  })

  it('a hole does not renumber its neighbours', () => {
    const fields = [mkField('line_items[1].description', 'One'), mkField('line_items[3].description', 'Three')]
    const rows = linesFromFields(fields)
    expect(rows.length, 'the hole test needs exactly two rows to be meaningful').toBe(2)
    expect(
      rows.map((r) => r.cells.description.name),
      'index 3 was renumbered to fill the gap left by the missing index 2',
    ).toEqual(['line_items[1].description', 'line_items[3].description'])
  })
})

// -- rowArithmetic --------------------------------------------------------------------------

describe('rowArithmetic', () => {
  it('all three present and exact: qty * price === total is ok', () => {
    expect(rowArithmetic(mkRow(1, { quantity: '2', unit_price: '3', line_total: '6.00' }))).toBe('ok')
  })

  it('a difference of exactly 0.01 does not flag (strictly-greater, mirroring exceedsTolerance)', () => {
    expect(rowArithmetic(mkRow(1, { quantity: '1', unit_price: '1.00', line_total: '1.01' }))).toBe('ok')
  })

  it('a difference of 0.02 flags', () => {
    expect(rowArithmetic(mkRow(1, { quantity: '1', unit_price: '1.00', line_total: '1.02' }))).toBe('flagged')
  })

  it('any of the three cells missing is unchecked, never folded to a pass', () => {
    expect(rowArithmetic(mkRow(1, { quantity: '1', line_total: '1.00' })), 'unit_price is absent').toBe('unchecked')
  })

  it('an unparseable total is unchecked, not folded to zero', () => {
    // qty=0, price=0 would make a zero-folded 'ok' indistinguishable from a genuinely
    // skipped check -- this pins that the garbled total is never coerced to 0.
    expect(
      rowArithmetic(mkRow(1, { quantity: '0', unit_price: '0', line_total: 'garbled' })),
      'a garbled total was folded to zero and compared as if it parsed',
    ).toBe('unchecked')
  })

  it('exact at 1e18: 99999999.999 x 9999999999.99 = 999999999989000000.00001 is ok', () => {
    // reconcile_adversarial_test.go:141-155's own fixture. A float64 implementation rounds
    // this product to 999999999988999936, off by ~107 -- two orders past LINE_TOLERANCE.
    expect(
      rowArithmetic(
        mkRow(1, { quantity: '99999999.999', unit_price: '9999999999.99', line_total: '999999999989000000.00001' }),
      ),
      'a float64-backed multiply diverged and wrongly flagged an exact match',
    ).toBe('ok')
  })

  it('the check still fires at that magnitude', () => {
    // Same operands, total nudged past tolerance -- without this control, the previous
    // spec would pass on a stub that always returns 'ok'.
    expect(
      rowArithmetic(
        mkRow(1, { quantity: '99999999.999', unit_price: '9999999999.99', line_total: '999999999989000000.03' }),
      ),
      'a genuine 1e18-scale mismatch was not flagged',
    ).toBe('flagged')
  })

  it("the parse grammar diverges from Go's decimal.NewFromString on a leading zero (D3)", () => {
    // DECIMAL_RE (invoices.ts) rejects a leading zero; decimal.NewFromString on the Go
    // side accepts it. Accepted, documented divergence -- pinned here, not discovered later.
    expect(
      rowArithmetic(mkRow(1, { quantity: '007', unit_price: '1.00', line_total: '7.00' })),
      "'007' should be unchecked under the browser's DECIMAL_RE grammar",
    ).toBe('unchecked')
    expect(
      rowArithmetic(mkRow(1, { quantity: '7', unit_price: '1.00', line_total: '7.00' })),
      'the same value without the leading zero must parse and check clean',
    ).toBe('ok')
  })
})

// -- lineSumState ---------------------------------------------------------------------------

describe('lineSumState', () => {
  const twoRows = [
    mkRow(1, { quantity: '1', unit_price: '100.00', line_total: '100.00' }),
    mkRow(2, { quantity: '1', unit_price: '200.00', line_total: '200.00' }),
  ]

  it('reports both numbers and the disagreement verdict', () => {
    expect(lineSumState(twoRows, '250.00'), 'sum/printed/agrees mismatch').toEqual({
      sum: '300.00',
      printed: '250.00',
      agrees: false,
    })
  })

  it('a difference of exactly the shared 0.01 tolerance still agrees', () => {
    expect(lineSumState(twoRows, '299.99'), 'the sum check should share LINE_TOLERANCE with the row check').toEqual({
      sum: '300.00',
      printed: '299.99',
      agrees: true,
    })
  })

  it('is null only when no row has a parseable line total, and non-null the moment one does', () => {
    const noTotals = [
      mkRow(1, { quantity: '1', unit_price: '100.00' }),
      mkRow(2, { quantity: '1', unit_price: '200.00' }),
    ]
    expect(
      lineSumState(noTotals, '250.00'),
      'a stub returning null unconditionally would still pass this line alone',
    ).toBeNull()

    const oneTotal = [
      mkRow(1, { quantity: '1', unit_price: '100.00', line_total: '100.00' }),
      mkRow(2, { quantity: '1', unit_price: '200.00' }),
    ]
    expect(lineSumState(oneTotal, '250.00'), 'one parseable line total must yield a non-null result').not.toBeNull()
  })

  it('an unparseable printed subtotal yields printed=null and agrees=null, never a false disagreement', () => {
    expect(lineSumState(twoRows, 'garbled'), 'a garbled subtotal must not read as a real disagreement').toEqual({
      sum: '300.00',
      printed: null,
      agrees: null,
    })
  })

  it('an absent printed subtotal yields printed=null and agrees=null', () => {
    expect(lineSumState(twoRows, null)).toEqual({ sum: '300.00', printed: null, agrees: null })
  })
})

// -- remapRoles -------------------------------------------------------------------------------

describe('remapRoles', () => {
  const base: LineRow = {
    key: 'r1',
    cells: {
      description: {
        name: 'line_items[1].description',
        value: 'Widget',
        region: { page: 1, x0: 0, y0: 0, x1: 0.1, y1: 0.02 },
        reason: '',
      },
      quantity: {
        name: 'line_items[1].quantity',
        value: '2',
        region: { page: 1, x0: 0.2, y0: 0, x1: 0.3, y1: 0.02 },
        reason: '',
      },
      unit_price: {
        name: 'line_items[1].unit_price',
        value: '3.00',
        region: { page: 1, x0: 0.4, y0: 0, x1: 0.5, y1: 0.02 },
        reason: '',
      },
      line_total: {
        name: 'line_items[1].line_total',
        value: '6.00',
        region: { page: 1, x0: 0.6, y0: 0, x1: 0.7, y1: 0.02 },
        reason: '',
      },
    },
  }

  it('swaps the whole cell -- value, region and name, not just the value -- and is its own inverse', () => {
    const swapped = remapRoles([base], 'quantity', 'unit_price')
    expect(swapped, 'a no-op or identity implementation would return the input unchanged').not.toEqual([base])
    expect(swapped[0].cells.quantity, "the region did not move with the value it belongs to").toEqual(
      base.cells.unit_price,
    )
    expect(swapped[0].cells.unit_price, 'the swap only moved one side').toEqual(base.cells.quantity)

    const restored = remapRoles(swapped, 'quantity', 'unit_price')
    expect(restored, 'remapRoles is not its own inverse').toEqual([base])
  })

  it('a three-cycle composes correctly', () => {
    // description -> quantity -> unit_price -> description, via two pairwise swaps.
    const step1 = remapRoles([base], 'description', 'unit_price')
    const step2 = remapRoles(step1, 'quantity', 'unit_price')
    expect(step2[0].cells.description, 'the three-cycle did not land unit_price in description').toEqual(
      base.cells.unit_price,
    )
    expect(step2[0].cells.quantity, 'the three-cycle did not land description in quantity').toEqual(
      base.cells.description,
    )
    expect(step2[0].cells.unit_price, 'the three-cycle did not land quantity in unit_price').toEqual(
      base.cells.quantity,
    )
    expect(step2[0].cells.line_total, 'the untouched role moved').toEqual(base.cells.line_total)
  })
})

// -- addRow / removeRow / linesToPost ----------------------------------------------------------

describe('removeRow', () => {
  it("keeps each surviving row's own wire identity -- it does not renumber to fill the gap", () => {
    const four = [
      mkRow(1, { quantity: '1', unit_price: '1.00', line_total: '1.00' }),
      mkRow(2, { quantity: '2', unit_price: '2.00', line_total: '4.00' }),
      mkRow(3, { quantity: '3', unit_price: '3.00', line_total: '9.00' }),
      mkRow(4, { quantity: '4', unit_price: '4.00', line_total: '16.00' }),
    ]
    const after = removeRow(four, 1) // 0-based: drops the array position holding index 2

    expect(after.length, 'removeRow dropped the wrong count').toBe(3)
    expect(
      after.map((r) => r.cells.description.name),
      "the ordinal is array position (D4) -- surviving rows keep their OWN wire names, they don't renumber 1..N",
    ).toEqual(['line_items[1].description', 'line_items[3].description', 'line_items[4].description'])
  })

  it('an out-of-range position returns a fresh, unchanged copy', () => {
    const two = [
      mkRow(1, { quantity: '1', unit_price: '1.00', line_total: '1.00' }),
      mkRow(2, { quantity: '2', unit_price: '2.00', line_total: '4.00' }),
    ]
    const result = removeRow(two, 99)
    expect(result, 'an out-of-range removeRow mutated the row set').toEqual(two)
    expect(result, 'removeRow must return a fresh array, not the same reference').not.toBe(two)
  })
})

describe('addRow', () => {
  it('appends one blank row and leaves every existing row untouched', () => {
    const two = [
      mkRow(1, { quantity: '1', unit_price: '1.00', line_total: '1.00' }),
      mkRow(2, { quantity: '2', unit_price: '2.00', line_total: '4.00' }),
    ]
    const after = addRow(two)
    expect(after.length, 'addRow did not append exactly one row').toBe(3)
    expect(after.slice(0, 2), 'the existing prefix was mutated').toEqual(two)
    const blank = after[2]
    for (const role of LINE_ROLES) {
      expect(blank.cells[role], `${role} of the appended row was not blank`).toEqual({
        name: null,
        value: '',
        region: null,
        reason: '',
      })
    }
  })
})

describe('linesToPost', () => {
  // Title corrected in QA: LineItemInput carries no ordinal key, so "renumbers by position"
  // was unassertable here. The server assigns line_no from array order; the order itself is
  // pinned by the adversarial spec below.
  it('drops an all-blank row, nulls a blank cell, and never rewrites a value', () => {
    const rows: LineRow[] = [
      mkRow(1, { description: 'Widget', quantity: '1', unit_price: '10.00', line_total: '10.00' }),
      {
        key: 'blank',
        cells: {
          description: cell(null, ''),
          quantity: cell(null, '   '), // whitespace-only counts as blank
          unit_price: cell(null, ''),
          line_total: cell(null, ''),
        },
      },
      mkRow(3, { description: '  Widget  ', quantity: '2', unit_price: '5.00', line_total: '10.00' }),
    ]

    const posted = linesToPost(rows)

    expect(posted.length, 'three rows in, two out -- the all-blank row must be dropped').toBe(2)
    expect(posted, "the blank cell must become null, and 'Widget' with its spaces must post verbatim").toEqual([
      { description: 'Widget', quantity: '1', unit_price: '10.00', line_total: '10.00' },
      { description: '  Widget  ', quantity: '2', unit_price: '5.00', line_total: '10.00' },
    ])
  })
})

// -- lineSetChanged ---------------------------------------------------------------------------

describe('lineSetChanged', () => {
  const wireRows = [
    mkRow(1, { quantity: '1', unit_price: '10.00', line_total: '10.00' }),
    mkRow(2, { quantity: '2', unit_price: '5.00', line_total: '10.00' }),
  ]

  it('retyping the identical characters is a no-op', () => {
    const draft = wireRows.map((r) => ({ ...r, cells: { ...r.cells } }))
    expect(lineSetChanged(wireRows, draft), 'an untouched draft read as changed').toBe(false)
  })

  it('one changed character is a real edit', () => {
    const draft = [
      { ...wireRows[0], cells: { ...wireRows[0].cells, quantity: { ...wireRows[0].cells.quantity, value: '9' } } },
      wireRows[1],
    ]
    expect(lineSetChanged(wireRows, draft), 'a genuine one-character edit was not detected').toBe(true)
  })

  it('a removed row is a real edit', () => {
    expect(lineSetChanged(wireRows, [wireRows[0]]), 'a removed row was not detected as a change').toBe(true)
  })

  it('blank and whitespace-only canonicalize the same, so typing spaces into an empty cell is no change', () => {
    const wire = [mkRow(1, { quantity: '1', unit_price: '10.00', line_total: '10.00' })] // description: ''
    const draft = [mkRow(1, { quantity: '1', unit_price: '10.00', line_total: '10.00', description: '   ' })]
    expect(
      lineSetChanged(wire, draft),
      "'' and whitespace-only must canonicalize the same, mirroring diffLineItems' canonField",
    ).toBe(false)
  })
})

// -- the arity pin: four roles, in extraction.LineRoles' own order -------------------------

describe('LINE_ROLES', () => {
  it('holds exactly the four roles, in order', () => {
    // Every `for (const role of LINE_ROLES)` in this file and in LineItemGrid.test.tsx is
    // bounded by this set, so a shortened one would make them all assert less and still pass.
    // Go pins its own side in lineitems_parse_qa_test.go.
    expect(LINE_ROLES.length, 'a loop over LINE_ROLES asserts less than it claims to').toBe(4)
    expect([...LINE_ROLES], "the order diverged from extraction.LineRoles").toEqual([
      'description',
      'quantity',
      'unit_price',
      'line_total',
    ])
  })
})

// -- the drift guard: LINE_TOLERANCE mirrors reconcile.go's own literal --------------------

describe("LINE_TOLERANCE equals reconcile.go's reconcileTolerance literal", () => {
  const GO_PATH = 'internal/extraction/reconcile.go'

  function extractTolerance(source: string): string | null {
    return /const\s+reconcileTolerance\s*=\s*"([^"]+)"/.exec(source)?.[1] ?? null
  }

  it('LINE_TOLERANCE equals the Go literal', () => {
    const src = repoFile(GO_PATH)
    // Floor: the read is real and lands in the right function.
    expect(src.length, 'reconcile.go read empty or truncated -- the floor is broken').toBeGreaterThan(1000)
    expect(src, 'the scan read the wrong file -- reconcileLines is not present').toContain('func reconcileLines(')

    // Planted positive: proves the extractor is a live comparator, not two empty strings
    // silently agreeing.
    expect(
      extractTolerance('const reconcileTolerance = "0.02"'),
      'the extractor cannot see a literal at all',
    ).toBe('0.02')

    const goValue = extractTolerance(src)
    expect(goValue, 'reconcileTolerance was not found in the real reconcile.go').not.toBeNull()
    expect(LINE_TOLERANCE, "LINE_TOLERANCE hasn't been implemented to match reconcile.go's literal yet").toBe(
      goValue,
    )
  })
})

// -- the EXTR-14 fence: no network, no storage (D11) ---------------------------------------

describe('lineItems.ts reaches no network and no storage', () => {
  it('the module source contains none of the forbidden needles; extractionReview.ts (the control) does', () => {
    const modulePath = fileURLToPath(new URL('./lineItems.ts', import.meta.url))
    const src = readFileSync(modulePath, 'utf8')
    expect(src.length, 'lineItems.ts read empty or truncated').toBeGreaterThan(500)
    expect(src, 'the scan read the wrong file').toContain('export function remapRoles(')

    // Needle-concatenation so this test file's own text can never self-match the scan.
    const needles = ['fetch', 'authed' + 'Fetch', 'local' + 'Storage', 'session' + 'Storage', 'indexed' + 'DB']
    for (const needle of needles) {
      expect(
        src.includes(needle),
        `lineItems.ts contains the forbidden needle "${needle}" -- EXTR-14 owns persistence, not this module`,
      ).toBe(false)
    }

    const reviewSrc = readFileSync(fileURLToPath(new URL('./extractionReview.ts', import.meta.url)), 'utf8')
    expect(
      needles.some((n) => reviewSrc.includes(n)),
      'the control file does not trip the scan -- the scan is not actually live',
    ).toBe(true)
  })
})

// == adversarial / edge / negative coverage (QA, Mode B) =======================================
// Added against the shipped body. Each row is pinned by a mutation that kills it; the notes
// name the gap the RED phase left open.

describe('remapRoles (adversarial)', () => {
  // The RED fixture ties all four reasons to '', so a mutant that leaves `reason` behind
  // survives every assertion above. These reasons differ, so the tie cannot hide the bug.
  const reasoned: LineRow = {
    key: 'r1',
    cells: {
      description: { name: 'line_items[1].description', value: 'Widget', region: null, reason: 'ambiguous' },
      quantity: { name: 'line_items[1].quantity', value: '2', region: null, reason: 'unreadable' },
      unit_price: { name: 'line_items[1].unit_price', value: '3.00', region: null, reason: 'inconsistent' },
      line_total: { name: 'line_items[1].line_total', value: '6.00', region: null, reason: 'missing' },
    },
  }

  it("the moved cell's reason travels with it, not just its value, name and region", () => {
    const reasons = LINE_ROLES.map((role) => reasoned.cells[role].reason)
    expect(
      new Set(reasons).size,
      'the fixture ties its reasons -- a tie cannot discriminate a reason that fails to move',
    ).toBe(LINE_ROLES.length)

    const swapped = remapRoles([reasoned], 'quantity', 'unit_price')
    expect(swapped[0].cells.quantity.reason, "the reason stayed behind while its value moved").toBe('inconsistent')
    expect(swapped[0].cells.unit_price.reason, 'the reason stayed behind while its value moved').toBe('unreadable')
    expect(swapped[0].cells.description.reason, 'an untouched role lost its reason').toBe('ambiguous')
  })

  it('remapping a role onto itself is the identity, not a self-erasure', () => {
    expect(
      remapRoles([reasoned], 'quantity', 'quantity'),
      'from === to must be total and leave the row exactly as it was',
    ).toEqual([reasoned])
  })

  it('an empty row set is returned empty, not undefined', () => {
    expect(remapRoles([], 'quantity', 'unit_price'), 'remapRoles is not total over an empty set').toEqual([])
  })
})

describe('rowArithmetic (adversarial)', () => {
  // The RED boundaries only ever put the total ABOVE the product (1.01, 1.02), so the
  // positive branch of absScaled is never exercised at the boundary. These are its mirror.
  it('the tolerance is symmetric: the product exceeding the total by 0.01 does not flag either', () => {
    expect(rowArithmetic(mkRow(1, { quantity: '1', unit_price: '1.00', line_total: '0.99' })), '+0.01').toBe('ok')
    expect(rowArithmetic(mkRow(1, { quantity: '1', unit_price: '1.00', line_total: '0.98' })), '+0.02').toBe('flagged')
  })

  it('a difference off the 0.01 grid still compares: 0.011 flags where 0.010 does not', () => {
    expect(
      rowArithmetic(mkRow(1, { quantity: '1', unit_price: '1.000', line_total: '0.990' })),
      'exactly 0.010 must not flag',
    ).toBe('ok')
    expect(
      rowArithmetic(mkRow(1, { quantity: '1', unit_price: '1.000', line_total: '0.989' })),
      'an equality-against-0.02 check (rather than a comparison) would let 0.011 pass',
    ).toBe('flagged')
  })

  it('operands of different scales compare exactly, and 0.1 x 0.2 is 0.02 (not float64 0.020000000000000004)', () => {
    expect(0.1 * 0.2, 'the float64 trap this row exists for has been fixed in the language').not.toBe(0.02)
    expect(
      rowArithmetic(mkRow(1, { quantity: '0.1', unit_price: '0.2', line_total: '0.02' })),
      'a float64 multiply leaves 4e-18 of residue here; the exact kit must land on zero',
    ).toBe('ok')
    expect(
      rowArithmetic(mkRow(1, { quantity: '1', unit_price: '1', line_total: '1.0000' })),
      'scale 0 against scale 4 must rescale, not compare mantissas raw',
    ).toBe('ok')
  })

  it('negative money is checked, not skipped or absolute-valued into agreement', () => {
    expect(rowArithmetic(mkRow(1, { quantity: '-2', unit_price: '3.00', line_total: '-6.00' })), 'exact').toBe('ok')
    expect(
      rowArithmetic(mkRow(1, { quantity: '-2', unit_price: '3.00', line_total: '-6.02' })),
      'a sign-blind comparison would call this exact',
    ).toBe('flagged')
  })
})

describe('lineSumState (adversarial)', () => {
  it('an empty row set is null, mirroring haveLineTotal on no rows at all', () => {
    expect(lineSumState([], '100.00'), 'an empty set has no parseable line total, so there is no sum').toBeNull()
  })

  it('rows of differing scale sum exactly', () => {
    const rows = [mkRow(1, { line_total: '0.5' }), mkRow(2, { line_total: '0.25' })]
    expect(lineSumState(rows, '0.75'), 'a mixed-scale sum did not land exactly').toEqual({
      sum: '0.75',
      printed: '0.75',
      agrees: true,
    })
  })

  it('an unparseable line total is skipped, not folded to zero, and the rest still sum', () => {
    const rows = [mkRow(1, { line_total: '100.00' }), mkRow(2, { line_total: 'garbled' })]
    expect(
      lineSumState(rows, '100.00'),
      'a garbled line total was folded into the sum instead of being skipped',
    ).toEqual({ sum: '100.00', printed: '100.00', agrees: true })
  })
})

describe('linesFromFields (adversarial)', () => {
  it('a field the module does not own is ignored, and the line fields still land', () => {
    const fields = [
      mkField('invoice_number', 'INV-1'),
      mkField('line_items[1].description', 'One'),
      mkField('document_text_layer', 'blah'),
      mkField('line_items', 'the block itself'),
      mkField('subtotal', '100.00'),
    ]
    // Floor: the fixture really does carry non-line fields to be dropped.
    const lineFieldCount = fields.filter((f) => parseLineFieldName(f.name) !== null).length
    expect(lineFieldCount, 'the fixture has no line fields at all').toBe(1)
    expect(fields.length - lineFieldCount, 'the fixture has no foreign fields to ignore').toBe(4)

    const rows = linesFromFields(fields)
    expect(rows.length, 'a foreign field was grouped into a row, or the line field was dropped').toBe(1)
    expect(rows[0].cells.description.value, 'the surviving row lost its value').toBe('One')
  })

  it('a wire cell with a null value becomes an empty string, never null', () => {
    const rows = linesFromFields([mkField('line_items[1].quantity', null)])
    expect(rows.length, 'the null-valued cell dropped its whole row').toBe(1)
    expect(
      rows[0].cells.quantity.value,
      "an input holds '' and never null -- a null here would render as the string 'null'",
    ).toBe('')
  })

  it('an empty field list yields no rows, not one blank row', () => {
    expect(linesFromFields([]), 'an empty wire fabricated a row').toEqual([])
  })
})

describe('addRow (adversarial)', () => {
  it('two consecutive appends get distinct keys, so React never sees a duplicate', () => {
    const once = addRow([])
    const twice = addRow(once)
    const keys = twice.map((r) => r.key)
    expect(keys.length, 'the append fixture is empty').toBe(2)
    expect(new Set(keys).size, 'two appended rows share a key -- React would collapse them').toBe(2)
  })

  it('an appended key does not collide with a key already in the set', () => {
    const seeded: LineRow[] = [{ ...mkRow(1), key: 'n1' }]
    const after = addRow(seeded)
    expect(after[1].key, 'the fresh key collided with the existing n1').not.toBe('n1')
  })
})

describe('linesToPost / lineSetChanged (adversarial)', () => {
  it('the posted order is array order, independent of the wire index each row carries', () => {
    // Rows deliberately out of wire-index order: the body must not re-sort them.
    const rows = [
      mkRow(9, { description: 'Nine' }),
      mkRow(2, { description: 'Two' }),
      mkRow(5, { description: 'Five' }),
    ]
    expect(
      linesToPost(rows).map((l) => l.description),
      'linesToPost re-sorted by wire index; the server assigns line_no from array order',
    ).toEqual(['Nine', 'Two', 'Five'])
  })

  it('every cell of a posted row is carried, not just the ones the grid shows', () => {
    const posted = linesToPost([mkRow(1, { description: 'W', quantity: '1', unit_price: '2.00', line_total: '2.00' })])
    expect(posted.length, 'the single row was dropped').toBe(1)
    expect(Object.keys(posted[0]).sort(), 'the posted body gained or lost a key').toEqual([
      'description',
      'line_total',
      'quantity',
      'unit_price',
    ])
  })

  it('two empty sets are unchanged, and an added row is a change', () => {
    expect(lineSetChanged([], []), 'two empty sets read as changed').toBe(false)
    expect(lineSetChanged([], addRow([])), 'an appended blank row was not detected as a change').toBe(true)
  })

  it('an edit confined to the last row is still detected', () => {
    const wire = [mkRow(1, { quantity: '1' }), mkRow(2, { quantity: '2' })]
    const draft = [wire[0], { ...wire[1], cells: { ...wire[1].cells, quantity: { ...wire[1].cells.quantity, value: '9' } } }]
    expect(lineSetChanged(wire, draft), 'a short-circuit that only checks the first row would miss this').toBe(true)
  })
})
