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
import { describe, expect, it, vi } from 'vitest'

import { fmtDateTime } from './format'
import { initMappingFromHeaders, recognize, restoreMapping } from './mapping'
import {
  applySavedMapping,
  applySuggestion,
  canSubmitAllMappings,
  columnSignature,
  coverageSentence,
  groupByLayout,
  groupOfFile,
  placementBadge,
  rememberMapping,
  restoreGroups,
  restoredNotice,
  returnToAutomatic,
  splitOut,
  suggestGroups,
  type MappingGroup,
} from './mappingGroups'
import type { ImportPreview, SavedMapping, SuggestMapping } from './importApi'
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
    document_id: '5e0a3b21-8d47-4c6f-b912-7fa4e6c30d58',
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
    restored: null,
    suggested: null,
  }
}

// A literal, not applySavedMapping, so a failure points at the function under test.
// `documentId` overrides mkPreview's fixed id so lookups can be told apart.
function mkRestored(
  fileIds: string[],
  cols: string[],
  mapping: Mapping,
  savedAt: string,
  documentId?: string,
): MappingGroup {
  const preview = mkPreview(cols)
  return {
    ...mkGroup(fileIds, mapping, cols),
    restored: { savedAt, mapping: { ...mapping } },
    preview: documentId ? { ...preview, document_id: documentId } : preview,
  }
}

// A fresh seed produces neither placement: invoice_number has no alias, and 'Total' seeds `total`.
const LAGOS_SAVE = { mapping: { invoice_number: 'Invoice No', subtotal: 'Total' }, saved_at: '2026-09-01T10:15:00Z' }

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

  it('SM-FE-03: groupByLayout sets restored to null on every group', () => {
    const groups = groupByLayout([
      { fileId: 'f1', preview: mkPreview(LAGOS_COLS) },
      { fileId: 'f2', preview: mkPreview(TILL_COLS) },
      { fileId: 'f3', preview: mkPreview(LAGOS_COLS) },
    ])
    expect(groups).toHaveLength(2)
    groups.forEach((g) => expect(g.restored).toBeNull())
  })

  it('AIRS-14: groupByLayout seeds suggested to null on every group', () => {
    const groups = groupByLayout([
      { fileId: 'f1', preview: mkPreview(LAGOS_COLS) },
      { fileId: 'f2', preview: mkPreview(TILL_COLS) },
    ])
    expect(groups).toHaveLength(2)
    groups.forEach((g) => expect(g.suggested).toBeNull())
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

  it('SM-FE-07: a split carries the restored state', () => {
    const edited: Mapping = { ...LAGOS_SAVE.mapping, invoice_number: 'Invoice No' }
    const shared = mkRestored(['f1', 'f2'], LAGOS_COLS, edited, LAGOS_SAVE.saved_at)

    const result = splitOut([shared], 'f2')
    const remaining = result.find((g) => g.fileIds.includes('f1'))!
    const split = result.find((g) => g.fileIds.includes('f2'))!

    expect(split.restored).toEqual(shared.restored)
    expect(split.mapping).toEqual(shared.mapping)
    expect(remaining.restored).toEqual(shared.restored)
  })

  it('AIRS-12: a split carries the suggested snapshot forward, same as restored', () => {
    const shared: MappingGroup = {
      ...mkGroup(['f1', 'f2'], initMappingFromHeaders(LAGOS_COLS)),
      suggested: { headerRow: 3, mapping: { invoice_number: 'Invoice No' } },
    }

    const result = splitOut([shared], 'f2')
    const remaining = result.find((g) => g.fileIds.includes('f1'))!
    const split = result.find((g) => g.fileIds.includes('f2'))!

    expect(split.suggested).toEqual(shared.suggested)
    expect(remaining.suggested).toEqual(shared.suggested)
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

describe('applySavedMapping', () => {
  it('SM-FE-04: with no saved mapping returns the same group object', () => {
    const group = mkGroup(['f1'], initMappingFromHeaders(LAGOS_COLS))
    expect(applySavedMapping(group, null)).toBe(group)
  })

  it('SM-FE-05: applySavedMapping restores the saved placements and snapshots them', () => {
    const group = mkGroup(['f1'], initMappingFromHeaders(LAGOS_COLS))
    const result = applySavedMapping(group, LAGOS_SAVE)

    expect(result.mapping).toEqual(restoreMapping(LAGOS_COLS, LAGOS_SAVE.mapping))
    expect(result.mapping.invoice_number).toBe('Invoice No')
    expect(result.mapping.total).toBeNull()
    expect(result.restored).toEqual({ savedAt: LAGOS_SAVE.saved_at, mapping: result.mapping })
    expect(result.id).toBe(group.id)
    expect(result.signature).toBe(group.signature)
    expect(result.fileIds).toBe(group.fileIds)
    expect(result.preview).toBe(group.preview)
    expect(group.restored).toBeNull()
  })
})

describe('returnToAutomatic', () => {
  it('SM-FE-06: reseeds from the column names and clears the restored state', () => {
    const seed = initMappingFromHeaders(LAGOS_COLS)
    const moved: Mapping = { ...seed, total: null, subtotal: 'Total' }
    const restored = mkRestored(['f1'], LAGOS_COLS, moved, LAGOS_SAVE.saved_at)

    const result = returnToAutomatic(restored)

    expect(result.mapping).toEqual(initMappingFromHeaders(LAGOS_COLS))
    expect(result.restored).toBeNull()
    expect(result.id).toBe(restored.id)
    expect(result.signature).toBe(restored.signature)
    expect(result.fileIds).toBe(restored.fileIds)
    expect(result.preview).toBe(restored.preview)
    expect(restored.restored).not.toBeNull()
  })

  it('AIRS-11: also clears a suggested snapshot, not just restored', () => {
    const seeded = mkGroup(['f1'], initMappingFromHeaders(LAGOS_COLS))
    const suggestedGroup: MappingGroup = {
      ...seeded,
      suggested: { headerRow: 1, mapping: { invoice_number: 'Invoice No' } },
    }

    const result = returnToAutomatic(suggestedGroup)

    expect(result.suggested).toBeNull()
  })
})

describe('placementBadge', () => {
  const cols = ['Invoice No', 'Subtotal', 'Total', 'VAT']

  it('SM-FE-08: RESTORED wins over AUTO, a moved placement loses it, and a hand placement on an alias reads AUTO', () => {
    const recognized = recognize(cols)
    const restoredSnapshot: Mapping = { invoice_number: 'Invoice No', total: 'Total' }
    const restoredGroup = mkRestored(['f1'], cols, restoredSnapshot, LAGOS_SAVE.saved_at)

    // moved off the restored placement: neither the old nor the new header reads restored
    const movedTotal: MappingGroup = { ...restoredGroup, mapping: { ...restoredGroup.mapping, total: 'Subtotal' } }
    expect(placementBadge(movedTotal, 'total', 'Subtotal', recognized)).toBeNull()
    expect(placementBadge(movedTotal, 'total', 'Total', recognized)).toBeNull()

    // a fresh group's hand placement on invoice_number (no alias) reads neither badge
    const freshGroup = mkGroup(['f2'], { invoice_number: 'Invoice No', total: 'Total' }, cols)
    expect(placementBadge(freshGroup, 'invoice_number', 'Invoice No', recognized)).toBeNull()

    // a fresh group's alias placement reads AUTO
    expect(placementBadge(freshGroup, 'total', 'Total', recognized)).toBe('auto')

    // a field the saved mapping never touched, hand-placed after restore, reads AUTO
    const withHandVat: MappingGroup = { ...restoredGroup, mapping: { ...restoredGroup.mapping, vat: 'VAT' } }
    expect(placementBadge(withHandVat, 'vat', 'VAT', recognized)).toBe('auto')

    // the restored group's own placements read RESTORED, even where an alias also matches
    expect(placementBadge(restoredGroup, 'invoice_number', 'Invoice No', recognized)).toBe('restored')
    expect(placementBadge(restoredGroup, 'total', 'Total', recognized)).toBe('restored')
  })

  it('AIRS-01: a placement on its suggested header reads SUGGESTED when nothing else matches', () => {
    const recognized = recognize(cols)
    const group: MappingGroup = {
      ...mkGroup(['f1'], { invoice_number: 'Invoice No' }, cols),
      suggested: { headerRow: 1, mapping: { invoice_number: 'Invoice No' } },
    }
    expect(placementBadge(group, 'invoice_number', 'Invoice No', recognized)).toBe('suggested')
  })

  // Unreachable in production (restore runs first and suggestGroups skips a restored
  // group), kept as a total-function unit spec on a hand-built fixture.
  it('AIRS-02: RESTORED wins over SUGGESTED when a placement matches both', () => {
    const recognized = recognize(cols)
    const group: MappingGroup = {
      ...mkGroup(['f1'], { invoice_number: 'Invoice No' }, cols),
      restored: { savedAt: LAGOS_SAVE.saved_at, mapping: { invoice_number: 'Invoice No' } },
      suggested: { headerRow: 1, mapping: { invoice_number: 'Invoice No' } },
    }
    expect(placementBadge(group, 'invoice_number', 'Invoice No', recognized)).toBe('restored')
  })

  it('AIRS-03: SUGGESTED requires the field itself to match, not merely that a suggestion object exists', () => {
    const recognized = recognize(cols)
    // The suggestion only touched `total`; this checks `invoice_number`, which it never
    // recorded a header for -- must read null, not SUGGESTED.
    const group: MappingGroup = {
      ...mkGroup(['f1'], { invoice_number: 'Invoice No', total: 'Total' }, cols),
      suggested: { headerRow: 1, mapping: { total: 'Total' } },
    }
    expect(placementBadge(group, 'invoice_number', 'Invoice No', recognized)).toBeNull()
  })
})

describe('restoredNotice', () => {
  const SAVED_AT = '2026-09-01T10:15:00Z'

  it('SM-FE-09: names the save time, only on a restored group, and survives edits', () => {
    // control: fmtDateTime actually transforms the ISO string
    const formatted = fmtDateTime(SAVED_AT)
    expect(formatted).not.toBe(SAVED_AT)
    expect(formatted).not.toBe('—')

    const freshGroup = mkGroup(['f1'], initMappingFromHeaders(LAGOS_COLS))
    expect(restoredNotice(freshGroup)).toBeNull()

    const restoredGroup = mkRestored(['f1'], LAGOS_COLS, { invoice_number: 'Invoice No' }, SAVED_AT)
    expect(restoredNotice(restoredGroup)).toBe(`Mapping restored from this client's earlier import, saved ${formatted}.`)

    const editedGroup: MappingGroup = { ...restoredGroup, mapping: { ...restoredGroup.mapping, total: 'Total' } }
    expect(restoredNotice(editedGroup)).toBe(`Mapping restored from this client's earlier import, saved ${formatted}.`)

    expect(restoredNotice(returnToAutomatic(restoredGroup))).toBeNull()
  })
})

describe('restoreGroups', () => {
  const flush = () => new Promise((r) => setTimeout(r, 0))

  function deferred(): { promise: Promise<SavedMapping | null>; resolve: (v: SavedMapping | null) => void } {
    let resolve!: (v: SavedMapping | null) => void
    const promise = new Promise<SavedMapping | null>((res) => {
      resolve = res
    })
    return { promise, resolve }
  }

  it("SM-FE-10: looks up one group at a time, in group order, by each group's document id", async () => {
    const a = mkRestored(['f1', 'f2'], LAGOS_COLS, {}, '2026-01-01T00:00:00Z', 'doc-a')
    const b: MappingGroup = { ...mkGroup(['f3'], initMappingFromHeaders(TILL_COLS), TILL_COLS), preview: { ...mkPreview(TILL_COLS), document_id: 'doc-b' } }
    const c = mkRestored(['f4'], ['A', 'B'], {}, '2026-01-01T00:00:00Z', 'doc-c')

    const events: string[] = []
    const pending = new Map<string, ReturnType<typeof deferred>>()
    const lookup = (documentId: string) => {
      events.push(`start:${documentId}`)
      const d = deferred()
      pending.set(documentId, d)
      return d.promise
    }

    const resultPromise = restoreGroups([a, b, c], lookup)

    await flush()
    expect(events).toEqual(['start:doc-a'])
    pending.get('doc-a')!.resolve({ mapping: { invoice_number: 'Invoice No' }, saved_at: '2026-02-01T00:00:00Z' })

    await flush()
    expect(events).toEqual(['start:doc-a', 'start:doc-b'])
    pending.get('doc-b')!.resolve(null)

    await flush()
    expect(events).toEqual(['start:doc-a', 'start:doc-b', 'start:doc-c'])
    pending.get('doc-c')!.resolve({ mapping: { invoice_number: 'Invoice No' }, saved_at: '2026-03-01T00:00:00Z' })

    const result = await resultPromise
    expect(events).toHaveLength(3)
    expect(result[0].restored?.savedAt).toBe('2026-02-01T00:00:00Z')
    expect(result[1]).toBe(b)
    expect(result[2].restored?.savedAt).toBe('2026-03-01T00:00:00Z')
  })

  it('SM-FE-11: a rejected lookup leaves that group on today\'s seed and the run continues', async () => {
    const a = mkGroup(['f1'], initMappingFromHeaders(LAGOS_COLS))
    const b = mkGroup(['f2'], initMappingFromHeaders(TILL_COLS), TILL_COLS)
    const c = mkGroup(['f3'], initMappingFromHeaders(LAGOS_COLS))
    const saveA = { mapping: { invoice_number: 'Invoice No' }, saved_at: '2026-01-01T00:00:00Z' }
    const saveC = { mapping: { invoice_number: 'Invoice No' }, saved_at: '2026-02-01T00:00:00Z' }

    const lookup = vi
      .fn()
      .mockResolvedValueOnce(saveA)
      .mockRejectedValueOnce(new Error('network'))
      .mockResolvedValueOnce(saveC)

    const result = await restoreGroups([a, b, c], lookup)

    expect(result).toHaveLength(3)
    expect(result[0].restored?.savedAt).toBe(saveA.saved_at)
    expect(result[1]).toBe(b)
    expect(result[2].restored?.savedAt).toBe(saveC.saved_at)
  })

  it('SM-FE-12: with no entity, restoreGroups makes no call and returns the groups unchanged', async () => {
    const a = mkGroup(['f1'], initMappingFromHeaders(LAGOS_COLS))
    const b = mkGroup(['f2'], initMappingFromHeaders(TILL_COLS), TILL_COLS)

    const result = await restoreGroups([a, b], null)

    expect(result).toHaveLength(2)
    expect(result[0]).toBe(a)
    expect(result[1]).toBe(b)
  })
})

describe('applySuggestion', () => {
  const cols = ['Invoice No', 'Subtotal', 'Total', 'VAT']

  // Population floor (total placed) before the absence claim (vat unplaced) -- an empty
  // mapping must not pass this assertion vacuously.
  it('AIRS-04: an AI suggestion REPLACES the seed -- a field it omits stays unplaced, not auto-filled by recognize', () => {
    const group = mkGroup(['f1'], initMappingFromHeaders(['X']), ['X'])
    const res: SuggestMapping = {
      source: 'ai',
      header_row: 1,
      columns: cols,
      sample_rows: [['INV-1', '10', '11', '1']],
      rows_total: 1,
      mapping: { invoice_number: 'Invoice No', total: 'Total' }, // omits vat on purpose
      saved_at: null,
    }

    const result = applySuggestion(group, res)

    expect(result.mapping.total).toBe('Total')
    expect(result.mapping.vat).toBeNull()

    const recognized = recognize(cols)
    const withHandVat: MappingGroup = { ...result, mapping: { ...result.mapping, vat: 'VAT' } }
    expect(placementBadge(withHandVat, 'vat', 'VAT', recognized)).toBe('auto')
  })

  it('AIRS-08: a "none" response is the identity -- the same object back', () => {
    const group = mkGroup(['f1'], initMappingFromHeaders(LAGOS_COLS))
    const noneRes: SuggestMapping = {
      source: 'none',
      header_row: 1,
      columns: LAGOS_COLS,
      sample_rows: [],
      rows_total: 0,
      mapping: {},
      saved_at: null,
    }
    expect(applySuggestion(group, noneRes)).toBe(group)
  })

  it('AIRS-09: preview, sample rows, and row count all come from the response, and signature is recomputed to match', () => {
    const group = mkGroup(['f1'], initMappingFromHeaders(LAGOS_COLS))
    const res: SuggestMapping = {
      source: 'ai',
      header_row: 3,
      columns: TILL_COLS,
      sample_rows: [['INV-9', 'USD']],
      rows_total: 42,
      mapping: { invoice_number: 'Invoice No' },
      saved_at: null,
    }

    const result = applySuggestion(group, res)

    expect(result.preview.columns).toEqual(TILL_COLS)
    expect(result.preview.sample_rows).toEqual(res.sample_rows)
    expect(result.preview.rows_total).toBe(42)
    expect(result.signature).toBe(columnSignature(result.preview.columns))
  })

  it('AIRS-10: a "saved" response sets restored and leaves suggested null, not the other way round', () => {
    const group = mkGroup(['f1'], initMappingFromHeaders(LAGOS_COLS))
    const res: SuggestMapping = {
      source: 'saved',
      header_row: 1,
      columns: LAGOS_COLS,
      sample_rows: [],
      rows_total: 1,
      mapping: { invoice_number: 'Invoice No' },
      saved_at: '2026-05-01T00:00:00Z',
    }

    const result = applySuggestion(group, res)

    expect(result.restored).not.toBeNull()
    expect(result.restored?.mapping.invoice_number).toBe('Invoice No')
    expect(result.suggested).toBeNull()
  })

  it('AIRS-13: a "saved" response with a null saved_at still yields a string savedAt, rendered as "—" by restoredNotice', () => {
    const group = mkGroup(['f1'], initMappingFromHeaders(LAGOS_COLS))
    const res: SuggestMapping = {
      source: 'saved',
      header_row: 1,
      columns: LAGOS_COLS,
      sample_rows: [],
      rows_total: 1,
      mapping: { invoice_number: 'Invoice No' },
      saved_at: null,
    }

    const result = applySuggestion(group, res)

    expect(result.restored).not.toBeNull()
    expect(typeof result.restored?.savedAt).toBe('string')
    expect(restoredNotice(result)).toBe(`Mapping restored from this client's earlier import, saved ${fmtDateTime('')}.`)
  })
})

describe('suggestGroups', () => {
  const flush = () => new Promise((r) => setTimeout(r, 0))

  function deferred(): { promise: Promise<SuggestMapping>; resolve: (v: SuggestMapping) => void } {
    let resolve!: (v: SuggestMapping) => void
    const promise = new Promise<SuggestMapping>((res) => {
      resolve = res
    })
    return { promise, resolve }
  }

  const aiRes = (mapping: Mapping, columns = LAGOS_COLS): SuggestMapping => ({
    source: 'ai',
    header_row: 1,
    columns,
    sample_rows: [],
    rows_total: 0,
    mapping: mapping as Record<string, string>,
    saved_at: null,
  })

  it("AIRS-05: suggests one group at a time, in group order, by each group's document id", async () => {
    const a: MappingGroup = { ...mkGroup(['f1'], initMappingFromHeaders(LAGOS_COLS)), preview: { ...mkPreview(LAGOS_COLS), document_id: 'doc-a' } }
    const b: MappingGroup = { ...mkGroup(['f2'], initMappingFromHeaders(TILL_COLS), TILL_COLS), preview: { ...mkPreview(TILL_COLS), document_id: 'doc-b' } }
    const c: MappingGroup = { ...mkGroup(['f3'], initMappingFromHeaders(LAGOS_COLS)), preview: { ...mkPreview(LAGOS_COLS), document_id: 'doc-c' } }

    const events: string[] = []
    const pending = new Map<string, ReturnType<typeof deferred>>()
    const suggest = (documentId: string) => {
      events.push(`start:${documentId}`)
      const d = deferred()
      pending.set(documentId, d)
      return d.promise
    }

    const resultPromise = suggestGroups([a, b, c], suggest)

    await flush()
    expect(events).toEqual(['start:doc-a'])
    pending.get('doc-a')!.resolve(aiRes({ invoice_number: 'Invoice No' }))

    await flush()
    expect(events).toEqual(['start:doc-a', 'start:doc-b'])
    pending.get('doc-b')!.resolve(aiRes({ invoice_number: 'Invoice No' }, TILL_COLS))

    await flush()
    expect(events).toEqual(['start:doc-a', 'start:doc-b', 'start:doc-c'])
    pending.get('doc-c')!.resolve(aiRes({ invoice_number: 'Invoice No' }))

    const result = await resultPromise
    expect(events).toHaveLength(3)
    expect(result[0].suggested?.mapping.invoice_number).toBe('Invoice No')
    expect(result[1].suggested?.mapping.invoice_number).toBe('Invoice No')
    expect(result[2].suggested?.mapping.invoice_number).toBe('Invoice No')
  })

  it('AIRS-06: skips a group that already has a restored snapshot; only a null restored gets a suggestion', async () => {
    const untouched = mkGroup(['f1'], initMappingFromHeaders(LAGOS_COLS))
    const restoredGroup = mkRestored(['f2'], LAGOS_COLS, { invoice_number: 'Invoice No' }, LAGOS_SAVE.saved_at, 'doc-restored')

    const suggest = vi.fn().mockResolvedValue(aiRes({ invoice_number: 'Invoice No' }))

    const result = await suggestGroups([restoredGroup, untouched], suggest)

    expect(suggest).toHaveBeenCalledTimes(1)
    expect(suggest).toHaveBeenCalledWith(untouched.preview.document_id)
    expect(result[0]).toBe(restoredGroup)
    expect(result[1].suggested?.mapping.invoice_number).toBe('Invoice No')
  })

  it('AIRS-07: a rejected suggestion leaves that group on its current seed and the run continues to the next group', async () => {
    const a = mkGroup(['f1'], initMappingFromHeaders(LAGOS_COLS))
    const b = mkGroup(['f2'], initMappingFromHeaders(TILL_COLS), TILL_COLS)
    const c = mkGroup(['f3'], initMappingFromHeaders(LAGOS_COLS))

    const suggest = vi
      .fn()
      .mockResolvedValueOnce(aiRes({ invoice_number: 'Invoice No' }))
      .mockRejectedValueOnce(new Error('network'))
      .mockResolvedValueOnce(aiRes({ invoice_number: 'Invoice No' }))

    const result = await suggestGroups([a, b, c], suggest)

    expect(result).toHaveLength(3)
    expect(result[0].suggested?.mapping.invoice_number).toBe('Invoice No')
    expect(result[1]).toBe(b)
    expect(result[2].suggested?.mapping.invoice_number).toBe('Invoice No')
  })
})

describe('rememberMapping', () => {
  it('SM-FE-15: only an untouched restored group opts out of remembering', () => {
    const fresh = mkGroup(['f1'], initMappingFromHeaders(LAGOS_COLS))
    expect(rememberMapping(fresh)).toBe(true)

    const untouched = mkRestored(['f1'], LAGOS_COLS, { ...LAGOS_SAVE.mapping }, LAGOS_SAVE.saved_at)

    const moved: MappingGroup = { ...untouched, mapping: { ...untouched.mapping, subtotal: null } }
    expect(rememberMapping(moved)).toBe(true)

    const newlyPlaced: MappingGroup = { ...untouched, mapping: { ...untouched.mapping, total: 'Total' } }
    expect(rememberMapping(newlyPlaced)).toBe(true)

    expect(rememberMapping(returnToAutomatic(untouched))).toBe(true)

    const invoiceNumberMoved: MappingGroup = { ...untouched, mapping: { ...untouched.mapping, invoice_number: null } }
    expect(rememberMapping(invoiceNumberMoved)).toBe(true)

    // the untouched restore itself opts out
    expect(rememberMapping(untouched)).toBe(false)

    // edited then reverted to the exact saved values: still opts out
    const midEdit: MappingGroup = { ...untouched, mapping: { ...untouched.mapping, subtotal: null } }
    const revertedBack: MappingGroup = { ...midEdit, mapping: { ...midEdit.mapping, subtotal: 'Total' } }
    expect(rememberMapping(revertedBack)).toBe(false)
  })

  it('SM-FE-16: after a split, each copy of a restored group decides for itself', () => {
    const snapshot: Mapping = { ...LAGOS_SAVE.mapping }
    const shared = mkRestored(['fB', 'fA'], LAGOS_COLS, snapshot, LAGOS_SAVE.saved_at)

    const split1 = splitOut([shared], 'fB')
    const groupB1 = split1.find((g) => g.fileIds.includes('fB'))!
    const groupA1 = split1.find((g) => g.fileIds.includes('fA'))!
    const editedB: MappingGroup = { ...groupB1, mapping: { ...groupB1.mapping, subtotal: null } }
    expect(rememberMapping(editedB)).toBe(true)

    const split2 = splitOut([shared], 'fB')
    const groupA2 = split2.find((g) => g.fileIds.includes('fA'))!
    const groupB2 = split2.find((g) => g.fileIds.includes('fB'))!
    const editedA: MappingGroup = { ...groupA2, mapping: { ...groupA2.mapping, subtotal: null } }
    expect(rememberMapping(editedA)).toBe(true)

    // the untouched copy in each split keeps opting out
    expect(rememberMapping(groupA1)).toBe(false)
    expect(rememberMapping(groupB2)).toBe(false)
  })
})

// ============================================================================
// QA Mode B (task-309) — adversarial/edge coverage beyond the architect's Test
// Specs table (BULK-04-1..12). Every mutation-tested spec above (BULK-04-5,
// BULK-04-9, BULK-04-10, BULK-04-11) was hand-verified to turn red on the
// targeted line before this coverage was added; these specs target failure
// modes the original table left unexercised.
// ============================================================================

describe('columnSignature edges (QA Mode B)', () => {
  // Falsification: an impl that signs only column COUNT or only the header set,
  // rather than the full ordered array — both would wrongly equate a file with one
  // extra trailing column to its shorter sibling.
  it('a trailing extra column produces a different signature from its shorter sibling', () => {
    const shorter = columnSignature(['Invoice No', 'Total'])
    const longer = columnSignature(['Invoice No', 'Total', 'Currency'])
    expect(shorter).not.toBe(longer)
  })

  // Falsification: an impl that de-duplicates headers before signing (e.g. via a Set),
  // which would wrongly equate a file with a repeated header to its deduped sibling —
  // and CreateMapping.tsx keys its column grid by INDEX specifically because duplicate
  // headers are preserved verbatim, so the signature must preserve them too.
  it('a duplicate header is not signature-equal to its deduped form', () => {
    const withDup = columnSignature(['A', 'A', 'B'])
    const deduped = columnSignature(['A', 'B'])
    expect(withDup).not.toBe(deduped)
  })

  // Falsification: an impl that strips falsy/empty entries before signing, which would
  // wrongly equate a file with a blank-named column to one without that column at all —
  // isMappableColumn treats '' as unmappable but groupByLayout must still see it as a
  // real, present column for grouping purposes.
  it('an empty-string column is not signature-equal to a file missing that column', () => {
    const withBlank = columnSignature(['A', ''])
    const withoutBlank = columnSignature(['A'])
    expect(withBlank).not.toBe(withoutBlank)
  })
})

describe('groupByLayout — whitespace vs blank headers are never collapsed (QA Mode B)', () => {
  // Falsification: an impl that trims headers before signing (e.g. `h.trim()`), which
  // would collapse a whitespace-only header and a blank header into the same group even
  // though lib/importFlow.ts's isMappableColumn treats them oppositely (blank
  // unmappable, whitespace mappable) — a merged group here would misstate which file's
  // columns are actually being shown.
  it('a whitespace-only header and an empty header stay in two separate groups', () => {
    const groups = groupByLayout([
      { fileId: 'f1', preview: mkPreview(['A', ' ']) },
      { fileId: 'f2', preview: mkPreview(['A', '']) },
    ])
    expect(groups).toHaveLength(2)
    expect(groups[0].fileIds).toEqual(['f1'])
    expect(groups[1].fileIds).toEqual(['f2'])
  })
})

describe('groupByLayout — zero previews (QA Mode B)', () => {
  // Falsification: an impl that assumes `previewed` is non-empty (e.g. reads
  // previewed[0] unconditionally for some fast-path), which would throw instead of
  // returning the empty run readAllColumns can, in principle, hand it.
  it('an empty previewed list returns an empty array, not a throw', () => {
    expect(() => groupByLayout([])).not.toThrow()
    expect(groupByLayout([])).toEqual([])
  })
})

describe('splitOut churn (QA Mode B)', () => {
  // Falsification: an impl whose single-file guard checks something other than the
  // CURRENT group's fileIds.length (e.g. a stale count captured before the first
  // split), which would still try to split a now-lone file on the second call.
  it('splitting the same file a second time, now alone, is a no-op', () => {
    const shared = mkGroup(['f1', 'f2'], initMappingFromHeaders(LAGOS_COLS))
    const once = splitOut([shared], 'f2')
    expect(once).toHaveLength(2)
    const twice = splitOut(once, 'f2')
    expect(twice).toHaveLength(2)
    expect(twice).toEqual(once)
  })

  // Falsification: an impl that snapshots the mapping once at the FIRST split and
  // reuses it for every later split off the same original group (rather than reading
  // the shared group's CURRENT mapping at each split's own moment), or one that lets a
  // later edit to the shared group retroactively mutate an already-split-off group's
  // mapping (i.e. shares a reference instead of copying).
  it('three sequential splits off a 3-file group each freeze the mapping the shared group held at THAT split, not an earlier or later one', () => {
    const seed = initMappingFromHeaders(LAGOS_COLS)
    const afterFirstPlacement: Mapping = { ...seed, invoice_number: 'Invoice No' }
    let groups: MappingGroup[] = [mkGroup(['f1', 'f2', 'f3'], afterFirstPlacement)]

    // Split f3 off first, while the shared group holds afterFirstPlacement.
    groups = splitOut(groups, 'f3')
    const f3Group = groupOfFile(groups, 'f3')!
    expect(f3Group.mapping).toEqual(afterFirstPlacement)

    // The operator places a SECOND field on the still-shared group (f1, f2) before the
    // next split -- f3's already-split group must not see this. `subtotal` (not
    // `total`) on purpose: mapping.ts's ALIAS table auto-recognizes 'Total' for the
    // `total` field, so `total` would already be identical between the two snapshots
    // and this assertion would pass vacuously; `subtotal` has no ALIAS entry.
    const sharedGroup = groupOfFile(groups, 'f1')!
    const afterSecondPlacement: Mapping = { ...sharedGroup.mapping, subtotal: 'Total' }
    groups = groups.map((g) => (g.id === sharedGroup.id ? { ...g, mapping: afterSecondPlacement } : g))

    // Split f2 off now -- its frozen mapping must be afterSecondPlacement, never
    // afterFirstPlacement (the earlier snapshot f3 got) and never a fresh re-seed.
    groups = splitOut(groups, 'f2')
    const f2Group = groupOfFile(groups, 'f2')!
    expect(f2Group.mapping).toEqual(afterSecondPlacement)
    expect(f2Group.mapping).not.toEqual(afterFirstPlacement)

    // f1 is now alone -- splitting it is a no-op, but it still carries
    // afterSecondPlacement (the mapping the shared group held when it became lone).
    groups = splitOut(groups, 'f1')
    const f1Group = groupOfFile(groups, 'f1')!
    expect(f1Group.fileIds).toEqual(['f1'])
    expect(f1Group.mapping).toEqual(afterSecondPlacement)

    // End state: three single-file groups, and f3's earlier snapshot was never
    // retroactively touched by either later edit.
    expect(groups).toHaveLength(3)
    groups.forEach((g) => expect(g.fileIds).toHaveLength(1))
    expect(groupOfFile(groups, 'f3')!.mapping).toEqual(afterFirstPlacement)
  })
})

describe('canSubmitAllMappings — empty group list (QA Mode B)', () => {
  // Pinned answer: an empty run has nothing ready to submit. Falsification: an impl
  // whose `.every()` over an empty array vacuously returns true with no length guard,
  // which would report an empty run as "ready to submit".
  it('an empty group list is NOT ready to submit', () => {
    expect(canSubmitAllMappings([])).toBe(false)
  })
})

describe('coverageSentence — missing name lookup (QA Mode B)', () => {
  // Falsification: an impl that reads `names[id]` directly with no fallback, which
  // would interpolate the literal string "undefined" into the sentence for any fileId
  // the lookup doesn't have an entry for yet (e.g. a render racing the names map).
  it('a fileId absent from the names lookup falls back to the id, never the literal "undefined"', () => {
    const group = mkGroup(['f9-unlisted'], initMappingFromHeaders(LAGOS_COLS))
    const sentence = coverageSentence(group, {})
    expect(sentence).not.toContain('undefined')
    expect(sentence).toContain('f9-unlisted')
  })
})

describe('saved-mapping helpers — adversarial (QA Mode B)', () => {
  it("SM-FE-QA-1: applySavedMapping leaves the input group's mapping untouched", () => {
    const seed = initMappingFromHeaders(LAGOS_COLS)
    const before = { ...seed }
    const group = mkGroup(['f1'], seed)

    const result = applySavedMapping(group, LAGOS_SAVE)

    expect(result.mapping.invoice_number).toBe('Invoice No')
    expect(group.mapping).toEqual(before)
    expect(group.mapping.invoice_number).toBeNull()
  })

  it('SM-FE-QA-2: with no lookup it resolves the very same array; with no groups it makes no call', async () => {
    const groups = [mkGroup(['f1'], initMappingFromHeaders(LAGOS_COLS))]
    expect(await restoreGroups(groups, null)).toBe(groups)

    const lookup = vi.fn()
    expect(await restoreGroups([], lookup)).toEqual([])
    expect(lookup).not.toHaveBeenCalled()
  })

  it('SM-FE-QA-3: a mapping that lost a key the snapshot placed still remembers', () => {
    const untouched = mkRestored(['f1'], LAGOS_COLS, { ...LAGOS_SAVE.mapping }, LAGOS_SAVE.saved_at)
    const lostKey: MappingGroup = { ...untouched, mapping: { invoice_number: untouched.mapping.invoice_number } }

    expect(rememberMapping(untouched)).toBe(false)
    expect(rememberMapping(lostKey)).toBe(true)
  })

  it('SM-FE-QA-4: a group restored through restoreGroups reads RESTORED and opts out until edited', async () => {
    const recognized = recognize(LAGOS_COLS)
    const [restored] = await restoreGroups([mkGroup(['f1'], initMappingFromHeaders(LAGOS_COLS))], async () => LAGOS_SAVE)

    expect(placementBadge(restored, 'invoice_number', 'Invoice No', recognized)).toBe('restored')
    expect(placementBadge(restored, 'subtotal', 'Total', recognized)).toBe('restored')
    const notice = `Mapping restored from this client's earlier import, saved ${fmtDateTime(LAGOS_SAVE.saved_at)}.`
    expect(restoredNotice(restored)).toBe(notice)
    expect(rememberMapping(restored)).toBe(false)

    const edited: MappingGroup = { ...restored, mapping: { ...restored.mapping, subtotal: null, total: 'Total' } }
    expect(placementBadge(edited, 'total', 'Total', recognized)).toBe('auto')
    expect(restoredNotice(edited)).toBe(notice)
    expect(rememberMapping(edited)).toBe(true)
  })
})
