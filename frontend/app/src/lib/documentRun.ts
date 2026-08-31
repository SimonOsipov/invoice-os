// The document run's pure half — the poll-state reducer, the terminal-state predicate and
// the budget arithmetic. No DOM, no network: App.tsx injects the transport, matching
// importRun.ts's own node-testable discipline. Pinned by documentRun.test.ts.

import type { FileOutcome, ImportRun, RunFileRow } from './importRun'
import type { ExtractionJob, ImportReport } from './importApi'
import { LIVE_POLL_MS } from './invoices'

// Mirrors e2e/api/contract-document-upload.spec.ts's POLL_BUDGET_MS: above the mock
// extractor's sub-second work, below the worker's own 10-minute River timeout.
export const EXTRACTION_POLL_BUDGET_MS = 120_000

// 'waiting' is the only non-terminal verdict; both terminal ones carry what the caller
// needs to settle a FileOutcome without re-reading the job.
export type PollVerdict = { kind: 'waiting' } | { kind: 'succeeded'; jobId: string } | { kind: 'failed'; reason: string }

// 'failed' is NOT terminal — River retries a failed attempt; only a dead-letter ends it.
export function isTerminalExtractionState(state: string): boolean {
  return state === 'succeeded' || state === 'dead_lettered'
}

// The newest job by created_at, NOT jobs[0] — the array's order is the server's, and a
// reducer that trusts it is right only by coincidence.
export function newestJob(jobs: readonly ExtractionJob[]): ExtractionJob | null {
  let newest: ExtractionJob | null = null
  for (const job of jobs) {
    if (newest === null || job.created_at > newest.created_at) newest = job
  }
  return newest
}

// Sole copy owner of the budget-expiry reason, beside deadLetterRefusal — the run renders
// what these return verbatim. Distinct texts: AC-5 wants the three failure causes
// distinguishable.
export function pollBudgetRefusal(): string {
  return `Extraction is still running after ${Math.round(EXTRACTION_POLL_BUDGET_MS / 1000)} seconds. The document was stored and its extraction continues — open it again later.`
}

export function deadLetterRefusal(lastError: string | null): string {
  const detail = lastError === null || lastError === '' ? 'the server gave no reason' : lastError
  return `Extraction failed for this document — ${detail}`
}

// The reducer: the whole jobs[] array plus how long this document has been polled, in.
// One verdict out. Budget arithmetic lives here so no caller re-derives the boundary.
//
// A terminal job outranks the budget: once the server has settled, the elapsed clock is
// no longer the honest reason.
export function pollVerdict(jobs: readonly ExtractionJob[], elapsedMs: number): PollVerdict {
  const job = newestJob(jobs)
  if (job !== null && isTerminalExtractionState(job.state)) {
    if (job.state === 'succeeded') return { kind: 'succeeded', jobId: job.id }
    return { kind: 'failed', reason: deadLetterRefusal(job.last_error) }
  }
  if (elapsedMs > EXTRACTION_POLL_BUDGET_MS) return { kind: 'failed', reason: pollBudgetRefusal() }
  return { kind: 'waiting' }
}

// The card's per-document word, over the migration's CHECK set (EXTR-10-01). 'succeeded'
// and 'dead_lettered' get no word -- pollVerdict already owns their wording.
export type DocumentRowState =
  | { kind: 'queued' }
  | { kind: 'reading' }
  | { kind: 'retrying' }
  | { kind: 'processing' }
  | { kind: 'imported'; count: number }
  | { kind: 'failed'; reason: string }

// The ONE place extraction_jobs.state becomes a word. null -> queued: the worker's own tx
// makes state='queued' unobservable, so an empty jobs[] must still read as "started".
export function stageOf(job: ExtractionJob | null): DocumentRowState | null {
  if (job === null || job.state === 'queued') return { kind: 'queued' }
  if (job.state === 'extracting') return { kind: 'reading' }
  if (job.state === 'failed') return { kind: 'retrying' }
  return null
}

// The poll loop, moved out of App.tsx (EXTR-10-02) so its per-tick reporting has an
// oracle. Reports the stage it READ on every tick; a terminal tick reports nothing
// because stageOf already returns null for succeeded/dead_lettered (D-8).
export async function pollUntilSettled(
  documentId: string,
  deps: {
    getJobs: (documentId: string) => Promise<readonly ExtractionJob[]>
    onStage: (state: DocumentRowState) => void
    sleep: (ms: number) => Promise<void>
    now: () => number
  },
): Promise<PollVerdict> {
  const startedAt = deps.now()
  for (;;) {
    const jobs = await deps.getJobs(documentId)
    const stage = stageOf(newestJob(jobs))
    if (stage !== null) deps.onStage(stage)
    const verdict = pollVerdict(jobs, deps.now() - startedAt)
    if (verdict.kind !== 'waiting') return verdict
    await deps.sleep(LIVE_POLL_MS)
  }
}

// One row per run.files entry, joined by RunFile.id -- never by name (importRun.ts:77-78
// records why a name-keyed join is wrong); the own-property check keeps an id like
// 'constructor' from resolving through Object.prototype instead of the ?? fallback.
export function documentRunRows(run: ImportRun, stages: Readonly<Record<string, DocumentRowState>>): RunFileRow[] {
  return run.files.map((f) => ({ name: f.name, ...(Object.hasOwn(stages, f.id) ? stages[f.id] : { kind: 'queued' }) }))
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
  // fileId stays optional through the RED phase -- the call site below is not yet passing
  // it (that pass-through is task-785's GREEN, alongside flipping this to required).
  poll: (documentId: string, fileId?: string) => Promise<PollVerdict>
  importDocument: (documentId: string) => Promise<ImportReport>
  // Required, not optional: an optional hook silently no-ops when a caller forgets it —
  // the defect class this story exists to close. Wired by EXTR-10-03 (task-785).
  onStage: (fileId: string, state: DocumentRowState) => void
}

export interface DocumentRunOutcome {
  id: string
  name: string
  outcome: FileOutcome
}

function messageOf(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

// N pipelines, each upload -> poll -> import, all started before any of them is awaited
// (Core AC #9). One file's failure settles that file and nothing else — every pipeline
// catches its own, so Promise.all can never reject. Pinned by RUN-1 / RUN-4.
export function startDocumentRun(
  files: readonly DocumentRunFile[],
  deps: DocumentPipelineDeps,
): Promise<DocumentRunOutcome[]> {
  return Promise.all(
    files.map(async (f): Promise<DocumentRunOutcome> => {
      try {
        const documentId = await deps.upload(f.file)
        const verdict = await deps.poll(documentId)
        // Carried VERBATIM: the poll owns the wording, this only relays it.
        if (verdict.kind !== 'succeeded') {
          const reason = verdict.kind === 'failed' ? verdict.reason : 'extraction never settled'
          return { id: f.id, name: f.name, outcome: { kind: 'failed', message: reason } }
        }
        const report = await deps.importDocument(documentId)
        return { id: f.id, name: f.name, outcome: { kind: 'imported', batchId: report.id, report } }
      } catch (err) {
        return { id: f.id, name: f.name, outcome: { kind: 'failed', message: messageOf(err) } }
      }
    }),
  )
}
