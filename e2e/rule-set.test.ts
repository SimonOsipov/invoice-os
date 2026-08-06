// BUG-05-05 (task-414) QA: e2e/rule-set.ts's header is a hand list, and hand lists rot --
// this exact header has already under-counted a live consumer twice (day30.spec.ts, then
// topology/persona-surfaces.spec.ts, both per rule-set.ts's own [positional-pins-are-invisible]
// history). A header comment cannot enforce its own completeness; this test can. It
// re-derives both the header's CLAIMED consumers and the ACTUAL ones from source (grepping
// the same two signals the header's own prose names: the literal `ACTIVE_RULE_SET_VERSION`
// token, and `VALIDATION_EXPECTED.ruleSetVersion`) and diffs them in both directions, so a
// future consumer -- or a stale header entry -- fails HERE, not on a third miss. Mirrors
// internal/validation/rule_set_v2_qa_test.go's grep-for-a-literal-pin shape, and
// personas.test.ts's derive-from-source-vs-hand-list comparison (rows 4-6, 13, 15-17).
import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'

const E2E_ROOT = dirname(fileURLToPath(import.meta.url))
const RULE_SET_SRC = join(E2E_ROOT, 'rule-set.ts')
const SELF_SRC = join(E2E_ROOT, 'rule-set.test.ts')

// Every .ts file under e2e/, excluding node_modules -- rule-set.ts (the declaration, not a
// consumer) and this file itself (its own doc comment necessarily reproduces both anchor
// strings) are excluded by the caller, the same self-exclusion rule_set_v2_qa_test.go and
// personas.test.ts's row 11 both apply.
function listTsFiles(dir: string): string[] {
  const out: string[] = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.name === 'node_modules' || entry.name.startsWith('.')) continue
    const full = join(dir, entry.name)
    if (entry.isDirectory()) out.push(...listTsFiles(full))
    else if (entry.name.endsWith('.ts')) out.push(full)
  }
  return out
}

function relPath(full: string): string {
  return relative(E2E_ROOT, full).split('\\').join('/')
}

// Parses the header's own bulleted list (`//   - <path>   -- <prose>`) between two anchor
// strings -- never trusted as a count, only as the CLAIM this test checks against source.
function parseHeaderBullets(src: string, startAnchor: string, endAnchor: string): string[] {
  const start = src.indexOf(startAnchor)
  const end = src.indexOf(endAnchor, start)
  if (start === -1 || end === -1) {
    throw new Error(`rule-set.ts header anchors moved ("${startAnchor}" / "${endAnchor}") -- update e2e/rule-set.test.ts`)
  }
  return [...src.slice(start, end).matchAll(/^\/\/ {3}- (\S+)/gm)].map((m) => m[1])
}

const RULE_SET_TS_SRC = readFileSync(RULE_SET_SRC, 'utf8')
const OTHER_FILES = listTsFiles(E2E_ROOT).filter((f) => f !== RULE_SET_SRC && f !== SELF_SRC)

describe("e2e/rule-set.ts header vs actual source ([positional-pins-are-invisible])", () => {
  it('direct ACTIVE_RULE_SET_VERSION importers match the header exactly', () => {
    const headerDirect = parseHeaderBullets(
      RULE_SET_TS_SRC,
      '// Direct importers of ACTIVE_RULE_SET_VERSION:',
      "// Transitive consumers, via topology/targets.ts's VALIDATION_EXPECTED.ruleSetVersion",
    )
    expect(headerDirect.length, 'header direct-importer bullets parsed (vacuity guard)').toBeGreaterThanOrEqual(1)

    const actualDirect = OTHER_FILES.filter((f) => readFileSync(f, 'utf8').includes('ACTIVE_RULE_SET_VERSION')).map(relPath).sort()
    expect(actualDirect, 'zero actual direct consumers found (vacuity guard)').not.toEqual([])

    expect(new Set(actualDirect), 'a real consumer is missing from the header').toEqual(new Set(headerDirect))
  })

  it('transitive VALIDATION_EXPECTED.ruleSetVersion consumers match the header exactly', () => {
    const headerTransitive = parseHeaderBullets(
      RULE_SET_TS_SRC,
      "// Transitive consumers, via topology/targets.ts's VALIDATION_EXPECTED.ruleSetVersion",
      '// All of the above are steps of the one gated',
    )
    expect(headerTransitive.length, 'header transitive-consumer bullets parsed (vacuity guard)').toBeGreaterThanOrEqual(1)

    const actualTransitive = OTHER_FILES.filter((f) => readFileSync(f, 'utf8').includes('VALIDATION_EXPECTED.ruleSetVersion'))
      .map(relPath)
      .sort()
    expect(actualTransitive, 'zero actual transitive consumers found (vacuity guard)').not.toEqual([])

    expect(new Set(actualTransitive), 'a real consumer is missing from the header').toEqual(new Set(headerTransitive))
  })
})
