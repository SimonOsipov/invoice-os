// RED specs (BULK-01-04, task-309, BULK-04-1..12) — pin lib/mappingGroups.ts's grouping
// contract before the executor implements the bodies. Core AC 3 (whole), decision
// [shared-mapping-shown]: files sharing an identical column layout are mapped once, the
// mapping step states which files that mapping covers, and the operator can split any
// file out to map it separately — sharing is never silent.
//
// vitest environment is 'node' (vitest.config.ts:5) — no jsdom, no Testing Library. No
// spec here touches a DOM or a component.
//
// Spec map:
//   BULK-04-1   identical layouts group                                  (AC1,2)
//   BULK-04-2   different layouts do not                                 (AC1)
//   BULK-04-3   order is part of the signature                           (AC1)
//   BULK-04-4   case is part of the signature                            (AC1)
//   BULK-04-5   grouping order is first-appearance                      (AC2)
//   BULK-04-6   the coverage sentence is never absent                    (AC3)
//   BULK-04-7   the sentence names every covered file                    (AC3)
//   BULK-04-8   split moves exactly one file                             (AC4)
//   BULK-04-9   split preserves the operator's work                      (AC4)
//   BULK-04-10  split of a lone file is a no-op                          (AC4)
//   BULK-04-11  the run gate is all groups                               (AC5)
//   BULK-04-12  each file gets its own group's mapping                   (AC4)
//
// BULK-04-13 (Object.keys(STAGE_OF) unchanged) lives in importFlow.test.ts, not here —
// it is a shipped guard this subtask must keep green, not author.
//
// Every spec below currently fails because columnSignature/groupByLayout/splitOut/
// coverageSentence/groupOfFile/canSubmitAllMappings's stub bodies throw
// new Error('not implemented') before ever returning anything — that IS the correct RED
// reason (assertion / not-implemented), not an import/compile error.
import { describe, expect, it } from 'vitest'

import { initMappingFromHeaders } from './mapping'
import {
  canSubmitAllMappings,
  columnSignature,
  coverageSentence,
  groupByLayout,
  groupOfFile,
  splitOut,
  type MappingGroup,
} from './mappingGroups'
import type { ImportPreview } from './importApi'
import type { Mapping } from '../types'

// Fixture filenames deliberately avoid the two single-quoted CreateStep-literal words
// importFlow.test.ts's STEPS-7 (:363-369) and QA-MOCK-3 (:538-546) source-scan every
// .ts/.tsx under frontend/app/src for, repo-wide (one names a retired IMPORT_STEPS
// label, the other a retired CreateStep union member) — BULK-01-03's QA hit exactly
// this collision with a fixture named after the first of the two.
const LAGOS_COLS = ['Invoice No', 'Total']
const ABUJA_COLS = ['Invoice No', 'Total'] // same layout as lagos.csv, different file
const TILL_COLS = ['Invoice No', 'Currency'] // different layout

function mkPreview(columns: string[]): ImportPreview {
  return {
    format: 'csv',
    delimiter: ',',
    encoding: 'utf-8',
    columns,
    sample_rows: [columns.map((_, i) => `v${i}`)],
    rows_total: 1,
  }
}

// Builds a MappingGroup fixture directly, WITHOUT calling the (stubbed, throwing)
// columnSignature — these specs are not exercising columnSignature, so its signature
// value is opaque here; a hardcoded string keeps a failure in one of these tests
// attributable to the function actually under test, not to an unrelated stub.
function mkGroup(fileIds: string[], mapping: Mapping, columns: string[] = LAGOS_COLS): MappingGroup {
  return {
    id: `g-${fileIds.join('-')}`,
    signature: JSON.stringify(columns),
    fileIds,
    preview: mkPreview(columns),
    mapping,
  }
}

describe('columnSignature', () => {
  // BULK-04-3 — falsification: an impl that sorts columns before signing, which would
  // wrongly merge two files whose columns are in a different order.
  it('BULK-04-3: order is part of the signature', () => {
    const ab = columnSignature(['A', 'B'])
    const ba = columnSignature(['B', 'A'])
    expect(ab).not.toBe(ba)
  })

  // BULK-04-4 — falsification: an impl that case-folds headers before signing, which
  // would wrongly merge 'Total' and 'total' into one group.
  it('BULK-04-4: case is part of the signature', () => {
    const upper = columnSignature(['Total'])
    const lower = columnSignature(['total'])
    expect(upper).not.toBe(lower)
  })
})

describe('groupByLayout', () => {
  // BULK-04-1 — falsification: an impl that never merges anything (one group per file)
  // or one that seeds a blank mapping instead of the shipped initMappingFromHeaders.
  it('BULK-04-1: two files with identical, ordered, same-case columns share one group', () => {
    const groups = groupByLayout([
      { fileId: 'f1', preview: mkPreview(LAGOS_COLS) },
      { fileId: 'f2', preview: mkPreview(ABUJA_COLS) },
    ])
    expect(groups).toHaveLength(1)
    expect(groups[0].fileIds).toEqual(['f1', 'f2'])
    expect(groups[0].mapping).toEqual(initMappingFromHeaders(LAGOS_COLS))
  })

  // BULK-04-2 — falsification: an impl that merges everything into one group regardless
  // of layout.
  it('BULK-04-2: two files with different columns get two groups, one file each', () => {
    const groups = groupByLayout([
      { fileId: 'f1', preview: mkPreview(LAGOS_COLS) },
      { fileId: 'f2', preview: mkPreview(TILL_COLS) },
    ])
    expect(groups).toHaveLength(2)
    expect(groups[0].fileIds).toEqual(['f1'])
    expect(groups[1].fileIds).toEqual(['f2'])
  })

  // BULK-04-5 — falsification: an impl that groups by LAST appearance (would put the
  // X-group after the Y-group) or that re-sorts groups by signature/size.
  it('BULK-04-5: grouping order is first-appearance, not last', () => {
    const groups = groupByLayout([
      { fileId: 'f1', preview: mkPreview(LAGOS_COLS) }, // X
      { fileId: 'f2', preview: mkPreview(TILL_COLS) }, // Y
      { fileId: 'f3', preview: mkPreview(LAGOS_COLS) }, // X again
    ])
    expect(groups).toHaveLength(2)
    expect(groups[0].fileIds).toEqual(['f1', 'f3'])
    expect(groups[1].fileIds).toEqual(['f2'])
  })
})

describe('coverageSentence', () => {
  const names: Record<string, string> = { f1: 'lagos.csv', f2: 'abuja.csv', f3: 'till.csv' }

  // BULK-04-6 — falsification: an impl that renders the sentence only when fileIds.length
  // > 1, leaving a lone-file group's screen silent about what it covers
  // ([coverage-sentence-is-unconditional]).
  it('BULK-04-6: renders for a group of one, naming that file', () => {
    const group = mkGroup(['f1'], initMappingFromHeaders(LAGOS_COLS))
    const sentence = coverageSentence(group, names)
    expect(sentence.length).toBeGreaterThan(0)
    expect(sentence).toContain('lagos.csv')
  })

  // BULK-04-7 — falsification: an impl that names only the first file, or a count
  // ("3 files") instead of every name.
  it('BULK-04-7: names every covered file in a 3-file group', () => {
    const group = mkGroup(['f1', 'f2', 'f3'], initMappingFromHeaders(LAGOS_COLS))
    const sentence = coverageSentence(group, names)
    expect(sentence).toContain('lagos.csv')
    expect(sentence).toContain('abuja.csv')
    expect(sentence).toContain('till.csv')
  })
})

describe('splitOut', () => {
  // BULK-04-8 — falsification: an impl that removes the file without appending a new
  // group, or that moves the wrong file.
  it('BULK-04-8: splits exactly one file out of a 3-file group', () => {
    const shared = mkGroup(['f1', 'f2', 'f3'], initMappingFromHeaders(LAGOS_COLS))
    const result = splitOut([shared], 'f2')

    expect(result).toHaveLength(2)
    const remaining = result.find((g) => g.fileIds.includes('f1'))!
    const split = result.find((g) => g.fileIds.includes('f2'))!
    expect(remaining.fileIds).toEqual(['f1', 'f3'])
    expect(split.fileIds).toEqual(['f2'])
  })

  // BULK-04-9 — the fixture is built so a fresh initMappingFromHeaders(LAGOS_COLS) and
  // the shared group's actual mapping provably DIFFER: invoice_number is never
  // auto-placed by recognize() (mapping.ts's ALIAS table deliberately omits it), so
  // hand-placing it here is exactly the placement a fresh re-seed would NOT produce.
  // Falsification: an impl that re-seeds the split group via initMappingFromHeaders
  // instead of copying the shared group's mapping at split time
  // ([split-copies-the-mapping]) — invoice_number would come back null.
  it("BULK-04-9: the split group keeps the operator's hand-placed mapping, not a fresh re-seed", () => {
    const freshSeed = initMappingFromHeaders(LAGOS_COLS)
    expect(freshSeed.invoice_number).toBeNull() // sanity: a fresh seed would NOT have it

    const handPlaced: Mapping = { ...freshSeed, invoice_number: 'Invoice No' }
    const shared = mkGroup(['f1', 'f2'], handPlaced)

    const result = splitOut([shared], 'f2')
    const split = result.find((g) => g.fileIds.includes('f2'))!

    expect(split.mapping).toEqual(handPlaced)
    expect(split.mapping).not.toEqual(freshSeed)
  })

  // BULK-04-10 — falsification: an impl that appends an empty/duplicate group even when
  // there is nothing to split off a lone file.
  it('BULK-04-10: is a no-op on a single-file group', () => {
    const lone = mkGroup(['f1'], initMappingFromHeaders(LAGOS_COLS))
    const result = splitOut([lone], 'f1')
    expect(result).toHaveLength(1)
    expect(result).toEqual([lone])
  })
})

describe('groupOfFile', () => {
  // BULK-04-12 — falsification: an impl that keeps resolving f2 to the original
  // (now-remaining) group instead of the newly-appended split group.
  it('BULK-04-12: resolves to the NEW split group after a split, not the group f1 is still in', () => {
    const shared = mkGroup(['f1', 'f2'], initMappingFromHeaders(LAGOS_COLS))
    const result = splitOut([shared], 'f2')

    const groupForF1 = groupOfFile(result, 'f1')
    const groupForF2 = groupOfFile(result, 'f2')

    expect(groupForF2).not.toBeNull()
    expect(groupForF2!.fileIds).toEqual(['f2'])
    expect(groupForF2).not.toBe(groupForF1)
  })
})

describe('canSubmitAllMappings', () => {
  // BULK-04-11 — falsification: an impl that only checks the FIRST group (would return
  // true here even though f2 lacks invoice_number), or one that adds a second gate
  // beyond invoice_number (would return false for the single-field group below even
  // though canSubmitMapping alone accepts it).
  it('BULK-04-11: false when any group lacks invoice_number, true only when every group has it', () => {
    const withInvoiceNumber = mkGroup(['f1'], { invoice_number: 'Invoice No' })
    const withoutInvoiceNumber = mkGroup(['f2'], { invoice_number: null })
    expect(canSubmitAllMappings([withInvoiceNumber, withoutInvoiceNumber])).toBe(false)

    const alsoHasIt = mkGroup(['f2'], { invoice_number: 'Invoice No' })
    expect(canSubmitAllMappings([withInvoiceNumber, alsoHasIt])).toBe(true)

    // Positive companion: invoice_number ALONE is sufficient — no second gate.
    expect(canSubmitAllMappings([mkGroup(['f3'], { invoice_number: 'Invoice No' })])).toBe(true)
  })
})
