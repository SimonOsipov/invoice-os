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
//
// Every export below is a STUB (BULK-01-03, test-first) — importRun.test.ts's RED specs
// pin the contract before these bodies exist. Throwing (rather than a wrong-but-plausible
// guess) makes every spec fail on an assertion/thrown-error mismatch, never an
// import/compile error — same precedent as lib/importFlow.ts's computeNoEntity STUB
// (task-304, INVCR-01-19).

export const MAX_RUN_FILES = 5 // [five-file-cap] — Core AC 1: a run accepts at most 5 files.

export interface PickedFile {
  id: string
  file: File
}

export interface SelectionResult {
  files: PickedFile[]
  refusal: string | null
}

// STUB (BULK-01-03, test-first). Appends `incoming` to `current`, preserving order,
// capped at MAX_RUN_FILES. Whenever the cap drops any incoming file, `refusal` names the
// cap and how many files were not added (via capRefusal) — never a silent truncation.
// Exactly at cap, under cap, or zero incoming: refusal is null. A bad-extension file is
// still appended — this function never drops on extension, only on the count cap.
export function addFiles(_current: PickedFile[], _incoming: File[]): SelectionResult {
  throw new Error('not implemented')
}

// STUB (BULK-01-03, test-first). Removes the file matching `id`, preserving the order of
// the rest. An unknown id is a no-op.
export function removeFile(_current: PickedFile[], _id: string): PickedFile[] {
  throw new Error('not implemented')
}

// STUB (BULK-01-03, test-first). Sole copy owner of the cap-refusal text once
// implemented — CreateUpload renders whatever this returns verbatim, never a re-worded
// copy of it. Names MAX_RUN_FILES and `dropped`.
export function capRefusal(_dropped: number): string {
  throw new Error('not implemented')
}

// STUB (BULK-01-03, test-first). Thin delegator (AC #4): `true` iff `files` is non-empty
// AND canReadColumns (imported unmodified from ./importFlow) is true for every file — one
// bad file blocks the whole selection's read gate. Must not re-implement the extension
// check or add an entity clause (AC #5 — see importFlow.ts's canReadColumns doc comment;
// that contract is load-bearing and not this subtask's to touch).
export function canReadColumnsAll(_files: PickedFile[]): boolean {
  throw new Error('not implemented')
}
