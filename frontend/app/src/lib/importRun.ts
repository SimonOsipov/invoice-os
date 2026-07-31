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

// ============================================================================
// RUN-REDUCER HALF (BULK-01-05, task-308)
// ============================================================================
//
// Extends this module (BULK-01-03 created the selection half above) with the
// sequential-run state machine: one createImport in flight at a time, each file its
// own outcome, continuation through failures ([partial-success-kept], Core AC 2/5/6).
// App.tsx's startRun() is the only writer of RunAction — it awaits each createImport
// in turn (never Promise.all — [sequential-not-parallel]) and dispatches 'phase'/
// 'settled' off that one call, so this reducer never has to reason about more than one
// in-flight request at a time.
import { routeAfterImport } from './reviewBatch'
import type { ImportReport, UploadPhase } from './importApi'

// One file's state within a run. 'pending' before its turn; 'uploading' while
// createImport's onPhase is firing (phase mirrors UploadPhase VERBATIM, no
// re-derivation); 'imported'/'failed' once createImport has settled. `failed` here is
// a REQUEST-level failure (network/http/malformed) -- a batch that came back
// `completed` with `ready_invoices: 0` (everything quarantined) is still 'imported':
// the create succeeded, it just made zero ready invoices (BULK-05-10). That is what
// keeps its batchId eligible for runBatchIds/routeAfterRun even though nothing in it
// was ready.
export type FileOutcome =
  | { kind: 'pending' }
  | { kind: 'uploading'; phase: UploadPhase }
  | { kind: 'imported'; batchId: string; report: ImportReport }
  | { kind: 'failed'; message: string }

export interface RunFile {
  id: string
  name: string
  groupId: string
  outcome: FileOutcome
}

export interface ImportRun {
  files: RunFile[]
  cursor: number
  status: 'idle' | 'running' | 'finished'
}

export type RunAction =
  | { type: 'start'; files: RunFile[] }
  | { type: 'phase'; phase: UploadPhase }
  | { type: 'settled'; outcome: Extract<FileOutcome, { kind: 'imported' } | { kind: 'failed' }> }

// One helper so 'start' and 'settled' can never disagree about when a run counts as
// finished — the cursor has walked past the last file. Applying it at 'start' too
// means a (never-expected-in-practice) zero-file run resolves to 'finished' rather
// than a 'running' state nothing will ever advance out of.
function runStatus(fileCount: number, cursor: number): ImportRun['status'] {
  return cursor >= fileCount ? 'finished' : 'running'
}

// [partial-success-kept]: `settled` ALWAYS advances the cursor, regardless of
// outcome.kind -- a failure cannot end the run early (AC #1). Only the settle whose
// new cursor reaches files.length flips status to 'finished' (AC #2); every earlier
// settle leaves it 'running'. `phase` writes onto files[cursor] ONLY, leaving every
// other file's outcome untouched (AC #2), and is a total no-op -- returns the SAME run
// instance, not a copy -- once status is 'finished': a late phase event from a
// settled/aborted request must not resurrect a row or re-render anything (BULK-05-4's
// identity check).
export function runReducer(run: ImportRun, a: RunAction): ImportRun {
  switch (a.type) {
    case 'start':
      return { files: a.files, cursor: 0, status: runStatus(a.files.length, 0) }
    case 'phase': {
      if (run.status === 'finished') return run
      return {
        ...run,
        files: run.files.map((f, i) => (i === run.cursor ? { ...f, outcome: { kind: 'uploading', phase: a.phase } } : f)),
      }
    }
    case 'settled': {
      const files = run.files.map((f, i) => (i === run.cursor ? { ...f, outcome: a.outcome } : f))
      const cursor = run.cursor + 1
      return { files, cursor, status: runStatus(files.length, cursor) }
    }
  }
}

// AC #3: the ids of 'imported' outcomes, in run order, omitting 'failed'/'pending'/
// 'uploading' entries. A `ready_invoices: 0` batch still counts -- its outcome kind is
// 'imported', not 'failed' (BULK-05-10).
export function runBatchIds(run: ImportRun): string[] {
  const ids: string[] = []
  for (const f of run.files) {
    if (f.outcome.kind === 'imported') ids.push(f.outcome.batchId)
  }
  return ids
}

// AC #4: `{name, message}` per 'failed' outcome, in run order. `name` is the RunFile's
// own name (never re-derived from the request); `message` is the outcome's message
// VERBATIM, never re-worded.
export function runFailures(run: ImportRun): { name: string; message: string }[] {
  const failures: { name: string; message: string }[] = []
  for (const f of run.files) {
    if (f.outcome.kind === 'failed') failures.push({ name: f.name, message: f.outcome.message })
  }
  return failures
}

// One row's shape per honesty-preserving state (ImportProgress.tsx's own header
// comment, AC #10) -- 'queued'/'sending'/'processing' carry NOTHING beyond the name:
// no stage list, no row counter, no byte counter, no percentage, no rule-set version.
// 'imported' carries `count` -- the server's OWN ready_invoices, read back AFTER the
// fact once the file has settled, never a client guess made WHILE the request is in
// flight (that is exactly what the no-row-counter constraint forbids; this is a
// different moment in the lifecycle). 'failed' carries `reason` verbatim, never
// re-worded.
export type RunFileRow =
  | { name: string; kind: 'queued' }
  | { name: string; kind: 'sending' }
  | { name: string; kind: 'processing' }
  | { name: string; kind: 'imported'; count: number }
  | { name: string; kind: 'failed'; reason: string }

// AC #10: one row per file, TOTAL over the whole run (BULK-05-12) -- every file
// appears regardless of its own outcome kind, in file order. An 'uploading' outcome
// maps any UploadPhase kind other than 'sending' to 'processing' -- the same ternary
// ImportProgress.tsx's own header comment already uses for UploadPhase, never a third
// label invented here.
export function runFileRows(run: ImportRun): RunFileRow[] {
  return run.files.map((f): RunFileRow => {
    switch (f.outcome.kind) {
      case 'pending':
        return { name: f.name, kind: 'queued' }
      case 'uploading':
        return { name: f.name, kind: f.outcome.phase.kind === 'sending' ? 'sending' : 'processing' }
      case 'imported':
        return { name: f.name, kind: 'imported', count: f.outcome.report.ready_invoices }
      case 'failed':
        return { name: f.name, kind: 'failed', reason: f.outcome.message }
    }
  })
}

export type RunRoute = { kind: 'single'; invoiceId: string } | { kind: 'review'; batchIds: string[] } | { kind: 'none' }

// AC #5, order is load-bearing:
//  1. No batch ids at all (every file failed) -> 'none'.
//  2. EXACTLY one file in the run AND routeAfterImport(thatReport, resolvedInvoiceId)
//     .kind === 'single' -> 'single'. routeAfterImport itself is UNCHANGED
//     (lib/reviewBatch.ts) -- this never re-derives its truthiness-on-
//     resolvedInvoiceId rule, it only calls it (BULK-05-9). Gated on `run.files.length`
//     (the RUN's own size), never on runBatchIds().length -- a 2-file run where one
//     file failed and the other alone would have routed 'single' must still fall
//     through to 'review' (BULK-05-8's "run-size gate").
//  3. Otherwise -> 'review' with EVERY batch id, in run order -- including a batch
//     whose own ready_invoices is 0 (BULK-05-10): joining the review is not
//     conditioned on what routeAfterImport would have said about that one file alone.
export function routeAfterRun(run: ImportRun, resolvedInvoiceId: string | null): RunRoute {
  const batchIds = runBatchIds(run)
  if (batchIds.length === 0) return { kind: 'none' }
  if (run.files.length === 1) {
    const outcome = run.files[0].outcome
    if (outcome.kind === 'imported') {
      const route = routeAfterImport(outcome.report, resolvedInvoiceId)
      if (route.kind === 'single') return { kind: 'single', invoiceId: route.invoiceId }
    }
  }
  return { kind: 'review', batchIds }
}
