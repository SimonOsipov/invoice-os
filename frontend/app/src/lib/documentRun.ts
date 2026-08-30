// The document run's pure half — the poll-state reducer, the terminal-state predicate and
// the budget arithmetic. No DOM, no network: App.tsx injects the transport, matching
// importRun.ts's own node-testable discipline. Pinned by documentRun.test.ts.

import type { FileOutcome } from './importRun'
import type { ExtractionJob, ImportReport } from './importApi'

// Mirrors e2e/api/contract-document-upload.spec.ts's POLL_BUDGET_MS: above the mock
// extractor's sub-second work, below the worker's own 10-minute River timeout.
export const EXTRACTION_POLL_BUDGET_MS = 120_000

export const EXTRACTION_POLL_INTERVAL_MS = 2_000

// 'waiting' is the only non-terminal verdict; both terminal ones carry what the caller
// needs to settle a FileOutcome without re-reading the job.
export type PollVerdict = { kind: 'waiting' } | { kind: 'succeeded'; jobId: string } | { kind: 'failed'; reason: string }

// STAGE-2.5 STUB (EXTR-09-07, test-first) — throwing rather than guessing keeps every
// spec RED on an assertion/not-implemented mismatch, never on a compile error.
export function isTerminalExtractionState(_state: string): boolean {
  throw new Error(
    "not implemented — isTerminalExtractionState must return true for 'succeeded' and 'dead_lettered' only; 'queued'/'extracting'/'failed' are still in flight (River retries a 'failed' attempt)",
  )
}

// The newest job by created_at, NOT jobs[0] — the array's order is the server's, and a
// reducer that trusts it is right only by coincidence.
export function newestJob(_jobs: readonly ExtractionJob[]): ExtractionJob | null {
  throw new Error('not implemented — newestJob must return the job with the greatest created_at, or null for an empty array')
}

// Sole copy owner of the budget-expiry reason, beside deadLetterRefusal — the run renders
// what these return verbatim. Distinct texts: AC-5 wants the three failure causes
// distinguishable.
export function pollBudgetRefusal(): string {
  throw new Error('not implemented — pollBudgetRefusal must return the non-empty budget-expiry reason, distinct from deadLetterRefusal')
}

export function deadLetterRefusal(_lastError: string | null): string {
  throw new Error("not implemented — deadLetterRefusal must carry last_error VERBATIM inside a non-empty reason, and must not render a null last_error as the text 'null'")
}

// The reducer: the whole jobs[] array plus how long this document has been polled, in.
// One verdict out. Budget arithmetic lives here so no caller re-derives the boundary.
export function pollVerdict(_jobs: readonly ExtractionJob[], _elapsedMs: number): PollVerdict {
  throw new Error(
    "not implemented — pollVerdict must reduce jobs[] newest-first to {kind:'succeeded'|'failed'|'waiting'}, and must return {kind:'failed', reason: pollBudgetRefusal()} once elapsedMs exceeds EXTRACTION_POLL_BUDGET_MS rather than waiting forever",
  )
}

export interface DocumentRunFile {
  id: string
  name: string
  file: File
}

// The injected transport. `poll` resolves only once the document reaches a terminal
// verdict (or the budget expires); the loop and its clock are the caller's.
export interface DocumentPipelineDeps {
  upload: (file: File) => Promise<string>
  poll: (documentId: string) => Promise<PollVerdict>
  importDocument: (documentId: string) => Promise<ImportReport>
}

export interface DocumentRunOutcome {
  id: string
  name: string
  outcome: FileOutcome
}

// STAGE-2.5 STUB (EXTR-09-07, test-first) — the real body is Stage 3's.
//
// N pipelines, each upload -> poll -> import, all started before any of them is awaited
// (Core AC #9). One file's failure settles that file and nothing else.
export function startDocumentRun(_files: readonly DocumentRunFile[], _deps: DocumentPipelineDeps): Promise<DocumentRunOutcome[]> {
  throw new Error(
    'not implemented — startDocumentRun must start every file\'s upload before awaiting any of them (Promise.all over N pipelines, never a sequential for-await), and must resolve one DocumentRunOutcome per input file in input order, a rejected pipeline settling as {kind:\'failed\'} rather than rejecting the run',
  )
}
