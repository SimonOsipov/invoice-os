import { describe, expect, it } from 'vitest'
import { enclosesRect, type Rect } from './layout'

// enclosesRect's only callers are deploy-only Playwright assertions, so an inverted
// slack would first show up as a burnt gate cycle. These pin the direction: slack
// grows the OUTER box, so a sub-pixel overhang passes and a real one still fails.

const OUTER: Rect = { x: 10, y: 10, width: 100, height: 50 }

const rect = (x: number, y: number, width: number, height: number): Rect => ({ x, y, width, height })

describe('enclosesRect', () => {
  it('accepts an inner rect strictly inside, and one flush with every edge', () => {
    expect(enclosesRect(OUTER, rect(20, 20, 10, 10))).toBe(true)
    expect(enclosesRect(OUTER, OUTER)).toBe(true)
  })

  it('rejects an overhang on each edge independently, with the default zero slack', () => {
    // One clause each: every other clause holds in the rect below it names.
    expect(enclosesRect(OUTER, rect(9, 20, 10, 10)), 'left edge').toBe(false)
    expect(enclosesRect(OUTER, rect(20, 9, 10, 10)), 'top edge').toBe(false)
    expect(enclosesRect(OUTER, rect(105, 20, 10, 10)), 'right edge').toBe(false)
    expect(enclosesRect(OUTER, rect(20, 55, 10, 10)), 'bottom edge').toBe(false)
  })

  it('applies slack outward, so a 1px overhang on any edge is absorbed', () => {
    expect(enclosesRect(OUTER, rect(9, 20, 10, 10), 1), 'left edge').toBe(true)
    expect(enclosesRect(OUTER, rect(20, 9, 10, 10), 1), 'top edge').toBe(true)
    expect(enclosesRect(OUTER, rect(101, 20, 10, 10), 1), 'right edge').toBe(true)
    expect(enclosesRect(OUTER, rect(20, 51, 10, 10), 1), 'bottom edge').toBe(true)
  })

  it('does not let slack swallow a real overhang', () => {
    expect(enclosesRect(OUTER, rect(7, 20, 10, 10), 1), 'left edge').toBe(false)
    expect(enclosesRect(OUTER, rect(20, 7, 10, 10), 1), 'top edge').toBe(false)
    expect(enclosesRect(OUTER, rect(105, 20, 10, 10), 1), 'right edge').toBe(false)
    expect(enclosesRect(OUTER, rect(20, 55, 10, 10), 1), 'bottom edge').toBe(false)
  })

  it('is not symmetric: the first argument is the container', () => {
    const inner = rect(20, 20, 10, 10)
    expect(enclosesRect(OUTER, inner)).toBe(true)
    expect(enclosesRect(inner, OUTER)).toBe(false)
  })
})
