// Wizard multi-file import — grouping half (BULK-01, task-309/BULK-01-04). Once several
// files are picked (BULK-01-03), files sharing an identical column layout are mapped
// ONCE instead of once-per-file, the mapping screen states which files that mapping
// covers, and the operator can split any file out to map it separately. Sharing is
// never silent (decision [shared-mapping-shown], founder call 2026-07-31). Node-testable
// under this project's jsdom-less vitest config (vitest.config.ts:5), same discipline as
// lib/importFlow.ts and lib/importRun.ts.
//
// MappingGroup is pure client-side React state (App.tsx's groups/groupIndex), never a
// persisted entity — no new table, no new endpoint, no group id ever crosses the wire
// ([run-is-client-state]).
//
// Every export below is a STUB (BULK-01-04, test-first) — mappingGroups.test.ts's RED
// specs (BULK-04-1..12) pin the contract before these bodies exist. Throwing (rather
// than a wrong-but-plausible guess) makes every spec fail on an assertion/thrown-error
// mismatch, never an import/compile error — same precedent as lib/importFlow.ts's
// computeNoEntity STUB (task-304, INVCR-01-19) and lib/importRun.ts's selection-half
// STUBs (BULK-01-03).

import type { ImportPreview } from './importApi'
import type { Mapping } from '../types'

// STUB (BULK-01-04, test-first). The exact, ordered, case-sensitive column list —
// JSON.stringify of the array, no sorting, no case-folding. Two files share a group
// IFF their signatures are equal ([layout-signature-is-ordered]: the mapping screen
// renders one file's column grid, so claiming coverage of a differently-ordered/cased
// file states a share the operator cannot verify by eye).
export function columnSignature(_columns: string[]): string {
  throw new Error('not implemented')
}

export interface MappingGroup {
  id: string
  signature: string
  fileIds: string[]
  preview: ImportPreview
  mapping: Mapping
}

// STUB (BULK-01-04, test-first). Walks `previewed` in pick order and buckets by
// columnSignature, preserving first-appearance order of groups. Each new group's
// mapping is seeded with the shipped initMappingFromHeaders(preview.columns) — never a
// blank mapping.
export function groupByLayout(_previewed: { fileId: string; preview: ImportPreview }[]): MappingGroup[] {
  throw new Error('not implemented')
}

// STUB (BULK-01-04, test-first). No-op on a single-file group (returns the identical
// group list — nothing appended for a lone file). On a multi-file group, removes
// `fileId` from the shared group's fileIds and appends a new single-file group whose
// mapping is a DEEP COPY of the shared group's mapping at split time
// ([split-copies-the-mapping] — never a fresh initMappingFromHeaders; the operator
// splits to change one field, and discarding their existing placements would be a
// punishment, not a clarification).
export function splitOut(_groups: MappingGroup[], _fileId: string): MappingGroup[] {
  throw new Error('not implemented')
}

// STUB (BULK-01-04, test-first). Renders on EVERY group, including a group of one
// ([coverage-sentence-is-unconditional] — showing it only when >1 file is covered would
// make its absence read as "no sharing", which is exactly the silent share
// [shared-mapping-shown] forbids). Names every file in group.fileIds via `names`.
export function coverageSentence(_group: MappingGroup, _names: Record<string, string>): string {
  throw new Error('not implemented')
}

// STUB (BULK-01-04, test-first). Looks up which group currently owns a file id — after
// a split, resolves to the new split group, not the original.
export function groupOfFile(_groups: MappingGroup[], _fileId: string): MappingGroup | null {
  throw new Error('not implemented')
}

// STUB (BULK-01-04, test-first). Delegates to the shipped lib/mapping.ts canSubmitMapping
// (invoice_number-only structural gate matching resolveMapping) for EVERY group — no
// second, parallel gate is introduced.
export function canSubmitAllMappings(_groups: MappingGroup[]): boolean {
  throw new Error('not implemented')
}
