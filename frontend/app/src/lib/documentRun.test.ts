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
import type { ImportRun, RunFile } from './importRun'
import { LIVE_POLL_MS } from './invoices'

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

// EXTR-10-01: documentRunRows joins on RunFile, not DocumentRunFile -- a fresh helper
// rather than reusing docFiles, whose shape (a File) is the wrong side of the seam.
function runFile(id: string, name: string): RunFile {
  return { id, name, groupId: '', outcome: { kind: 'pending' } }
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
    expect(verdict.reason).toBe(deadLetterRefusal(LAST_ERROR))
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
