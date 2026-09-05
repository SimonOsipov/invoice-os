// BUG-14-04 (task-901) local guard. The action cluster's geometry cannot run locally -- it
// needs a deployed fleet -- and a push to main runs zero browser specs, so the failure modes
// that ARE checkable without a deployment are guarded here as source text: a non-vacuity
// control moved below the geometry it protects, a deleted sweep-coverage check, a raw pixel
// pinned in place of a relationship, or an absence needle authored in the spec instead of
// read off the wire. Any of those leaves every geometry claim passing on nothing.
//
// soleIndex and testBody are RE-DECLARED, never imported from blockedRowProofGuards.test.ts:
// importing from a `.test.ts` re-registers that file's describes.
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

/**
 * The body of the test whose title contains `title`, up to the next test at the same depth.
 *
 * BUG-09's twin scanned to the next top-level `\ntest(`; the geometry block lives INSIDE a
 * test.describe.serial, so its sibling tests are indented and that bound would run to the
 * end of the file and break every uniqueness claim below. `indent` is the sibling's own.
 */
function testBody(source: string, title: string, file: string, indent = '  '): string {
  const start = soleIndex(source, title, file)
  const next = source.indexOf(`\n${indent}test(`, start)
  const body = source.slice(start, next === -1 ? source.length : next)
  expect(
    body.length,
    `${file}: the block from ${JSON.stringify(title)} is near-empty -- a drifted title scanned nothing`,
  ).toBeGreaterThan(1_000)
  return body
}

describe('[bug-14-04] the action-cluster geometry proof keeps its controls above its claims', () => {
  const file = 'invoice-surfaces.spec.ts'
  const source = readFileSync(join(TOPOLOGY_DIR, file), 'utf8')
  const body = testBody(source, 'R1-R6: the same six controls, in three right-aligned rows', file)

  const resolves = () => soleIndex(body, 'toHaveCount(1)', file)
  const enabled = () => soleIndex(body, '.toBeEnabled()', file)
  const disabled = () => soleIndex(body, '.toBeDisabled()', file)
  const firstBox = () => soleIndex(body, 'const boxes = await Promise.all(', file)
  const sweep = () => soleIndex(body, 'for (const width of WIDE_WIDTHS)', file)
  const coverage = () => soleIndex(body, 'toEqual([...WIDE_WIDTHS])', file)
  const wireRead = () => soleIndex(body, 'await getInvoice(', file)
  const textAbsence = () => soleIndex(body, 'not.toContainText(reason)', file)

  it('every control is proven to resolve before any geometry is measured', () => {
    expect(resolves(), 'a cluster that lost a control makes the band count agree by accident').toBeLessThan(firstBox())
  })

  it('the pair is proven to be one editable and one non-editable invoice before any geometry is measured', () => {
    expect(enabled(), 'without this the fixture may be two identical statuses and R1 proves nothing').toBeLessThan(firstBox())
    expect(disabled(), 'without this the fixture may be two identical statuses and R1 proves nothing').toBeLessThan(firstBox())
  })

  it('the sweep asserts it actually measured every WIDE_WIDTHS entry, at both statuses', () => {
    expect(sweep(), 'the coverage check must follow the sweep it checks').toBeLessThan(coverage())
  })

  it('the absence needle is read off the wire before the claim that uses it', () => {
    expect(wireRead(), 'the wire read must precede the absence claim it feeds').toBeLessThan(textAbsence())
  })

  it('no absence needle is authored in the spec', () => {
    // A string literal here would be a hardcoded copy of backend copy, which is what moving
    // the needle to getInvoice() exists to stop -- every not.toContainText takes a variable.
    expect(body).not.toMatch(/not\.toContainText\(\s*['"`]/)
  })

  it('no raw pixel height or width is pinned', () => {
    expect(body).not.toMatch(/toHaveCSS\(\s*['"](height|width)['"]/)
    expect(body).not.toMatch(/\.(height|width)\s*\)\s*\.toBe\(/)
  })
})

describe('[bug-14-04] the role-axis claim keeps its controls above its wire read', () => {
  // The geometry block above only ever holds the admin seat; AC-3's role axis lives on the
  // persona switcher's own journey. Guarded here so the split costs no coverage.
  const file = 'demo-persona.spec.ts'
  const source = readFileSync(join(TOPOLOGY_DIR, file), 'utf8')
  const body = testBody(source, 'as a preparer, the server refuses the same approval it allowed the seat to see', file, '')

  const resolves = soleIndex(body, 'must still resolve for a preparer', file)
  const disabled = soleIndex(body, 'must be disabled for a preparer', file)
  const wireRead = soleIndex(body, 'const preparerWire = await getInvoice(', file)
  // The absence claim's own argument, not its call text: a prettier reflow moves the
  // linebreak but never the identifier.
  const textAbsence = soleIndex(body, 'preparerWire.approve_blocked_reason!,', file)

  it('all six controls are counted before the role-gated pair is claimed disabled', () => {
    expect(resolves, 'the set claim must precede the state claim, or a shorter cluster passes on its two survivors').toBeLessThan(
      disabled,
    )
  })

  it("the preparer's refusal is read off HER token before the absence claim that uses it", () => {
    expect(wireRead, "the seat's own reason is a different sentence -- the wire read must be the preparer's").toBeLessThan(
      textAbsence,
    )
  })

  it('the role claim names the whole control set, not just Approve', () => {
    // Scoped to the declaration's own text. Scanning `source` instead passed with
    // detail-submit dropped from the array, because the file names that testid three more
    // times in assertions of its own.
    const open = soleIndex(source, 'const CLUSTER_CONTROLS = [', file)
    const close = source.indexOf(']', open)
    expect(close, `${file}: the CLUSTER_CONTROLS declaration never closes`).toBeGreaterThan(open)
    const declaration = source.slice(open, close)
    for (const testid of ['view-ubl', 'detail-approve', 'detail-reject', 'edit-toggle', 'revalidate', 'detail-submit']) {
      expect(declaration, `${testid} must be in the role-axis control set`).toContain(`'${testid}'`)
    }
  })
})
