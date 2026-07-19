// Wizard import step — pure helpers backing CreateUpload/CreateMapping/CreateFlow's
// header and the two-panel Read-columns/Import gates (M4-08-04, task-173). Every
// derivation the UI reads lives here so it is node-testable without jsdom (plan §C).
// Pinned by importFlow.test.ts (FLOW-01..14). Plan §C/§E are authoritative.
//
// Stage 2.5 (Mode A): every FUNCTION below is a signature-only skeleton whose body
// throws 'not implemented' until the executor fills it in (same throw-skeleton
// pattern as importApi.ts@d2ed19e / mapping.ts@8c77842). STAGE_OF/IMPORT_STAGE_OF are
// pure DATA, not logic under test — no FLOW spec targets them directly, only through
// wizardHeader — so they carry their real, plan-pinned values now (plan §C: "MOVED
// here from CreateFlow.tsx:16-24 ... adding report: 2").
//
// 'report' added to CreateStep here, not in M4-08-05 as story §6 originally assigned
// (plan B1/DRIFT-1): wizardHeader's report->2 branch does not compile against the
// pre-existing CreateStep union, and CreateFlow.tsx:16-24's STAGE_OF is the ONLY total
// Record<CreateStep, number> in the frontend, so the union addition cascades there too.

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
  review: 2,
  validating: 3,
  results: 4,
  report: 2, // unreachable via 'document' — see wizardHeader
}

export const IMPORT_STAGE_OF: Partial<Record<CreateStep, number>> = { upload: 0, mapping: 1, report: 2 }

// The header-path resolver ([wizard-steps-split], debate finding J1). Exact rule:
// path = 'document' iff createStep in {parsing, form, validating, results} OR
// (createStep === 'upload' AND uploadFile !== null AND importFile === null);
// otherwise 'import'. Total over CreateStep via `?? 0` — 'review' (dead from this
// commit, deleted by -06) and any future addition fall to the import path, index 0,
// rather than ever returning undefined/NaN (FLOW-14).
export function wizardHeader(
  _createStep: CreateStep,
  _uploadFile: string | null,
  _importFile: File | null,
): { steps: [string, string][]; stageIndex: number } {
  throw new Error('not implemented')
}

// Last-segment match only: 'a.csv'/'a.xlsx' (any case) match; 'a.csv.bak' does not.
export function hasImportableExtension(_name: string): boolean {
  throw new Error('not implemented')
}

// = !!entityId && file !== null && hasImportableExtension(file.name). One predicate is
// the sole gate — the extension rule is not also duplicated in the setter.
export function canReadColumns(_entityId: string | null, _file: File | null): boolean {
  throw new Error('not implemented')
}

// = preview !== null && canSubmitMapping(mapping). Delegates to M4-08-03's shipped
// gate (lib/mapping.ts) rather than re-deriving !!mapping.invoice_number (FLOW-04).
export function canStartImport(_preview: ImportPreview | null, _mapping: Mapping | null): boolean {
  throw new Error('not implemented')
}

// = header !== '' — EXACTLY, not header.trim() !== ''. '' is the reserved unplaced
// sentinel toImportMapping strips; a whitespace-only header is an ordinary column
// resolveMapping matches exactly server-side (Core AC3), so it must stay mappable.
export function isMappableColumn(_header: string): boolean {
  throw new Error('not implemented')
}

// Spreadsheet-style column letters: A..Z, AA, AB, ... (NOT String.fromCharCode(65+ci),
// which breaks past column 26).
export function columnLetter(_ci: number): string {
  throw new Error('not implemented')
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
export function previewColumns(_preview: ImportPreview, _sampleCount: number): PreviewColumn[] {
  throw new Error('not implemented')
}
