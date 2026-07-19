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
//   FLOW-01  canReadColumns: gates on BOTH entity and file, neither alone           (AC3)
//   FLOW-02  canReadColumns rejects a non-csv/xlsx extension (e.g. .pdf)          (AC1,3)
//   FLOW-03  canStartImport: needs preview AND invoice_number placed, not all 11    (AC3)
//   FLOW-04  canStartImport delegates to canSubmitMapping, never re-derives         (AC3)
//   FLOW-05  hasImportableExtension: case-insensitive, last-segment match only      (AC1)
//   FLOW-06  previewColumns: column-major samples, not row-major                    (AC2)
//   FLOW-07  previewColumns: ragged sample_rows read as '' not undefined            (AC2)
//   FLOW-08  previewColumns: duplicate headers kept as distinct entries             (AC2)
//   FLOW-09  isMappableColumn: '' blocked, whitespace-only header stays mappable    (AC2)
//   FLOW-10  columnLetter: A..Z, AA, AB, ... past column 26                         (AC2)
//   FLOW-11  wizardHeader document set: parsing/form/validating/results             (AC6)
//   FLOW-12  wizardHeader import set: mapping/report/upload(with importFile)        (AC2)
//   FLOW-13  wizardHeader 'upload' disambiguation, importFile wins over stale       (AC6)
//            uploadFile sample selection
//   FLOW-14  wizardHeader totality: every CreateStep literal, never undefined/NaN   (AC6)
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
  // FLOW-01 — falsification: an impl gating on the file alone (the exact [entity-picker]
  // failure: importing under a guessed entity) or on the entity alone.
  it('requires BOTH a real entity and a real file — neither alone is enough', () => {
    expect(canReadColumns(null, null)).toBe(false)
    expect(canReadColumns('e1', null)).toBe(false)
    expect(canReadColumns(null, csvFile)).toBe(false)
    expect(canReadColumns('e1', csvFile)).toBe(true)
  })

  // FLOW-02 — falsification: an impl omitting the extension check, which lets a PDF
  // reach previewImport and 400 server-side.
  it('rejects a non-csv/xlsx file even with a real entity chosen', () => {
    expect(canReadColumns('e1', pdfFile)).toBe(false)
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

describe('wizardHeader (FLOW-11..14)', () => {
  const importFile = new File([], 'data.csv')

  // FLOW-11 — document set. Falsification: an impl routing every step to the 3-step
  // import list, silently rewriting the untouched single-document wizard header.
  it('routes the single-document steps to WIZARD_STEPS at their existing stage index', () => {
    expect(wizardHeader('parsing', null, null)).toEqual({ steps: WIZARD_STEPS, stageIndex: 0 })
    expect(wizardHeader('form', null, null)).toEqual({ steps: WIZARD_STEPS, stageIndex: 2 })
    expect(wizardHeader('validating', null, null)).toEqual({ steps: WIZARD_STEPS, stageIndex: 3 })
    expect(wizardHeader('results', null, null)).toEqual({ steps: WIZARD_STEPS, stageIndex: 4 })
    expect(WIZARD_STEPS.length).toBe(5)
  })

  // FLOW-12 — import set. Falsification: an impl keyed on the old flat STAGE_OF — it
  // maps mapping->1 by coincidence but returns the 5-step list and undefined for 'report'.
  it('routes mapping/report/upload(with an import file) to the 3-step import list', () => {
    expect(wizardHeader('mapping', null, importFile)).toEqual({ steps: IMPORT_STEPS, stageIndex: 1 })
    expect(wizardHeader('report', null, importFile)).toEqual({ steps: IMPORT_STEPS, stageIndex: 2 })
    expect(wizardHeader('upload', null, importFile)).toEqual({ steps: IMPORT_STEPS, stageIndex: 0 })
    expect(IMPORT_STEPS.length).toBe(3)
  })

  // FLOW-13 — 'upload' disambiguation. Falsification: an impl defaulting bare 'upload'
  // to the document path (case 1); an impl letting a stale uploadFile sample selection
  // override a chosen import file (case 3).
  it('disambiguates the shared upload step; a chosen import file always wins', () => {
    expect(wizardHeader('upload', null, null)).toEqual({ steps: IMPORT_STEPS, stageIndex: 0 })
    expect(wizardHeader('upload', 'pdf', null)).toEqual({ steps: WIZARD_STEPS, stageIndex: 0 })
    expect(wizardHeader('upload', 'pdf', importFile)).toEqual({ steps: IMPORT_STEPS, stageIndex: 0 })
    expect(wizardHeader('upload', null, importFile)).toEqual({ steps: IMPORT_STEPS, stageIndex: 0 })
  })

  // FLOW-14 — totality. Falsification: an impl with no `?? 0` fallback — 'review'/
  // 'report' return undefined and the header renders with no active step.
  it('is total over every CreateStep literal — stageIndex is always a valid index', () => {
    const ALL_STEPS: CreateStep[] = ['upload', 'parsing', 'mapping', 'form', 'review', 'validating', 'results', 'report']
    ALL_STEPS.forEach((step) => {
      const { steps, stageIndex } = wizardHeader(step, null, null)
      expect(steps.length).toBeGreaterThanOrEqual(3)
      expect(Number.isInteger(stageIndex)).toBe(true)
      expect(stageIndex).toBeGreaterThanOrEqual(0)
      expect(stageIndex).toBeLessThan(steps.length)
    })
  })
})
