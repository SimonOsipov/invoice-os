// Wizard import step — pure helpers backing CreateUpload/CreateMapping/CreateFlow's
// header and the two-panel Read-columns/Import gates (M4-08-04, task-173). Every
// derivation the UI reads lives here so it is node-testable without jsdom (plan §C).
// Pinned by importFlow.test.ts (FLOW-01..14). Plan §C/§E are authoritative.
//
// 'review' (added to CreateStep here by M4-08-04 under its former name, plan B1/DRIFT-1;
// renamed by INVCR-01-04) lives here rather than in M4-08-05 as story §6 originally
// assigned: wizardHeader's index-2 branch does not compile against the pre-existing
// CreateStep union, and STAGE_OF below is the ONLY total Record<CreateStep, number> in
// the frontend, so the union addition cascades to it too.

import { WIZARD_STEPS } from '../data'
import { canSubmitMapping } from './mapping'
import type { CreateStep, Mapping } from '../types'
import type { ImportPreview } from './importApi'
import type { AsyncStatus } from '@invoice-os/api-client'
import type { Entity } from './portfolio'

// Repurposed by EXTR-09-06 (D-08): 'document' used to mean the manually TYPED single
// invoice here, which stops being readable in the story that makes source documents real.
export type WizardPath = 'typed' | 'import' | 'document'

export const IMPORT_STEPS: [string, string][] = [
  ['1', 'Import'],
  ['2', 'Map'],
  ['3', 'Review'],
]

// MOVED here from CreateFlow.tsx:16-24 (one table, one owner — two copies would
// drift). CreateFlow deletes its local STAGE_OF + wizardStage and calls wizardHeader
// instead (M4-08-04 step 4).
//
// Do NOT "dedupe" this against IMPORT_STAGE_OF. `form` is the ONLY entry wizardHeader
// ever reads here — DOCUMENT_ONLY_STEPS is ['form'], so every other step routes to
// IMPORT_STAGE_OF instead. upload/mapping/review exist solely to keep the type a TOTAL
// Record<CreateStep, number>: that totality is the compiler's exhaustiveness anchor (a
// member added to CreateStep without a stage stops this file compiling) and it is the
// ground truth two shipped deletion guards in importFlow.test.ts read via
// Object.keys(STAGE_OF) to prove a deleted step never creeps back. Their values mirror
// IMPORT_STAGE_OF's so the two tables can never disagree.
export const STAGE_OF: Record<CreateStep, number> = {
  upload: 0,
  mapping: 1,
  form: 0, // WIZARD_STEPS[0] = 'Enter' — the one entry that is actually read
  review: 2, // mirrors IMPORT_STAGE_OF; present so the Record stays total
}

export const IMPORT_STAGE_OF: Partial<Record<CreateStep, number>> = { upload: 0, mapping: 1, review: 2 }

// The header-path resolver ([wizard-steps-split], debate finding J1). Exact rule:
// path = 'document' iff createStep === 'form'; otherwise 'import'. Total over CreateStep
// via `?? 0` — a step added to the union without an IMPORT_STAGE_OF entry falls to the
// import path at index 0 rather than ever returning undefined/NaN (FLOW-14).
//
// The two strips it resolves between ([three-stages], INVCR-01-04): typing an invoice by
// hand is `Enter · Review` and lights 'Enter'; dropping a file is `Import · Map · Review`.
// 'Review' is the last stage on both paths — the real invoice detail view for a single
// typed invoice, the import report for a batch.
const DOCUMENT_ONLY_STEPS: readonly CreateStep[] = ['form']

// STAGE-2.5 STUB (EXTR-09-06, task-773): the run kind is ACCEPTED AND IGNORED. 'review'
// is shared by all three paths, so the step alone cannot pick a strip — STEPS-D4/STEPS-D5
// fail on that until Stage 3 reads this argument.
export function wizardHeader(createStep: CreateStep, _runKind?: WizardPath | null): { steps: [string, string][]; stageIndex: number } {
  return DOCUMENT_ONLY_STEPS.includes(createStep)
    ? { steps: WIZARD_STEPS, stageIndex: STAGE_OF[createStep] ?? 0 }
    : { steps: IMPORT_STEPS, stageIndex: IMPORT_STAGE_OF[createStep] ?? 0 }
}

// The picker's per-file verdict (EXTR-09 §1). 'spreadsheet' keeps the shipped
// preview/mapping flow, 'document' routes to POST /v1/documents, null is refused at
// selection with a named reason.
export type PickedKind = 'spreadsheet' | 'document'

// One row of the accepted-type table. The shape is load-bearing, not taste: CLASSIFY-5
// (internal/extraction/handlers_upload_test.go) reads the literal below out of THIS source
// and compares its document half to classify.go's acceptedDocumentTypes.
export interface AcceptedPickedType {
  ext: string
  kind: PickedKind
  contentTypes: readonly string[]
}

// The one accepted-type table: `accept`, the classifier and the copy all trace here.
//
// contentTypes holds only the types that DECIDE a verdict on the fallback path, so story
// §1's `text/plain` alias for .csv is not listed: an unrecognised extension falls through
// to the declared type, and listing it would make 'notes.txt' declared text/plain a
// spreadsheet (CLASSIFY-4 requires null). A .csv declared text/plain still resolves — by
// its extension, which wins (CLASSIFY-1).
export const ACCEPTED_PICKED_TYPES: readonly AcceptedPickedType[] = [
  { ext: '.csv', kind: 'spreadsheet', contentTypes: ['text/csv'] },
  { ext: '.xlsx', kind: 'spreadsheet', contentTypes: ['application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'] },
  { ext: '.pdf', kind: 'document', contentTypes: ['application/pdf'] },
  { ext: '.png', kind: 'document', contentTypes: ['image/png'] },
  { ext: '.jpg', kind: 'document', contentTypes: ['image/jpeg'] },
  { ext: '.jpeg', kind: 'document', contentTypes: ['image/jpeg'] },
  { ext: '.webp', kind: 'document', contentTypes: ['image/webp'] },
  { ext: '.docx', kind: 'document', contentTypes: ['application/vnd.openxmlformats-officedocument.wordprocessingml.document'] },
]

// The selection gate (EXTR-09 §1). Last-segment extension first, then the declared type
// with its parameters stripped, both case-insensitively — detectFormat's rule
// (internal/importer/handlers.go), mirrored by classifyDocumentType server-side. null is a
// refusal, never a fallback kind.
export function classifyPickedFile(name: string, type: string): PickedKind | null {
  const lower = name.toLowerCase()
  const dot = lower.lastIndexOf('.')
  const byExt = dot === -1 ? undefined : ACCEPTED_PICKED_TYPES.find((row) => row.ext === lower.slice(dot))
  if (byExt) return byExt.kind

  // Drops any "; charset=…" parameter, as mime.ParseMediaType does on the Go side.
  const base = type.split(';')[0].trim().toLowerCase()
  return ACCEPTED_PICKED_TYPES.find((row) => row.contentTypes.includes(base))?.kind ?? null
}

// Thin delegate over the table above, so the spreadsheet path's shipped call sites keep
// their meaning. The empty content type leaves the extension as the only input, which is
// exactly the shipped last-segment rule: 'a.csv'/'a.xlsx' (any case) match, 'a.csv.bak'
// does not (FLOW-05).
export function hasImportableExtension(name: string): boolean {
  return classifyPickedFile(name, '') === 'spreadsheet'
}

// STAGE-2.5 STUB (EXTR-09-05, task-772, test-first). Exported so SIZE-1/SIZE-2 compile;
// canReadColumns below does NOT consult it yet — Stage 3 owns the gate itself. Binary
// MiB, the same number as internal/importer's maxUploadBytes and documents.size_bytes'
// CHECK ceiling; TestMaxUploadBytes_MatchesTheBrowserConstant /
// TestMaxUploadBytes_MatchesTheColumnCheck (internal/importer/handlers_upload_once_test.go)
// are what keep the three equal.
export const MAX_UPLOAD_BYTES = 15 * 1024 * 1024

// = file !== null && hasImportableExtension(file.name). One predicate is the sole gate —
// the extension rule is not also duplicated in the setter.
//
// Deliberately does NOT gate on an entity. POST /api/invoice/v1/imports/preview sends the
// file and nothing else (importApi.previewImport), and the server's PreviewHandler is
// documented "no entity_id, no mapping, no persistence" (internal/importer/handlers.go) —
// reading columns is a pure inspection of the uploaded bytes. The entity requirement lives
// on the COMMIT step instead (startImport()'s !entityId guard, which is what writes
// invoices.entity_id / import_batches.entity_id — both NOT NULL). It used to be asserted
// here too, and because in-house workspaces had a permanently-null entityId (no
// business_entities row at all, before task-304 gave in-house a real one) that
// belt-and-braces copy locked them out of the wizard's front door entirely: the dropzone
// never rendered at all. Preview gate = file only; commit gate = entity.
//
// Still exactly true after INVCR-01-05, and worth restating because that subtask makes it
// LOOK otherwise: the upload screen now shows an entity-less workspace an amber "No linked
// business entity" panel. That panel is INFORMATIONAL — it tells the user early what the
// commit will refuse, and it disables nothing. Do not "make it consistent" by adding an
// entity clause here or to anything upstream of `Read columns`; that is precisely the
// belt-and-braces copy the paragraph above describes, and re-adding it re-closes the front
// door on in-house workspaces.
export function canReadColumns(file: File | null): boolean {
  return file !== null && hasImportableExtension(file.name)
}

// = preview !== null && canSubmitMapping(mapping). Delegates to M4-08-03's shipped
// gate (lib/mapping.ts) rather than re-deriving !!mapping.invoice_number (FLOW-04).
export function canStartImport(preview: ImportPreview | null, mapping: Mapping | null): boolean {
  return preview !== null && canSubmitMapping(mapping)
}

// STUB (task-304, INVCR-01-19, test-first) — importFlow.test.ts's RED specs pin the
// contract before this body exists; throwing here (rather than a wrong-but-plausible
// guess) makes every spec fail on an assertion/thrown-error mismatch, never an
// import/compile error.
//
// Whether CreateUpload's "No linked business entity" amber panel should render.
// Extracted verbatim from CreateUpload.tsx's own three `const`s (unchanged logic, this
// story only MOVED it) so it is node-testable under the no-jsdom constraint — the
// component itself stays unrenderable in this suite, but the derivation it reads is not.
// task-304 AC-6 needs this: the panel is generic over BOTH personas and BOTH reasons a
// workspace can have no resolved entity — an in-house tenant with none yet (the
// bootstrap window, AC-3) and a firm tenant whose active entity has been archived out of
// the roster while others remain are the SAME honest "nothing to file against", so this
// stays one predicate, never two, and never gains a persona check.
//
// Two guards beyond `activeEntity === null` exist for ONE reason: this panel is loud and
// amber, so a single frame of it on a user who DOES have an entity is a visible lie.
//  1. entityAnswerSettled — the entities fetch must have definitively answered ('ready'
//     or 'empty'); 'idle'/'loading'/'error' have not, and 'idle' also covers the
//     no-gateway build.
//  2. !rosterCatchingUp — `clients` is derived from `entities` by a useEffect one render
//     late; on the render where the fetch resolves, entitiesState is already settled
//     while `clients` is still [], which would otherwise flash the panel for one frame.
export function computeNoEntity(
  activeEntity: Entity | null,
  entitiesState: AsyncStatus,
  entitiesCount: number,
  clientsCount: number,
): boolean {
  const entityAnswerSettled = entitiesState === 'ready' || entitiesState === 'empty'
  const rosterCatchingUp = entitiesCount > 0 && clientsCount === 0
  return activeEntity === null && entityAnswerSettled && !rosterCatchingUp
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
