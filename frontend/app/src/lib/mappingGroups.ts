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
// Implemented (BULK-01-04, task-309) against the RED specs (BULK-04-1..12) authored in
// mappingGroups.test.ts before these bodies existed — same precedent as
// lib/importFlow.ts's computeNoEntity (task-304, INVCR-01-19) and lib/importRun.ts's
// selection-half (BULK-01-03).

import { canSubmitMapping, initMappingFromHeaders } from './mapping'
import type { ImportPreview } from './importApi'
import type { Mapping } from '../types'

// The exact, ordered, case-sensitive column list — JSON.stringify of the array, no
// sorting, no case-folding. Two files share a group IFF their signatures are equal
// ([layout-signature-is-ordered]: the mapping screen renders one file's column grid, so
// claiming coverage of a differently-ordered/cased file states a share the operator
// cannot verify by eye).
export function columnSignature(columns: string[]): string {
  return JSON.stringify(columns)
}

export interface MappingGroup {
  id: string
  signature: string
  fileIds: string[]
  preview: ImportPreview
  mapping: Mapping
}

// Walks `previewed` in pick order and buckets by columnSignature, preserving
// first-appearance order of groups. Each new group's mapping is seeded with the shipped
// initMappingFromHeaders(preview.columns) — never a blank mapping.
export function groupByLayout(previewed: { fileId: string; preview: ImportPreview }[]): MappingGroup[] {
  const groups: MappingGroup[] = []
  const bySignature = new Map<string, MappingGroup>()
  previewed.forEach(({ fileId, preview }) => {
    const signature = columnSignature(preview.columns)
    const existing = bySignature.get(signature)
    if (existing) {
      existing.fileIds.push(fileId)
      return
    }
    const group: MappingGroup = {
      id: crypto.randomUUID(),
      signature,
      fileIds: [fileId],
      preview,
      mapping: initMappingFromHeaders(preview.columns),
    }
    bySignature.set(signature, group)
    groups.push(group)
  })
  return groups
}

// No-op on a single-file group (returns the identical group list — nothing appended for
// a lone file, and no unknown/already-removed fileId does anything either). On a
// multi-file group, removes `fileId` from the shared group's fileIds and appends a new
// single-file group whose mapping is a DEEP COPY of the shared group's mapping at split
// time ([split-copies-the-mapping] — never a fresh initMappingFromHeaders; the operator
// splits to change one field, and discarding their existing placements would be a
// punishment, not a clarification). `Mapping` is flat/primitive-valued (types.ts), so a
// shallow spread IS a deep copy here.
export function splitOut(groups: MappingGroup[], fileId: string): MappingGroup[] {
  const idx = groups.findIndex((g) => g.fileIds.includes(fileId))
  if (idx === -1) return groups
  const group = groups[idx]
  if (group.fileIds.length <= 1) return groups

  const remaining: MappingGroup = { ...group, fileIds: group.fileIds.filter((id) => id !== fileId) }
  const split: MappingGroup = {
    id: crypto.randomUUID(),
    signature: group.signature,
    fileIds: [fileId],
    preview: group.preview,
    mapping: { ...group.mapping },
  }

  const next = groups.slice()
  next[idx] = remaining
  next.push(split)
  return next
}

// Renders on EVERY group, including a group of one
// ([coverage-sentence-is-unconditional] — showing it only when >1 file is covered would
// make its absence read as "no sharing", which is exactly the silent share
// [shared-mapping-shown] forbids). Names every file in group.fileIds via `names`.
export function coverageSentence(group: MappingGroup, names: Record<string, string>): string {
  const fileNames = group.fileIds.map((id) => names[id] ?? id)
  const list =
    fileNames.length === 1
      ? fileNames[0]
      : `${fileNames.slice(0, -1).join(', ')} and ${fileNames[fileNames.length - 1]}`
  const noun = fileNames.length === 1 ? 'file' : 'files'
  return `This mapping applies to ${fileNames.length} ${noun}: ${list}.`
}

// Looks up which group currently owns a file id — after a split, resolves to the new
// split group, not the original. Unknown fileId resolves to null.
export function groupOfFile(groups: MappingGroup[], fileId: string): MappingGroup | null {
  return groups.find((g) => g.fileIds.includes(fileId)) ?? null
}

// Delegates to the shipped lib/mapping.ts canSubmitMapping (invoice_number-only
// structural gate matching resolveMapping) for EVERY group — no second, parallel gate is
// introduced. Mirrors lib/importRun.ts's canReadColumnsAll idiom: an empty group list has
// nothing ready to submit.
export function canSubmitAllMappings(groups: MappingGroup[]): boolean {
  return groups.length > 0 && groups.every((g) => canSubmitMapping(g.mapping))
}
