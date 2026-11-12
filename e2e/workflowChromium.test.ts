// Runner apt mirrors are unreachable, so `playwright install --with-deps` (apt-get of fonts) fails the E2E gate.
// Absence assertions carry a control needle and a floor so a parse failure cannot read as a clean zero.
import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO = dirname(dirname(fileURLToPath(import.meta.url)))
const WORKFLOWS = join(REPO, '.github/workflows')
const E2E_JOBS = ['e2e', 'topology', 'email']

const stripComments = (text: string) =>
  text
    .split('\n')
    .map((l) => l.replace(/(^|\s)#.*$/, '$1'))
    .join('\n')

const lines = (text: string) => stripComments(text).split('\n')

function jobBlock(text: string, job: string): string[] {
  const all = text.split('\n')
  const start = all.findIndex((l) => l === `  ${job}:`)
  if (start < 0) return []
  const rel = all.slice(start + 1).findIndex((l) => /^  [a-z0-9_-]+:$/.test(l))
  const end = rel < 0 ? all.length : start + 1 + rel
  return lines(all.slice(start, end).join('\n'))
}

const devEnv = readFileSync(join(WORKFLOWS, 'dev-env.yml'), 'utf8')

describe('workflow Chromium install', () => {
  it('no playwright install command in any workflow uses --with-deps', () => {
    const files = readdirSync(WORKFLOWS).filter((f) => f.endsWith('.yml'))
    expect(files).toContain('dev-env.yml')
    const installs = files.flatMap((f) =>
      lines(readFileSync(join(WORKFLOWS, f), 'utf8'))
        .filter((l) => /playwright install/.test(l))
        .map((l) => `${f}: ${l.trim()}`),
    )
    expect(installs.length).toBeGreaterThanOrEqual(3)
    expect(installs.filter((l) => l.includes('--with-deps'))).toEqual([])
    const depsAnywhere = files.flatMap((f) =>
      lines(readFileSync(join(WORKFLOWS, f), 'utf8'))
        .filter((l) => /--with-deps|install-deps/i.test(l))
        .map((l) => `${f}: ${l.trim()}`),
    )
    expect(depsAnywhere).toEqual([])
  })

  it('dev-env.yml has at least 3 playwright install commands', () => {
    const n = lines(devEnv).filter((l) => /playwright install/.test(l)).length
    expect(n).toBeGreaterThanOrEqual(3)
  })

  describe.each(E2E_JOBS)('job %s', (job) => {
    const block = jobBlock(devEnv, job)

    it('is found and runs playwright install', () => {
      expect(block.length).toBeGreaterThan(0)
      expect(block.some((l) => /playwright install/.test(l))).toBe(true)
    })

    it('runs no apt or apt-get step', () => {
      expect(block.length).toBeGreaterThan(0)
      expect(block.filter((l) => /\bapt(-get)?\b/i.test(l))).toEqual([])
    })
  })
})
