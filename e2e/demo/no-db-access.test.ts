// M4-22-06 (task-165, Test Specs #2 + #3): the vitest-runnable half of "the demo
// suite holds no DB plumbing" -- mirrors e2e/api/no-db-access.test.ts (M4-22-05,
// task-163). RED right now: db.ts still imports pg and reads the superuser DSN env
// var directly (see its own file header) -- goes GREEN once the executor deletes
// db.ts in Stage 3. Also guards the parked AC-7's shape: day30.spec.ts must invoke
// test.skip with a literal `true`, never a conditional expression (M4-22-06
// [park-shape] / [loud-park]) -- the mechanical anti-disappearance guarantee, since
// Playwright's list reporter never prints the skip() reason string, only a test's
// own console output and title (M4-22-06 Stage 1+2 validation, Correction A).
import { describe, expect, it } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const DEMO_DIR = dirname(fileURLToPath(import.meta.url))
const SELF = fileURLToPath(import.meta.url)
const DAY30_SPEC = join(DEMO_DIR, 'day30.spec.ts')

// Recursively lists every file under DEMO_DIR, excluding this scanner's own file --
// otherwise the DSN-name check below would flag its own source for naming the very
// env var it exists to detect.
function listFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) {
      out.push(...listFiles(full))
    } else if (entry.isFile() && full !== SELF) {
      out.push(full)
    }
  }
  return out
}

const DSN_ENV_VAR = 'DATABASE_SUPERUSER_URL_DEV'
const PG_IMPORT = /from\s+['"]pg['"]|require\(\s*['"]pg['"]\s*\)/

describe('demo suite holds no DB plumbing', () => {
  const files = listFiles(DEMO_DIR)

  it('has files to scan', () => {
    // Guards against a future refactor emptying the directory and the checks
    // below passing vacuously.
    expect(files.length).toBeGreaterThan(0)
  })

  it('imports no pg', () => {
    const offenders = files.filter((f) => PG_IMPORT.test(readFileSync(f, 'utf8')))
    expect(offenders, `pg imported in: ${offenders.join(', ')}`).toEqual([])
  })

  it(`references no ${DSN_ENV_VAR}`, () => {
    const offenders = files.filter((f) => readFileSync(f, 'utf8').includes(DSN_ENV_VAR))
    expect(offenders, `${DSN_ENV_VAR} referenced in: ${offenders.join(', ')}`).toEqual([])
  })

  it('has no db.ts file', () => {
    const offenders = files.filter((f) => f.endsWith('/db.ts'))
    expect(offenders, `db.ts still present: ${offenders.join(', ')}`).toEqual([])
  })
})

// Strips comments before the two checks below match against source text. Without
// this, a comment that merely QUOTES the guarded call -- e.g. day30.spec.ts's own
// explanatory header, which contains the literal substring `test.skip(true,` inside
// a `//` comment describing what the real call does -- satisfies a presence assertion
// (toContain) exactly as well as the real call would, making the anti-disappearance
// guard decorative (QA Stage 4, 2026-07-19: mutation-confirmed -- deleting the real
// call entirely still left this test green). Handles block comments and line
// comments; a `//` immediately preceded by `:` is treated as part of a URL (e.g.
// `https://`), not a comment start -- the one realistic false-positive class a TS
// source file in this repo could contain.
function stripComments(src: string): string {
  const noBlockComments = src.replace(/\/\*[\s\S]*?\*\//g, '')
  return noBlockComments
    .split('\n')
    .map((line) => line.replace(/(^|[^:])\/\/.*$/, '$1'))
    .join('\n')
}

describe('parked AC-7 is unconditional', () => {
  const source = stripComments(readFileSync(DAY30_SPEC, 'utf8'))

  it('day30.spec.ts contains a literal test.skip(true, ...) call -- the anti-disappearance guard for Core AC 3', () => {
    expect(source).toContain('test.skip(true,')
  })

  it('never gates the parked skip on a conditional expression', () => {
    // A conditionally-gated skip once read as green (M4-04-08); this rules out the
    // common conditional shapes directly, rather than relying solely on the
    // literal-true substring check above.
    expect(source).not.toMatch(/test\.skip\(\s*(dbEnabled\(\)|process\.env)/)
  })
})
