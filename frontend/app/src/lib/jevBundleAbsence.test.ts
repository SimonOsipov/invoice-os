// jevBundleAbsence.test.ts (CHECK-01-07 AC-10): the jev measurement harness must never ride
// into a shipped build or a shipped source file. First describe reads the BUILT bundle,
// following src/demo/bundleAbsence.test.ts's shape. Second content-scans src/ itself.
import { describe, expect, it } from 'vitest'
import { existsSync, readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const ASSETS_DIR = join(dirname(fileURLToPath(import.meta.url)), '../../dist/assets')

const MISSING_DIST_MESSAGE =
  'frontend/app/dist/assets holds no .js file — this test reads the built bundle and cannot pass ' +
  'without one. Run `pnpm --filter @invoice-os/app build` first; CI builds it at the Build step, ' +
  'one step before the app suite runs.'

// One guard, called by every test below, so a missing/empty dist/assets fails EVERY
// assertion with the named remedy instead of skipping or reporting a silent clean pass.
function readBundle(): string {
  if (!existsSync(ASSETS_DIR)) throw new Error(MISSING_DIST_MESSAGE)
  const jsFiles = readdirSync(ASSETS_DIR).filter((f) => f.endsWith('.js'))
  if (jsFiles.length === 0) throw new Error(MISSING_DIST_MESSAGE)
  return jsFiles.map((f) => readFileSync(join(ASSETS_DIR, f), 'utf8')).join('\n')
}

// Absent unless the harness rides into a shipped component -- an identifier's string
// literals survive minification even though the identifier itself does not.
const ABSENT_SENTINELS = ['JEV_OUT', 'TYPESAFE_API_KEY', 'api.typesafe.ai', 'jevmeasure', 'autoPlacements']

describe('the built bundle carries no jev harness string', () => {
  // Control needle: if this fails, the read itself is wrong, and a clean-looking absence
  // below would be the read failing, not the guard passing.
  it('contains the control needle "Sign out"', () => {
    expect(readBundle()).toContain('Sign out')
  })

  it.each(ABSENT_SENTINELS)('does not contain the sentinel %j', (sentinel) => {
    expect(readBundle()).not.toContain(sentinel)
  })
})

// Row 14: content-based, not filename-based -- a harness helper named util.ts would defeat a
// filename filter; a shipped mapping.ts growing an autoPlacements export is what this must catch.
const SRC_ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const HARNESS_NEEDLE = /jev|typesafe|autoplacement/i

function walkSrc(): Array<{ path: string; content: string }> {
  const entries = readdirSync(SRC_ROOT, { recursive: true, withFileTypes: true })
  return entries
    .filter((e) => e.isFile())
    .map((e) => {
      const full = join(e.parentPath, e.name)
      return { path: full, content: readFileSync(full, 'utf8') }
    })
}

describe('frontend/app/src holds no non-test jev harness file', () => {
  it('every harness-needle match is a .test.ts file, and at least one match exists', () => {
    const files = walkSrc()
    // Floor: measured today 283 (not the plan's 282 -- every file under src, not just .ts/.tsx).
    expect(files.length, 'the src scan read too few files -- looks truncated').toBeGreaterThan(200)

    const matches = files.filter((f) => HARNESS_NEEDLE.test(f.content))
    expect(matches.length, 'no file matched the harness needle -- a broken walk reads exactly this way').toBeGreaterThan(0)

    const nonTest = matches.filter((f) => !f.path.endsWith('.test.ts')).map((f) => f.path)
    expect(nonTest, 'a non-test file carries a jev/typesafe/autoplacement string').toEqual([])
  })
})
