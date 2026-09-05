// RED specs (EXTR-09-07, task-774, Mode A / test-first) — pin the document run's pure
// half before the executor writes the bodies in documentRun.ts. task-774's Test Specs
// table (POLL-1..4, RUN-1, RUN-4 here; FORK-1..3, RUN-2/3/5 in importRun.test.ts) is
// authoritative.
//
// vitest environment is 'node' (vitest.config.ts:5) — no jsdom, which is exactly why the
// reducer, the terminal predicate, the budget arithmetic and the concurrency orchestrator
// live in a module of their own rather than inside App.tsx.
//
// Every spec below currently fails because documentRun.ts's stub bodies throw
// `new Error('not implemented — …')` before returning anything; each message spells the
// expected shape. That IS the correct RED reason (assertion / not-implemented), never an
// import or collection error — same precedent as importRun.test.ts's own BULK-03 header.
import { readFileSync } from 'node:fs'
import path from 'node:path'

import { describe, expect, it, vi } from 'vitest'

import * as documentRunModule from './documentRun'
import {
  EXTRACTION_POLL_BUDGET_MS,
  deadLetterRefusal,
  documentRunRows,
  isTerminalExtractionState,
  newestJob,
  pollBudgetRefusal,
  pollUntilSettled,
  pollVerdict,
  stageOf,
  startDocumentRun,
} from './documentRun'
import type { DocumentPipelineDeps, DocumentRowState, DocumentRunFile } from './documentRun'
import type { ExtractionJob, ImportReport } from './importApi'
import { routeAfterRun } from './importRun'
import type { FileOutcome, ImportRun, RunFile } from './importRun'
import { LIVE_POLL_MS } from './invoices'

function job(over: Partial<ExtractionJob>): ExtractionJob {
  return {
    id: 'job-1',
    document_id: 'doc-1',
    state: 'queued',
    created_at: '2026-08-30T10:00:00Z',
    last_error: null,
    failure_kind: null,
    ...over,
  }
}

function report(id: string, readyInvoices = 1): ImportReport {
  return {
    id,
    status: 'completed',
    format: 'pdf',
    delimiter: '',
    encoding: 'utf-8',
    rows_total: 1,
    rows_valid: 1,
    rows_invalid: 0,
    ready_invoices: readyInvoices,
    quarantined_invoices: 0,
    errors: [],
    rule_set_version: 5,
    invoices_clean: readyInvoices,
    invoices_with_violations: 0,
    invoice_violations: [],
  }
}

function docFiles(names: string[]): DocumentRunFile[] {
  return names.map((name, i) => ({ id: `f${i + 1}`, name, file: new File([], name, { type: 'application/pdf' }) }))
}

// EXTR-10-01: documentRunRows joins on RunFile, not DocumentRunFile -- a fresh helper
// rather than reusing docFiles, whose shape (a File) is the wrong side of the seam.
function runFile(id: string, name: string): RunFile {
  return { id, name, groupId: '', outcome: { kind: 'pending' } }
}

// --- EXTR-15-04 (task-830) shared seam ----------------------------------------
//
// The widened signature. Declared as a TYPE and assigned, never called two-arg against the
// shipped export: TS accepts a one-arg function where a two-arg type is wanted, so this
// compiles against BOTH the shipped `deadLetterRefusal(lastError)` and the widened
// `deadLetterRefusal(failureKind, lastError)`. The red below is therefore an assertion red,
// not a package-wide `tsc` red that would mask every other typecheck failure. The arity
// itself has its own oracle in TS15-1.
type Refuse = (failureKind: string | null, lastError: string | null) => string
const refuse: Refuse = deadLetterRefusal

// --- POLL-1 ----------------------------------------------------------------

describe('isTerminalExtractionState — only succeeded and dead_lettered end the poll (POLL-1, AC-5)', () => {
  // The five values migrations/20260827084025_extraction_jobs.sql:20-21 permits. 'failed'
  // is NOT terminal: River retries a failed attempt, and treating it as an ending is the
  // failure this table exists to catch.
  const TABLE: readonly [string, boolean][] = [
    ['queued', false],
    ['extracting', false],
    ['failed', false],
    ['succeeded', true],
    ['dead_lettered', true],
  ]

  it('POLL-1: every state of the CHECK constraint is classified, and only the last two are terminal', () => {
    // Guards against a table that could pass vacuously: it must be the full five and must
    // contain both verdicts.
    expect(TABLE).toHaveLength(5)
    expect(TABLE.some(([, terminal]) => terminal)).toBe(true)
    expect(TABLE.some(([, terminal]) => !terminal)).toBe(true)

    for (const [state, terminal] of TABLE) {
      expect(isTerminalExtractionState(state), `state ${state}`).toBe(terminal)
    }
  })
})

// --- POLL-2 ----------------------------------------------------------------

describe('pollVerdict — the budget expires into a failed outcome, never a hang (POLL-2, AC-5)', () => {
  // Falsification: a reducer that only ever answers on the job's own state waits forever
  // on a document River never finishes. AUDIT-06 shipped an unbounded actionTimeout for
  // exactly this reason; this spec is what stops the second one.
  it('POLL-2: an extracting job past EXTRACTION_POLL_BUDGET_MS fails with the budget reason', () => {
    const jobs = [job({ state: 'extracting' })]

    // The boundary from below, so the spec cannot pass on an impl that fails immediately.
    expect(pollVerdict(jobs, EXTRACTION_POLL_BUDGET_MS - 1)).toEqual({ kind: 'waiting' })

    const verdict = pollVerdict(jobs, EXTRACTION_POLL_BUDGET_MS + 1)
    expect(verdict.kind).toBe('failed')
    if (verdict.kind !== 'failed') throw new Error('unreachable — narrowed above')
    // Sole copy owner, and DISTINCT from the dead-letter reason: AC-5 wants the three
    // causes of a failed file distinguishable from each other, not one generic sentence.
    expect(pollBudgetRefusal().length).toBeGreaterThan(0)
    expect(verdict.reason).toBe(pollBudgetRefusal())
    // Retargeted, not weakened, for EXTR-15-04's widened signature: the kind is now the
    // first argument. Same claim -- the budget reason is not the dead-letter reason.
    expect(pollBudgetRefusal()).not.toBe(refuse('pages_not_rendered', 'pdfium: render failed'))
  })
})

// --- POLL-3 ----------------------------------------------------------------

describe('pollVerdict — dead_lettered carries last_error verbatim (POLL-3, AC-5)', () => {
  const LAST_ERROR = 'pdfium: page 1 render failed (code 3)'

  it('POLL-3: the reason contains the server’s own last_error, unreworded', () => {
    const verdict = pollVerdict([job({ state: 'dead_lettered', last_error: LAST_ERROR })], 0)
    expect(verdict.kind).toBe('failed')
    if (verdict.kind !== 'failed') throw new Error('unreachable — narrowed above')
    expect(verdict.reason).toContain(LAST_ERROR)
  })

  it('POLL-3b: a dead_lettered job with no last_error still names a reason, never the text "null"', () => {
    const verdict = pollVerdict([job({ state: 'dead_lettered', last_error: null })], 0)
    expect(verdict.kind).toBe('failed')
    if (verdict.kind !== 'failed') throw new Error('unreachable — narrowed above')
    expect(verdict.reason.length).toBeGreaterThan(0)
    expect(verdict.reason).not.toMatch(/\bnull\b/)
    expect(verdict.reason).not.toMatch(/\bundefined\b/)
  })
})

// --- POLL-4 ----------------------------------------------------------------

// The multi-job input below is DEFENSIVE, not a description of the server. EXTR-09-01's
// enqueue seam dedupes permanently (idempotency_keys has no DELETE grant), so one document
// yields at most one job today; EXTR-17 owns re-extraction, which is what would make a
// second job reachable. GET /v1/extractions returns an ARRAY regardless, so the client's
// newest-wins rule is real logic that must not depend on the server's ordering.
describe('pollVerdict / newestJob — the newest job wins when a document has several (POLL-4, AC-5)', () => {
  const older = job({ id: 'job-old', state: 'dead_lettered', created_at: '2026-08-30T10:00:00Z', last_error: 'stale attempt' })
  const newer = job({ id: 'job-new', state: 'succeeded', created_at: '2026-08-30T10:05:00Z' })

  it('POLL-4: a newer succeeded job beats an older dead_lettered one, in EITHER array order', () => {
    // Both orders, because the server returns newest-first: an impl that reads jobs[0]
    // would be right by coincidence against one input and wrong against the other.
    const orders: readonly ExtractionJob[][] = [
      [newer, older],
      [older, newer],
    ]
    expect(orders).toHaveLength(2)

    for (const jobs of orders) {
      expect(pollVerdict(jobs, 0), `order ${jobs.map((j) => j.id).join(',')}`).toEqual({ kind: 'succeeded', jobId: 'job-new' })
    }
  })

  it('POLL-4b: newestJob picks by created_at, and answers null for an empty array', () => {
    expect(newestJob([older, newer])?.id).toBe('job-new')
    expect(newestJob([newer, older])?.id).toBe('job-new')
    expect(newestJob([])).toBeNull()
  })
})

// --- RUN-1 -----------------------------------------------------------------

// A concurrency probe that cannot hang the suite. Every upload records its arrival and
// waits on one shared gate; the gate opens either when all N have arrived (concurrent) or
// after escapeMs (sequential, where the Nth arrival never comes). `peak` is the honest
// observable: a sequential run's peak is 1 no matter how long the escape waits, so a spec
// that merely counted three upload CALLS would pass on the very loop this forbids.
function uploadBarrier(n: number, escapeMs: number) {
  let inFlight = 0
  let peak = 0
  let open!: () => void
  const gate = new Promise<void>((resolve) => {
    open = resolve
  })
  const escape = setTimeout(() => open(), escapeMs)
  return {
    get peak() {
      return peak
    },
    async arrive() {
      inFlight += 1
      peak = Math.max(peak, inFlight)
      if (inFlight >= n) {
        clearTimeout(escape)
        open()
      }
      await gate
      inFlight -= 1
    },
    dispose() {
      clearTimeout(escape)
      open()
    },
  }
}

describe('startDocumentRun — the N pipelines are concurrent (RUN-1, AC-4, Core AC #9)', () => {
  it('RUN-1: all three uploads are in flight before any resolves, and before the first import is sent', async () => {
    const barrier = uploadBarrier(3, 250)
    const events: string[] = []
    const files = docFiles(['a.pdf', 'b.pdf', 'c.pdf'])

    const deps: DocumentPipelineDeps = {
      upload: async (file) => {
        events.push(`upload:start:${file.name}`)
        await barrier.arrive()
        events.push(`upload:end:${file.name}`)
        return `doc-${file.name}`
      },
      poll: async (documentId) => {
        events.push(`poll:${documentId}`)
        return { kind: 'succeeded', jobId: `job-${documentId}` }
      },
      importDocument: async (documentId) => {
        events.push(`import:start:${documentId}`)
        return report(`batch-${documentId}`)
      },
      onStage: () => {},
    }

    try {
      const outcomes = await startDocumentRun(files, deps)

      expect(events.length).toBeGreaterThan(0)
      expect(outcomes).toHaveLength(3)

      // (a) Three uploads genuinely overlapped. A `for (const f of files) await …` loop
      //     peaks at 1 and fails here.
      expect(barrier.peak).toBe(3)

      // (b) AC-4 restated literally: every upload had STARTED before the first import was
      //     sent. Non-empty on both sides before the comparison, so an impl that sends no
      //     import at all cannot pass by producing two empty sets.
      const uploadStarts = events.map((e, i) => [e, i] as const).filter(([e]) => e.startsWith('upload:start:'))
      const importStarts = events.map((e, i) => [e, i] as const).filter(([e]) => e.startsWith('import:start:'))
      expect(uploadStarts).toHaveLength(3)
      expect(importStarts).toHaveLength(3)
      expect(uploadStarts[uploadStarts.length - 1][1]).toBeLessThan(importStarts[0][1])
    } finally {
      barrier.dispose()
    }
  })
})

// --- RUN-4 -----------------------------------------------------------------

describe('startDocumentRun — one failure does not end the run (RUN-4, AC-5, AC-7)', () => {
  it('RUN-4: the middle document dead-letters; the other two still import and route to review with two batch ids', async () => {
    // A literal reason, not deadLetterRefusal's: the wording is POLL-3's spec, and what
    // RUN-4 pins is that the pipeline carries whatever the poll verdict said, verbatim.
    const DEAD_LETTER = 'docling: no text layer and no page render'
    const files = docFiles(['first.pdf', 'second.pdf', 'third.pdf'])
    const imported: string[] = []

    const deps: DocumentPipelineDeps = {
      upload: async (file) => `doc-${file.name}`,
      poll: async (documentId) =>
        documentId === 'doc-second.pdf'
          ? { kind: 'failed', reason: DEAD_LETTER }
          : { kind: 'succeeded', jobId: `job-${documentId}` },
      importDocument: async (documentId) => {
        imported.push(documentId)
        return report(`batch-${documentId}`)
      },
      onStage: () => {},
    }

    const outcomes = await startDocumentRun(files, deps)

    expect(outcomes).toHaveLength(3)
    expect(outcomes.map((o) => o.name)).toEqual(['first.pdf', 'second.pdf', 'third.pdf'])
    expect(outcomes.map((o) => o.outcome.kind)).toEqual(['imported', 'failed', 'imported'])

    const failed = outcomes[1].outcome
    expect(failed.kind).toBe('failed')
    if (failed.kind !== 'failed') throw new Error('unreachable — narrowed above')
    expect(failed.message).toContain(DEAD_LETTER)

    // Positive half first, so the absence below is not the only claim: the two survivors
    // WERE imported, and the dead-lettered one was not.
    expect(imported).toEqual(['doc-first.pdf', 'doc-third.pdf'])
    expect(imported).not.toContain('doc-second.pdf')

    // The run's landing, through the SHIPPED router — two batch ids, in run order.
    const runFiles: RunFile[] = outcomes.map((o) => ({ id: o.id, name: o.name, groupId: '', outcome: o.outcome }))
    const run: ImportRun = { files: runFiles, cursor: runFiles.length, status: 'finished' }
    expect(routeAfterRun(run, null)).toEqual({ kind: 'review', batchIds: ['batch-doc-first.pdf', 'batch-doc-third.pdf'] })
  })

  it('RUN-4b: a rejected pipeline settles that one file, never the whole run', async () => {
    const files = docFiles(['ok.pdf', 'boom.pdf'])
    const uploadSpy = vi.fn(async (file: File) => {
      if (file.name === 'boom.pdf') throw new Error('network: connection reset')
      return `doc-${file.name}`
    })
    const deps: DocumentPipelineDeps = {
      upload: uploadSpy,
      poll: async (documentId) => ({ kind: 'succeeded', jobId: `job-${documentId}` }),
      importDocument: async (documentId) => report(`batch-${documentId}`),
      onStage: () => {},
    }

    const outcomes = await startDocumentRun(files, deps)

    expect(uploadSpy).toHaveBeenCalledTimes(2)
    expect(outcomes.map((o) => o.outcome.kind)).toEqual(['imported', 'failed'])
    const failed = outcomes[1].outcome
    if (failed.kind !== 'failed') throw new Error('unreachable — asserted above')
    expect(failed.message).toContain('connection reset')
  })
})

// --- RUN-5..10 (EXTR-10-03, task-785) ---------------------------------------
//
// GREEN: startDocumentRun reports onStage(fileId, ...) for the import leg, each settle,
// and every failure arm; poll receives (documentId, fileId). Mutation-verified in the
// QA pass: each spec below fails on a real assertion when its own source line is
// mutated, never on a type or throw error.

describe('startDocumentRun — the import leg gets its own word (RUN-5, Core AC 3, task-785)', () => {
  it('RUN-5: onStage(id,{kind:"processing"}) fires before importDocument is invoked', async () => {
    const events: string[] = []
    const files = docFiles(['a.pdf'])
    const onStage = vi.fn((fileId: string, state: DocumentRowState) => {
      if (state.kind === 'processing') events.push(`stage:processing:${fileId}`)
    })
    const deps: DocumentPipelineDeps = {
      upload: async (file) => `doc-${file.name}`,
      poll: async (documentId) => ({ kind: 'succeeded', jobId: `job-${documentId}` }),
      importDocument: async (documentId) => {
        events.push(`import:start:${documentId}`)
        return report(`batch-${documentId}`)
      },
      onStage,
    }

    await startDocumentRun(files, deps)

    // Pin the count before trusting an index into it -- an onStage never called would
    // make indexOf's -1 vs -1 comparison below vacuously "pass".
    const processingCalls = onStage.mock.calls.filter(([, s]) => s.kind === 'processing')
    expect(processingCalls).toHaveLength(1)

    const stageIdx = events.indexOf(`stage:processing:${files[0].id}`)
    const importIdx = events.indexOf(`import:start:doc-${files[0].name}`)
    expect(stageIdx).toBeGreaterThanOrEqual(0)
    expect(importIdx).toBeGreaterThanOrEqual(0)
    expect(stageIdx).toBeLessThan(importIdx)
  })
})

describe('startDocumentRun — each settle is reported as it happens (RUN-6, Core AC 3, task-785)', () => {
  it('RUN-6: resolving the third import first reports f3 imported while f1/f2 are still unsettled', async () => {
    const files = docFiles(['a.pdf', 'b.pdf', 'c.pdf'])
    const resolvers = new Map<string, (r: ImportReport) => void>()
    const onStage = vi.fn()
    const deps: DocumentPipelineDeps = {
      upload: async (file) => `doc-${file.name}`,
      poll: async (documentId) => ({ kind: 'succeeded', jobId: `job-${documentId}` }),
      importDocument: (documentId) =>
        new Promise<ImportReport>((resolve) => {
          resolvers.set(documentId, resolve)
        }),
      onStage,
    }

    const runPromise = startDocumentRun(files, deps)

    // A macrotask boundary flushes every pending microtask, so by here all three
    // pipelines have genuinely reached importDocument -- not an assumption, a count.
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(resolvers.size).toBe(3)

    const f3 = files[2]
    resolvers.get('doc-c.pdf')!(report('batch-doc-c.pdf'))
    await new Promise((resolve) => setTimeout(resolve, 0))

    const importedCalls = onStage.mock.calls.filter(([, s]) => s.kind === 'imported')
    expect(importedCalls).toHaveLength(1)
    expect(importedCalls[0][0]).toBe(f3.id)

    const settledOthers = onStage.mock.calls.filter(
      ([id, s]) => id !== f3.id && (s.kind === 'imported' || s.kind === 'failed'),
    )
    expect(settledOthers).toHaveLength(0)

    // Let the run finish so no promise is left dangling past the test.
    resolvers.get('doc-a.pdf')!(report('batch-doc-a.pdf'))
    resolvers.get('doc-b.pdf')!(report('batch-doc-b.pdf'))
    await runPromise
  })
})

describe('startDocumentRun — a failed document reports its reason on its own id, verbatim (RUN-7, Core AC 4, task-785)', () => {
  it('RUN-7: only the middle file gets a failed stage; f1 and f3 still import', async () => {
    const REASON = 'R'
    const files = docFiles(['first.pdf', 'second.pdf', 'third.pdf'])
    const onStage = vi.fn()
    const imported: string[] = []
    const deps: DocumentPipelineDeps = {
      upload: async (file) => `doc-${file.name}`,
      poll: async (documentId) =>
        documentId === 'doc-second.pdf' ? { kind: 'failed', reason: REASON } : { kind: 'succeeded', jobId: `job-${documentId}` },
      importDocument: async (documentId) => {
        imported.push(documentId)
        return report(`batch-${documentId}`)
      },
      onStage,
    }

    await startDocumentRun(files, deps)

    const failedCalls = onStage.mock.calls.filter(([, s]) => s.kind === 'failed')
    expect(failedCalls).toHaveLength(1)
    expect(failedCalls[0]).toEqual([files[1].id, { kind: 'failed', reason: REASON }])

    // Positive half too: the two survivors actually imported.
    expect(imported).toHaveLength(2)
    expect(imported).toEqual(['doc-first.pdf', 'doc-third.pdf'])
  })
})

describe('startDocumentRun — a thrown transport settles that file only (RUN-8, Core AC 4, task-785)', () => {
  it('RUN-8: upload rejecting for one file reports failed on that file only, and the run resolves', async () => {
    const files = docFiles(['ok.pdf', 'boom.pdf'])
    const onStage = vi.fn()
    const uploadSpy = vi.fn(async (file: File) => {
      if (file.name === 'boom.pdf') throw new Error('network: connection reset')
      return `doc-${file.name}`
    })
    const deps: DocumentPipelineDeps = {
      upload: uploadSpy,
      poll: async (documentId) => ({ kind: 'succeeded', jobId: `job-${documentId}` }),
      importDocument: async (documentId) => report(`batch-${documentId}`),
      onStage,
    }

    const outcomes = await startDocumentRun(files, deps)

    expect(outcomes).toHaveLength(2)
    const failedCalls = onStage.mock.calls.filter(([, s]) => s.kind === 'failed')
    expect(failedCalls).toHaveLength(1)
    expect(failedCalls[0][0]).toBe(files[1].id)
    const stateArg = failedCalls[0][1] as DocumentRowState
    if (stateArg.kind !== 'failed') throw new Error('unreachable — filtered above')
    expect(stateArg.reason).toContain('connection reset')

    const otherFailed = onStage.mock.calls.filter(([id, s]) => id === files[0].id && s.kind === 'failed')
    expect(otherFailed).toHaveLength(0)
  })
})

describe('startDocumentRun — poll is told which file it is polling (RUN-9, Core AC 3, task-785)', () => {
  it("RUN-9: deps.poll receives each pipeline's own file id, and the three ids are distinct", async () => {
    const files = docFiles(['a.pdf', 'b.pdf', 'c.pdf'])
    const pollSpy = vi.fn(async (documentId: string, _fileId?: string) => ({ kind: 'succeeded' as const, jobId: `job-${documentId}` }))
    const deps: DocumentPipelineDeps = {
      upload: async (file) => `doc-${file.name}`,
      poll: pollSpy,
      importDocument: async (documentId) => report(`batch-${documentId}`),
      onStage: () => {},
    }

    await startDocumentRun(files, deps)

    expect(pollSpy).toHaveBeenCalledTimes(3)
    const fileIdArgs = pollSpy.mock.calls.map(([, fileId]) => fileId)
    expect(fileIdArgs).toHaveLength(3)
    expect(new Set(fileIdArgs).size).toBe(3)
    expect(new Set(fileIdArgs)).toEqual(new Set(files.map((f) => f.id)))
  })
})

describe("startDocumentRun — the imported stage carries the server's own ready_invoices (RUN-10, Core AC 4, task-785)", () => {
  it('RUN-10: a duplicate-shaped report (ready_invoices: 0) reports imported/count:0, unmodified', async () => {
    const files = docFiles(['dup.pdf'])
    const onStage = vi.fn()
    const deps: DocumentPipelineDeps = {
      upload: async (file) => `doc-${file.name}`,
      poll: async (documentId) => ({ kind: 'succeeded', jobId: `job-${documentId}` }),
      importDocument: async (documentId) => report(`batch-${documentId}`, 0),
      onStage,
    }

    await startDocumentRun(files, deps)

    const importedCalls = onStage.mock.calls.filter(([, s]) => s.kind === 'imported')
    expect(importedCalls).toHaveLength(1)
    expect(importedCalls[0]).toEqual([files[0].id, { kind: 'imported', count: 0 }])
  })
})

// --- RUN-11..15 (task-785 QA pass) -------------------------------------------
//
// Adversarial coverage the RED phase (RUN-5..10) did not exercise.

describe('startDocumentRun — the happy-path sequence is exact and total, not just present (RUN-11, Core AC 3)', () => {
  it('RUN-11: one succeeding file reports exactly [processing, imported], nothing before or after', async () => {
    const files = docFiles(['a.pdf'])
    const stages: string[] = []
    const onStage = vi.fn((_fileId: string, state: DocumentRowState) => stages.push(state.kind))
    const deps: DocumentPipelineDeps = {
      upload: async (file) => `doc-${file.name}`,
      poll: async (documentId) => ({ kind: 'succeeded', jobId: `job-${documentId}` }),
      importDocument: async (documentId) => report(`batch-${documentId}`),
      onStage,
    }

    await startDocumentRun(files, deps)

    expect(stages).toEqual(['processing', 'imported'])
  })
})

describe('startDocumentRun — importDocument itself throwing is a third, distinct failure route (RUN-12, Core AC 4)', () => {
  it('RUN-12: importDocument rejecting (not poll, not upload) reports failed with messageOf(err), same string on the outcome', async () => {
    const MSG = 'POST /v1/imports: 502 bad gateway'
    const files = docFiles(['a.pdf'])
    const onStage = vi.fn()
    const deps: DocumentPipelineDeps = {
      upload: async (file) => `doc-${file.name}`,
      poll: async (documentId) => ({ kind: 'succeeded', jobId: `job-${documentId}` }),
      importDocument: async () => {
        throw new Error(MSG)
      },
      onStage,
    }

    const outcomes = await startDocumentRun(files, deps)

    const failedCalls = onStage.mock.calls.filter(([, s]) => s.kind === 'failed')
    expect(failedCalls).toHaveLength(1)
    const [fileId, state] = failedCalls[0]
    expect(fileId).toBe(files[0].id)
    if (state.kind !== 'failed') throw new Error('unreachable — filtered above')
    expect(state.reason).toBe(MSG)

    expect(outcomes).toHaveLength(1)
    const outcome = outcomes[0].outcome
    if (outcome.kind !== 'failed') throw new Error('unreachable — importDocument threw')
    expect(outcome.message).toBe(state.reason)

    // The processing report already fired before the import leg blew up -- it is not
    // rolled back, only the eventual outcome is failed.
    expect(onStage.mock.calls.filter(([, s]) => s.kind === 'processing')).toHaveLength(1)
  })
})

describe('startDocumentRun — the reported reason and the returned outcome never disagree (RUN-13, D-14 invariant)', () => {
  it('RUN-13: a poll-failed verdict, an upload throw, and an import throw each carry one identical string on both sides', async () => {
    type Case = { name: string; build: (msg: string) => Omit<DocumentPipelineDeps, 'onStage'> }
    const cases: Case[] = [
      {
        name: 'poll-failed',
        build: (msg) => ({
          upload: async (file) => `doc-${file.name}`,
          poll: async () => ({ kind: 'failed', reason: msg }),
          importDocument: async (documentId) => report(`batch-${documentId}`),
        }),
      },
      {
        name: 'upload-throw',
        build: (msg) => ({
          upload: async () => {
            throw new Error(msg)
          },
          poll: async (documentId) => ({ kind: 'succeeded', jobId: `job-${documentId}` }),
          importDocument: async (documentId) => report(`batch-${documentId}`),
        }),
      },
      {
        name: 'import-throw',
        build: (msg) => ({
          upload: async (file) => `doc-${file.name}`,
          poll: async (documentId) => ({ kind: 'succeeded', jobId: `job-${documentId}` }),
          importDocument: async () => {
            throw new Error(msg)
          },
        }),
      },
    ]
    expect(cases).toHaveLength(3)

    for (const { name, build } of cases) {
      const msg = `${name}: boom`
      const files = docFiles(['a.pdf'])
      const onStage = vi.fn()
      const outcomes = await startDocumentRun(files, { ...build(msg), onStage })

      const failedCalls = onStage.mock.calls.filter(([, s]) => s.kind === 'failed')
      expect(failedCalls, name).toHaveLength(1)
      const [, state] = failedCalls[0]
      if (state.kind !== 'failed') throw new Error(`unreachable — ${name} is a failure case`)

      const outcome = outcomes[0].outcome
      if (outcome.kind !== 'failed') throw new Error(`unreachable — ${name} is a failure case`)

      expect(state.reason, name).toBe(outcome.message)
    }
  })
})

describe('startDocumentRun — every file failing still resolves once, each on its own id (RUN-14, AC-4/AC-5)', () => {
  it('RUN-14: three failing files each report their own failed stage; Promise.all resolves; outcomes stay length 3 in file order', async () => {
    const files = docFiles(['a.pdf', 'b.pdf', 'c.pdf'])
    const onStage = vi.fn()
    const deps: DocumentPipelineDeps = {
      upload: async (file) => `doc-${file.name}`,
      poll: async (documentId) => ({ kind: 'failed', reason: `${documentId}: dead-lettered` }),
      importDocument: async (documentId) => report(`batch-${documentId}`),
      onStage,
    }

    const outcomes = await startDocumentRun(files, deps)

    expect(outcomes).toHaveLength(3)
    expect(outcomes.map((o) => o.name)).toEqual(['a.pdf', 'b.pdf', 'c.pdf'])
    expect(outcomes.every((o) => o.outcome.kind === 'failed')).toBe(true)

    const failedCalls = onStage.mock.calls.filter(([, s]) => s.kind === 'failed')
    expect(failedCalls).toHaveLength(3)
    expect(new Set(failedCalls.map(([id]) => id))).toEqual(new Set(files.map((f) => f.id)))
  })
})

describe('startDocumentRun — a throwing onStage is swallowed by that pipeline’s own catch, not the run (RUN-15, AC-4 boundary)', () => {
  it('RUN-15: onStage throwing while reporting one file imported turns THAT outcome to failed; the sibling is untouched', async () => {
    const files = docFiles(['ok.pdf', 'boom.pdf'])
    const onStage = vi.fn((fileId: string, state: DocumentRowState) => {
      if (fileId === files[1].id && state.kind === 'imported') {
        throw new Error('setState on an unmounted component')
      }
    })
    const deps: DocumentPipelineDeps = {
      upload: async (file) => `doc-${file.name}`,
      poll: async (documentId) => ({ kind: 'succeeded', jobId: `job-${documentId}` }),
      importDocument: async (documentId) => report(`batch-${documentId}`),
      onStage,
    }

    const outcomes = await startDocumentRun(files, deps)

    expect(outcomes).toHaveLength(2)
    expect(outcomes[0].outcome.kind).toBe('imported')

    // Pinned, not endorsed: startDocumentRun's per-pipeline try/catch has no way to tell
    // "the import already succeeded, onStage merely misbehaved" apart from "the import
    // failed" -- both land in the same catch. The document WAS imported server-side; the
    // outcome the caller sees says otherwise. Flagged in the QA report, not fixed here --
    // no AC of this subtask asks for onStage to be treated as fallible.
    expect(outcomes[1].outcome.kind).toBe('failed')
    const boomOutcome = outcomes[1].outcome
    if (boomOutcome.kind !== 'failed') throw new Error('unreachable — asserted above')
    expect(boomOutcome.message).toBe('setState on an unmounted component')
  })
})

// --- STAGE-1..6 (EXTR-10-01, task-783) --------------------------------------

describe('stageOf — classifies every state in the CHECK constraint, and only those (STAGE-1, Core AC 1/2)', () => {
  // migrations/20260827084025_extraction_jobs.sql:20-21's five values, literal so a
  // dropped case shrinks this list (and the length assertion below) rather than passing
  // silently.
  const CHECK_STATES = ['queued', 'extracting', 'succeeded', 'failed', 'dead_lettered'] as const

  it('STAGE-1: stageOf classifies every state in the CHECK constraint, and only those', () => {
    expect(CHECK_STATES).toHaveLength(5)

    const TABLE: readonly [string, DocumentRowState | null][] = [
      ['queued', { kind: 'queued' }],
      ['extracting', { kind: 'reading' }],
      ['succeeded', null],
      ['failed', { kind: 'retrying' }],
      ['dead_lettered', null],
    ]
    // The table's states are exactly CHECK_STATES, same order -- so this spec cannot pass
    // by covering some other five-item list.
    expect(TABLE.map(([state]) => state)).toEqual(CHECK_STATES)

    for (const [state, expected] of TABLE) {
      expect(stageOf(job({ state })), `state ${state}`).toEqual(expected)
    }

    // Outside the CHECK set entirely -- a fall-through to a default word is what this guards.
    expect(stageOf(job({ state: 'weird' }))).toBeNull()
  })
})

describe('stageOf — no extraction_jobs row yet is QUEUED, never blank (STAGE-2, Core AC 1)', () => {
  it('STAGE-2: no extraction_jobs row yet is QUEUED, never blank', () => {
    expect(stageOf(null)).toEqual({ kind: 'queued' })
  })
})

describe('documentRunRows — total over the run and keeps file order (STAGE-3, Core AC 3)', () => {
  it('STAGE-3: documentRunRows is total over the run and keeps file order', () => {
    const files: RunFile[] = [runFile('f1', 'a.pdf'), runFile('f2', 'b.pdf'), runFile('f3', 'c.pdf')]
    const run: ImportRun = { files, cursor: 0, status: 'running' }
    const stages: Record<string, DocumentRowState> = { f2: { kind: 'reading' } }

    const rows = documentRunRows(run, stages)

    expect(rows).toHaveLength(3)
    expect(rows.map((r) => r.name)).toEqual(['a.pdf', 'b.pdf', 'c.pdf'])
    expect(rows[0]).toEqual({ name: 'a.pdf', kind: 'queued' })
    expect(rows[1]).toEqual({ name: 'b.pdf', kind: 'reading' })
    expect(rows[2]).toEqual({ name: 'c.pdf', kind: 'queued' })
  })
})

describe('documentRunRows — rows join on file id, never on filename (STAGE-4, Core AC 3)', () => {
  it('STAGE-4: rows join on file id, never on filename', () => {
    // Same name, different ids -- the exact byte-identical-pick trap importRun.ts:77-78
    // records for attachDocumentIds; documentRunRows must not repeat it.
    const files: RunFile[] = [runFile('f1', 'dup.pdf'), runFile('f2', 'dup.pdf')]
    const run: ImportRun = { files, cursor: 0, status: 'running' }
    const stages: Record<string, DocumentRowState> = { f1: { kind: 'reading' }, f2: { kind: 'retrying' } }

    const rows = documentRunRows(run, stages)

    expect(rows).toHaveLength(2)
    expect(rows).toEqual([
      { name: 'dup.pdf', kind: 'reading' },
      { name: 'dup.pdf', kind: 'retrying' },
    ])
  })
})

describe('documentRunRows — the map is the whole truth while the run is live (STAGE-5, Core AC 1)', () => {
  it('STAGE-5: the map is the whole truth while the run is live', () => {
    // outcome stays 'pending' -- runReducer's own settle hasn't happened -- while the
    // stage map already says imported. An impl reading f.outcome first shows QUEUED for a
    // document that has already settled: today's exact defect (App.tsx:945-951).
    const files: RunFile[] = [runFile('f1', 'a.pdf')]
    const run: ImportRun = { files, cursor: 0, status: 'running' }
    const stages: Record<string, DocumentRowState> = { f1: { kind: 'imported', count: 2 } }

    const rows = documentRunRows(run, stages)

    expect(rows).toHaveLength(1)
    expect(rows[0]).toEqual({ name: 'a.pdf', kind: 'imported', count: 2 })
  })
})

describe('documentRunRows — the widened union adds no row property (STAGE-6, Core AC 2)', () => {
  it('STAGE-6: the widened union adds no row property', () => {
    const STATES: readonly DocumentRowState[] = [
      { kind: 'queued' },
      { kind: 'reading' },
      { kind: 'retrying' },
      { kind: 'processing' },
      { kind: 'imported', count: 1 },
      { kind: 'failed', reason: 'boom' },
    ]
    expect(STATES).toHaveLength(6)

    const files: RunFile[] = STATES.map((_, i) => runFile(`f${i + 1}`, `file${i + 1}.pdf`))
    const stages: Record<string, DocumentRowState> = Object.fromEntries(STATES.map((s, i) => [`f${i + 1}`, s]))
    const run: ImportRun = { files, cursor: 0, status: 'running' }

    const rows = documentRunRows(run, stages)
    expect(rows).toHaveLength(6)

    // BULK-05-12's own forbidden set (importRun.test.ts:683-695) -- a stage carrying a
    // payload reopens the honesty hole the card exists to close.
    const FORBIDDEN = [
      'stage',
      'percent',
      'loaded',
      'total',
      'rows',
      'rowsRead',
      'bytes',
      'ruleSetVersion',
      'rule_set_version',
    ]
    for (const row of rows) {
      for (const prop of FORBIDDEN) {
        expect(row, `${JSON.stringify(row)} should not carry "${prop}"`).not.toHaveProperty(prop)
      }
    }
  })
})

// --- STAGE adversarial coverage (task-783 QA pass) --------------------------
//
// Added during Mode B verification, past what the RED-phase STAGE-1..6 specs cover.

describe('stageOf — null for inputs outside the CHECK set, including prototype-name strings (STAGE-ADV-1, AC-1)', () => {
  it('returns null for the empty string, whitespace, wrong case, and JS prototype keys', () => {
    const INPUTS = ['', ' ', 'EXTRACTING', 'constructor', '__proto__', 'toString']
    expect(INPUTS).toHaveLength(6)
    for (const state of INPUTS) {
      expect(stageOf(job({ state })), `state ${JSON.stringify(state)}`).toBeNull()
    }
  })
})

describe('documentRunRows — a zero-file run (STAGE-ADV-2, AC-2)', () => {
  it('returns an empty array, not undefined or a thrown error', () => {
    const run: ImportRun = { files: [], cursor: 0, status: 'idle' }
    const rows = documentRunRows(run, {})
    expect(rows).toHaveLength(0)
    expect(rows).toEqual([])
  })
})

describe('documentRunRows — a stale stage entry outlives its file (STAGE-ADV-3, AC-2)', () => {
  it('ignores a stages entry whose id is not among run.files, and does not disturb the file that is', () => {
    const files: RunFile[] = [runFile('f1', 'a.pdf')]
    const run: ImportRun = { files, cursor: 0, status: 'running' }
    // f-ghost: a leftover from an earlier run/picker session -- must produce no row of
    // its own and must not leak onto f1's row.
    const stages: Record<string, DocumentRowState> = {
      f1: { kind: 'reading' },
      'f-ghost': { kind: 'failed', reason: 'stale' },
    }

    const rows = documentRunRows(run, stages)

    expect(rows).toHaveLength(1)
    expect(rows).toEqual([{ name: 'a.pdf', kind: 'reading' }])
  })
})

describe('documentRunRows — file order wins over id order (STAGE-ADV-4, AC-2)', () => {
  it('preserves run.files order even when ids are not sorted', () => {
    // ids deliberately NOT sorted, so a sort-by-id bug is distinguishable from correct
    // insertion-order behaviour -- STAGE-3's own files happen to be id-sorted already
    // and cannot tell the two apart.
    const files: RunFile[] = [runFile('f3', 'third.pdf'), runFile('f1', 'first.pdf'), runFile('f2', 'second.pdf')]
    const run: ImportRun = { files, cursor: 0, status: 'running' }
    const stages: Record<string, DocumentRowState> = {
      f1: { kind: 'reading' },
      f2: { kind: 'retrying' },
      f3: { kind: 'imported', count: 1 },
    }

    const rows = documentRunRows(run, stages)

    expect(rows).toHaveLength(3)
    expect(rows.map((r) => r.name)).toEqual(['third.pdf', 'first.pdf', 'second.pdf'])
    expect(rows).toEqual([
      { name: 'third.pdf', kind: 'imported', count: 1 },
      { name: 'first.pdf', kind: 'reading' },
      { name: 'second.pdf', kind: 'retrying' },
    ])
  })
})

describe('documentRunRows — a RunFile.id equal to a JS prototype key (STAGE-ADV-5)', () => {
  it('"constructor" and "__proto__" as ids have no map entry and still render queued', () => {
    // stages[f.id] on a plain object would resolve 'constructor'/'__proto__' through
    // Object.prototype -- a function, so `?? { kind: 'queued' }` never fires and the
    // row silently loses `kind`. Object.hasOwn gates the lookup to own properties only,
    // so a file with no map entry renders queued like any other (AC-4). See documentRun.ts:82.
    const files: RunFile[] = [runFile('constructor', 'evil.pdf'), runFile('__proto__', 'evil2.pdf')]
    const run: ImportRun = { files, cursor: 0, status: 'running' }
    const stages = {} as Record<string, DocumentRowState>

    const rows = documentRunRows(run, stages)

    expect(rows).toHaveLength(2)
    expect(rows).toEqual([
      { name: 'evil.pdf', kind: 'queued' },
      { name: 'evil2.pdf', kind: 'queued' },
    ])
  })
})

// --- POLL-5..10 (EXTR-10-02, task-784) --------------------------------------
//
// pollUntilSettled's stub throws `new Error('EXTR-10-02: not implemented — …')`
// (documentRun.ts), so every spec below is RED on that throw -- except POLL-6, which never
// calls pollUntilSettled and fails on a real assertion: EXTRACTION_POLL_INTERVAL_MS is
// still exported today.

describe('pollUntilSettled — the document poll runs at the app’s one cadence (POLL-5, AC-3)', () => {
  it('POLL-5: sleep is called with LIVE_POLL_MS, the imported binding, on every non-terminal tick', async () => {
    const ticks: ExtractionJob[][] = [[], [job({ state: 'extracting' })], [job({ state: 'succeeded' })]]
    let call = 0
    const getJobs = vi.fn(async () => ticks[call++])
    const onStage = vi.fn()
    const sleep = vi.fn(async (_ms: number) => {})
    const now = vi.fn(() => 0)

    const verdict = await pollUntilSettled('doc-1', { getJobs, onStage, sleep, now })

    // Two non-terminal ticks (queued, then extracting) each sleep once; the third,
    // terminal tick returns without a further sleep.
    expect(sleep).toHaveBeenCalledTimes(2)
    for (const [ms] of sleep.mock.calls) {
      expect(ms).toBe(LIVE_POLL_MS)
    }
    expect(verdict).toEqual({ kind: 'succeeded', jobId: 'job-1' })
  })
})

describe('documentRun module — there is no second interval constant (POLL-6, AC-4)', () => {
  it('POLL-6: EXTRACTION_POLL_INTERVAL_MS is gone from the namespace, while EXTRACTION_POLL_BUDGET_MS -- the control needle -- still is', () => {
    // Control needle first: a module that exported neither constant would make the
    // absence below meaningless.
    expect('EXTRACTION_POLL_BUDGET_MS' in documentRunModule).toBe(true)
    expect('EXTRACTION_POLL_INTERVAL_MS' in documentRunModule).toBe(false)
  })
})

describe('pollUntilSettled — every tick reports the stage it read (POLL-7, Core AC 1/2)', () => {
  it('POLL-7: onStage sees queued then reading, in order, and nothing after the terminal tick', async () => {
    const ticks: ExtractionJob[][] = [[], [job({ state: 'extracting' })], [job({ state: 'succeeded' })]]
    let call = 0
    const getJobs = vi.fn(async () => ticks[call++])
    const onStage = vi.fn()
    const sleep = vi.fn(async () => {})
    const now = vi.fn(() => 0)

    const verdict = await pollUntilSettled('doc-1', { getJobs, onStage, sleep, now })

    expect(getJobs).toHaveBeenCalledTimes(3)
    expect(onStage).toHaveBeenCalledTimes(2)
    expect(onStage.mock.calls.map(([s]) => s)).toEqual([{ kind: 'queued' }, { kind: 'reading' }])
    expect(verdict).toEqual({ kind: 'succeeded', jobId: 'job-1' })
  })
})

describe('pollUntilSettled — a terminal state reports no word (POLL-8, Core AC 2/5)', () => {
  it('POLL-8: dead_lettered on the first tick never calls onStage, and the reason is deadLetterRefusal verbatim', async () => {
    const LAST_ERROR = 'boom'
    const getJobs = vi.fn(async () => [job({ state: 'dead_lettered', last_error: LAST_ERROR })])
    const onStage = vi.fn()
    const sleep = vi.fn(async () => {})
    const now = vi.fn(() => 0)

    const verdict = await pollUntilSettled('doc-1', { getJobs, onStage, sleep, now })

    expect(getJobs).toHaveBeenCalledTimes(1)
    expect(onStage).toHaveBeenCalledTimes(0)
    expect(sleep).not.toHaveBeenCalled()
    expect(verdict.kind).toBe('failed')
    if (verdict.kind !== 'failed') throw new Error('unreachable — narrowed above')
    // Retargeted for the widened signature. The fixture's failure_kind is null, so the
    // expected sentence is the unknown-kind one -- still the verbatim relay this row claims.
    expect(verdict.reason).toBe(refuse(null, LAST_ERROR))
  })
})

describe('pollUntilSettled — the budget still ends the loop (POLL-9, AC-5)', () => {
  it('POLL-9: an extracting job past EXTRACTION_POLL_BUDGET_MS fails with the budget reason, and the last stage reported was reading', async () => {
    const getJobs = vi.fn(async () => [job({ state: 'extracting' })])
    const onStage = vi.fn()
    const sleep = vi.fn(async () => {})
    // First call is startedAt; the second -- this tick's elapsed calc -- has already
    // crossed the budget, so this pins the elapsed clock rather than a real wall-clock wait.
    const nowValues = [0, EXTRACTION_POLL_BUDGET_MS + 1]
    let nowCall = 0
    const now = vi.fn(() => nowValues[nowCall++])

    const verdict = await pollUntilSettled('doc-1', { getJobs, onStage, sleep, now })

    expect(getJobs).toHaveBeenCalledTimes(1)
    expect(sleep).not.toHaveBeenCalled()
    expect(onStage).toHaveBeenCalledTimes(1)
    expect(onStage.mock.calls[0][0]).toEqual({ kind: 'reading' })
    expect(verdict).toEqual({ kind: 'failed', reason: pollBudgetRefusal() })
  })
})

describe('pollUntilSettled — a getJobs rejection is not swallowed (POLL-10, Core AC 6)', () => {
  it('POLL-10: pollUntilSettled rejects with the same sentinel getJobs rejected with', async () => {
    const E = new Error('network: connection reset')
    const getJobs = vi.fn(async () => {
      throw E
    })
    const onStage = vi.fn()
    const sleep = vi.fn(async () => {})
    const now = vi.fn(() => 0)

    await expect(pollUntilSettled('doc-1', { getJobs, onStage, sleep, now })).rejects.toBe(E)
    expect(onStage).not.toHaveBeenCalled()
  })
})

// --- POLL-ADV (EXTR-10-02, task-784 QA) -------------------------------------
//
// Adversarial coverage the RED phase (POLL-5..10) did not exercise: the retry arm of the
// story's own `(failed -> extracting)*` state sequence, undeclared dedup behaviour, the
// pre-worker window, and newestJob's tie contract.

describe('pollUntilSettled — the retry cycle reports every arm (POLL-ADV-1, Core AC 1/2)', () => {
  it('POLL-ADV-1: extracting -> failed -> extracting -> succeeded reports reading, retrying, reading, and nothing on the terminal tick', async () => {
    const ticks: ExtractionJob[][] = [
      [job({ state: 'extracting' })],
      [job({ state: 'failed' })],
      [job({ state: 'extracting' })],
      [job({ state: 'succeeded' })],
    ]
    let call = 0
    const getJobs = vi.fn(async () => ticks[call++])
    const onStage = vi.fn()
    const sleep = vi.fn(async () => {})
    const now = vi.fn(() => 0)

    const verdict = await pollUntilSettled('doc-1', { getJobs, onStage, sleep, now })

    expect(getJobs).toHaveBeenCalledTimes(4)
    expect(onStage).toHaveBeenCalledTimes(3)
    expect(onStage.mock.calls.map(([s]) => s)).toEqual([{ kind: 'reading' }, { kind: 'retrying' }, { kind: 'reading' }])
    // Three waiting ticks sleep once each; the fourth, terminal tick returns without one.
    expect(sleep).toHaveBeenCalledTimes(3)
    expect(verdict).toEqual({ kind: 'succeeded', jobId: 'job-1' })
  })
})

describe('pollUntilSettled — onStage is not deduplicated across identical ticks (POLL-ADV-2, Core AC 1/2)', () => {
  it('POLL-ADV-2: two consecutive extracting ticks fire "reading" twice, not once', async () => {
    const ticks: ExtractionJob[][] = [[job({ state: 'extracting' })], [job({ state: 'extracting' })], [job({ state: 'succeeded' })]]
    let call = 0
    const getJobs = vi.fn(async () => ticks[call++])
    const onStage = vi.fn()
    const sleep = vi.fn(async () => {})
    const now = vi.fn(() => 0)

    await pollUntilSettled('doc-1', { getJobs, onStage, sleep, now })

    // Pinned deliberately: documentRun.ts:79 says "every tick", not "on change" -- a
    // future de-dup would be a silent behaviour change a React re-render (EXTR-10-04)
    // could depend on either way.
    expect(onStage).toHaveBeenCalledTimes(2)
    expect(onStage.mock.calls.map(([s]) => s)).toEqual([{ kind: 'reading' }, { kind: 'reading' }])
  })
})

describe('pollUntilSettled — no job row yet reads as queued until the budget ends it (POLL-ADV-3, AC-5)', () => {
  it('POLL-ADV-3: getJobs returning [] on every tick reports queued each time and still expires on budget, never crashing', async () => {
    const getJobs = vi.fn(async (): Promise<ExtractionJob[]> => [])
    const onStage = vi.fn()
    const sleep = vi.fn(async () => {})
    // now() calls: startedAt, then one per tick's elapsed check.
    const nowValues = [0, 30_000, 60_000, 90_000, EXTRACTION_POLL_BUDGET_MS + 1]
    let nowCall = 0
    const now = vi.fn(() => nowValues[nowCall++])

    const verdict = await pollUntilSettled('doc-1', { getJobs, onStage, sleep, now })

    expect(getJobs).toHaveBeenCalledTimes(4)
    expect(onStage).toHaveBeenCalledTimes(4)
    for (const [s] of onStage.mock.calls) {
      expect(s).toEqual({ kind: 'queued' })
    }
    expect(sleep).toHaveBeenCalledTimes(3)
    expect(verdict).toEqual({ kind: 'failed', reason: pollBudgetRefusal() })
  })
})

describe('newestJob — a created_at tie is broken by first occurrence, not last (POLL-ADV-4, AC-5)', () => {
  it('POLL-ADV-4: two jobs sharing one created_at resolve to whichever the array names first', () => {
    const first = job({ id: 'job-a', created_at: '2026-08-30T10:00:00Z' })
    const second = job({ id: 'job-b', created_at: '2026-08-30T10:00:00Z' })

    expect([first, second]).toHaveLength(2)
    expect(newestJob([first, second])?.id).toBe('job-a')
    expect(newestJob([second, first])?.id).toBe('job-b')
  })
})

// ==========================================================================================
// EXTR-15-04 (task-830) — RED specs, Mode A. Six terminal sentences that say what was tried
// and what to do next. Written BEFORE deadLetterRefusal/pollBudgetRefusal are widened; every
// row here fails on its own assertion against the shipped one-sentence-fits-all bodies.
// ==========================================================================================

// The CHECK set of migrations/20260904154655_extraction_jobs_failure_kind.sql, in the order
// internal/extraction/audit.go declares it. Floored against that migration in TS15-1, so the
// list cannot drift from the column.
const KINDS = [
  'document_unavailable',
  'pages_not_rendered',
  'page_rows_not_written',
  'extract_failed',
  'text_not_read',
] as const

const NO_KIND = '<null>'

/** The six sentences, keyed by the kind that produced each. `null` is the sixth. */
function terminalSentences(): Record<string, string> {
  const out: Record<string, string> = {}
  for (const k of KINDS) out[k] = refuse(k, null)
  out[NO_KIND] = refuse(null, null)
  return out
}

/** The seven strings AC-2 and AC-3 range over: the six plus the budget refusal. */
function allRefusals(): Record<string, string> {
  return { ...terminalSentences(), '<budget>': pollBudgetRefusal() }
}

// AC-2's "names a next action", as two independent properties rather than prose: the sentence
// offers MANUAL work and says the person ENTERS something. Both matchers are proved
// non-vacuous against a control in TS15-2 before any absence is read off them.
const MANUAL = /\bmanual(?:ly)?\b/i
const ENTRY = /\b(?:enter|enters|entered|entering|entry|type|typing|keying|by hand)\b/i

const DEAD_PROMISE = 'open it again later'

function readRepoFile(rel: string, needle: string): string {
  const src = readFileSync(path.join(process.cwd(), rel), 'utf8')
  // Planted needle: a moved, renamed or gutted file must fail loudly rather than yield ''
  // and make every absence below vacuous.
  expect(src.length, `the scan read nothing from ${rel}`).toBeGreaterThan(500)
  expect(src, `the scan ran over a moved or renamed ${rel}`).toContain(needle)
  return src
}

/** The View union's members, read from types.ts rather than re-typed here. */
function viewMembers(): string[] {
  const src = readRepoFile('src/types.ts', 'export type View =')
  const line = /export type View =([^\n]*)/.exec(src)
  expect(line, 'no View union found — the screen-name absence below would be vacuous').not.toBeNull()
  return Array.from((line as RegExpExecArray)[1].matchAll(/'([a-z_]+)'/g), (m) => m[1])
}

/**
 * The member this sentence names as a destination, or null. Two shapes: "<member> screen" and
 * "go to/open/return to <member>". Deliberately NOT a bare substring test — 'create',
 * 'detail' and 'reports' are ordinary English words and banning them outright would push the
 * implementer into contorted prose without catching one real dead promise.
 */
function namesAScreen(sentence: string, members: readonly string[]): string | null {
  for (const m of members) {
    if (new RegExp(String.raw`\b${m}\b\s+(?:screen|page|tab|view|section)\b`, 'i').test(sentence)) return m
    if (new RegExp(String.raw`\b(?:go to|open|return to|visit|navigate to|back to)\s+(?:the\s+)?${m}\b`, 'i').test(sentence)) return m
  }
  return null
}

describe('deadLetterRefusal — one sentence per failure kind (TS15-1, AC-1)', () => {
  // Its own row so it cannot mask the sentence assertions below it: the signature and the
  // prose are two separate ways for this to be wrong, and both must be measurable.
  it('TS15-1: deadLetterRefusal takes the kind and the error', () => {
    // Function.length is the declared parameter count. Without this row nothing here can
    // tell a widened function from one that silently ignores its second argument.
    // NOTE for whoever writes the body: a DEFAULT or REST parameter is not counted, so
    // `(failureKind, lastError = null)` reads 1 here and keeps this red. Declare both plain.
    expect(deadLetterRefusal.length, 'deadLetterRefusal still takes one argument').toBe(2)
  })

  it('TS15-1: the kind list is the migration’s CHECK set, and it yields six distinct sentences that echo no identifier', () => {
    // Floor first: an empty or drifted kind list would make every row below vacuous.
    const sql = readRepoFile('../../migrations/20260904154655_extraction_jobs_failure_kind.sql', 'ADD COLUMN failure_kind')
    const declared = Array.from(sql.matchAll(/'([a-z_]+)'/g), (m) => m[1])
    expect(declared.length, 'the migration’s CHECK named no kind').toBe(KINDS.length)
    expect([...declared].sort(), 'the kind list has drifted from the column’s CHECK set').toEqual([...KINDS].sort())

    const all = terminalSentences()
    expect(Object.keys(all), 'six sentences: five kinds plus the unknown/absent one').toHaveLength(6)
    expect(new Set(Object.values(all)).size, 'two kinds share one sentence').toBe(6)

    for (const [key, sentence] of Object.entries(all)) {
      expect(sentence.length, `${key} returned nothing`).toBeGreaterThan(40)
      // The refusal is prose, not a relayed identifier. This is what reds a body that
      // interpolates the kind token and calls it a sentence — the shipped shape.
      expect(sentence, `${key}: the sentence echoes a snake_case identifier`).not.toMatch(/[a-z]_[a-z]/)
      for (const k of KINDS) {
        expect(sentence.includes(k), `${key}: the sentence names the raw kind ${k}`).toBe(false)
      }
    }
  })
})

describe('every refusal names manual entry as the next step (TS15-2, AC-2)', () => {
  it('TS15-2: the two matchers catch a control and reject a bare failure report', () => {
    // Non-vacuity: without this, a matcher broken into never-matching would turn every row
    // below into a silent pass on the day someone edits it.
    const control = 'Nothing was read from it — enter this invoice manually to carry on.'
    expect(MANUAL.test(control), 'the manual matcher does not match its own control').toBe(true)
    expect(ENTRY.test(control), 'the entry matcher does not match its own control').toBe(true)
    expect(MANUAL.test('Extraction failed for this document — boom')).toBe(false)
  })

  it.each(Object.keys(allRefusals()))('TS15-2: %s offers manual entry', (key) => {
    const sentence = allRefusals()[key]
    expect(sentence.length, `${key} returned nothing`).toBeGreaterThan(0)
    expect(sentence, `${key} does not offer manual work`).toMatch(MANUAL)
    expect(sentence, `${key} does not say what the person enters`).toMatch(ENTRY)
  })
})

describe('no refusal promises a screen that does not exist (TS15-3, AC-3)', () => {
  it('TS15-3: the View union carries no documents member — the reason the old promise was dead', () => {
    const members = viewMembers()
    // Two floors on the parse and one on the premise. If `documents` is ever added to View
    // this row fails, and AC-3's whole rationale needs revisiting rather than silently
    // outliving it.
    expect(members.length, 'the View union parsed to nothing').toBeGreaterThan(10)
    expect(members, 'the View parse lost a known member').toEqual(expect.arrayContaining(['dashboard', 'extraction']))
    expect(members, 'View now has a documents member — "open it again later" may no longer be a dead promise').not.toContain('documents')
  })

  it('TS15-3: namesAScreen catches a real destination and clears an ordinary sentence', () => {
    const members = viewMembers()
    expect(namesAScreen('Open it again on the extraction screen.', members), 'the screen matcher is blind').toBe('extraction')
    expect(namesAScreen('Go to the dashboard.', members), 'the screen matcher is blind to a navigation verb').toBe('dashboard')
    expect(namesAScreen('The reader opened the file and got no text — enter the invoice manually.', members)).toBeNull()
  })

  it.each(Object.keys(allRefusals()))('TS15-3: %s promises no destination', (key) => {
    const sentence = allRefusals()[key]
    expect(sentence.toLowerCase(), `${key} still promises "${DEAD_PROMISE}"`).not.toContain(DEAD_PROMISE)
    expect(sentence, `${key} carries a route`).not.toMatch(/[/#]/)
    expect(namesAScreen(sentence, viewMembers()), `${key} names a screen`).toBeNull()
  })
})

describe('extract_failed carries last_error as a subordinate clause (TS15-4, AC-4)', () => {
  const KIND = 'extract_failed'

  it('TS15-4: a non-empty last_error appears verbatim, and empty/null take the shipped fallback', () => {
    const FALLBACK = 'the server gave no reason'

    const withError = refuse(KIND, 'boom')
    expect(withError, 'the server’s reason was dropped').toContain('boom')

    const withEmpty = refuse(KIND, '')
    const withNull = refuse(KIND, null)
    expect(withEmpty, 'an empty last_error lost the fallback').toContain(FALLBACK)
    expect(withNull, 'a null last_error lost the fallback').toContain(FALLBACK)
    expect(withEmpty, 'empty and null must read identically').toBe(withNull)

    // Discriminating floor: without this the three could all be one constant string.
    expect(withError, 'the detail clause is not carried at all').not.toBe(withNull)

    // The detail is SUBORDINATE, never the sentence: swapping the fallback for the real
    // reason must reproduce the error variant exactly, so the surrounding prose is one
    // sentence and not two different ones.
    expect(
      withNull.replace(FALLBACK, 'boom'),
      'the two variants are different sentences, not one sentence with one clause swapped',
    ).toBe(withError)

    // And it is still a sentence with a next step, not a bare error report.
    expect(withError, 'the error variant lost the next step').toMatch(MANUAL)
  })
})

describe('the budget refusal states its budget in seconds, derived (TS15-5, AC-5)', () => {
  it('TS15-5: the seconds come from EXTRACTION_POLL_BUDGET_MS, and the dead promise is gone', () => {
    expect(EXTRACTION_POLL_BUDGET_MS, 'the budget constant is not a usable number').toBeGreaterThan(1000)
    const seconds = String(Math.round(EXTRACTION_POLL_BUDGET_MS / 1000))
    expect(seconds.length, 'the derived token is empty — the assertion below is vacuous').toBeGreaterThan(0)

    const sentence = pollBudgetRefusal()
    expect(sentence, 'the budget is not stated in seconds').toContain(seconds)
    expect(sentence, 'the budget is printed in raw milliseconds').not.toContain(String(EXTRACTION_POLL_BUDGET_MS))

    expect(sentence.toLowerCase(), `the budget sentence still promises "${DEAD_PROMISE}"`).not.toContain(DEAD_PROMISE)
    expect(sentence, 'the budget sentence offers no next step').toMatch(MANUAL)
  })
})

// TS15-5b closes a gap TS15-5 cannot: with EXTRACTION_POLL_BUDGET_MS at 120_000, a hard-coded
// `const seconds = 120` renders the identical sentence and TS15-5 stays green. Only the source
// says which one is written (QA mutation (d) survived the whole suite without this).
describe('the budget seconds are derived in SOURCE, not just equal by luck (TS15-5b, AC-5)', () => {
  it('TS15-5b: pollBudgetRefusal computes its seconds from the constant', () => {
    const src = readRepoFile('src/lib/documentRun.ts', 'export function pollBudgetRefusal(')
    const body = src.slice(src.indexOf('export function pollBudgetRefusal('))
    const end = body.indexOf('\nexport function ', 1)
    const fn = end === -1 ? body : body.slice(0, end)
    expect(fn.length, 'the slice found no function body — the assertion below would be vacuous').toBeGreaterThan(80)
    expect(fn, 'pollBudgetRefusal names no budget constant; its seconds are a literal').toContain(
      'EXTRACTION_POLL_BUDGET_MS',
    )
  })
})

// AC-4 requires lastError VERBATIM, so the detail clause is quoted server text and cannot be
// sanitized. TS15-3's no-destination guard therefore governs the prose this module AUTHORS,
// which is why it only ever calls the refusals with a null reason. This pins the other half:
// a future sanitizer that "cleaned" a slash out of the clause would break AC-4 silently.
describe('the detail clause is quoted verbatim, slashes and all (TS15-3b, AC-4)', () => {
  const RAW = 'docling: post /v1/read: context deadline exceeded'

  it.each(['extract_failed', null])('TS15-3b: kind %s quotes the server’s reason unaltered', (kind) => {
    const sentence = deadLetterRefusal(kind as string | null, RAW)
    expect(sentence, 'the server’s own reason was reworded or stripped').toContain(RAW)
    expect(sentence, 'the authored prose lost its next step').toMatch(MANUAL)
  })
})

describe('pollVerdict passes the newest job’s failure_kind through (TS15-6, AC-6)', () => {
  it.each([...KINDS])('TS15-6: a dead_lettered job of kind %s settles with that kind’s sentence', (kind) => {
    const verdict = pollVerdict([job({ state: 'dead_lettered', failure_kind: kind, last_error: 'boom' })], 0)

    expect(verdict).toEqual({ kind: 'failed', reason: refuse(kind, 'boom') })
  })

  it('TS15-6: the five kinds settle into five different reasons, and the newest job’s kind is the one used', () => {
    const reasons = KINDS.map((k) => {
      const v = pollVerdict([job({ state: 'dead_lettered', failure_kind: k, last_error: 'boom' })], 0)
      if (v.kind !== 'failed') throw new Error(`kind ${k} did not settle as failed`)
      return v.reason
    })
    // The whole point of AC-6: a reducer that drops the kind returns one string five times.
    expect(new Set(reasons).size, 'pollVerdict returns the same reason for every failure kind').toBe(KINDS.length)

    // newestJob picks by created_at, not array order — so the kind must travel with THAT job.
    const older = job({ id: 'old', created_at: '2026-08-30T09:00:00Z', state: 'dead_lettered', failure_kind: 'document_unavailable', last_error: 'old' })
    const newer = job({ id: 'new', created_at: '2026-08-30T11:00:00Z', state: 'dead_lettered', failure_kind: 'text_not_read', last_error: 'new' })
    expect(pollVerdict([newer, older], 0)).toEqual({ kind: 'failed', reason: refuse('text_not_read', 'new') })
  })
})

describe('the deployed dead-letter assertion tracks the shipped sentence (TS15-10b, AC-10)', () => {
  // e2e/topology specs run only on the deploy gate, so this is their local oracle: the needle
  // EXTR10-E2E-02 pins must be a fragment of the pages_not_rendered sentence and of no other,
  // or that spec stays green on a substring the code no longer emits.
  it('TS15-10b: the e2e needle is a discriminating fragment of the pages_not_rendered sentence', () => {
    const spec = readRepoFile('../../e2e/topology/import-wizard.spec.ts', 'EXTR10-E2E-02')
    const m = /const DEAD_LETTER_NEEDLE = '([^']+)'/.exec(spec)
    expect(m, 'EXTR10-E2E-02 no longer pins its dead-letter reason through DEAD_LETTER_NEEDLE').not.toBeNull()
    const needle = (m as RegExpExecArray)[1]
    expect(needle.length, 'the needle is too short to identify a sentence').toBeGreaterThan(20)

    const all = terminalSentences()
    expect(all['pages_not_rendered'], 'the deployed spec pins text the pages_not_rendered sentence does not contain').toContain(needle)
    const others = Object.entries(all).filter(([k]) => k !== 'pages_not_rendered')
    expect(others.length, 'nothing to discriminate against').toBe(5)
    for (const [k, sentence] of others) {
      expect(sentence.includes(needle), `the needle also matches ${k} — it does not identify the kind`).toBe(false)
    }
  })
})

// ============================================================================
// RED specs (EXTR-15-07, task-833, Mode A) — the hand-off's provenance.
//
// A dead-lettered document is stored and can never be re-extracted, so the only route left
// is manual entry; the invoice typed that way must still name the document it came from
// (EXTR-15-06 shipped source_document_id on the create wire). That id has to survive the
// pipeline that failed.
//
// startDocumentRun binds `documentId` INSIDE the try (documentRun.ts:167), so the catch at
// :179 holds only f.id/f.name/err. Hoisting it to `let documentId: string | undefined` is
// what makes the table below pass — and the undefined leg is why it must stay optional:
// when deps.upload itself throws, no document exists to hand off.
//
// documentIdOf casts rather than reading the property directly. The one typecheck red for
// the missing field belongs to importRun.test.ts's HO-2, where AC-1 owns it.
function documentIdOf(outcome: FileOutcome): string | undefined {
  return (outcome as { documentId?: string }).documentId
}

describe('startDocumentRun — a failure names the document it failed on (HO-1, AC-2)', () => {
  const DOC_ID = 'doc-a.pdf-stored'
  type Leg = 'poll-verdict' | 'poll-throws' | 'import-throws' | 'upload-throws'

  // Every leg uploads the SAME id, so a row's expectation differs only by where the
  // pipeline died — never by what upload returned.
  function depsFor(leg: Leg): DocumentPipelineDeps {
    return {
      upload: async () => {
        if (leg === 'upload-throws') throw new Error('network: connection reset')
        return DOC_ID
      },
      poll: async () => {
        if (leg === 'poll-throws') throw new Error('GET /v1/extractions: 502 bad gateway')
        if (leg === 'poll-verdict') return { kind: 'failed', reason: 'docling: no text layer and no page render' }
        return { kind: 'succeeded', jobId: 'job-1' }
      },
      importDocument: async () => {
        if (leg === 'import-throws') throw new Error('POST /v1/imports: 502 bad gateway')
        return report('batch-1')
      },
      onStage: () => {},
    }
  }

  const table: { leg: Leg; want: string | undefined; why: string }[] = [
    // The non-succeeded verdict (documentRun.ts:170-174) is already inside the try, so it
    // can see the binding today; red only because the field does not exist yet.
    { leg: 'poll-verdict', want: DOC_ID, why: 'the poll returned a terminal failure' },
    // The two post-upload throws land in the catch, where the hoist is load-bearing.
    { leg: 'poll-throws', want: DOC_ID, why: 'poll rejected after the upload stored the document' },
    { leg: 'import-throws', want: DOC_ID, why: 'importDocument rejected after the upload stored the document' },
    // The one honest undefined: upload never returned, so there is nothing to hand off.
    { leg: 'upload-throws', want: undefined, why: 'upload itself threw — no document exists' },
  ]

  for (const row of table) {
    it(`HO-1 ${row.leg}: documentId is ${row.want ?? 'undefined'} — ${row.why}`, async () => {
      const outcomes = await startDocumentRun(docFiles(['a.pdf']), depsFor(row.leg))

      // Population floor and kind first, so the undefined row is never a vacuous pass on an
      // empty array or on an 'imported' outcome that carries no documentId by definition.
      expect(outcomes).toHaveLength(1)
      const outcome = outcomes[0].outcome
      expect(outcome.kind).toBe('failed')
      if (outcome.kind !== 'failed') throw new Error('unreachable — asserted above')
      expect(outcome.message.length, 'a failed outcome always carries a reason').toBeGreaterThan(0)

      expect(documentIdOf(outcome)).toBe(row.want)
    })
  }

  it('HO-1e: within ONE run, the file whose upload threw carries no id while its sibling does', async () => {
    // The discriminator the four single-file rows cannot be: one pipeline hoisting
    // correctly and the other not is invisible to a run of size 1.
    const files = docFiles(['ok.pdf', 'boom.pdf'])
    const deps: DocumentPipelineDeps = {
      upload: async (file) => {
        if (file.name === 'boom.pdf') throw new Error('network: connection reset')
        return `doc-${file.name}`
      },
      poll: async () => ({ kind: 'failed', reason: 'docling: no text layer and no page render' }),
      importDocument: async () => report('batch-1'),
      onStage: () => {},
    }

    const outcomes = await startDocumentRun(files, deps)

    expect(outcomes.map((o) => o.name)).toEqual(['ok.pdf', 'boom.pdf'])
    expect(outcomes.map((o) => o.outcome.kind)).toEqual(['failed', 'failed'])
    expect(outcomes.map((o) => documentIdOf(o.outcome))).toEqual(['doc-ok.pdf', undefined])
  })
})
