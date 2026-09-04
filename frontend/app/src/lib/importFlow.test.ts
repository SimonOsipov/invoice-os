// RED specs (M4-08-04, task-173, FLOW-01..14) — pin the wizard import step's pure
// helpers (wizardHeader's path resolver, the Read-columns/Import gates, and the Map
// step's whole column derivation) before the executor implements the bodies in
// importFlow.ts. Plan §E is authoritative; the task description's stale "FLOW-01…04,
// FLOW-07" clause does not apply — see importFlow.ts's doc comment.
//
// vitest environment is 'node' (vitest.config.ts) — no jsdom, no Testing Library. File
// is a real node global (-02 §A); no spec here touches a DOM or a component.
//
// Spec map (AC coverage complete — plan §E):
//   FLOW-01  canReadColumns: gates on the file ONLY — no entity required            (AC3)
//   FLOW-02  canReadColumns rejects a non-csv/xlsx extension (e.g. .pdf)          (AC1,3)
//   FLOW-03  canStartImport: needs preview AND invoice_number placed, not all 11    (AC3)
//   FLOW-04  canStartImport delegates to canSubmitMapping, never re-derives         (AC3)
//   FLOW-05  hasImportableExtension: case-insensitive, last-segment match only      (AC1)
//   FLOW-06  previewColumns: column-major samples, not row-major                    (AC2)
//   FLOW-07  previewColumns: ragged sample_rows read as '' not undefined            (AC2)
//   FLOW-08  previewColumns: duplicate headers kept as distinct entries             (AC2)
//   FLOW-09  isMappableColumn: '' blocked, whitespace-only header stays mappable    (AC2)
//   FLOW-10  columnLetter: A..Z, AA, AB, ... past column 26                         (AC2)
//   FLOW-11  wizardHeader document set: form -> WIZARD_STEPS (Enter) at 0             (AC6)
//   FLOW-12  wizardHeader import set: upload/mapping/review — one path per CreateStep (AC2,6)
//   FLOW-14  wizardHeader totality: every CreateStep literal, never undefined/NaN     (AC6)
//
// FLOW-15..17 (task-304, INVCR-01-19, Test-first): computeNoEntity, CreateUpload's amber-
// panel predicate, extracted for testability under the no-jsdom constraint. See its own
// doc comment in importFlow.ts and the block near the end of this file.
//
// Every spec below currently fails because wizardHeader/hasImportableExtension/
// canReadColumns/canStartImport/isMappableColumn/columnLetter/previewColumns's stub
// bodies throw `new Error('not implemented')` before ever returning anything — that IS
// the correct RED reason (assertion / not-implemented), not an import/compile error.
import { readdirSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import { ENTER_STEPS, WIZARD_STEPS } from '../data'
import { canSubmitMapping } from './mapping'
import {
  IMPORT_STAGE_OF,
  IMPORT_STEPS,
  MAX_UPLOAD_BYTES,
  STAGE_OF,
  canReadColumns,
  canStartImport,
  classifyPickedFile,
  columnLetter,
  computeNoEntity,
  hasImportableExtension,
  isMappableColumn,
  previewColumns,
  wizardHeader,
} from './importFlow'
import type { PickedKind, WizardPath } from './importFlow'
import type { Entity } from './portfolio'
import type { ImportPreview } from './importApi'
import type { CreateStep, Mapping } from '../types'

// EXTR-09-06: 'documents' joins CreateStep in Stage 3, not here — the cast keeps
// STEPS-D3's key-set assertion genuinely red instead of green at birth. Same precedent as
// 'review', which this file cast until INVCR-01-04 widened the union. Stage 3 drops it.
const DOCUMENTS_STEP = 'documents' as unknown as CreateStep

function mkPreview(overrides: Partial<ImportPreview> = {}): ImportPreview {
  return {
    document_id: '9c4e1f70-6a2b-4d18-9e33-0b57c8a1d2e4',
    format: 'csv',
    delimiter: ',',
    encoding: 'utf-8',
    columns: ['Invoice No', 'Total'],
    sample_rows: [['INV-1', '100']],
    rows_total: 9,
    ...overrides,
  }
}

const csvFile = new File([], 'invoice.csv')
const pdfFile = new File([], 'scan.pdf')

describe('canReadColumns (FLOW-01, FLOW-02)', () => {
  // FLOW-01 — the gate is the FILE, and only the file. Reading columns posts the bytes to
  // /imports/preview, which takes no entity_id and persists nothing, so requiring an entity
  // here bought no safety and cost in-house workspaces (permanently-null entityId) the
  // wizard's entire first step. Falsification: an impl that reintroduces an entity clause,
  // or one that accepts a null file.
  it('requires a real file, and does NOT require an entity', () => {
    expect(canReadColumns(null)).toBe(false)
    expect(canReadColumns(csvFile)).toBe(true)
  })

  // FLOW-02 — falsification: an impl omitting the extension check, which lets a PDF
  // reach previewImport and 400 server-side.
  it('rejects a non-csv/xlsx file', () => {
    expect(canReadColumns(pdfFile)).toBe(false)
  })
})

describe('canStartImport (FLOW-03, FLOW-04)', () => {
  const preview = mkPreview()
  const placed: Mapping = { invoice_number: 'A' }

  // FLOW-03 — falsification: an impl ignoring preview (Import enabled before columns
  // are read); an impl requiring all 11 CANON fields placed (stricter than the server).
  it('needs a preview AND invoice_number placed — not every field', () => {
    expect(canStartImport(null, placed)).toBe(false)
    expect(canStartImport(preview, { invoice_number: null, total: 'T' })).toBe(false)
    expect(canStartImport(preview, { invoice_number: 'A' })).toBe(true)
  })

  // FLOW-04 — falsification: a re-derived `!!m.invoice_number` inside importFlow that
  // can drift from M4-08-03's shipped gate. Cross-checks against the real
  // canSubmitMapping across every mapping shape, not a mirrored literal.
  it('delegates to canSubmitMapping for every mapping shape, never re-derives the gate', () => {
    const shapes: Mapping[] = [
      { invoice_number: null, total: null }, // none placed
      { invoice_number: 'A' }, // only invoice_number
      { invoice_number: 'A', total: 'T' }, // invoice_number + others
      { invoice_number: null, total: 'T' }, // others only, no invoice_number
    ]
    shapes.forEach((m) => {
      expect(canStartImport(preview, m)).toBe(canSubmitMapping(m))
    })
  })
})

describe('hasImportableExtension (FLOW-05)', () => {
  // Falsification: `.includes('.csv')` (passes 'a.csv.bak'); a case-sensitive impl
  // (fails 'A.CSV').
  it('matches .csv/.xlsx case-insensitively, last segment only', () => {
    expect(hasImportableExtension('a.csv')).toBe(true)
    expect(hasImportableExtension('a.xlsx')).toBe(true)
    expect(hasImportableExtension('A.CSV')).toBe(true)
    expect(hasImportableExtension('a.CsV')).toBe(true)
    expect(hasImportableExtension('a.csv.bak')).toBe(false)
    expect(hasImportableExtension('a.pdf')).toBe(false)
    expect(hasImportableExtension('a.xls')).toBe(false)
    expect(hasImportableExtension('csv')).toBe(false)
  })
})

describe('previewColumns (FLOW-06..09)', () => {
  // FLOW-06 — falsification: a ROW-major read (sample_rows[ci]) instead of
  // column-major — the shape swap the header-keyed `r[h]` at CreateMapping.tsx:37 invites.
  it('reads samples column-major: samples[r] is row r of THIS column, not row ci', () => {
    const preview = mkPreview({
      columns: ['A', 'B'],
      sample_rows: [
        ['1', '2'],
        ['3', '4'],
      ],
    })
    const cols = previewColumns(preview, 3)
    expect(cols).toHaveLength(2)
    expect(cols[0].samples).toEqual(['1', '3'])
    expect(cols[1].samples).toEqual(['2', '4'])
  })

  // FLOW-07 — ragged sample_rows ([preview-samples], PRV-09): rows are verbatim and
  // unpadded. Falsification: an unguarded `row[ci]`, which yields undefined —
  // toEqual distinguishes [undefined] from [''].
  it('reads a short (ragged) row as an empty string cell, never undefined', () => {
    const preview = mkPreview({ columns: ['A', 'B', 'C', 'D'], sample_rows: [['1', '2']] })
    const cols = previewColumns(preview, 3)
    expect(cols[2].samples).toEqual([''])
    expect(typeof cols[2].samples[0]).toBe('string')
  })

  // FLOW-08 — falsification: any impl that dedupes or keys by header (what
  // `key={col.header}` and a header-keyed record do), collapsing a real column.
  it('keeps duplicate headers as distinct entries, one per column index', () => {
    const preview = mkPreview({ columns: ['VAT', 'VAT', 'Total'], sample_rows: [['1', '2', '3']] })
    const cols = previewColumns(preview, 3)
    expect(cols).toHaveLength(3)
    expect(cols[0].header).toBe('VAT')
    expect(cols[1].header).toBe('VAT')
    expect(cols[0].letter).toBe('A')
    expect(cols[1].letter).toBe('B')
    expect(cols[2].letter).toBe('C')
  })

  // FLOW-09 — falsification: an all-true impl (blank column becomes a silent-drop
  // target); AND a `.trim() !== ''` impl, which fails the whitespace-header case — a
  // stricter-than-server gate blocking a column resolveMapping matches exactly (Core AC3).
  it('blocks only the empty-string header — a whitespace-only header stays mappable', () => {
    expect(isMappableColumn('')).toBe(false)
    expect(isMappableColumn('Total')).toBe(true)
    expect(isMappableColumn('   ')).toBe(true)

    const preview = mkPreview({ columns: ['', 'Total', '   '], sample_rows: [['a', 'b', 'c']] })
    const cols = previewColumns(preview, 3)
    expect(cols[0].mappable).toBe(false)
    expect(cols[1].mappable).toBe(true)
    expect(cols[2].mappable).toBe(true)
  })
})

describe('columnLetter (FLOW-10)', () => {
  // Falsification: the shipped String.fromCharCode(65+ci) (CreateMapping.tsx:34), which
  // returns '[' at ci===26 — reachable the moment a real 27-column export is imported.
  it('spells spreadsheet-style letters past column 26 instead of breaking into ASCII', () => {
    expect(columnLetter(0)).toBe('A')
    expect(columnLetter(25)).toBe('Z')
    expect(columnLetter(26)).toBe('AA')
    expect(columnLetter(27)).toBe('AB')
    expect(columnLetter(51)).toBe('AZ')
    expect(columnLetter(52)).toBe('BA')
  })
})

describe('wizardHeader (FLOW-11, FLOW-12, FLOW-14)', () => {
  // EXTR-09-06: WIZARD_STEPS is the DOCUMENT strip now (Import · Review); manual entry's
  // one-item strip moved to ENTER_STEPS, so the typed assertion follows it there.
  it('routes the typed step to ENTER_STEPS at its existing stage index', () => {
    expect(wizardHeader('form', 'typed')).toEqual({ steps: ENTER_STEPS, stageIndex: 0 })
    expect(WIZARD_STEPS.length).toBe(2)
  })

  // FLOW-12 absorbs FLOW-13's one surviving assertion (bare 'upload' -> IMPORT_STEPS@0)
  // now that there is no second file arg left to disambiguate with.
  it('routes upload/mapping/review to the 3-step import list', () => {
    expect(wizardHeader('upload')).toEqual({ steps: IMPORT_STEPS, stageIndex: 0 })
    expect(wizardHeader('mapping')).toEqual({ steps: IMPORT_STEPS, stageIndex: 1 })
    expect(wizardHeader('review')).toEqual({ steps: IMPORT_STEPS, stageIndex: 2 })
    expect(IMPORT_STEPS.length).toBe(3)
  })

  // FLOW-14, rewritten (INVCR-01-04, task-280). The old loop's
  // `toBeGreaterThanOrEqual(3)` breaks the moment WIZARD_STEPS shrinks to 2
  // entries, and relaxing it to `>= 1` would assert nothing — replaced with
  // the exact [step, len, idx] table (plan §5). Authored RED against the
  // pre-rename tables and observed failing for the right reason first: the
  // 'form' row threw before the loop reached any other (steps.length was 5,
  // not 2), and the 'review' row would independently have failed too, its
  // stageIndex resolving to 0 through the `?? 0` fallback rather than the
  // real 2. 'review' was cast per this file's own QA-WH-FALLBACK precedent
  // below until the union widened; the cast is gone now that it is a real
  // CreateStep member.
  // [closes-d-04a-typed-review-residual]: 'form' row reflects WIZARD_STEPS at 1 item.
  it('is total over every CreateStep literal — stageIndex is always a valid index', () => {
    const ALL: Array<[CreateStep, number, number]> = [
      ['upload', 3, 0],
      ['mapping', 3, 1],
      ['form', 1, 0],
      ['review', 3, 2],
    ]
    ALL.forEach(([step, len, idx]) => {
      const { steps, stageIndex } = wizardHeader(step)
      expect(steps.length, step).toBe(len)
      expect(stageIndex, step).toBe(idx)
      expect(Number.isInteger(stageIndex)).toBe(true)
      expect(stageIndex).toBeLessThan(steps.length)
    })
  })

  // EXTR-09-06 turns this guard around: wizardHeader takes the run kind as a second
  // argument now, so a 1-arg regression makes the two-arg call below a TS2554 and
  // typecheck fails on THIS line. The directive moves onto a THIRD argument, which is
  // still refused; if a third parameter is ever added it goes unused (TS2578) and fails
  // here too.
  it('AC-2: wizardHeader takes the step and the run kind — two arguments, never three', () => {
    const twoArg = wizardHeader('upload', 'import')
    expect(twoArg).toEqual({ steps: IMPORT_STEPS, stageIndex: 0 })
    // @ts-expect-error — a third argument does not compile.
    const threeArg = wizardHeader('upload', 'import', null)
    expect(threeArg).toEqual({ steps: IMPORT_STEPS, stageIndex: 0 })
  })
})

// ============================================================================
// INVCR-01-04 (task-280), Mode A — RED-FIRST specs authored BEFORE the rename
// landed, against the pre-rename tables at e08fb68 (verified independently
// before authoring) and observed failing for the right reason first: every
// assertion below failed at RUNTIME on a real value mismatch, never at
// compile time, and only the rename itself turned them green. 'review' was
// not yet a member of CreateStep then, so where a value of that type was
// required these reused the file's own QA-WH-FALLBACK precedent further down
// ('reconcile' as unknown as CreateStep) rather than widening the union
// early; the union widened and those casts dropped in the GREEN commit. They
// stay here as REGRESSION guards. Nothing below re-derives an expectation
// from the code under test: STAGE_OF/IMPORT_STAGE_OF/WIZARD_STEPS/
// IMPORT_STEPS' target values are the literal tables from the plan.
// ============================================================================
describe('INVCR-01-04 three-stage model — report -> review (RED, task-280)', () => {
  const reviewStep: CreateStep = 'review'

  // AC-1. Falsification: an impl that leaves 'Report' as the third label, or
  // reorders/renames either of the other two. RED before the rename:
  // IMPORT_STEPS[2][1] was 'Report'.
  it('STEPS-1: import strip is Import · Map · Review', () => {
    expect(IMPORT_STEPS).toEqual([
      ['1', 'Import'],
      ['2', 'Map'],
      ['3', 'Review'],
    ])
  })

  // EXTR-09-06: the typed strip moved to ENTER_STEPS; WIZARD_STEPS is the document strip
  // now, whose literal STEPS-D1 owns.
  it('STEPS-2: typed strip is Enter', () => {
    expect(ENTER_STEPS).toEqual([['1', 'Enter']])
  })

  // AC-2. STAGE_OF must stay a TOTAL Record<CreateStep, number>, not become
  // partial — an impl that drops a key, or renames the TYPE member but
  // leaves the runtime key as 'report', both fail this. RED before the
  // rename: form was 2 (not 0) and the runtime key was 'report'.
  it('STEPS-3: STAGE_OF is total, renamed to review, and form resolves to 0', () => {
    // EXTR-09-06: 'documents' joins at stage 0. `review` still mirrors IMPORT_STAGE_OF
    // (QA-MIRROR-1 below), never the document strip's own index 1.
    expect(STAGE_OF).toEqual({ upload: 0, mapping: 1, form: 0, review: 2, documents: 0 })
    expect(Object.keys(STAGE_OF)).not.toContain('report')
    expect(Object.keys(STAGE_OF)).toHaveLength(5)
  })

  // AC-2. RED before the rename: IMPORT_STAGE_OF's third key was 'report'.
  it('STEPS-3b: IMPORT_STAGE_OF is upload/mapping/review', () => {
    expect(IMPORT_STAGE_OF).toEqual({ upload: 0, mapping: 1, review: 2 })
  })

  // AC-3. The case that actually exercises the rename through wizardHeader:
  // IMPORT_STAGE_OF must carry a REAL 'review' entry rather than resolve
  // through the `?? 0` fallback that QA-WH-FALLBACK (below) proves exists
  // for a step NOT in the union — 'review' IS in the union post-rename, so
  // it must not need that fallback. RED before the rename: IMPORT_STAGE_OF
  // had no 'review' key, so stageIndex resolved to 0 via the fallback rather
  // than the real 2.
  it('STEPS-4: review resolves to the import path at index 2', () => {
    expect(wizardHeader(reviewStep)).toEqual({ steps: IMPORT_STEPS, stageIndex: 2 })
  })

  // AC-3. RED before the rename: STAGE_OF.form was 2 (the old 5-item index),
  // so wizardHeader('form') returned stageIndex 2, not 0.
  it('STEPS-5: typed path lights Enter at index 0', () => {
    // EXTR-09-06: the strip is ENTER_STEPS now, for every run kind.
    expect(wizardHeader('form', 'typed')).toEqual({ steps: ENTER_STEPS, stageIndex: 0 })
  })

  // AC-4. Falsification: an impl that renames the type member but leaves any
  // one of the four retired labels sitting in either table. RED before the
  // rename: all four retired labels were present (Build/Validate/Approve in
  // WIZARD_STEPS, Report in IMPORT_STEPS) and 'Review' appeared zero times,
  // not twice.
  // [closes-d-04a-typed-review-residual]: WIZARD_STEPS no longer carries a 'Review'
  // label, so only IMPORT_STEPS' own entry remains.
  it('STEPS-6: no removed stage name survives in any table', () => {
    // EXTR-09-06: three strips now, and 'Review' is the last stage on TWO of them —
    // the import report and the document run's own review surface.
    const labels = [...IMPORT_STEPS, ...WIZARD_STEPS, ...ENTER_STEPS].map(([, label]) => label)
    expect(labels.length, 'vacuity floor: the three strips are not all empty').toBeGreaterThan(0)
    expect(labels).not.toContain('Build')
    expect(labels).not.toContain('Validate')
    expect(labels).not.toContain('Approve')
    expect(labels).not.toContain('Report')
    expect(labels.filter((l) => l === 'Review')).toHaveLength(2)
  })

  // AC-6. The ONLY runtime proof that App.tsx's setCreateStep(...) call in
  // startImport and CreateFlow.tsx's step-equality branch actually changed —
  // every spec above pins a VALUE the compiler doesn't force, but those two
  // call sites compile unchanged either way (they don't reference STAGE_OF/
  // WIZARD_STEPS/IMPORT_STEPS at all). Needle built from concatenated parts
  // and self-excluded, same idiom as QA-MOCK-2/QA-DEL-2 below: THIS file
  // legitimately spells the quoted literal and always will — STEPS-3 above
  // asserts Object.keys(STAGE_OF) does NOT contain it, and this spec's own
  // title names it — so without the exclusion the scan would match itself
  // and could never fail. Verified NOT to false-positive on importReport/
  // ImportReport/reportSummary/ctx.report/setReport (no closing quote
  // immediately follows "report" in any of those), nor on the plural View
  // member 'reports', nor on CreateReport.tsx's double-quoted step comment.
  // RED before the rename: 4 real hits — App.tsx, types.ts, CreateFlow.tsx,
  // lib/importFlow.ts (verified independently via a standalone source scan
  // before authoring this spec).
  it("STEPS-7: the quoted CreateStep literal 'report' is gone from frontend/app/src", () => {
    const srcRoot = fileURLToPath(new URL('..', import.meta.url))
    const selfRelPath = path.join('lib', 'importFlow.test.ts')
    const needle = "'rep" + "ort'"
    const hits = scanForIdentifier(srcRoot, needle).filter((relPath) => relPath !== selfRelPath)
    expect(hits.sort()).toEqual([])
  })
})

// QA adversarial coverage (Stage 4, task-280 verification). importFlow.ts's own STAGE_OF
// doc comment warns: "Do NOT dedupe this against IMPORT_STAGE_OF ... Their values mirror
// IMPORT_STAGE_OF's so the two tables can never disagree" — but nothing before this point
// enforces that as an INVARIANT. STEPS-3/STEPS-3b pin literal snapshots of both tables;
// if a future edit changed one table's shared-key values and updated both literals to
// match (or if either literal spec is later modified/removed), a real divergence between
// the two tables would ship undetected. These specs derive their expectation from the
// tables themselves, not from a re-typed literal, so they survive that scenario.
describe('STAGE_OF / IMPORT_STAGE_OF structural invariants (QA, task-280)', () => {
  it('QA-MIRROR-1: STAGE_OF never disagrees with IMPORT_STAGE_OF on their shared keys', () => {
    const sharedKeys = Object.keys(IMPORT_STAGE_OF) as CreateStep[]
    sharedKeys.forEach((key) => {
      expect(STAGE_OF[key], key).toBe(IMPORT_STAGE_OF[key])
    })
    // Positive companion: guards the loop above against vacuously passing over an
    // empty key set (e.g. if IMPORT_STAGE_OF were ever accidentally emptied).
    expect(sharedKeys.length).toBeGreaterThan(0)
    // 'form' is DOCUMENT_ONLY -- deliberately absent from IMPORT_STAGE_OF, so it must
    // never appear in the "shared keys" this spec walks (would make the loop assert
    // nothing about the one entry that actually differs between the two tables).
    expect(sharedKeys).not.toContain('form')
  })

  // D-04a case (b) (plan §1): a step AHEAD names a place to stand that doesn't exist —
  // exactly what Build/Validate/Approve were on the retired 5-item strip. Every
  // import-path CreateStep (upload/mapping/review) IS reachable via IMPORT_STAGE_OF,
  // so IMPORT_STEPS' length must always equal the highest reachable index + 1, derived
  // structurally rather than re-pinning the literal STEPS-1 already checks — this still
  // catches a phantom trailing IMPORT_STEPS entry even if STEPS-1's literal is edited to
  // match it. (D-04a case (b), "Recorded residual, flagged not fixed": closed, not
  // reversed. The WIZARD_STEPS exemption above is retired now that it's 1 item — see
  // QA-NO-PHANTOM-TYPED-STEP below.)
  it('QA-NO-PHANTOM-IMPORT-STEP: IMPORT_STEPS carries no entry past IMPORT_STAGE_OF’s reachable ceiling', () => {
    const reachableIndices = Object.values(IMPORT_STAGE_OF) as number[]
    const maxReachable = Math.max(...reachableIndices)
    expect(IMPORT_STEPS.length).toBe(maxReachable + 1)
  })

  // EXTR-09-06: the typed strip is ENTER_STEPS now; STAGE_OF.form is still its only
  // reachable index. WIZARD_STEPS' own ceiling moves to STEPS-D7.
  it('QA-NO-PHANTOM-TYPED-STEP: ENTER_STEPS carries no entry past STAGE_OF.form’s reachable ceiling', () => {
    expect(ENTER_STEPS.length).toBe(STAGE_OF.form + 1)
  })
})

// INVCR-01-01 (task-277): the ONE genuinely RED-first spec in that subtask's deletion of
// the sandbox PDF/JPG document mock. Authored against the pre-deletion code, where
// STAGE_OF still carried a runtime `parsing: 0` entry, and observed failing for the right
// reason first (`expected [ Array(7) ] to not include 'parsing'`); only deleting 'parsing'
// from CreateStep and STAGE_OF turned it green. Every other importFlow.test.ts change
// task-277 made was a green→green narrowing of a table that already passed — do not
// mistake those for red-first specs.
//
// It stays as a REGRESSION guard: the runtime key set, not just the type. 'parsing' left
// the CreateStep union, so a reintroduced literal would be a compile error — but STAGE_OF
// is a plain object and an extra runtime key would otherwise slip in silently.
describe("STAGE_OF runtime shape (task-277 AC-1, AC-8 — RED-first)", () => {
  it("has no 'parsing' stage left in the runtime step tables", () => {
    expect(Object.keys(STAGE_OF)).not.toContain('parsing')
    // 6 -> 4 in INVCR-01-03, 4 -> 5 in EXTR-09-06 ('documents'); STEP-1 below owns the count.
    expect(Object.keys(STAGE_OF)).toHaveLength(5)
  })

  // STEP-1 (INVCR-01-03, task-279, Mode A — RED-first): 'validating'/'results' leave
  // CreateStep here -- the mock validate/approve tail's own two steps. STAGE_OF stays a
  // total Record over the shrunk union (4 keys: upload/mapping/form/review, the last
  // renamed from its old name by INVCR-01-04) -- see
  // task-279 plan §5.6/§10. Genuinely RED at runtime today: STAGE_OF still carries both
  // keys (6 total), so this discriminates against an INCOMPLETE deletion, not a wrong
  // algorithm.
  it("STEP-1: 'validating'/'results' leave the runtime step table, which stays a total Record", () => {
    expect(Object.keys(STAGE_OF)).not.toContain('validating')
    expect(Object.keys(STAGE_OF)).not.toContain('results')
    // 4 -> 5 in EXTR-09-06: 'documents' joins the union.
    expect(Object.keys(STAGE_OF)).toHaveLength(5)
  })
})

// QA (M4-08-04): adversarial/edge coverage beyond the architect's FLOW-01..12,14
// specs. New describe blocks only — nothing above this point is modified.
describe('wizardHeader — full truth table over every CreateStep (QA)', () => {
  // Literal expected values, not re-derived from STAGE_OF/IMPORT_STAGE_OF. The 1-arg
  // signature makes every step a pure function of createStep alone, so there is no
  // file-state axis left to hold constant across (FLOW-13's old reason is gone with it).
  it('routes typed-only steps to ENTER_STEPS at their fixed index', () => {
    // EXTR-09-06: renamed with the strip. 'form' answers ENTER_STEPS for EVERY run kind.
    const expected: Array<[CreateStep, number]> = [['form', 0]]
    expected.forEach(([step, idx]) => {
      expect(wizardHeader(step, 'typed')).toEqual({ steps: ENTER_STEPS, stageIndex: idx })
      expect(wizardHeader(step, 'document')).toEqual({ steps: ENTER_STEPS, stageIndex: idx })
    })
  })

  // Gains ['upload', 0] here (absorbed from deleted QA-3/FLOW-13): import-side table
  // is now total over every IMPORT_STEPS-routed CreateStep, not just mapping/review.
  it('routes upload/mapping/review to IMPORT_STEPS at their fixed index', () => {
    const expected: Array<[CreateStep, number]> = [['upload', 0], ['mapping', 1], ['review', 2]]
    expected.forEach(([step, idx]) => {
      expect(wizardHeader(step)).toEqual({ steps: IMPORT_STEPS, stageIndex: idx })
    })
  })

  // QA (Stage 4, task-277): STAGE_OF is typed Record<CreateStep, number> — a compiler-
  // enforced EXHAUSTIVE mapped type. Add or remove a CreateStep member without updating
  // STAGE_OF and the file fails to compile, so STAGE_OF's own key set is ground truth
  // for "every CreateStep member that currently exists" — unlike the hand-maintained
  // ALL_STEPS/table arrays scattered through this file, which are plain literals the
  // compiler does not check against the union at all. This pins those hand-written lists
  // to the compiler-enforced source, so a future CreateStep addition that updates
  // STAGE_OF but forgets DOCUMENT_ONLY_STEPS/IMPORT_STAGE_OF is caught by a set-equality
  // failure here, rather than only by silently falling through the `?? 0` fallback
  // (covered separately below).
  it('QA-WH-KEYS / STEPS-D6: the typed/import/document cover matches exactly the members STAGE_OF is compiler-required to have', () => {
    // EXTR-09-06: three sets, and 'review' is deliberately in TWO of them — that overlap
    // is the whole reason wizardHeader needs the run kind (AC-4). So this is a COVER, not
    // a disjoint partition, and the old disjointness clause retires with the two-path model.
    const typedSet: CreateStep[] = ['form']
    const importSet: CreateStep[] = ['upload', 'mapping', 'review']
    const documentSet: CreateStep[] = [DOCUMENTS_STEP, 'review']
    const cover = [...new Set([...typedSet, ...importSet, ...documentSet])].sort()
    // Both directions, plus a floor so an emptied STAGE_OF could not satisfy this vacuously.
    expect(cover).toEqual(Object.keys(STAGE_OF).sort())
    expect(cover.length).toBe(5)
    Object.keys(STAGE_OF).forEach((k) => expect(cover, k).toContain(k))
  })

  // QA (Stage 4, task-277): none of the 6 real CreateStep members exercises the `?? 0`
  // fallback today — STAGE_OF is total (every document-only step has a real entry) and
  // IMPORT_STAGE_OF's 3 keys are exactly the 3 steps NOT in DOCUMENT_ONLY_STEPS, so
  // `IMPORT_STAGE_OF[createStep] ?? 0` is currently DEAD for every value the type system
  // can actually produce. The comment above wizardHeader promises this fallback protects
  // a FUTURE union member added without a matching IMPORT_STAGE_OF entry (FLOW-14's own
  // docstring, carried over from task-173) — the only way to prove that promise without
  // waiting for such a member to exist for real is a type-unsafe cast, deliberately
  // isolated to this one test and nowhere else in the file.
  it('QA-WH-FALLBACK: a hypothetical CreateStep absent from IMPORT_STAGE_OF resolves to the import path at index 0 — never undefined or NaN', () => {
    const hypotheticalStep = 'reconcile' as unknown as CreateStep
    const result = wizardHeader(hypotheticalStep)
    expect(result).toEqual({ steps: IMPORT_STEPS, stageIndex: 0 })
    expect(Number.isNaN(result.stageIndex)).toBe(false)
    expect(result.stageIndex).not.toBeUndefined()
  })
})

// QA (Stage 4, task-277): a source-scanning meta test in the style of
// invoices.test.ts:1514-1532's scanForIdentifier/INV-06-T11b — the single highest-value
// guard for a DELETION subtask, because every other test above can only prove the
// current implementation is correct; it cannot stop the deleted mock's identifiers from
// creeping back into a later, unrelated change. Makes the deletion permanent rather than
// a one-off.
describe('the deleted PDF/JPG document mock does not creep back into frontend/app/src (QA adversarial, task-277)', () => {
  // Root resolved from THIS file's own location, never process.cwd() — vitest may run
  // from the monorepo root or from frontend/app, and cwd-relative traversal would
  // silently scan the wrong subtree depending on which (same rationale as
  // invoices.test.ts's own scanForIdentifier root).
  const srcRoot = fileURLToPath(new URL('..', import.meta.url))
  const selfRelPath = path.join('lib', 'importFlow.test.ts')

  it('QA-MOCK-1: sanity — the scan actually walks and reads real files (positive companion)', () => {
    // A walker that silently visited nothing (wrong root, swallowed error, empty dir)
    // would make every negative assertion below pass vacuously. 'wizardHeader' is known
    // to exist in this very directory.
    expect(scanForIdentifier(srcRoot, 'wizardHeader').length).toBeGreaterThan(0)
  })

  it('QA-MOCK-2: the deleted sample-file list, its label list, and its type export never reappear as identifiers anywhere under src', () => {
    // Built from parts so this test's own source text never literally contains the
    // needles verbatim — a literal spelling in the needle itself (or in this test's own
    // title/comments) would make the scan match this file and could never fail even if
    // the identifier were reintroduced elsewhere. Self-excluded defensively too, the
    // same way QA-MOCK-3 below is, in case a future edit ever spells one out here.
    const needles = ['SAMPLE' + '_FILES', 'PARSE' + '_LABELS', 'Sample' + 'FileDef']
    needles.forEach((needle) => {
      const hits = scanForIdentifier(srcRoot, needle).filter((relPath) => relPath !== selfRelPath)
      expect(hits, needle).toEqual([])
    })
  })

  it("QA-MOCK-3: the quoted CreateStep literal 'parsing' does not reappear outside this file's own runtime-shape regression guard", () => {
    // This file is deliberately excluded: the 'STAGE_OF runtime shape (task-277 AC-1,
    // AC-8 — RED-first)' describe block above must spell the literal 'parsing' to assert
    // against the real runtime STAGE_OF object, and its surrounding prose names the
    // deleted union member too. Everywhere else in frontend/app/src the quoted string has
    // zero legitimate reason to exist any more: 'parsing' left the CreateStep union whole
    // (types.ts) and its runtime STAGE_OF entry in this same deletion (task-277).
    const hits = scanForIdentifier(srcRoot, "'parsing'").filter((relPath) => relPath !== selfRelPath)
    expect(hits).toEqual([])
  })

  // DEL-1 (INVCR-01-03, task-279, Mode A — RED-first): the mock validate/approve
  // tail's own permanence guard -- its two deleted components and the deleted label
  // list must never creep back anywhere under src. Needles built from concatenated
  // parts (same reason as QA-MOCK-2 above): a literal spelling in the needle -- or
  // anywhere in this comment -- would let the scan match this file's own source and
  // never fail even if the identifier were reintroduced elsewhere. Genuinely RED at
  // runtime today: all three identifiers this spec checks for are still live in the
  // tree (verified independently before authoring this spec) -- it discriminates
  // against an INCOMPLETE deletion, not a wrong algorithm.
  it('DEL-1: the deleted results/scanline components and the deleted label list never creep back', () => {
    const needles = ['VAL_' + 'LABELS', 'Scanline' + 'Steps', 'Create' + 'Results']
    needles.forEach((needle) => {
      const hits = scanForIdentifier(srcRoot, needle).filter((relPath) => relPath !== selfRelPath)
      expect(hits, needle).toEqual([])
    })
  })

  // QA (Stage 4, task-279): extends DEL-1's same scanner idiom (not a third copy) over
  // the REST of AC-3's deleted-identifier list that DEL-1 itself does not check --
  // runValidation, applyFix, backToEdit, warnGlyph, and the valTimer/valDone/clearVal
  // trio. Each is confirmed zero-hits under src today (verified independently before
  // authoring this spec, same as DEL-1's own claim) and none is built from concatenated
  // parts here because none is a plausible English/UI word -- unlike DEL-1's own
  // needles, there is no legitimate surviving surface these could collide with.
  //
  // Deliberately NOT scanned: `approve` and `valIdx`. `approve` is a live substring of
  // the unrelated Workflows approval-policy feature (WorkflowInspector.tsx/
  // WorkflowBuilder.tsx/WorkflowParts.tsx, plus lib/workflows.ts's AutoApproveNode and
  // the 'Approver' role labels), so a scan for it would fail on legitimate, in-scope
  // code and teach nothing about the deleted mock's `approve()` handler specifically.
  // (This clause used to cite data.tsx's WIZARD_STEPS strip label 'Approve' as a second
  // reason; INVCR-01-04 retired that label, but the Workflows reason alone still stands
  // and still forbids the scan.)
  it('QA-DEL-2: the rest of the deleted validate/approve tail (runValidation, applyFix, backToEdit, warnGlyph, valTimer, valDone, clearVal) never creeps back', () => {
    const needles = ['runValidation', 'applyFix', 'backToEdit', 'warnGlyph', 'valTimer', 'valDone', 'clearVal']
    needles.forEach((needle) => {
      const hits = scanForIdentifier(srcRoot, needle).filter((relPath) => relPath !== selfRelPath)
      expect(hits, needle).toEqual([])
    })
  })
})

// AC7-1 (INVCR-01-03, task-279, Mode A — RED-first): lib/validation.ts SURVIVES this
// subtask (task-279 plan §1, [validation-module-survives]) -- lib/clients.ts needs it
// for the mock dashboard's own failing-count. This is the ACHIEVABLE half of the
// story's original zero-hits grep AC, rewritten to name the one legitimate importer
// that must remain rather than assert nobody imports it at all (impossible while
// clients.ts still does). Genuinely RED at runtime today: the validation module still
// has TWO importers, App.tsx and lib/clients.ts -- discriminating against an
// incomplete deletion, not a wrong algorithm.
describe("lib/validation.ts's import specifier keeps exactly one surviving importer (INVCR-01-03, task-279, AC7-1)", () => {
  const srcRoot = fileURLToPath(new URL('..', import.meta.url))
  const selfRelPath = path.join('lib', 'importFlow.test.ts')

  it("AC7-1: only the mock dashboard module still imports it once the create flow's mock tail is deleted", () => {
    // Needle built from concatenated parts, like DEL-1/QA-MOCK-2 above, so this file's
    // own source text never literally contains the target substring -- a literal
    // spelling here would self-match and could never fail even if a stray importer
    // reappeared. The needle is a slash immediately followed by the six letters of the
    // module's name and the specifier's own closing quote -- so a relative import
    // whose last path segment is exactly that name hits, at any nesting depth, while a
    // differently-named sibling module whose name merely starts the same way does not,
    // because extra letters sit between the match and the closing quote there.
    const needle = '/valid' + "ation'"
    const hits = scanForIdentifier(srcRoot, needle).filter((relPath) => relPath !== selfRelPath)
    expect(hits.sort()).toEqual(['lib/clients.ts'])
  })
})

// Recursively walks `rootDir`, reading every .ts/.tsx file, and returns the relative
// paths of every file whose text contains `needle` as a literal substring. Same
// implementation as invoices.test.ts's scanForIdentifier — duplicated locally rather
// than imported/shared because these are test-only helpers in two independently owned
// spec files, not production code (no shared module would be surgical here).
function scanForIdentifier(rootDir: string, needle: string): string[] {
  const hits: string[] = []
  function walk(dir: string): void {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      const full = path.join(dir, entry.name)
      if (entry.isDirectory()) {
        walk(full)
      } else if (/\.(ts|tsx)$/.test(entry.name) && readFileSync(full, 'utf8').includes(needle)) {
        hits.push(path.relative(rootDir, full))
      }
    }
  }
  walk(rootDir)
  return hits
}

describe('previewColumns — adversarial edge cases (QA)', () => {
  it('returns an empty array for a zero-column preview', () => {
    const preview = mkPreview({ columns: [], sample_rows: [['a', 'b']] })
    expect(previewColumns(preview, 3)).toEqual([])
  })

  it('returns one entry per blank-named column, all unmappable, letters still assigned by index', () => {
    const preview = mkPreview({ columns: ['', '', ''], sample_rows: [['a', 'b', 'c']] })
    const cols = previewColumns(preview, 3)
    expect(cols).toHaveLength(3)
    expect(cols.every((c) => c.mappable === false)).toBe(true)
    expect(cols.map((c) => c.letter)).toEqual(['A', 'B', 'C'])
  })

  it('ignores extra cells on a row longer than the column count — reads only preview.columns.length entries', () => {
    const preview = mkPreview({ columns: ['A'], sample_rows: [['1', '2', '3']] })
    const cols = previewColumns(preview, 3)
    expect(cols).toHaveLength(1)
    expect(cols[0].samples).toEqual(['1'])
  })

  it('returns an empty samples array (not undefined, not a padded row) when sample_rows is empty', () => {
    const preview = mkPreview({ columns: ['A', 'B'], sample_rows: [] })
    const cols = previewColumns(preview, 3)
    expect(cols[0].samples).toEqual([])
    expect(cols[1].samples).toEqual([])
  })
})

describe('columnLetter — three-letter boundary (QA)', () => {
  // FLOW-10 pins 0/25/26/27/51/52 (the one/two-letter boundaries). This extends
  // to the two/three-letter boundary: 676 two-letter combos (AA..ZZ) occupy
  // indices 26..701, so 701 is the last two-letter value and 702 the first
  // three-letter one.
  it('rolls over from the two-letter to the three-letter form at the ZZ/AAA boundary', () => {
    expect(columnLetter(701)).toBe('ZZ')
    expect(columnLetter(702)).toBe('AAA')
  })
})

describe('canReadColumns / canStartImport — truth tables (QA)', () => {
  const preview = mkPreview()

  it('canReadColumns: rejects a non-csv/xlsx file, and a null file', () => {
    expect(canReadColumns(pdfFile)).toBe(false)
    expect(canReadColumns(null)).toBe(false)
  })

  // The in-house regression, stated as a unit fact: an entity-less workspace can still
  // reach the preview. The entity is asserted at the COMMIT (App.tsx startImport) and
  // surfaced by CreateMapping's canFile — never here.
  it('canReadColumns: opens for a workspace with no entity at all — .csv and .xlsx alike', () => {
    expect(canReadColumns(csvFile)).toBe(true)
    expect(canReadColumns(new File([], 'ledger.xlsx'))).toBe(true)
  })

  it('canStartImport: false when mapping is null outright, and false when invoice_number is an empty string (falsy, not "placed")', () => {
    expect(canStartImport(preview, null)).toBe(false)
    expect(canStartImport(preview, { invoice_number: '' })).toBe(false)
  })

  it('canStartImport: false when a mapping is otherwise complete but preview has not been read yet', () => {
    expect(canStartImport(null, { invoice_number: 'A', total: 'T', vat: 'V' })).toBe(false)
  })
})

describe('hasImportableExtension — adversarial edge cases (QA)', () => {
  it('matches an uppercase .XLSX extension', () => {
    expect(hasImportableExtension('REPORT.XLSX')).toBe(true)
  })

  it('rejects a filename with no extension at all', () => {
    expect(hasImportableExtension('invoices_export')).toBe(false)
  })

  it('matches a dotfile whose entire name is the extension — endsWith has no basename requirement', () => {
    expect(hasImportableExtension('.csv')).toBe(true)
    expect(hasImportableExtension('.xlsx')).toBe(true)
  })
})

// RED specs (task-304, INVCR-01-19, Test-first) — pin computeNoEntity's contract before
// the executor implements its body. Every spec below currently fails because the stub
// throws `new Error('not implemented')` before ever returning anything — that IS the
// correct RED reason, not an import/compile error.
//
// This is the ONLY node-testable proof left, at the deployed-e2e layer, that the amber
// panel keeps firing for a zero-entity FIRM once [inhouse-can-start] stops being the sole
// browser test exercising it (AC-6/AC-7/AC-8) — every persona this suite's fixtures can
// sign in as now legitimately has at least one entity (firm's 10 curated rows, in-house's
// one seeded row, db/seed.dev.sql), so a genuinely-zero-entity workspace is no longer
// reachable through any browser spec's fixtures. The predicate itself stays generic and
// unit-tested here instead.
const mkEntity = (id: string): Entity => ({
  id,
  name: 'Acme Ltd',
  tin: '10000000-0001',
  registration: null,
  sector: null,
  address: null,
  status: 'active',
  created_at: '2026-01-01T00:00:00Z',
})

describe('computeNoEntity (FLOW-15..17, task-304 AC-6)', () => {
  // FLOW-15 — the keystone claim AC-6 exists to protect: a FIRM (or any) workspace whose
  // active entity resolved to null, with the fetch settled and the roster caught up,
  // shows the panel. Falsification: an impl that gates on anything persona-shaped instead
  // of `activeEntity` alone.
  it('FLOW-15: fires whenever activeEntity is null and the fetch has genuinely settled', () => {
    expect(computeNoEntity(null, 'ready', 3, 3)).toBe(true)
    expect(computeNoEntity(null, 'empty', 0, 0)).toBe(true)
  })

  // FLOW-15b — positive companion: a resolved entity never fires the panel, regardless of
  // fetch status. Falsification: an impl that fires on `entitiesState` alone.
  it('does not fire once an entity is resolved, whatever the fetch status', () => {
    expect(computeNoEntity(mkEntity('e-1'), 'ready', 3, 3)).toBe(false)
    expect(computeNoEntity(mkEntity('e-1'), 'loading', 3, 0)).toBe(false)
  })

  // FLOW-16 — guard 1 (entityAnswerSettled): 'idle'/'loading'/'error' have not
  // definitively answered yet, so the panel must not flash for that one frame.
  // Falsification: an impl checking only `activeEntity === null`.
  it('FLOW-16: does not fire while the entities fetch has not settled (idle/loading/error)', () => {
    expect(computeNoEntity(null, 'idle', 0, 0)).toBe(false)
    expect(computeNoEntity(null, 'loading', 0, 0)).toBe(false)
    expect(computeNoEntity(null, 'error', 0, 0)).toBe(false)
  })

  // FLOW-17 — guard 2 (rosterCatchingUp): the one-render-late window where entities has
  // already landed but the `clients` effect has not yet rebuilt from it. Falsification: an
  // impl missing this guard would fire the panel for one frame on every load with data.
  it('FLOW-17: does not fire in the one-render roster-catching-up window (entities landed, clients still [])', () => {
    expect(computeNoEntity(null, 'ready', 5, 0)).toBe(false)
  })

  // Positive companion to FLOW-17: once BOTH entities and clients are empty (a genuinely
  // zero-entity tenant, AC-3's bootstrap window), the roster-catching-up guard must not
  // also suppress the honest case — entitiesCount === 0 is never "still catching up".
  it('fires for a genuinely zero-entity tenant — entitiesCount 0 is not mistaken for the catching-up window', () => {
    expect(computeNoEntity(null, 'empty', 0, 0)).toBe(true)
    expect(computeNoEntity(null, 'ready', 0, 0)).toBe(true)
  })
})

// BULK-03-11 (BULK-01-03, task-310) — REGRESSION assertion, not a RED-first spec.
// canReadColumns is UNMODIFIED by BULK-01-03: Core AC 6 ("every file in a run is filed
// against the already-selected entity; the run never asks again") is answered by
// lib/importRun.ts's canReadColumnsAll delegating to this function per-file, never by
// touching it. Restates FLOW-01's no-entity claim under BULK-01-03's own id so a future
// edit made in this subtask's own PR trail cannot silently weaken FLOW-01 without a
// second, independently-authored assertion also failing — same reasoning as STEPS-3/
// STEPS-3b's mirrored-literal guards above. Deliberately GREEN the moment it is authored:
// canReadColumns already ships this behavior today, so there is no stub here to turn RED.
// Keep this green, never weaken it — it is exactly the past regression
// ([inhouse-can-start]) that locked in-house workspaces out of the wizard's front door.
describe('canReadColumns — entity contract did not move (BULK-03-11, BULK-01-03 regression)', () => {
  it('BULK-03-11: a valid file, with no entity anywhere in scope, still opens the read gate', () => {
    expect(canReadColumns(new File([], 'ledger.csv'))).toBe(true)
    expect(canReadColumns(new File([], 'ledger.xlsx'))).toBe(true)
  })
})

// ============================================================================
// FLOW-DOC-01 IS RETIRED — EXTR-09 (EXTR-09-05, task-772), Core AC #4.
// ============================================================================
// The spec that stood here (DOC-01-07, task-355, AC-6) asserted that a 20 MB csv still
// opens the read gate, and its comment read "no size gate exists anywhere in
// frontend/app/src today and none may be introduced". EXTR-09 Core AC #4 REVERSES that
// decision deliberately: the picker now accepts five document types the extractor reads
// byte-by-byte, so a file the server will 413 must be refused where the user can still
// see and remove it, not after a 15 MiB upload.
//
// The guarantee FLOW-DOC-01 actually protected — that no client copy of the limit can
// drift away from the server's — is NOT dropped. It moves to
// TestMaxUploadBytes_MatchesTheBrowserConstant and
// TestMaxUploadBytes_MatchesTheColumnCheck (internal/importer/handlers_upload_once_test.go),
// which read MAX_UPLOAD_BYTES out of importFlow.ts, maxUploadBytes out of handlers.go and
// documents.size_bytes' CHECK ceiling out of the migration, and fail loudly if any of the
// three regexes matches nothing. SIZE-1/SIZE-2 below are what replace the assertion.
//
// Nothing here removes the SERVER's 413: the client gate is an addition, not a substitute.

// SIZE-1..2 (EXTR-09-05, task-772, Mode A) — RED specs for the client-side size gate.
//
// SIZE-1 was authored RED on its assertion, not on a missing export: importFlow.ts's
// Stage-2.5 stub exported MAX_UPLOAD_BYTES and canReadColumns did not consult it, so an
// over-cap file still opened the read gate. SIZE-2 was GREEN at birth by construction — the stub
// carries the real value, because a deliberately-wrong constant would make every boundary
// in SIZE-1 (and both Go agreement tests) meaningless rather than red.
function sizedFile(name: string, size: number): File {
  const f = new File([], name)
  // File([]) is always 0 bytes; the property override is how this suite has always made a
  // size assertion possible without allocating 15 MiB (the retired FLOW-DOC-01 did the
  // same). Asserted below before it is relied on.
  Object.defineProperty(f, 'size', { value: size })
  return f
}

describe('canReadColumns — the client-side size gate (SIZE-1..2, EXTR-09-05)', () => {
  // AC-1. Falsification: a `<` where the spec says `<=` (which would refuse a file of
  // exactly the cap the server accepts), a decimal-MB cap, or a gate that replaces the
  // extension rule instead of ANDing with it.
  it('SIZE-1: the boundary is exact — at the cap accepted, one byte over refused', () => {
    // The override must actually take, or every assertion below reads size 0 and passes
    // vacuously against any implementation.
    expect(sizedFile('probe.csv', MAX_UPLOAD_BYTES + 1).size, 'File.size override did not take').toBe(MAX_UPLOAD_BYTES + 1)

    expect(canReadColumns(sizedFile('under.csv', MAX_UPLOAD_BYTES - 1)), 'one byte under the cap').toBe(true)
    expect(canReadColumns(sizedFile('exact.csv', MAX_UPLOAD_BYTES)), 'exactly the cap').toBe(true)
    expect(canReadColumns(sizedFile('over.csv', MAX_UPLOAD_BYTES + 1)), 'one byte over the cap').toBe(false)
    expect(canReadColumns(sizedFile('way-over.xlsx', 40 * 1024 * 1024)), 'far over the cap').toBe(false)

    // The size gate is ANDed onto the extension rule, never a replacement for it: a
    // small file of an unreadable type is still refused, and an empty csv is still fine.
    expect(canReadColumns(sizedFile('archive.zip', 10))).toBe(false)
    expect(canReadColumns(sizedFile('empty.csv', 0))).toBe(true)
  })

  // AC-1's second half. Falsification: 15_000_000, or 15 * 1000 * 1000.
  it('SIZE-2: the cap is binary MiB, matching the server, never 15,000,000 decimal', () => {
    expect(MAX_UPLOAD_BYTES).toBe(15728640)
    expect(MAX_UPLOAD_BYTES).toBe(15 * 1024 * 1024)
    expect(MAX_UPLOAD_BYTES).not.toBe(15_000_000)
  })
})

// SIZE-5b (EXTR-09-05, task-772, Mode A) — the "exactly one owner" half of AC-2/AC-5.
// SIZE-5a (importRun.test.ts) pins what the sentence SAYS; this pins who may say it.
// Authored RED against a CreateUpload.tsx that spelled its own per-file note inline, so
// the scan returned only lib/importRun.ts.
describe('the size-refusal copy has exactly one owner (SIZE-5b, EXTR-09-05)', () => {
  const srcRoot = fileURLToPath(new URL('..', import.meta.url))

  it('SIZE-5b: oversizeNote is defined in lib/ and rendered by CreateUpload — nobody re-words it', () => {
    // Needle built from parts, same idiom as QA-MOCK-2/DEL-1 below: a literal spelling
    // here would put THIS file in every hit set. Spec files are excluded outright — they
    // legitimately name the function, and they are not the copy's owner either way.
    const needle = 'oversize' + 'Note'
    const isSpec = (relPath: string) => /\.test\.tsx?$/.test(relPath)

    // Population floor: prove the walker reached a real subtree before any set equality
    // below is trusted. Every module under src has at least one `export`.
    expect(scanForIdentifier(srcRoot, 'export').length, 'population floor: the scan must reach the real src tree').toBeGreaterThan(50)
    // Control needle: capRefusal is oversizeNote's shipped sibling and is known to live in
    // exactly these two kinds of place, so a scan that found nothing would fail HERE.
    expect(scanForIdentifier(srcRoot, 'capRefusal').filter((p) => !isSpec(p)).length, 'control needle: capRefusal must be found').toBeGreaterThan(0)

    const hits = scanForIdentifier(srcRoot, needle).filter((p) => !isSpec(p))
    expect(hits.sort()).toEqual([path.join('components', 'CreateUpload.tsx'), path.join('lib', 'importRun.ts')])
  })
})

// CLASSIFY-1..4 (EXTR-09-04, task-771, Test-first) — RED specs for classifyPickedFile, the
// one selection gate that replaces hasImportableExtension. They fail today because the stub
// returns null unconditionally (importFlow.ts), which is an assertion failure, not a
// missing export.
//
// The table below is SPELLED HERE, not read off ACCEPTED_PICKED_TYPES: a spec that
// recomputed the expectation through the implementation's own literal would agree with any
// value it happened to hold. Story §1 is the source. The cross-language pin that the two
// literals stay one table is CLASSIFY-5, in internal/extraction/handlers_upload_test.go.
//
// Load-bearing beyond `accept`: CreateUpload.tsx:123-127 hands a DROPPED file straight to
// addPickedFiles with no filter at all, so the accept attribute gates the OS picker only.
// This function is the real client gate, and widening accept without it widens nothing.
const CLASSIFY_XLSX = 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'
const CLASSIFY_DOCX = 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'

const CLASSIFY_TABLE: Array<[string, string, PickedKind]> = [
  ['ledger.csv', 'text/csv', 'spreadsheet'],
  ['ledger.csv', 'text/plain', 'spreadsheet'],
  ['book.xlsx', CLASSIFY_XLSX, 'spreadsheet'],
  ['scan.pdf', 'application/pdf', 'document'],
  ['letter.docx', CLASSIFY_DOCX, 'document'],
]

// EXTR-15-03: what LEFT the table (BQ-2). Named once; PN-6's population is derived from it, so
// the refusal count follows the narrowing rather than a literal typed beside it.
const CLASSIFY_NARROWED_OUT: Array<[string, string]> = [
  ['scan.png', 'image/png'],
  ['scan.jpg', 'image/jpeg'],
  ['scan.jpeg', 'image/jpeg'],
  ['scan.webp', 'image/webp'],
]

describe('classifyPickedFile (CLASSIFY-1..4, EXTR-09-04)', () => {
  // CLASSIFY-1 — every row of story §1 resolves, by extension AND by declared type. The
  // second half matters because §1's rule is detectFormat's: a recognised extension is the
  // verdict, an UNRECOGNISED one falls through to the declared content type.
  it('CLASSIFY-1: every row of the accepted-type table resolves', () => {
    // Vacuity floor: an empty table would make the loop below assert over nothing.
    expect(CLASSIFY_TABLE.length, 'the narrowed table must have all five rows').toBe(5)

    for (const [name, contentType, expected] of CLASSIFY_TABLE) {
      expect(classifyPickedFile(name, contentType), `${name} declared ${contentType}`).toBe(expected)
    }

    // By content type alone — no extension to read, so the declared type decides.
    expect(classifyPickedFile('scan', 'application/pdf')).toBe('document')
    expect(classifyPickedFile('export', 'text/csv')).toBe('spreadsheet')
    expect(classifyPickedFile('book', CLASSIFY_XLSX)).toBe('spreadsheet')
    expect(classifyPickedFile('letter', CLASSIFY_DOCX)).toBe('document')
  })

  // CLASSIFY-2 — a recognised extension beats a disagreeing declared type, in both
  // directions. Browsers guess Content-Type from the extension anyway, so a disagreement is
  // either a rename or a lie; the extension is the half the user can see.
  //
  // The two languages DISAGREE on ('ledger.csv', 'application/pdf') and that is by
  // construction, not drift. Go's acceptedDocumentTypes (classify.go) holds the DOCUMENT
  // half only, so '.csv' is unrecognised there and classifyDocumentType falls through to
  // the declared 'application/pdf' and accepts the file as a PDF — EXTR-09-02's QA found
  // exactly that. TypeScript's table holds BOTH halves, so '.csv' is recognised and the
  // verdict is 'spreadsheet'. The file therefore never reaches POST /v1/documents on the
  // client path. The two tables agree on the domain they share (the six document
  // extensions); CLASSIFY-5 is what pins that shared domain.
  it('CLASSIFY-2: the extension wins over a disagreeing content type', () => {
    expect(classifyPickedFile('scan.pdf', 'text/csv')).toBe('document')
    expect(classifyPickedFile('a.csv', 'application/pdf')).toBe('spreadsheet')
    expect(classifyPickedFile('book.xlsx', 'image/png')).toBe('spreadsheet')
    expect(classifyPickedFile('letter.docx', 'text/plain')).toBe('document')
  })

  // CLASSIFY-3 — filename case, content-type case and ";charset=" parameters change no
  // verdict. Mirrors mime.ParseMediaType on the Go side (classify.go:33-36).
  it('CLASSIFY-3: case and charset parameters do not change the verdict', () => {
    expect(classifyPickedFile('SCAN.PDF', 'APPLICATION/PDF; charset=utf-8')).toBe('document')
    expect(classifyPickedFile('Ledger.CSV', 'TEXT/CSV; charset=windows-1252')).toBe('spreadsheet')
    expect(classifyPickedFile('BOOK.XLSX', CLASSIFY_XLSX.toUpperCase())).toBe('spreadsheet')
    // Extension absent, so the parameterised declared type is the only thing to read.
    expect(classifyPickedFile('noextension', 'text/csv; charset=utf-8')).toBe('spreadsheet')
    // Was Image/JPEG until EXTR-15-03 dropped it; the case-and-charset claim needs a type that
    // is still accepted, and PN-6 owns the refusal of the ones that are not.
    expect(classifyPickedFile('noextension', `${CLASSIFY_DOCX.toUpperCase()}; charset=binary`)).toBe('document')
  })

  // CLASSIFY-4 — null is a refusal, never a fallback kind. The positive control runs FIRST
  // and is not optional: the Stage-2.5 stub returns null for everything, so the three null
  // assertions below would pass vacuously against it without a needle that must be found.
  it('CLASSIFY-4: an unlisted type is null, not a default', () => {
    expect(classifyPickedFile('ledger.csv', 'text/csv'), 'control needle: a listed row must NOT be null, or the nulls below prove nothing').not.toBeNull()
    expect(classifyPickedFile('scan.pdf', 'application/pdf'), 'control needle: a listed document row must NOT be null').not.toBeNull()

    expect(classifyPickedFile('archive.zip', 'application/zip')).toBeNull()
    expect(classifyPickedFile('x', '')).toBeNull()
    expect(classifyPickedFile('notes.txt', 'text/plain')).toBeNull()
    // '.csv.bak' is not a csv: hasImportableExtension's last-segment rule (FLOW-05) must
    // survive the replacement, not be loosened into a substring match.
    expect(classifyPickedFile('ledger.csv.bak', 'application/octet-stream')).toBeNull()
  })
})

// ============================================================================
// PN-6 (EXTR-15-03, task-854, Mode A) — the client gate narrows to PDF + DOCX.
// ============================================================================
// `accept` gates the OS picker only: CreateUpload hands a DROPPED file straight to
// addPickedFiles unfiltered, so classifyPickedFile is the real client refusal. Narrowing the
// attribute without narrowing this function narrows nothing.

describe('classifyPickedFile after the narrowing (PN-6, EXTR-15-03)', () => {
  it('PN-6: the four dropped extensions and their three content types are null', () => {
    // Controls first, both kinds. The nulls below all hold for a function that returns null
    // for everything — these are what separate a narrowed gate from a broken one.
    expect(classifyPickedFile('scan.pdf', 'application/pdf'), 'control: .pdf must still resolve').toBe('document')
    expect(classifyPickedFile('letter.docx', CLASSIFY_DOCX), 'control: .docx must still resolve').toBe('document')
    expect(classifyPickedFile('ledger.csv', 'text/csv'), 'control: .csv must still resolve').toBe('spreadsheet')
    expect(classifyPickedFile('book.xlsx', CLASSIFY_XLSX), 'control: .xlsx must still resolve').toBe('spreadsheet')

    // Population, derived: four extensions and the three distinct types behind them.
    expect(CLASSIFY_NARROWED_OUT.length, 'the narrowed-out table must not be empty').toBe(4)
    const droppedTypes = [...new Set(CLASSIFY_NARROWED_OUT.map(([, type]) => type))]
    expect(droppedTypes.length, 'the four dropped extensions carry three distinct content types').toBe(3)

    // By extension. Each declared with its own former content type, so a gate that dropped the
    // extension but kept the type still reds here.
    for (const [name, type] of CLASSIFY_NARROWED_OUT) {
      expect(classifyPickedFile(name, type), `${name} declared ${type} must be refused`).toBeNull()
      expect(classifyPickedFile(name, ''), `${name} with no declared type must be refused`).toBeNull()
    }

    // By declared type alone — no extension to read, so the type is the only signal.
    for (const type of droppedTypes) {
      expect(classifyPickedFile('noextension', type), `a file declared ${type} must be refused`).toBeNull()
      expect(classifyPickedFile('noextension', `${type}; charset=binary`), `${type} with a charset parameter must be refused`).toBeNull()
    }

    // Case is not an escape hatch: CLASSIFY-3's rule cuts both ways.
    expect(classifyPickedFile('SCAN.PNG', 'IMAGE/PNG')).toBeNull()
  })

  it('PN-6: the accepted table is exactly .csv, .xlsx, .pdf, .docx', () => {
    // The narrowed set restated as an equality over what RESOLVES, so it cannot pass over a
    // gate that refuses everything.
    const probes: Array<[string, string]> = [
      ['ledger.csv', 'text/csv'],
      ['book.xlsx', CLASSIFY_XLSX],
      ['scan.pdf', 'application/pdf'],
      ['letter.docx', CLASSIFY_DOCX],
      ...CLASSIFY_NARROWED_OUT,
      ['archive.zip', 'application/zip'],
    ]
    const resolved = probes.filter(([name, type]) => classifyPickedFile(name, type) !== null).map(([name]) => name)
    expect(resolved.sort()).toEqual(['book.xlsx', 'ledger.csv', 'letter.docx', 'scan.pdf'])
  })
})

// ============================================================================
// STEPS-D1..D9 (EXTR-09-06, task-773, Mode A) — RED specs for the three-strip step model.
// ============================================================================
// Authored RED, every one on an assertion: WIZARD_STEPS was the one-item typed strip,
// ENTER_STEPS an empty Stage-2.5 stub, 'documents' not yet a CreateStep member
// (DOCUMENTS_STEP casts it, as this file cast 'review' before INVCR-01-04), and
// wizardHeader accepted the run kind but ignored it.
//
// The strips are SPELLED here, never read back off the implementation: a spec that
// recomputed its expectation through the code under test would agree with any value the
// code happened to hold.
const ENTER_STRIP: [string, string][] = [['1', 'Enter']]
const IMPORT_STRIP: [string, string][] = [
  ['1', 'Import'],
  ['2', 'Map'],
  ['3', 'Review'],
]
const DOC_STRIP: [string, string][] = [
  ['1', 'Import'],
  ['2', 'Review'],
]

const RUN_KINDS: Array<WizardPath | null> = ['typed', 'import', 'document', null]
const ALL_CREATE_STEPS: CreateStep[] = ['upload', 'mapping', 'form', 'review', DOCUMENTS_STEP]

type HeaderRow = [CreateStep, WizardPath | null, [string, string][], number]

describe('the three-strip step model (STEPS-D1..D9, EXTR-09-06)', () => {
  // AC-1. Falsification: a strip that keeps 'Map' (documents are never mapped), or one
  // that leaves the typed 'Enter' entry sitting on the document path.
  it('STEPS-D1: WIZARD_STEPS is the document strip', () => {
    expect(WIZARD_STEPS).toEqual(DOC_STRIP)
    expect(WIZARD_STEPS.length).toBe(2)
  })

  // AC-1. INVCR-01-03's one-screen decision must survive the move, not be regressed by it.
  it('STEPS-D2: ENTER_STEPS keeps the typed path at one item', () => {
    expect(ENTER_STEPS).toEqual(ENTER_STRIP)
    expect(ENTER_STEPS.length).toBe(1)
  })

  // AC-3. Runtime key set, five members, and no retired step creeping back in.
  it('STEPS-D3: STAGE_OF is total over five members and carries no retired step', () => {
    expect(Object.keys(STAGE_OF).sort()).toEqual(['documents', 'form', 'mapping', 'review', 'upload'])
    const retired = ['report', 'parsing', 'validating', 'results']
    expect(retired.length, 'vacuity floor').toBe(4)
    retired.forEach((dead) => {
      expect(Object.keys(STAGE_OF), dead).not.toContain(dead)
    })
  })

  // AC-3's compiler half. Totality is a TYPE claim, so it is pinned at the type layer,
  // in both directions:
  //   - the witness assignment stops compiling if STAGE_OF is widened to Partial<...>,
  //     i.e. if a member is allowed to have no stage;
  //   - the directive goes unused (TS2578) if STAGE_OF is widened to Record<string,
  //     number>, i.e. if the key domain stops being CreateStep at all.
  // Either widening fails `pnpm -r typecheck` on THIS block, which is what makes
  // "a member added without a stage stops importFlow.ts compiling" enforced rather than
  // merely true today.
  it('STEPS-D3b: STAGE_OF totality is compiler-enforced, not merely satisfied today', () => {
    const witness: Record<CreateStep, number> = STAGE_OF
    expect(Object.keys(witness)).toEqual(Object.keys(STAGE_OF))
    // @ts-expect-error — a key outside CreateStep is not indexable on Record<CreateStep, number>
    expect(STAGE_OF['reconcile']).toBeUndefined()
  })

  // AC-4. Literal expectations for every REACHABLE (step, run kind) pair. The four
  // unreachable pairs — upload/mapping/review on a 'typed' run, mapping on a 'document'
  // run — are deliberately left to STEPS-D4b's totality check rather than pinned to a
  // guess Stage 3 has no reason to honour.
  it('STEPS-D4: the truth table over step × run kind is exact wherever the pair is reachable', () => {
    const table: HeaderRow[] = [
      ['form', 'typed', ENTER_STRIP, 0],
      ['form', 'import', ENTER_STRIP, 0],
      ['form', 'document', ENTER_STRIP, 0],
      ['form', null, ENTER_STRIP, 0],
      ['upload', 'import', IMPORT_STRIP, 0],
      ['upload', null, IMPORT_STRIP, 0],
      ['upload', 'document', DOC_STRIP, 0],
      ['mapping', 'import', IMPORT_STRIP, 1],
      ['mapping', null, IMPORT_STRIP, 1],
      ['review', 'import', IMPORT_STRIP, 2],
      ['review', null, IMPORT_STRIP, 2],
      ['review', 'document', DOC_STRIP, 1],
      [DOCUMENTS_STEP, 'typed', DOC_STRIP, 0],
      [DOCUMENTS_STEP, 'import', DOC_STRIP, 0],
      [DOCUMENTS_STEP, 'document', DOC_STRIP, 0],
      [DOCUMENTS_STEP, null, DOC_STRIP, 0],
    ]
    expect(table.length, 'vacuity floor: the table must not be empty').toBe(16)
    table.forEach(([step, kind, steps, stageIndex]) => {
      expect(wizardHeader(step, kind), `${step} × ${String(kind)}`).toEqual({ steps, stageIndex })
    })
  })

  // AC-4's totality half, over ALL 20 pairs including the unreachable ones: a strip is
  // always non-empty and the index always lands inside it — never undefined, never NaN.
  it('STEPS-D4b: every step × run kind resolves to a real index inside a real strip', () => {
    let checked = 0
    ALL_CREATE_STEPS.forEach((step) => {
      RUN_KINDS.forEach((kind) => {
        const label = `${step} × ${String(kind)}`
        const { steps, stageIndex } = wizardHeader(step, kind)
        expect(steps.length, label).toBeGreaterThan(0)
        expect(Number.isInteger(stageIndex), label).toBe(true)
        expect(stageIndex, label).toBeGreaterThanOrEqual(0)
        expect(stageIndex, label).toBeLessThan(steps.length)
        checked += 1
      })
    })
    expect(checked, 'vacuity floor: 5 steps × 4 run kinds').toBe(20)
  })

  // AC-4, stated as the reason the second argument exists at all: 'review' is shared, so
  // the two answers must genuinely differ.
  it('STEPS-D5: review resolves per path — the step alone cannot pick a strip', () => {
    expect(wizardHeader('review', 'document')).toEqual({ steps: DOC_STRIP, stageIndex: 1 })
    expect(wizardHeader('review', 'import')).toEqual({ steps: IMPORT_STRIP, stageIndex: 2 })
    expect(wizardHeader('review', 'document'), 'the run-kind axis must change the answer').not.toEqual(wizardHeader('review', 'import'))
  })

  // AC-3. Ceilings derived through wizardHeader itself, so this survives an edit that
  // changes a strip literal and its snapshot spec together: no strip may carry an entry
  // past the highest index anything can actually stand on.
  it('STEPS-D7: no strip carries an entry past its own reachable ceiling', () => {
    const reachable = new Map<[string, string][], number[]>()
    ALL_CREATE_STEPS.forEach((step) => {
      RUN_KINDS.forEach((kind) => {
        const { steps, stageIndex } = wizardHeader(step, kind)
        reachable.set(steps, [...(reachable.get(steps) ?? []), stageIndex])
      })
    })
    const strips: Array<[string, [string, string][]]> = [
      ['ENTER_STEPS', ENTER_STEPS],
      ['IMPORT_STEPS', IMPORT_STEPS],
      ['WIZARD_STEPS', WIZARD_STEPS],
    ]
    strips.forEach(([name, strip]) => {
      const indices = reachable.get(strip) ?? []
      // Floor: a strip nothing routes to would satisfy any ceiling vacuously.
      expect(indices.length, `${name} must be reachable from some step × run kind`).toBeGreaterThan(0)
      expect(strip.length, `${name} ceiling`).toBe(Math.max(...indices) + 1)
    })
  })

  // D-08: "document" stops meaning "manually typed" inside this module. Source scan
  // because the constant is module-private and has no runtime surface.
  it('STEPS-D9: DOCUMENT_ONLY_STEPS is renamed TYPED_ONLY_STEPS', () => {
    const srcRoot = fileURLToPath(new URL('..', import.meta.url))
    const isSpec = (relPath: string) => /\.test\.tsx?$/.test(relPath)
    // Needles built from parts so this file's own text is not a hit; spec files are
    // excluded outright, they are not the owner either way.
    const gone = 'DOCUMENT_ONLY' + '_STEPS'
    const arrived = 'TYPED_ONLY' + '_STEPS'

    // Population floor + control needle: the walker must reach the real subtree and find
    // something known, or the absence assertion below proves nothing.
    expect(scanForIdentifier(srcRoot, 'export').length, 'population floor').toBeGreaterThan(50)
    expect(scanForIdentifier(srcRoot, 'wizardHeader').filter((p) => !isSpec(p)).length, 'control needle').toBeGreaterThan(0)

    expect(scanForIdentifier(srcRoot, gone).filter((p) => !isSpec(p))).toEqual([])
    expect(scanForIdentifier(srcRoot, arrived).filter((p) => !isSpec(p))).toEqual([path.join('lib', 'importFlow.ts')])
  })
})
