import { mkdtempSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, describe, expect, it } from 'vitest'
import FlakyReporter, {
  collectFlaky,
  formatFlakySummary,
  REPO_ROOT,
  shortenPath,
  type TestLike,
} from './flakyReporter'

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
    ], '/repo')
    expect(got).toEqual([
      {
        file: '/repo/e2e/topology/invoice-surfaces.spec.ts',
        line: 1099,
        title: 'a failed invoice is an honest dead end',
        attempts: 2,
        repoFile: 'e2e/topology/invoice-surfaces.spec.ts',
        titlePath: ['a failed invoice is an honest dead end'],
        error: '',
      },
    ])
  })

  it('returns nothing for a clean run', () => {
    expect(collectFlaky([test('expected', 'fine', 1, 1)])).toEqual([])
  })

  it('does not throw when a test carries no location or results', () => {
    const bare: TestLike = { outcome: () => 'flaky', titlePath: () => ['x'] }
    expect(collectFlaky([bare], '/repo')).toEqual([
      {
        file: '<unknown>',
        line: 0,
        title: 'x',
        attempts: 0,
        repoFile: '<unknown>',
        titlePath: ['x'],
        error: '',
      },
    ])
  })

  const flakyAt = (
    titlePath: string[],
    results: { error?: { message?: string } }[] | undefined,
    file = '/repo/e2e/topology/x.spec.ts',
  ): TestLike => ({
    outcome: () => 'flaky',
    titlePath: () => titlePath,
    location: { file, line: 58 },
    results,
  })
  const head = ['', 'serial-lane', 'x.spec.ts']

  it('writes the repo-relative file, the title path and the first-attempt error', () => {
    const t = flakyAt([...head, 'session', 'signs out'], [
      { error: { message: '\u001b[31mError: boom\u001b[39m\n  at y' } },
      {},
    ])
    expect(collectFlaky([t], '/repo')).toEqual([
      {
        file: '/repo/e2e/topology/x.spec.ts',
        line: 58,
        title: 'signs out',
        attempts: 2,
        repoFile: 'e2e/topology/x.spec.ts',
        titlePath: ['session', 'signs out'],
        error: 'Error: boom',
      },
    ])
  })

  it('same last title under two describes gives two title paths', () => {
    const got = collectFlaky(
      [flakyAt([...head, 'A', 'saves'], [{}, {}]), flakyAt([...head, 'B', 'saves'], [{}, {}])],
      '/repo',
    )
    expect(got.map((r) => r.titlePath)).toEqual([['A', 'saves'], ['B', 'saves']])
    expect(got[0].title).toBe(got[1].title)
  })

  it('first error line skips blank lines and is cut to 300 characters', () => {
    const message = '\n\n' + 'x'.repeat(400) + '\nmore'
    const [row] = collectFlaky([flakyAt(['t'], [{ error: { message } }, {}])], '/repo')
    expect(row.error).toBe('x'.repeat(300))
  })

  it('no first-attempt error gives an empty error', () => {
    expect(collectFlaky([flakyAt(['t'], [{}, {}])], '/repo')[0].error).toBe('')
  })

  it('a blank or ANSI-only first-attempt message gives an empty error', () => {
    for (const message of ['  \n\t', '\u001b[31m\u001b[39m']) {
      expect(collectFlaky([flakyAt(['t'], [{ error: { message } }, {}])], '/repo')[0].error).toBe('')
    }
  })

  it('empty results give attempts 0 and an empty error', () => {
    const [row] = collectFlaky([flakyAt(['t'], [])], '/repo')
    expect(row.attempts).toBe(0)
    expect(row.error).toBe('')
  })

  it('CRLF line breaks leave no carriage return', () => {
    const message = 'Error: boom\r\n  at y'
    expect(collectFlaky([flakyAt(['t'], [{ error: { message } }, {}])], '/repo')[0].error).toBe(
      'Error: boom',
    )
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

describe('FlakyReporter', () => {
  const saved = {
    FLAKY_JSON_DIR: process.env.FLAKY_JSON_DIR,
    GITHUB_STEP_SUMMARY: process.env.GITHUB_STEP_SUMMARY,
  }
  let dir = ''
  afterEach(() => {
    for (const [k, v] of Object.entries(saved)) {
      if (v === undefined) delete process.env[k]
      else process.env[k] = v
    }
    if (dir) rmSync(dir, { recursive: true, force: true })
  })

  it('onEnd writes the new fields to <label>.json', () => {
    dir = mkdtempSync(join(tmpdir(), 'flaky-reporter-'))
    process.env.FLAKY_JSON_DIR = dir
    delete process.env.GITHUB_STEP_SUMMARY
    const t: TestLike = {
      outcome: () => 'flaky',
      titlePath: () => ['', 'serial-lane', 'x.spec.ts', 'session', 'signs out'],
      location: { file: join(REPO_ROOT, 'e2e/topology/x.spec.ts'), line: 58 },
      results: [{ error: { message: 'Error: boom\n  at y' } }, {}],
    }
    const reporter = new FlakyReporter({ label: 'topology' })
    reporter.onBegin({} as never, { allTests: () => [t] } as never)
    reporter.onEnd()
    const rows = JSON.parse(readFileSync(join(dir, 'topology.json'), 'utf8'))
    expect(rows).toHaveLength(1)
    expect(rows[0]).toMatchObject({
      repoFile: 'e2e/topology/x.spec.ts',
      titlePath: ['session', 'signs out'],
      error: 'Error: boom',
    })
  })
})
