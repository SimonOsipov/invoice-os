// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// EXTR-11-07. Started as Mode A red specs; `ExtractionReview.tsx` now exists and every row
// below is green against it. Each row is mutation-proved against the SHIPPED file — the
// mutant that reds it is named in its own comment where the answer is not obvious.
//
// The pane GEOMETRY has no oracle here and none is attempted: jsdom computes no layout, so
// a rendered width is `EXTR11-E2E-02`/`-10`'s claim, written in this same commit. What this
// file holds is the state ladder, the selection contract, the reset-on-`jobId` contract, and
// the four style objects as a STRUCTURAL regression guard over the declarations that make
// containment work. A rendered `boundingBox().width === 620` stays forbidden (`D-4`).
//
// ASSUMED CONTRACT: `<ExtractionReview ctx={ctx} jobId={jobId} />`. System Design §7 writes
// the route as `<ExtractionReview ctx={ctx} />` with the id off `ctx.extractionJobId`, but
// that ctx member is EXTR-11-08's to add — reading it here makes THIS subtask's own commit
// fail `tsc`. The prop also matches `ExtractionCanvas`'s shape and gives AC-9 a handle to
// change. EXTR-11-08 then renders `<ExtractionReview ctx={ctx} jobId={ctx.extractionJobId} />`.
//
// MEASURED jsdom 27.4.0 serialization — read back off a rendered probe, not assumed:
//   `flex: 1` reads `1 1 0%`; `flex: '1 1 auto'` and `flex: '1 1 620px'` round-trip raw
//   `minHeight: 0` and `minWidth: 0` read `0`, NOT `0px`; `minWidth: 470` reads `470px`
//   `width: 620` reads `620px`; `overflow: 'hidden'` and a `border`/`background` with
//   `var()` round-trip raw
//   `Element.prototype.scrollTop` IS implemented and stores what it is given — so AC-9's
//   scroll-position clause has a real oracle, floored below by reading 480 back
// jsdom implements NO `IntersectionObserver` and NO `Element.prototype.scrollTo`.
//
// The `scrollTo` shim is installed at MODULE scope and never removed. `scrollRegionIntoView`
// (extractionReview.ts:151) defers on `setTimeout(…, 20)`, which fires on the REAL clock in
// any spec that does not fake timers; an install/remove pair around each spec leaves a window
// with no shim and throws an uncaught TypeError into an unrelated spec 20ms later. That
// failure is on record against `ExtractionCanvas.test.tsx` — the app suite exited 1 while
// reporting every test passed. Do not reintroduce a per-spec install.

import { readFileSync } from 'node:fs'
import path from 'node:path'

import { createPortal } from 'react-dom'

import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import type {
  ExtractionDetail,
  ExtractionDocument,
  ExtractionFieldState,
  ExtractionPage,
  ExtractionRegion,
} from '../lib/extractionReview'
import type { PlatformCtx } from '../types'
import { ExtractionReview } from './ExtractionReview'

const JOB_ID = 'c3d4e5f6-a7b8-4c3d-9e4f-5a6b7c8d9e0f'
const OTHER_JOB_ID = 'a1b2c3d4-e5f6-4a1b-8c2d-3e4f5a6b7c8d'
const DOCUMENT_ID = 'b2c3d4e5-f6a7-4b2c-8d3e-4f5a6b7c8d9e'

// System Design → Invented copy. Literals, never a regex: both sentences are this subtask's
// own and a paraphrase is a different product.
const STILL_READING = 'This document is still being read.'
const COULD_NOT_READ = 'This document could not be read.'

// ErrorState's hardcoded heading (api-client/src/components/ErrorState.tsx:45) and its
// retry label (:73). The shared surface is the AC's subject, so both are read off it.
const ERROR_HEADING = 'Something went wrong'
const RETRY_LABEL = 'Retry'

// Loading's own class (api-client/src/components/Loading.tsx:26) — the house oracle for the
// shared surface (ApprovalsView.test.tsx:175, WorkflowsView.test.tsx:111).
const LOADING_SPINNER = '.apic-loading-spin'

// §6's ladder, in the story's own words: `failed` is NOT terminal (River retries), so it
// takes the still-reading sentence beside `queued` and `extracting`.
const UNSETTLED = ['queued', 'extracting', 'failed'] as const

const LETTER: ExtractionPage = { page: 1, width_px: 1275, height_px: 1651 } // US-Letter @150
const A4: ExtractionPage = { page: 2, width_px: 1240, height_px: 1754 } // A4 @150

function mkPage(o: Partial<ExtractionPage> = {}): ExtractionPage {
  return { ...LETTER, ...o }
}

function mkRegion(o: Partial<ExtractionRegion> = {}): ExtractionRegion {
  return { page: 1, x0: 0.62, y0: 0.08, x1: 0.9, y1: 0.13, ...o }
}

function mkField(o: Partial<ExtractionFieldState> = {}): ExtractionFieldState {
  return {
    name: 'invoice_number',
    value: 'INV-2026-0037',
    region: mkRegion(),
    reason: '',
    alternatives: [],
    corrected: null,
    ...o,
  }
}

function mkDocument(o: Partial<ExtractionDocument> = {}): ExtractionDocument {
  return {
    filename: 'june-invoices.pdf',
    content_type: 'application/pdf',
    size_bytes: 151552, // exactly 148 KiB
    stored_at: '2026-08-30T10:42:07Z',
    ...o,
  }
}

// Three pages and three fields whose regions land on three different pages, so a selection
// can name a page the mount never touched. Every absence row below floors against this.
const THREE_PAGES: ExtractionPage[] = [A4, mkPage({ page: 1 }), mkPage({ page: 3 })]
const THREE_FIELDS: ExtractionFieldState[] = [
  mkField({ name: 'invoice_number', region: mkRegion({ page: 1 }) }),
  mkField({ name: 'invoice_date', region: mkRegion({ page: 2, x0: 0.1, y0: 0.3, x1: 0.38, y1: 0.35 }) }),
  mkField({ name: 'total_amount', region: mkRegion({ page: 3, x0: 0.62, y0: 0.7, x1: 0.9, y1: 0.76 }) }),
]

function mkDetail(o: Partial<ExtractionDetail> = {}): ExtractionDetail {
  return {
    id: JOB_ID,
    document_id: DOCUMENT_ID,
    state: 'succeeded',
    document: mkDocument(),
    pages: THREE_PAGES,
    fields: THREE_FIELDS,
    ...o,
  }
}

// -- the detail wire -----------------------------------------------------------------------

// The screen reads its detail through `ctx.authedFetch`, the house seam every other screen
// uses (AuditView.tsx:111). No module mock: a `vi.mock` of extractionReview.ts would take
// the four style helpers with it and the panes would render nothing.
type ReviewCtx = Pick<PlatformCtx, 'authedFetch' | 'getToken'>

/** One request the screen made, as the seam saw it. */
interface WireCall {
  url: string
  method: string
  body: unknown
}

interface Wire {
  ctx: PlatformCtx
  /**
   * Every job id the screen asked the wire for, in order. An EXACT-EQUALITY pin in three
   * shipped rows, and it takes the URL's LAST path segment — so a POST appends the literal
   * 'corrections'. Left exactly as it is; `calls()` is what the write rows read.
   */
  asked: () => string[]
  /** Every request, whole: url, method and body. */
  calls: () => WireCall[]
}

function wire(reply: (jobId: string, url: string, opts?: { method?: string; body?: unknown }) => Promise<unknown>): Wire {
  const asked: string[] = []
  const calls: WireCall[] = []
  const authedFetch = async (url: string, opts?: { method?: string; body?: unknown }): Promise<unknown> => {
    const last = url.split('?')[0].split('/').filter(Boolean).pop() ?? ''
    asked.push(decodeURIComponent(last))
    calls.push({ url, method: opts?.method ?? 'GET', body: opts?.body })
    return reply(decodeURIComponent(last), url, opts)
  }
  const fake: ReviewCtx = {
    authedFetch: authedFetch as unknown as PlatformCtx['authedFetch'],
    getToken: () => 'tok',
  }
  return { ctx: fake as unknown as PlatformCtx, asked: () => [...asked], calls: () => [...calls] }
}

/** The ordinary wire: one detail, whatever job is asked for. */
function serving(detail: ExtractionDetail = mkDetail()): Wire {
  return wire(async () => detail)
}

interface ReviewProps {
  ctx: PlatformCtx
  jobId: string
}

function review(over: Partial<ReviewProps> & { ctx: PlatformCtx }) {
  const props: ReviewProps = { jobId: JOB_ID, ...over }
  return <ExtractionReview {...props} />
}

// -- the page-bytes transport ----------------------------------------------------------------

function pageOf(url: string): number {
  const m = /\/pages\/(\d+)(?:\?|$)/.exec(url)
  expect(m, `not a page-image URL: ${url}`).not.toBeNull()
  return Number((m as RegExpExecArray)[1])
}

interface PageFetch {
  /** Every page number requested, in dispatch order. */
  requested: () => number[]
}

function pageFetch(): PageFetch {
  const requested: number[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      requested.push(pageOf(url))
      return { ok: true, status: 200, statusText: 'OK', arrayBuffer: async () => new ArrayBuffer(8) }
    }),
  )
  return { requested: () => [...requested] }
}

/** Holds every page request open, so no handle can land inside a fake-timer window. */
function deferredPageFetch(): void {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      pageOf(url)
      await new Promise<void>(() => {})
      return { ok: true, status: 200, statusText: 'OK', arrayBuffer: async () => new ArrayBuffer(8) }
    }),
  )
}

// jsdom implements no IntersectionObserver, and `ExtractionCanvas` then loads every frame at
// mount (ExtractionCanvas.tsx:243-247). That makes "a selection requests its own page" and
// "the map empties" both vacuous — every page is already loaded. This observer exists and
// never fires, so the ONLY byte request in such a spec is the one the selection made.
class SilentObserver {
  static built = 0
  constructor(_cb: unknown, _opts?: unknown) {
    SilentObserver.built += 1
  }
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
  takeRecords(): [] {
    return []
  }
}

function silenceObserver(): void {
  SilentObserver.built = 0
  vi.stubGlobal('IntersectionObserver', SilentObserver)
}

/** Deletes the constructor outright — jsdom's own default, and the eager-load environment. */
function removeObserver(): void {
  vi.stubGlobal('IntersectionObserver', undefined)
  delete (globalThis as { IntersectionObserver?: unknown }).IntersectionObserver
}

// useAsync awaits the producer, then dispatches; fetchPageImage awaits fetch, then
// res.arrayBuffer(), then the caller's own .then. Drained generously: a thin flush turns a
// correct implementation red for being one tick slower than this helper.
async function flush(): Promise<void> {
  await act(async () => {
    for (let i = 0; i < 8; i += 1) await Promise.resolve()
  })
}

// -- DOM readers ------------------------------------------------------------------------------

function root(): HTMLElement {
  return screen.getByTestId('extraction-review')
}

function body(): HTMLElement {
  return screen.getByTestId('extraction-review-body')
}

function canvasPane(): HTMLElement {
  return screen.getByTestId('extraction-canvas')
}

function fieldsPane(): HTMLElement {
  return screen.getByTestId('extraction-fields')
}

function ground(): HTMLElement {
  return screen.getByTestId('extraction-ground')
}

function fieldRow(name: string): HTMLElement {
  return screen.getByTestId(`extraction-field-${name}`)
}

/** The trailing hyphen matters: `extraction-fields` is itself prefixed by `extraction-field`. */
function fieldRows(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>('[data-testid^="extraction-field-"]'))
}

// `aria-current`, not `aria-pressed`: the cell holds an input and buttons and can no longer
// take a button role. The name is left alone — sixteen shipped call sites read it.
function pressedRows(): string[] {
  return fieldRows()
    .filter((el) => el.getAttribute('aria-current') === 'true')
    .map((el) => el.dataset.testid ?? '')
}

function inputOf(name: string): HTMLInputElement | null {
  return screen.queryByTestId(`extraction-input-${name}`) as HTMLInputElement | null
}

function chipsOf(name: string): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>(`[data-testid^="extraction-chip-${name}-"]`))
}

function saveButton(): HTMLButtonElement | null {
  return screen.queryByTestId('extraction-save') as HTMLButtonElement | null
}

/** Every request that was not the detail GET. */
function writes(w: Wire): WireCall[] {
  return w.calls().filter((c) => c.method !== 'GET')
}

function highlights(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>('[data-testid="extraction-highlight"]'))
}

function pageImages(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>('[data-testid^="extraction-page-image-"]'))
}

function frames(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>('[data-testid^="extraction-page-"]')).filter((el) =>
    /^extraction-page-\d+$/.test(el.dataset.testid ?? ''),
  )
}

function pick(style: CSSStyleDeclaration, keys: readonly string[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const k of keys) out[k] = (style as unknown as Record<string, string>)[k]
  return out
}

const REVIEW_TSX = path.join(process.cwd(), 'src/components/ExtractionReview.tsx')

function source(): string {
  const src = readFileSync(REVIEW_TSX, 'utf8')
  // Planted needles: a moved, renamed or gutted component must fail rather than report clear.
  expect(src.length, 'the scan read nothing').toBeGreaterThan(500)
  expect(src, 'the scan ran over a moved or renamed component').toContain('data-testid="extraction-review"')
  return src
}

let createSpy: MockInstance<typeof URL.createObjectURL>
let revokeSpy: MockInstance<typeof URL.revokeObjectURL>
let scrollToSpy: ReturnType<typeof vi.fn>

function urls(): string[] {
  return createSpy.mock.results.map((r) => String(r.value))
}

function revoked(): string[] {
  return revokeSpy.mock.calls.map((c) => String(c[0]))
}

// Installed ONCE, for the file's whole lifetime, and never removed — see the file header.
// `isConnected` drops a stale timer's call onto an earlier spec's detached ground, so every
// scroll counted below landed on a mounted one. Reads `scrollToSpy` at call time, so each
// spec observes its own fresh spy.
Object.defineProperty(Element.prototype, 'scrollTo', {
  value: function (this: Element, ...args: unknown[]) {
    if (this.isConnected) (scrollToSpy as unknown as (this: Element, ...a: unknown[]) => void).apply(this, args)
  },
  configurable: true,
  writable: true,
})

beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', 'https://gw')
  let n = 0
  createSpy = vi.spyOn(URL, 'createObjectURL').mockImplementation(() => `blob:${++n}`)
  revokeSpy = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
  scrollToSpy = vi.fn()
  removeObserver()
  pageFetch()
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  vi.unstubAllEnvs()
  SilentObserver.built = 0
})

// ==========================================================================================
// The shell and its two panes (AC-1, AC-2, AC-3, AC-4)
// ==========================================================================================

describe('the screen shell', () => {
  it('roots the screen at its own testid, over a body holding exactly the two panes', async () => {
    render(review({ ctx: serving().ctx }))
    await flush()

    // Floor: both panes rendered real content, so the structure below is not two empty divs.
    expect(frames(), 'the document pane rendered no frame').toHaveLength(3)
    expect(fieldRows(), 'the fields pane rendered no row').toHaveLength(3)

    expect(root().contains(body()), 'the body is not inside the screen root').toBe(true)

    // §4's tree: the panes are the body's OWN children. A wrapper around either would carry
    // a second set of flex declarations beside the pane's own — the averaged shape AC-3 and
    // AC-4 exist to prevent, and the shape that makes `flex: '1 1 620px'` resolve against
    // the wrong parent.
    expect(
      Array.from(body().children).map((el) => (el as HTMLElement).dataset.testid ?? '<no testid>'),
      'the body must hold the document pane and the fields pane, in that order, and nothing else',
    ).toEqual(['extraction-canvas', 'extraction-fields'])
  })

  it('declares a definite height on the screen root', async () => {
    render(review({ ctx: serving().ctx }))
    await flush()

    // AC-2 / `P-40`. A DECLARATION PIN, and nothing more: jsdom applies no CSS, so no row in
    // this file can tell `height: '100%'` from its absence behaviourally. Both real oracles
    // are deployed and neither has run — `EXTR11-E2E-02b`'s scroll precondition and
    // `EXTR11-E2E-06`'s zoom-150 clause. Without this row the declaration had NO oracle at
    // any layer that executes: deleting it left all 19 rows green (QA Mode B, mutant M2).
    // `.pf-scroll` (App.tsx:1435) is a block container, so at `height: auto` this root grows
    // to its content and every containment declaration below it is decorative.
    expect(pick(root().style, ['height', 'display', 'flexDirection'])).toEqual({
      height: '100%',
      display: 'flex',
      flexDirection: 'column',
    })
  })

  it('declares the containment idiom on the body', async () => {
    render(review({ ctx: serving().ctx }))
    await flush()

    // AC-2, a STRUCTURAL regression guard, never a layout oracle. `SourceDocumentModal.tsx:224-232`
    // is the idiom: without `flex: 1` + `minHeight: 0` a flex item takes a content-based
    // automatic minimum and the pane grows to its content's height. `C-2`.
    // MEASURED: React writes `flex: 1` as the triple.
    expect(pick(body().style, ['display', 'flex', 'minHeight', 'overflow'])).toEqual({
      display: 'flex',
      flex: '1 1 0%',
      minHeight: '0',
      overflow: 'hidden',
    })
  })

  it('leaves the document pane its own basis, its floor and the artboard border', async () => {
    render(review({ ctx: serving().ctx }))
    await flush()

    // AC-3, on the pane element itself (`Recognition Review.dc.html:34`). `minHeight: 0` is
    // the half this subtask added to `ExtractionCanvas.tsx`'s `PANE`: without it this pane
    // grows to its ground's content and the ground never scrolls — the relationship
    // `EXTR11-E2E-06`'s zoom-150 `groundScrollsY` clause measures on the deployed build.
    // `borderRight` has no behavioural oracle at any layer; its only check is the fidelity
    // diff `EXTR11-E2E-11` (EXTR-11-09). Pinned here so it cannot vanish unremarked.
    expect(pick(canvasPane().style, ['flex', 'minWidth', 'minHeight', 'borderRight'])).toEqual({
      flex: '1 1 auto',
      minWidth: '0',
      minHeight: '0',
      borderRight: '1px solid var(--line-1)',
    })
  })

  it('leaves the fields pane its own basis, its floor and the artboard ground', async () => {
    render(review({ ctx: serving().ctx }))
    await flush()

    // AC-4 (`:223`). `minWidth: 470` is the relationship `EXTR11-E2E-02a`'s spill sweep and
    // `EXTR11-E2E-10`'s floor both protect; `minHeight: 0` is the half this subtask added to
    // `ExtractionFields.tsx`'s `PANE`, and `EXTR11-E2E-02b`'s
    // precondition is what would notice its absence. `background` is fidelity only —
    // `EXTR11-E2E-11`'s diff, nothing else.
    expect(pick(fieldsPane().style, ['flex', 'minWidth', 'minHeight', 'background'])).toEqual({
      flex: '1 1 620px',
      minWidth: '470px',
      minHeight: '0',
      background: 'var(--bg-1)',
    })
  })
})

// ==========================================================================================
// The state ladder (AC-5, AC-6, AC-7)
// ==========================================================================================

describe('the state ladder', () => {
  it('renders the shared Loading surface while the detail is in flight, and neither pane', async () => {
    const held = wire(() => new Promise<ExtractionDetail>(() => {}))
    render(review({ ctx: held.ctx }))
    await flush()

    // Floor: the screen really did ask, so the absence below is an unlanded fetch and not a
    // screen that never fetched.
    expect(held.asked(), 'the screen asked the wire for nothing').toEqual([JOB_ID])
    expect(document.querySelector(LOADING_SPINNER), 'the shared Loading surface must render').toBeTruthy()
    expect(screen.queryByTestId('extraction-canvas'), 'the document pane rendered over an unlanded fetch').toBeNull()
    expect(screen.queryByTestId('extraction-fields'), 'the fields pane rendered over an unlanded fetch').toBeNull()
  })

  it('renders ErrorState with a retry that fetches again and lands the panes', async () => {
    let attempt = 0
    const flaky = wire(async () => {
      attempt += 1
      if (attempt === 1) throw new ApiError('http', 'Not Found', 404)
      return mkDetail()
    })
    render(review({ ctx: flaky.ctx }))
    await flush()

    expect(screen.getByText(ERROR_HEADING), 'the shared ErrorState must render').toBeTruthy()
    expect(screen.queryByTestId('extraction-canvas'), 'the document pane rendered over a refused fetch').toBeNull()

    // "with a working retry": the control exists AND re-runs the fetch AND the screen
    // recovers. A rendered button that refetches nothing passes the first clause alone.
    const retry = screen.getByRole('button', { name: RETRY_LABEL })
    await act(async () => {
      fireEvent.click(retry)
    })
    await flush()

    expect(flaky.asked(), 'the retry fetched nothing').toEqual([JOB_ID, JOB_ID])
    expect(screen.queryByText(ERROR_HEADING), 'the error survived a successful retry').toBeNull()
    expect(frames(), 'the panes never landed after the retry').toHaveLength(3)
  })

  it.each([...UNSETTLED])('renders one sentence and no canvas for a %s job', async (state) => {
    // The floor and the needle in one fixture: the SAME pages and fields that render three
    // frames under `succeeded` are on the wire here. So "no canvas" is a real absence, not
    // the consequence of an empty document.
    const unsettled = mkDetail({ state })
    expect(unsettled.pages.length, 'the fixture carries no page — the absence below is vacuous').toBe(3)
    expect(unsettled.fields.length, 'the fixture carries no field — the absence below is vacuous').toBe(3)

    render(review({ ctx: serving(unsettled).ctx }))
    await flush()

    expect(screen.queryByTestId('extraction-canvas'), 'a job that has not settled rendered the document pane').toBeNull()
    expect(frames(), 'a job that has not settled rendered page frames').toHaveLength(0)

    // "one sentence" read literally: strip it and the screen has nothing else to say. This
    // is what refuses a stage name, a percentage or a duration beside it — the vocabulary
    // EXTR-12 owns — without hunting for each needle by name.
    expect(screen.getByText(STILL_READING), 'the still-reading sentence did not render').toBeTruthy()
    expect((root().textContent ?? '').replace(STILL_READING, '').trim()).toBe('')
  })

  it('renders one sentence and no canvas for a dead-lettered job', async () => {
    const dead = mkDetail({ state: 'dead_lettered' })
    expect(dead.pages.length, 'the fixture carries no page — the absence below is vacuous').toBe(3)

    render(review({ ctx: serving(dead).ctx }))
    await flush()

    // `D-9`: EXTR-15 owns the designed could-not-read screen. This is the minimum honest
    // placeholder, and it must not be the still-reading sentence — River is done retrying.
    expect(screen.queryByTestId('extraction-canvas'), 'a dead-lettered job rendered the document pane').toBeNull()
    expect(screen.getByText(COULD_NOT_READ), 'the could-not-read sentence did not render').toBeTruthy()
    expect(screen.queryByText(STILL_READING), 'a dead-lettered job is not still being read').toBeNull()
    expect((root().textContent ?? '').replace(COULD_NOT_READ, '').trim()).toBe('')
  })

  it('leaves exactly one of the migration’s states to the else-branch, and it is succeeded', () => {
    // The ladder ends in an else, so an unrecognised state renders BOTH panes over whatever
    // `pages`/`fields` the wire sent — measured: a `cancelled` job renders the toolbar, the
    // frames and the rows, claiming a document was read (QA Mode B, probe P10). `state` is
    // `string` on the wire (extractionReview.ts:51), so tsc cannot notice, and the four rungs
    // above are hand-transcribed from a CHECK constraint nothing links them to.
    //
    // The house idiom for a cross-language set nothing links at compile time is
    // wireMirrors.test.ts; this is that, one level down, over an enum rather than a key set.
    const sql = readFileSync(
      path.join(process.cwd(), '../../migrations/20260827084025_extraction_jobs.sql'),
      'utf8',
    )
    // Anchored on a symbol OTHER than the list being extracted, so a moved or renamed table
    // fails loudly instead of yielding [] and passing on two empty sets.
    expect(sql, 'the scan ran over a moved or renamed migration').toContain('CREATE TABLE extraction_jobs (')
    const clause = /CHECK \(state IN \(([^)]*)\)\)/.exec(sql)
    expect(clause, 'no state CHECK found — the comparison below is vacuous').not.toBeNull()
    const declared = Array.from((clause as RegExpExecArray)[1].matchAll(/'([a-z_]+)'/g), (m) => m[1])

    // Two floors: a non-empty population, and the one value the else-branch is FOR. Without
    // the second, an extractor that silently returned only `['succeeded']` would pass.
    expect(declared.length, 'the CHECK named no state').toBeGreaterThan(3)
    expect(declared, 'the CHECK does not name succeeded — the difference below means nothing').toContain('succeeded')

    // Every state the shell names, as a quoted literal. `succeeded` is deliberately absent
    // from the component: it is what the else-branch is for.
    const named = new Set(Array.from(source().matchAll(/'([a-z_]+)'/g), (m) => m[1]))
    expect(
      declared.filter((s) => !named.has(s)),
      'the shell names no rung for one of the migration’s states, so that state falls through to both panes and claims the document was read',
    ).toEqual(['succeeded'])
  })

  it('renders both panes for a settled job — the control the four rungs above are absences against', async () => {
    render(review({ ctx: serving(mkDetail({ state: 'succeeded' })).ctx }))
    await flush()

    expect(document.querySelector(LOADING_SPINNER), 'a settled job still showed the loading surface').toBeNull()
    expect(screen.queryByText(STILL_READING), 'a settled job claimed to be still reading').toBeNull()
    expect(screen.queryByText(COULD_NOT_READ), 'a settled job claimed it could not be read').toBeNull()
    expect(canvasPane()).toBeTruthy()
    expect(fieldsPane()).toBeTruthy()
    expect(frames(), 'the settled control rendered no frame').toHaveLength(3)
  })
})

// ==========================================================================================
// Selecting a field (AC-8) and D-25
// ==========================================================================================

describe('selecting a field', () => {
  it('sets the selection, requests that region’s page, and centres it in the ground', async () => {
    vi.useFakeTimers()
    silenceObserver()
    const bytes = pageFetch()
    render(review({ ctx: serving().ctx }))
    await flush()

    // Floor: with an observer installed and nothing crossing, the mount requested no bytes.
    // Without it every page is already loaded and the request below proves nothing.
    expect(SilentObserver.built, 'no observer was constructed — the pane loaded eagerly').toBeGreaterThan(0)
    expect(bytes.requested(), 'the mount requested page bytes before anything was selected').toEqual([])
    expect(scrollToSpy, 'the screen scrolled before anything was selected').not.toHaveBeenCalled()

    // invoice_date's region is on page 2 — neither the first page nor the row's own index.
    fireEvent.click(fieldRow('invoice_date'))
    await flush()

    expect(pressedRows(), 'the click did not set the selection').toEqual(['extraction-field-invoice_date'])
    expect(bytes.requested(), 'the selection did not request its own region’s page').toEqual([2])

    const node = highlights()
    expect(node, 'the selection drew no highlight').toHaveLength(1)
    expect(node[0].dataset.snip, 'the highlight is not tagged with the selected field').toBe('invoice_date')

    act(() => {
      vi.advanceTimersByTime(20)
    })

    expect(scrollToSpy, 'the selection never centred its region').toHaveBeenCalledTimes(1)
    expect(scrollToSpy.mock.instances[0], 'the screen scrolled something other than the ground').toBe(ground())
  })

  it('centres again when the already-selected row is clicked a second time', async () => {
    // `D-25`. EXTR-11-06 shipped its half: the pane re-reports rather than guarding on
    // `f.name !== selected` (ExtractionFields.test.tsx, "reports again when the
    // already-selected row is clicked"). This is the other half. `ExtractionCanvas`'s scroll
    // effect keys on `[selected, jobId]`, so a shell that only calls `setSelected(name)`
    // bails out of the re-render and the reader who scrolled away gets no re-centre — the
    // brief's "reachable in one action" is false on the second click. A per-click nonce or a
    // scroll call in the handler both satisfy this; neither is mandated.
    //
    // The nonce is also why re-selection is NOT made idempotent: `scrollRegionIntoView` is a
    // direct scrollTop assignment, so a repeat with the same selection recomputes the same
    // coordinates and costs nothing, while suppressing it would disable the feature the nonce
    // exists for.
    vi.useFakeTimers()
    silenceObserver()
    deferredPageFetch()
    render(review({ ctx: serving().ctx }))
    await flush()

    fireEvent.click(fieldRow('total_amount'))
    await flush()
    act(() => {
      vi.advanceTimersByTime(20)
    })
    expect(scrollToSpy, 'the first selection never centred').toHaveBeenCalledTimes(1)

    fireEvent.click(fieldRow('total_amount'))
    await flush()
    act(() => {
      vi.advanceTimersByTime(20)
    })

    expect(scrollToSpy, 'a second click on the selected row did not re-centre it').toHaveBeenCalledTimes(2)
    // The row stayed selected: a shell that toggled the selection off would also scroll
    // twice, and would be a different product.
    expect(pressedRows(), 'the second click cleared the selection instead of re-centring it').toEqual([
      'extraction-field-total_amount',
    ])
    expect(highlights(), 'the second click removed the highlight').toHaveLength(1)
  })

  it('does not centre again on a re-render that changed no selection', async () => {
    // The pair to the row above: `useEffect(() => scroll())` with no deps, or a scroll on
    // every render, satisfies D-25 and makes the ground jump under a reader who did nothing.
    vi.useFakeTimers()
    silenceObserver()
    deferredPageFetch()
    const served = serving()
    const { rerender } = render(review({ ctx: served.ctx }))
    await flush()

    fireEvent.click(fieldRow('invoice_number'))
    await flush()
    act(() => {
      vi.advanceTimersByTime(20)
    })
    expect(scrollToSpy, 'the selection never centred').toHaveBeenCalledTimes(1)

    rerender(review({ ctx: served.ctx }))
    await flush()
    act(() => {
      vi.advanceTimersByTime(50)
    })

    expect(scrollToSpy, 'a re-render that changed nothing scrolled the ground').toHaveBeenCalledTimes(1)
  })

  it('leaves nothing selected and nothing highlighted on first render', async () => {
    // The brief: "On load nothing is overlaid. No pile of detected rectangles."
    render(review({ ctx: serving().ctx }))
    await flush()

    expect(fieldRows(), 'no row rendered — the absence below is vacuous').toHaveLength(3)
    expect(pressedRows(), 'the screen opened with a row already selected').toEqual([])
    expect(highlights(), 'the screen opened with a highlight already drawn').toHaveLength(0)
  })
})

// ==========================================================================================
// The reset on a jobId change (AC-9) and the release on unmount (AC-10)
// ==========================================================================================

describe('switching to another job', () => {
  it('releases every handle, clears the selection, empties the page map and returns the ground to the top', async () => {
    silenceObserver()
    const served = serving()
    const { rerender } = render(review({ ctx: served.ctx, jobId: JOB_ID }))
    await flush()

    // Load exactly one page's bytes, through a selection: with the observer silent this is
    // the only byte request, so the map below has a known, non-empty population.
    fireEvent.click(fieldRow('invoice_number'))
    await flush()

    expect(pageImages(), 'no page image landed — every reset claim below is vacuous').toHaveLength(1)
    const held = urls()
    expect(held, 'no object URL was created — the release claim below is vacuous').toHaveLength(1)
    expect(revoked(), 'a handle was released before the job changed').toEqual([])
    expect(pressedRows()).toEqual(['extraction-field-invoice_number'])

    // MEASURED: jsdom's `scrollTop` stores what it is given. The read-back is the floor —
    // without it a property that always returned 0 would satisfy the assertion below.
    ground().scrollTop = 480
    expect(ground().scrollTop, 'jsdom did not keep the scroll position — the reset claim is vacuous').toBe(480)

    rerender(review({ ctx: served.ctx, jobId: OTHER_JOB_ID }))
    await flush()

    // `useAsync` on `[jobId]`: the screen re-reads under the new id rather than re-rendering
    // document 1's detail.
    expect(served.asked(), 'the screen did not re-read the detail under the new job id').toEqual([JOB_ID, OTHER_JOB_ID])

    // Every handle the replaced job held, released. Compared against the snapshot taken
    // BEFORE the change: a screen that reloads under the new id creates more, and those are
    // not this claim's subject.
    for (const url of held) expect(revoked(), `${url} outlived the job it belonged to`).toContain(url)
    expect(pressedRows(), 'document 2 opened carrying document 1’s selection').toEqual([])
    expect(highlights(), 'document 2 opened carrying document 1’s highlight').toHaveLength(0)
    expect(pageImages(), 'document 2 opened showing document 1’s page bytes').toHaveLength(0)

    // AC-9's last clause, stated where the AC states it. It does NOT discriminate here: a
    // shell that shows `Loading` between the two details remounts the pane, and a fresh
    // ground reads 0 whatever the effect does. The discriminating row is
    // ExtractionCanvas.test.tsx, "returns the ground to the top on a jobId change" — same
    // contract, asserted on a ground proven to be the same node. `D-24`.
    expect(ground().scrollTop, 'document 2 opened at document 1’s scroll position').toBe(0)
  })

  it('does not hand document 1 its old selection back on the way to it', async () => {
    // A -> B -> A with the pane never unmounted. The tag is what clears the selection on the
    // A -> B leg; if it only gates the READ, the pick survives in state and `pick.jobId ===
    // jobId` becomes true again on the way back, so document A reopens with a row selected
    // and its region centred — against AC-10's "clears `selected`" and against §6's "nothing
    // selected (first render) — no highlight anywhere".
    //
    // Unreachable from today's routing (`P-43`: the entry control is on `InvoiceDetail`, so a
    // second document is a fresh mount). Closed rather than recorded because the branch is
    // inside this subtask's own state and costs one line, not because the path exists.
    silenceObserver()
    const two = wire(async (id) => mkDetail({ id, state: 'succeeded' }))
    const { rerender } = render(review({ ctx: two.ctx, jobId: JOB_ID }))
    await flush()

    fireEvent.click(fieldRow('invoice_number'))
    await flush()
    // Floor: the selection really was made, so its absence at the end is a fact.
    expect(pressedRows(), 'the click did not select — every claim below is vacuous').toEqual([
      'extraction-field-invoice_number',
    ])
    expect(highlights(), 'the click drew no highlight — the absence below is vacuous').toHaveLength(1)

    rerender(review({ ctx: two.ctx, jobId: OTHER_JOB_ID }))
    await flush()
    expect(pressedRows(), 'document 2 opened carrying document 1’s selection').toEqual([])

    rerender(review({ ctx: two.ctx, jobId: JOB_ID }))
    await flush()

    // The control: the panes really did come back, so "nothing selected" is a live screen and
    // not an unmounted one.
    expect(fieldRows(), 'document 1 rendered no row on the way back').toHaveLength(3)
    expect(pressedRows(), 'document 1 reopened carrying the selection it was left with').toEqual([])
    expect(highlights(), 'document 1 reopened carrying the highlight it was left with').toHaveLength(0)
  })

  it('releases every page handle on unmount', async () => {
    const bytes = pageFetch()
    const { unmount } = render(review({ ctx: serving().ctx }))
    await flush()

    // No observer: `ExtractionCanvas` loads every frame at mount, so all three handles exist.
    expect(bytes.requested().sort(), 'the mount loaded no page — the release claim is vacuous').toEqual([1, 2, 3])
    expect(urls(), 'no object URL was created — the release claim is vacuous').toHaveLength(3)
    expect(revoked(), 'a handle was released while the screen was still mounted').toEqual([])

    unmount()

    expect(revoked().sort(), 'the screen leaked a page handle on unmount').toEqual(urls().sort())
  })
})

// ==========================================================================================
// Nothing portalled (AC-11), and zoom stays out of the shell (D-23)
// ==========================================================================================

describe('the screen renders inside its own tree', () => {
  /** Every element in the document that the screen's own container does not contain. */
  function outside(container: HTMLElement): string[] {
    return Array.from(document.body.querySelectorAll('*'))
      .filter((el) => !container.contains(el))
      .map((el) => `${el.tagName.toLowerCase()}${el.getAttribute('data-testid') ? `[${el.getAttribute('data-testid')}]` : ''}`)
  }

  it('renders every node inside its own root, never through a portal', async () => {
    // The control needle FIRST: `outside()` must be able to see a portal, or the absence it
    // reports for the real screen means nothing.
    function Portalled() {
      return <div>{createPortal(<span data-testid="portal-needle" />, document.body)}</div>
    }
    const control = render(<Portalled />)
    expect(outside(control.container), 'the detector cannot see a portal — every claim below is vacuous').toContain(
      'span[portal-needle]',
    )
    control.unmount()
    control.container.remove() // `unmount` empties the container; `cleanup` is what removes it.

    const { container } = render(review({ ctx: serving().ctx }))
    await flush()

    // Floor: a screen that rendered almost nothing leaves nothing outside either.
    expect(container.querySelectorAll('*').length, 'the screen rendered almost nothing').toBeGreaterThan(20)
    expect(within(container).getByTestId('extraction-review')).toBeTruthy()

    // Every `--bg-*` / `--fg-*` / `--status-*` token is declared on `.asc-app`
    // (packages/design-tokens/app-layer.css:18). Markup outside that tree resolves none of
    // them, so a portalled node renders unstyled on the deployed build and jsdom, which
    // applies no CSS at all, would never say so.
    expect(outside(container), 'the screen rendered outside its own root').toEqual([])
  })

  it('keeps zoom out of the shell', () => {
    // `D-23`: zoom is `useState` inside `ExtractionCanvas` and survives a jobId change, so a
    // reader working through a queue does not re-click 150% on every document. Pinned from
    // the other side by ExtractionCanvas.test.tsx, "keeps the zoom the reader chose across a
    // jobId change" — which cannot see a shell that lifts the state up here.
    // Matched on the declaration and the hand-down, never on the word: a comment saying zoom
    // lives in the canvas is the right comment to write and must not fail this row.
    expect(source(), 'the shell declares or hands down zoom state').not.toMatch(/\bsetZoom\b|\bZOOMS\b|\bzoom=\{/)
  })
})

// ==========================================================================================
// EXTR-12-07. One shared draft, one Save, N POSTs. Written RED against the SHIPPED shell,
// which holds no draft and renders no Save — so every row fails on the write it is about.
//
// `serving()` replies an ExtractionDetail to ANY url, a POST included, so every row below
// declares its own reply rather than letting a detail object arrive typed as a correction.
// ==========================================================================================

const SAVE = 'Save what you settled'

const AMBIGUOUS_JOB: ExtractionDetail = mkDetail({
  fields: [
    mkField({
      name: 'issue_date',
      value: '2026-01-01',
      region: mkRegion({ page: 1 }),
      reason: 'ambiguous',
      alternatives: [
        { value: '2026-01-10', region: mkRegion({ page: 2 }) },
        { value: '2026-10-01', region: mkRegion({ page: 3 }) },
      ],
    }),
    mkField({ name: 'subtotal', value: '950.00', region: mkRegion({ page: 1 }) }),
    mkField({ name: 'total', value: '1000.00', region: mkRegion({ page: 1 }) }),
  ],
})

const CORRECTED_JOB: ExtractionDetail = mkDetail({
  fields: [
    mkField({
      name: 'total',
      value: '1,500.00',
      region: mkRegion({ page: 1 }),
      corrected: { method: 'typed', was: '1000.00', where: null },
    }),
    mkField({ name: 'subtotal', value: '950.00', region: mkRegion({ page: 1 }) }),
  ],
})

/** The 201 body the correction route answers, so a POST reply is never a detail object. */
function mkCorrectionResponse(field: string, value: string, method: string): Record<string, unknown> {
  return {
    id: 'ec1f2a3b-4c5d-4e6f-8a9b-0c1d2e3f4a5b',
    field_name: field,
    value,
    method,
    region: null,
    invoice_id: '5d2f7a10-6b3c-4e8d-9f01-2a3b4c5d6e7f',
    created_at: '2026-09-01T09:00:00Z',
  }
}

/** Answers the detail GET with `detail` and every POST with a 201-shaped correction body. */
function writing(detail: ExtractionDetail, onPost?: (url: string, body: unknown) => Promise<unknown>): Wire {
  return wire(async (_last, url, opts) => {
    if ((opts?.method ?? 'GET') === 'GET') return detail
    if (onPost) return onPost(url, opts?.body)
    return mkCorrectionResponse('total', '1', 'typed')
  })
}

describe('one shared draft, one Save', () => {
  it('drafts a chosen chip and posts nothing until Save', async () => {
    // The story's own Description: "no per-field Save. One shared draft across the pane and one
    // Save action ... Choosing a candidate, typing a value and Undo all write through the same
    // draft." A chip that POSTs on click is the shape the subtask's Test Specs assumed and the
    // Description forbids.
    const w = writing(AMBIGUOUS_JOB)
    render(review({ ctx: w.ctx }))
    await flush()

    const chips = chipsOf('issue_date')
    expect(chips.length, 'the ambiguous field rendered no chip — every claim below is vacuous').toBe(3)

    fireEvent.click(chips[2])
    await flush()

    expect(writes(w), 'the chip click wrote to the register before anyone pressed Save').toEqual([])
    expect(saveButton(), 'the screen renders no Save button').toBeTruthy()
    expect(saveButton()!.disabled, 'a drafted chip did not arm Save').toBe(false)
  })

  it('disables Save while nothing is drafted, and neutralises its filter', async () => {
    // `.v2-btn-primary:hover` sets `filter: brightness(1.22)` with no `:disabled` guard
    // (app-layer.css:213), so a disabled Save brightens under the cursor and reads enabled.
    // There is NO disabled reason: the only condition is "nothing settled yet", which is
    // self-evident, and both shipped precedents disable without one.
    const w = writing(AMBIGUOUS_JOB)
    render(review({ ctx: w.ctx }))
    await flush()

    const save = saveButton()
    expect(save, 'the screen renders no Save button').toBeTruthy()
    expect(save!.textContent, 'the Save control is unlabelled').toBe(SAVE)
    expect(save!.disabled, 'Save is pressable with an empty draft').toBe(true)
    expect(save!.style.filter, 'a disabled Save still brightens on hover and reads enabled').toBe('none')
    expect(save!.hasAttribute('title'), 'the Save control hides something in a tooltip').toBe(false)
  })

  it('sends one POST per drafted field in vocabulary order, and keeps what did not commit', async () => {
    // Two fields typed, the first accepted and the second refused with a sentence that appears
    // in no SPA constant. A status->copy table cannot render it, and a run that fires both
    // POSTs in parallel cannot promise the order the append-only table records them in.
    const REFUSAL = 'the invoice refused this value'
    const w = writing(mkDetail({ fields: AMBIGUOUS_JOB.fields }), async (url) => {
      if (url.includes('/fields/subtotal/')) return mkCorrectionResponse('subtotal', '1,111.00', 'typed')
      throw new ApiError('http', REFUSAL, 400)
    })
    render(review({ ctx: w.ctx }))
    await flush()

    const subtotal = inputOf('subtotal')
    const total = inputOf('total')
    expect(subtotal && total, 'the pane renders no inputs — every claim below is vacuous').toBeTruthy()

    fireEvent.change(subtotal as HTMLInputElement, { target: { value: '1,111.00' } })
    fireEvent.change(total as HTMLInputElement, { target: { value: '2,222.00' } })
    await flush()

    await act(async () => {
      fireEvent.click(saveButton() as HTMLElement)
    })
    await flush()

    const posted = writes(w)
    expect(
      posted.map((c) => c.url.split('/fields/')[1]?.split('/')[0]),
      'the POSTs do not follow the vocabulary order the person reads',
    ).toEqual(['subtotal', 'total'])
    expect(posted.every((c) => c.method === 'POST'), 'a correction was sent with the wrong verb').toBe(true)

    // The committed half comes back from the server; the refused one keeps the person's own
    // typing, because it is not the screen's to discard.
    expect(inputOf('total')!.value, 'the refused field lost the typing nobody accepted').toBe('2,222.00')
    expect(
      screen.queryByTestId('extraction-write-error')?.textContent,
      "the server's own sentence was not rendered verbatim",
    ).toBe(REFUSAL)
  })

  it('discards a blank typed entry when the save that dropped it commits another field', async () => {
    // `savableCorrections` never posts a blank, so a blank entry is never in `committed`. A
    // handler keeping every UNCOMMITTED entry therefore keeps it forever, and `applyDraft`
    // re-lays it over every fresh read: the cell denies a value the register still holds, and
    // Save is disabled again so no gesture is left that clears it. Keep an entry only if it
    // was POSTED and did not commit.
    const w = writing(
      mkDetail({
        fields: [
          mkField({ name: 'subtotal', value: '950.00', region: mkRegion({ page: 1 }) }),
          mkField({ name: 'total', value: '1000.00', region: mkRegion({ page: 1 }) }),
        ],
      }),
      async (_url, body) => mkCorrectionResponse('total', String((body as { value?: string }).value ?? ''), 'typed'),
    )
    render(review({ ctx: w.ctx }))
    await flush()

    const subtotal = inputOf('subtotal')
    const total = inputOf('total')
    expect(subtotal && total, 'the pane renders no inputs — every claim below is vacuous').toBeTruthy()

    fireEvent.change(subtotal as HTMLInputElement, { target: { value: '' } })
    fireEvent.change(total as HTMLInputElement, { target: { value: '2,222.00' } })
    await flush()
    expect(inputOf('subtotal')!.value, 'the blank never reached the draft, so the save below proves nothing').toBe('')

    await act(async () => {
      fireEvent.click(saveButton() as HTMLElement)
    })
    await flush()

    // The floor: exactly one POST, for the field that carried a value.
    expect(
      writes(w).map((c) => c.url.split('/fields/')[1]?.split('/')[0]),
      'the blank was posted, or the field beside it was not',
    ).toEqual(['total'])
    expect(
      screen.queryByTestId('extraction-write-error'),
      'the save was refused, so nothing below is about the blank',
    ).toBeNull()

    // The claim. A kept blank survives the post-save re-read and every one after it.
    expect(
      inputOf('subtotal')!.value,
      'the dropped blank outlived the save and the cell now denies a value the register holds',
    ).toBe('950.00')
  })

  it('re-reads after a write and never blanks while it does', async () => {
    // `asyncReducer`'s start arm returns `{ status: 'loading', data: null }` and the shell
    // renders `<Loading/>` on `data === null`, so a `detail.run()` re-read unmounts BOTH panes,
    // re-fetches every page image and drops the canvas scroll. A run() implementation PASSES
    // the second-GET clause and fails the never-blank one — that pair is this row's whole point.
    const w = writing(AMBIGUOUS_JOB)
    render(review({ ctx: w.ctx }))
    await flush()

    const before = w.calls().filter((c) => c.method === 'GET').length
    expect(before, 'the screen never read the detail — the count below is vacuous').toBeGreaterThan(0)

    const chips = chipsOf('issue_date')
    expect(chips.length, 'the ambiguous field rendered no chip').toBe(3)
    fireEvent.click(chips[1])
    await flush()

    let blanked = false
    for (let i = 0; i < 8; i += 1) {
      await act(async () => {
        if (i === 0) fireEvent.click(saveButton() as HTMLElement)
        await Promise.resolve()
      })
      if (screen.queryByTestId('extraction-fields') === null) blanked = true
      if (document.querySelector(LOADING_SPINNER) !== null) blanked = true
    }
    await flush()

    expect(
      w.calls().filter((c) => c.method === 'GET').length,
      'the screen never re-read the detail, so the settled state on screen is the client’s guess',
    ).toBeGreaterThan(before)
    expect(blanked, 'the re-read unmounted both panes and threw away the canvas scroll').toBe(false)
  })

  it('drafts an Undo and posts one undone correction on Save, never a DELETE', async () => {
    const w = writing(CORRECTED_JOB, async () => mkCorrectionResponse('total', '1,500.00', 'undone'))
    render(review({ ctx: w.ctx }))
    await flush()

    const undo = screen.queryByTestId('extraction-undo-total')
    expect(undo, 'the corrected field offers no way back — every claim below is vacuous').toBeTruthy()

    fireEvent.click(undo as HTMLElement)
    await flush()
    expect(writes(w), 'Undo wrote to the register before anyone pressed Save').toEqual([])

    // The artboard's own draft rule (`:634`): the value resets before Save, not after it.
    expect(inputOf('total')!.value, 'the drafted undo still shows the value it is about to discard').toBe('1000.00')

    await act(async () => {
      fireEvent.click(saveButton() as HTMLElement)
    })
    await flush()

    const posted = writes(w)
    expect(posted, 'Save sent no correction for the drafted undo').toHaveLength(1)
    expect((posted[0].body as { method?: string }).method, 'the undo was recorded as some other method').toBe('undone')
    expect(posted[0].url, 'the undo was posted to another field').toContain('/fields/total/corrections')

    // Vacuous on its own; non-vacuous only because the clause above proves a request was made.
    expect(
      w.calls().map((c) => c.method),
      'a correction was deleted; the table is append-only and an undo is a new row',
    ).not.toContain('DELETE')
  })
})
