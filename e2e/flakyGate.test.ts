import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { flakyInChangedFiles, readFlakyDir } from './flakyGate'
import FlakyReporter, { type FlakySpec, type TestLike } from './flakyReporter'

const ROOT = '/home/runner/work/invoice-os/invoice-os'
const spec = (rel: string, line = 10): FlakySpec => ({
  file: `${ROOT}/${rel}`,
  line,
  title: `t ${rel}`,
  attempts: 2,
})

describe('flakyInChangedFiles', () => {
  it('fails a flaky spec whose file the PR changed and ignores the others', () => {
    const changed = spec('e2e/topology/import-wizard.spec.ts')
    const untouched = spec('e2e/smoke/landing-nav.spec.ts')
    expect(
      flakyInChangedFiles([untouched, changed], ['e2e/topology/import-wizard.spec.ts', 'go.mod'], ROOT),
    ).toEqual([changed])
  })

  it('returns nothing when no flaky spec is in the PR', () => {
    expect(flakyInChangedFiles([spec('e2e/api/import.spec.ts')], ['e2e/api/perf.spec.ts'], ROOT)).toEqual([])
  })

  it('accepts a repo root with a trailing slash and list lines with stray whitespace', () => {
    const hit = spec('e2e/api/import.spec.ts')
    expect(flakyInChangedFiles([hit], ['  e2e/api/import.spec.ts\r', ''], `${ROOT}/`)).toEqual([hit])
  })

  it('matches the whole repo-relative path, not a suffix', () => {
    const flaky = spec('e2e/topology/roles.spec.ts')
    expect(flakyInChangedFiles([flaky], ['topology/roles.spec.ts', 'roles.spec.ts'], ROOT)).toEqual([])
  })

  it('never matches an empty line against an unknown location', () => {
    const unknown: FlakySpec = { file: '<unknown>', line: 0, title: 'x', attempts: 2 }
    expect(flakyInChangedFiles([unknown], ['', '<unknown>'], ROOT)).toEqual([])
  })
})

describe('reporter JSON feeds the gate', () => {
  let dir: string
  const saved = { FLAKY_JSON_DIR: process.env.FLAKY_JSON_DIR, GITHUB_STEP_SUMMARY: process.env.GITHUB_STEP_SUMMARY }

  beforeEach(() => {
    dir = join(mkdtempSync(join(tmpdir(), 'flaky-gate-')), 'out')
    process.env.FLAKY_JSON_DIR = dir
    // Under CI this suite must not append a fake flaky table to the real job summary.
    delete process.env.GITHUB_STEP_SUMMARY
  })
  afterEach(() => {
    for (const [k, v] of Object.entries(saved)) {
      if (v === undefined) delete process.env[k]
      else process.env[k] = v
    }
  })

  const run = (label: string, tests: TestLike[]) => {
    const r = new FlakyReporter({ label })
    r.onBegin({} as never, { allTests: () => tests } as never)
    r.onEnd()
  }
  const t = (outcome: string, file: string): TestLike => ({
    outcome: () => outcome,
    titlePath: () => ['', 'chromium', file, 'a title'],
    location: { file, line: 7 },
    results: [{}, {}],
  })

  it('writes one file per suite, empty when nothing flaked, and merges them', () => {
    run('smoke', [t('expected', '/r/e2e/smoke/a.spec.ts')])
    run('topology', [t('flaky', '/r/e2e/topology/b.spec.ts')])
    expect(JSON.parse(readFileSync(join(dir, 'smoke.json'), 'utf8'))).toEqual([])
    expect(readFlakyDir(dir).map((f) => f.file)).toEqual(['/r/e2e/topology/b.spec.ts'])
  })

  it('reads a missing directory as no flaky specs', () => {
    expect(readFlakyDir(join(dir, 'never-written'))).toEqual([])
  })
})

describe('flakyGate CLI', () => {
  const gate = resolve(import.meta.dirname, 'flakyGate.ts')
  const repoRoot = resolve(import.meta.dirname, '..')

  const cli = (flaky: FlakySpec[], changed: string[]) => {
    const tmp = mkdtempSync(join(tmpdir(), 'flaky-cli-'))
    writeFileSync(join(tmp, 'topology.json'), JSON.stringify(flaky))
    writeFileSync(join(tmp, 'changed.txt'), changed.join('\n'))
    try {
      return { code: 0, out: execFileSync(process.execPath, [gate, tmp, join(tmp, 'changed.txt')], { encoding: 'utf8' }) }
    } catch (err) {
      const e = err as { status: number; stdout: string }
      return { code: e.status, out: e.stdout }
    }
  }
  const local: FlakySpec = { file: `${repoRoot}/e2e/topology/roles.spec.ts`, line: 42, title: 'roles', attempts: 2 }

  it('exits 1 and annotates the changed flaky spec', () => {
    const { code, out } = cli([local], ['e2e/topology/roles.spec.ts'])
    expect(code).toBe(1)
    expect(out).toContain('::error file=e2e/topology/roles.spec.ts,line=42::')
  })

  it('exits 0 when the flaky spec was not changed by the PR', () => {
    expect(cli([local], ['e2e/api/import.spec.ts']).code).toBe(0)
  })
})
