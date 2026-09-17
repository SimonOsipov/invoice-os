// Unit tests for layout.ts. The browser-facing helpers run here against a fake page;
// invoice-surfaces.spec.ts exercises them on the deployed app.
import type { Locator, Page } from '@playwright/test'
import { describe, expect, it } from 'vitest'

import * as layout from './layout'
import { assertFillsColumn, assertSameHeight, gaps, settleAnimations, WIDE_WIDTHS, type Box } from './layout'

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

// --- the 2D overlap primitive -------------------------------------------------
//
// `gaps` answers a one-axis question. A fixed card and content in normal flow can clear
// each other on either axis alone, so a one-axis check calls a correct page broken, and
// an area scalar cannot tell a 2px graze from a 232px one — the same "a cap and its
// placement are two facts" defect this file already carries for BUG-03-05.
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

// A geometry catalogue, NOT a snapshot of any shipped screen. These rects began as the
// landing's bottom-left cookie notice and closing CTA; the notice was right-anchored on
// 2026-08-18 and the numbers no longer describe it. They are kept because each one still
// exercises one branch of the helpers below — containment, a single-axis miss on either
// axis, a shared edge, a graze — and pinning them to a live layout only bought a fixture
// that rots silently while staying green.
//
// The landing's real clearance is proven where it can go red: landing-consent.spec.ts C2
// sweeps the closing CTA against the deployed card, and C4 does the footer's controls.
const CARD: Rect = { x: 24, y: 464, width: 460, height: 232 } // x 24..484, y 464..696
const Y_DISJOINT: Rect = { x: 88, y: 300, width: 634, height: 96 } // shares x, clears on y
const OVERLAPS_CARD: Rect = { x: 88, y: 470, width: 440, height: 52 } // covers both axes
const X_DISJOINT: Rect = { x: 770, y: 300, width: 422, height: 396 } // shares y, clears on x
const CONTAINER: Rect = { x: 32, y: 96, width: 1216, height: 600 } // encloses neither
const INSIDE_CARD: Rect = { x: 48, y: 616, width: 412, height: 40 } // wholly within CARD
const ABUTS_CARD_RIGHT: Rect = { x: 484, y: 464, width: 300, height: 232 } // shares one edge
const ABUTS_CARD_TOP: Rect = { x: 88, y: 404, width: 440, height: 60 } // bottom lands on 464
const GRAZES_CARD_RIGHT: Rect = { x: 482, y: 464, width: 420, height: 232 } // 2 x 232 overlap
const GRAZES_CARD_TOP: Rect = { x: 252, y: 234, width: 232, height: 232 } // 232 x 2 overlap

describe('rectsOverlap', () => {
  it('is false when two rects share a y band but not an x band (T5-1)', () => {
    expect(rectsOverlap(CARD, X_DISJOINT)).toBe(false)
    // Non-vacuous: the two really do share 232px of y, so only the x axis rules it out.
    expect(overlapOf(CARD, X_DISJOINT).height).toBe(232)
  })

  it('is false when one rect clears the other on y alone (T5-2)', () => {
    // The case a horizontal-only check misses: these two overlap on x by 396px, so an
    // x-only assertion calls a correct layout broken.
    expect(overlapOf(CARD, Y_DISJOINT).width).toBe(396)
    expect(rectsOverlap(CARD, Y_DISJOINT)).toBe(false)
  })

  it('treats a shared edge as clearance, on either axis (T5-3)', () => {
    expect(rectsOverlap(CARD, ABUTS_CARD_RIGHT)).toBe(false)
    expect(rectsOverlap(CARD, ABUTS_CARD_TOP)).toBe(false)
    // One pixel of real overlap, and the same pair reads as overlapping — the control
    // that stops this passing on an implementation that never returns true.
    expect(rectsOverlap(CARD, { ...ABUTS_CARD_RIGHT, x: 483 })).toBe(true)
    expect(rectsOverlap(CARD, { ...ABUTS_CARD_TOP, height: 61 })).toBe(true)
  })

  it('is symmetric over containment, both single-axis misses, and a shared edge (T5-4)', () => {
    const pairs: Array<[string, Rect, Rect]> = [
      ['card x an overlapping block', CARD, OVERLAPS_CARD],
      ['card x a block inside it (containment)', CARD, INSIDE_CARD],
      ['card x a y-disjoint block', CARD, Y_DISJOINT],
      ['card x an x-disjoint block', CARD, X_DISJOINT],
      ['card x its right-hand neighbour (edge)', CARD, ABUTS_CARD_RIGHT],
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
    expect(rectsOverlap(CARD, INSIDE_CARD)).toBe(true)
  })

  it('catches a rect sitting on another on both axes (T5-6)', () => {
    expect(rectsOverlap(CARD, OVERLAPS_CARD)).toBe(true)
  })

  it('is false for a zero-width or zero-height rect sitting inside the card', () => {
    // Both probes are well inside the card on the other axis, so the collapsed extent
    // is the only thing that can decide the answer.
    const collapsedWidth: Rect = { x: 200, y: 500, width: 0, height: 100 }
    const collapsedHeight: Rect = { x: 200, y: 500, width: 100, height: 0 }

    expect(rectsOverlap(CARD, collapsedWidth)).toBe(false)
    expect(rectsOverlap(CARD, collapsedHeight)).toBe(false)
    // Restore one pixel on the collapsed axis and both probes overlap: the zero, not the
    // placement, is what made them false.
    expect(rectsOverlap(CARD, { ...collapsedWidth, width: 1 })).toBe(true)
    expect(rectsOverlap(CARD, { ...collapsedHeight, height: 1 })).toBe(true)
  })
})

describe('overlapOf', () => {
  it('returns the inner rect exactly when one rect contains the other (T5-5)', () => {
    expect(overlapOf(CARD, INSIDE_CARD)).toEqual(INSIDE_CARD)
  })

  it('returns the covered band of an overlapping rect, both extents (T5-6)', () => {
    expect(overlapOf(CARD, OVERLAPS_CARD)).toEqual({ x: 88, y: 470, width: 396, height: 52 })
  })

  it('collapses the touched axis to 0 and never reports a negative extent (T5-3)', () => {
    const onX = overlapOf(CARD, ABUTS_CARD_RIGHT)
    expect(onX.width).toBe(0)
    expect(onX.height).toBe(232)

    const onY = overlapOf(CARD, ABUTS_CARD_TOP)
    expect(onY.height).toBe(0)
    expect(onY.width).toBe(396)

    for (const disjoint of [Y_DISJOINT, X_DISJOINT]) {
      const o = overlapOf(CARD, disjoint)
      expect(o.width).toBeGreaterThanOrEqual(0)
      expect(o.height).toBeGreaterThanOrEqual(0)
    }
  })

  it('clamps per axis, so a single-axis miss still reports the other axis honestly', () => {
    // Why per axis: the failure message names WHICH axis saved the layout. Zeroing the
    // whole rect on a miss throws that away and the caller is back to one scalar.
    expect(overlapOf(CARD, Y_DISJOINT).height).toBe(0)
    expect(overlapOf(CARD, Y_DISJOINT).width).toBe(396)
    expect(overlapOf(CARD, X_DISJOINT).width).toBe(0)
    expect(overlapOf(CARD, X_DISJOINT).height).toBe(232)
  })

  it('tells a 2x232 graze from a 232x2 one, which an area cannot (T5-7)', () => {
    const side = overlapOf(CARD, GRAZES_CARD_RIGHT)
    const top = overlapOf(CARD, GRAZES_CARD_TOP)

    // Equal area, different defects: one is a sliver down the card's right edge, the
    // other is the card resting on 232px of content. A scalar reports 464 for both.
    expect(side.width * side.height).toBe(top.width * top.height)
    expect(side).not.toEqual(top)

    expect(side).toEqual({ x: 482, y: 464, width: 2, height: 232 })
    expect(top).toEqual({ x: 252, y: 464, width: 232, height: 2 })
  })

  it('a Rect is a Box, so gaps() still typechecks and runs on the same rects', () => {
    // The structural requirement the contract states: Rect must satisfy Box, or every
    // existing caller of gaps() needs a conversion. It also shows why the 2D primitive
    // exists — gaps() reports the card as 8px outside the container on the left and
    // 764px clear on the right, and says nothing at all about whether they overlap.
    expect(gaps(CARD, CONTAINER)).toEqual({ left: -8, right: 764 })
  })
})

// --- settling animations before a read -----------------------------------------
//
// A fake page: every resize starts a slide on the measured elements' shared ancestor.
// `settled()` is false until it finishes, and each box reads its mid-slide geometry
// until then. The mid-slide values sit inside each helper's slack, so expect.poll
// accepts them and only a settle before the read returns the layout's numbers.
const SLIDE_MS = 150
const never = new Promise<void>(() => {})

function slidingPage() {
  let viewport = { width: 1280, height: 1080 }
  let sliding = false
  let slide: Promise<void> = Promise.resolve()
  const startSlide = () => {
    sliding = true
    slide = new Promise((resolve) => setTimeout(() => ((sliding = false), resolve()), SLIDE_MS))
  }

  type FakeEl = { ownerDocument: unknown; contains: (n: unknown) => boolean }
  const elements: FakeEl[] = []
  const ancestor = { contains: (n: unknown) => n === ancestor || elements.includes(n as FakeEl) }
  const unrelated = { contains: (n: unknown) => n === unrelated }
  const animation = (target: unknown, endTime: number, finished: () => Promise<void>) => ({
    effect: { target, getComputedTiming: () => ({ endTime }) },
    get finished() {
      return finished()
    },
  })
  const doc = {
    getAnimations: () => [
      animation(ancestor, SLIDE_MS, () => slide),
      animation(ancestor, Infinity, () => never), // a spinner: waiting on it hangs
      animation(unrelated, SLIDE_MS, () => never), // not an ancestor: waiting on it hangs
    ],
  }

  const locator = (box: (sliding: boolean) => { x: number; y: number; width: number; height: number }) => {
    const el: FakeEl = { ownerDocument: doc, contains: (n) => n === el }
    elements.push(el)
    return {
      evaluate: (fn: (el: unknown) => unknown) => Promise.resolve(fn(el)),
      boundingBox: async () => box(sliding),
    } as unknown as Locator
  }

  const page = {
    viewportSize: () => viewport,
    setViewportSize: async (v: { width: number; height: number }) => {
      viewport = v
      startSlide()
    },
  } as unknown as Page

  return { page, locator, startSlide, settled: () => !sliding }
}

describe('settleAnimations', () => {
  it('waits for an ancestor animation, skipping infinite and unrelated ones', async () => {
    const fake = slidingPage()
    const el = fake.locator(() => ({ x: 0, y: 0, width: 10, height: 10 }))
    fake.startSlide()
    expect(fake.settled()).toBe(false)

    await settleAnimations(el)
    expect(fake.settled(), 'settleAnimations returned before the ancestor slide finished').toBe(true)
  })
})

describe('the sweeps measure settled geometry', () => {
  it('assertFillsColumn reports the settled gaps, not the mid-slide ones', async () => {
    const fake = slidingPage()
    const outer = fake.locator(() => ({ x: 0, y: 0, width: 1000, height: 500 }))
    // Mid-slide the inner box is 12px right of its place: inside the 24px slack.
    const inner = fake.locator((sliding) => ({ x: sliding ? 12 : 0, y: 0, width: 1000, height: 500 }))

    const fits = await assertFillsColumn(fake.page, inner, outer, 'fake')
    expect(fits).toHaveLength(WIDE_WIDTHS.length)
    for (const fit of fits) expect({ left: fit.left, right: fit.right }).toEqual({ left: 0, right: 0 })
  })

  it('assertSameHeight reports the settled heights, not the mid-slide ones', async () => {
    const fake = slidingPage()
    const b = fake.locator(() => ({ x: 0, y: 0, width: 100, height: 100 }))
    // Mid-slide `a` stands 0.6px short: rounds inside the 1px tolerance.
    const a = fake.locator((sliding) => ({ x: 0, y: 0, width: 100, height: sliding ? 99.4 : 100 }))

    const pairs = await assertSameHeight(fake.page, a, b, 'fake')
    expect(pairs).toHaveLength(WIDE_WIDTHS.length)
    for (const pair of pairs) expect(pair.delta).toBe(0)
  })
})
