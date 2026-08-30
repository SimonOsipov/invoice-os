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
import { describe, expect, it, vi } from 'vitest'

import {
  EXTRACTION_POLL_BUDGET_MS,
  deadLetterRefusal,
  isTerminalExtractionState,
  newestJob,
  pollBudgetRefusal,
  pollVerdict,
  startDocumentRun,
} from './documentRun'
import type { DocumentPipelineDeps, DocumentRunFile } from './documentRun'
import type { ExtractionJob, ImportReport } from './importApi'
import { routeAfterRun } from './importRun'
import type { ImportRun, RunFile } from './importRun'

function job(over: Partial<ExtractionJob>): ExtractionJob {
  return {
    id: 'job-1',
    document_id: 'doc-1',
    state: 'queued',
    created_at: '2026-08-30T10:00:00Z',
    last_error: null,
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
    expect(pollBudgetRefusal()).not.toBe(deadLetterRefusal('pdfium: render failed'))
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
    }

    const outcomes = await startDocumentRun(files, deps)

    expect(uploadSpy).toHaveBeenCalledTimes(2)
    expect(outcomes.map((o) => o.outcome.kind)).toEqual(['imported', 'failed'])
    const failed = outcomes[1].outcome
    if (failed.kind !== 'failed') throw new Error('unreachable — asserted above')
    expect(failed.message).toContain('connection reset')
  })
})
