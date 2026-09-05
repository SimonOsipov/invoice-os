// A bare sessionStorage/localStorage is absent under vitest's `node` environment on CI's
// Node 22 but present on a Node >= 24 dev box, so this break is invisible locally and red
// on every CI run. A per-spec vi.stubGlobal does not license a file-scope access, so jsdom
// is the only satisfying form. Scoped to frontend/app/src: the walk must not leave this
// package, or a sibling /ralph worktree joins the corpus.
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

const SRC = fileURLToPath(new URL('.', import.meta.url))
const SELF = fileURLToPath(import.meta.url)

function testFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.name === 'node_modules') continue
    const path = join(dir, entry.name)
    if (entry.isDirectory()) out.push(...testFiles(path))
    else if (/\.test\.tsx?$/.test(entry.name) && path !== SELF) out.push(path)
  }
  return out
}

// A property access on the bare global. The lookbehind drops `window.localStorage` and the
// quoted name in `vi.stubGlobal('localStorage', ...)`, neither of which needs the global.
const BARE_ACCESS = /(?<![\w$.'"`])(?:session|local)Storage\s*\./
// Split: vitest scans this file for the docblock too, and a whole literal would put THIS
// scan under jsdom, where import.meta.url is an http URL and fileURLToPath throws.
const DECLARES_JSDOM = new RegExp('@vitest-' + 'environment jsdom')

const rel = (path: string) => path.slice(SRC.length)
const FILES = testFiles(SRC)
const AT_RISK = FILES.filter((path) => BARE_ACCESS.test(readFileSync(path, 'utf8'))).map(rel)

describe('a test reading a Web Storage global declares the jsdom environment', () => {
  it('control: the scan reaches this package and still matches', () => {
    expect(FILES.length, 'the walk drifted off frontend/app/src').toBeGreaterThanOrEqual(100)
    expect(AT_RISK.length, 'the bare-access matcher stopped matching').toBeGreaterThanOrEqual(10)
    expect(AT_RISK, 'the known bare-access file went unseen').toContain('lib/deepLink.test.ts')
  })

  it('control: a stubbed-only file is not counted as bare access', () => {
    expect(AT_RISK).not.toContain('lib/session.test.ts')
  })

  it('every file reading a storage global declares jsdom', () => {
    const undeclared = AT_RISK.filter(
      (path) => !DECLARES_JSDOM.test(readFileSync(join(SRC, path), 'utf8')),
    )
    expect(undeclared, 'add `// @vitest-' + 'environment jsdom` as the first line').toEqual([])
  })
})
