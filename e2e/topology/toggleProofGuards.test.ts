// BUG-10-04 local guard. The two deployed toggle proofs cannot run locally -- they need a
// deployed fleet -- and dev-env.yml runs no browser spec on a push to main
// ([dev-env-skips-e2e-on-push]), so the failure modes that ARE checkable without a
// deployment are guarded here as source text: a non-vacuity control moved BELOW the claim it
// protects, a deleted sweep-coverage check, a pinned raw pixel, or an absence claim widened
// from the filtered empty state to the whole page -- where Header.tsx's persistent create
// CTA makes it false, so it would go red on a build with no defect at all.
//
// soleIndex/testBody are re-declared here rather than imported from
// blockedRowProofGuards.test.ts: importing them would require exporting from a .test.ts
// file, which re-registers that file's own describes inside this one.
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const TOPOLOGY_DIR = dirname(fileURLToPath(import.meta.url))
const FILE = 'invoice-surfaces.spec.ts'
const SOURCE = readFileSync(join(TOPOLOGY_DIR, FILE), 'utf8')

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

// THE CONTROL. Every claim below is an indexOf into a string, and a string that lost its
// content would satisfy every negative assertion. If the spec is renamed, or its seams are
// replaced, this fails loudly instead of the whole guard passing vacuously.
describe('[bug-10-04] the guard is reading the file it names', () => {
  it('the spec still declares the four seams the two proofs are built on', () => {
    soleIndex(SOURCE, 'async function installRowLatch(page: Page)', FILE)
    soleIndex(SOURCE, 'async function readRowLatch(page: Page)', FILE)
    soleIndex(SOURCE, 'async function toggleNeedsAttention(page: Page, want: boolean)', FILE)
    soleIndex(SOURCE, 'async function sweepY(', FILE)
  })

  it('the sampler is an observer, never a timer', () => {
    // A fixed-period sampler straddles a flash shorter than its period.
    soleIndex(SOURCE, 'new MutationObserver(() => {\n      const now = read()', FILE)
    const install = SOURCE.slice(
      soleIndex(SOURCE, 'async function installRowLatch(page: Page)', FILE),
      soleIndex(SOURCE, 'async function readRowLatch(page: Page)', FILE),
    )
    expect(install).not.toMatch(/setInterval|setTimeout/)
  })
})

describe('[bug-10-04] the toggle proof keeps its controls above its claims', () => {
  const body = testBody(SOURCE, 'register geometry: toggling needs-attention moves nothing above the rows', FILE)

  const latchInstall = () => soleIndex(body, 'await installRowLatch(page)', FILE)
  const latchRead = () => soleIndex(body, 'await readRowLatch(page)', FILE)
  const narrows = () => soleIndex(body, 'toBeLessThan(unfilteredCount)', FILE)
  const span = () => soleIndex(body, 'toBeGreaterThan(lowWater)', FILE)
  const unfilteredSweep = () => soleIndex(body, "sweepY(page, head, firstRow, 'unfiltered')", FILE)
  const filteredSweep = () => soleIndex(body, "sweepY(page, head, firstRow, 'filtered')", FILE)
  const coverage = () => soleIndex(body, 'toEqual([...WIDE_WIDTHS])', FILE)

  it('the sampler is installed before the toggle it has to watch', () => {
    // The narrowing count is read AFTER the first toggle, so this ordering is a
    // unique-needle proof that the observer was in place before the click.
    expect(latchInstall(), 'a sampler installed after the toggle records only the settled state').toBeLessThan(narrows())
    expect(latchInstall()).toBeLessThan(latchRead())
  })

  it('the filter is proven to narrow the set before any geometry is measured', () => {
    expect(narrows(), 'without this control the two states may be the same table and every equality is free').toBeLessThan(unfilteredSweep())
    expect(narrows()).toBeLessThan(filteredSweep())
  })

  it('the sampler is proven to have spanned the transition before every claim resting on it', () => {
    expect(latchRead()).toBeLessThan(span())
    expect(span(), 'the never-empty claim rests on a sampler that saw the set change').toBeLessThan(soleIndex(body, 'expect(lowWater,', FILE))
    expect(span(), 'so does the unmount claim').toBeLessThan(soleIndex(body, '(s) => !s.list', FILE))
    expect(span(), 'and so does the spinner claim').toBeLessThan(soleIndex(body, '(s) => s.spinner', FILE))
  })

  it('both sweeps assert they actually measured every WIDE_WIDTHS entry', () => {
    // sweepY records a width only when its settle poll succeeded, so a sweep that measured
    // nothing returns [] and every per-width comparison below passes.
    expect(unfilteredSweep()).toBeLessThan(filteredSweep())
    expect(filteredSweep(), 'the coverage check must follow both sweeps it checks').toBeLessThan(coverage())
  })

  it('no raw pixel is pinned -- every claim is a relationship between the two states', () => {
    expect(body).not.toMatch(/toHaveCSS\(/)
    expect(body).not.toMatch(/\.toBe\(\s*\d/)
    expect(body).not.toMatch(/toBeCloseTo\(/)
  })

  it('the position is measured from the viewport, not from inside the list container', () => {
    // .pf-list-head is invoices-list's FIRST child, so an in-container offset is 0 in both
    // states and the assertion would be vacuous. boundingBox() is viewport-relative; an
    // evaluate() reading offsetTop against the container is not.
    expect(body, 'an in-container offset is 0 in both filter states -- the defect moved the container itself').not.toMatch(/offsetTop|offsetParent/)
  })
})

describe('[bug-10-04] the filtered empty-state proof keeps its controls above its claim', () => {
  const body = testBody(SOURCE, 'register empty state: a filter that matches nothing says so', FILE)

  const populated = () => soleIndex(body, 'the register must really hold invoices', FILE)
  const notYetFiltered = () => soleIndex(body, 'must not be on screen before the filter is on', FILE)
  const toggleOn = () => soleIndex(body, 'await toggleNeedsAttention(page, true)', FILE)
  const filteredCopy = () => soleIndex(body, "toContainText('Nothing needs attention')", FILE)
  const wayBack = () => soleIndex(body, "getByTestId('clear-needs-attention').click()", FILE)

  it('the register is proven populated before the filtered empty state is claimed', () => {
    expect(populated(), 'on a genuinely empty register this branch renders for the wrong reason and the claim is vacuous').toBeLessThan(toggleOn())
    expect(notYetFiltered(), 'the negative control must be read while the filter is still off').toBeLessThan(toggleOn())
    expect(toggleOn()).toBeLessThan(filteredCopy())
  })

  it('the way back is exercised after the empty state is asserted, never before', () => {
    expect(filteredCopy()).toBeLessThan(wayBack())
  })

  it('the New invoice absence claim is scoped to the filtered empty state, never the page', () => {
    // Header.tsx:136 renders a PERSISTENT create CTA on every screen. A page-wide absence
    // claim is FALSE against correct product behaviour -- it would fail on a build with no
    // defect at all, which is a worse outcome than not asserting it.
    soleIndex(body, "expect(emptyFiltered).not.toContainText('New invoice')", FILE)
    expect(body, 'the page-wide form of this claim is false, not merely strict').not.toMatch(/expect\(page\)[^\n]{0,60}not\.toContainText\(\s*'New invoice'/)
  })
})
