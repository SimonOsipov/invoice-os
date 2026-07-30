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
//   FLOW-11  wizardHeader document set: form (validating/results left in INVCR-01-03) (AC6)
//   FLOW-12  wizardHeader import set: upload/mapping/report — one path per CreateStep (AC2,6)
//   FLOW-14  wizardHeader totality: every CreateStep literal, never undefined/NaN     (AC6)
//
// Every spec below currently fails because wizardHeader/hasImportableExtension/
// canReadColumns/canStartImport/isMappableColumn/columnLetter/previewColumns's stub
// bodies throw `new Error('not implemented')` before ever returning anything — that IS
// the correct RED reason (assertion / not-implemented), not an import/compile error.
import { readdirSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import { WIZARD_STEPS } from '../data'
import { canSubmitMapping } from './mapping'
import {
  IMPORT_STEPS,
  STAGE_OF,
  canReadColumns,
  canStartImport,
  columnLetter,
  hasImportableExtension,
  isMappableColumn,
  previewColumns,
  wizardHeader,
} from './importFlow'
import type { ImportPreview } from './importApi'
import type { CreateStep, Mapping } from '../types'

function mkPreview(overrides: Partial<ImportPreview> = {}): ImportPreview {
  return {
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
  // Down to ONE document step since INVCR-01-03 deleted the mock validate/approve tail.
  // WIZARD_STEPS itself is deliberately UNCHANGED here (still 5 entries, 'form' still at
  // index 2) — replacing the strip with the two-stage Enter·Review model is INVCR-01-04.
  it('routes the single-document steps to WIZARD_STEPS at their existing stage index', () => {
    expect(wizardHeader('form')).toEqual({ steps: WIZARD_STEPS, stageIndex: 2 })
    expect(WIZARD_STEPS.length).toBe(5)
  })

  // FLOW-12 absorbs FLOW-13's one surviving assertion (bare 'upload' -> IMPORT_STEPS@0)
  // now that there is no second file arg left to disambiguate with.
  it('routes upload/mapping/report to the 3-step import list', () => {
    expect(wizardHeader('upload')).toEqual({ steps: IMPORT_STEPS, stageIndex: 0 })
    expect(wizardHeader('mapping')).toEqual({ steps: IMPORT_STEPS, stageIndex: 1 })
    expect(wizardHeader('report')).toEqual({ steps: IMPORT_STEPS, stageIndex: 2 })
    expect(IMPORT_STEPS.length).toBe(3)
  })

  it('is total over every CreateStep literal — stageIndex is always a valid index', () => {
    const ALL_STEPS: CreateStep[] = ['upload', 'mapping', 'form', 'report']
    ALL_STEPS.forEach((step) => {
      const { steps, stageIndex } = wizardHeader(step)
      expect(steps.length).toBeGreaterThanOrEqual(3)
      expect(Number.isInteger(stageIndex)).toBe(true)
      expect(stageIndex).toBeGreaterThanOrEqual(0)
      expect(stageIndex).toBeLessThan(steps.length)
    })
  })

  // AC-2 regression guard (QA addition, not in the architect's FLOW map): pins the
  // 1-arg SIGNATURE itself, not just return values. Verified against a local stub both
  // directions before landing this: a 2-arg call against a 1-arg fn is a real TS2554
  // the directive suppresses; a bare 1-arg call needs no directive. If wizardHeader
  // ever regresses to 2/3-arg, @ts-expect-error goes unused (TS2578) and typecheck
  // fails on THIS line, not silently elsewhere.
  it('AC-2: wizardHeader is 1-arg only under noUnusedParameters — a stale 2-arg call does not compile', () => {
    // @ts-expect-error — regression guard: the story's original AC#2 wording specified a
    // 2-arg signature, which does not compile once uploadFile/importFile are dropped (D-01a).
    const twoArg = wizardHeader('upload', null)
    expect(twoArg).toEqual({ steps: IMPORT_STEPS, stageIndex: 0 })
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
    // 6 -> 4 in INVCR-01-03, which deleted two more members; STEP-1 below owns that count.
    expect(Object.keys(STAGE_OF)).toHaveLength(4)
  })

  // STEP-1 (INVCR-01-03, task-279, Mode A — RED-first): 'validating'/'results' leave
  // CreateStep here -- the mock validate/approve tail's own two steps. STAGE_OF stays a
  // total Record over the shrunk union (4 keys: upload/mapping/form/report) -- see
  // task-279 plan §5.6/§10. Genuinely RED at runtime today: STAGE_OF still carries both
  // keys (6 total), so this discriminates against an INCOMPLETE deletion, not a wrong
  // algorithm.
  it("STEP-1: 'validating'/'results' leave the runtime step table, which stays a total 4-key Record", () => {
    expect(Object.keys(STAGE_OF)).not.toContain('validating')
    expect(Object.keys(STAGE_OF)).not.toContain('results')
    expect(Object.keys(STAGE_OF)).toHaveLength(4)
  })
})

// QA (M4-08-04): adversarial/edge coverage beyond the architect's FLOW-01..12,14
// specs. New describe blocks only — nothing above this point is modified.
describe('wizardHeader — full truth table over every CreateStep (QA)', () => {
  // Literal expected values, not re-derived from STAGE_OF/IMPORT_STAGE_OF. The 1-arg
  // signature makes every step a pure function of createStep alone, so there is no
  // file-state axis left to hold constant across (FLOW-13's old reason is gone with it).
  it('routes document-only steps to WIZARD_STEPS at their fixed index', () => {
    const expected: Array<[CreateStep, number]> = [['form', 2]]
    expected.forEach(([step, idx]) => {
      expect(wizardHeader(step)).toEqual({ steps: WIZARD_STEPS, stageIndex: idx })
    })
  })

  // Gains ['upload', 0] here (absorbed from deleted QA-3/FLOW-13): import-side table
  // is now total over every IMPORT_STEPS-routed CreateStep, not just mapping/report.
  it('routes upload/mapping/report to IMPORT_STEPS at their fixed index', () => {
    const expected: Array<[CreateStep, number]> = [['upload', 0], ['mapping', 1], ['report', 2]]
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
  it('QA-WH-KEYS: the document/import partition covers exactly the members STAGE_OF is compiler-required to have — no member left un-partitioned', () => {
    const documentSet: CreateStep[] = ['form']
    const importSet: CreateStep[] = ['upload', 'mapping', 'report']
    expect([...documentSet, ...importSet].slice().sort()).toEqual(Object.keys(STAGE_OF).sort())
    // Positive companion: the two sets are actually disjoint, so the equality above
    // isn't hiding a step counted (or miscounted) on both sides.
    expect(documentSet.filter((s) => (importSet as string[]).includes(s))).toEqual([])
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
  // (a) data.tsx's own WIZARD_STEPS strip label 'Approve' -- untouched by this subtask
  // per [stage-strip-stays-transient] -- and (b) the unrelated Workflows
  // approval-policy feature (WorkflowInspector.tsx/WorkflowBuilder.tsx/
  // WorkflowParts.tsx), so a scan for it would fail on legitimate, in-scope code and
  // teach nothing about the deleted mock's `approve()` handler specifically.
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
