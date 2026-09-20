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

import { canSubmitMapping, fillUnplacedFromAliases, initMappingFromHeaders, restoreMapping } from './mapping'
import type { ImportPreview, SavedMapping, SuggestMapping } from './importApi'
import type { Mapping } from '../types'
import { fmtDateTime } from './format'

// The exact, ordered, case-sensitive column list — JSON.stringify of the array, no
// sorting, no case-folding. Two files share a group IFF their signatures are equal
// ([layout-signature-is-ordered]: the mapping screen renders one file's column grid, so
// claiming coverage of a differently-ordered/cased file states a share the operator
// cannot verify by eye).
export function columnSignature(columns: string[]): string {
  return JSON.stringify(columns)
}

export interface RestoredFrom {
  savedAt: string
  mapping: Mapping // the placements as restored; a placement still equal to this renders RESTORED
}

export interface SuggestedFrom {
  headerRow: number
  mapping: Mapping // the placements as suggested; a placement still equal to this renders SUGGESTED
}

export interface MappingGroup {
  id: string
  signature: string
  fileIds: string[]
  preview: ImportPreview
  headerRow: number // the row the columns were decoded at; the import must read the same row
  mapping: Mapping
  restored: RestoredFrom | null
  suggested: SuggestedFrom | null
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
      headerRow: 1,
      mapping: initMappingFromHeaders(preview.columns),
      restored: null,
      suggested: null,
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
    headerRow: group.headerRow,
    mapping: { ...group.mapping },
    restored: group.restored,
    suggested: group.suggested,
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

// The snapshot shares the restored object: App's assign/unmap replace group.mapping, never
// write into it, so the snapshot stays as restored.
export function applySavedMapping(group: MappingGroup, saved: SavedMapping | null): MappingGroup {
  if (!saved) return group
  const mapping = restoreMapping(group.preview.columns, saved.mapping)
  return { ...group, mapping, restored: { savedAt: saved.saved_at, mapping } }
}

// No undo: the restored and suggested snapshots are both dropped.
export function returnToAutomatic(group: MappingGroup): MappingGroup {
  return { ...group, mapping: initMappingFromHeaders(group.preview.columns), restored: null, suggested: null }
}

export type PlacementBadge = 'restored' | 'suggested' | 'auto' | null

// RESTORED > SUGGESTED > AUTO. A placement moved off its recorded header loses its badge.
export function placementBadge(group: MappingGroup, field: string, header: string, recognized: Mapping): PlacementBadge {
  if (group.mapping[field] !== header) return null
  if (group.restored?.mapping[field] === header) return 'restored'
  if (group.suggested?.mapping[field] === header) return 'suggested'
  if (recognized[field] === header) return 'auto'
  return null
}

// Survives edits; only returnToAutomatic clears it.
export function restoredNotice(group: MappingGroup): string | null {
  if (!group.restored) return null
  return `Mapping restored from this client's earlier import, saved ${fmtDateTime(group.restored.savedAt)}.`
}

// One lookup at a time, in group order. A failed lookup leaves that group on today's seed.
export async function restoreGroups(
  groups: MappingGroup[],
  lookup: ((documentId: string) => Promise<SavedMapping | null>) | null,
): Promise<MappingGroup[]> {
  if (!lookup) return groups
  const result: MappingGroup[] = []
  for (const group of groups) {
    try {
      const saved = await lookup(group.preview.document_id)
      result.push(applySavedMapping(group, saved))
    } catch {
      result.push(group)
    }
  }
  return result
}

// A `saved` answer takes the restore path; `none` is the identity. The `saved` snapshot
// shares the mapping object; the `suggested` snapshot keeps the AI's OWN placements while
// group.mapping also carries the alias fallback, so a fallback badges AUTO, not SUGGESTED.
export function applySuggestion(group: MappingGroup, res: SuggestMapping): MappingGroup {
  if (res.source === 'none') return group
  const preview: ImportPreview = { ...group.preview, columns: res.columns, sample_rows: res.sample_rows, rows_total: res.rows_total }
  const mapping = restoreMapping(res.columns, res.mapping)
  const base = { ...group, preview, signature: columnSignature(res.columns), mapping, headerRow: res.header_row }
  if (res.source === 'saved') {
    return { ...base, restored: { savedAt: res.saved_at ?? '', mapping }, suggested: null }
  }
  return { ...base, mapping: fillUnplacedFromAliases(res.columns, mapping), suggested: { headerRow: res.header_row, mapping } }
}

// One suggestion at a time, in group order, for the groups restoreGroups left unrestored.
// A failed suggestion leaves that group on today's seed.
export async function suggestGroups(
  groups: MappingGroup[],
  suggest: ((documentId: string) => Promise<SuggestMapping>) | null,
): Promise<MappingGroup[]> {
  if (!suggest) return groups
  const result: MappingGroup[] = []
  for (const group of groups) {
    if (group.restored) {
      result.push(group)
      continue
    }
    try {
      const res = await suggest(group.preview.document_id)
      result.push(applySuggestion(group, res))
    } catch {
      result.push(group)
    }
  }
  return result
}

// Delegates to the shipped lib/mapping.ts canSubmitMapping (invoice_number-only
// structural gate matching resolveMapping) for EVERY group — no second, parallel gate is
// introduced. Mirrors lib/importRun.ts's canReadColumnsAll idiom: an empty group list has
// nothing ready to submit.
export function canSubmitAllMappings(groups: MappingGroup[]): boolean {
  return groups.length > 0 && groups.every((g) => canSubmitMapping(g.mapping))
}

// An untouched restore skips the save, so it cannot overwrite an edited copy posted earlier in
// the run.
export function rememberMapping(group: MappingGroup): boolean {
  if (!group.restored) return true
  return !mappingsEqual(group.mapping, group.restored.mapping)
}

// Flat values; the key union also catches a key present on one side only.
function mappingsEqual(a: Mapping, b: Mapping): boolean {
  const keys = new Set([...Object.keys(a), ...Object.keys(b)])
  for (const k of keys) {
    if (a[k] !== b[k]) return false
  }
  return true
}
