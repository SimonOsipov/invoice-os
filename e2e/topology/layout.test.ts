// Unit tests for layout.ts's pure half. assertFillsColumn needs a browser and a
// deployed app, so it is exercised by invoice-surfaces.spec.ts in the topology
// suite; `gaps` is where the arithmetic that BUG-03-05 got wrong actually lives.
import { describe, expect, it } from 'vitest'

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
