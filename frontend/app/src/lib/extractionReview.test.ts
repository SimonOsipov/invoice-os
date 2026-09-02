// lib/extractionReview.ts's contract. Written RED (EXTR-11-04, Mode A) against a stub that
// threw, so every behavioural row failed on its own assertion rather than on an import error.
//
// vitest environment is 'node' (vitest.config.ts:5) — no jsdom, no DOM. scrollRegionIntoView
// is exercised against a stub scroller for exactly that reason.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, join, normalize } from 'node:path'
import { fileURLToPath } from 'node:url'

import { ApiError } from '@invoice-os/api-client'

import {
  applyDraft,
  correctedMarker,
  docMetaLine,
  fetchPageImage,
  fieldLabel,
  fieldNote,
  getExtractionDetail,
  highlightStyle,
  isDrawnBox,
  normaliseBox,
  pageFrameStyle,
  pointBoxStyle,
  pointedEntry,
  reasonPill,
  regionPhrase,
  savableCorrections,
  scrollRegionIntoView,
  typedEntry,
} from './extractionReview'
import type {
  DraftEntries,
  ExtractionDetail,
  ExtractionDocument,
  ExtractionFieldState,
  ExtractionPage,
  ExtractionRegion,
  FrameBox,
  ViewportPoint,
} from './extractionReview'

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  vi.useRealTimers()
})

const BASE = 'https://gw'
const JOB_ID = 'c3d4e5f6-a7b8-4c3d-9e4f-5a6b7c8d9e0f'

const MODULE_SRC = fileURLToPath(new URL('./extractionReview.ts', import.meta.url))
const PACKAGE_JSON = fileURLToPath(new URL('../../package.json', import.meta.url))

function mkRegion(o: Partial<ExtractionRegion> = {}): ExtractionRegion {
  return { page: 1, x0: 0, y0: 0, x1: 1, y1: 1, ...o }
}

function mkDocument(o: Partial<ExtractionDocument> = {}): ExtractionDocument {
  return {
    filename: 'x.pdf',
    content_type: 'application/pdf',
    size_bytes: 151552, // exactly 148 KiB
    stored_at: '2026-08-30T10:42:07Z',
    ...o,
  }
}

// -- highlightStyle (AC-6) -------------------------------------------------------------

describe('highlightStyle', () => {
  it('top-left origin: every percentage comes straight off the region', () => {
    expect(highlightStyle({ page: 1, x0: 0.25, y0: 0.1, x1: 0.75, y1: 0.2 })).toMatchObject({
      left: '25%',
      top: '10%',
      width: '50%',
      height: '10%',
    })
  })

  it('a full-page region is 0/0/100/100', () => {
    expect(highlightStyle(mkRegion({ x0: 0, y0: 0, x1: 1, y1: 1 }))).toMatchObject({
      left: '0%',
      top: '0%',
      width: '100%',
      height: '100%',
    })
  })

  it('a zero-area region keeps its origin and stays zero-width', () => {
    // The migration permits x0 === x1; no clamp may move the box off its origin.
    expect(highlightStyle(mkRegion({ x0: 0.5, y0: 0.4, x1: 0.5, y1: 0.4 }))).toMatchObject({
      left: '50%',
      top: '40%',
      width: '0%',
      height: '0%',
    })
  })

  it("the mock fixture's own region carries no IEEE-754 tail", () => {
    // invoice_number's box in internal/extraction/mock.go is what the deployed AC-4 oracle
    // measures, and (0.90 - 0.62) * 100 is 28.000000000000004 in doubles. An unrounded
    // template literal ships '28.000000000000004%'.
    expect(highlightStyle({ page: 1, x0: 0.62, y0: 0.08, x1: 0.9, y1: 0.13 })).toMatchObject({
      left: '62%',
      top: '8%',
      width: '28%',
      height: '5%',
    })
  })

  it('every deployed mock region rounds clean, not just the one the spec above names', () => {
    // Every region internal/extraction/mock.go deploys: the clean-invoice fixture's five, then
    // mockDefaultResult's three that no fixture shares — issue_date's two alternatives and vat.
    // The tails sit on different axes: issue_date's y0 is 14.000000000000002 and its height
    // 4.999999999999999, total's height 6.000000000000005, vat's 4.999999999999993. The row
    // above only exercises x1 - x0. Nothing forces this mirror to track Go's region count.
    const MOCK_REGIONS: ExtractionRegion[] = [
      { page: 1, x0: 0.62, y0: 0.08, x1: 0.9, y1: 0.13 },
      { page: 1, x0: 0.62, y0: 0.14, x1: 0.9, y1: 0.19 },
      { page: 1, x0: 0.62, y0: 0.7, x1: 0.9, y1: 0.76 },
      { page: 1, x0: 0.1, y0: 0.08, x1: 0.38, y1: 0.13 },
      { page: 1, x0: 0.1, y0: 0.3, x1: 0.38, y1: 0.35 },
      { page: 1, x0: 0.62, y0: 0.8, x1: 0.9, y1: 0.85 },
      { page: 1, x0: 0.1, y0: 0.5, x1: 0.38, y1: 0.55 },
      { page: 1, x0: 0.62, y0: 0.64, x1: 0.9, y1: 0.69 },
    ]
    expect(MOCK_REGIONS, 'an empty table would pass every assertion below').toHaveLength(8)

    // Control: at least one axis really does carry a tail, so the property is not vacuous.
    const raw = MOCK_REGIONS.flatMap((r) => [r.x0 * 100, r.y0 * 100, (r.x1 - r.x0) * 100, (r.y1 - r.y0) * 100])
    expect(
      raw.some((n) => String(n).replace(/^\d+\.?/, '').length > 4),
      'no mock region has an IEEE-754 tail — this row proves nothing',
    ).toBe(true)

    for (const region of MOCK_REGIONS) {
      const style = highlightStyle(region)
      const pairs: Array<[string, number]> = [
        [String(style.left), region.x0 * 100],
        [String(style.top), region.y0 * 100],
        [String(style.width), (region.x1 - region.x0) * 100],
        [String(style.height), (region.y1 - region.y0) * 100],
      ]
      for (const [rendered, exact] of pairs) {
        expect(rendered, `${rendered} is not a percentage`).toMatch(/^-?\d+(\.\d{1,4})?%$/)
        // 1e-4 of a percent is 0.0026px on a 2560px render — three orders below a device pixel.
        expect(Number(rendered.slice(0, -1))).toBeCloseTo(exact, 4)
      }
    }
  })

  // The five appearance values are System Design §3's, read off the artboard
  // (Recognition Review.dc.html:597-598 for the fill and the ring, :76 for the radius and
  // the transition). They live here rather than in the component, so this is their only
  // non-circular oracle: EXTR-11-05 reads them back off highlightStyle.
  it("carries the artboard's amber fill, ring and radius", () => {
    expect(highlightStyle(mkRegion())).toMatchObject({
      background: 'oklch(72% .15 65 / .32)',
      boxShadow: '0 0 0 3px oklch(72% .15 65 / .32)',
      borderRadius: 3,
    })
  })

  it('is inert to the pointer, so the page image underneath stays clickable', () => {
    expect(highlightStyle(mkRegion())).toMatchObject({ position: 'absolute', pointerEvents: 'none' })
  })

  it('transitions colour only — never a position or a size', () => {
    // A transition on left/top/width/height makes a boundingBox() taken right after a
    // selection measure a mid-transition rect, and EXTR11-E2E-03's ratio oracle takes
    // exactly that measurement.
    const transition = String(highlightStyle(mkRegion()).transition)
    expect(transition).toContain('background')
    expect(transition).toContain('box-shadow')
    for (const banned of ['all', 'left', 'top', 'width', 'height', 'transform', 'inset']) {
      expect(transition, `the transition covers ${banned}`).not.toMatch(new RegExp(`(^|[\\s,])${banned}([\\s,]|$)`))
    }
  })

  it('is zoom-free: one argument, and the same output at every zoom the screen offers', () => {
    const region = mkRegion({ x0: 0.62, y0: 0.08, x1: 0.9, y1: 0.13 })
    const baseline = highlightStyle(region)

    expect(highlightStyle.length, 'highlightStyle takes a zoom argument').toBe(1)

    const zooms = [0.5, 1, 1.5] // SourceDocumentPages.tsx:16
    expect(zooms.length).toBeGreaterThan(0)
    const extraArg = highlightStyle as unknown as (r: ExtractionRegion, zoom: number) => unknown
    for (const zoom of zooms) {
      expect(extraArg(region, zoom), `zoom ${zoom} changed the highlight`).toEqual(baseline)
    }
  })
})

// -- pageFrameStyle (AC-7) -------------------------------------------------------------

describe('pageFrameStyle', () => {
  const LETTER: ExtractionPage = { page: 1, width_px: 1275, height_px: 1651 }

  it('the ratio comes from the stored grid', () => {
    expect(pageFrameStyle(LETTER, 1)).toMatchObject({ aspectRatio: '1275 / 1651' })
  })

  it('a non-Letter grid gets its own ratio, so the value is not a constant', () => {
    expect(pageFrameStyle({ page: 2, width_px: 1240, height_px: 1754 }, 1)).toMatchObject({
      aspectRatio: '1240 / 1754',
    })
  })

  it('zoom moves width, never transform', () => {
    const style = pageFrameStyle(LETTER, 1.5)
    expect(style).toMatchObject({ width: '150%' })
    expect(style, 'a scale leaves the layout box alone, so the ground would never scroll').not.toHaveProperty(
      'transform',
    )
  })

  it('the band scales with the same zoom', () => {
    // Recognition Review.dc.html:72 — the artboard's page floor and ceiling.
    expect(pageFrameStyle(LETTER, 1)).toMatchObject({ width: '100%', minWidth: '560px', maxWidth: '640px' })
    expect(pageFrameStyle(LETTER, 1.5)).toMatchObject({ width: '150%', minWidth: '840px', maxWidth: '960px' })
    expect(pageFrameStyle(LETTER, 0.5)).toMatchObject({ width: '50%', minWidth: '280px', maxWidth: '320px' })
  })

  it("carries the artboard's page card, background included", () => {
    // Recognition Review.dc.html:72. System Design §3 tabulates the same card and omits
    // only `background`; EXTR-11-05's AC names it, so it is pinned at its source here.
    expect(pageFrameStyle(LETTER, 1)).toMatchObject({
      margin: '0 auto 18px',
      background: '#fff',
      border: '1px solid var(--line-2)',
      boxShadow: '0 1px 3px oklch(20% .02 210 / .08)',
    })
  })

  it('padding is zero, so the padding box and the content box coincide', () => {
    // CSS 2.1 §10.1: the absolutely-positioned highlight resolves its percentages against
    // the padding box, the in-flow image its width against the content box.
    const style = pageFrameStyle(LETTER, 1)
    expect([0, '0', '0px'], 'a padded frame separates the two boxes and the highlight drifts').toContain(style.padding)
    expect(style).toMatchObject({ position: 'relative' })
  })
})

// -- docMetaLine (AC-8) ----------------------------------------------------------------

describe('docMetaLine', () => {
  it("the artboard's four segments, in order", () => {
    expect(docMetaLine(mkDocument(), 2)).toBe('PDF · 2 PAGES · 148 KB · STORED 11:42 WAT')
  })

  it('one page is singular', () => {
    const line = docMetaLine(mkDocument(), 1)
    expect(line).toContain('1 PAGE ·')
    expect(line).not.toContain('1 PAGES')
  })

  it('a null filename still names a type', () => {
    // formatLabel's own fallback (SourceDocumentStates.tsx:37) — not a re-implementation.
    expect(docMetaLine(mkDocument({ filename: null }), 2)).toMatch(/^application\/pdf ·/)
  })

  it('the byte segment is formatBytes, not a re-rounding', () => {
    // 1048575 reads '1 MB', not '1.0 MB' — sourceDocument.ts:272's documented boundary.
    expect(docMetaLine(mkDocument({ size_bytes: 1048575 }), 2)).toContain('· 1 MB ·')
  })
})

// -- fmtTimeWAT placement + composition (AC-8, AC-9) ------------------------------------
//
// fmtTimeWAT's own behaviour is pinned in format.test.ts, beside the other time formatters
// and the ICU hazard T-4 already documents (the story's own placement).

describe('extractionReview.ts composes the shipped helpers', () => {
  it('imports formatLabel, formatBytes and fmtTimeWAT rather than re-implementing them', () => {
    const src = readFileSync(MODULE_SRC, 'utf8')
    expect(src, 'the scan read the wrong file').toContain('export function docMetaLine(')

    expect(src, 'formatLabel is not imported').toContain('formatLabel')
    expect(src, 'formatBytes is not imported').toContain('formatBytes')
    expect(src, 'fmtTimeWAT is not imported').toContain('fmtTimeWAT')

    expect(src, 'formatLabel re-implemented: the extension branch is here').not.toContain('toUpperCase(')
    expect(src, 'formatBytes re-implemented: the 1024 base is here').not.toContain('1024')
    expect(src, 'fmtTimeWAT re-implemented: the formatter belongs in format.ts').not.toContain('Intl.DateTimeFormat')
  })
})

// -- the pixel-grid absence proof (AC-7) ------------------------------------------------

// oklch() colour literals legitimately carry '72' (the artboard's highlight fill is
// oklch(72% .15 65 / .32)). Everything outside them must not.
function scrubColours(src: string): string {
  return src.replace(/oklch\([^)]*\)/g, 'oklch(COLOUR)')
}

describe('pageFrameStyle never recomputes the pixel grid', () => {
  it('the scrubber strips colours without swallowing arithmetic', () => {
    // Planted positive, in-memory only: the scrubber must not over-strip and read clean.
    const planted = "const a = 'oklch(72% .15 65 / .32)'\nconst b = pt * dpi / 72\n"
    const scrubbed = scrubColours(planted)
    expect(scrubbed).not.toContain('oklch(72%')
    expect(scrubbed, 'the scrubber ate the arithmetic it exists to expose').toContain('/ 72')
    expect(scrubbed).toContain('dpi')
  })

  it("no 72 and no dpi survive outside a colour literal, and the module's own needle is present", () => {
    const src = readFileSync(MODULE_SRC, 'utf8')
    expect(src.length, 'the scan read nothing').toBeGreaterThan(0)
    expect(src, 'the scan ran over a moved or renamed module').toContain('export function pageFrameStyle(')

    const scrubbed = scrubColours(src)
    expect(scrubbed, 'go-pdfium ceils pt * dpi / 72 — US-Letter at 150 DPI is 1651 rows, not 1650').not.toMatch(/72/)
    // No \b around dpi: '_' is a word character, so \bdpi\b misses RENDER_DPI entirely.
    expect(scrubbed).not.toMatch(/dpi/i)
  })
})

// -- scrollRegionIntoView (AC-10) -------------------------------------------------------

interface GroundStub {
  clientHeight: number
  scrollTop: number
  getBoundingClientRect: () => { top: number; height: number }
  querySelector: (selector: string) => { getBoundingClientRect: () => { top: number; height: number } } | null
  scrollTo: (arg: { top?: number } | number) => void
}

const GROUND_TOP = 100 // non-zero, so an implementation that forgets `- cr.top` fails

function stubGround(opts: {
  clientHeight: number
  scrollTop: number
  targetOffset: number | null
  targetHeight?: number
}): { ground: GroundStub; asked: string[] } {
  const asked: string[] = []
  const offset = opts.targetOffset
  const height = opts.targetHeight ?? 20
  const ground: GroundStub = {
    clientHeight: opts.clientHeight,
    scrollTop: opts.scrollTop,
    getBoundingClientRect: () => ({ top: GROUND_TOP, height: opts.clientHeight }),
    querySelector(selector: string) {
      asked.push(selector)
      if (offset === null) return null
      return { getBoundingClientRect: () => ({ top: GROUND_TOP + offset, height }) }
    },
    scrollTo(arg) {
      ground.scrollTop = typeof arg === 'number' ? arg : (arg.top ?? ground.scrollTop)
    },
  }
  return { ground, asked }
}

function callScroll(ground: GroundStub, fieldName: string): void {
  scrollRegionIntoView(ground as unknown as HTMLElement, fieldName)
}

describe('scrollRegionIntoView', () => {
  it('centres the region in the scroller', () => {
    // Recognition Review.dc.html:508-514.
    vi.useFakeTimers()
    const { ground } = stubGround({ clientHeight: 600, scrollTop: 0, targetOffset: 900, targetHeight: 20 })

    callScroll(ground, 'invoice_number')
    vi.runAllTimers()

    expect(ground.scrollTop).toBe(610)
  })

  it('never scrolls above zero', () => {
    vi.useFakeTimers()
    const { ground } = stubGround({ clientHeight: 600, scrollTop: 500, targetOffset: -900, targetHeight: 20 })

    callScroll(ground, 'invoice_number')
    vi.runAllTimers()

    expect(ground.scrollTop, 'a target above the viewport must clamp, not go negative').toBe(0)
  })

  it('defers past the render that mounts the frame', () => {
    // document-recognition-prompt.md §13: a per-item ref on a repeated element does not
    // attach, so the lookup has to happen after the paint that created the node.
    vi.useFakeTimers()
    const { ground } = stubGround({ clientHeight: 600, scrollTop: 7, targetOffset: 900, targetHeight: 20 })

    callScroll(ground, 'invoice_number')
    expect(ground.scrollTop, 'the scroll ran synchronously, before the frame could mount').toBe(7)

    vi.runAllTimers()
    expect(ground.scrollTop).toBe(617)
  })

  it('resolves by attribute, not by ref', () => {
    vi.useFakeTimers()
    const { ground, asked } = stubGround({ clientHeight: 600, scrollTop: 0, targetOffset: 900 })

    callScroll(ground, 'invoice_number')
    vi.runAllTimers()

    expect(asked, 'the scroller was never asked for a selector').toHaveLength(1)
    expect(asked[0]).toMatch(/^\[data-snip=["']?invoice_number["']?\]$/)
  })

  it('a line-item field name goes into the selector verbatim', () => {
    // reconcile.go names fields like line_items[0].line_total. Inside a quoted attribute
    // value the brackets and the dot are ordinary code points, so no escaping is owed; only
    // a double quote or a backslash would need it, and no field name carries either.
    vi.useFakeTimers()
    const { ground, asked } = stubGround({ clientHeight: 600, scrollTop: 0, targetOffset: 900 })

    callScroll(ground, 'line_items[0].line_total')
    vi.runAllTimers()

    expect(asked).toEqual(['[data-snip="line_items[0].line_total"]'])
    expect(ground.scrollTop, 'the bracketed name never resolved').toBe(610)
  })

  it('an unknown field name is a silent no-op', () => {
    vi.useFakeTimers()
    const { ground, asked } = stubGround({ clientHeight: 600, scrollTop: 42, targetOffset: null })

    expect(() => {
      callScroll(ground, 'nope')
      vi.runAllTimers()
    }).not.toThrow()

    expect(asked).toHaveLength(1)
    expect(ground.scrollTop, 'a missing snip moved the scroller').toBe(42)
  })

  it('a null scroller is a silent no-op', () => {
    vi.useFakeTimers()
    expect(() => {
      scrollRegionIntoView(null, 'invoice_number')
      vi.runAllTimers()
    }).not.toThrow()
  })

  it('the module holds no scrollIntoView call and no ref', () => {
    const src = readFileSync(MODULE_SRC, 'utf8')
    expect(src, 'the scan ran over a moved or renamed module').toContain('export function scrollRegionIntoView(')

    expect(src, "scrollIntoView on a repeated frame is the brief's named failure").not.toContain('scrollIntoView')
    expect(src).not.toContain('useRef')
    expect(src).not.toContain('createRef')
  })
})

// -- fetchPageImage (AC-5) --------------------------------------------------------------

function mockPageFetch(bytes: number[], opts: { ok?: boolean; status?: number; statusText?: string } = {}) {
  const buffer = new Uint8Array(bytes).buffer
  const fetchMock = vi.fn().mockResolvedValue({
    ok: opts.ok ?? true,
    status: opts.status ?? 200,
    statusText: opts.statusText ?? 'OK',
    headers: new Headers({ 'Content-Type': 'image/png' }),
    arrayBuffer: () => Promise.resolve(buffer),
    blob: () => Promise.resolve(new Blob([buffer], { type: 'image/png' })),
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

const PAGE_URL = `${BASE}/api/submission/v1/extractions/${JOB_ID}/pages/2`

describe('fetchPageImage', () => {
  it('sends the bearer header to the page route', async () => {
    const fetchMock = mockPageFetch([1, 2, 3, 4])
    const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:page-2')

    const result = await fetchPageImage(() => 'tok', BASE, JOB_ID, 2)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe(PAGE_URL)
    expect(new Headers(init.headers).get('authorization')).toBe('Bearer tok')

    const blob = createObjectURL.mock.calls[0][0] as Blob
    expect(blob.type, 'a bare <img src> paints nothing without image/png').toBe('image/png')
    expect(blob.size, 'an empty blob would still pass a type-only check').toBe(4)
    expect(result.url).toBe('blob:page-2')
  })

  it('release is idempotent', async () => {
    mockPageFetch([1, 2, 3, 4])
    vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:page-2')
    const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})

    const { release } = await fetchPageImage(() => 'tok', BASE, JOB_ID, 2)

    release()
    expect(revokeObjectURL).toHaveBeenCalledTimes(1)
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:page-2')

    release()
    release()
    expect(revokeObjectURL, 'a second release revoked again').toHaveBeenCalledTimes(1)
  })

  it('a non-2xx throws ApiError carrying the status', async () => {
    mockPageFetch([], { ok: false, status: 404, statusText: 'Not Found' })

    const err = await fetchPageImage(() => 'tok', BASE, JOB_ID, 2).catch((e: unknown) => e)

    expect(err, 'a missing page resolved instead of throwing').toBeInstanceOf(ApiError)
    expect((err as ApiError).status, 'a page failure must stay distinguishable from a network one').toBe(404)
    expect((err as ApiError).kind).toBe('http')
  })

  it('a non-2xx leaks no object URL', async () => {
    mockPageFetch([], { ok: false, status: 403, statusText: 'Forbidden' })
    const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:leaked')

    await fetchPageImage(() => 'tok', BASE, JOB_ID, 2).catch(() => undefined)

    expect(createObjectURL, 'a refused page pinned a blob with no release() to revoke it').not.toHaveBeenCalled()
  })

  it('a body that fails mid-read leaks no object URL', async () => {
    // arrayBuffer() is awaited inside the createObjectURL argument, so a truncated body must
    // reject before any blob exists. A truncated response arriving as a 200 has happened on
    // this stack before (the evidence-bundle ZIP).
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: true,
        status: 200,
        statusText: 'OK',
        arrayBuffer: () => Promise.reject(new TypeError('network error')),
      }),
    )
    const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:leaked')

    const err = await fetchPageImage(() => 'tok', BASE, JOB_ID, 2).catch((e: unknown) => e)

    expect(err, 'a truncated page resolved as a page').toBeInstanceOf(Error)
    expect(createObjectURL).not.toHaveBeenCalled()
  })

  it('a network throw propagates and leaks no object URL', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockRejectedValue(new TypeError('Failed to fetch')),
    )
    const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:leaked')

    const err = await fetchPageImage(() => 'tok', BASE, JOB_ID, 2).catch((e: unknown) => e)

    expect(err, 'a dropped connection resolved as a page').toBeInstanceOf(Error)
    expect(createObjectURL).not.toHaveBeenCalled()
  })
})

// -- getExtractionDetail ----------------------------------------------------------------

describe('getExtractionDetail', () => {
  it('reads the detail route through authedFetch', async () => {
    const detail: ExtractionDetail = {
      id: JOB_ID,
      document_id: 'd1',
      state: 'succeeded',
      document: mkDocument(),
      pages: [],
      fields: [],
    }
    const authedFetch = vi.fn().mockResolvedValue(detail)

    const got = await getExtractionDetail(authedFetch, BASE, JOB_ID)

    expect(authedFetch).toHaveBeenCalledTimes(1)
    expect(authedFetch.mock.calls[0][0]).toBe(`${BASE}/api/submission/v1/extractions/${JOB_ID}`)
    expect(got).toEqual(detail)
  })
})

// -- the runtime dependency fence (AC-1) -------------------------------------------------

describe('frontend/app/package.json', () => {
  it('still lists exactly four runtime dependencies', () => {
    // No test under frontend/app/src read package.json before this one, so a fifth runtime
    // dependency went green through tsc, vitest, the Go suite and the deploy gate.
    const pkg = JSON.parse(readFileSync(PACKAGE_JSON, 'utf8')) as {
      name?: string
      dependencies?: Record<string, string>
    }
    expect(pkg.name, 'the scan read the wrong package.json').toBe('@invoice-os/app')

    expect(Object.keys(pkg.dependencies ?? {}).sort()).toEqual([
      '@invoice-os/api-client',
      '@invoice-os/design-tokens',
      'react',
      'react-dom',
    ])
  })
})

// -- the lib -> components edge this module deliberately keeps ---------------------------

// docMetaLine imports formatLabel from components/SourceDocumentStates.tsx rather than
// re-implementing it, which is the only lib -> components import in the app. It costs
// nothing (that component is already bundled via SourceDocumentCard.tsx) and closes no
// cycle today. This row is what keeps that true: the moment anything the component reaches
// imports extractionReview, the graph has a cycle and module init order starts to matter.
describe('the SourceDocumentStates import closes no cycle', () => {
  const SRC = join(dirname(MODULE_SRC), '..')

  function resolveImport(from: string, spec: string): string | null {
    if (!spec.startsWith('.')) return null
    const base = normalize(join(dirname(from), spec))
    for (const ext of ['.ts', '.tsx', '/index.ts', '/index.tsx']) {
      if (existsSync(base + ext)) return base + ext
    }
    return existsSync(base) ? base : null
  }

  function closureOf(entry: string): Set<string> {
    const seen = new Set<string>()
    const queue = [entry]
    while (queue.length) {
      const file = queue.pop() as string
      if (seen.has(file)) continue
      seen.add(file)
      for (const m of readFileSync(file, 'utf8').matchAll(/from\s+'([^']+)'/g)) {
        const next = resolveImport(file, m[1])
        if (next) queue.push(next)
      }
    }
    return seen
  }

  it('nothing SourceDocumentStates.tsx reaches imports extractionReview', () => {
    const entry = join(SRC, 'components/SourceDocumentStates.tsx')
    expect(existsSync(entry), 'the component moved — this scan proves nothing').toBe(true)

    const closure = closureOf(entry)
    // Floor + a known member: an empty or one-file closure would pass the absence below.
    expect(closure.size, 'the walk followed no imports').toBeGreaterThan(3)
    expect([...closure], 'the walk never reached lib/').toContain(join(SRC, 'lib/sourceDocument.ts'))

    expect([...closure], 'lib -> components -> lib/extractionReview is a cycle').not.toContain(MODULE_SRC)
  })
})

// -- the four field-cell mappings (EXTR-12-06, AC-2/AC-3/AC-4/AC-5) ----------------------
//
// Every string below is a literal from the story's Invented-copy table, never an import of
// the constant under test: a test that imports the string it asserts asserts the module
// against itself.

describe('fieldLabel', () => {
  it('maps the ten header names and falls back to the wire name', () => {
    // The nine editable ones come from EDIT_FIELD_LABELS (reviewBatch.ts); invoice_number is
    // not editable (EDIT_FIELD_KEYS is nine wide by [D9]), so this module carries the tenth.
    expect(fieldLabel('invoice_number')).toBe('Invoice number')
    expect(fieldLabel('issue_date')).toBe('Issue date')
    expect(fieldLabel('supplier_tin')).toBe('Supplier TIN')
    expect(fieldLabel('supplier_name')).toBe('Supplier name')
    expect(fieldLabel('buyer_tin')).toBe('Buyer TIN')
    expect(fieldLabel('buyer_name')).toBe('Buyer name')
    expect(fieldLabel('currency')).toBe('Currency')
    expect(fieldLabel('subtotal')).toBe('Subtotal')
    // The artboard says "VAT at 7.5%"; that label carries a hard-coded rate, which is data,
    // and a per cent sign on this pane reds AC-1's own residue sweep.
    expect(fieldLabel('vat')).toBe('VAT')
    expect(fieldLabel('total')).toBe('Total')

    // Off the vocabulary: both real extractors emit document_text_layer, and reconcileLines
    // emits a per-row line_items[N].line_total. Neither has a curated label.
    expect(fieldLabel('document_text_layer')).toBe('document_text_layer')
    expect(fieldLabel('line_items[0].line_total')).toBe('line_items[0].line_total')
  })

  it('does not humanise an unmapped name', () => {
    // AuditRow.tsx:52 is a second, private fieldLabel that snake-cases a key into prose. It
    // is the rejected convention: this row is where the two disagree by construction, and the
    // curated row above catches it too (it would answer 'Supplier tin', not 'Supplier TIN').
    expect(fieldLabel('document_text_layer')).toBe('document_text_layer')
    expect(fieldLabel('document_text_layer'), 'the mechanical humaniser won').not.toBe('Document text layer')
  })
})

describe('reasonPill', () => {
  it('maps each reason code to its copy-table string', () => {
    expect(reasonPill('unreadable')).toBe("COULDN'T READ THIS CLEARLY")
    expect(reasonPill('ambiguous')).toBe('FOUND TWO POSSIBLE VALUES')
    expect(reasonPill('inconsistent')).toBe("DOESN'T ADD UP")
    expect(reasonPill('missing')).toBe('NOT FOUND')

    // A clean field has nothing to say, and the cell's one pill slot then falls back to the
    // shipped NO REGION cue.
    expect(reasonPill(''), 'a clean field claimed the pill slot').toBeNull()
  })
})

describe('fieldNote', () => {
  it('splits the one inconsistent code three ways', () => {
    const sum = fieldNote('inconsistent', 'subtotal')
    const tin = fieldNote('inconsistent', 'supplier_tin')
    const name = fieldNote('inconsistent', 'supplier_name')
    const other = fieldNote('inconsistent', 'total')

    expect(sum).toBe('The line items we read do not add up to this subtotal.')
    expect(tin).toBe(
      "This document's supplier doesn't match the client you picked. It is filed from your client record either way.",
    )
    expect(name, 'supplier_name reads differently from supplier_tin').toBe(tin)
    expect(other).toBe('This value disagrees with the other numbers on the document.')

    // Pairwise, so a later copy edit cannot collapse two arms into one and stay green.
    expect(sum, 'the sum check and the supplier check read the same').not.toBe(tin)
    expect(sum, 'the sum check and the generic arm read the same').not.toBe(other)
    expect(tin, 'the supplier check and the generic arm read the same').not.toBe(other)
  })

  it('names the two supplier fields rather than matching them by prefix', () => {
    // The pane-level client-record sentence carries the same guard, for the same reason: the
    // entity match compares supplier_tin and supplier_name, and a third supplier_* field is
    // not the pair it explains. `startsWith('supplier')` passes every other row in this file.
    expect(fieldNote('inconsistent', 'supplier_address')).toBe(
      'This value disagrees with the other numbers on the document.',
    )
    expect(fieldNote('inconsistent', 'supplier_address'), 'a prefix match claimed the entity sentence').not.toBe(
      fieldNote('inconsistent', 'supplier_tin'),
    )
  })

  it('returns null for every reason that is not inconsistent', () => {
    // Keyed on the reason FIRST. A note keyed on the NAME alone renders the subtotal sentence
    // on a clean subtotal, and passes the row above.
    for (const reason of ['', 'unreadable', 'ambiguous', 'missing'] as const) {
      expect(fieldNote(reason, 'subtotal'), `a "${reason}" subtotal carried a note`).toBeNull()
    }
  })
})

describe('correctedMarker', () => {
  it('maps each method to its label and its was-line', () => {
    expect(correctedMarker({ method: 'typed', was: 'SFS-2026-O418', where: null }, mkRegion({ page: 5 }))).toEqual({
      label: 'YOU CHANGED THIS',
      was: 'We read SFS-2026-O418',
    })

    // A minimal pair: these two differ ONLY in `where`, and they fail in opposite directions.
    // Reading `where` alone yields 'Taken from ' on the first; ignoring it yields
    // 'Taken from page 3' on the second. The page is never 1, so a hard-coded page reds too.
    expect(correctedMarker({ method: 'pointed', was: null, where: null }, mkRegion({ page: 3 }))).toEqual({
      label: 'YOU POINTED THIS OUT',
      was: 'Taken from page 3',
    })
    expect(
      correctedMarker({ method: 'pointed', was: null, where: 'a box you drew' }, mkRegion({ page: 3 })),
    ).toEqual({ label: 'YOU POINTED THIS OUT', was: 'Taken from a box you drew' })

    expect(correctedMarker({ method: 'chosen', was: '2026-01-10', where: null }, mkRegion({ page: 2 }))).toEqual({
      label: 'YOU CHOSE THIS',
      was: 'We found more than one candidate',
    })
  })

  it('omits the was-line when the correction has no provenance to state', () => {
    // `'We read ' + was` renders "We read null" on the deployed mock's own buyer_tin, whose
    // Value is nil; a template literal renders the dangling "We read ". Omitting the sentence
    // invents no copy, and the label still says the field was settled.
    expect(correctedMarker({ method: 'typed', was: null, where: null }, null)).toEqual({
      label: 'YOU CHANGED THIS',
      was: null,
    })
    // A pointed correction naming a field the extractor never read carries no region either.
    expect(correctedMarker({ method: 'pointed', was: null, where: null }, null)).toEqual({
      label: 'YOU POINTED THIS OUT',
      was: null,
    })
  })

  it('returns null for an uncorrected field', () => {
    // What lets the cell branch on the return value alone rather than re-testing `corrected`.
    expect(correctedMarker(null, mkRegion({ page: 4 }))).toBeNull()
  })
})

// -- EXTR-12-07. The shared draft, and the phrase both the chip and the was-line read --------
//
// Written RED against stubs that answer one fixed wrong value each: `regionPhrase` -> 'page 0'
// (never right: the page CHECK is `page >= 1`), `applyDraft` -> every field rewritten, and
// `savableCorrections` -> one request for a field named 'no field yet'.

function mkField(o: Partial<ExtractionFieldState> = {}): ExtractionFieldState {
  return {
    name: 'total',
    value: '1000.00',
    region: mkRegion({ page: 2 }),
    reason: '',
    alternatives: [],
    corrected: null,
    ...o,
  }
}

describe('regionPhrase', () => {
  it('names the page, and is null without a region', () => {
    // Never page 1 in the fixture: a hardcoded 'page 1' would pass on it. Two different pages,
    // so a phrase that reads any region's page but the field's own also fails.
    expect(regionPhrase(mkRegion({ page: 3 }))).toBe('page 3')
    expect(regionPhrase(mkRegion({ page: 5 }))).toBe('page 5')

    // An alternative the extractor found nowhere renders no sub-label at all, rather than a
    // dangling 'page '.
    expect(regionPhrase(null), 'a region-less candidate claimed a page').toBeNull()
  })

  it('is the same phrase correctedMarker already states for a pointed correction', () => {
    // The extraction's own proof: the pointed was-line is `Taken from ` + this phrase, and
    // correctedMarker's shipped rows stay green byte-for-byte because nothing else moved.
    expect(correctedMarker({ method: 'pointed', was: null, where: null }, mkRegion({ page: 3 }))).toEqual({
      label: 'YOU POINTED THIS OUT',
      was: `Taken from ${regionPhrase(mkRegion({ page: 3 }))}`,
    })
  })
})

describe('applyDraft', () => {
  it('lays a typed value over the wire and claims no correction', () => {
    // A drafted field must not say YOU CHANGED THIS: nothing has been recorded, and the copy
    // table has no pending string. Its reason and its region are equally untouched -- a draft
    // is the user's own text echoed back, not a new reading.
    const fields = [mkField({ name: 'total', value: '1000.00', region: mkRegion({ page: 2 }), reason: 'inconsistent' })]
    const entries: DraftEntries = { total: { kind: 'typed', value: '9,999.00', region: null } }

    const out = applyDraft(fields, entries)

    expect(out, 'the draft dropped or duplicated a field').toHaveLength(1)
    expect(out[0].value, 'the typed draft never reached the field').toBe('9,999.00')
    expect(out[0].corrected, 'a drafted field claims a correction the server has not accepted').toBeNull()
    expect(out[0].reason, 'the draft cleared the reason the extractor reported').toBe('inconsistent')
    expect(out[0].region?.page ?? null, 'a typed draft moved the highlight').toBe(2)
  })

  it("moves a chosen field's region to that alternative's own box", () => {
    // Three distinct pages, so neither keeping the decided region (1) nor taking the FIRST
    // alternative (3) can pass. This is Core AC 7's both-ways binding: the highlight follows
    // the chip before anything is saved.
    const fields = [
      mkField({
        name: 'issue_date',
        value: '2026-01-01',
        region: mkRegion({ page: 1 }),
        reason: 'ambiguous',
        alternatives: [
          { value: '2026-01-10', region: mkRegion({ page: 3 }) },
          { value: '2026-10-01', region: mkRegion({ page: 5 }) },
        ],
      }),
    ]
    const entries: DraftEntries = {
      issue_date: { kind: 'chosen', value: '2026-10-01', region: mkRegion({ page: 5 }) },
    }

    const out = applyDraft(fields, entries)

    expect(out[0].value, 'the chosen candidate never reached the field').toBe('2026-10-01')
    expect(out[0].region?.page ?? null, "the highlight stayed on the reading the chip replaced").toBe(5)
    expect(out[0].corrected, 'a drafted chip claims a correction the server has not accepted').toBeNull()
  })

  it("resets an undone field to the extractor's reading and drops the marker", () => {
    // The artboard's own draft rule (`Recognition Review.dc.html:634`): an undo resets the
    // rendered value BEFORE any Save. `corrected.was` is the reading the correction superseded
    // and it is always present while an Undo control exists, so nothing here is invented.
    const fields = [
      mkField({
        name: 'total',
        value: '1,500.00',
        corrected: { method: 'typed', was: '1000.00', where: null },
      }),
    ]
    const entries: DraftEntries = { total: { kind: 'undone', value: '1,500.00', region: null } }

    const out = applyDraft(fields, entries)

    // Not the entry's own value: an undo posts a value the server ignores, and the screen must
    // show what the register is about to hold.
    expect(out[0].value, 'the drafted undo still shows the value it is about to discard').toBe('1000.00')
    expect(out[0].corrected, 'a drafted undo still renders its marker, its was-line and its Undo').toBeNull()
  })

  it('empties an undone field the extractor never read', () => {
    // buyer_tin and vat carry a NULL rank-0 reading on the deployed mock, and the server CLEARS
    // the invoice column for them. An input cannot render null, so '' is how the screen says the
    // same thing. The artboard's `f.was || value` would keep the typed value on screen instead
    // -- a deliberate deviation, because it reopens the screen/register divergence the
    // server-side undo exists to close.
    const fields = [
      mkField({
        name: 'buyer_tin',
        value: '31775208-0003',
        region: null,
        corrected: { method: 'typed', was: null, where: null },
      }),
    ]
    const entries: DraftEntries = { buyer_tin: { kind: 'undone', value: '31775208-0003', region: null } }

    const out = applyDraft(fields, entries)

    expect(out[0].value, "the undo kept a value the extractor never read and the register will not hold").toBe('')
    expect(out[0].corrected, 'a drafted undo still renders its marker, its was-line and its Undo').toBeNull()
  })

  it('leaves a field with no entry byte-identical', () => {
    // A do-nothing PASSES this row, and it is kept anyway: it catches the opposite mutation, a
    // mapper that rewrites every field. Stated rather than counted as coverage.
    const untouched = [
      mkField({ name: 'subtotal', value: '950.00', reason: 'inconsistent' }),
      mkField({ name: 'vat', value: null, region: null, reason: 'unreadable' }),
    ]
    const fields = [mkField({ name: 'total', value: '1000.00' }), ...untouched]
    const entries: DraftEntries = { total: { kind: 'typed', value: '2,000.00', region: null } }

    const out = applyDraft(fields, entries)

    expect(out.map((f) => f.name), 'the draft reordered the wire').toEqual(['total', 'subtotal', 'vat'])
    expect(out[1], 'a field nobody drafted was rewritten').toEqual(untouched[0])
    expect(out[2], 'a field nobody drafted was rewritten').toEqual(untouched[1])
  })
})

describe('savableCorrections', () => {
  it('emits one request per entry, in HeaderFields order', () => {
    // The entries are inserted in the REVERSE of the vocabulary order, so `Object.keys` alone
    // answers total, vat, issue_date and fails here -- the order clause is not a tie.
    const fields = [
      mkField({ name: 'issue_date', value: '2026-01-01' }),
      mkField({ name: 'vat', value: '75.00' }),
      mkField({ name: 'total', value: '1000.00' }),
    ]
    const entries: DraftEntries = {
      total: { kind: 'typed', value: '2,000.00', region: null },
      vat: { kind: 'typed', value: '150.00', region: null },
      issue_date: { kind: 'chosen', value: '2026-01-10', region: mkRegion({ page: 3 }) },
    }

    const out = savableCorrections(fields, entries)

    expect(out.map((p) => p.field), 'the POSTs do not follow the order the person reads').toEqual([
      'issue_date',
      'vat',
      'total',
    ])
    expect(out.map((p) => p.body.method)).toEqual(['chosen', 'typed', 'typed'])
    expect(out.map((p) => p.body.value)).toEqual(['2026-01-10', '150.00', '2,000.00'])

    // A non-pointed request carrying a region is a 400 on the deployed build
    // (msgRegionDisagrees), even when the draft knows the box the chip came from.
    for (const p of out) {
      expect(p.body.region, `${p.field} sent a region on a ${p.body.method} correction`).toBeNull()
      expect(p.body.anchor_label, `${p.field} sent an anchor label`).toBe('')
    }

    expect(savableCorrections(fields, {}), 'an empty draft owes a POST').toEqual([])
  })

  it('drops an entry equal to the value already on the wire', () => {
    // diffEditInput's rule (invoices.ts): a no-op correction recorded as a human decision is a
    // lie the append-only table cannot take back.
    const fields = [mkField({ name: 'subtotal', value: '950.00' }), mkField({ name: 'total', value: '1000.00' })]
    const entries: DraftEntries = {
      subtotal: { kind: 'typed', value: '950.00', region: null },
      total: { kind: 'typed', value: '1,500.00', region: null },
    }

    const out = savableCorrections(fields, entries)

    expect(out.map((p) => p.field), 'a no-op correction was recorded as a human decision').toEqual(['total'])
  })

  it('drops a typed entry the person emptied, whitespace included', () => {
    // The boundary 400s a blank value (msgBlankValue), so the round trip and its message buy
    // nothing. The neighbour is what makes the absence real: an implementation that emits
    // nothing at all passes the first clause on its own.
    const fields = [
      mkField({ name: 'subtotal', value: '950.00' }),
      mkField({ name: 'vat', value: '75.00' }),
      mkField({ name: 'total', value: '1000.00' }),
    ]
    const entries: DraftEntries = {
      subtotal: { kind: 'typed', value: '', region: null },
      vat: { kind: 'typed', value: '   ', region: null },
      total: { kind: 'typed', value: '1,500.00', region: null },
    }

    const out = savableCorrections(fields, entries)

    expect(out.map((p) => p.field), 'a blank the boundary refuses was posted anyway').toEqual(['total'])
  })

  it('keeps a chosen entry even when its value equals the wire', () => {
    // The sibling of the undone row below, and for the same reason: the no-op guard is about
    // the VALUE, and a chosen entry is a decision about the FIELD. Agreeing with the extractor
    // is what clears the reason and the chip row, so dropping it leaves an ambiguous field the
    // only one a person cannot settle -- their routes would be to pick a reading they believe
    // is wrong, or leave it flagged forever.
    const fields = [
      mkField({
        name: 'issue_date',
        value: '2026-01-01',
        reason: 'ambiguous',
        region: mkRegion({ page: 1 }),
        alternatives: [{ value: '2026-01-10', region: mkRegion({ page: 3 }) }],
      }),
    ]
    const entries: DraftEntries = {
      issue_date: { kind: 'chosen', value: '2026-01-01', region: mkRegion({ page: 1 }) },
    }

    const out = savableCorrections(fields, entries)

    expect(
      out.map((p) => p.field),
      'agreeing with the extractor recorded nothing, so the field can never settle',
    ).toEqual(['issue_date'])
    expect(out[0].body.method, 'the settle was recorded as some other method').toBe('chosen')
    expect(out[0].body.value).toBe('2026-01-01')
  })

  it('keeps an undone entry even when its value equals the wire', () => {
    // The no-op guard is about the VALUE; an undo is a decision about the CORRECTION, and the
    // server ignores the value it carries. A guard keyed on the value alone swallows it.
    const fields = [mkField({ name: 'total', value: '1,500.00', corrected: { method: 'typed', was: '1000.00', where: null } })]
    const entries: DraftEntries = { total: { kind: 'undone', value: '1,500.00', region: null } }

    const out = savableCorrections(fields, entries)

    expect(out.map((p) => p.field), 'the undo was dropped as a no-op').toEqual(['total'])
    expect(out[0].body.method).toBe('undone')
    expect(out[0].body.value, 'an undo posts a blank value and the boundary 400s it').not.toBe('')
  })
})

// ==========================================================================================
// EXTR-12-08 — pointing at a region. Written RED against the five stubs `c994ecee` landed
// (page 0, -1 coordinates, `undone`, `undefined`), so every row below fails on its own
// assertion and none can pass on a constant.
//
// The `/72/` and `/dpi/i` source guards read MODULE_SRC (`extractionReview.ts`), never this
// file, so the arithmetic and the colour literal below are unscanned here and scrubbed there.
// ==========================================================================================

// highlightStyle emits its percentages through round4 (`Number(v.toFixed(4))`), so the
// browser can only place a corner on a 4-decimal percentage. The fixtures below reproduce
// that quantization rather than idealising it.
function r4(n: number): number {
  return Number(n.toFixed(4))
}

// x0 carries SEVEN decimals on purpose. At six (`.123456`) round4 is the identity — measured,
// `Number((12.3456).toFixed(4)) === 12.3456` — so the tolerance would absorb nothing. At seven
// the round trip loses 3.0e-7: inside 1e-6, outside 1e-7.
// The four coordinates are pairwise distinct and the box is wider (.4465) than it is tall
// (.21), so an axis swap or a transposed divisor cannot agree with it.
const DRAWN: ExtractionRegion = { page: 2, x0: 0.1234567, y0: 0.41, x1: 0.57, y1: 0.62 }

// One origin, three sizes. Non-zero `left`/`top`, so a missing translate is visible; three
// DISTINCT widths and heights, so this is three geometries and not one measured three times.
const FRAMES: FrameBox[] = [
  { left: 37, top: 91, width: 560, height: 725 },
  { left: 37, top: 91, width: 620, height: 803 },
  { left: 37, top: 91, width: 960, height: 1243 },
]

/** Where the browser puts a normalised coordinate on one axis of one frame. */
function atX(f: FrameBox, v: number): number {
  return f.left + (r4(v * 100) / 100) * f.width
}

function atY(f: FrameBox, v: number): number {
  return f.top + (r4(v * 100) / 100) * f.height
}

const AXES = ['x0', 'y0', 'x1', 'y1'] as const

describe('normaliseBox', () => {
  it('turns a drag into the box highlightStyle would draw, at three frame sizes', () => {
    // Floor: three different frames, or "at every zoom" is one geometry measured three times.
    expect(new Set(FRAMES.map((f) => f.width)).size, 'the three frames are not three sizes').toBe(3)
    expect(new Set(FRAMES.map((f) => f.height)).size, 'the three frames share a height').toBe(3)

    for (const f of FRAMES) {
      const a: ViewportPoint = { x: atX(f, DRAWN.x0), y: atY(f, DRAWN.y0) }
      const b: ViewportPoint = { x: atX(f, DRAWN.x1), y: atY(f, DRAWN.y1) }

      const got = normaliseBox(f, a, b, 2)

      expect(got.page, `frame ${f.width}: the box claims page ${got.page}, and the drag was on page 2`).toBe(2)

      // Clause 1 — the tolerance bounds the ROUND-TRIP ERROR. 1e-6 is AC-2's own claim and
      // 3.0e-7 of it is spent by the rendering's own 4-decimal grid.
      for (const axis of AXES) {
        expect(
          Math.abs(got[axis] - DRAWN[axis]),
          `frame ${f.width}: ${axis} came back ${got[axis]}, and the drag was drawn at ${DRAWN[axis]}`,
        ).toBeLessThan(1e-6)
      }

      // Clause 2 — the quantization proves ROUND4 RAN. The clause above is satisfied by an
      // identity that never renders at all, because the input is within 1e-6 of itself; this
      // one asks whether the value landed on the grid round4 produces. A correct build sits
      // ~3e-11 off the integer; an unrendered .1234567 sits 0.3 off it.
      expect(
        Math.abs(got.x0 * 1e6 - Math.round(got.x0 * 1e6)),
        `frame ${f.width}: x0 is ${got.x0}, which is not on the 4-decimal grid the browser renders`,
      ).toBeLessThan(1e-3)
    }
  })

  it('orders the corners, so a drag up and to the left is still a legal box', () => {
    // An ordinary gesture: the hand starts bottom-right. `bbox_normalised` (the migration's
    // CHECK) and normalisedBox (handlers_correction.go) both refuse x0 > x1.
    const f = FRAMES[0]
    const bottomRight: ViewportPoint = { x: atX(f, DRAWN.x1), y: atY(f, DRAWN.y1) }
    const topLeft: ViewportPoint = { x: atX(f, DRAWN.x0), y: atY(f, DRAWN.y0) }

    const got = normaliseBox(f, bottomRight, topLeft, 2)

    expect(got.x0, `x0 came back ${got.x0} and x1 ${got.x1} — the boundary refuses that box`).toBeLessThan(got.x1)
    expect(got.y0, `y0 came back ${got.y0} and y1 ${got.y1} — the boundary refuses that box`).toBeLessThan(got.y1)
    for (const axis of AXES) {
      expect(
        Math.abs(got[axis] - DRAWN[axis]),
        `dragged the other way, ${axis} came back ${got[axis]} instead of ${DRAWN[axis]}`,
      ).toBeLessThan(1e-6)
    }
  })

  it('clamps a release outside the frame into [0,1]', () => {
    // A fast exit delivers a mouseup at a fractionally out-of-frame coordinate before
    // mouseleave. Un-clamped this posts -0.0645 and 1.0967, the two values the boundary 400s.
    const f = FRAMES[1]
    const a: ViewportPoint = { x: f.left - 40, y: f.top - 25 }
    const b: ViewportPoint = { x: f.left + f.width + 60, y: f.top + f.height + 30 }

    const got = normaliseBox(f, a, b, 3)

    // Four separate assertions, so a clamp on one axis only still reds.
    expect(got.page, `the box claims page ${got.page}`).toBe(3)
    expect(got.x0, `x0 escaped the page at ${got.x0}`).toBe(0)
    expect(got.y0, `y0 escaped the page at ${got.y0}`).toBe(0)
    expect(got.x1, `x1 escaped the page at ${got.x1}`).toBe(1)
    expect(got.y1, `y1 escaped the page at ${got.y1}`).toBe(1)
  })
})

describe('isDrawnBox', () => {
  it("admits the artboard's floor and refuses everything under it", () => {
    // The artboard refuses `b.w < 24 || b.h < 12`, so 24x12 itself passes. The last row is an
    // ordinary up-and-left drag: without Math.abs it reads as a negative box and is refused.
    const from: ViewportPoint = { x: 100, y: 200 }
    const by = (w: number, h: number): ViewportPoint => ({ x: from.x + w, y: from.y + h })

    for (const [w, h, want] of [
      [24, 12, true],
      [23, 12, false],
      [24, 11, false],
      [0, 0, false],
      [300, 300, true],
      [-40, -20, true],
    ] as const) {
      expect(isDrawnBox(from, by(w, h)), `a ${w}x${h} gesture`).toBe(want)
    }
  })
})

describe('pointBoxStyle', () => {
  it("draws the region highlightStyle would, in the artboard's own amber", () => {
    // Asymmetric on both axes and on page 4, so a swapped axis or a hardcoded page cannot agree.
    const region = mkRegion({ page: 4, x0: 0.13, y0: 0.41, x1: 0.57, y1: 0.62 })

    const box = pointBoxStyle(region)
    const highlight = highlightStyle(region)

    // The geometry is highlightStyle's, read off highlightStyle rather than retyped: the live
    // box and the settled highlight must resolve against the same padding box.
    for (const key of ['left', 'top', 'width', 'height'] as const) {
      expect(box[key], `the live box's ${key} is ${String(box[key])}, the highlight's is ${String(highlight[key])}`).toBe(
        highlight[key],
      )
    }

    expect(box.position).toBe('absolute')
    expect(box.pointerEvents, 'the live box swallows the drag it is drawn by').toBe('none')
    expect(box.border, "the artboard's drag box is a 2px amber outline").toBe('2px solid var(--accent)')
    expect(box.background).toBe('oklch(72% .15 65 / .18)')
    expect(box.borderRadius).toBe(2)

    // The likely mutant is a SPREAD of highlightStyle, which passes every clause above while
    // inheriting the settled highlight's 3px ring and its transition — a treatment the
    // artboard's own drag box does not have. Presence-only assertions cannot see what rides
    // along, so both are asserted absent.
    expect(box.boxShadow, 'the live box inherited the settled highlight ring').toBeUndefined()
    expect(box.transition, 'the live box inherited the settled highlight transition').toBeUndefined()
  })
})

const POINTED_BOX = mkRegion({ page: 4, x0: 0.11, y0: 0.22, x1: 0.33, y1: 0.44 })
const CHOSEN_BOX = mkRegion({ page: 3 })

describe('typedEntry', () => {
  it('keeps a pointed entry pointed and its box', () => {
    // Three arms, three different answers: no constant survives. Typing beside a box you drew
    // does not unsay the box; typing over a chip's value does drop the chip's provenance.
    expect(
      typedEntry({ kind: 'pointed', value: '31775208-0000', region: POINTED_BOX }, '31775208-0003'),
      'typing beside a drawn box threw the box away',
    ).toEqual({ kind: 'pointed', value: '31775208-0003', region: POINTED_BOX })

    expect(
      typedEntry({ kind: 'chosen', value: '2026-01-10', region: CHOSEN_BOX }, '2026-02-11'),
      "typing over a chosen chip kept the chip's box as the provenance",
    ).toEqual({ kind: 'typed', value: '2026-02-11', region: null })

    expect(typedEntry(undefined, '1,500.00'), 'typing into an untouched field').toEqual({
      kind: 'typed',
      value: '1,500.00',
      region: null,
    })
  })
})

describe('pointedEntry', () => {
  it('keeps the value the person already has', () => {
    // The box records WHERE and the person types WHAT: nothing in this build reads text out of
    // a region, so drawing on an untouched field yields a blank-valued waypoint.
    expect(pointedEntry(undefined, null, POINTED_BOX), 'a box on a field the extractor read nothing for').toEqual({
      kind: 'pointed',
      value: '',
      region: POINTED_BOX,
    })

    expect(pointedEntry(undefined, 'MOCK-INV-0001', POINTED_BOX), "a box on a field the extractor did read").toEqual({
      kind: 'pointed',
      value: 'MOCK-INV-0001',
      region: POINTED_BOX,
    })

    expect(
      pointedEntry({ kind: 'typed', value: '31775208-0003', region: null }, 'ignored', POINTED_BOX),
      'drawing a box discarded the value the person had already typed',
    ).toEqual({ kind: 'pointed', value: '31775208-0003', region: POINTED_BOX })
  })
})

describe('applyDraft, pointed', () => {
  it("moves a pointed field's highlight to the box that was drawn", () => {
    // `region: null` on the wire is what makes this discriminating: no existing box can be
    // mistaken for the drawn one, so a build with no pointed arm renders no highlight at all.
    const fields = [mkField({ name: 'buyer_tin', value: null, region: null, reason: 'missing' })]
    const entries: DraftEntries = {
      buyer_tin: { kind: 'pointed', value: '31775208-0003', region: POINTED_BOX },
    }

    const out = applyDraft(fields, entries)

    expect(out, 'the draft dropped or duplicated a field').toHaveLength(1)
    expect(out[0].value, 'the pointed value never reached the field').toBe('31775208-0003')
    expect(out[0].region, 'the drawn box never reached the field, so nothing can highlight it').toEqual(POINTED_BOX)
    // The two lies a drafted point must not tell: nothing is recorded until Save, and the
    // field is still the one the extractor could not find.
    expect(out[0].corrected, 'a drafted point claimed a correction nobody has recorded').toBeNull()
    expect(out[0].reason, 'a drafted point cleared the reason the server still holds').toBe('missing')
  })
})

describe('savableCorrections, pointed', () => {
  it("sends a pointed correction's box, and keeps it when the value has not moved", () => {
    // A pointed entry is never a no-op: its region is always new, and it is the anchor context
    // a correction is required to store. The `total` neighbour closes the other direction —
    // a mutant dropping every no-op regardless of kind returns [].
    const fields = [mkField({ name: 'buyer_tin', value: '31775208-0003' }), mkField({ name: 'total', value: '1000.00' })]
    const entries: DraftEntries = {
      buyer_tin: { kind: 'pointed', value: '31775208-0003', region: POINTED_BOX },
      total: { kind: 'typed', value: '1000.00', region: null },
    }

    const out = savableCorrections(fields, entries)

    expect(
      out.map((p) => p.field),
      'pointing at the right place for an already-right value recorded nothing, so the box is lost',
    ).toEqual(['buyer_tin'])
    expect(out[0].body, 'the drawn box never reached the wire').toEqual({
      value: '31775208-0003',
      method: 'pointed',
      region: POINTED_BOX,
      anchor_label: '',
    })
  })

  it('drops a point nobody typed a value into', () => {
    // msgBlankValue 400s it, so the round trip and its message buy nothing. The neighbour is
    // what makes the absence real: an implementation returning [] passes the first clause alone.
    const fields = [mkField({ name: 'buyer_tin', value: null }), mkField({ name: 'total', value: '1000.00' })]
    const entries: DraftEntries = {
      buyer_tin: { kind: 'pointed', value: '   ', region: POINTED_BOX },
      total: { kind: 'typed', value: '1,500.00', region: null },
    }

    const out = savableCorrections(fields, entries)

    expect(
      out.map((p) => p.field),
      'a valueless point was posted, and the boundary refuses a blank value',
    ).toEqual(['total'])
  })
})
