// A workspace package can ship a test script that no CI job ever runs, and the
// result looks exactly like a package with no tests: nothing red, nothing to
// notice. `frontend/support-console` did it — 50 tests in 3 files, and grepping
// ci.yml for "support-console" returned nothing until 2026-08-13.
//
// internal/tools/rlsgate/ci_registration_test.go asks this same question of the
// Go half. It walks Go packages only, so the pnpm workspace had no equivalent.
//
// This asserts an ABSENCE (no unregistered package), so per RALPH Stage 4 it
// carries both defences against reporting a clean zero because it broke: a
// CONTROL NEEDLE (@invoice-os/app must be found, with a test script, registered)
// and a FLOOR on the population it walked.
import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync, existsSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO = dirname(dirname(fileURLToPath(import.meta.url)))
const CI_YML = join(REPO, '.github/workflows/ci.yml')

type Pkg = { name: string; dir: string; testScripts: string[] }

/** Expand pnpm-workspace.yaml's `packages:` list. Only `dir/*` and literals are used here. */
function workspaceDirs(): string[] {
  const yaml = readFileSync(join(REPO, 'pnpm-workspace.yaml'), 'utf8')
  const globs = [...yaml.matchAll(/^\s*-\s*["']?([^"'\n]+?)["']?\s*$/gm)].map((m) => m[1])
  const dirs: string[] = []
  for (const glob of globs) {
    if (glob.endsWith('/*')) {
      const parent = join(REPO, glob.slice(0, -2))
      if (!existsSync(parent)) continue
      for (const entry of readdirSync(parent, { withFileTypes: true })) {
        if (entry.isDirectory()) dirs.push(join(parent, entry.name))
      }
    } else {
      dirs.push(join(REPO, glob))
    }
  }
  return dirs
}

function packages(): Pkg[] {
  const found: Pkg[] = []
  for (const dir of workspaceDirs()) {
    const manifest = join(dir, 'package.json')
    if (!existsSync(manifest)) continue
    const pkg = JSON.parse(readFileSync(manifest, 'utf8'))
    const scripts: Record<string, string> = pkg.scripts ?? {}
    const testScripts = Object.keys(scripts).filter((s) => s === 'test' || s.startsWith('test:'))
    if (pkg.name) found.push({ name: pkg.name, dir, testScripts })
  }
  return found
}

/** Every `pnpm --filter <name> <script>` invocation ci.yml makes, as "name script". */
function ciFilterInvocations(): Set<string> {
  const yml = readFileSync(CI_YML, 'utf8')
  const calls = new Set<string>()
  for (const m of yml.matchAll(/pnpm\s+--filter\s+["']?(@?[\w./@-]+)["']?\s+([\w:]+)/g)) {
    calls.add(`${m[1]} ${m[2]}`)
  }
  return calls
}

describe('every workspace package with tests is run by a CI job', () => {
  const all = packages()
  const withTests = all.filter((p) => p.testScripts.length > 0)
  const ciCalls = ciFilterInvocations()

  it('walked a plausible workspace (floor — a broken walk must not read as clean)', () => {
    expect(all.length, 'found no workspace packages at all — the walk is broken').toBeGreaterThanOrEqual(5)
    expect(withTests.length, 'no package appears to have a test script — the manifest read is broken').toBeGreaterThanOrEqual(5)
    expect(ciCalls.size, 'parsed no `pnpm --filter` steps out of ci.yml — the ci.yml scan is broken').toBeGreaterThanOrEqual(5)
  })

  it('found its control needle: @invoice-os/app, with tests, registered in ci.yml', () => {
    const app = withTests.find((p) => p.name === '@invoice-os/app')
    expect(app, 'control needle @invoice-os/app not found with a test script').toBeDefined()
    expect(ciCalls.has('@invoice-os/app test'), 'control needle is not registered in ci.yml').toBe(true)
  })

  it('leaves no package whose tests nothing runs', () => {
    const unregistered = withTests
      .filter((p) => !p.testScripts.some((s) => ciCalls.has(`${p.name} ${s}`)))
      .map((p) => p.name)
    expect(
      unregistered,
      `these packages have test scripts that no ci.yml step runs — add a step, ` +
        `or the suite is silently dead: ${unregistered.join(', ')}`,
    ).toEqual([])
  })
})
