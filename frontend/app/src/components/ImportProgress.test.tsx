// @vitest-environment jsdom
// RED specs (task-786, EXTR-10-04, Mode A) -- pin the settled contract for ImportProgress's
// document-run rows before ImportProgress.tsx is touched: the row source picked on
// ctx.runKind, the four in-flight kinds through one IN_FLIGHT_LABEL table, RETRYING in
// --status-amber-text, and the two new testids (import-progress / import-progress-row).
// CARD-1..8 exactly as .ralph/story-append.md / task-786's Test Specs table. Rows are
// selected ONLY via the not-yet-existing testid, so every spec here reads zero rows
// against today's component -- a genuine behavioural RED, not a resolution error. Second
// test file for this component; ctx cast follows CreateFlow.test.tsx:60
// (`as unknown as PlatformCtx`).
import { cleanup, render } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import type { DocumentRowState } from '../lib/documentRun'
import type { ImportReport } from '../lib/importApi'
import { runFileRows } from '../lib/importRun'
import type { FileOutcome, ImportRun, RunFile } from '../lib/importRun'
import type { PlatformCtx } from '../types'
import { ImportProgress } from './ImportProgress'

// Mirrors documentRun.test.ts:72's runFile -- a document RunFile always starts 'pending';
// the map (documentStages), not run.outcome, is the whole truth while the run is live
// (STAGE-5).
function runFile(id: string, name: string): RunFile {
  return { id, name, groupId: '', outcome: { kind: 'pending' } }
}

// Duplicated locally rather than imported, same reason importRun.test.ts:401 duplicates
// its own BASE_RUN_REPORT -- independently owned test fixtures, not production code a
// shared module would be surgical over.
const BASE_REPORT: ImportReport = {
  id: 'batch-run-base',
  status: 'completed',
  format: 'csv',
  delimiter: ',',
  encoding: 'utf-8',
  rows_total: 4,
  rows_valid: 4,
  rows_invalid: 0,
  ready_invoices: 4,
  quarantined_invoices: 0,
  errors: [],
  rule_set_version: 5,
  invoices_clean: 4,
  invoices_with_violations: 0,
  invoice_violations: [],
}

function sheetFile(id: string, name: string, outcome: FileOutcome): RunFile {
  return { id, name, groupId: 'g1', outcome }
}

// Minimal PlatformCtx: ImportProgress today reads only ctx.run; the settled design adds
// ctx.runKind / ctx.documentStages. Narrower than CreateFlow.test.tsx's ctx (no step
// components mount here), same `as unknown as PlatformCtx` idiom (:60).
function progressCtx(runKind: 'document' | 'spreadsheet' | null, run: ImportRun, documentStages: Record<string, DocumentRowState>): PlatformCtx {
  const ctx = { runKind, run, documentStages }
  return ctx as unknown as PlatformCtx
}

function rows(container: HTMLElement): Element[] {
  return Array.from(container.querySelectorAll('[data-testid="import-progress-row"]'))
}

// The row's first child is always the filename span (unchanged across every arm).
function rowName(row: Element): string {
  return (row.children[0]?.textContent ?? '').trim()
}

// The row's second child is the whole status representation. For queued/imported it IS
// the labelled span; for the four in-flight kinds it is a wrapper around the shimmer span
// plus a nested `.mono` label; for failed it is the reason span directly. `.textContent`
// collapses all three shapes to the same visible word because the shimmer span carries no
// text.
function statusText(row: Element): string {
  return (row.children[1]?.textContent ?? '').trim()
}

// Resolved inline style, not a class (failed's reason span carries no `.mono` class) --
// the innermost span carrying the visible text is either the wrapper itself or its
// nested `.mono` label.
function statusColor(row: Element): string {
  const el = row.children[1] as HTMLElement | undefined
  if (!el) return ''
  const mono = el.matches('.mono') ? el : el.querySelector<HTMLElement>('.mono')
  return (mono ?? el).style.color
}

// Shimmer span dimensions (28x6) are the literal pair ImportProgress.tsx is pinned by,
// same idiom as CreateFlow.test.tsx's connectorCount (36x1) -- collides with nothing else
// on the row (filename/label spans carry no explicit width+height pair).
function hasShimmer(row: Element): boolean {
  return Array.from(row.querySelectorAll('span')).some((s) => s.style.width === '28px' && s.style.height === '6px')
}

describe('ImportProgress — document rows (CARD-1..8, EXTR-10-04)', () => {
  afterEach(() => cleanup())

  it('CARD-1: an extracting document reads READING', () => {
    const run: ImportRun = { files: [runFile('f1', 'a.pdf')], cursor: 0, status: 'running' }
    const stages: Record<string, DocumentRowState> = { f1: { kind: 'reading' } }
    const { container } = render(<ImportProgress ctx={progressCtx('document', run, stages)} />)

    const r = rows(container)
    expect(r).toHaveLength(1)
    expect(statusText(r[0])).toBe('READING')
    expect(hasShimmer(r[0])).toBe(true)
  })

  it('CARD-2: RETRYING is a warning, still in flight', () => {
    const run: ImportRun = { files: [runFile('f1', 'a.pdf')], cursor: 0, status: 'running' }
    const stages: Record<string, DocumentRowState> = { f1: { kind: 'retrying' } }
    const { container } = render(<ImportProgress ctx={progressCtx('document', run, stages)} />)

    const r = rows(container)
    expect(r).toHaveLength(1)
    expect(statusText(r[0])).toBe('RETRYING')
    expect(statusColor(r[0])).toBe('var(--status-amber-text)')
    expect(hasShimmer(r[0])).toBe(true)
  })

  it('CARD-3: three documents show three words at once, in picked order', () => {
    const run: ImportRun = {
      files: [runFile('f1', 'a.pdf'), runFile('f2', 'b.pdf'), runFile('f3', 'c.pdf')],
      cursor: 0,
      status: 'running',
    }
    const stages: Record<string, DocumentRowState> = {
      f1: { kind: 'reading' },
      f2: { kind: 'retrying' },
      f3: { kind: 'processing' },
    }
    const { container } = render(<ImportProgress ctx={progressCtx('document', run, stages)} />)

    const r = rows(container)
    expect(r).toHaveLength(3)
    const texts = r.map(statusText)
    expect(new Set(texts).size).toBe(3)
    expect(texts).toEqual(['READING', 'RETRYING', 'SERVER PROCESSING'])
    expect(r.map(rowName)).toEqual(['a.pdf', 'b.pdf', 'c.pdf'])
  })

  it('CARD-4: a failed document names its reason on its own row', () => {
    const run: ImportRun = {
      files: [runFile('f1', 'a.pdf'), runFile('f2', 'b.pdf'), runFile('f3', 'c.pdf')],
      cursor: 0,
      status: 'running',
    }
    const REASON = 'Server rejected this file: unreadable PDF'
    const stages: Record<string, DocumentRowState> = {
      f1: { kind: 'reading' },
      f2: { kind: 'failed', reason: REASON },
      f3: { kind: 'reading' },
    }
    const { container } = render(<ImportProgress ctx={progressCtx('document', run, stages)} />)

    const r = rows(container)
    expect(r).toHaveLength(3)
    expect(statusText(r[1])).toBe(REASON)
    expect(statusColor(r[1])).toBe('var(--status-red-text)')
    expect(statusText(r[0])).toBe('READING')
    expect(statusText(r[2])).toBe('READING')
  })

  it('CARD-5: no row implies a queue position', () => {
    const run: ImportRun = {
      files: [
        runFile('f1', 'a.pdf'),
        runFile('f2', 'b.pdf'),
        runFile('f3', 'c.pdf'),
        runFile('f4', 'd.pdf'),
        runFile('f5', 'e.pdf'),
        runFile('f6', 'f.pdf'),
      ],
      cursor: 0,
      status: 'running',
    }
    // f1 left unmapped -> queued. One file per DocumentRowState kind.
    const stages: Record<string, DocumentRowState> = {
      f2: { kind: 'reading' },
      f3: { kind: 'retrying' },
      f4: { kind: 'processing' },
      f5: { kind: 'imported', count: 2 },
      f6: { kind: 'failed', reason: 'boom' },
    }
    const { container } = render(<ImportProgress ctx={progressCtx('document', run, stages)} />)

    const r = rows(container)
    expect(r).toHaveLength(6)
    const texts = r.map(statusText)
    for (const t of texts) {
      expect(t).not.toMatch(/\b\d+\s*(of|\/)\s*\d+\b/i)
      expect(t).not.toMatch(/^(\d+(st|nd|rd|th)\b)/i)
    }
  })

  it('CARD-6: the vocabulary is closed', () => {
    const REASON = 'Explicit failure reason, verbatim'
    const run: ImportRun = {
      files: [
        runFile('f1', 'a.pdf'),
        runFile('f2', 'b.pdf'),
        runFile('f3', 'c.pdf'),
        runFile('f4', 'd.pdf'),
        runFile('f5', 'e.pdf'),
        runFile('f6', 'f.pdf'),
      ],
      cursor: 0,
      status: 'running',
    }
    const stages: Record<string, DocumentRowState> = {
      f2: { kind: 'reading' },
      f3: { kind: 'retrying' },
      f4: { kind: 'processing' },
      f5: { kind: 'imported', count: 7 },
      f6: { kind: 'failed', reason: REASON },
      // f1 left unmapped -> queued
    }
    const { container } = render(<ImportProgress ctx={progressCtx('document', run, stages)} />)

    const r = rows(container)
    const texts = r.map(statusText)
    expect(texts).toHaveLength(6)
    const allowed = new Set(['QUEUED', 'SENDING FILE', 'SERVER PROCESSING', 'READING', 'RETRYING'])
    for (const t of texts) {
      expect(allowed.has(t) || /^\d+ IMPORTED$/.test(t) || t === REASON).toBe(true)
    }
  })

  it('CARD-7: a spreadsheet run ignores the map', () => {
    const files: RunFile[] = [
      sheetFile('s1', 'a.csv', { kind: 'pending' }),
      sheetFile('s2', 'b.csv', { kind: 'uploading', phase: { kind: 'sending', loaded: 10, total: 100 } }),
      sheetFile('s3', 'c.csv', { kind: 'imported', batchId: 'batch-1', report: BASE_REPORT }),
      sheetFile('s4', 'd.csv', { kind: 'failed', message: 'boom' }),
    ]
    const run: ImportRun = { files, cursor: 0, status: 'running' }
    // Non-empty and document-shaped -- must be ignored by a spreadsheet/null run.
    const nonEmptyDocStages: Record<string, DocumentRowState> = { s1: { kind: 'reading' }, s2: { kind: 'retrying' } }
    const expected = runFileRows(run)
    expect(expected.length).toBeGreaterThan(0) // sanity: the fixture actually has files

    for (const runKind of ['spreadsheet', null] as const) {
      const { container } = render(<ImportProgress ctx={progressCtx(runKind, run, nonEmptyDocStages)} />)
      const r = rows(container)
      expect(r).toHaveLength(expected.length)
      expect(r.map(rowName)).toEqual(expected.map((e) => e.name))
      expect(r.map(statusText)).toEqual(
        expected.map((e) => {
          if (e.kind === 'queued') return 'QUEUED'
          if (e.kind === 'sending') return 'SENDING FILE'
          if (e.kind === 'processing') return 'SERVER PROCESSING'
          if (e.kind === 'imported') return `${e.count} IMPORTED`
          if (e.kind === 'failed') return e.reason
          // runFileRows never produces these (EXTR-10-05's SHEET-2) — a fixture bug, not
          // a real card state.
          throw new Error(`unexpected RunFileRow kind: ${e.kind}`)
        }),
      )
      cleanup()
    }
  })

  it('CARD-8: the card is empty-safe', () => {
    const run: ImportRun = { files: [], cursor: 0, status: 'idle' }
    const { container } = render(<ImportProgress ctx={progressCtx('document', run, {})} />)

    expect(rows(container)).toHaveLength(0)
    expect(container.firstChild).toBeNull()
  })
})

// task-786 QA (Mode B) -- adversarial/edge coverage the RED phase did not cover. CARD-1..8
// pin the closed vocabulary and the per-file join; these pin the empty-map default, the
// duplicate's un-special-cased count, and the shimmer's presence/absence across every kind.
describe('ImportProgress — document rows, adversarial coverage (task-786 QA)', () => {
  afterEach(() => cleanup())

  it('an empty stage map reads every row QUEUED, not blank', () => {
    const run: ImportRun = {
      files: [runFile('f1', 'a.pdf'), runFile('f2', 'b.pdf'), runFile('f3', 'c.pdf')],
      cursor: 0,
      status: 'running',
    }
    const { container } = render(<ImportProgress ctx={progressCtx('document', run, {})} />)

    const r = rows(container)
    expect(r).toHaveLength(3)
    for (const row of r) {
      expect(statusText(row)).toBe('QUEUED')
      expect(hasShimmer(row)).toBe(false)
    }
  })

  it('a duplicate (0 IMPORTED) renders like any other imported count, no special-casing (D-14)', () => {
    const run: ImportRun = { files: [runFile('f1', 'a.pdf')], cursor: 0, status: 'running' }
    const stages: Record<string, DocumentRowState> = { f1: { kind: 'imported', count: 0 } }
    const { container } = render(<ImportProgress ctx={progressCtx('document', run, stages)} />)

    const r = rows(container)
    expect(r).toHaveLength(1)
    expect(statusText(r[0])).toBe('0 IMPORTED')
    expect(statusColor(r[0])).toBe('var(--status-green-text)')
    expect(hasShimmer(r[0])).toBe(false)
  })

  it('the shimmer marks every in-flight row and only those, across a mixed settled/in-flight card', () => {
    const REASON = 'Server rejected this file: corrupt PDF'
    const run: ImportRun = {
      files: [
        runFile('f1', 'a.pdf'),
        runFile('f2', 'b.pdf'),
        runFile('f3', 'c.pdf'),
        runFile('f4', 'd.pdf'),
        runFile('f5', 'e.pdf'),
        runFile('f6', 'f.pdf'),
      ],
      cursor: 0,
      status: 'running',
    }
    const stages: Record<string, DocumentRowState> = {
      // f1 unmapped -> queued
      f2: { kind: 'reading' },
      f3: { kind: 'retrying' },
      f4: { kind: 'processing' },
      f5: { kind: 'imported', count: 3 },
      f6: { kind: 'failed', reason: REASON },
    }
    const { container } = render(<ImportProgress ctx={progressCtx('document', run, stages)} />)

    const r = rows(container)
    expect(r).toHaveLength(6)
    const shimmer = r.map(hasShimmer)
    // f1 queued, f5 imported, f6 failed: settled or not-yet-started, never a shimmer.
    // f2/f3/f4 reading/retrying/processing: the three in-flight kinds this run kind can
    // ever carry (D-3 -- 'sending' is spreadsheet-only, covered separately below).
    expect(shimmer).toEqual([false, true, true, true, false, false])
  })

  it('SENDING FILE (spreadsheet-only in-flight kind) still carries the shimmer', () => {
    const files: RunFile[] = [sheetFile('s1', 'a.csv', { kind: 'uploading', phase: { kind: 'sending', loaded: 1, total: 10 } })]
    const run: ImportRun = { files, cursor: 0, status: 'running' }
    const { container } = render(<ImportProgress ctx={progressCtx('spreadsheet', run, {})} />)

    const r = rows(container)
    expect(r).toHaveLength(1)
    expect(statusText(r[0])).toBe('SENDING FILE')
    expect(hasShimmer(r[0])).toBe(true)
  })
})

// EXTR-10-05 (task-787) — Core AC 7's own named oracle: the spreadsheet card renders the
// same five words it always did. Unlike CARD-7 (which derives its expectation from
// runFileRows itself), this asserts the literal shipped strings, so it also catches a
// renamed IN_FLIGHT_LABEL entry or the spreadsheet branch quietly rerouted through
// documentRunRows.
describe('ImportProgress — spreadsheet path unmoved (SHEET-3, EXTR-10-05, Core AC 7)', () => {
  afterEach(() => cleanup())

  it('SHEET-3: a five-file mixed spreadsheet run renders the same five words, same colours', () => {
    const REASON = 'Row 4: invoice number is required'
    const files: RunFile[] = [
      sheetFile('s1', 'a.csv', { kind: 'pending' }),
      sheetFile('s2', 'b.csv', { kind: 'uploading', phase: { kind: 'sending', loaded: 10, total: 100 } }),
      sheetFile('s3', 'c.csv', { kind: 'uploading', phase: { kind: 'processing' } }),
      sheetFile('s4', 'd.csv', { kind: 'imported', batchId: 'batch-1', report: BASE_REPORT }),
      sheetFile('s5', 'e.csv', { kind: 'failed', message: REASON }),
    ]
    const run: ImportRun = { files, cursor: 3, status: 'running' }
    const { container } = render(<ImportProgress ctx={progressCtx('spreadsheet', run, {})} />)

    const r = rows(container)
    expect(r).toHaveLength(5)
    expect(r.map(rowName)).toEqual(['a.csv', 'b.csv', 'c.csv', 'd.csv', 'e.csv'])

    expect(statusText(r[0])).toBe('QUEUED')
    expect(statusColor(r[0])).toBe('var(--fg-3)')
    expect(hasShimmer(r[0])).toBe(false)

    expect(statusText(r[1])).toBe('SENDING FILE')
    expect(statusColor(r[1])).toBe('var(--action)')
    expect(hasShimmer(r[1])).toBe(true)

    expect(statusText(r[2])).toBe('SERVER PROCESSING')
    expect(statusColor(r[2])).toBe('var(--action)')
    expect(hasShimmer(r[2])).toBe(true)

    expect(statusText(r[3])).toBe('4 IMPORTED')
    expect(statusColor(r[3])).toBe('var(--status-green-text)')
    expect(hasShimmer(r[3])).toBe(false)

    expect(statusText(r[4])).toBe(REASON)
    expect(statusColor(r[4])).toBe('var(--status-red-text)')
    expect(hasShimmer(r[4])).toBe(false)
  })
})
