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
  correctedMarker,
  docMetaLine,
  fetchPageImage,
  fieldLabel,
  fieldNote,
  getExtractionDetail,
  highlightStyle,
  pageFrameStyle,
  reasonPill,
  scrollRegionIntoView,
} from './extractionReview'
import type { ExtractionDetail, ExtractionDocument, ExtractionPage, ExtractionRegion } from './extractionReview'

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
