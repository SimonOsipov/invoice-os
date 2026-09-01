// @vitest-environment jsdom
// Per-file opt-in: vitest.config.ts stays `environment: 'node'` for every other suite.
//
// EXTR-11-05. Started as Mode A red specs; `ExtractionCanvas.tsx` now exists and every row
// below is green against it. Every row was first proven to fail on
// its OWN assertion, and to go green, against a throwaway reference build that was deleted
// before this commit; 22 mutants of that build were each caught by the row that names them.
//
// MEASURED jsdom 27.4.0 serialization — every one of these bit a draft of this file:
//   `background: 'oklch(72% .15 65 / .32)'`  reads back `oklch(0.72 0.15 65 / 0.32)`
//   `margin: '0 auto 18px'`                  reads back `0px auto 18px`
//   `padding: 0`                             reads back `0px`, but `minHeight: 0` reads `0`
//   `flex: 1` reads `1 1 0%`; `flex: 'none'` reads `0 0 auto`
//   `boxShadow`, `transition`, `border`, `aspectRatio`, `width` round-trip raw
//   setting `overflowY` alone leaves `style.overflow` EMPTY — which is what makes the
//   ground's "both axes" assertion a real oracle rather than a restatement
// jsdom implements NO `IntersectionObserver`, NO `Element.prototype.scrollTo` and NO
// `scrollIntoView`. `scrollRegionIntoView` calls `scrollTo`, so this file installs one.
// That is the artboard's own call (`Recognition Review.dc.html:513`), smooth behaviour and
// all; System Design §5 proposed `scrollTop` to avoid this shim and lost the smooth scroll
// doing it. The reference wins, so the shim is permanent — EXTR-11-06 and -07 need it too.
//
// Style oracles go through `serialized()` — the shipped helper's own object, rendered and
// read back — so a spec can never disagree with `extractionReview.ts` about a literal.

import { readFileSync } from 'node:fs'
import path from 'node:path'
import { StrictMode, type CSSProperties } from 'react'

import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi, type MockInstance } from 'vitest'

import * as glyphs from '../glyphs'
import {
  docMetaLine,
  highlightStyle,
  pageFrameStyle,
  type ExtractionDocument,
  type ExtractionFieldState,
  type ExtractionPage,
  type ExtractionRegion,
} from '../lib/extractionReview'
import type { PlatformCtx } from '../types'
import { ExtractionCanvas } from './ExtractionCanvas'
import { fileTypeTone, formatLabel } from './SourceDocumentStates'

const JOB_ID = 'c3d4e5f6-a7b8-4c3d-9e4f-5a6b7c8d9e0f'
const OTHER_JOB_ID = 'a1b2c3d4-e5f6-4a1b-8c2d-3e4f5a6b7c8d'

// System Design §## Decisions -> Invented copy. Literals, never a regex: these three
// sentences are the story's own and a paraphrase is a different product.
const NO_REGION = 'We have no region for this field, so there is nothing to highlight.'
const NO_PAGES = 'This document has no page images.'
const PAGE_FAILED = 'This page could not be loaded.'

// ErrorState's hardcoded heading (api-client/src/components/ErrorState.tsx:45). AC-16 makes
// the zero-pages state normal, so this string must be absent from that render.
const ERROR_STATE_HEADING = 'Something went wrong'

// SourceDocumentPages.test.tsx:31's list, verbatim: both classes force `border-radius`
// with `!important`, from two different rules (app-layer.css:193-197 and :275).
const RADIUS_FORCING = ['pf-btn', 'pf-chip', 'v2-btn', 'ops-btn', 'dev-btn', 'ops-chip', 'dev-chip']

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

// Three pages, deliberately out of order on the wire, and three fields whose regions land
// on three different pages. Every "absence" row below floors against this fixture.
const THREE_PAGES: ExtractionPage[] = [A4, mkPage({ page: 1 }), mkPage({ page: 3 })]
const THREE_FIELDS: ExtractionFieldState[] = [
  mkField({ name: 'invoice_number', region: mkRegion({ page: 1 }) }),
  mkField({ name: 'invoice_date', region: mkRegion({ page: 2, x0: 0.1, y0: 0.3, x1: 0.38, y1: 0.35 }) }),
  mkField({ name: 'total_amount', region: mkRegion({ page: 3, x0: 0.62, y0: 0.7, x1: 0.9, y1: 0.76 }) }),
]

// Typed against the real PlatformCtx so a rename breaks the typecheck; the cast stands in
// for the ~90 fields the pane never touches (SourceDocumentPages.test.tsx:58's idiom).
type CanvasCtx = Pick<PlatformCtx, 'getToken'>

// ONE instance for the whole file, never a fresh object per render. A ctx rebuilt on every
// call changes the identity of anything the pane memoises on it, which re-runs its effects
// on a re-render that changed nothing -- and one row below asserts exactly that it does not.
const CTX: PlatformCtx = { getToken: () => 'tok' } satisfies CanvasCtx as unknown as PlatformCtx

interface CanvasProps {
  ctx: PlatformCtx
  jobId: string
  /** `detail.document`. Named `doc`, not `document`: a prop called `document` shadows the global. */
  doc: ExtractionDocument
  pages: ExtractionPage[]
  fields: ExtractionFieldState[]
  selected: string | null
  /** The caller's per-click nonce. Held constant unless a row is asserting on it. */
  scrollNonce: number
}

function canvas(over: Partial<CanvasProps> = {}) {
  const props: CanvasProps = {
    ctx: CTX,
    jobId: JOB_ID,
    doc: mkDocument(),
    pages: THREE_PAGES,
    fields: THREE_FIELDS,
    selected: null,
    scrollNonce: 0,
    ...over,
  }
  return <ExtractionCanvas {...props} />
}

// -- style oracles ----------------------------------------------------------------------

// Renders a style object and reads it back, so every comparison below is
// serialization-for-serialization. A spec can therefore never disagree with
// extractionReview.ts about a literal -- the values themselves are pinned at their source
// in extractionReview.test.ts, and this only asserts the pane spreads them.
function serialized(style: CSSProperties, keys: readonly string[]): Record<string, string> {
  const probe = render(<div data-testid="style-probe" style={style} />)
  const declared = pick(probe.getByTestId('style-probe').style, keys)
  probe.unmount()
  return declared
}

function pick(style: CSSStyleDeclaration, keys: readonly string[]): Record<string, string> {
  const out: Record<string, string> = {}
  for (const k of keys) out[k] = (style as unknown as Record<string, string>)[k]
  return out
}

const FRAME_KEYS = [
  'width',
  'minWidth',
  'maxWidth',
  'aspectRatio',
  'margin',
  'position',
  'padding',
  'background',
  'border',
  'boxShadow',
] as const

const HIGHLIGHT_KEYS = [
  'position',
  'pointerEvents',
  'left',
  'top',
  'width',
  'height',
  'background',
  'boxShadow',
  'borderRadius',
  'transition',
] as const

/** The nearest ancestor carrying a dashed inline border, or null. The app's panel invariant. */
function dashedAncestor(el: HTMLElement): HTMLElement | null {
  for (let node: HTMLElement | null = el; node; node = node.parentElement) {
    if (/^1px dashed /.test(node.style.border)) return node
  }
  return null
}

// -- the driving IntersectionObserver -----------------------------------------------------

// jsdom implements none, and the repo's only existing stub
// (landing/src/components/Nav.scrollSpy.test.tsx:16-20) is a no-op that never invokes its
// callback -- it cannot tell "loads when observed" from "loads at mount". This one records
// its targets so a spec can fire a crossing for ONE frame and count the fetches.
type EntryLike = { target: Element; isIntersecting: boolean; intersectionRatio: number }
type ObserverCallback = (entries: EntryLike[], observer: unknown) => void
type ObserverInit = { root?: Element | Document | null; rootMargin?: string; threshold?: number | number[] }

class DrivingObserver {
  static instances: DrivingObserver[] = []
  readonly targets = new Set<Element>()
  disconnected = 0

  constructor(
    readonly cb: ObserverCallback,
    readonly options: ObserverInit = {},
  ) {
    DrivingObserver.instances.push(this)
  }

  observe(el: Element): void {
    this.targets.add(el)
  }
  unobserve(el: Element): void {
    this.targets.delete(el)
  }
  disconnect(): void {
    this.disconnected += 1
    this.targets.clear()
  }
  takeRecords(): EntryLike[] {
    return []
  }
}

function installObserver(): void {
  DrivingObserver.instances = []
  vi.stubGlobal('IntersectionObserver', DrivingObserver)
}

/** Deletes the constructor outright -- the AC-7 environment, and jsdom's own default. */
function removeObserver(): void {
  vi.stubGlobal('IntersectionObserver', undefined)
  delete (globalThis as { IntersectionObserver?: unknown }).IntersectionObserver
}

function observerWatching(el: Element): DrivingObserver {
  const found = DrivingObserver.instances.find((o) => o.targets.has(el))
  expect(found, 'no observer is watching that frame').toBeDefined()
  return found as DrivingObserver
}

function cross(el: Element, isIntersecting = true): void {
  const obs = observerWatching(el)
  act(() => {
    obs.cb([{ target: el, isIntersecting, intersectionRatio: isIntersecting ? 1 : 0 }], obs)
  })
}

// -- the page-bytes transport --------------------------------------------------------------

// `Blob.prototype.arrayBuffer` is undefined under this jsdom, but fetchPageImage calls
// `res.arrayBuffer()` on the RESPONSE, which is entirely ours
// (SourceDocumentPages.test.tsx:86-90's measured note).
function pageOf(url: string): number {
  const m = /\/pages\/(\d+)(?:\?|$)/.exec(url)
  expect(m, `not a page-image URL: ${url}`).not.toBeNull()
  return Number((m as RegExpExecArray)[1])
}

interface PageFetch {
  mock: ReturnType<typeof vi.fn>
  /** Every page number requested, in dispatch order. */
  requested: () => number[]
}

function pageFetch(opts: { failing?: number[] } = {}): PageFetch {
  const failing = opts.failing ?? []
  const requested: number[] = []
  const mock = vi.fn(async (url: string) => {
    const page = pageOf(url)
    requested.push(page)
    if (failing.includes(page)) return { ok: false, status: 404, statusText: 'Not Found' }
    return { ok: true, status: 200, statusText: 'OK', arrayBuffer: async () => new ArrayBuffer(8) }
  })
  vi.stubGlobal('fetch', mock)
  return { mock, requested: () => [...requested] }
}

/** Holds every page request open, so a handle can be made to land after unmount. */
function deferredPageFetch(): { open: () => void; requested: () => number[] } {
  let open!: () => void
  const gate = new Promise<void>((r) => {
    open = r
  })
  const requested: number[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      requested.push(pageOf(url))
      await gate
      return { ok: true, status: 200, statusText: 'OK', arrayBuffer: async () => new ArrayBuffer(8) }
    }),
  )
  return { open, requested: () => [...requested] }
}

// fetchPageImage awaits fetch, then res.arrayBuffer(), then the caller's own .then -- four
// microtask ticks before a handle reaches state. Drained generously: a thin flush turns a
// correct implementation red for being one tick slower than this helper.
async function flush(): Promise<void> {
  await act(async () => {
    for (let i = 0; i < 8; i += 1) await Promise.resolve()
  })
}

// -- DOM readers -----------------------------------------------------------------------

function frames(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>('[data-testid^="extraction-page-"]')).filter((el) =>
    /^extraction-page-\d+$/.test(el.dataset.testid ?? ''),
  )
}

function frameIds(): string[] {
  return frames().map((el) => el.dataset.testid ?? '')
}

function frame(n: number): HTMLElement {
  return screen.getByTestId(`extraction-page-${n}`)
}

function highlights(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>('[data-testid="extraction-highlight"]'))
}

function images(): HTMLElement[] {
  return Array.from(document.querySelectorAll<HTMLElement>('[data-testid^="extraction-page-image-"]'))
}

function zoomPressed(): Record<string, string | null> {
  return {
    50: screen.getByTestId('extraction-zoom-50').getAttribute('aria-pressed'),
    100: screen.getByTestId('extraction-zoom-100').getAttribute('aria-pressed'),
    150: screen.getByTestId('extraction-zoom-150').getAttribute('aria-pressed'),
  }
}

let createSpy: MockInstance<typeof URL.createObjectURL>
let revokeSpy: MockInstance<typeof URL.revokeObjectURL>
let scrollToSpy: ReturnType<typeof vi.fn>

function urls(): string[] {
  return createSpy.mock.results.map((r) => String(r.value))
}

function revoked(): string[] {
  return revokeSpy.mock.calls.map((c) => c[0])
}

// jsdom 27.4.0 implements NO `Element.prototype.scrollTo`, and `scrollRegionIntoView` calls
// it (extractionReview.ts:151). Its `setTimeout(…, 20)` fires on the REAL clock in every spec
// that does not fake timers.
//
// Installed ONCE, for the file's whole lifetime, and never removed: the last spec's timer
// fires after that spec's afterEach, so an install/remove pair around each spec leaves a
// window with no shim and throws an uncaught TypeError into the run.
//
// The shim only stops the throw; the stale CALL still arrives, on the earlier spec's detached
// ground, and would inflate whatever the running spec counts. `isConnected` drops exactly
// those -- every scroll the file counts is a scroll onto a mounted ground. It reads
// `scrollToSpy` at call time, so beforeEach's fresh spy is the one each spec observes.
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
  // jsdom 27.4.0 DOES implement createObjectURL, so these spy and restore rather than
  // assign-and-delete, which would strip a working global for the rest of the worker.
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
  DrivingObserver.instances = []
})

// ==========================================================================================
// Frames (AC-1, AC-2, AC-3, AC-4)
// ==========================================================================================

describe('the page frames', () => {
  it('renders one frame per page row, in page order', () => {
    render(canvas())

    // The fixture arrives 2,1,3 on the wire. Ascending order is the pane's, not the server's.
    expect(THREE_PAGES.map((p) => p.page), 'the fixture is already sorted -- this row proves nothing').toEqual([2, 1, 3])
    expect(frameIds()).toEqual(['extraction-page-1', 'extraction-page-2', 'extraction-page-3'])
  })

  it('gives each frame the stored aspect ratio, not a constant', () => {
    render(canvas())

    // Two different grids: a hardcoded '1275 / 1651' passes the first and fails the second.
    expect(frame(1).style.aspectRatio).toBe('1275 / 1651')
    expect(frame(2).style.aspectRatio).toBe('1240 / 1754')
  })

  it('spreads pageFrameStyle for every key, rather than re-declaring it', () => {
    render(canvas())

    expect(frames(), 'no frame rendered -- every comparison below would be vacuous').toHaveLength(3)
    expect(pick(frame(1).style, FRAME_KEYS)).toEqual(serialized(pageFrameStyle(mkPage({ page: 1 }), 1), FRAME_KEYS))
    expect(pick(frame(2).style, FRAME_KEYS)).toEqual(serialized(pageFrameStyle(A4, 1), FRAME_KEYS))
  })

  it('carries no transform on any frame or image', async () => {
    const bytes = pageFetch()
    render(canvas())
    await flush()

    // Floor first: a render that produced nothing passes every absence below.
    expect(frames(), 'no frame rendered').toHaveLength(3)
    expect(images(), 'no image rendered').toHaveLength(3)
    expect(bytes.requested().sort(), 'the floor above must be real bytes, not empty frames').toEqual([1, 2, 3])

    // The idiom this pane copies carries `transform: 'rotate(-1.1deg)'`
    // (SourceDocumentPages.tsx:108). A rotated frame lands every percentage off its region.
    for (const el of [...frames(), ...images()]) {
      expect(el.style.transform, `${el.dataset.testid} carries a transform`).toBe('')
    }
  })

  it('sets padding to zero on the positioned frame', () => {
    render(canvas())

    expect(frames(), 'no frame rendered').toHaveLength(3)
    for (const el of frames()) {
      // CSS 2.1 §10.1: the absolutely-positioned highlight resolves its percentages against
      // the padding box, the in-flow image its width against the content box. Padding
      // separates the two and every highlight drifts.
      expect(el.style.padding, `${el.dataset.testid} is padded`).toBe('0px')
      expect(el.style.position).toBe('relative')
    }
  })

  it("keeps the artboard's page card on the frame itself, with no chrome wrapper", () => {
    render(canvas())

    const f = frame(1)
    expect(f.style.border).toBe('1px solid var(--line-2)')
    expect(f.style.boxShadow).toBe('0 1px 3px oklch(20% .02 210 / .08)')
    // MEASURED: jsdom rewrites the bare zero. The authored literal is '0 auto 18px'.
    expect(f.style.margin).toBe('0px auto 18px')
    expect(f.style.background).toBe('rgb(255, 255, 255)')
  })

  it('puts the image directly in the frame at the artboard width', async () => {
    render(canvas())
    await flush()

    const img = within(frame(1)).getByTestId('extraction-page-image-1')
    expect(img.tagName).toBe('IMG')
    expect(img.style.width).toBe('100%')
    expect(img.style.display).toBe('block')
    expect(img.getAttribute('src'), 'the frame rendered no bytes').toMatch(/^blob:/)
    // No wrapper for chrome (AC-3): the image's parent IS the frame.
    expect(img.parentElement).toBe(frame(1))
  })
})

// ==========================================================================================
// The ground (AC-5)
// ==========================================================================================

describe('the ground', () => {
  it('scrolls on both axes, and contains itself', () => {
    render(canvas())

    const ground = screen.getByTestId('extraction-ground')
    // MEASURED: setting `overflowY` alone leaves `style.overflow` EMPTY under this jsdom,
    // so this assertion really does refuse a single-axis scroller.
    expect(ground.style.overflow, 'a single-axis scroller leaves the shorthand empty').toBe('auto')
    expect(ground.style.minHeight).toBe('0')
    expect(ground.style.flex).toBe('1 1 0%') // MEASURED: React writes `flex: 1` as the triple
  })

  it("pads the ground's inner surface with the artboard's values", () => {
    render(canvas())

    const ground = screen.getByTestId('extraction-ground')
    const inner = ground.firstElementChild as HTMLElement | null
    expect(inner, 'the ground has no inner pad').not.toBeNull()

    expect(inner!.style.padding).toBe('24px 24px 40px')
    expect(inner!.style.background).toBe('var(--bg-0)')
    expect(inner!.style.minHeight).toBe('100%')
    expect(inner!.style.position).toBe('relative')
    // Floor: the pad really is the thing holding the frames, not a sibling.
    expect(inner!.querySelectorAll('[data-testid^="extraction-page-"]').length).toBe(3)
  })
})

// ==========================================================================================
// Zoom (AC-8)
// ==========================================================================================

describe('zoom', () => {
  it('moves the frame width and its band, and adds no transform', () => {
    render(canvas())

    expect(frame(1).style.width).toBe('100%')
    fireEvent.click(screen.getByTestId('extraction-zoom-150'))

    expect(frames(), 'the frames vanished at 150%').toHaveLength(3)
    for (const f of frames()) {
      expect(f.style.width).toBe('150%')
      expect(f.style.minWidth).toBe('840px')
      expect(f.style.maxWidth).toBe('960px')
      // A scale leaves the layout box alone, so the ground would never scroll to reveal
      // the enlarged page.
      expect(f.style.transform).toBe('')
    }
    // The one thing zoom must NOT move.
    expect(frame(1).style.aspectRatio).toBe('1275 / 1651')

    fireEvent.click(screen.getByTestId('extraction-zoom-50'))
    expect(pick(frame(1).style, FRAME_KEYS)).toEqual(serialized(pageFrameStyle(mkPage({ page: 1 }), 0.5), FRAME_KEYS))
  })

  it('offers exactly three segments, exactly one of them pressed', () => {
    render(canvas())

    const segments = Array.from(document.querySelectorAll('[data-testid^="extraction-zoom-"]'))
    expect(segments.map((el) => el.getAttribute('data-testid'))).toEqual([
      'extraction-zoom-50',
      'extraction-zoom-100',
      'extraction-zoom-150',
    ])

    const pressedCount = () => Object.values(zoomPressed()).filter((v) => v === 'true').length
    expect(zoomPressed()).toEqual({ 50: 'false', 100: 'true', 150: 'false' })

    for (const z of [150, 50, 100, 150]) {
      fireEvent.click(screen.getByTestId(`extraction-zoom-${z}`))
      expect(zoomPressed()[String(z)], `zoom ${z} is not pressed after its own click`).toBe('true')
      expect(pressedCount(), `after zoom ${z}, more or fewer than one segment is pressed`).toBe(1)
    }
  })

  it('carries no radius-forcing class on any control in the pane', () => {
    render(canvas())

    const root = screen.getByTestId('extraction-canvas')
    const all = [root, ...Array.from(root.querySelectorAll<HTMLElement>('*'))]
    expect(all.length, 'the walk read an empty tree').toBeGreaterThan(10)

    for (const el of all) {
      const classes = (el.getAttribute('class') ?? '').split(/\s+/)
      for (const forced of RADIUS_FORCING) {
        expect(classes, `${el.dataset.testid ?? el.tagName} carries ${forced}`).not.toContain(forced)
      }
    }
  })
})

// ==========================================================================================
// The highlight (AC-11, AC-12, AC-13)
// ==========================================================================================

describe('the highlight', () => {
  it("carries the artboard's amber fill, ring and colour-only transition", () => {
    render(canvas({ selected: 'invoice_number' }))

    const [node] = highlights()
    expect(node, 'no highlight rendered').toBeDefined()

    // MEASURED: jsdom rewrites the percentage and the leading dots in `background`, and
    // leaves `boxShadow` raw. Asserting the authored literal on both fails.
    expect(node.style.background).toBe('oklch(0.72 0.15 65 / 0.32)')
    expect(node.style.boxShadow).toBe('0 0 0 3px oklch(72% .15 65 / .32)')
    expect(node.style.borderRadius).toBe('3px')

    // A transition on a position or a size makes a boundingBox() taken right after a
    // selection measure a mid-transition rect -- EXTR11-E2E-03 takes exactly that
    // measurement ([[drawer-animation-defeats-geometry-specs]]).
    const transition = node.style.transition
    expect(transition).toContain('background')
    expect(transition).toContain('box-shadow')
    for (const banned of ['all', 'left', 'top', 'width', 'height', 'transform', 'inset']) {
      expect(transition, `the transition covers ${banned}`).not.toMatch(new RegExp(`(^|[\\s,])${banned}([\\s,]|$)`))
    }
  })

  it('spreads highlightStyle for every key, rather than re-declaring it', () => {
    const region = mkRegion({ page: 2, x0: 0.1, y0: 0.3, x1: 0.38, y1: 0.35 })
    render(canvas({ selected: 'invoice_date' }))

    const [node] = highlights()
    expect(node, 'no highlight rendered').toBeDefined()
    expect(pick(node.style, HIGHLIGHT_KEYS)).toEqual(serialized(highlightStyle(region), HIGHLIGHT_KEYS))
  })

  it("renders on the selected field's page and nowhere else", () => {
    render(canvas({ selected: 'invoice_date' })) // its region is on page 2

    expect(frames(), 'no frame rendered').toHaveLength(3)
    expect(within(frame(2)).getAllByTestId('extraction-highlight')).toHaveLength(1)
    expect(within(frame(1)).queryAllByTestId('extraction-highlight')).toHaveLength(0)
    expect(within(frame(3)).queryAllByTestId('extraction-highlight')).toHaveLength(0)
    expect(highlights(), 'a highlight escaped the frames').toHaveLength(1)
  })

  it('carries data-snip so the scroll helper can resolve it', () => {
    render(canvas({ selected: 'total_amount' }))

    const [node] = highlights()
    expect(node, 'no highlight rendered').toBeDefined()
    expect(node.getAttribute('data-snip')).toBe('total_amount')
    // Inside the ground, or scrollRegionIntoView's querySelector finds nothing.
    expect(screen.getByTestId('extraction-ground').contains(node)).toBe(true)
  })

  it('writes a line-item field name into data-snip verbatim', () => {
    // reconcile.go names fields like line_items[0].line_total. Inside a quoted attribute
    // value the brackets and the dot are ordinary code points, so no escaping is owed.
    const name = 'line_items[0].line_total'
    render(
      canvas({
        fields: [mkField({ name, region: mkRegion({ page: 1 }) })],
        selected: name,
      }),
    )

    const ground = screen.getByTestId('extraction-ground')
    expect(highlights(), 'no highlight rendered').toHaveLength(1)
    expect(ground.querySelector(`[data-snip="${name}"]`), 'the helper would resolve nothing').not.toBeNull()
  })

  it('renders no highlight when nothing is selected', () => {
    render(canvas({ selected: null }))

    // Floor first, both halves: three frames to draw on, and three fields that DO carry a
    // region -- otherwise "zero highlights" is true because nothing could ever be drawn.
    expect(frames(), 'no frame rendered').toHaveLength(3)
    expect(THREE_FIELDS.filter((f) => f.region !== null), 'no fixture field carries a region').toHaveLength(3)

    expect(highlights(), 'the brief: "On load nothing is overlaid."').toHaveLength(0)
  })

  it('never leaves two highlights behind, whichever field is selected next', () => {
    const { rerender } = render(canvas({ selected: null }))

    expect(frames(), 'no frame rendered').toHaveLength(3)
    expect(THREE_FIELDS, 'fewer than two selectable fields -- the row proves nothing').toHaveLength(3)

    for (const name of ['invoice_number', 'invoice_date', 'total_amount', 'invoice_number']) {
      rerender(canvas({ selected: name }))
      expect(highlights(), `selecting ${name} left more or fewer than one highlight`).toHaveLength(1)
      expect(highlights()[0].getAttribute('data-snip')).toBe(name)
    }

    rerender(canvas({ selected: null }))
    expect(highlights(), 'deselecting left a highlight behind').toHaveLength(0)
  })

  it('draws nothing for a field name that matches no row', () => {
    render(canvas({ selected: 'no_such_field' }))

    expect(frames(), 'no frame rendered').toHaveLength(3)
    expect(highlights()).toHaveLength(0)
    // Not a no-region banner either: the field does not exist, it has no absent region.
    expect(screen.queryByTestId('extraction-no-region')).toBeNull()
  })

  it('draws nothing for a region pointing at a page the document does not have', () => {
    // The wire permits it (no FK from a region's page to a page-images row) and a frame
    // keyed on a missing page would be an unmounted highlight or a crash.
    render(
      canvas({
        fields: [mkField({ name: 'invoice_number', region: mkRegion({ page: 9 }) })],
        selected: 'invoice_number',
      }),
    )

    expect(frames(), 'no frame rendered').toHaveLength(3)
    expect(highlights()).toHaveLength(0)
  })
})

// ==========================================================================================
// Selection scrolls the ground (AC-18)
// ==========================================================================================

describe('selecting a field', () => {
  const GROUND_TOP = 100 // non-zero, so an implementation that forgets `- cr.top` fails

  function measure(el: HTMLElement, rect: { top: number; height: number }, clientHeight?: number): void {
    Object.defineProperty(el, 'getBoundingClientRect', {
      configurable: true,
      value: () => ({ top: rect.top, height: rect.height, left: 0, right: 0, bottom: rect.top + rect.height, width: 0, x: 0, y: rect.top }),
    })
    if (clientHeight !== undefined) Object.defineProperty(el, 'clientHeight', { configurable: true, value: clientHeight })
  }

  it("centres the selected region in the ground, by attribute and after the paint", () => {
    vi.useFakeTimers()
    const scrollTo = scrollToSpy
    // Bytes never arrive: the frame is aspect-locked from the wire, so the scroll must land
    // without them. This also keeps a pending fetch out of the fake-timer clock.
    deferredPageFetch()

    const { rerender } = render(canvas({ selected: null }))
    expect(scrollTo, 'the pane scrolled before anything was selected').not.toHaveBeenCalled()

    rerender(canvas({ selected: 'invoice_date' })) // page 2
    const node = highlights()[0]
    expect(node, 'no highlight to resolve').toBeDefined()

    const ground = screen.getByTestId('extraction-ground')
    measure(ground, { top: GROUND_TOP, height: 600 }, 600)
    measure(node, { top: GROUND_TOP + 900, height: 20 })

    // Deferred past the render that mounts the frame: a per-item ref on a repeated element
    // does not attach, so the lookup happens after the paint that created the node.
    expect(scrollTo, 'the scroll ran synchronously, before the frame could mount').not.toHaveBeenCalled()

    act(() => {
      vi.advanceTimersByTime(20)
    })

    // scrollTop(0) + (1000 - 100) - 600/2 + 20/2
    expect(scrollTo).toHaveBeenCalledTimes(1)
    expect(scrollTo.mock.instances[0], 'the pane scrolled something other than the ground').toBe(ground)
    expect(scrollTo.mock.calls[0][0]).toEqual({ top: 610, behavior: 'smooth' })
  })

  it('scrolls again for the next selection, and not for the same one twice', () => {
    vi.useFakeTimers()
    const scrollTo = scrollToSpy
    deferredPageFetch()

    const { rerender } = render(canvas({ selected: 'invoice_number' }))
    act(() => {
      vi.advanceTimersByTime(20)
    })
    expect(scrollTo, 'the first selection never scrolled').toHaveBeenCalledTimes(1)

    rerender(canvas({ selected: 'invoice_number' })) // same selection, re-rendered
    act(() => {
      vi.advanceTimersByTime(20)
    })
    expect(scrollTo, 'a re-render of the same selection scrolled again').toHaveBeenCalledTimes(1)

    rerender(canvas({ selected: 'total_amount' }))
    act(() => {
      vi.advanceTimersByTime(20)
    })
    expect(scrollTo).toHaveBeenCalledTimes(2)
  })

  it('scrolls again when only scrollNonce moved, and not when it did not', () => {
    // `D-25`'s other half, stated where the prop lives. The row above holds `scrollNonce`
    // constant and moves `selected`; this one does the reverse. Without it the prop is
    // declared here and pinned only from ExtractionReview.test.tsx, so a second caller has
    // no local statement of what it is for.
    vi.useFakeTimers()
    const scrollTo = scrollToSpy
    deferredPageFetch()

    const { rerender } = render(canvas({ selected: 'invoice_number', scrollNonce: 1 }))
    act(() => {
      vi.advanceTimersByTime(20)
    })
    expect(scrollTo, 'the first selection never scrolled').toHaveBeenCalledTimes(1)

    // The pair, first: an unmoved nonce must not scroll, or "it scrolled" below is just
    // "it re-rendered".
    rerender(canvas({ selected: 'invoice_number', scrollNonce: 1 }))
    act(() => {
      vi.advanceTimersByTime(20)
    })
    expect(scrollTo, 'a re-render at the same nonce scrolled again').toHaveBeenCalledTimes(1)

    rerender(canvas({ selected: 'invoice_number', scrollNonce: 2 }))
    act(() => {
      vi.advanceTimersByTime(20)
    })

    expect(scrollTo, 'a bumped nonce did not re-centre the unchanged selection').toHaveBeenCalledTimes(2)
    expect(scrollTo.mock.instances[1], 'the pane scrolled something other than the ground').toBe(
      screen.getByTestId('extraction-ground'),
    )
  })

  it('scrolls nothing when the selected field has no region', () => {
    vi.useFakeTimers()
    const scrollTo = scrollToSpy
    deferredPageFetch()

    render(
      canvas({
        fields: [mkField({ name: 'buyer_tin', value: null, region: null })],
        selected: 'buyer_tin',
      }),
    )
    act(() => {
      vi.advanceTimersByTime(50)
    })

    // Floor: the banner proves the selection really was applied, so the absence is a fact.
    expect(screen.getByTestId('extraction-no-region')).toBeTruthy()
    expect(scrollTo).not.toHaveBeenCalled()
  })

  it('reaches for scrollRegionIntoView and never for scrollIntoView', () => {
    const src = readFileSync(path.join(process.cwd(), 'src/components/ExtractionCanvas.tsx'), 'utf8')
    expect(src.length, 'the scan read nothing').toBeGreaterThan(800)
    expect(src, 'the scan ran over a moved or renamed component').toContain('data-testid="extraction-canvas"')

    expect(src, 'scrollRegionIntoView is not used').toContain('scrollRegionIntoView')
    // 'scrollRegionIntoView' does not contain 'scrollIntoView' as a substring.
    expect(src, 'jsdom implements no scrollIntoView, and a mapped frame has no ref to use').not.toContain(
      'scrollIntoView',
    )
    expect(src, 'the pane must not re-implement the centring arithmetic').not.toContain('clientHeight')

    // AC-18's third clause, which had no oracle: "no per-frame `ref`". The frames are a mapped
    // list, so a per-item ref does not attach -- the ground's is the only ref this pane holds.
    expect(src.match(/\bref=\{/g) ?? [], 'the pane holds a ref other than the ground').toHaveLength(1)
    expect(src, 'the one ref is not the ground').toContain('ref={groundRef}')
  })
})

// ==========================================================================================
// The toolbar (AC-9)
// ==========================================================================================

describe('the toolbar', () => {
  it("carries the artboard's own declarations", () => {
    render(canvas())

    const toolbar = screen.getByTestId('extraction-toolbar')
    expect(toolbar.style.flex).toBe('0 0 auto') // MEASURED: React writes `flex: 'none'` as the triple
    expect(toolbar.style.display).toBe('flex')
    expect(toolbar.style.alignItems).toBe('center')
    expect(toolbar.style.gap).toBe('11px')
    expect(toolbar.style.padding).toBe('10px 16px')
    expect(toolbar.style.background).toBe('var(--bg-2)')
    expect(toolbar.style.borderBottom).toBe('1px solid var(--line-1)')
  })

  it("renders the artboard's four elements", () => {
    const doc = mkDocument()
    render(canvas({ doc }))

    const toolbar = screen.getByTestId('extraction-toolbar')
    expect((toolbar.textContent ?? '').length, 'the toolbar rendered no text at all').toBeGreaterThan(0)

    // 1. The lettered type tile. `exact` matching means the meta line ('PDF · 2 PAGES · …')
    //    is a different node and cannot be mistaken for it.
    const tile = within(toolbar).getByText(formatLabel(doc.filename, doc.content_type))
    expect(tile.style.width).toBe('32px')
    expect(tile.style.height).toBe('32px')
    expect(tile.style.borderRadius).toBe('8px')
    expect(tile.style.display).toBe('grid')
    expect(tile.style.placeItems).toBe('center')
    expect(tile.style.fontFamily).toBe('var(--font-mono)')
    expect(tile.style.fontSize).toBe('9px')
    expect(tile.style.fontWeight).toBe('700')
    // The artboard's tile is a lettered chip, not a glyph.
    expect(tile.querySelector('svg'), 'the tile rendered an icon instead of letters').toBeNull()

    // 2. The filename.
    const filename = within(toolbar).getByText('june-invoices.pdf')
    expect(filename.style.fontSize).toBe('13px')
    expect(filename.style.fontWeight).toBe('600')
    expect(filename.style.letterSpacing).toBe('-0.01em')
    expect(filename.style.textOverflow, 'a long filename must ellipsise, not wrap the toolbar').toBe('ellipsis')

    // 3. The mono meta line, from the shipped helper -- THREE pages in this fixture, so a
    //    hardcoded '2 PAGES' fails here.
    const meta = screen.getByTestId('extraction-doc-meta')
    expect(meta.textContent).toBe(docMetaLine(doc, 3))
    expect(meta.textContent).toContain('3 PAGES')
    expect(meta.className.split(/\s+/)).toContain('mono')
    expect(meta.style.fontSize).toBe('10px')
    expect(meta.style.color).toBe('var(--fg-3)')
    expect(meta.style.letterSpacing).toBe('0.04em')
    expect(meta.style.marginTop).toBe('2px')

    // 4. The READ ONLY pill.
    const pill = screen.getByTestId('extraction-read-only')
    expect(pill.textContent).toBe('READ ONLY')
    expect(pill.className.split(/\s+/)).toContain('mono')
    expect(pill.style.fontSize).toBe('9px')
    expect(pill.style.fontWeight).toBe('700')
    expect(pill.style.letterSpacing).toBe('0.09em')
    expect(pill.style.color).toBe('var(--fg-3)')
    expect(pill.style.border).toBe('1px solid var(--line-2)')
    expect(pill.style.borderRadius).toBe('999px')
    expect(pill.style.padding).toBe('3px 9px')
  })

  it('tints the type tile with fileTypeTone, not a fixed colour', () => {
    const pdf = mkDocument()
    const { rerender } = render(canvas({ doc: pdf }))

    const tileOf = (label: string) => within(screen.getByTestId('extraction-toolbar')).getByText(label)
    const pdfTone = fileTypeTone(pdf.filename, pdf.content_type)
    expect(tileOf('PDF').style.background).toBe(pdfTone.bg)
    expect(tileOf('PDF').style.color).toBe(pdfTone.fg)

    // A second kind, so the tone cannot be a constant that happens to match PDF.
    const jpg = mkDocument({ filename: 'receipt.jpg', content_type: 'image/jpeg' })
    const jpgTone = fileTypeTone(jpg.filename, jpg.content_type)
    expect(jpgTone, 'both kinds resolve the same tone -- this row proves nothing').not.toEqual(pdfTone)

    rerender(canvas({ doc: jpg }))
    expect(tileOf('JPG').style.background).toBe(jpgTone.bg)
    expect(tileOf('JPG').style.color).toBe(jpgTone.fg)
  })

  it('counts the pages it actually rendered', () => {
    const doc = mkDocument()
    render(canvas({ doc, pages: [mkPage({ page: 1 })] }))

    // Singular, from the shipped helper's own branch.
    expect(screen.getByTestId('extraction-doc-meta').textContent).toBe(docMetaLine(doc, 1))
    expect(screen.getByTestId('extraction-doc-meta').textContent).toContain('1 PAGE ·')
  })
})

// ==========================================================================================
// infoGlyph (AC-10)
// ==========================================================================================

describe('infoGlyph', () => {
  // A namespace import, never a named one: a missing named export from an ESM module fails
  // MODULE LINKING, which would take the whole file down instead of this row.
  it("is the artboard's paths, built through the file's own Icon", () => {
    const glyph = (glyphs as Record<string, unknown>).infoGlyph
    expect(glyph, 'glyphs.tsx exports no infoGlyph').toBeDefined()

    const probe = render(<span data-testid="glyph-probe">{glyph as React.ReactNode}</span>)
    const svg = probe.getByTestId('glyph-probe').querySelector('svg')
    expect(svg, 'infoGlyph rendered no svg').not.toBeNull()

    expect(Array.from(svg!.querySelectorAll('path')).map((p) => p.getAttribute('d'))).toEqual([
      'M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18Z',
      'M12 11v5',
      'M12 8h.01',
    ])
    // Through Icon, not a hand-rolled svg: these five are Icon's, not the glyph's.
    expect(svg!.getAttribute('viewBox')).toBe('0 0 24 24')
    expect(svg!.getAttribute('fill')).toBe('none')
    expect(svg!.getAttribute('stroke')).toBe('currentColor')
    expect(svg!.getAttribute('aria-hidden')).toBe('true')
    expect(svg!.getAttribute('width')).toBe('15')
    expect(svg!.getAttribute('height')).toBe('15')
  })

  it('adds no icon library to glyphs.tsx', () => {
    const src = readFileSync(path.join(process.cwd(), 'src/glyphs.tsx'), 'utf8')
    expect(src, 'the scan read the wrong file').toContain('export const infoGlyph')

    // The brief: "No Lucide icons -- inline stroke SVG only."
    for (const lib of ['lucide', 'react-icons', '@heroicons', 'feather-icons', '@tabler/icons']) {
      expect(src, `glyphs.tsx imports ${lib}`).not.toContain(lib)
    }
  })
})

// ==========================================================================================
// The no-region banner (AC-14)
// ==========================================================================================

describe('the no-region banner', () => {
  const REGIONLESS: ExtractionFieldState[] = [
    mkField({ name: 'invoice_number', region: mkRegion({ page: 1 }) }),
    mkField({ name: 'buyer_tin', value: null, region: null }),
  ]

  it('renders for a selected field with no region, and no highlight with it', () => {
    render(canvas({ fields: REGIONLESS, selected: 'buyer_tin' }))

    const banner = screen.getByTestId('extraction-no-region')
    expect(banner.textContent).toContain(NO_REGION)
    expect(highlights(), 'a region-less field was still highlighted').toHaveLength(0)

    // The artboard's banded strip, between the toolbar and the ground.
    expect(banner.style.flex).toBe('0 0 auto')
    expect(banner.style.gap).toBe('9px')
    expect(banner.style.padding).toBe('9px 16px')
    expect(banner.style.background).toBe('var(--bg-3)')
    expect(banner.style.borderBottom).toBe('1px solid var(--line-1)')
    expect(banner.querySelector('svg'), 'the banner carries no info glyph').not.toBeNull()

    const text = within(banner).getByText(NO_REGION)
    expect(text.style.fontSize).toBe('12px')
    expect(text.style.color).toBe('var(--fg-2)')
  })

  it('stays absent when the selected field has a region', () => {
    // The control needle for the row above: a banner rendered unconditionally passes it.
    render(canvas({ fields: REGIONLESS, selected: 'invoice_number' }))

    expect(highlights(), 'the floor: this selection really did resolve a region').toHaveLength(1)
    expect(screen.queryByTestId('extraction-no-region')).toBeNull()
    expect(screen.getByTestId('extraction-canvas').textContent).not.toContain(NO_REGION)
  })

  it('stays absent with nothing selected', () => {
    render(canvas({ fields: REGIONLESS, selected: null }))

    expect(frames(), 'no frame rendered').toHaveLength(3)
    expect(screen.queryByTestId('extraction-no-region')).toBeNull()
  })
})

// ==========================================================================================
// Zero pages and failed pages (AC-15, AC-16)
// ==========================================================================================

describe('the empty and failed states', () => {
  it('renders the dashed zero-pages panel, not an ErrorState', () => {
    render(canvas({ pages: [] }))

    const text = screen.getByText(NO_PAGES)
    expect(frames(), 'a frame rendered for a document with no pages').toHaveLength(0)

    // A normal state. ErrorState's `error` prop is required and non-nullable, which alone
    // disqualifies it here (api-client/src/components/ErrorState.tsx:13).
    const root = screen.getByTestId('extraction-canvas')
    expect(root.textContent).not.toContain(ERROR_STATE_HEADING)
    expect(root.textContent).not.toMatch(/retry/i)

    const panel = dashedAncestor(text)
    expect(panel, "the app's empty-panel invariant is a dashed border").not.toBeNull()
    expect(panel!.style.border).toBe('1px dashed var(--line-3)')
    expect(panel!.style.borderRadius).toBe('var(--radius-md)')

    // The toolbar still renders -- the document exists, it just has no page images.
    expect(screen.getByTestId('extraction-doc-meta').textContent).toContain('0 PAGES')
  })

  it('requests no bytes at all for a document with no pages', () => {
    const bytes = pageFetch()
    render(canvas({ pages: [] }))

    expect(bytes.requested()).toEqual([])
  })

  it('keeps the other pages when one page fails', async () => {
    const bytes = pageFetch({ failing: [2] })
    render(canvas())
    await flush()

    expect(frames(), 'no frame rendered').toHaveLength(3)
    expect(bytes.requested().sort(), 'all three pages must have been attempted').toEqual([1, 2, 3])

    // The failure is contained to its own frame.
    await waitFor(() => expect(within(frame(2)).getByText(PAGE_FAILED)).toBeTruthy())
    expect(within(frame(2)).queryByTestId('extraction-page-image-2'), 'a failed page rendered an image').toBeNull()

    expect(within(frame(1)).getByTestId('extraction-page-image-1').getAttribute('src')).toMatch(/^blob:/)
    expect(within(frame(3)).getByTestId('extraction-page-image-3').getAttribute('src')).toMatch(/^blob:/)

    // NEW in this story: every shipped failure panel in the app is SOLID
    // `1px solid var(--status-red-border)`. AC-15 asks for a dashed one, and dashed +
    // status-red is the consistent extrapolation of the two existing invariants.
    const panel = dashedAncestor(within(frame(2)).getByText(PAGE_FAILED))
    expect(panel, 'the failure panel is not dashed').not.toBeNull()
    expect(panel!.style.border).toBe('1px dashed var(--status-red-border)')
  })

  it('leaks no object URL for a page that failed', async () => {
    pageFetch({ failing: [1, 2, 3] })
    const { unmount } = render(canvas())
    await flush()

    // fetchPageImage throws BEFORE createObjectURL, so a refused page pins no blob.
    expect(createSpy, 'a refused page created an object URL with no release() to revoke it').not.toHaveBeenCalled()
    unmount()
    expect(revoked()).toEqual([])
  })
})

// ==========================================================================================
// Lazy byte loading (AC-6, AC-7)
// ==========================================================================================

describe('page bytes', () => {
  const FIVE_PAGES = [1, 2, 3, 4, 5].map((page) => mkPage({ page }))
  const PAGE_4_FIELD = [mkField({ name: 'invoice_number', region: mkRegion({ page: 4 }) })]

  it('requests a frame\'s bytes only when that frame is observed', () => {
    installObserver()
    const bytes = pageFetch()

    render(canvas({ pages: FIVE_PAGES, fields: PAGE_4_FIELD, selected: null }))

    // pdfium.go:30 caps a document at 800 pages at ~113 KiB each. Eager loading is 800
    // concurrent authenticated fetches and ~90 MB of blob URLs, none of it reusable under
    // `Cache-Control: private, no-store`.
    expect(frames(), 'no frame rendered -- the zero below would be vacuous').toHaveLength(5)
    expect(bytes.requested(), 'every page loaded at mount').toEqual([])

    cross(frame(3))
    expect(bytes.requested(), 'a crossing loaded the wrong page, or more than one').toEqual([3])

    cross(frame(5))
    expect(bytes.requested()).toEqual([3, 5])
  })

  it('watches the ground with one frame of margin', () => {
    installObserver()
    render(canvas({ pages: FIVE_PAGES }))

    const obs = observerWatching(frame(1))
    expect(obs.targets.size, 'the observer is not watching every frame').toBe(5)
    expect(obs.options.root, 'the observer roots on the viewport, not the ground').toBe(
      screen.getByTestId('extraction-ground'),
    )

    // "Plus one frame of margin" -- a `0px` rootMargin loads a frame only once it is
    // already on screen, which is the behaviour AC-6 exists to avoid.
    const margin = obs.options.rootMargin ?? ''
    expect(margin, 'no rootMargin was set').not.toBe('')
    const px = margin.split(/\s+/).map((v) => parseFloat(v))
    expect(Math.max(...px), `rootMargin ${margin} pre-loads nothing`).toBeGreaterThan(0)
  })

  it('does not request the same page twice', () => {
    installObserver()
    const bytes = pageFetch()
    render(canvas({ pages: FIVE_PAGES }))

    cross(frame(3))
    cross(frame(3))
    cross(frame(3), false)
    cross(frame(3))

    expect(bytes.requested(), 'a re-crossing re-fetched a page already held').toEqual([3])
  })

  it('keeps a loaded page when its frame leaves the viewport', async () => {
    installObserver()
    render(canvas({ pages: FIVE_PAGES }))

    cross(frame(2))
    await flush()
    expect(within(frame(2)).getByTestId('extraction-page-image-2').getAttribute('src')).toMatch(/^blob:/)

    cross(frame(2), false)
    await flush()

    // Nothing is evicted: eviction under `no-store` means re-fetching on every scroll back.
    expect(within(frame(2)).getByTestId('extraction-page-image-2').getAttribute('src')).toMatch(/^blob:/)
    expect(revoked(), 'a page was released while the pane was still alive').toEqual([])
  })

  it("loads a selection's page directly, without waiting for a crossing", () => {
    installObserver()
    const bytes = pageFetch()

    const { rerender } = render(canvas({ pages: FIVE_PAGES, fields: PAGE_4_FIELD, selected: null }))
    expect(bytes.requested(), 'the fixture loaded eagerly, so the row below proves nothing').toEqual([])

    rerender(canvas({ pages: FIVE_PAGES, fields: PAGE_4_FIELD, selected: 'invoice_number' }))

    // "Reachable in one action" must not race a scroll callback.
    expect(bytes.requested()).toEqual([4])
    expect(DrivingObserver.instances.some((o) => o.targets.size > 0), 'the observer was never installed').toBe(true)
  })

  it('loads every page at mount when the environment has no IntersectionObserver', async () => {
    removeObserver()
    expect((globalThis as { IntersectionObserver?: unknown }).IntersectionObserver, 'the constructor is still here').toBeUndefined()
    const bytes = pageFetch()

    expect(() => render(canvas({ pages: FIVE_PAGES }))).not.toThrow()
    await flush()

    expect(bytes.requested().sort((a, b) => a - b)).toEqual([1, 2, 3, 4, 5])
    expect(images(), 'the eager path rendered no bytes').toHaveLength(5)
  })

  it('disconnects its observer on unmount', () => {
    installObserver()
    const { unmount } = render(canvas({ pages: FIVE_PAGES }))

    const obs = observerWatching(frame(1))
    expect(obs.disconnected).toBe(0)
    unmount()
    expect(obs.disconnected, 'the observer outlived the pane').toBeGreaterThan(0)
  })
})

// ==========================================================================================
// Object-URL lifecycle (AC-17)
// ==========================================================================================

describe('the pane owns every object URL', () => {
  it('releases every loaded page on unmount', async () => {
    render(canvas())
    await flush()

    // Floor: three real handles exist to be released.
    expect(urls(), 'no object URL was created').toEqual(['blob:1', 'blob:2', 'blob:3'])
    expect(revoked(), 'a page was released while it was still on screen').toEqual([])

    // Floor: the spy is live wire, so the empty array above is a fact and not a dead spy.
    URL.revokeObjectURL('blob:probe')
    expect(revoked()).toEqual(['blob:probe'])

    cleanup()
    expect(revoked().slice(1).sort()).toEqual(['blob:1', 'blob:2', 'blob:3'])
  })

  it('releases every loaded page on a jobId change, and reloads under the new one', async () => {
    const bytes = pageFetch()
    const { rerender } = render(canvas())
    await flush()

    expect(urls()).toEqual(['blob:1', 'blob:2', 'blob:3'])
    expect(bytes.requested().sort()).toEqual([1, 2, 3])

    rerender(canvas({ jobId: OTHER_JOB_ID }))
    await flush()

    // Without this, document 2 opens showing document 1's pages.
    expect(revoked().sort(), 'the previous job\'s handles were leaked').toEqual(['blob:1', 'blob:2', 'blob:3'])
    expect(bytes.requested()).toHaveLength(6)
    expect(urls()).toHaveLength(6)

    const src = (n: number) => within(frame(n)).getByTestId(`extraction-page-image-${n}`).getAttribute('src')
    expect([src(1), src(2), src(3)].sort(), 'a frame is still showing the previous job\'s bytes').toEqual([
      'blob:4',
      'blob:5',
      'blob:6',
    ])
  })

  it('releases a handle that lands after the pane is gone', async () => {
    const gate = deferredPageFetch()
    const { unmount } = render(canvas())

    expect(gate.requested().sort(), 'the requests never went out').toEqual([1, 2, 3])
    expect(createSpy, 'the bytes resolved early -- nothing is in flight').not.toHaveBeenCalled()

    unmount()
    gate.open()

    // fetchPageImage creates the object URL before its promise resolves, so the handle is
    // already on the floor when it lands -- SourceDocumentModal.tsx:84-88's named bug class.
    await waitFor(() => expect(revoked().sort()).toEqual(['blob:1', 'blob:2', 'blob:3']))
    expect(urls()).toHaveLength(3)
  })

  it('leaks nothing under StrictMode\'s doubled mount', async () => {
    render(<StrictMode>{canvas()}</StrictMode>)
    await flush()

    const live = images().map((el) => el.getAttribute('src') ?? '')
    expect(live.filter((s) => s.startsWith('blob:')), 'no handle reached the frames').toHaveLength(3)
    for (const url of live) expect(revoked(), `${url} was revoked out from under the frame`).not.toContain(url)

    cleanup()
    // Every handle the doubled run produced, released exactly once each.
    expect([...new Set(revoked())].sort()).toEqual([...new Set(urls())].sort())
    for (const url of new Set(urls())) {
      expect(revoked().filter((u) => u === url), `${url} was revoked more than once`).toHaveLength(1)
    }
  })
})
// ==========================================================================================
// QA (Mode B) — the jobId-change path, resolution order, and the degenerate inputs
// ==========================================================================================

/**
 * One resolver per request, in dispatch order, so a spec can land job A's page AFTER job B's.
 * `deferredPageFetch` holds every request on one gate and can only open them together, which
 * cannot express an interleave.
 */
interface QueuedFetch {
  /** Every request, in dispatch order, with the job it went out under. */
  requests: () => { jobId: string; page: number }[]
  /** Resolves request `i` and drains the microtasks its handle travels through. */
  settle: (i: number) => Promise<void>
}

function queuedPageFetch(): QueuedFetch {
  const gates: Array<() => void> = []
  const requests: { jobId: string; page: number }[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      const m = /\/extractions\/([^/]+)\/pages\/(\d+)(?:\?|$)/.exec(url)
      expect(m, `not a page-image URL: ${url}`).not.toBeNull()
      const hit = m as RegExpExecArray
      requests.push({ jobId: hit[1], page: Number(hit[2]) })
      await new Promise<void>((resolve) => gates.push(resolve))
      return { ok: true, status: 200, statusText: 'OK', arrayBuffer: async () => new ArrayBuffer(8) }
    }),
  )
  return {
    requests: () => [...requests],
    settle: async (i: number) => {
      expect(gates[i], `request ${i} was never dispatched`).toBeDefined()
      await act(async () => {
        gates[i]()
        for (let k = 0; k < 8; k += 1) await Promise.resolve()
      })
    },
  }
}

describe('switching to another job', () => {
  const ONE_PAGE = [mkPage({ page: 1 })]

  it("releases a page that lands after the job it belongs to was replaced", async () => {
    const q = queuedPageFetch()
    const { rerender } = render(canvas({ pages: ONE_PAGE, fields: [], selected: null }))

    expect(q.requests(), 'job A never requested its page').toEqual([{ jobId: JOB_ID, page: 1 }])

    rerender(canvas({ jobId: OTHER_JOB_ID, pages: ONE_PAGE, fields: [], selected: null }))
    expect(q.requests()[1], 'job B never requested its page').toEqual({ jobId: OTHER_JOB_ID, page: 1 })

    // B lands FIRST, so the tagged map is now B's and the frame is showing B's bytes.
    await q.settle(1)
    const img = () => within(frame(1)).queryByTestId('extraction-page-image-1')
    expect(img(), "job B's page never reached the frame").not.toBeNull()
    expect(img()!.getAttribute('src')).toBe('blob:1')

    // Now A's page lands, late. The state writer's `prev.jobId !== job` arm would re-tag the
    // map to A; the derived map for B is then `{}` and every frame blanks -- permanently,
    // because `requested` already holds page 1 and no crossing will fetch it again.
    await q.settle(0)

    expect(urls(), 'both jobs must have produced a handle, or the row below is vacuous').toEqual(['blob:1', 'blob:2'])
    expect(img(), "a late page from the replaced job blanked the current job's frame").not.toBeNull()
    expect(img()!.getAttribute('src'), "the replaced job's bytes took the current job's frame").toBe('blob:1')
    expect(revoked(), "the replaced job's late handle was kept instead of released").toEqual(['blob:2'])
  })

  it('releases three late pages from the replaced job, and keeps none of them', async () => {
    const q = queuedPageFetch()
    const { rerender } = render(canvas({ fields: [], selected: null }))

    expect(q.requests().map((r) => r.page).sort(), 'job A never requested its three pages').toEqual([1, 2, 3])

    rerender(canvas({ jobId: OTHER_JOB_ID, fields: [], selected: null }))
    expect(q.requests(), 'job B never re-requested under its own id').toHaveLength(6)

    // Job B settles one page, then all three of A's land on top of it.
    await q.settle(3)
    for (const i of [0, 1, 2]) await q.settle(i)

    expect(urls(), 'four handles must exist, or the accounting below proves nothing').toHaveLength(4)
    // Exactly the three A handles, and none of B's.
    expect(revoked().sort()).toEqual(['blob:2', 'blob:3', 'blob:4'])
    expect(within(frame(q.requests()[3].page)).queryByTestId(`extraction-page-image-${q.requests()[3].page}`), "job B's settled page lost its bytes").not.toBeNull()
  })

  it("shows no page between jobs, and never the previous document's", async () => {
    const { rerender } = render(canvas({ fields: [], selected: null }))
    await flush()
    expect(images().map((el) => el.getAttribute('src')), 'job A never loaded').toEqual(['blob:1', 'blob:2', 'blob:3'])

    // Job B is a NARROWER document whose bytes are held open. Two facts in one window: the
    // frames the new job does not have are gone, and the frames it does have are BLANK --
    // an untagged page map would render document 1's (already revoked) bytes into them.
    const gate = deferredPageFetch()
    rerender(canvas({ jobId: OTHER_JOB_ID, pages: ONE_PAGE, fields: [], selected: null }))
    await flush()

    expect(frameIds(), "the previous job's extra frames survived").toEqual(['extraction-page-1'])
    expect(gate.requested(), 'job B never requested its own page').toEqual([1])
    expect(images(), "the previous document's pages are still on screen").toHaveLength(0)
    expect(revoked().sort(), "the previous job's handles were leaked").toEqual(['blob:1', 'blob:2', 'blob:3'])
  })

  it("a crossing after a jobId change loads the new job's page, never the old job's", () => {
    installObserver()
    const q = queuedPageFetch()

    // The SAME `pages` array identity across both renders: only the job changed, so an
    // observer effect keyed on the page list alone would keep the mount-time `load` and the
    // job id that closure captured -- and fetch document 1's pixels for document 2.
    const { rerender } = render(canvas({ pages: ONE_PAGE, fields: [], selected: null }))
    cross(frame(1))
    expect(q.requests(), 'the first crossing never fetched').toEqual([{ jobId: JOB_ID, page: 1 }])

    rerender(canvas({ jobId: OTHER_JOB_ID, pages: ONE_PAGE, fields: [], selected: null }))
    cross(frame(1))

    expect(q.requests(), 'the crossing after the switch fetched nothing').toHaveLength(2)
    expect(q.requests()[1], 'a stale load closure fetched the replaced job').toEqual({
      jobId: OTHER_JOB_ID,
      page: 1,
    })
  })

  it('keeps the zoom the reader chose across a jobId change', () => {
    // `## Decisions -> D-23`. The brief's reset rule ("Reset the review component's state
    // whenever docName or variant changes, or document 2 inherits document 1's corrections")
    // and the artboard's own componentDidUpdate (`Recognition Review.dc.html:566-573`) reset
    // CONTENT state only -- focus, hover, selection, edits, drawn boxes. Zoom is a view
    // preference the artboard does not have, and an accountant working a queue of documents
    // would re-click it on every one.
    const { rerender } = render(canvas())
    fireEvent.click(screen.getByTestId('extraction-zoom-150'))
    expect(frame(1).style.width, 'the zoom click did not land').toBe('150%')

    rerender(canvas({ jobId: OTHER_JOB_ID }))

    expect(frames(), 'no frame rendered').toHaveLength(3)
    expect(zoomPressed(), 'the new document opened at a zoom the reader did not choose').toEqual({
      50: 'false',
      100: 'false',
      150: 'true',
    })
    expect(frame(1).style.width).toBe('150%')
  })

  // EXTR-11-07, Mode A. RED until the pane's [jobId] effect returns the ground to the top.
  it('returns the ground to the top on a jobId change', () => {
    // `D-24`. The ground sits behind this pane's private `groundRef`, so `ExtractionReview`
    // cannot reach it and document 2 opens at document 1's scroll position. The shell's own
    // row (ExtractionReview.test.tsx, "releases every handle, clears the selection...") states
    // the same contract but cannot discriminate: a shell that shows `Loading` between the two
    // details remounts this pane, and a fresh ground reads 0 whatever the effect does.
    const { rerender } = render(canvas())
    const g = screen.getByTestId('extraction-ground')

    // MEASURED: jsdom's `scrollTop` stores what it is given. The read-back is the floor.
    g.scrollTop = 480
    expect(g.scrollTop, 'jsdom did not keep the scroll position -- the claim below is vacuous').toBe(480)

    rerender(canvas({ jobId: OTHER_JOB_ID }))

    // Identity, not presence: `<ExtractionCanvas key={jobId}>` would satisfy the reset by
    // remounting, and `D-24` rules that out -- it resets zoom against `D-23` and makes the
    // tagged page map and the `jobRef` guard production-dead while their specs stay green.
    expect(screen.getByTestId('extraction-ground'), 'the pane remounted -- the reset below is vacuous').toBe(g)
    expect(g.scrollTop, "document 2 opened at document 1's scroll position").toBe(0)
  })
})

describe('page bytes, adversarially', () => {
  const FIVE = [1, 2, 3, 4, 5].map((page) => mkPage({ page }))

  it('never re-requests a page whose bytes failed', async () => {
    installObserver()
    const bytes = pageFetch({ failing: [2] })
    render(canvas({ pages: FIVE, fields: [], selected: null }))

    cross(frame(2))
    await flush()
    await waitFor(() => expect(within(frame(2)).getByText(PAGE_FAILED)).toBeTruthy())
    expect(bytes.requested(), 'the first crossing never fetched').toEqual([2])

    // Scrolled away and back. The shipped choice is that a refused page stays refused for the
    // life of the pane: `requested` is added to before the fetch and never removed, and there
    // is no retry affordance anywhere on this screen. The alternative -- deleting the key in
    // the catch -- re-fetches on every scroll past a page whose object is genuinely gone.
    cross(frame(2), false)
    cross(frame(2))
    await flush()

    expect(bytes.requested(), 'a refused page re-fetched on the next crossing').toEqual([2])
    expect(within(frame(2)).getByText(PAGE_FAILED), 'the failure panel was cleared without a retry').toBeTruthy()
  })

  it('fetches once when a selection and a crossing want the same page', async () => {
    installObserver()
    const bytes = pageFetch()
    const PAGE_4 = [mkField({ name: 'invoice_number', region: mkRegion({ page: 4 }) })]

    const { rerender } = render(canvas({ pages: FIVE, fields: PAGE_4, selected: null }))
    expect(bytes.requested(), 'the fixture loaded eagerly').toEqual([])

    // The selection's direct load and the observer's crossing are two different call sites
    // for the same page. `requested` is added to synchronously, before the await, so the
    // second caller is refused; a direct fetchPageImage on the selection path would not be.
    rerender(canvas({ pages: FIVE, fields: PAGE_4, selected: 'invoice_number' }))
    cross(frame(4))
    await flush()

    expect(bytes.requested(), 'the same page went out twice').toEqual([4])
    expect(urls(), 'two blob URLs for one page, and only one of them in a frame').toEqual(['blob:1'])
    expect(within(frame(4)).getByTestId('extraction-page-image-4').getAttribute('src')).toBe('blob:1')

    // And two entries for the SAME frame inside ONE callback. `requested` is written before
    // the await, so the second entry is refused in the same tick.
    const obs = observerWatching(frame(5))
    act(() => {
      obs.cb(
        [
          { target: frame(5), isIntersecting: true, intersectionRatio: 1 },
          { target: frame(5), isIntersecting: true, intersectionRatio: 1 },
        ],
        obs,
      )
    })
    expect(bytes.requested(), 'one callback with two entries for one frame fetched it twice').toEqual([4, 5])
    await flush() // drain the last two handles inside this spec, not into the next one
  })

  it('watches nothing and fetches nothing for a document with no pages', async () => {
    installObserver()
    const bytes = pageFetch()
    expect(() => render(canvas({ pages: [], fields: [], selected: null }))).not.toThrow()

    expect(screen.getByText(NO_PAGES), 'the empty panel did not render').toBeTruthy()
    expect(frames(), 'a frame rendered for a document with no pages').toHaveLength(0)
    expect(bytes.requested()).toEqual([])

    // Control needle: the same mount WITH a page, under the same stubbed observer, does fetch
    // on a crossing -- so the empty array above is a fact about zero pages, not a dead stub.
    cleanup()
    render(canvas({ pages: [mkPage({ page: 1 })], fields: [], selected: null }))
    cross(frame(1))
    expect(bytes.requested()).toEqual([1])
    await flush()
  })

  it('keys every frame on its own page number, never on its position', async () => {
    installObserver()
    const bytes = pageFetch()
    // Nothing on the wire guarantees a contiguous 1..n -- the rows come from a query, not a
    // range -- and every other fixture in this file is contiguous from 1, where a position
    // and a page number agree.
    render(canvas({ pages: [5, 2, 9].map((page) => mkPage({ page })), fields: [], selected: null }))

    expect(frameIds()).toEqual(['extraction-page-2', 'extraction-page-5', 'extraction-page-9'])
    for (const el of frames()) {
      const fromTestid = (el.dataset.testid ?? '').replace('extraction-page-', '')
      // The two attributes are independent expressions of the same number. The observer reads
      // `data-page`; every spec in this file reads the testid.
      expect(el.dataset.page, `${el.dataset.testid} is tagged for a different page`).toBe(fromTestid)
    }

    cross(frame(9))
    expect(bytes.requested(), 'the crossing fetched a position, not a page').toEqual([9])
    await flush()
  })

  it('watches a frame added after the first render, and drops the observer it replaces', async () => {
    installObserver()
    const bytes = pageFetch()
    const TWO = [mkPage({ page: 1 }), mkPage({ page: 2 })]
    const { rerender } = render(canvas({ pages: TWO, fields: [], selected: null }))

    const first = observerWatching(frame(1))
    expect(first.targets.size, 'the first observer is not watching both frames').toBe(2)

    // A later read of the same job carries a third page.
    rerender(canvas({ pages: [...TWO, mkPage({ page: 3 })], fields: [], selected: null }))

    expect(first.disconnected, 'the observer outlived the page set it was built for').toBeGreaterThan(0)
    const second = observerWatching(frame(3))
    expect(second, 'the frame added after the first render is watched by the old observer').not.toBe(first)
    expect(second.targets.size, 'the rebuilt observer is not watching every frame').toBe(3)
    cross(frame(3))
    expect(bytes.requested(), 'a frame added after the first render never loads').toEqual([3])

    // And a frame that goes away is not still being watched by anyone.
    const gone = frame(3)
    rerender(canvas({ pages: TWO, fields: [], selected: null }))
    expect(
      DrivingObserver.instances.some((o) => o.targets.has(gone)),
      'an unmounted frame is still observed',
    ).toBe(false)
    await flush()
  })

  it('re-fetches nothing when the ctx prop churns on every render', () => {
    installObserver()
    const gate = deferredPageFetch()
    const fresh = () => ({ getToken: () => 'tok' }) as unknown as PlatformCtx

    const { rerender } = render(canvas({ ctx: fresh(), pages: FIVE, fields: [], selected: null }))
    cross(frame(2))
    expect(gate.requested(), 'the first crossing never fetched').toEqual([2])

    // Every other spec in this file passes ONE ctx instance. A caller that rebuilds it per
    // render changes `load`'s identity, which rebuilds the observer, which re-observes a frame
    // already on screen and fires `isIntersecting` again. Only `requested` -- a ref, not the
    // rendered page map -- keeps that from re-fetching bytes still in flight.
    for (let i = 0; i < 3; i += 1) {
      rerender(canvas({ ctx: fresh(), pages: FIVE, fields: [], selected: null }))
      cross(frame(2))
    }

    expect(DrivingObserver.instances.length, 'the ctx churn rebuilt no observer, so the row below is vacuous').toBeGreaterThan(1)
    expect(gate.requested(), 'a churning ctx re-fetched a page already in flight').toEqual([2])
  })
})

describe('the highlight, adversarially', () => {
  it('draws one highlight when two rows carry the same field name', () => {
    // Nothing on the wire enforces unique field names, and the highlight is a SINGLE derived
    // value ("Nothing else is highlighted, ever."). `fields.find` takes the first row; a
    // filter-and-map would put one node on each of two different pages.
    const dupes = [
      mkField({ name: 'invoice_number', region: mkRegion({ page: 1 }) }),
      mkField({ name: 'invoice_number', region: mkRegion({ page: 3, x0: 0.1, y0: 0.7, x1: 0.4, y1: 0.8 }) }),
    ]
    render(canvas({ fields: dupes, selected: 'invoice_number' }))

    expect(frames(), 'no frame rendered').toHaveLength(3)
    expect(dupes.filter((f) => f.name === 'invoice_number'), 'the fixture has no duplicate to resolve').toHaveLength(2)
    expect(highlights(), 'a duplicated field name drew two highlights').toHaveLength(1)
    expect(within(frame(1)).getAllByTestId('extraction-highlight')).toHaveLength(1)
    expect(within(frame(3)).queryAllByTestId('extraction-highlight')).toHaveLength(0)
  })

  it('draws nothing, and says nothing, for a page-2 region on a one-page document', () => {
    render(
      canvas({
        pages: [mkPage({ page: 1 })],
        fields: [mkField({ name: 'invoice_number', region: mkRegion({ page: 2 }) })],
        selected: 'invoice_number',
      }),
    )

    expect(frames(), 'no frame rendered').toHaveLength(1)
    expect(highlights()).toHaveLength(0)
    // Not the no-region banner either: this field HAS a region, and the banner's sentence
    // ("We have no region for this field") would be false.
    expect(screen.queryByTestId('extraction-no-region'), 'a field with a region got the no-region banner').toBeNull()
  })
})

describe('the ground scrolls once per selection', () => {
  it("does not scroll again when a page's bytes arrive", async () => {
    vi.useFakeTimers()
    const q = queuedPageFetch()
    render(canvas({ selected: 'invoice_date' }))

    await act(async () => {
      vi.advanceTimersByTime(20)
    })
    expect(scrollToSpy, 'the selection never scrolled').toHaveBeenCalledTimes(1)

    // Every page lands, each one a re-render. The scroll effect's deps are the selection and
    // the job -- adding the page map to them (the obvious way to silence the exhaustive-deps
    // disable on that effect) yanks the ground out from under the reader on every byte.
    const total = q.requests().length
    expect(total, 'no request went out -- the re-renders below never happen').toBe(3)
    for (let i = 0; i < total; i += 1) await q.settle(i)
    await act(async () => {
      vi.advanceTimersByTime(200)
    })

    expect(images(), 'no bytes arrived, so the row below is vacuous').toHaveLength(3)
    expect(scrollToSpy, 'a page arriving scrolled the ground again').toHaveBeenCalledTimes(1)
  })
})

// ==========================================================================================
// The falsified header (AC-19)
// ==========================================================================================

describe('SourceDocumentPages.tsx', () => {
  it('no longer claims the server records no page count', () => {
    // cwd, not import.meta.url: under jsdom the latter is an http: URL and fileURLToPath
    // throws (SourceDocumentPages.test.tsx:528).
    const src = readFileSync(path.join(process.cwd(), 'src/components/SourceDocumentPages.tsx'), 'utf8')

    // The planted needle first: a deleted or moved file must fail here rather than report
    // all-clear on an empty read.
    expect(src, 'the scan ran over a moved or renamed component').toContain('data-testid="pdf-embed"')
    expect(src.length).toBeGreaterThan(800)

    // extraction_page_images records exactly the page count this clause says nothing keeps.
    expect(src, 'SourceDocumentPages.tsx:7 still carries the false clause').not.toContain('the server records none')
    expect(src).not.toContain('records none')

    // The rest of the paragraph is still true and must survive the edit.
    expect(src, 'the true half of the paragraph was deleted with the false half').toContain(
      'Nothing in a browser exposes the page count',
    )
  })
})
