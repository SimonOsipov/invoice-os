// EXTR-18-05 (task-849) local guard. The twelve specs this story re-points cannot run
// locally -- they need a deployed environment (docs/e2e-convention.md) -- and every failure
// mode here is a plain string: a wrong testid, a wrong field-name literal, a selector that
// drifted from the element it names. `pnpm -r typecheck` and the vitest lane (which cannot
// even load .spec.ts, see vitest.config.ts) catch none of it. This reads import-wizard.spec.ts
// as source text and asserts what Stage 3's rewrite must and must not touch.
//
// Two of the checks below are RED on this commit by construction: the `-total` testids and
// the `f.name === 'total'` literal inside EXTR11-E2E-11 are what Stage 3 (task-851) still has
// to move to `-subtotal` / `'subtotal'`. That is the honest state of a Mode-A guard authored
// before the rewrite it is guarding.
import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const TOPOLOGY_DIR = dirname(fileURLToPath(import.meta.url))
const E2E_ROOT = dirname(TOPOLOGY_DIR)
const SPEC_PATH = join(TOPOLOGY_DIR, 'import-wizard.spec.ts')
const source = readFileSync(SPEC_PATH, 'utf8')

// Located by the spec-id markers actually in the source, not by hardcoded line numbers -- a
// line range that drifted would otherwise silently scan zero lines and pass vacuously.
function blockBetween(startNeedle: string, endNeedle: string): string {
  const start = source.indexOf(startNeedle)
  if (start === -1) throw new Error(`start marker not found in import-wizard.spec.ts: ${JSON.stringify(startNeedle)}`)
  const end = source.indexOf(endNeedle, start + startNeedle.length)
  if (end === -1) throw new Error(`end marker not found in import-wizard.spec.ts: ${JSON.stringify(endNeedle)}`)
  return source.slice(start, end)
}

const FIDELITY_START = 'EXTR11-E2E-11 · the AC-8 fidelity diff'
const FIDELITY_END = 'EXTR-12-09 · the settle-every-field journey'
const fidelityBlock = blockBetween(FIDELITY_START, FIDELITY_END)

// The scope named by task-849: "EXTR12-E2E-02 ... sits adjacent (:3614-3744)". extraction-input-
// total (the 4th testid this block preserves) is actually inside the NEXT test, EXTR12-E2E-03 --
// its own assertion that the read-only badge's replacement is still editable -- so the end
// marker below reaches just past it rather than stopping at EXTR12-E2E-03's own start.
const PRESERVED_START = "EXTR12-E2E-02 (AC-7): the corrected marker sits inside the value control"
const PRESERVED_END = "await testInfo.attach('extraction-read-only-retired.json'"
const preservedBlock = blockBetween(PRESERVED_START, PRESERVED_END)

describe('[extr-18-05] guard population (control needles + floors)', () => {
  it('read a non-trivial import-wizard.spec.ts', () => {
    expect(source.length, 'the file read as empty or truncated -- every scan below is vacuous').toBeGreaterThan(300_000)
  })

  it('found the EXTR11-E2E-11 block marker (control needle)', () => {
    expect(source).toContain(FIDELITY_START)
  })

  it('the EXTR11-E2E-11 block is non-trivial', () => {
    expect(fidelityBlock.length, 'the block between its start and end marker is near-empty -- a drifted marker scanned nothing').toBeGreaterThan(20_000)
  })

  it('the EXTR12-E2E-02 block is non-trivial', () => {
    expect(preservedBlock.length, 'the block between its start and end marker is near-empty -- a drifted marker scanned nothing').toBeGreaterThan(2_000)
  })
})

describe('[extr-18-05] EXTR11-E2E-11 must be re-pointed off total', () => {
  it('no extraction-*-total testid remains inside the block', () => {
    const matches = [...fidelityBlock.matchAll(/extraction-[a-z][a-z-]*-total\b/g)].map((m) => m[0])
    expect(
      matches,
      `found ${matches.length} -total testid reference(s) still inside EXTR11-E2E-11 -- Stage 3 must rename these to -subtotal: ${[...new Set(matches)].join(', ')}`,
    ).toEqual([])
  })

  it("no bare f.name === 'total' literal remains inside the block", () => {
    const found = /f\.name\s*===\s*['"]total['"]/.test(fidelityBlock)
    expect(found, "a literal f.name === 'total' is still inside EXTR11-E2E-11 -- Stage 3 must move it to 'subtotal'").toBe(false)
  })
})

describe('[extr-18-05] the FIDELITY table never lets element and selector disagree', () => {
  // Each FIDELITY row is one line in this file (fixtures_test.go-adjacent convention). A row
  // carries a custom `selector` only when its element has no testid of its own; the trap this
  // guards is a rename that moves `element` but not the `[data-testid=...]` string inside
  // `selector`, or vice versa -- typechecks clean, lints clean, reds only on the deployed run.
  const rowLines = fidelityBlock.split('\n').filter((l) => /\belement:\s*'[^']+'/.test(l) && /\bselector:\s*'[^']+'/.test(l))

  it('found rows carrying a selector (control needle)', () => {
    expect(rowLines.length, 'no FIDELITY row carries both element and selector -- the agreement check below covers nothing').toBeGreaterThan(0)
  })

  it('every element: / selector: pair names the same field', () => {
    const offenders: string[] = []
    for (const line of rowLines) {
      const element = line.match(/element:\s*'([^']+)'/)?.[1]
      const selector = line.match(/selector:\s*'([^']+)'/)?.[1]
      if (!element || !selector) {
        offenders.push(`unparsable row: ${line.trim()}`)
        continue
      }
      const field = selector.match(/data-testid="extraction-field-([a-z_]+)"/)?.[1]
      if (!field) {
        offenders.push(`${element}: selector carries no [data-testid="extraction-field-<name>"]: ${selector}`)
        continue
      }
      if (!element.endsWith(`-${field}`)) {
        offenders.push(`${element} targets field "${field}" per its selector, but its own name disagrees`)
      }
    }
    expect(offenders, offenders.join('\n')).toEqual([])
  })
})

describe('[extr-18-05] EXTR12-E2E-02 keeps its -total testids (it is not rewritten)', () => {
  for (const testid of ['extraction-field-total', 'extraction-control-total', 'extraction-marker-total', 'extraction-input-total']) {
    it(`still references ${testid}`, () => {
      expect(preservedBlock, `${testid} is missing from EXTR12-E2E-02 -- it must keep asserting on 'total', not 'subtotal'`).toContain(testid)
    })
  }
})

describe('[extr-18-05] line_total is never rewritten to line_subtotal', () => {
  function listTsFiles(dir: string): string[] {
    const out: string[] = []
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      if (entry.name === 'node_modules') continue
      const full = join(dir, entry.name)
      if (entry.isDirectory()) out.push(...listTsFiles(full))
      else if (entry.isFile() && entry.name.endsWith('.ts')) out.push(full)
    }
    return out
  }

  // Excludes this guard's own file: its source text necessarily names both literals, which
  // would otherwise report itself as an offender.
  const SELF = fileURLToPath(import.meta.url)
  const files = listTsFiles(E2E_ROOT).filter((f) => f !== SELF)
  const contents = files.map((f) => ({ path: f, text: readFileSync(f, 'utf8') }))

  it('scanned a real population of e2e/ .ts files', () => {
    expect(files.length, 'the walk found suspiciously few .ts files -- it may be scanning the wrong directory').toBeGreaterThan(20)
  })

  it('control needle: line_total appears somewhere under e2e/', () => {
    const found = contents.some((c) => c.text.includes('line_total'))
    expect(found, 'line_total was not found anywhere under e2e/ -- the scan below proves nothing').toBe(true)
  })

  it('no file under e2e/ contains line_subtotal', () => {
    const offenders = contents.filter((c) => c.text.includes('line_subtotal')).map((c) => c.path.slice(E2E_ROOT.length + 1))
    expect(offenders, offenders.join(', ')).toEqual([])
  })
})
