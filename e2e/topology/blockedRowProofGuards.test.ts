// BUG-09-03 (task-860) local guard. The two blocked-row geometry proofs cannot run
// locally -- they need a deployed fleet -- so the one failure mode that IS checkable
// without a deployment is guarded here as source text: an edit that moves a non-vacuity
// control BELOW the geometry it protects, or deletes the sweep-coverage check, leaves
// every geometry claim passing on a fixture that drifted into two identical rows.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const TOPOLOGY_DIR = dirname(fileURLToPath(import.meta.url))

/** The one index of `needle` in `source`, or a failure naming which uniqueness rule broke. */
function soleIndex(source: string, needle: string, where: string): number {
  const first = source.indexOf(needle)
  expect(first, `${where}: needle absent -- ${JSON.stringify(needle)}`).toBeGreaterThanOrEqual(0)
  expect(
    source.indexOf(needle, first + 1),
    `${where}: needle appears more than once, so an ordering claim about it names no single site -- ${JSON.stringify(needle)}`,
  ).toBe(-1)
  return first
}

/** The body of the test whose title contains `title`, up to the next top-level `test(`. */
function testBody(source: string, title: string, file: string): string {
  const start = soleIndex(source, title, file)
  const next = source.indexOf('\ntest(', start)
  const body = source.slice(start, next === -1 ? source.length : next)
  expect(body.length, `${file}: the block from ${JSON.stringify(title)} is near-empty -- a drifted title scanned nothing`).toBeGreaterThan(1_000)
  return body
}

describe('[bug-09-03] the register geometry proof keeps its controls above its claims', () => {
  const file = 'invoice-surfaces.spec.ts'
  const source = readFileSync(join(TOPOLOGY_DIR, file), 'utf8')
  const body = testBody(source, 'register geometry: a blocked row costs no extra line', file)

  const disabled = () => soleIndex(body, 'await expect(blockedBox).toBeDisabled()', file)
  const titleRead = () => soleIndex(body, "blockedBox.getAttribute('title')", file)
  const enabled = () => soleIndex(body, '.toBeEnabled()', file)
  const gridChildren = () => soleIndex(body, "head.locator('> *')", file)
  const sameHeight = () => soleIndex(body, 'assertSameHeight(page,', file)
  const textAbsence = () => soleIndex(body, 'not.toContainText(title)', file)

  it('the blocked checkbox is proven disabled before any geometry is measured', () => {
    expect(disabled(), 'the disabled control must precede the grid-child comparison').toBeLessThan(gridChildren())
    expect(disabled()).toBeLessThan(sameHeight())
  })

  it('the clean checkbox is proven enabled before any geometry is measured', () => {
    expect(enabled(), 'without this control the pair may be two blocked rows and every claim is vacuous').toBeLessThan(gridChildren())
    expect(enabled()).toBeLessThan(sameHeight())
  })

  it("the reason needle is read out of the DOM before it is used, never authored", () => {
    expect(titleRead(), 'the title must be read before the absence claim that uses it').toBeLessThan(textAbsence())
  })

  it('the height sweep asserts it actually measured every WIDE_WIDTHS entry', () => {
    // assertSameHeight only records a width whose post-poll re-read succeeded, so a helper
    // that swept nothing returns [] and every height claim passes. This is that guard.
    soleIndex(body, 'toEqual([...WIDE_WIDTHS])', file)
    expect(sameHeight(), 'the coverage check must follow the sweep it checks').toBeLessThan(soleIndex(body, 'toEqual([...WIDE_WIDTHS])', file))
  })

  it('no raw pixel height or width is pinned', () => {
    expect(body).not.toMatch(/toHaveCSS\(\s*'height'/)
    expect(body).not.toMatch(/\.height\s*\)\s*\.toBe\(/)
  })
})

describe('[bug-09-03] the review geometry proof keeps its control above its claims', () => {
  const file = 'import-wizard.spec.ts'
  const source = readFileSync(join(TOPOLOGY_DIR, file), 'utf8')
  const body = testBody(source, 'INVCR-E2E-7 kept-as-is drops out of Needs a fix', file)

  const disabled = soleIndex(body, 'a kept row stays non-selectable', file)
  const titleRead = soleIndex(body, "violateBox.getAttribute('title')", file)
  const gridChildren = soleIndex(body, "reviewHead.locator('> *')", file)
  const textAbsence = soleIndex(body, 'not.toContainText(reviewTitle)', file)
  const selectAll = soleIndex(body, "getByTestId('review-select-all').click()", file)

  it('the kept row is proven disabled before its grid children are compared', () => {
    expect(disabled, 'the disabled control is this block\'s only non-vacuity guard').toBeLessThan(gridChildren)
  })

  it('the reason needle is read out of the DOM before it is used, never authored', () => {
    expect(titleRead).toBeLessThan(textAbsence)
  })

  it('both claims land before select-all mutates the table', () => {
    expect(gridChildren, 'select-all re-renders the rows -- a claim after it measures a different table').toBeLessThan(selectAll)
    expect(textAbsence).toBeLessThan(selectAll)
  })
})
