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
// .claude/rules/e2e.md bans: nothing here compares pixels or keeps a baseline.
// They read geometry the same way an assertion reads text.
import { expect, test, type Locator, type Page } from '@playwright/test'

/**
 * Waits until web fonts have loaded and every finite animation on each element and its ancestors has finished.
 * A rect read mid-animation is the animation's geometry, and an ancestor's slide moves its children.
 * A rect read before fonts load is the fallback font's geometry.
 */
export async function settleAnimations(...targets: Locator[]): Promise<void> {
  for (const target of targets) {
    await target.evaluate(async (el) => {
      await el.ownerDocument.fonts.ready
      const running = el.ownerDocument.getAnimations().filter((a) => {
        const on = (a.effect as KeyframeEffect | null)?.target
        // An infinite loop (spinner, shimmer) never finishes.
        return on?.contains(el) && a.effect?.getComputedTiming().endTime !== Infinity
      })
      return Promise.all(running.map((a) => a.finished.catch(() => undefined))).then(() => undefined)
    })
  }
}

/** Widest first — see the file header for why the order is load-bearing. */
export const WIDE_WIDTHS = [2560, 1920, 1440, 1280] as const

/** Phone widths; 320 is the narrowest phone in use, 767 is the library's PHONE_MAX_WIDTH. */
export const PHONE_WIDTHS = [320, 375, 390, 414, 767] as const

/** The horizontal extent of a rendered element. Playwright's boundingBox() shape. */
export type Box = { x: number; width: number }

/** What one width contributed to a fit sweep. Attach it; the numbers outlive the run. */
export type ColumnFit = { width: number; left: number; right: number; outerWidth: number }

/** What one width contributed to a height sweep. Attach it; the numbers outlive the run. */
export type HeightPair = { width: number; a: number; b: number; delta: number }

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
 * Does `outer` contain `inner` on both axes? Containment is the two-axis form of
 * gaps(): slackPx absorbs sub-pixel rounding on each edge, not a real overhang.
 */
export function enclosesRect(outer: Rect, inner: Rect, slackPx = 0): boolean {
  return (
    inner.x >= outer.x - slackPx &&
    inner.y >= outer.y - slackPx &&
    inner.x + inner.width <= outer.x + outer.width + slackPx &&
    inner.y + inner.height <= outer.y + outer.height + slackPx
  )
}

/** `r` grown by `px` on every side. */
export function inflateRect(r: Rect, px: number): Rect {
  return { x: r.x - px, y: r.y - px, width: r.width + 2 * px, height: r.height + 2 * px }
}

/**
 * Asserts `inner` fills `outer` horizontally at every width in WIDE_WIDTHS, and
 * returns what it measured so the caller can attach the numbers.
 *
 * `slackPx` absorbs a scrollbar gutter (up to ~10px) and sub-pixel rounding. It
 * is not a dimension bound in disguise: the defect this exists to catch strands
 * hundreds of pixels, so the passing and failing cases are nowhere near it.
 *
 * Each read settles animations first, and `expect.poll` waits out a re-render
 * triggered by the resize. The entry viewport is restored afterwards.
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
        await settleAnimations(inner, outer)
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

/**
 * Asserts `a` and `b` stand the same height at every width in WIDE_WIDTHS, and
 * returns what it measured so the caller can attach the numbers.
 *
 * Each read settles animations first, and `expect.poll` waits out a re-render.
 * A null or zero-height box fails the matcher rather than passing as a match.
 */
export async function assertSameHeight(
  page: Page,
  a: Locator,
  b: Locator,
  label: string,
  tolerancePx = 1,
): Promise<HeightPair[]> {
  const entry = page.viewportSize()
  const measured: HeightPair[] = []

  try {
    for (const width of WIDE_WIDTHS) {
      await page.setViewportSize({ width, height: 1080 })

      const read = async (): Promise<HeightPair | null> => {
        await settleAnimations(a, b)
        const [aBox, bBox] = await Promise.all([a.boundingBox(), b.boundingBox()])
        if (!aBox || !bBox) return null
        if (aBox.height <= 0 || bBox.height <= 0) return null
        return { width, a: aBox.height, b: bBox.height, delta: Math.abs(aBox.height - bBox.height) }
      }

      await expect
        .poll(async () => {
          const pair = await read()
          return pair === null ? null : Math.round(pair.delta)
        }, {
          message: `${label} must stand the same height at ${width}px wide (a null here means one of the two never rendered, or rendered zero-height)`,
          timeout: 10_000,
        })
        .toBeLessThanOrEqual(tolerancePx)

      const pair = await read()
      if (pair) measured.push(pair)
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }

  return measured
}

/** Asserts the app shell's scroller, `.pf-scroll`, has a box and does not scroll sideways. */
export async function assertPageDoesNotScrollSideways(page: Page, label: string): Promise<{ scrollWidth: number; clientWidth: number }> {
  const reading = await page
    .locator('main.pf-main .pf-scroll')
    .evaluate(async (el) => {
      await el.ownerDocument.fonts.ready
      return { scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }
    })
  expect(reading.clientWidth, `${label}: the page column (.pf-scroll) has no box`).toBeGreaterThan(0)
  expect(
    reading.scrollWidth - reading.clientWidth,
    `${label}: the page column scrolls sideways (scrollWidth ${reading.scrollWidth} vs clientWidth ${reading.clientWidth})`,
  ).toBeLessThanOrEqual(1)
  return reading
}

/** What one width contributed to a narrow fit sweep. */
export type NarrowFit = { width: number; scrollWidth: number; clientWidth: number }

/**
 * Asserts `container` does not scroll sideways (scrollWidth <= clientWidth) at every width
 * in `widths`, and returns what it measured. The entry viewport is restored afterwards.
 */
export async function assertFitsNarrow(
  page: Page,
  container: Locator,
  label: string,
  widths: readonly number[] = PHONE_WIDTHS,
): Promise<NarrowFit[]> {
  const entry = page.viewportSize()
  const measured: NarrowFit[] = []

  try {
    for (const width of widths) {
      await page.setViewportSize({ width, height: 844 })

      const read = async (): Promise<NarrowFit | null> => {
        await settleAnimations(container)
        const r = await container.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth }))
        return r.clientWidth > 0 ? { width, ...r } : null
      }

      await expect
        .poll(async () => {
          const fit = await read()
          return fit === null ? null : fit.scrollWidth - fit.clientWidth
        }, {
          message: `${label} must not scroll sideways at ${width}px wide (a null here means the container has no box)`,
          timeout: 10_000,
        })
        .toBeLessThanOrEqual(0)

      const fit = await read()
      if (fit) measured.push(fit)
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }

  return measured
}

// Settled boxes of `control` and every locator it must clear; a missing box reads as null.
async function controlBoxes(control: Locator, container: Locator, others: Locator[]): Promise<{ c: Rect; box: Rect; others: Rect[] } | null> {
  await settleAnimations(control, container, ...others)
  const [c, box, ...rest] = await Promise.all([control.boundingBox(), container.boundingBox(), ...others.map((o) => o.boundingBox())])
  if (!c || !box || rest.some((r) => !r)) return null
  return { c, box, others: rest as Rect[] }
}

// At every width: `control` sits inside `container`, overlaps none of `others`, and (wide only) shares `centreWith`'s vertical centre.
export async function assertControlFits(
  page: Page,
  control: Locator,
  container: Locator,
  others: Locator[],
  centreWith: Locator | null,
  label: string,
): Promise<void> {
  const entry = page.viewportSize()
  try {
    for (const width of [...WIDE_WIDTHS, ...PHONE_WIDTHS]) {
      await page.setViewportSize({ width, height: 900 })
      const wide = (WIDE_WIDTHS as readonly number[]).includes(width)
      await expect
        .poll(
          async () => {
            const m = await controlBoxes(control, container, centreWith && wide ? [...others, centreWith] : others)
            if (!m) return 'an element has no box'
            if (!enclosesRect(m.box, m.c, 1)) return 'not enclosed by its container'
            if (m.others.slice(0, others.length).some((o) => rectsOverlap(m.c, o))) return 'overlaps a sibling'
            if (centreWith && wide) {
              const ref = m.others[others.length]
              if (Math.abs(m.c.y + m.c.height / 2 - (ref.y + ref.height / 2)) > 1) return 'centre differs from its sibling'
            }
            return 'ok'
          },
          { message: `${label} at ${width}px wide`, timeout: 10_000 },
        )
        .toBe('ok')
      if (wide) {
        await assertPageDoesNotScrollSideways(page, `${label} at ${width}px wide`)
      } else {
        await assertControlAddsNoOverflow(page, control, `${label} at ${width}px wide`, width)
      }
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }
}

/** Overflow the page column carries without the control, and what the control adds to it. */
export function controlOverflow(shown: number, hidden: number): { hiddenOverflow: number; addedByControl: number } {
  return { hiddenOverflow: hidden, addedByControl: shown - hidden }
}

// Phone: the page may already overflow for another reason; the control must add at most 1px. The overflow with the control hidden is attached.
export async function assertControlAddsNoOverflow(page: Page, control: Locator, label: string, width: number): Promise<void> {
  const column = page.locator('main.pf-main .pf-scroll')
  const read = () =>
    column.evaluate(async (el) => {
      await el.ownerDocument.fonts.ready
      return el.scrollWidth - el.clientWidth
    })
  const shown = await read()
  let hidden: number
  await control.evaluate((el) => {
    el.dataset.prevDisplay = el.style.display
    el.style.display = 'none'
  })
  try {
    hidden = await read()
  } finally {
    await control.evaluate((el) => {
      el.style.display = el.dataset.prevDisplay ?? ''
      delete el.dataset.prevDisplay
    })
  }
  const { hiddenOverflow, addedByControl } = controlOverflow(shown, hidden)
  await test.info().attach(`control-overflow-${width}.json`, {
    body: JSON.stringify({ label, width, shownOverflow: shown, hiddenOverflow, addedByControl }, null, 2),
    contentType: 'application/json',
  })
  expect(addedByControl, `${label}: showing the control widens the page column by ${addedByControl}px (shown ${shown}, hidden ${hiddenOverflow})`).toBeLessThanOrEqual(1)
}

export type FilterBarRects = { column: Rect; header: Rect; action: Rect; bar: Rect; filters: Rect[]; list: Rect }

// One message per broken relationship, 1px slack on every edge comparison.
export function filterBarProblems(r: FilterBarRects): string[] {
  const slack = 1
  const out: string[] = []
  if (r.filters.length === 0) out.push('no filters were measured')
  if (!enclosesRect(r.column, r.bar, slack)) out.push('the bar is not inside the column')
  if (r.filters.some((f) => !enclosesRect(r.bar, f, slack))) out.push('a filter is not inside the bar')
  if (Math.abs(r.bar.x - r.list.x) > slack) out.push("the bar's left edge is off the list's left edge")
  if (r.filters.length > 0 && Math.abs(r.filters[0].x - r.bar.x) > slack) out.push("the first filter's left edge is off the bar's left edge")
  if (r.bar.y < r.header.y + r.header.height - slack) out.push("the bar's top is above the header's bottom")
  if (r.bar.y + r.bar.height > r.list.y + slack) out.push("the bar's bottom is below the list's top")
  if (rectsOverlap(r.bar, r.header)) out.push('the bar overlaps the header')
  if (rectsOverlap(r.bar, r.list)) out.push('the bar overlaps the list')
  if (!enclosesRect(r.header, r.action, slack)) out.push('the action is not inside the header')
  if (rectsOverlap(r.action, r.bar)) out.push('the action overlaps the bar')
  if (r.filters.some((f) => rectsOverlap(f, r.header))) out.push('a filter overlaps the header')
  return out
}

type FilterBarParts = { column: Locator; header: Locator; action: Locator; bar: Locator; filters: Locator[]; list: Locator }

async function readFilterBar(p: FilterBarParts): Promise<FilterBarRects | null> {
  await settleAnimations(p.column, p.header, p.action, p.bar, p.list, ...p.filters)
  const [column, header, action, bar, list, ...filters] = await Promise.all([
    p.column.boundingBox(),
    p.header.boundingBox(),
    p.action.boundingBox(),
    p.bar.boundingBox(),
    p.list.boundingBox(),
    ...p.filters.map((f) => f.boundingBox()),
  ])
  if (!column || !header || !action || !bar || !list || filters.some((f) => !f)) return null
  return { column, header, action, bar, list, filters: filters as Rect[] }
}

// At every width: the bar sits between header and list, holds every filter from its left edge, and adds <=1px of sideways overflow.
export async function assertFilterBarPlacement(
  page: Page,
  parts: FilterBarParts,
  label: string,
): Promise<Array<{ width: number; rects: FilterBarRects }>> {
  const entry = page.viewportSize()
  const readings: Array<{ width: number; rects: FilterBarRects }> = []
  try {
    for (const width of [...WIDE_WIDTHS, ...PHONE_WIDTHS]) {
      await page.setViewportSize({ width, height: 900 })
      await expect
        .poll(
          async () => {
            const rects = await readFilterBar(parts)
            return rects ? filterBarProblems(rects) : ['an element has no box']
          },
          { message: `${label} at ${width}px wide`, timeout: 10_000 },
        )
        .toEqual([])
      await assertControlAddsNoOverflow(page, parts.bar, `${label} at ${width}px wide`, width)
      const rects = await readFilterBar(parts)
      if (rects) readings.push({ width, rects })
    }
  } finally {
    if (entry) await page.setViewportSize(entry)
  }
  return readings
}
