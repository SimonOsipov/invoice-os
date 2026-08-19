// The load-bearing bundle oracle: reads the BUILT assets, not source, so a demo import
// escaping src/demo/ or the DEMO_MODE guard being removed would show up here. Path
// resolves from import.meta.url, never process.cwd() -- CI runs vitest from the package
// dir, a developer may run from the repo root (e2e/personas.test.ts:11-12).
import { describe, expect, it } from 'vitest'
import { existsSync, readdirSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const ASSETS_DIR = join(dirname(fileURLToPath(import.meta.url)), '../../dist/assets')

const MISSING_DIST_MESSAGE =
  'frontend/app/dist/assets holds no .js file — this test reads the built bundle and cannot pass ' +
  'without one. Run `pnpm --filter @invoice-os/app build` first; CI builds it at ' +
  '.github/workflows/ci.yml:537, one step before the app suite at :541.'

// One guard, called by every test below, so a missing/empty dist/assets fails EVERY
// assertion with the named remedy instead of skipping or reporting a silent clean pass.
function readBundle(): string {
  if (!existsSync(ASSETS_DIR)) throw new Error(MISSING_DIST_MESSAGE)
  const jsFiles = readdirSync(ASSETS_DIR).filter((f) => f.endsWith('.js'))
  if (jsFiles.length === 0) throw new Error(MISSING_DIST_MESSAGE)
  return jsFiles.map((f) => readFileSync(join(ASSETS_DIR, f), 'utf8')).join('\n')
}

// String literals (unlike identifiers) survive minification -- the bundle carries 100
// literal U+00B7 middle dots today, so a sentinel built from one is findable.
// GUARD: green on write (the toast doesn't exist yet, so the string is absent either
// way) -- fences the `DEMO_MODE &&` gate on App's new toast mount once it ships.
const ABSENT_SENTINELS = [
  'DEMO ONLY · BECOME ANOTHER MEMBER',
  'DEMO BUILD',
  'Return to the signed-in seat',
  'persona-trigger',
  'You are now {full name}',
  // task-594, DEMO-06-06: the invoice-detail note naming the blocked preparer.
  'Switch to a Reviewer to act on this step.',
]

describe('the built bundle carries no demo string', () => {
  // Control needle: if this fails, the read itself is wrong (empty/mis-resolved file),
  // and a clean-looking absence below would be the read failing, not the guard passing.
  it('contains the control needle "Sign out"', () => {
    expect(readBundle()).toContain('Sign out')
  })

  it.each(ABSENT_SENTINELS)('does not contain the sentinel %j', (sentinel) => {
    expect(readBundle()).not.toContain(sentinel)
  })

  // Documentation only -- carries no signal. Component identifiers do not survive
  // minification: Sidebar/InvoiceDetail/MembersTable/ApprovalsView/WorkflowBuilder/
  // SourceDocumentModal each occur 0 times in the built bundle despite all shipping, so
  // this assertion can never go red. The four string-literal sentinels above are the
  // actual proof; kept here only so a future reader does not mistake its absence for one.
  it('PersonaFooter (documentation only, cannot go red -- see ABSENT_SENTINELS for real coverage)', () => {
    expect(readBundle()).not.toContain('PersonaFooter')
  })
})
