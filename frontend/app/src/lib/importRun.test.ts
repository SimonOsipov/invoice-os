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

import { MAX_RUN_FILES, addFiles, canReadColumnsAll, capRefusal, removeFile } from './importRun'
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

// ============================================================================
// QA Mode B (task-310) — adversarial/edge coverage beyond the architect's Test
// Specs table (BULK-03-1..10). Every mutation below was hand-verified to turn
// the corresponding BULK-03 spec red before this coverage was added; these
// specs target failure modes the original table left unexercised.
// ============================================================================

describe('addFiles — cap boundary adversarials (QA Mode B)', () => {
  // Falsification: an impl whose refusal text is generic ("cap reached") instead of
  // naming ALL 3 dropped files, or one that mutates/reorders the untouched selection
  // while refusing.
  it('adding to an already-full selection drops every incoming file and names all of them', () => {
    const full = mkPicked(['a.csv', 'b.csv', 'c.csv', 'd.csv', 'e.csv'])
    const x = new File([], 'x.csv')
    const y = new File([], 'y.csv')
    const z = new File([], 'z.csv')
    const result = addFiles(full, [x, y, z])
    expect(result.files).toHaveLength(5)
    expect(result.files.map((pf) => pf.file.name)).toEqual(['a.csv', 'b.csv', 'c.csv', 'd.csv', 'e.csv'])
    expect(result.refusal).not.toBeNull()
    expect(result.refusal).toContain('3')
    expect(result.refusal).toContain(String(MAX_RUN_FILES))
  })

  // Falsification: an off-by-one in the room calculation (`room = current.length -
  // MAX_RUN_FILES` or similar) that refuses the exact-fit case Core AC 1 says must be
  // silent, distinct from BULK-03-4 (empty selection) by starting from a partial one.
  it('adding exactly the remaining room onto a partial selection is accepted silently', () => {
    const threeAlready = mkPicked(['a.csv', 'b.csv', 'c.csv'])
    const x = new File([], 'x.csv')
    const y = new File([], 'y.csv')
    const result = addFiles(threeAlready, [x, y])
    expect(result.files).toHaveLength(5)
    expect(result.files.map((pf) => pf.file.name)).toEqual(['a.csv', 'b.csv', 'c.csv', 'x.csv', 'y.csv'])
    expect(result.refusal).toBeNull()
  })

  // Falsification: an impl that only checks the cap against `incoming.length` in
  // isolation (e.g. a `for` loop that breaks without counting the rest), which could
  // under- or over-report the dropped count on a single large batch.
  it('a single addFiles call with 20 files at once still caps at 5 and names the 15 dropped', () => {
    const twenty = Array.from({ length: 20 }, (_unused, i) => new File([], `f${i}.csv`))
    const result = addFiles([], twenty)
    expect(result.files).toHaveLength(MAX_RUN_FILES)
    expect(result.files.map((pf) => pf.file.name)).toEqual(['f0.csv', 'f1.csv', 'f2.csv', 'f3.csv', 'f4.csv'])
    expect(result.refusal).not.toBeNull()
    expect(result.refusal).toContain('15')
    expect(result.refusal).toContain(String(MAX_RUN_FILES))
  })
})

describe('addFiles/removeFile — identity and duplicate-name behaviour (QA Mode B)', () => {
  // A user dropping the very same File twice (e.g. two overlapping drag events) must
  // not silently collapse to one entry — the list is keyed by a fresh id per pick, not
  // by File identity or name. Falsification: an impl that dedupes by object reference
  // or reuses one id for both entries.
  it('adding the exact same File object twice keeps both as separate entries with unique ids', () => {
    const f = new File([], 'dup.csv')
    const result = addFiles([], [f, f])
    expect(result.files).toHaveLength(2)
    expect(result.files[0].file).toBe(f)
    expect(result.files[1].file).toBe(f)
    const ids = result.files.map((pf) => pf.id)
    expect(new Set(ids).size).toBe(2)
    expect(result.refusal).toBeNull()
  })

  // Two distinct files that merely share a filename (common with exported reports)
  // must both be kept and independently removable. Falsification: an impl that keys
  // removal by name instead of id, which would remove both or the wrong one.
  it('two distinct File objects sharing the same name are both kept, and removeFile removes exactly one', () => {
    const f1 = new File([], 'same.csv')
    const f2 = new File([], 'same.csv')
    const added = addFiles([], [f1, f2])
    expect(added.files).toHaveLength(2)
    expect(added.files.map((pf) => pf.file.name)).toEqual(['same.csv', 'same.csv'])
    const ids = added.files.map((pf) => pf.id)
    expect(new Set(ids).size).toBe(2)

    const afterRemove = removeFile(added.files, added.files[0].id)
    expect(afterRemove).toHaveLength(1)
    expect(afterRemove[0].id).toBe(added.files[1].id)
    expect(afterRemove[0].file).toBe(f2)
  })
})

describe('order stability under add/remove churn (QA Mode B)', () => {
  // Falsification: any impl backed by a Set, a Map keyed by insertion re-sort, or a
  // removeFile that re-appends survivors in a different order than they started in.
  it('add, remove-from-middle, add-again lands in exactly the order a user would predict', () => {
    const a = new File([], 'a.csv')
    const b = new File([], 'b.csv')
    const c = new File([], 'c.csv')
    const d = new File([], 'd.csv')
    const step1 = addFiles([], [a, b, c])
    const bId = step1.files[1].id
    const step2 = removeFile(step1.files, bId)
    const step3 = addFiles(step2, [d])
    expect(step3.files.map((pf) => pf.file.name)).toEqual(['a.csv', 'c.csv', 'd.csv'])
    expect(step3.refusal).toBeNull()
  })
})

describe('capRefusal — the copy names both numbers (QA Mode B)', () => {
  // Falsification: a future edit that reduces the message to a vague "some files were
  // not added" — these pin that both MAX_RUN_FILES and the dropped count are always
  // present in the rendered text, not just asserted indirectly via addFiles.
  it('names the cap and the dropped count for a single dropped file', () => {
    const msg = capRefusal(1)
    expect(msg).toContain(String(MAX_RUN_FILES))
    expect(msg).toContain('1')
  })

  it('names the cap and the dropped count for several dropped files', () => {
    const msg = capRefusal(4)
    expect(msg).toContain(String(MAX_RUN_FILES))
    expect(msg).toContain('4')
  })
})

describe('canReadColumnsAll — extension-rule edges routed through the shipped canReadColumns (QA Mode B)', () => {
  // Pins that the delegation in canReadColumnsAll does not accidentally re-derive a
  // stricter/looser extension rule than the shipped hasImportableExtension. Falsification:
  // an impl that re-implements the check with case-sensitive or different-segment logic.
  it('an uppercase .CSV extension is still readable', () => {
    expect(canReadColumnsAll(mkPicked(['REPORT.CSV']))).toBe(true)
  })

  it('a double extension like a.csv.bak is not readable — last segment only', () => {
    expect(canReadColumnsAll(mkPicked(['a.csv.bak']))).toBe(false)
  })

  it('a name that is exactly .csv is readable', () => {
    expect(canReadColumnsAll(mkPicked(['.csv']))).toBe(true)
  })

  it('a file with no extension at all is not readable', () => {
    expect(canReadColumnsAll(mkPicked(['ledger']))).toBe(false)
  })
})
