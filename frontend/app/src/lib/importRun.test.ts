// RED specs (BULK-01-03, task-310, Test-first) — pin the multi-file selection module's
// contract before the executor implements the bodies in importRun.ts. Plan's Test Specs
// table (BULK-03-1..10 here, BULK-03-11 in importFlow.test.ts) is authoritative.
//
// vitest environment is 'node' (vitest.config.ts:5) — no jsdom, no Testing Library. File
// is a real node global with no polyfill, same idiom as importFlow.test.ts:70-71
// (`new File([], 'invoice.csv')`); no spec here touches a DOM or a component.
//
// Spec map (task-310 Test Specs table):
//   BULK-03-1   addFiles appends incoming files, preserving order                  (AC1)
//   BULK-03-2   cap is 5; over-cap drop names the cap and how many were dropped     (AC1)
//   BULK-03-3   the cap counts the EXISTING selection, not just the incoming batch  (AC1)
//   BULK-03-4   exactly at cap is accepted silently — refusal null                  (AC1)
//   BULK-03-5   zero incoming files is a no-op                                      (AC1)
//   BULK-03-6   removeFile removes by id, preserves order of the rest               (AC2)
//   BULK-03-7   removeFile with an unknown id is a no-op                            (AC2)
//   BULK-03-8   a bad-extension file is listed, never dropped by addFiles           (AC3)
//   BULK-03-9   one bad-extension file blocks the whole selection's read gate       (AC3,4)
//   BULK-03-10  an empty selection cannot read columns                             (AC3,4)
// BULK-03-11 (canReadColumns's no-entity contract is unmoved) lives in
// importFlow.test.ts — it is a regression assertion on an already-shipped function, not a
// spec against this module.
//
// Every spec below currently fails because addFiles/removeFile/canReadColumnsAll's stub
// bodies throw `new Error('not implemented')` before ever returning anything — that IS
// the correct RED reason (assertion / not-implemented, surfaced as a thrown error vitest
// reports as a test failure), never an import/compile error. Same precedent as
// importFlow.ts's computeNoEntity STUB (task-304, INVCR-01-19) and this same file's own
// wizardHeader/canReadColumns/canStartImport stubs before M4-08-04 landed.
import { describe, expect, it } from 'vitest'

import { MAX_RUN_FILES, addFiles, canReadColumnsAll, removeFile } from './importRun'
import type { PickedFile } from './importRun'

// Builds fixture PickedFile[] with stable, human-readable ids derived from the file's own
// name — good enough for these specs, which only ever look an id up by the very entry
// that owns it. The real id-generation strategy (crypto.randomUUID or otherwise) is
// addFiles's to decide; nothing here assumes a particular scheme for FRESH entries.
function mkPicked(names: string[]): PickedFile[] {
  return names.map((name) => ({ id: `existing:${name}`, file: new File([], name) }))
}

describe('addFiles — ordering and the five-file cap (BULK-03-1..5)', () => {
  // BULK-03-1 — falsification: an impl that reorders, dedupes, or silently truncates a
  // batch that never crosses the cap at all.
  it('BULK-03-1: appends incoming files onto an empty selection, preserving order', () => {
    const a = new File([], 'a.csv')
    const b = new File([], 'b.csv')
    const c = new File([], 'c.csv')
    const result = addFiles([], [a, b, c])
    expect(result.files.map((pf) => pf.file.name)).toEqual(['a.csv', 'b.csv', 'c.csv'])
    expect(result.refusal).toBeNull()
  })

  // BULK-03-2 — falsification: an impl that silently truncates to 5 without setting
  // `refusal`, or one whose refusal text omits the cap (5) or the dropped count (1).
  it('BULK-03-2: offering 6 files caps the selection at 5 and names the cap plus the 1 dropped', () => {
    const six = Array.from({ length: 6 }, (_unused, i) => new File([], `f${i}.csv`))
    const result = addFiles([], six)
    expect(result.files).toHaveLength(MAX_RUN_FILES)
    expect(result.files.map((pf) => pf.file.name)).toEqual(['f0.csv', 'f1.csv', 'f2.csv', 'f3.csv', 'f4.csv'])
    expect(result.refusal).not.toBeNull()
    expect(result.refusal).toContain(String(MAX_RUN_FILES))
    expect(result.refusal).toContain('1')
  })

  // BULK-03-3 — falsification: an impl that caps only the INCOMING batch (e.g. allows all
  // 2 of `incoming` through because 2 <= 5, ignoring the 4 already picked) — the cap must
  // be over `current.length + incoming.length`, not incoming alone.
  it('BULK-03-3: the cap counts files already in the selection, not just the incoming batch', () => {
    const fourAlready = mkPicked(['a.csv', 'b.csv', 'c.csv', 'd.csv'])
    const x = new File([], 'x.csv')
    const y = new File([], 'y.csv')
    const result = addFiles(fourAlready, [x, y])
    expect(result.files).toHaveLength(MAX_RUN_FILES)
    expect(result.files.map((pf) => pf.file.name)).toEqual(['a.csv', 'b.csv', 'c.csv', 'd.csv', 'x.csv'])
    expect(result.refusal).not.toBeNull()
    expect(result.refusal).toContain('1')
  })

  // BULK-03-4 — falsification: an impl whose refusal fires on `>=` the cap instead of
  // `>`, refusing the exact-cap case that Core AC 1 says must be silent.
  it('BULK-03-4: offering exactly 5 files onto an empty selection is accepted silently', () => {
    const five = Array.from({ length: 5 }, (_unused, i) => new File([], `f${i}.csv`))
    const result = addFiles([], five)
    expect(result.files).toHaveLength(5)
    expect(result.files.map((pf) => pf.file.name)).toEqual(['f0.csv', 'f1.csv', 'f2.csv', 'f3.csv', 'f4.csv'])
    expect(result.refusal).toBeNull()
  })

  // BULK-03-5 — falsification: an impl that treats an empty `incoming` array as "nothing
  // to check" and skips straight past preserving `current`, e.g. returning `[]`.
  it('BULK-03-5: zero incoming files is a no-op — the existing selection comes back unchanged', () => {
    const existing = mkPicked(['a.csv', 'b.csv'])
    const result = addFiles(existing, [])
    expect(result.files.map((pf) => pf.file.name)).toEqual(['a.csv', 'b.csv'])
    expect(result.refusal).toBeNull()
  })
})

describe('removeFile — removal by id (BULK-03-6, BULK-03-7)', () => {
  // BULK-03-6 — falsification: an impl that removes by array index instead of id (would
  // remove the wrong entry once the list has been reordered by a prior removal), or one
  // that does not preserve the order of the survivors.
  it('BULK-03-6: removes the file matching the given id, preserving order of the rest', () => {
    const [a, b, c] = mkPicked(['a.csv', 'b.csv', 'c.csv'])
    const result = removeFile([a, b, c], b.id)
    expect(result.map((pf) => pf.file.name)).toEqual(['a.csv', 'c.csv'])
  })

  // BULK-03-7 — falsification: an impl that throws or empties the list on an id that
  // matches nothing, rather than treating it as a no-op.
  it('BULK-03-7: removing an unknown id is a no-op', () => {
    const [a] = mkPicked(['a.csv'])
    const result = removeFile([a], 'nope')
    expect(result).toEqual([a])
  })
})

describe('canReadColumnsAll — the gate on Read columns (BULK-03-8..10)', () => {
  // BULK-03-8 — falsification: any impl that filters incoming files by extension before
  // appending. A bad-extension file must be visible in the list (so the UI can name it),
  // never silently excluded by addFiles itself — that gate belongs to canReadColumnsAll,
  // not to selection.
  it('BULK-03-8: a bad-extension file is still added by addFiles, never dropped', () => {
    const csv = new File([], 'a.csv')
    const pdf = new File([], 'notes.pdf')
    const result = addFiles([], [csv, pdf])
    expect(result.files).toHaveLength(2)
    expect(result.files.map((pf) => pf.file.name)).toEqual(['a.csv', 'notes.pdf'])
  })

  // BULK-03-9 — falsification: an impl that reads columns as long as ANY file is valid
  // (an OR instead of an AND across the selection), which would let a mixed batch reach
  // previewImport for the bad file too.
  it("BULK-03-9: one bad-extension file blocks the whole selection's read gate", () => {
    const good = mkPicked(['a.csv'])
    const mixedWithBad = [...good, ...mkPicked(['notes.pdf'])]
    expect(canReadColumnsAll(mixedWithBad)).toBe(false)

    const allGood = [...good, ...mkPicked(['b.xlsx'])]
    expect(canReadColumnsAll(allGood)).toBe(true)
  })

  // BULK-03-10 — falsification: an impl using Array.prototype.every alone, which is
  // vacuously true over an empty array — exactly the trap this spec exists to catch.
  it('BULK-03-10: an empty selection cannot read columns', () => {
    expect(canReadColumnsAll([])).toBe(false)
  })
})
