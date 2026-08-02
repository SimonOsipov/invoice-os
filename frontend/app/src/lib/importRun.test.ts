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

import {
  MAX_RUN_FILES,
  addFiles,
  attachDocumentIds,
  canReadColumnsAll,
  capRefusal,
  markRunFailed,
  markRunRouted,
  removeFile,
  routeAfterRun,
  runBatchIds,
  runFailures,
  runFileRows,
  runIsActive,
  runReducer,
} from './importRun'
import type { FileOutcome, ImportRun, PickedFile, RunFile } from './importRun'
import type { ImportPreview, ImportReport } from './importApi'
// Read-only cross-module import, same licence as filesStrip below: BULK-DOC-01's claim is
// that document ids do NOT follow layout grouping, which needs the real groupByLayout as
// the counterweight rather than a hand-rolled restatement of it.
import { groupByLayout } from './mappingGroups'
// Read-only cross-module import: filesStrip is reviewBatch.ts's, not this module's,
// but the ONE describe block below that uses it exists to pin the actual App.tsx
// wiring bug (BULK-01-07) at the one honest layer this no-jsdom suite can reach — see
// that block's own comment. Nothing here writes to lib/reviewBatch.ts or its own test
// file.
import { filesStrip } from './reviewBatch'

// Builds fixture PickedFile[] with stable, human-readable ids derived from the file's own
// name — good enough for these specs, which only ever look an id up by the very entry
// that owns it. The real id-generation strategy (crypto.randomUUID or otherwise) is
// addFiles's to decide; nothing here assumes a particular scheme for FRESH entries.
function mkPicked(names: string[]): PickedFile[] {
  return names.map((name) => ({ id: `existing:${name}`, file: new File([], name), documentId: null }))
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

// ============================================================================
// RED specs (BULK-01-05, task-308, Test-first) — the run-reducer half. Pin
// runReducer/runBatchIds/runFailures/runFileRows/routeAfterRun's contract before the
// executor implements the bodies (currently STUBs in importRun.ts, throwing 'not
// implemented'). Plan's Test Specs table (BULK-05-1..12) is authoritative; this
// section transcribes it row by row and does not exceed it — adversarial/edge
// coverage beyond this table is QA Mode B's job, once the implementation lands.
//
// Fixture note: BASE_RUN_REPORT mirrors reviewBatch.test.ts's own BASE_ROUTE_REPORT
// fixture (same 13-field ImportReport shape) — duplicated locally rather than
// imported, same reason scanForIdentifier is duplicated across test files in this
// codebase: these are test-only fixtures in independently owned spec files, not
// production code a shared module would be surgical over.
const BASE_RUN_REPORT: ImportReport = {
  id: 'batch-run-base',
  status: 'completed',
  format: 'csv',
  delimiter: ',',
  encoding: 'utf-8',
  rows_total: 10,
  rows_valid: 10,
  rows_invalid: 0,
  ready_invoices: 1,
  quarantined_invoices: 0,
  errors: [],
  rule_set_version: 5,
  invoices_clean: 1,
  invoices_with_violations: 0,
  invoice_violations: [],
}

// Builds a RunFile with a caller-given id/name/outcome, defaulting groupId to a single
// shared group -- no spec below exercises per-group routing, so a constant keeps every
// fixture terse without implying group identity matters to any of these functions.
function mkRunFile(id: string, name: string, outcome: FileOutcome, groupId = 'g1'): RunFile {
  return { id, name, groupId, outcome }
}

function pendingFile(id: string, name: string): RunFile {
  return mkRunFile(id, name, { kind: 'pending' })
}

// Dispatches the 'start' action so every spec below begins from the SAME entry point
// the real App.startRun() would use, rather than hand-assembling an already-running
// ImportRun object -- if 'start' itself is wrong (e.g. cursor seeded at something other
// than 0), every spec here inherits that RED rather than masking it.
function startRun(files: RunFile[]): ImportRun {
  return runReducer({ files: [], cursor: 0, status: 'idle' }, { type: 'start', files })
}

describe('runReducer — settled advances the cursor without branching on outcome kind (BULK-05-1, [partial-success-kept])', () => {
  // Falsification: an impl that branches on outcome.kind and only advances the cursor
  // on 'imported' (or worse, jumps straight to status:'finished' on the first
  // failure) -- exactly the early-stop [partial-success-kept] exists to forbid. The
  // spec asserts BOTH cursor and status, per the task's own instruction: a passing
  // cursor with a wrongly-flipped status would still be a real bug.
  it('BULK-05-1: a failed settle at file 1 of 3 advances the cursor and keeps the run running', () => {
    const files = [pendingFile('f1', 'a.csv'), pendingFile('f2', 'b.csv'), pendingFile('f3', 'c.csv')]
    const started = startRun(files)
    const afterFirst = runReducer(started, { type: 'settled', outcome: { kind: 'failed', message: 'boom' } })
    expect(afterFirst.cursor).toBe(1)
    expect(afterFirst.status).toBe('running')
    expect(afterFirst.status).not.toBe('finished')
  })
})

describe('runReducer — the run only finishes after the LAST file settles (BULK-05-2)', () => {
  // Falsification: an impl that flips to 'finished' on ANY settle, or one that never
  // flips at all (cursor keeps climbing past files.length forever).
  it('BULK-05-2: three files stay running through the first two settles and finish on the third', () => {
    const files = [pendingFile('f1', 'a.csv'), pendingFile('f2', 'b.csv'), pendingFile('f3', 'c.csv')]
    let run = startRun(files)
    run = runReducer(run, {
      type: 'settled',
      outcome: { kind: 'imported', batchId: 'b1', report: { ...BASE_RUN_REPORT, id: 'b1' } },
    })
    expect(run.status).toBe('running')
    run = runReducer(run, { type: 'settled', outcome: { kind: 'failed', message: 'x' } })
    expect(run.status).toBe('running')
    run = runReducer(run, {
      type: 'settled',
      outcome: { kind: 'imported', batchId: 'b3', report: { ...BASE_RUN_REPORT, id: 'b3' } },
    })
    expect(run.status).toBe('finished')
    expect(run.cursor).toBe(3)
  })
})

describe('runReducer — phase applies to the cursor file only (BULK-05-3)', () => {
  // Falsification: an impl that writes the phase onto the WRONG index (e.g. cursor-1,
  // or files[0] unconditionally), or one that mutates an already-settled file's
  // outcome in place instead of leaving f1/f2 untouched.
  it('BULK-05-3: after two settles, a phase action writes onto file 3 only, leaving f1/f2 outcomes untouched', () => {
    const files = [pendingFile('f1', 'a.csv'), pendingFile('f2', 'b.csv'), pendingFile('f3', 'c.csv')]
    let run = startRun(files)
    const f1Outcome: FileOutcome = { kind: 'imported', batchId: 'b1', report: { ...BASE_RUN_REPORT, id: 'b1' } }
    const f2Outcome: FileOutcome = { kind: 'failed', message: 'nope' }
    run = runReducer(run, { type: 'settled', outcome: f1Outcome })
    run = runReducer(run, { type: 'settled', outcome: f2Outcome })

    const phase = { kind: 'sending' as const, loaded: 10, total: 100 }
    const afterPhase = runReducer(run, { type: 'phase', phase })

    expect(afterPhase.files[0].outcome).toEqual(f1Outcome)
    expect(afterPhase.files[1].outcome).toEqual(f2Outcome)
    expect(afterPhase.files[2].outcome).toEqual({ kind: 'uploading', phase })
  })
})

describe('runReducer — a late phase on a finished run is a total no-op (BULK-05-4)', () => {
  // Falsification: an impl that always returns a fresh `{...run}` (or a fresh
  // `{...run, files:[...run.files]}`) regardless of status -- a deep-equal check
  // alone would pass that impl. The identity assertion (`toBe`) is what actually
  // discriminates "ignored" from "recomputed to the same values": a component
  // memoizing off reference equality would re-render needlessly under the latter,
  // and AC #2's "ignored" reads as identity, not coincidental equality.
  it('BULK-05-4: a phase action on a finished run returns the IDENTICAL state object', () => {
    const files = [pendingFile('f1', 'a.csv')]
    let run = startRun(files)
    run = runReducer(run, {
      type: 'settled',
      outcome: { kind: 'imported', batchId: 'b1', report: { ...BASE_RUN_REPORT, id: 'b1' } },
    })
    expect(run.status).toBe('finished')

    const after = runReducer(run, { type: 'phase', phase: { kind: 'sending', loaded: 1, total: 2 } })

    expect(after).toBe(run)
    expect(after).toEqual(run)
  })
})

describe('runBatchIds — successes in run order (BULK-05-5)', () => {
  // Falsification: an impl that sorts, dedupes, or includes the failed file's id (it
  // has none), or one that returns ids in settle-completion order rather than the
  // run's own file order (the two coincide here, which is exactly why BULK-05-11's
  // identity check below matters too).
  it('BULK-05-5: f1 imported, f2 failed, f3 imported yields runBatchIds [b1, b3]', () => {
    const files = [pendingFile('f1', 'a.csv'), pendingFile('f2', 'b.csv'), pendingFile('f3', 'c.csv')]
    let run = startRun(files)
    run = runReducer(run, {
      type: 'settled',
      outcome: { kind: 'imported', batchId: 'b1', report: { ...BASE_RUN_REPORT, id: 'b1' } },
    })
    run = runReducer(run, { type: 'settled', outcome: { kind: 'failed', message: 'nope' } })
    run = runReducer(run, {
      type: 'settled',
      outcome: { kind: 'imported', batchId: 'b3', report: { ...BASE_RUN_REPORT, id: 'b3' } },
    })
    expect(runBatchIds(run)).toEqual(['b1', 'b3'])
  })
})

describe('runFailures — filename + reason, in run order (BULK-05-6)', () => {
  // Falsification: an impl that re-words the server's message, drops the filename, or
  // reads the name off the outcome instead of the RunFile (the outcome carries no
  // name field at all in the 'failed' variant -- runFailures must reach for
  // RunFile.name).
  it("BULK-05-6: a failed file's name and the server's message survive verbatim", () => {
    const files = [pendingFile('f1', 'a.csv'), pendingFile('f2', 'b.csv')]
    let run = startRun(files)
    run = runReducer(run, {
      type: 'settled',
      outcome: { kind: 'imported', batchId: 'b1', report: { ...BASE_RUN_REPORT, id: 'b1' } },
    })
    run = runReducer(run, { type: 'settled', outcome: { kind: 'failed', message: 'duplicate invoice number 42' } })
    expect(runFailures(run)).toEqual([{ name: 'b.csv', message: 'duplicate invoice number 42' }])
  })
})

describe('routeAfterRun — order is load-bearing (BULK-05-7..10)', () => {
  // BULK-05-7 — falsification: an impl that falls through to 'review' with an empty
  // batchIds array instead of the distinct 'none' kind CreateFlow needs to know to
  // stay on 'mapping' (AC #9).
  it('BULK-05-7: two files, both failed, routes to none -- no batch was ever created', () => {
    const files = [pendingFile('f1', 'a.csv'), pendingFile('f2', 'b.csv')]
    let run = startRun(files)
    run = runReducer(run, { type: 'settled', outcome: { kind: 'failed', message: 'x' } })
    run = runReducer(run, { type: 'settled', outcome: { kind: 'failed', message: 'y' } })
    expect(routeAfterRun(run, null)).toEqual({ kind: 'none' })
  })

  // BULK-05-8 — falsification: an impl that checks `report.ready_invoices === 1` (or
  // calls routeAfterImport) WITHOUT first checking the run itself holds exactly one
  // file. The identical report/id resolves 'single' alone and 'review' as one of two
  // -- proving the run-size gate is real, not coincidental to the report's own shape.
  it('BULK-05-8: the single shortcut only fires when the run has exactly one file', () => {
    const soleReport: ImportReport = { ...BASE_RUN_REPORT, id: 'b1', ready_invoices: 1 }

    let soloRun = startRun([pendingFile('f1', 'a.csv')])
    soloRun = runReducer(soloRun, { type: 'settled', outcome: { kind: 'imported', batchId: 'b1', report: soleReport } })
    expect(routeAfterRun(soloRun, 'inv-7')).toEqual({ kind: 'single', invoiceId: 'inv-7' })

    let pairRun = startRun([pendingFile('f1', 'a.csv'), pendingFile('f2', 'b.csv')])
    pairRun = runReducer(pairRun, { type: 'settled', outcome: { kind: 'imported', batchId: 'b1', report: soleReport } })
    pairRun = runReducer(pairRun, {
      type: 'settled',
      outcome: { kind: 'imported', batchId: 'b2', report: { ...BASE_RUN_REPORT, id: 'b2', ready_invoices: 1 } },
    })
    expect(routeAfterRun(pairRun, 'inv-7').kind).toBe('review')
    expect(routeAfterRun(pairRun, 'inv-7')).toEqual({ kind: 'review', batchIds: ['b1', 'b2'] })
  })

  // BULK-05-9 — falsification: an impl using `resolvedInvoiceId != null` instead of
  // truthiness, which would let '' through into 'single' with an empty invoiceId --
  // routeAfterImport's own ROUTE-7 guards this at the single-file layer; this spec
  // guards that routeAfterRun does not reintroduce the bug by handling the id itself
  // instead of delegating.
  it("BULK-05-9: a resolved id of '' degrades to review, never single with an empty id", () => {
    let run = startRun([pendingFile('f1', 'a.csv')])
    run = runReducer(run, {
      type: 'settled',
      outcome: { kind: 'imported', batchId: 'b1', report: { ...BASE_RUN_REPORT, id: 'b1', ready_invoices: 1 } },
    })
    const result = routeAfterRun(run, '')
    expect(result).toEqual({ kind: 'review', batchIds: ['b1'] })
    expect(result.kind).not.toBe('single')
  })

  // BULK-05-10 — falsification: an impl that treats a `ready_invoices: 0` outcome as
  // equivalent to 'failed' and drops its batch id from the review -- f2's own
  // createImport SUCCEEDED (an 'imported' outcome), it just produced zero ready
  // invoices; BULK-01-08's partial-failure e2e depends on both ids landing in the
  // same review.
  it('BULK-05-10: a batch with ready_invoices:0 (everything quarantined) still joins the review with BOTH ids', () => {
    let run = startRun([pendingFile('f1', 'a.csv'), pendingFile('f2', 'b.csv')])
    run = runReducer(run, {
      type: 'settled',
      outcome: { kind: 'imported', batchId: 'b1', report: { ...BASE_RUN_REPORT, id: 'b1', ready_invoices: 3 } },
    })
    run = runReducer(run, {
      type: 'settled',
      outcome: {
        kind: 'imported',
        batchId: 'b2',
        report: { ...BASE_RUN_REPORT, id: 'b2', ready_invoices: 0, quarantined_invoices: 5 },
      },
    })
    expect(routeAfterRun(run, null)).toEqual({ kind: 'review', batchIds: ['b1', 'b2'] })
  })
})

describe('partial success is kept intact through a mixed run (BULK-05-11)', () => {
  // Falsification: an impl whose settled arm mutates a shared object, or rebuilds
  // `files` in a way that loses a previously-settled entry's own batchId/report (e.g.
  // collapsing every 'imported' outcome onto the LAST one written).
  it('BULK-05-11: every imported outcome retains its own batchId and report after later settles', () => {
    const report1: ImportReport = { ...BASE_RUN_REPORT, id: 'b1' }
    const report3: ImportReport = { ...BASE_RUN_REPORT, id: 'b3', ready_invoices: 7 }
    let run = startRun([pendingFile('f1', 'a.csv'), pendingFile('f2', 'b.csv'), pendingFile('f3', 'c.csv')])
    run = runReducer(run, { type: 'settled', outcome: { kind: 'imported', batchId: 'b1', report: report1 } })
    run = runReducer(run, { type: 'settled', outcome: { kind: 'failed', message: 'x' } })
    run = runReducer(run, { type: 'settled', outcome: { kind: 'imported', batchId: 'b3', report: report3 } })

    expect(run.files[0].outcome).toEqual({ kind: 'imported', batchId: 'b1', report: report1 })
    expect(run.files[2].outcome).toEqual({ kind: 'imported', batchId: 'b3', report: report3 })
  })
})

describe('runFileRows — one row per file, total over the run, and no honesty-constraint field ever appears (BULK-05-12, AC #10)', () => {
  // Falsification (row count/kind mapping): an impl that skips a file, reorders rows,
  // or maps 'processing' onto the same label as 'sending' (or vice versa) instead of
  // the two-word distinction ImportProgress.tsx's own header already renders.
  //
  // Falsification (shape): an impl that widens ANY row -- 'queued'/'sending'/
  // 'processing' picking up a loaded/total/percent field, 'imported' picking up a row
  // count instead of the server's own ready_invoices, or ANY row gaining a rule-set
  // field. `toEqual` is a deep structural check, so a row carrying one extra key
  // fails even where that key's value looks harmless -- which is the whole point:
  // this is the one spec standing between the five honesty constraints in
  // ImportProgress.tsx's header comment and a future PR quietly widening RunFileRow.
  it('BULK-05-12: pending/uploading(sending)/uploading(processing)/imported/failed each render their own row shape, nothing more', () => {
    const report: ImportReport = { ...BASE_RUN_REPORT, id: 'b4', ready_invoices: 4 }
    const files: RunFile[] = [
      pendingFile('f1', 'a.csv'),
      mkRunFile('f2', 'b.csv', { kind: 'uploading', phase: { kind: 'sending', loaded: 10, total: 100 } }),
      mkRunFile('f3', 'c.csv', { kind: 'uploading', phase: { kind: 'processing' } }),
      mkRunFile('f4', 'd.csv', { kind: 'imported', batchId: 'b4', report }),
      mkRunFile('f5', 'e.csv', { kind: 'failed', message: 'nope' }),
    ]
    const run: ImportRun = { files, cursor: 3, status: 'running' }

    const rows = runFileRows(run)

    expect(rows).toHaveLength(5)
    expect(rows[0]).toEqual({ name: 'a.csv', kind: 'queued' })
    expect(rows[1]).toEqual({ name: 'b.csv', kind: 'sending' })
    expect(rows[2]).toEqual({ name: 'c.csv', kind: 'processing' })
    expect(rows[3]).toEqual({ name: 'd.csv', kind: 'imported', count: 4 })
    expect(rows[4]).toEqual({ name: 'e.csv', kind: 'failed', reason: 'nope' })

    // Belt-and-suspenders beyond the toEqual checks above: names each forbidden field
    // explicitly (percent/loaded/total/rows/bytes/stage/rule-set) so a future reader
    // of a failing diff sees WHICH honesty constraint broke, not just "shape
    // mismatch".
    rows.forEach((row) => {
      expect(row).not.toHaveProperty('percent')
      expect(row).not.toHaveProperty('loaded')
      expect(row).not.toHaveProperty('total')
      expect(row).not.toHaveProperty('rows')
      expect(row).not.toHaveProperty('rowsRead')
      expect(row).not.toHaveProperty('bytes')
      expect(row).not.toHaveProperty('stage')
      expect(row).not.toHaveProperty('ruleSetVersion')
      expect(row).not.toHaveProperty('rule_set_version')
    })
  })
})

// ============================================================================
// QA Mode B (task-308) — adversarial/edge coverage beyond the architect's Test Specs
// table (BULK-05-1..12). Every one of the four mutations these guard against was
// hand-verified (revert the targeted line, confirm the corresponding spec goes red for
// the right reason, restore, confirm `git diff` empty) before this coverage was added;
// these specs target failure modes the original table left unexercised.
// ============================================================================

describe('zero progress events is legal (QA Mode B, IMPAPI-08 -- no `phase` action ever required before a settle)', () => {
  // Falsification: an impl that assumes runFileRows/runBatchIds can only be called
  // after at least one 'phase' action has been dispatched for a file (e.g. reading a
  // field off `outcome` that only 'uploading' populates, or crashing on an outcome that
  // jumps straight from 'pending' to 'imported'/'failed'). A ~100 KB file on a fast link
  // may fire zero `sending` events (importApi.ts's own IMPAPI-08 comment) -- this pins
  // that the run-reducer half never depends on that never-guaranteed intermediate step.
  it('a file that settles IMPORTED with no phase event at all still produces a correct row, batch id, and finished status', () => {
    const report: ImportReport = { ...BASE_RUN_REPORT, id: 'b-noprogress', ready_invoices: 2 }
    const run = runReducer(startRun([pendingFile('f1', 'silent.csv')]), {
      type: 'settled',
      outcome: { kind: 'imported', batchId: 'b-noprogress', report },
    })
    expect(run.status).toBe('finished')
    expect(runFileRows(run)).toEqual([{ name: 'silent.csv', kind: 'imported', count: 2 }])
    expect(runBatchIds(run)).toEqual(['b-noprogress'])
  })

  // Same falsification, the FAILED half: a request can reject before its first
  // progress event too (e.g. a synchronous network-layer refusal) -- 'failed' must not
  // assume an 'uploading' outcome ever existed either.
  it('a file that settles FAILED with no phase event at all still produces a correct row and failure entry', () => {
    const run = runReducer(startRun([pendingFile('f1', 'silent-fail.csv')]), {
      type: 'settled',
      outcome: { kind: 'failed', message: 'network error' },
    })
    expect(run.status).toBe('finished')
    expect(runFileRows(run)).toEqual([{ name: 'silent-fail.csv', kind: 'failed', reason: 'network error' }])
    expect(runFailures(run)).toEqual([{ name: 'silent-fail.csv', message: 'network error' }])
  })
})

describe('malformed dispatch sequences must not throw (QA Mode B)', () => {
  // Falsification: an impl that indexes `run.files[run.cursor]` directly (e.g.
  // `run.files[run.cursor].outcome = ...`) instead of going through `.map`, which would
  // throw on an out-of-bounds cursor against an empty files array. App.startRun() never
  // dispatches 'phase' before 'start' in practice, but the reducer itself should not
  // rely on caller discipline to avoid a crash.
  it('a phase action dispatched before any start action does not throw, and is a no-op over the empty file list', () => {
    const idle: ImportRun = { files: [], cursor: 0, status: 'idle' }
    let after: ImportRun | undefined
    expect(() => {
      after = runReducer(idle, { type: 'phase', phase: { kind: 'sending', loaded: 1, total: 2 } })
    }).not.toThrow()
    expect(after).toEqual({ files: [], cursor: 0, status: 'idle' })
  })

  // Falsification: same indexing hazard on the 'settled' arm. `start([])` itself
  // resolves straight to 'finished' (runStatus's own zero-file rule, module comment
  // above) -- dispatching 'settled' on top of THAT degenerate state must still not
  // crash, even though App.startRun()'s `for` loop would never produce it (an empty
  // `runFiles` array iterates zero times, so no 'settled' is ever dispatched for it).
  it('a settled action dispatched against a zero-file run does not throw', () => {
    const run = startRun([])
    expect(run.status).toBe('finished')
    expect(() => runReducer(run, { type: 'settled', outcome: { kind: 'failed', message: 'x' } })).not.toThrow()
  })
})

describe('a 5-file mixed run preserves order and totality across all three derivations (QA Mode B)', () => {
  // Falsification: an impl whose `runBatchIds`/`runFailures` order comes from
  // SETTLE-COMPLETION order rather than the run's own FILE order (the two coincide in
  // every BULK-05 fixture above, which only ever settles files in file order) -- this
  // is still settled in file order here too (the reducer offers no other way to settle
  // out of order), so what this specifically falsifies is an impl that sorts, groups by
  // outcome kind, or otherwise reorders the two derived arrays independently of file
  // order once there are enough of each kind to notice a reorder.
  it('failed/imported/failed/imported/imported yields correctly ordered ids and failures, and a total row set', () => {
    const files = [
      pendingFile('f1', 'a.csv'),
      pendingFile('f2', 'b.csv'),
      pendingFile('f3', 'c.csv'),
      pendingFile('f4', 'd.csv'),
      pendingFile('f5', 'e.csv'),
    ]
    let run = startRun(files)
    run = runReducer(run, { type: 'settled', outcome: { kind: 'failed', message: 'fail-a' } })
    run = runReducer(run, { type: 'settled', outcome: { kind: 'imported', batchId: 'b2', report: { ...BASE_RUN_REPORT, id: 'b2' } } })
    run = runReducer(run, { type: 'settled', outcome: { kind: 'failed', message: 'fail-c' } })
    run = runReducer(run, { type: 'settled', outcome: { kind: 'imported', batchId: 'b4', report: { ...BASE_RUN_REPORT, id: 'b4' } } })
    run = runReducer(run, { type: 'settled', outcome: { kind: 'imported', batchId: 'b5', report: { ...BASE_RUN_REPORT, id: 'b5' } } })

    expect(run.status).toBe('finished')
    expect(runBatchIds(run)).toEqual(['b2', 'b4', 'b5'])
    expect(runFailures(run)).toEqual([
      { name: 'a.csv', message: 'fail-a' },
      { name: 'c.csv', message: 'fail-c' },
    ])
    // runFileRows is TOTAL over the run regardless of outcome mix -- one row per file,
    // always, in file order (BULK-05-12's own claim, exercised here at a length beyond
    // that spec's fixture).
    const rows = runFileRows(run)
    expect(rows).toHaveLength(5)
    expect(rows.map((r) => r.kind)).toEqual(['failed', 'imported', 'failed', 'imported', 'imported'])
  })
})

describe('routeAfterRun — a single file that imports ZERO invoices is review, never none (QA Mode B)', () => {
  // Pins the boundary between routeAfterRun's OWN 'none' (no batch was ever created --
  // every file request-level FAILED) and routeAfterImport's 'rejected' (a batch WAS
  // created, it just has zero ready invoices -- reviewBatch.ts:327). A single-file run
  // landing on 'rejected' must still fall through to 'review' with that one batch id,
  // exactly as the multi-file BULK-05-10 case does -- 'none' is reserved for
  // batchIds.length === 0 alone, never for "the one batch that exists says rejected".
  // Falsification: an impl that treats routeAfterImport's 'rejected' kind as
  // equivalent to no-batch-at-all and returns `{kind:'none'}` here instead.
  it('one file, completed with ready_invoices:0, routes to review carrying that one batch id -- a batch WAS created', () => {
    const run = runReducer(startRun([pendingFile('f1', 'empty.csv')]), {
      type: 'settled',
      outcome: {
        kind: 'imported',
        batchId: 'b-zero',
        report: { ...BASE_RUN_REPORT, id: 'b-zero', ready_invoices: 0, quarantined_invoices: 10 },
      },
    })
    expect(routeAfterRun(run, null)).toEqual({ kind: 'review', batchIds: ['b-zero'] })
    expect(routeAfterRun(run, null).kind).not.toBe('none')
  })
})

// ============================================================================
// Executor correction (task-308, BULK-01-05) — the all-failed run used to be a dead
// end: applyRoute (App.tsx) had no branch for RunRoute's `none` kind, so `run.status`
// stayed 'finished' forever, CreateFlow's `run.status !== 'idle'` gate stayed true, and
// the operator was stuck on ImportProgress (a card with zero buttons) with no way back
// to the mapping step short of the wizard-wide Cancel. These pin the fix's new
// contract: a distinct 'failed' status, and the one predicate (runIsActive) both
// CreateFlow's and CreateMapping's body-swap/upload-disable gates now share.
// ============================================================================

describe("runIsActive — 'running'/'finished' are active, 'idle'/'failed' are not (task-308 correction)", () => {
  // Falsification: an impl that keys off `status !== 'idle'` (the ORIGINAL bug's own
  // gate) would wrongly call 'failed' active too, reproducing the exact dead end this
  // fix retires — CreateFlow would keep rendering ImportProgress over a 'failed' run.
  it('is true for running and finished, false for idle and failed', () => {
    const files = [pendingFile('f1', 'a.csv')]
    expect(runIsActive({ files, cursor: 0, status: 'idle' })).toBe(false)
    expect(runIsActive({ files, cursor: 0, status: 'running' })).toBe(true)
    expect(runIsActive({ files, cursor: 1, status: 'finished' })).toBe(true)
    expect(runIsActive({ files, cursor: 1, status: 'failed' })).toBe(false)
  })
})

describe('markRunFailed — flips status only, files/cursor survive intact (task-308 correction, AC #9)', () => {
  // Falsification: an impl that resets `files`/`cursor` alongside `status` (mirroring
  // the 'single' route's `{files:[],cursor:0,status:'idle'}` reset — 'review' used
  // that same literal reset too until BULK-01-07's markRunRouted below replaced it,
  // for the identical reason) would wipe the exact failure history CreateMapping
  // needs to render — runFailures would come back empty and the operator would see no
  // reason for the run they just watched fail.
  it('a two-file run where both files failed keeps every failure readable after markRunFailed', () => {
    let run = startRun([pendingFile('f1', 'a.csv'), pendingFile('f2', 'b.csv')])
    run = runReducer(run, { type: 'settled', outcome: { kind: 'failed', message: 'network error' } })
    run = runReducer(run, { type: 'settled', outcome: { kind: 'failed', message: 'duplicate invoice number 7' } })
    expect(routeAfterRun(run, null)).toEqual({ kind: 'none' })

    const landed = markRunFailed(run)

    expect(landed.status).toBe('failed')
    expect(landed.cursor).toBe(run.cursor)
    expect(landed.files).toEqual(run.files)
    expect(runFailures(landed)).toEqual([
      { name: 'a.csv', message: 'network error' },
      { name: 'b.csv', message: 'duplicate invoice number 7' },
    ])
    expect(runBatchIds(landed)).toEqual([])
    expect(runIsActive(landed)).toBe(false)
  })

  // Falsification: an impl that mutates the run object in place instead of returning a
  // new one — App.tsx's `setRun(markRunFailed)` relies on a fresh reference so React
  // actually re-renders CreateFlow off the status change.
  it('returns a new object rather than mutating the run passed in', () => {
    const run = startRun([pendingFile('f1', 'a.csv')])
    const failedRun = { ...run, status: 'finished' as const }
    const landed = markRunFailed(failedRun)
    expect(landed).not.toBe(failedRun)
    expect(failedRun.status).toBe('finished')
  })
})

// ============================================================================
// Executor correction (BULK-01-07, task-307) — App.tsx's applyRoute reached the
// 'review' branch by resetting `run` to the literal `{files:[],cursor:0,status:'idle'}`
// — the exact shape markRunFailed's own falsification comment above already names as
// the wrong move. BULK-06-16 (reviewBatch.test.ts) pins that filesStrip(batches, run)
// reports a run-only failure — a file whose upload request itself failed before any
// batch ever existed, so nothing in `batches` represents it — but that spec calls
// filesStrip directly against a hand-built run and was never wired through
// applyRoute's actual reset, so it stayed green while the real app discarded that
// same data the instant a run finished. markRunRouted retires the literal reset the
// same way markRunFailed already retired it for the 'none' route.
// ============================================================================

describe('markRunRouted — flips status only, files/cursor survive intact (BULK-01-07 wiring correction)', () => {
  // Falsification: an impl that resets `files`/`cursor` alongside `status` (the exact
  // literal reset applyRoute's 'review' branch used before this fix) would wipe a
  // run-only failure the instant the run finished, reproducing the Core AC 5 miss
  // this fix retires.
  it('a mixed two-file run (one request-level failure, one imported) keeps both readable after markRunRouted', () => {
    let run = startRun([pendingFile('f1', 'bad.csv'), pendingFile('f2', 'ok.csv')])
    run = runReducer(run, { type: 'settled', outcome: { kind: 'failed', message: 'network error' } })
    run = runReducer(run, {
      type: 'settled',
      outcome: { kind: 'imported', batchId: 'b2', report: { ...BASE_RUN_REPORT, id: 'b2' } },
    })
    expect(routeAfterRun(run, null)).toEqual({ kind: 'review', batchIds: ['b2'] })

    const routed = markRunRouted(run)

    expect(routed.status).toBe('idle')
    expect(routed.cursor).toBe(run.cursor)
    expect(routed.files).toEqual(run.files)
    expect(runIsActive(routed)).toBe(false)
  })

  // Falsification: an impl that mutates the run object in place instead of returning a
  // new one — App.tsx's `setRun(markRunRouted)` relies on a fresh reference so React
  // actually re-renders off the status change (same reasoning as markRunFailed's own
  // identity spec above).
  it('returns a new object rather than mutating the run passed in', () => {
    const run = startRun([pendingFile('f1', 'a.csv')])
    const finishedRun = { ...run, status: 'finished' as const }
    const routed = markRunRouted(finishedRun)
    expect(routed).not.toBe(finishedRun)
    expect(finishedRun.status).toBe('finished')
  })
})

describe("filesStrip still reports a run-only failure after markRunRouted (BULK-01-07 wiring correction, Core AC 5) — the fix's actual integration point", () => {
  // applyRoute itself is component-level (App.tsx) and this suite runs under
  // vitest's node environment with no jsdom (vitest.config.ts:5, this file's own
  // header comment) — there is no oracle here that mounts CreateFlow/ReviewBatch and
  // drives a route through them. What IS honestly provable at this layer: filesStrip
  // (lib/reviewBatch.ts) reads `run.files` regardless of `run.status`, so a run-only
  // failure that survives markRunRouted's status-only change is one filesStrip can
  // still report. Falsification: had this spec run against the OLD literal
  // `{files:[],cursor:0,status:'idle'}` reset instead of markRunRouted's output, the
  // run-only row below would come back empty — exactly the bug this fix retires.
  it('a file that failed before any batch existed still produces its own filesStrip row after markRunRouted', () => {
    let run = startRun([pendingFile('f1', 'rejected-before-batch.csv'), pendingFile('f2', 'ok.csv')])
    run = runReducer(run, { type: 'settled', outcome: { kind: 'failed', message: 'the gateway refused this upload' } })
    run = runReducer(run, {
      type: 'settled',
      outcome: { kind: 'imported', batchId: 'b2', report: { ...BASE_RUN_REPORT, id: 'b2' } },
    })

    const routed = markRunRouted(run)
    // `batches: []` on purpose -- the whole point of a run-only failure is that no
    // batch was ever created for it, so the ONE row it can produce comes from `run`
    // alone, never from a batches array this spec would otherwise have to fabricate.
    const rows = filesStrip([], routed)

    expect(rows).toEqual([{ id: 'f1', filename: 'rejected-before-batch.csv', reason: 'the gateway refused this upload' }])
  })
})

// ---------------------------------------------------------------------------
// DOC-01-07 (task-355, Test-first) — upload-once: the per-file document id.
//
// The id lives on PickedFile because that is already the fileId-keyed state startRun
// snapshots and looks up, so removeFile/resetImport clear it with no new invariant.
// attachDocumentIds is the ONE exported pure helper readAllColumns writes the ids
// through; nothing in this suite mounts App.tsx, so that helper is the only honest
// oracle for AC-4.
//
//   BULK-DOC-01  three files, two sharing a layout, keep three distinct ids     (AC4)
//   BULK-DOC-03  two byte-identical files share one id, stay two entries        (AC4)
//   BULK-DOC-04  addFiles seeds documentId null                                 (AC4)
//   BULK-DOC-05  a file with no preview keeps null, never a neighbour's id      (AC4)
// BULK-DOC-02 (readAllColumns' component-local catch) is dropped — it is unchanged by
// this subtask and has no node-level oracle.

const SHARED_COLS = ['Invoice No', 'Total']
const OTHER_COLS = ['Invoice No', 'Currency']
const DOC_1 = '11111111-1111-4111-8111-111111111111'
const DOC_2 = '22222222-2222-4222-8222-222222222222'
const DOC_3 = '33333333-3333-4333-8333-333333333333'
const DOC_DUPE = 'dddddddd-dddd-4ddd-8ddd-dddddddddddd'

function mkDocPreview(columns: string[], documentId: string): ImportPreview {
  return {
    document_id: documentId,
    format: 'csv',
    delimiter: ',',
    encoding: 'utf-8',
    columns,
    sample_rows: [columns.map((_unused, i) => `v${i}`)],
    rows_total: 1,
  }
}

describe('attachDocumentIds — the per-file document id (BULK-DOC-01, 03, 04, 05)', () => {
  // Falsification: an impl that hangs the id off the MappingGroup (one preview per
  // layout) — the first two files share a layout and would collapse onto one id.
  it('BULK-DOC-01: three files, two sharing a layout but differing in content, carry three distinct document ids, each paired with its own file', () => {
    const files = mkPicked(['lagos.csv', 'abuja.csv', 'till.csv'])
    const previewed = [
      { fileId: files[0].id, preview: mkDocPreview(SHARED_COLS, DOC_1) },
      { fileId: files[1].id, preview: mkDocPreview(SHARED_COLS, DOC_2) },
      { fileId: files[2].id, preview: mkDocPreview(OTHER_COLS, DOC_3) },
    ]

    const result = attachDocumentIds(files, previewed)

    expect(result.map((pf) => [pf.file.name, pf.documentId])).toEqual([
      ['lagos.csv', DOC_1],
      ['abuja.csv', DOC_2],
      ['till.csv', DOC_3],
    ])
    expect(new Set(result.map((pf) => pf.documentId)).size).toBe(3)
    // The counterweight: grouping DOES collapse the first two files onto one group. The
    // ids must not follow it.
    const groups = groupByLayout(previewed)
    expect(groups).toHaveLength(2)
    expect(groups[0].fileIds).toEqual([files[0].id, files[1].id])
    // A fresh array/entries — App.tsx feeds this straight to setPickedFiles.
    expect(result).not.toBe(files)
    expect(files.map((pf) => pf.documentId)).toEqual([null, null, null])
  })

  // Byte-identical files legitimately resolve to ONE document (per-tenant content-hash
  // dedupe) while still being two files in the run. Falsification: keying the pairing by
  // documentId instead of fileId, which collapses the pair into a single entry and loses
  // one file's import entirely.
  it('BULK-DOC-03: two byte-identical files share one document id while staying two entries with distinct file ids', () => {
    const files = mkPicked(['branch-a.csv', 'branch-a-copy.csv', 'till.csv'])
    const previewed = [
      { fileId: files[0].id, preview: mkDocPreview(SHARED_COLS, DOC_DUPE) },
      { fileId: files[1].id, preview: mkDocPreview(SHARED_COLS, DOC_DUPE) },
      { fileId: files[2].id, preview: mkDocPreview(OTHER_COLS, DOC_3) },
    ]

    const result = attachDocumentIds(files, previewed)

    expect(result).toHaveLength(3)
    expect(result.map((pf) => pf.documentId)).toEqual([DOC_DUPE, DOC_DUPE, DOC_3])
    expect(result.map((pf) => pf.id)).toEqual(files.map((pf) => pf.id))
    expect(new Set(result.map((pf) => pf.id)).size).toBe(3)
    // Two surviving entries is what makes startRun issue two createImport calls against
    // the one document — two import_batches rows. The wire half of that is e2e's.
    expect(result.filter((pf) => pf.documentId === DOC_DUPE)).toHaveLength(2)
  })

  it('BULK-DOC-04: addFiles seeds documentId null — a freshly picked file has no id until preview stores its bytes', () => {
    const result = addFiles([], [new File([], 'a.csv'), new File([], 'b.csv')])
    expect(result.files).toHaveLength(2)
    expect(result.files.map((pf) => pf.documentId)).toEqual([null, null])
  })

  // Cross-file contamination guard: an index-shifted or positional pairing would hand
  // file 2 file 1's id.
  it("BULK-DOC-05: a file with no previewed entry keeps a null document id — it never inherits a neighbour's", () => {
    const files = mkPicked(['a.csv', 'b.csv'])
    const previewed = [{ fileId: files[1].id, preview: mkDocPreview(SHARED_COLS, DOC_1) }]

    const result = attachDocumentIds(files, previewed)

    expect(result.map((pf) => pf.documentId)).toEqual([null, DOC_1])
  })
})
