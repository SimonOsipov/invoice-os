// The landing build input loads only the v2 entry (AC 3). Source scans: a dead import or a class name
// in a string never reaches computed style. Test files are not scanned (D-14).
/// <reference types="node" />
import { readFileSync } from 'node:fs'
import { join, posix } from 'node:path'
import { describe, expect, it } from 'vitest'
import { LANDING_SRC, REPO_ROOT, landingBuildInput, scanBuildInput, stripSource } from './cssScan.test.util'

const V2_ENTRY = '@invoice-os/design-tokens/v2/styles.css'
const V1_ENTRY = '@invoice-os/design-tokens/styles.css'

function importSpecifiers(file: string, src: string): string[] {
  return [...stripSource(file, src).matchAll(/\bimport\s+(?:[^'"]*?\sfrom\s+)?['"]([^'"]+)['"]/g)].map((m) => m[1])
}

/** Files reached from `entry` through `@import`, `https://` skipped; paths are relative to `root`. */
function importChain(entry: string, read: (path: string) => string): string[] {
  const visited: string[] = []
  const queue = [posix.normalize(entry)]
  for (let path = queue.shift(); path !== undefined; path = queue.shift()) {
    if (visited.includes(path)) continue
    visited.push(path)
    const css = stripSource(path, read(path))
    for (const m of css.matchAll(/@import\s+(?:url\(\s*)?['"]?([^'")\s;]+)['"]?\s*\)?/gi)) {
      if (/^https?:\/\//i.test(m[1])) continue
      queue.push(posix.normalize(posix.join(posix.dirname(path), m[1])))
    }
  }
  return visited.sort()
}

describe('the landing loads only v2', () => {
  it('V2-01 population floor', () => {
    const keys = Object.keys(landingBuildInput())
    expect(keys.length).toBeGreaterThanOrEqual(25)
    expect(keys).toContain('../index.html')
    expect(keys.some((k) => /\.test\./.test(k))).toBe(false)
    // The scans must reach the bridge and the page base, or BR-* and V2-03 are blind to them.
    expect(keys).toContain('styles/bridge.css')
    expect(keys).toContain('styles/landing.css')
  })

  it('V2-03 no build input names asc-app, app-layer or the v1 entry', () => {
    const needles = [/asc-app/i, /app-layer/i, /design-tokens\/styles\.css/]
    const planted = scanBuildInput(
      {
        'planted.ts': `// asc-app\nconst c = 'ASC-APP'\nimport '${V1_ENTRY}'\nimport '${V2_ENTRY}'`,
        'planted.css': '/* app-layer */ a { color: red }',
      },
      needles,
    )
    expect(planted, 'control: comments are stripped, strings match case-insensitively, the v2 entry is not a hit').toEqual([
      'planted.ts: /asc-app/i',
      'planted.ts: /design-tokens\\/styles\\.css/',
    ])

    const files = landingBuildInput()
    expect(Object.keys(files).length).toBeGreaterThanOrEqual(25)
    const hits = scanBuildInput(files, needles)
    expect(hits, hits.join('\n')).toEqual([])
  })

  it('V2-04 main.tsx imports the v2 entry once', () => {
    const planted = importSpecifiers('p.tsx', `import '${V2_ENTRY}'\nimport a from "./a"\n// import '${V1_ENTRY}'`)
    expect(planted, 'control: side-effect and default imports are listed, a commented import is not').toEqual([V2_ENTRY, './a'])

    const specs = importSpecifiers('main.tsx', readFileSync(join(LANDING_SRC, 'main.tsx'), 'utf8'))
    expect(specs.length).toBeGreaterThanOrEqual(1)
    expect(specs.filter((s) => s === V2_ENTRY)).toHaveLength(1)
    expect(specs.filter((s) => s === V1_ENTRY)).toEqual([])
  })

  it('V2-06 main.tsx loads the v2 entry, then the bridge, then landing.css', () => {
    const order = ['@invoice-os/design-tokens/v2/styles.css', './styles/bridge.css', './styles/landing.css']
    const at = (specs: string[]) => order.map((s) => specs.indexOf(s))
    const planted = importSpecifiers('p.tsx', `import '${order[0]}'\nimport '${order[2]}'\n// import './styles/bridge.css'`)
    expect(at(planted), 'control: a commented import is absent, the others are found').toEqual([0, -1, 1])

    const specs = importSpecifiers('main.tsx', readFileSync(join(LANDING_SRC, 'main.tsx'), 'utf8'))
    const [v2, bridge, landing] = at(specs)
    expect(bridge, 'main.tsx imports bridge.css').toBeGreaterThanOrEqual(0)
    expect(specs.filter((s) => s === order[1])).toHaveLength(1)
    expect(v2, 'the bridge maps v2 names, so v2 loads first').toBeGreaterThanOrEqual(0)
    expect(v2).toBeLessThan(bridge)
    expect(bridge, 'landing.css reads the bridge names, so the bridge loads first').toBeLessThan(landing)
  })

  it('V2-05 the v2 entry import chain reaches no app-layer', () => {
    const fake: Record<string, string> = {
      'a.css': "@import url('./b.css'); @import 'c.css'; @import url(d.css); @import url(\"./sub/e.css\"); @import url('https://x.test/y.css'); /* @import 'z.css'; */",
      'b.css': '', 'c.css': '', 'd.css': '', 'sub/e.css': "@import '../f.css';", 'f.css': '',
    }
    expect(importChain('a.css', (p) => fake[p]), 'control: every import form is followed, https and commented imports are not').toEqual(
      ['a.css', 'b.css', 'c.css', 'd.css', 'f.css', 'sub/e.css'],
    )

    const root = join(REPO_ROOT, 'packages', 'design-tokens')
    const pkg = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8')) as { exports: Record<string, string> }
    const entry = pkg.exports['./v2/styles.css']
    expect(entry, "package.json exports['./v2/styles.css']").toBeTruthy()
    const visited = importChain(entry, (p) => readFileSync(join(root, p), 'utf8'))
    expect(visited).toEqual([
      'v2/styles.css',
      'v2/tokens/colors.css',
      'v2/tokens/hero-grid.css',
      'v2/tokens/spacing.css',
      'v2/tokens/typography.css',
      'v2/utilities.css',
    ])
    expect(visited.filter((p) => /app-layer\.css$/.test(p))).toEqual([])
  })
})
