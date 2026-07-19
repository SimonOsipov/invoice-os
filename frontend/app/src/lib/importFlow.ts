// Wizard import step — pure helpers backing CreateUpload/CreateMapping/CreateFlow's
// header and the two-panel Read-columns/Import gates (M4-08-04, task-173). Every
// derivation the UI reads lives here so it is node-testable without jsdom (plan §C).
// Pinned by importFlow.test.ts (FLOW-01..14). Plan §C/§E are authoritative.
//
// 'report' added to CreateStep here, not in M4-08-05 as story §6 originally assigned
// (plan B1/DRIFT-1): wizardHeader's report->2 branch does not compile against the
// pre-existing CreateStep union, and CreateFlow.tsx:16-24's STAGE_OF is the ONLY total
// Record<CreateStep, number> in the frontend, so the union addition cascades there too.

import { WIZARD_STEPS } from '../data'
import { canSubmitMapping } from './mapping'
import type { CreateStep, Mapping } from '../types'
import type { ImportPreview } from './importApi'

export type WizardPath = 'document' | 'import'

export const IMPORT_STEPS: [string, string][] = [
  ['1', 'Import'],
  ['2', 'Map'],
  ['3', 'Report'],
]

// MOVED here from CreateFlow.tsx:16-24 (one table, one owner — two copies would
// drift). CreateFlow deletes its local STAGE_OF + wizardStage and calls wizardHeader
// instead (M4-08-04 step 4). `report: 2` is unreachable via the document path —
// wizardHeader routes 'report' to the import path unconditionally.
export const STAGE_OF: Record<CreateStep, number> = {
  upload: 0,
  parsing: 0,
  mapping: 1,
  form: 2,
  validating: 3,
  results: 4,
  report: 2, // unreachable via 'document' — see wizardHeader
}

export const IMPORT_STAGE_OF: Partial<Record<CreateStep, number>> = { upload: 0, mapping: 1, report: 2 }

// The header-path resolver ([wizard-steps-split], debate finding J1). Exact rule:
// path = 'document' iff createStep in {parsing, form, validating, results} OR
// (createStep === 'upload' AND uploadFile !== null AND importFile === null);
// otherwise 'import'. Total over CreateStep via `?? 0` — a step added to the union
// without an IMPORT_STAGE_OF entry falls to the import path at index 0 rather than
// ever returning undefined/NaN (FLOW-14).
const DOCUMENT_ONLY_STEPS: readonly CreateStep[] = ['parsing', 'form', 'validating', 'results']

export function wizardHeader(
  createStep: CreateStep,
  uploadFile: string | null,
  importFile: File | null,
): { steps: [string, string][]; stageIndex: number } {
  // 'upload' is the ONE step both paths share, so it is the only one that needs the
  // two file slots to disambiguate: a chosen import file always wins over a stale
  // sample selection left behind by an earlier pass through the picker (FLOW-13).
  const isDocument =
    DOCUMENT_ONLY_STEPS.includes(createStep) ||
    (createStep === 'upload' && uploadFile !== null && importFile === null)

  return isDocument
    ? { steps: WIZARD_STEPS, stageIndex: STAGE_OF[createStep] ?? 0 }
    : { steps: IMPORT_STEPS, stageIndex: IMPORT_STAGE_OF[createStep] ?? 0 }
}

// Last-segment match only: 'a.csv'/'a.xlsx' (any case) match; 'a.csv.bak' does not.
export function hasImportableExtension(name: string): boolean {
  const n = name.toLowerCase()
  return n.endsWith('.csv') || n.endsWith('.xlsx')
}

// = !!entityId && file !== null && hasImportableExtension(file.name). One predicate is
// the sole gate — the extension rule is not also duplicated in the setter.
export function canReadColumns(entityId: string | null, file: File | null): boolean {
  return !!entityId && file !== null && hasImportableExtension(file.name)
}

// = preview !== null && canSubmitMapping(mapping). Delegates to M4-08-03's shipped
// gate (lib/mapping.ts) rather than re-deriving !!mapping.invoice_number (FLOW-04).
export function canStartImport(preview: ImportPreview | null, mapping: Mapping | null): boolean {
  return preview !== null && canSubmitMapping(mapping)
}

// = header !== '' — EXACTLY, not header.trim() !== ''. '' is the reserved unplaced
// sentinel toImportMapping strips; a whitespace-only header is an ordinary column
// resolveMapping matches exactly server-side (Core AC3), so it must stay mappable.
export function isMappableColumn(header: string): boolean {
  return header !== ''
}

// Spreadsheet-style column letters: A..Z, AA, AB, ... (NOT String.fromCharCode(65+ci),
// which breaks past column 26). Bijective base-26: the `- 1` after each division is what
// makes 'Z' -> 'AA' rather than the 'A0' a plain base-26 conversion would produce.
export function columnLetter(ci: number): string {
  let n = ci
  let out = ''
  while (n >= 0) {
    out = String.fromCharCode(65 + (n % 26)) + out
    n = Math.floor(n / 26) - 1
  }
  return out
}

export interface PreviewColumn {
  header: string
  letter: string
  mappable: boolean
  samples: string[]
}

// The Map step's whole column derivation, so the component is a dumb renderer with one
// call site instead of five inline dereferences. For each ci: header =
// preview.columns[ci], letter = columnLetter(ci), mappable = isMappableColumn(header),
// samples = preview.sample_rows.slice(0, sampleCount).map(row => row[ci] ?? '') — rows
// are ragged/unpadded ([preview-samples], PRV-09), so a short row reads as '', never
// undefined.
export function previewColumns(preview: ImportPreview, sampleCount: number): PreviewColumn[] {
  const rows = preview.sample_rows.slice(0, sampleCount)
  return preview.columns.map((header, ci) => ({
    header,
    letter: columnLetter(ci),
    mappable: isMappableColumn(header),
    // Column-major: samples[r] is row r of THIS column. `?? ''` is the ragged-row guard.
    samples: rows.map((row) => row[ci] ?? ''),
  }))
}
