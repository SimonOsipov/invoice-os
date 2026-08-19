// layout.ts — geometry helpers for layout assertions, per RALPH_PROMPT.md
// Phase 3.5 step 4 ("Assert the relationship, not the dimension").
//
// A width assertion passes on the very bug it should catch. BUG-03-05 capped the
// invoice detail at 1080px inside a much wider column and pinned it left; the
// check written to guard it, `width <= 1082`, was SATISFIED by the 588px of dead
// space that shipped to production, and the user found it rather than CI. A cap
// and its placement are two facts and that assertion measured one of them.
//
// MEASURE WIDEST FIRST. Every other sweep in e2e/ runs 1280 and narrower
// (landing-demo.spec.ts's ASSERTED_WIDTHS), and 1280 is Playwright's default
// viewport, so before this file the whole suite held exactly one assertion above
// 1280. The wide end is where an unguarded cap has room to strand a band, and it
// is where the one shipped instance of this defect actually lived.
//
// These helpers are deliberately NOT visual regression, which
// docs/e2e-convention.md bans: nothing here compares pixels or keeps a baseline.
// They read geometry the same way an assertion reads text.
import { expect, type Locator, type Page } from '@playwright/test'

/** Widest first — see the file header for why the order is load-bearing. */
export const WIDE_WIDTHS = [2560, 1920, 1440, 1280] as const

/** The horizontal extent of a rendered element. Playwright's boundingBox() shape. */
export type Box = { x: number; width: number }

/** What one width contributed to a fit sweep. Attach it; the numbers outlive the run. */
export type ColumnFit = { width: number; left: number; right: number; outerWidth: number }

/**
 * The unused strip on each side of `inner` within `outer`, in CSS pixels.
 *
 * Both sides, never just one. A right-only measurement reads a LEFT-pinned cap
 * as a large right gap (BUG-03-05's actual shape) but reads a RIGHT-pinned one
 * as a perfect fit, and a centred cap as half the truth on each side.
 */
export function gaps(inner: Box, outer: Box): { left: number; right: number } {
  return {
    left: inner.x - outer.x,
    right: outer.x + outer.width - (inner.x + inner.width),
  }
}

/** A rendered element's full extent. Playwright's boundingBox() shape; satisfies Box. */
export type Rect = { x: number; y: number; width: number; height: number }

/**
 * Do two elements cover any of the same pixels?
 *
 * Both axes, never one — gaps()'s rule in two dimensions. A fixed card and the content
 * it scrolls over can clear each other on x alone or on y alone, so a single-axis check
 * calls a correct page broken. A shared edge, and an element collapsed to zero on either
 * axis, are clearance rather than contact.
 */
export function rectsOverlap(a: Rect, b: Rect): boolean {
  const o = overlapOf(a, b)
  return o.width > 0 && o.height > 0
}

/**
 * The intersection of two elements, clamped per axis so an extent is never negative.
 *
 * A rect, not an area: 2x232 and 232x2 are both 464 and are different defects — a sliver
 * down one edge versus a card sitting on a paragraph. Clamping per axis instead of
 * zeroing the whole rect on a miss keeps the axis that saved the layout readable in the
 * failure message. The caller multiplies if it wants the area.
 */
export function overlapOf(a: Rect, b: Rect): Rect {
  const x = Math.max(a.x, b.x)
  const y = Math.max(a.y, b.y)
  return {
    x,
    y,
    width: Math.max(0, Math.min(a.x + a.width, b.x + b.width) - x),
    height: Math.max(0, Math.min(a.y + a.height, b.y + b.height) - y),
  }
}

/**
 * Asserts `inner` fills `outer` horizontally at every width in WIDE_WIDTHS, and
 * returns what it measured so the caller can attach the numbers.
 *
 * `slackPx` absorbs a scrollbar gutter (up to ~10px) and sub-pixel rounding. It
 * is not a dimension bound in disguise: the defect this exists to catch strands
 * hundreds of pixels, so the passing and failing cases are nowhere near it.
 *
 * The measurement is taken through `expect.poll`, so a React re-render triggered
 * by the resize is waited out rather than raced. The entry viewport is restored
 * afterwards, so a caller's later assertions see the size they were written for.
 */
export async function assertFillsColumn(
  page: Page,
  inner: Locator,
  outer: Locator,
  label: string,
  slackPx = 24,
): Promise<ColumnFit[]> {
  const entry = page.viewportSize()
  const measured: ColumnFit[] = []

  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const read = async (): Promise<ColumnFit | null> => {
        const [innerBox, outerBox] = await Promise.all([inner.boundingBox(), outer.boundingBox()])
        if (!innerBox || !outerBox) return null
        const g = gaps(innerBox, outerBox)
        return { width, left: g.left, right: g.right, outerWidth: outerBox.width }
      }

      await expect
        .poll(async () => {
          const fit = await read()
          return fit === null ? null : Math.round(Math.max(fit.left, fit.right))
        }, {
          message: `${label} must fill its column at ${width}px wide (a null here means one of the two elements never rendered)`,
          timeout: 10_000,
        })
        .toBeLessThanOrEqual(slackPx)

      const fit = await read()
      if (fit) measured.push(fit)
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }

  return measured
}
