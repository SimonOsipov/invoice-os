import { describe, expect, it } from 'vitest'
import { collectFlaky, formatFlakySummary, shortenPath, type TestLike } from './flakyReporter'

/**
 * The fixtures below are the three outcomes Playwright 1.61 actually produces,
 * taken from a real run rather than invented: a probe spec that fails on attempt
 * 0 and passes on attempt 1 reports `status: flaky` with two results; one that
 * always passes reports `expected` with one; one that always fails reports
 * `unexpected` with two. Only the first reports GREEN, which is why it is the one
 * worth surfacing.
 */
const test = (outcome: string, title: string, line: number, results: number): TestLike => ({
  outcome: () => outcome,
  titlePath: () => ['', 'chromium', 'probe.spec.ts', title],
  location: { file: '/repo/e2e/topology/invoice-surfaces.spec.ts', line },
  results: Array.from({ length: results }, () => ({})),
})

describe('collectFlaky', () => {
  it('selects only the retry-pass, which is the case that reports green', () => {
    const got = collectFlaky([
      test('flaky', 'a failed invoice is an honest dead end', 1099, 2),
      test('expected', 'always passes', 10, 1),
      test('unexpected', 'always fails', 20, 2),
      test('skipped', 'skipped', 30, 0),
    ])
    expect(got).toEqual([
      {
        file: '/repo/e2e/topology/invoice-surfaces.spec.ts',
        line: 1099,
        title: 'a failed invoice is an honest dead end',
        attempts: 2,
      },
    ])
  })

  it('returns nothing for a clean run', () => {
    expect(collectFlaky([test('expected', 'fine', 1, 1)])).toEqual([])
  })

  it('does not throw when a test carries no location or results', () => {
    const bare: TestLike = { outcome: () => 'flaky', titlePath: () => ['x'] }
    expect(collectFlaky([bare])).toEqual([
      { file: '<unknown>', line: 0, title: 'x', attempts: 0 },
    ])
  })

  it('orders by file then line so the table is stable across runs', () => {
    const at = (file: string, line: number): TestLike => ({
      outcome: () => 'flaky',
      titlePath: () => ['t'],
      location: { file, line },
      results: [{}, {}],
    })
    const got = collectFlaky([at('b.spec.ts', 5), at('a.spec.ts', 90), at('a.spec.ts', 9)])
    expect(got.map((f) => `${f.file}:${f.line}`)).toEqual([
      'a.spec.ts:9',
      'a.spec.ts:90',
      'b.spec.ts:5',
    ])
  })
})

describe('formatFlakySummary', () => {
  it('is empty for a clean run, so a green summary stays uncluttered', () => {
    expect(formatFlakySummary('smoke', [])).toBe('')
  })

  it('names the spec, its line and its attempt count', () => {
    const md = formatFlakySummary('topology', [
      {
        file: '/repo/e2e/topology/invoice-surfaces.spec.ts',
        line: 35,
        title: 'invoice detail round-trips the live engine',
        attempts: 2,
      },
    ])
    expect(md).toContain('topology: 1 flaky spec')
    expect(md).toContain('`topology/invoice-surfaces.spec.ts:35`')
    expect(md).toContain('invoice detail round-trips the live engine')
    // The count must be present: "passed on retry" without it hides how bad it is.
    expect(md).toMatch(/\|\s*2\s*\|/)
  })

  it('pluralises so the line does not read as a single flake', () => {
    const two = formatFlakySummary('smoke', [
      { file: 'a.spec.ts', line: 1, title: 'x', attempts: 2 },
      { file: 'b.spec.ts', line: 2, title: 'y', attempts: 2 },
    ])
    expect(two).toContain('2 flaky specs')
  })
})

describe('shortenPath', () => {
  it('trims an absolute runner path to the e2e-relative one', () => {
    expect(shortenPath('/home/runner/work/invoice-os/invoice-os/e2e/smoke/landing-nav.spec.ts'))
      .toBe('smoke/landing-nav.spec.ts')
  })

  it('leaves an already-relative path alone', () => {
    expect(shortenPath('topology/roles.spec.ts')).toBe('topology/roles.spec.ts')
  })
})
