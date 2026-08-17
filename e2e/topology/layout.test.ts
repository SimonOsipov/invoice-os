// Unit tests for layout.ts's pure half. assertFillsColumn needs a browser and a
// deployed app, so it is exercised by invoice-surfaces.spec.ts in the topology
// suite; `gaps` is where the arithmetic that BUG-03-05 got wrong actually lives.
import { describe, expect, it } from 'vitest'

import * as layout from './layout'
import { gaps, WIDE_WIDTHS, type Box } from './layout'

describe('gaps', () => {
  it('reports zero on both sides when the inner element fills the outer one', () => {
    const outer: Box = { x: 252, width: 1668 }
    expect(gaps({ x: 252, width: 1668 }, outer)).toEqual({ left: 0, right: 0 })
  })

  it('reports BUG-03-05 as it actually shipped: pinned left, a band stranded right', () => {
    // The measured production defect. `.pf-scroll` starts at x=252 (the sidebar's
    // width) and runs 1668px at a 1920 viewport; the detail was capped at 1080 and
    // pinned to the left edge, leaving 588px dead. The check that shipped WITH the
    // bug was `width <= 1082`, and 1080 satisfies it.
    const outer: Box = { x: 252, width: 1668 }
    const capped: Box = { x: 252, width: 1080 }

    expect(capped.width).toBeLessThanOrEqual(1082) // the assertion that passed
    expect(gaps(capped, outer)).toEqual({ left: 0, right: 588 }) // what it missed
  })

  it('sees a RIGHT-pinned cap, which a right-only measurement reads as a perfect fit', () => {
    const outer: Box = { x: 252, width: 1668 }
    const pinnedRight: Box = { x: 252 + 588, width: 1080 }

    const g = gaps(pinnedRight, outer)
    expect(g.right).toBe(0) // a right-only check would pass here
    expect(g.left).toBe(588) // the same 588px of dead space, on the other side
  })

  it('splits a centred cap across both sides rather than reporting half of it', () => {
    const outer: Box = { x: 252, width: 1668 }
    const centred: Box = { x: 252 + 294, width: 1080 }
    expect(gaps(centred, outer)).toEqual({ left: 294, right: 294 })
  })

  it('reports a negative gap when the inner element overflows its container', () => {
    const outer: Box = { x: 0, width: 800 }
    expect(gaps({ x: 0, width: 900 }, outer)).toEqual({ left: 0, right: -100 })
  })
})

describe('WIDE_WIDTHS', () => {
  it('runs widest first, because the wide end is where a cap can strand a band', () => {
    expect([...WIDE_WIDTHS]).toEqual([...WIDE_WIDTHS].sort((a, b) => b - a))
  })

  it('starts above 1280, which is Playwright\'s default and every other sweep\'s ceiling', () => {
    expect(Math.max(...WIDE_WIDTHS)).toBeGreaterThan(1280)
    expect(WIDE_WIDTHS).toContain(1280)
  })
})

// --- LAND-05: the 2D overlap primitive ---------------------------------------
//
// `gaps` answers a one-axis question. The cookie notice is `position: fixed` in the
// bottom-left and the closing CTA's copy is in normal flow, so the two share an x band
// at every width in WIDE_WIDTHS and the whole clearance claim lives on y. A horizontal
// check reads that as a permanent failure and an area scalar cannot tell a 2px graze
// from a 232px one — the same "a cap and its placement are two facts" defect this file
// already carries for BUG-03-05.
//
// Rect / rectsOverlap / overlapOf do not exist yet. They are resolved through the module
// namespace so `tsc --noEmit` stays green while these specs are red, and so a missing
// export fails as a named assertion rather than as a TypeError inside an arbitrary case.
type Rect = { x: number; y: number; width: number; height: number }

type OverlapApi = {
  rectsOverlap: (a: Rect, b: Rect) => boolean
  overlapOf: (a: Rect, b: Rect) => Rect
}

const api = layout as unknown as Partial<OverlapApi>

function rectsOverlap(a: Rect, b: Rect): boolean {
  expect(typeof api.rectsOverlap, 'layout.ts does not export rectsOverlap').toBe('function')
  return api.rectsOverlap!(a, b)
}

function overlapOf(a: Rect, b: Rect): Rect {
  expect(typeof api.overlapOf, 'layout.ts does not export overlapOf').toBe('function')
  return api.overlapOf!(a, b)
}

// The LAND-05 desktop shapes at 1280x720, taken from the shipped CSS and DemoCta's grid:
//   .cookie-note   left 24, bottom 24, width 460            (landing.css:180-185)
//   #demo          maxWidth 1280, padding '96px 32px'; card padding '64px 56px',
//                  grid '1.2fr 0.8fr' gap 48, <p> maxWidth 440   (DemoCta.tsx:7,15-17,31)
// so the left column opens at x=88 and the right-hand form card at x=770.
// The notice's HEIGHT is modelled at 232, not measured — LAND-05-06's C1 reads the
// deployed one. Only the relationships below depend on it.
const NOTICE: Rect = { x: 24, y: 464, width: 460, height: 232 } // x 24..484, y 464..696
const CTA_HEADING: Rect = { x: 88, y: 300, width: 634, height: 96 } // x 88..722, y 300..396
const CTA_PARAGRAPH: Rect = { x: 88, y: 470, width: 440, height: 52 } // x 88..528, y 470..522
const CTA_FORM_CARD: Rect = { x: 770, y: 300, width: 422, height: 396 } // right column
const CTA_CONTENT_BOX: Rect = { x: 32, y: 96, width: 1216, height: 600 } // #demo's content box
const NOTICE_BUTTON_ROW: Rect = { x: 48, y: 616, width: 412, height: 40 } // inside the card
const ABUTS_NOTICE_RIGHT: Rect = { x: 484, y: 464, width: 300, height: 232 } // shares one edge
const ABUTS_NOTICE_TOP: Rect = { x: 88, y: 404, width: 440, height: 60 } // bottom lands on 464
const GRAZES_RIGHT_EDGE: Rect = { x: 482, y: 464, width: 420, height: 232 } // 2 x 232 overlap
const GRAZES_TOP_EDGE: Rect = { x: 252, y: 234, width: 232, height: 232 } // 232 x 2 overlap

describe('rectsOverlap', () => {
  it('is false when the notice and the CTA form card share a y band but not an x band (T5-1)', () => {
    expect(rectsOverlap(NOTICE, CTA_FORM_CARD)).toBe(false)
    // Non-vacuous: the two really do share 232px of y, so only the x axis rules it out.
    expect(overlapOf(NOTICE, CTA_FORM_CARD).height).toBe(232)
  })

  it('is false when the notice clears the CTA heading on y alone — the LAND-05 shape (T5-2)', () => {
    // The case a horizontal-only check misses. These two overlap on x by 396px at 1280
    // and at every wider width, so an x-only assertion is red on a page that is correct.
    expect(overlapOf(NOTICE, CTA_HEADING).width).toBe(396)
    expect(rectsOverlap(NOTICE, CTA_HEADING)).toBe(false)
  })

  it('treats a shared edge as clearance, on either axis (T5-3)', () => {
    expect(rectsOverlap(NOTICE, ABUTS_NOTICE_RIGHT)).toBe(false)
    expect(rectsOverlap(NOTICE, ABUTS_NOTICE_TOP)).toBe(false)
    // One pixel of real overlap, and the same pair reads as overlapping — the control
    // that stops this passing on an implementation that never returns true.
    expect(rectsOverlap(NOTICE, { ...ABUTS_NOTICE_RIGHT, x: 483 })).toBe(true)
    expect(rectsOverlap(NOTICE, { ...ABUTS_NOTICE_TOP, height: 61 })).toBe(true)
  })

  it('is symmetric over containment, both single-axis misses, and a shared edge (T5-4)', () => {
    const pairs: Array<[string, Rect, Rect]> = [
      ['notice x CTA paragraph (overlapping)', NOTICE, CTA_PARAGRAPH],
      ['notice x its own button row (containment)', NOTICE, NOTICE_BUTTON_ROW],
      ['notice x CTA heading (y-disjoint)', NOTICE, CTA_HEADING],
      ['notice x CTA form card (x-disjoint)', NOTICE, CTA_FORM_CARD],
      ['notice x its right-hand neighbour (edge)', NOTICE, ABUTS_NOTICE_RIGHT],
    ]

    expect(pairs.length).toBeGreaterThanOrEqual(5) // population floor
    const answers = pairs.map(([label, a, b]) => {
      const forward = rectsOverlap(a, b)
      expect(rectsOverlap(b, a), `${label} answers differently when the arguments swap`).toBe(forward)
      expect(overlapOf(b, a), `${label}'s intersection differs when the arguments swap`).toEqual(overlapOf(a, b))
      return forward
    })

    // Symmetry is satisfied by a function that always returns false; these two make the
    // table say something.
    expect(answers).toContain(true)
    expect(answers).toContain(false)
  })

  it('counts containment as overlap (T5-5)', () => {
    expect(rectsOverlap(NOTICE, NOTICE_BUTTON_ROW)).toBe(true)
  })

  it('catches the notice sitting on the closing CTA copy (T5-6)', () => {
    expect(rectsOverlap(NOTICE, CTA_PARAGRAPH)).toBe(true)
  })

  it('is false for a zero-width or zero-height rect sitting inside the notice', () => {
    // Both probes are well inside the notice on the other axis, so the collapsed extent
    // is the only thing that can decide the answer.
    const collapsedWidth: Rect = { x: 200, y: 500, width: 0, height: 100 }
    const collapsedHeight: Rect = { x: 200, y: 500, width: 100, height: 0 }

    expect(rectsOverlap(NOTICE, collapsedWidth)).toBe(false)
    expect(rectsOverlap(NOTICE, collapsedHeight)).toBe(false)
    // Restore one pixel on the collapsed axis and both probes overlap: the zero, not the
    // placement, is what made them false.
    expect(rectsOverlap(NOTICE, { ...collapsedWidth, width: 1 })).toBe(true)
    expect(rectsOverlap(NOTICE, { ...collapsedHeight, height: 1 })).toBe(true)
  })
})

describe('overlapOf', () => {
  it('returns the inner rect exactly when one rect contains the other (T5-5)', () => {
    expect(overlapOf(NOTICE, NOTICE_BUTTON_ROW)).toEqual(NOTICE_BUTTON_ROW)
  })

  it('returns the covered band of the closing CTA copy, both extents (T5-6)', () => {
    expect(overlapOf(NOTICE, CTA_PARAGRAPH)).toEqual({ x: 88, y: 470, width: 396, height: 52 })
  })

  it('collapses the touched axis to 0 and never reports a negative extent (T5-3)', () => {
    const onX = overlapOf(NOTICE, ABUTS_NOTICE_RIGHT)
    expect(onX.width).toBe(0)
    expect(onX.height).toBe(232)

    const onY = overlapOf(NOTICE, ABUTS_NOTICE_TOP)
    expect(onY.height).toBe(0)
    expect(onY.width).toBe(396)

    for (const disjoint of [CTA_HEADING, CTA_FORM_CARD]) {
      const o = overlapOf(NOTICE, disjoint)
      expect(o.width).toBeGreaterThanOrEqual(0)
      expect(o.height).toBeGreaterThanOrEqual(0)
    }
  })

  it('clamps per axis, so a single-axis miss still reports the other axis honestly', () => {
    // Why per axis: the failure message names WHICH axis saved the layout. Zeroing the
    // whole rect on a miss throws that away and the caller is back to one scalar.
    expect(overlapOf(NOTICE, CTA_HEADING).height).toBe(0)
    expect(overlapOf(NOTICE, CTA_HEADING).width).toBe(396)
    expect(overlapOf(NOTICE, CTA_FORM_CARD).width).toBe(0)
    expect(overlapOf(NOTICE, CTA_FORM_CARD).height).toBe(232)
  })

  it('tells a 2x232 graze from a 232x2 one, which an area cannot (T5-7)', () => {
    const side = overlapOf(NOTICE, GRAZES_RIGHT_EDGE)
    const top = overlapOf(NOTICE, GRAZES_TOP_EDGE)

    // Equal area, different defects: one is a sliver down the notice's right edge, the
    // other is the notice resting on 232px of copy. A scalar reports 464 for both.
    expect(side.width * side.height).toBe(top.width * top.height)
    expect(side).not.toEqual(top)

    expect(side).toEqual({ x: 482, y: 464, width: 2, height: 232 })
    expect(top).toEqual({ x: 252, y: 464, width: 232, height: 2 })
  })

  it('a Rect is a Box, so gaps() still typechecks and runs on the same rects', () => {
    // The structural requirement the contract states: Rect must satisfy Box, or every
    // existing caller of gaps() needs a conversion. It also shows why the 2D primitive
    // exists — gaps() reports the notice as 8px outside the CTA container on the left
    // and 764px clear on the right, and says nothing at all about whether they overlap.
    expect(gaps(NOTICE, CTA_CONTENT_BOX)).toEqual({ left: -8, right: 764 })
  })
})
