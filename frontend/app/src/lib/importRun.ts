// Wizard multi-file import — selection-half pure helpers backing CreateUpload's ordered
// file list (BULK-01, task-310/BULK-01-03). Traces to Core AC 1 (pick/drop several files
// in one run, see them all listed, remove one, refuse past 5) and Core AC 6 (every file
// in a run files against the already-selected entity; the run never asks again — hence
// canReadColumnsAll delegates to canReadColumns, which never gates on an entity). Node-
// testable under this project's jsdom-less vitest config, same discipline as
// lib/importFlow.ts.
//
// SELECTION HALF ONLY. The run-reducer half — runReducer, runBatchIds, runFailures,
// runFileRows, routeAfterRun — is BULK-01-05's job. BULK-01-03 creates this module first
// (dependency order); BULK-01-05 EXTENDS it, never recreates it (task-308 correction).

import { canReadColumns } from './importFlow'

export const MAX_RUN_FILES = 5 // [five-file-cap] — Core AC 1: a run accepts at most 5 files.

export interface PickedFile {
  id: string
  file: File
}

export interface SelectionResult {
  files: PickedFile[]
  refusal: string | null
}

// Appends `incoming` onto `current`, preserving order, capped at MAX_RUN_FILES. Only the
// COUNT is capped here — a bad-extension file is still appended; that gate belongs to
// canReadColumnsAll, not to selection (AC3). Whenever the cap drops any incoming file,
// `refusal` names the cap and how many were not added (via capRefusal) — never a silent
// truncation. Exactly at cap, under cap, or zero incoming: refusal is null.
export function addFiles(current: PickedFile[], incoming: File[]): SelectionResult {
  const room = Math.max(0, MAX_RUN_FILES - current.length)
  const accepted = incoming.slice(0, room)
  const dropped = incoming.length - accepted.length
  const files = [...current, ...accepted.map((file) => ({ id: crypto.randomUUID(), file }))]
  return { files, refusal: dropped > 0 ? capRefusal(dropped) : null }
}

// Removes the file matching `id`, preserving the order of the rest. An unknown id is a
// no-op (`filter` simply keeps every entry).
export function removeFile(current: PickedFile[], id: string): PickedFile[] {
  return current.filter((pf) => pf.id !== id)
}

// Sole copy owner of the cap-refusal text — CreateUpload renders whatever this returns
// verbatim, never a re-worded copy of it. Names MAX_RUN_FILES and `dropped`.
export function capRefusal(dropped: number): string {
  const noun = dropped === 1 ? 'file' : 'files'
  const verb = dropped === 1 ? 'was' : 'were'
  return `A run accepts at most ${MAX_RUN_FILES} files — ${dropped} ${noun} ${verb} not added.`
}

// Thin delegator (AC #4): `true` iff `files` is non-empty AND canReadColumns (imported
// unmodified from ./importFlow) is true for every file — one bad file blocks the whole
// selection's read gate. Does not re-implement the extension check or add an entity
// clause (AC #5 — see importFlow.ts's canReadColumns doc comment; that contract is
// load-bearing and not this subtask's to touch).
export function canReadColumnsAll(files: PickedFile[]): boolean {
  return files.length > 0 && files.every((pf) => canReadColumns(pf.file))
}
