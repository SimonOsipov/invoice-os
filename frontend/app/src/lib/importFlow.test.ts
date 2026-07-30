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
//   FLOW-11  wizardHeader document set: form/validating/results                      (AC6)
//   FLOW-12  wizardHeader import set: upload/mapping/report — one path per CreateStep (AC2,6)
//   FLOW-14  wizardHeader totality: every CreateStep literal, never undefined/NaN     (AC6)
//
// Every spec below currently fails because wizardHeader/hasImportableExtension/
// canReadColumns/canStartImport/isMappableColumn/columnLetter/previewColumns's stub
// bodies throw `new Error('not implemented')` before ever returning anything — that IS
// the correct RED reason (assertion / not-implemented), not an import/compile error.
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
  it('routes the single-document steps to WIZARD_STEPS at their existing stage index', () => {
    expect(wizardHeader('form')).toEqual({ steps: WIZARD_STEPS, stageIndex: 2 })
    expect(wizardHeader('validating')).toEqual({ steps: WIZARD_STEPS, stageIndex: 3 })
    expect(wizardHeader('results')).toEqual({ steps: WIZARD_STEPS, stageIndex: 4 })
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
    const ALL_STEPS: CreateStep[] = ['upload', 'mapping', 'form', 'validating', 'results', 'report']
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
    expect(Object.keys(STAGE_OF)).toHaveLength(6)
  })
})

// QA (M4-08-04): adversarial/edge coverage beyond the architect's FLOW-01..12,14
// specs. New describe blocks only — nothing above this point is modified.
describe('wizardHeader — full truth table over every CreateStep (QA)', () => {
  // Literal expected values, not re-derived from STAGE_OF/IMPORT_STAGE_OF. The 1-arg
  // signature makes every step a pure function of createStep alone, so there is no
  // file-state axis left to hold constant across (FLOW-13's old reason is gone with it).
  it('routes document-only steps to WIZARD_STEPS at their fixed index', () => {
    const expected: Array<[CreateStep, number]> = [['form', 2], ['validating', 3], ['results', 4]]
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
})

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
