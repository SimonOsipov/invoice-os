// The document pane: the toolbar, one aspect-locked frame per page, and the highlight for
// the selected field's region. The frame and highlight geometry is spread from
// extractionReview.ts, whose values extractionReview.test.ts pins; the toolbar, banner and
// ground chrome below is declared here and pinned by ExtractionCanvas.test.tsx.

import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type MouseEvent } from 'react'

import { gatewayBase } from '@invoice-os/api-client'

import { infoGlyph } from '../glyphs'
import {
  docMetaLine,
  fetchPageImage,
  highlightStyle,
  isDrawnBox,
  normaliseBox,
  pageFrameStyle,
  pointBoxStyle,
  scrollRegionIntoView,
  type ExtractionDocument,
  type ExtractionFieldState,
  type ExtractionPage,
  type ExtractionRegion,
  type FrameBox,
  type ViewportPoint,
} from '../lib/extractionReview'
import type { DocumentBytes } from '../lib/sourceDocument'
import type { PlatformCtx } from '../types'
import { fileTypeTone, formatLabel } from './SourceDocumentStates'

const ZOOMS = [0.5, 1, 1.5]

// One frame of lead-in on each side: a page card is ~830px tall at the band's 640px cap, and
// a 0px margin would only load a frame once it is already on screen.
const PRELOAD_MARGIN = '800px 0px'

const NO_REGION = 'We have no region for this field, so there is nothing to highlight.'
const NO_PAGES = 'This document has no page images.'
const PAGE_FAILED = 'This page could not be loaded.'

const PANE: CSSProperties = {
  flex: '1 1 auto',
  minWidth: 0,
  minHeight: 0,
  display: 'flex',
  flexDirection: 'column',
  borderRight: '1px solid var(--line-1)',
}

const TOOLBAR: CSSProperties = {
  flex: 'none',
  display: 'flex',
  alignItems: 'center',
  gap: 11,
  padding: '10px 16px',
  background: 'var(--bg-2)',
  borderBottom: '1px solid var(--line-1)',
}

const TILE: CSSProperties = {
  flex: 'none',
  width: 32,
  height: 32,
  borderRadius: 8,
  display: 'grid',
  placeItems: 'center',
  fontFamily: 'var(--font-mono)',
  fontSize: 9,
  fontWeight: 700,
}

const FILENAME: CSSProperties = {
  fontSize: 13,
  fontWeight: 600,
  letterSpacing: '-0.01em',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
}

const META: CSSProperties = {
  fontSize: 10,
  color: 'var(--fg-3)',
  letterSpacing: '0.04em',
  marginTop: 2,
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
}

const ZOOM_GROUP: CSSProperties = {
  whiteSpace: 'nowrap',
  flex: 'none',
  display: 'flex',
  alignItems: 'center',
  gap: 2,
  padding: 2,
  background: 'var(--bg-1)',
  border: '1px solid var(--line-2)',
  borderRadius: 999,
}

const SEGMENT: CSSProperties = {
  whiteSpace: 'nowrap',
  flex: 'none',
  height: 26,
  padding: '0 12px',
  // A plain button, never `.pf-btn`/`.pf-chip`: both force `border-radius` with `!important`,
  // from app-layer.css:193-197 and :275 respectively.
  borderRadius: 999,
  border: '1px solid transparent',
  fontFamily: 'var(--font-sans)',
  fontSize: 12.5,
  fontWeight: 500,
  cursor: 'pointer',
}

const BANNER: CSSProperties = {
  flex: 'none',
  display: 'flex',
  alignItems: 'center',
  gap: 9,
  padding: '9px 16px',
  background: 'var(--bg-3)',
  borderBottom: '1px solid var(--line-1)',
}

const GROUND: CSSProperties = { flex: 1, minHeight: 0, overflow: 'auto' }

const GROUND_PAD: CSSProperties = {
  position: 'relative',
  minHeight: '100%',
  padding: '24px 24px 40px',
  background: 'var(--bg-0)',
}

const EMPTY_PANEL: CSSProperties = {
  padding: '14px 16px',
  border: '1px dashed var(--line-3)',
  borderRadius: 'var(--radius-md)',
  background: 'transparent',
  fontSize: 12.5,
  color: 'var(--fg-3)',
}

// Dashed rather than the solid `1px solid var(--status-red-border)` every other failure panel
// in the app uses: this one sits inside an aspect-locked frame where the missing bytes are the
// absence, so it takes the dashed empty-panel idiom in status red.
const FAILED_PANEL: CSSProperties = {
  ...EMPTY_PANEL,
  margin: 16,
  border: '1px dashed var(--status-red-border)',
}

// Four insets, so the overlay fills the frame's PADDING box -- the box highlightStyle's
// percentages resolve against, which is what makes the drag exact with no border arithmetic.
// `userSelect` is the other half of the mousedown's preventDefault.
const POINT_SURFACE: CSSProperties = {
  position: 'absolute',
  left: 0,
  top: 0,
  right: 0,
  bottom: 0,
  cursor: 'crosshair',
  userSelect: 'none',
}

type PageSlot = DocumentBytes | 'error'

/** One drag in progress: the page it began on, the surface it was measured against, its ends. */
interface PointDrag {
  page: number
  frame: FrameBox
  a: ViewportPoint
  b: ViewportPoint
}

function frameBoxOf(el: HTMLElement): FrameBox {
  const r = el.getBoundingClientRect()
  return { left: r.left, top: r.top, width: r.width, height: r.height }
}

export function ExtractionCanvas({
  ctx,
  jobId,
  doc,
  pages,
  fields,
  selected,
  scrollNonce,
  armed,
  onPoint,
}: {
  ctx: PlatformCtx
  jobId: string
  /** Named `doc`, not `document`: a prop called `document` shadows the global. */
  doc: ExtractionDocument
  pages: ExtractionPage[]
  fields: ExtractionFieldState[]
  selected: string | null
  /**
   * Bumped per click by the caller, so re-selecting the same row re-centres it (`D-25`).
   * Required, not optional: nothing else the pane already receives moves on a repeat click,
   * so a caller that omits this silently loses the re-centre.
   */
  scrollNonce: number
  /** The one field waiting for a box, or null. EXTR-12-08 renders the drag surface from it. */
  armed: string | null
  /** A completed drag, already normalised against the frame it was drawn on. */
  onPoint: (region: ExtractionRegion) => void
}) {
  const base = gatewayBase()
  const [zoom, setZoom] = useState(1)
  const groundRef = useRef<HTMLDivElement | null>(null)

  // Every handle ever created, released together — a page-keyed map loses three of six under
  // StrictMode's doubled mount. release() is idempotent, so order cannot matter
  // (SourceDocumentModal.tsx:84-117).
  const created = useRef<DocumentBytes[]>([])
  const requested = useRef<Set<number>>(new Set())
  const disposed = useRef(false)
  const jobRef = useRef(jobId)

  // Tagged with the job it was loaded under, so a stale map self-invalidates on a jobId change
  // without a setState in the cleanup that releases it.
  const [loaded, setLoaded] = useState<{ jobId: string; slots: Record<number, PageSlot> }>({ jobId, slots: {} })
  const slots = loaded.jobId === jobId ? loaded.slots : {}

  const ordered = useMemo(() => [...pages].sort((a, b) => a.page - b.page), [pages])

  const selectedField = fields.find((f) => f.name === selected) ?? null
  const region = selectedField?.region ?? null

  // Declared before the loading effect: its setup must clear `disposed` before any load runs.
  useEffect(() => {
    disposed.current = false
    jobRef.current = jobId
    // The shell cannot reach the ground behind this ref, so the reset lives here (`D-24`).
    if (groundRef.current) groundRef.current.scrollTop = 0
    return () => {
      disposed.current = true
      for (const h of created.current) h.release()
      created.current = []
      requested.current = new Set()
    }
  }, [jobId])

  const load = useCallback(
    (page: number) => {
      if (!base || requested.current.has(page)) return
      requested.current.add(page)
      const job = jobId
      const write = (slot: PageSlot) =>
        setLoaded((prev) => (prev.jobId === job ? { jobId: job, slots: { ...prev.slots, [page]: slot } } : { jobId: job, slots: { [page]: slot } }))
      fetchPageImage(ctx.getToken, base, job, page)
        .then((handle) => {
          // The URL is created before this resolves, so a pane or a job that is already gone
          // has to drop the handle here rather than leak it.
          if (disposed.current || jobRef.current !== job) {
            handle.release()
            return
          }
          created.current.push(handle)
          write(handle)
        })
        .catch(() => {
          if (!disposed.current && jobRef.current === job) write('error')
        })
    },
    [base, ctx, jobId],
  )

  useEffect(() => {
    const ground = groundRef.current
    const frames = ground ? Array.from(ground.querySelectorAll<HTMLElement>('[data-page]')) : []
    const pageOf = (el: Element) => Number((el as HTMLElement).dataset.page)

    // jsdom implements no IntersectionObserver; without the constructor every frame loads at
    // mount, which is the eager behaviour the observer exists to avoid on an 800-page document.
    const Observer = typeof IntersectionObserver === 'undefined' ? null : IntersectionObserver
    if (!Observer) {
      for (const el of frames) load(pageOf(el))
      return
    }

    const io = new Observer(
      (entries) => {
        for (const e of entries) if (e.isIntersecting) load(pageOf(e.target))
      },
      { root: ground, rootMargin: PRELOAD_MARGIN },
    )
    for (const el of frames) io.observe(el)
    return () => io.disconnect()
  }, [ordered, load])

  // A selection must not race the scroll callback for its own page's bytes.
  useEffect(() => {
    if (region) load(region.page)
  }, [region, load])

  // Deps are the selection and the shell's per-click nonce: a re-render that changed neither
  // must not scroll again.
  useEffect(() => {
    if (selected && region) scrollRegionIntoView(groundRef.current, selected)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selected, jobId, scrollNonce])

  // No ref: each handler is ON the element it measures, and re-reading per event keeps the rect
  // right if the ground scrolls mid-drag. `e.currentTarget` is read at the TOP of each handler
  // -- React nulls it on return, so a read inside a setState updater would be too late.
  const [drag, setDrag] = useState<PointDrag | null>(null)
  const live = drag === null ? null : normaliseBox(drag.frame, drag.a, drag.b, drag.page)

  // Re-arming mid-drag must not strand the box on the field that is no longer waiting for it.
  useEffect(() => {
    setDrag(null)
  }, [armed])

  const pointDown = (page: number) => (e: MouseEvent<HTMLDivElement>) => {
    const frame = frameBoxOf(e.currentTarget)
    e.preventDefault()
    const at = { x: e.clientX, y: e.clientY }
    setDrag({ page, frame, a: at, b: at })
  }

  const pointMove = (e: MouseEvent<HTMLDivElement>) => {
    if (drag === null) return
    const frame = frameBoxOf(e.currentTarget)
    setDrag({ ...drag, frame, b: { x: e.clientX, y: e.clientY } })
  }

  const pointUp = (e: MouseEvent<HTMLDivElement>) => {
    if (drag === null) return
    const frame = frameBoxOf(e.currentTarget)
    const at = { x: e.clientX, y: e.clientY }
    setDrag(null)
    // Under the floor the gesture was a click, not a box, and the boundary would be asked to
    // store a region nobody meant to draw.
    if (!isDrawnBox(drag.a, at)) return
    onPoint(normaliseBox(frame, drag.a, at, drag.page))
  }

  const tone = fileTypeTone(doc.filename, doc.content_type)
  const banner = selectedField !== null && selectedField.region === null

  return (
    <div data-testid="extraction-canvas" style={PANE}>
      <div data-testid="extraction-toolbar" style={TOOLBAR}>
        <div style={{ ...TILE, background: tone.bg, color: tone.fg }}>{formatLabel(doc.filename, doc.content_type)}</div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={FILENAME}>{doc.filename ?? '—'}</div>
          <div data-testid="extraction-doc-meta" className="mono" style={META}>
            {docMetaLine(doc, ordered.length)}
          </div>
        </div>
        <div style={ZOOM_GROUP}>
          {ZOOMS.map((z) => (
            <button
              key={z}
              type="button"
              data-testid={`extraction-zoom-${z * 100}`}
              aria-pressed={zoom === z}
              onClick={() => setZoom(z)}
              style={{ ...SEGMENT, background: zoom === z ? 'var(--action)' : 'transparent', color: zoom === z ? 'var(--text-on-dark)' : 'var(--fg-2)' }}
            >
              {`${z * 100}%`}
            </button>
          ))}
        </div>
      </div>

      {banner ? (
        <div data-testid="extraction-no-region" style={BANNER}>
          <span style={{ display: 'flex', flex: 'none', color: 'var(--fg-3)' }}>{infoGlyph}</span>
          <span style={{ fontSize: 12, color: 'var(--fg-2)' }}>{NO_REGION}</span>
        </div>
      ) : null}

      <div ref={groundRef} data-testid="extraction-ground" style={GROUND}>
        <div style={GROUND_PAD}>
          {ordered.length === 0 ? (
            <div style={EMPTY_PANEL}>{NO_PAGES}</div>
          ) : (
            ordered.map((p) => {
              const slot = slots[p.page]
              return (
                <div key={p.page} data-testid={`extraction-page-${p.page}`} data-page={p.page} style={pageFrameStyle(p, zoom)}>
                  {slot === 'error' ? <div style={FAILED_PANEL}>{PAGE_FAILED}</div> : null}
                  {slot && slot !== 'error' ? (
                    <img
                      data-testid={`extraction-page-image-${p.page}`}
                      src={slot.url}
                      alt={`Page ${p.page}`}
                      style={{ width: '100%', display: 'block' }}
                    />
                  ) : null}
                  {selectedField && region && region.page === p.page ? (
                    <div data-testid="extraction-highlight" data-snip={selectedField.name} style={highlightStyle(region)} />
                  ) : null}
                  {/* Last child, one per frame: the surface carries its own page, so a drag on
                      page 2 posts page 2 with no hit test. The live box is its child, so both
                      resolve against the same padding box. */}
                  {armed === null ? null : (
                    <div
                      data-testid={`extraction-point-surface-${p.page}`}
                      style={POINT_SURFACE}
                      onMouseDown={pointDown(p.page)}
                      onMouseMove={pointMove}
                      onMouseUp={pointUp}
                      onMouseLeave={() => setDrag(null)}
                    >
                      {live && live.page === p.page ? (
                        <div data-testid="extraction-point-box" style={pointBoxStyle(live)} />
                      ) : null}
                    </div>
                  )}
                </div>
              )
            })
          )}
        </div>
      </div>
    </div>
  )
}
