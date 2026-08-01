// e2e/envCopy.test.ts — guards against posture-dishonest copy reappearing on any deployed SPA
// (DEMO-01-09, Backlog task-326). Lives outside frontend/ (unlike a per-SPA vitest test) so
// the guard cannot self-match its own forbidden strings, per e2e/personas.test.ts:72-80's
// precedent of reading source across all four SPAs via REPO_ROOT (import.meta.url, never
// process.cwd() — CI invokes vitest via `pnpm --filter`, cwd differs from a dev's shell).
import { describe, expect, it } from 'vitest'
import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { FORBIDDEN_STRINGS } from './envCopyStrings'

const REPO_ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')

const SPAS = ['app', 'landing', 'ops-console', 'support-console'] as const

// The complete exception: a test file necessarily quotes the strings it guards. Nothing else
// is excepted — comments and frontend/landing are both in scope (AC-6/AC-7).
const TEST_FILE_RE = /\.test\.tsx?$/

type Hit = { file: string; line: number; needle: string }

// Enumerates tracked frontend/**/*.{ts,tsx,html} via git ls-files, never a recursive walk:
// git excludes untracked build output for free (frontend/**/dist is gitignored), so a stale
// bundle can't produce phantom hits. Throws on failure — a guard that silently scans nothing
// on a broken pathspec is worse than no guard.
function listFrontendSourceFiles(): string[] {
  let out: string
  try {
    out = execFileSync('git', ['ls-files', '--', 'frontend'], { cwd: REPO_ROOT, encoding: 'utf8' })
  } catch (err) {
    throw new Error(`envCopy guard: \`git ls-files -- frontend\` failed: ${(err as Error).message}`)
  }
  return out
    .split('\n')
    .filter((line) => line.length > 0)
    .filter((relPath) => /\.(ts|tsx|html)$/.test(relPath))
}

function partition(paths: string[]): { scanned: string[]; excluded: string[] } {
  const scanned: string[] = []
  const excluded: string[] = []
  for (const p of paths) {
    ;(TEST_FILE_RE.test(p) ? excluded : scanned).push(p)
  }
  return { scanned, excluded }
}

// The matcher itself, parameterized on source text + a label (not inlined into the scan loop)
// so row "the matcher is not vacuous" below can exercise the SAME function on an inline
// fixture, not a copy of its logic. Case-insensitive by construction — the live defect is
// `sub="Sent to NRS"` (capital S) against the lowercase-first needle list.
function findForbiddenHits(src: string, label: string): Hit[] {
  const hits: Hit[] = []
  const lines = src.split('\n')
  const lower = lines.map((l) => l.toLowerCase())
  lower.forEach((line, i) => {
    for (const needle of FORBIDDEN_STRINGS) {
      if (line.includes(needle.toLowerCase())) hits.push({ file: label, line: i + 1, needle })
    }
  })
  // A needle split across a line wrap (e.g. reformatted JSX text) evades the per-line pass
  // above. Also check each adjacent line pair joined by a space, skipping what's already hit.
  for (let i = 0; i < lower.length - 1; i++) {
    const pair = `${lower[i]} ${lower[i + 1]}`
    for (const needle of FORBIDDEN_STRINGS) {
      const n = needle.toLowerCase()
      if (pair.includes(n) && !lower[i].includes(n) && !lower[i + 1].includes(n)) {
        hits.push({ file: label, line: i + 1, needle })
      }
    }
  }
  return hits
}

function formatHit(hit: Hit): string {
  return `${hit.file}:${hit.line}: ${hit.needle}`
}

function scanFiles(paths: string[]): Hit[] {
  return paths.flatMap((relPath) => findForbiddenHits(readFileSync(join(REPO_ROOT, relPath), 'utf8'), relPath))
}

describe('environment posture copy guard (DEMO-01-09, task-326)', () => {
  it('every SPA is scanned', () => {
    const { scanned } = partition(listFrontendSourceFiles())

    // Vacuity guard: a broken pathspec (e.g. `-- frontendX`) collapses this to 0 and must go
    // RED here, never pass silently through to a vacuously-empty hit list below.
    expect(scanned.length, 'total scanned files (vacuity guard)').toBeGreaterThanOrEqual(120)
    for (const spa of SPAS) {
      const count = scanned.filter((p) => p.startsWith(`frontend/${spa}/`)).length
      expect(count, `${spa} scanned files (vacuity guard)`).toBeGreaterThanOrEqual(20)
    }
  })

  it('the exception list is exactly the test files', () => {
    const all = listFrontendSourceFiles()
    const { scanned, excluded } = partition(all)

    // Vacuity guard: on a broken pathspec `all` collapses to [], and both assertions below
    // pass on empty input (0+0===0, []===[]) without ever exercising the exception rule.
    expect(all.length, 'total tracked files (vacuity guard)').toBeGreaterThanOrEqual(120)
    expect(scanned.length + excluded.length, 'scanned + excluded must equal all tracked files').toBe(all.length)

    const nonTestExcluded = excluded.filter((p) => !TEST_FILE_RE.test(p))
    expect(nonTestExcluded, `excluded paths must all match *.test.ts(x): ${nonTestExcluded.join(', ')}`).toEqual([])
  })

  it('the matcher is not vacuous', () => {
    // One line per forbidden string, plus a case-shifted `Sent to NRS` — the positive control
    // proving case-insensitivity (the live defect is capital-S `Sent to NRS`).
    const SAMPLE = [
      'legally-valid outputs are produced here',
      'legally valid documents only',
      'this includes clearance evidence',
      'sub="Sent to NRS"',
      'documents transmits to NRS today',
      'msg: Transmitted to NRS · IRN assigned',
      'every document has been acknowledged by NRS',
      'tag: "PRODUCTION · NRS"',
      'label="NRS-accepted"',
      'sub="IRN + CSID returned"',
      'operating against the NRS test adapter',
    ].join('\n')

    const hits = findForbiddenHits(SAMPLE, 'sample.ts')
    const detected = new Set(hits.map((h) => h.needle))

    expect(hits.length, 'hits found in the sample (vacuity guard)').toBeGreaterThanOrEqual(FORBIDDEN_STRINGS.length)
    const undetected = FORBIDDEN_STRINGS.filter((needle) => !detected.has(needle))
    expect(undetected, `forbidden strings not detected in the sample: ${undetected.join(', ')}`).toEqual([])
  })

  it('catches a needle split across a line wrap', () => {
    const SAMPLE = ['Submissions are transmitted', 'to NRS and return evidence.'].join('\n')
    const hits = findForbiddenHits(SAMPLE, 'sample.ts')
    expect(hits.map((h) => h.needle)).toEqual(['transmitted to NRS'])
    expect(hits[0].line).toBe(1)
  })

  it('no forbidden claim appears in any SPA', () => {
    const { scanned } = partition(listFrontendSourceFiles())
    expect(scanned.length, 'files to scan (vacuity guard)').toBeGreaterThanOrEqual(120)

    const hits = scanFiles(scanned)
    expect(hits.map(formatHit), hits.map(formatHit).join('\n')).toEqual([])
  })

  // frontend/app has no dependency on this package (adding one just to share an array
  // would invert the graph — app is a product package, e2e is test infra), so its node-tier
  // guard keeps its own copy of the list. This is the cross-check that stops it drifting
  // out of sync the way topology/environment-posture.spec.ts's copy did.
  it("app's node-tier guard list matches the canonical list", () => {
    const src = readFileSync(join(REPO_ROOT, 'frontend/app/src/envPosture.test.ts'), 'utf8')
    const match = src.match(/const FORBIDDEN = \[([\s\S]*?)\]/)
    if (!match) throw new Error('envPosture.test.ts: could not find its FORBIDDEN array')

    const appList = [...match[1].matchAll(/'([^']*)'/g)].map((m) => m[1])
    expect(appList.length, 'the regex extracted nothing (vacuity guard)').toBeGreaterThan(0)
    expect(new Set(appList)).toEqual(new Set(FORBIDDEN_STRINGS))
  })
})
