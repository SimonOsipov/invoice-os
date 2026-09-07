// `node` environment (vitest.config.ts default) -- seven static source scans, nothing
// renders. Idiom-twin is lib/routeWriterGuard.test.ts, not the App.*.test.tsx family:
// every file in that family renders the app component and needs a jsdom URL reset this
// file does not, since it never mounts anything.
//
// AUDITED_ATOMS (App.atomAudit.data.ts) is the audit table's data.

import { readFileSync } from 'node:fs'
import path from 'node:path'

import { describe, expect, it } from 'vitest'

import { AUDITED_ATOMS, type AuditedAtom, type Citation } from './App.atomAudit.data'

function readSrc(relPath: string): string {
  return readFileSync(path.join(process.cwd(), relPath), 'utf8')
}

// Brace-counting body extractor -- a plain string search for the closing `}` stops at
// the first one, which is wrong once a body nests a block (switchClient nests none
// today, but the review-path mirror it sits beside does; match the safe idiom anyway).
function bracedBodyFrom(src: string, openBraceIdx: number): number {
  let depth = 0
  let i = openBraceIdx
  for (; i < src.length; i++) {
    if (src[i] === '{') depth++
    else if (src[i] === '}') {
      depth--
      if (depth === 0) break
    }
  }
  expect(i, 'no matching closing brace found from the given offset').toBeLessThan(src.length)
  return i
}

function lineNumberAt(src: string, charIndex: number): number {
  return src.slice(0, charIndex).split('\n').length
}

// Braced body of the first `{` after `marker`. Same idiom as lib/routeWriterGuard.test.ts.
function bodyAfterMarker(src: string, marker: string): string {
  const markerIdx = src.indexOf(marker)
  expect(markerIdx, `citation scope marker not found: ${JSON.stringify(marker)}`).toBeGreaterThan(-1)
  const braceIdx = src.indexOf('{', markerIdx)
  expect(braceIdx, `no opening brace after scope marker: ${JSON.stringify(marker)}`).toBeGreaterThan(-1)
  return src.slice(braceIdx, bracedBodyFrom(src, braceIdx) + 1)
}

// The one line a citation resolves to. Fails unless the needle matches EXACTLY one line in
// scope -- an ambiguous needle cites neither site, and the whole point of dropping line
// numbers was to stop filing a citation nothing checks.
function resolveCitation(appSrc: string, c: Citation, label: string): { line: number; text: string } {
  const scope = c.in === undefined ? appSrc : bodyAfterMarker(appSrc, c.in)
  const hits = scope.split('\n').filter((l) => l.trim() === c.text)
  expect(hits, `${label}: citation matched ${hits.length} lines, expected exactly 1 -- ${JSON.stringify(c.text)}`).toHaveLength(1)
  const idx = appSrc.split('\n').findIndex((l) => l.trim() === c.text && (c.in === undefined || scope.includes(l)))
  return { line: idx + 1, text: c.text }
}

const WORKSPACE_START = 'function Workspace('
const APP_START = 'export default function App() {'

// Verbatim bracket binding after `useState` -- never an `[x, setX]` pair regex, which
// drops the two setter-less atoms: Workspace's boot snapshot and its `seed`.
function extractStateBindings(slice: string): string[] {
  const re = /const \[([^\]]*)\]\s*=\s*useState/g
  const out: string[] = []
  let m: RegExpExecArray | null
  while ((m = re.exec(slice))) out.push(m[1].replace(/\s+/g, ' ').trim())
  return out
}

function extractRefBindings(slice: string): string[] {
  const re = /const (\w+) = useRef/g
  const out: string[] = []
  let m: RegExpExecArray | null
  while ((m = re.exec(slice))) out.push(m[1])
  return out
}

function requireAudited(): readonly AuditedAtom[] {
  expect(
    AUDITED_ATOMS,
    'AUDITED_ATOMS is not exported from App.atomAudit.data.ts yet -- the executor adds the audit table next',
  ).toBeDefined()
  return AUDITED_ATOMS
}

function switchClientBody(appSrc: string): { body: string; openBraceIdx: number; closeIdx: number } {
  const start = appSrc.indexOf('function switchClient(id: string)')
  expect(start, 'switchClient anchor not found -- App.tsx was restructured').toBeGreaterThan(-1)
  const openBraceIdx = appSrc.indexOf('{', start)
  expect(openBraceIdx, 'switchClient has no opening brace').toBeGreaterThan(-1)
  const closeIdx = bracedBodyFrom(appSrc, openBraceIdx)
  return { body: appSrc.slice(openBraceIdx + 1, closeIdx), openBraceIdx, closeIdx }
}

describe('AC-4: every Workspace atom has an audit row', () => {
  it('guard_everyWorkspaceAtomHasAnAuditRow', () => {
    const audited = requireAudited()
    const appSrc = readSrc('src/App.tsx')
    const startIdx = appSrc.indexOf(WORKSPACE_START)
    const endIdx = appSrc.indexOf(APP_START)
    expect(startIdx, 'the `function Workspace(` anchor moved -- re-anchor the slice, do not relax it').toBeGreaterThan(-1)
    expect(endIdx, 'the `export default function App() {` anchor moved -- re-anchor the slice').toBeGreaterThan(-1)
    const slice = appSrc.slice(startIdx, endIdx)

    const sourceNames = new Set([...extractStateBindings(slice), ...extractRefBindings(slice)])
    const auditedNames = new Set(audited.map((a) => a.binding))

    const missing = [...sourceNames].filter((n) => !auditedNames.has(n))
    const phantom = [...auditedNames].filter((n) => !sourceNames.has(n))

    expect(missing, `atoms declared in Workspace with no AUDITED_ATOMS row: ${missing.join(' | ')}`).toEqual([])
    expect(phantom, `AUDITED_ATOMS rows naming no atom in Workspace: ${phantom.join(' | ')}`).toEqual([])
  })

  it('guard_theAtomSliceIsNotVacuous', () => {
    requireAudited()
    const appSrc = readSrc('src/App.tsx')
    const startIdx = appSrc.indexOf(WORKSPACE_START)
    const endIdx = appSrc.indexOf(APP_START)

    // Anchor assertions first: a mis-anchored slice must fail naming the anchor, not
    // fail the floor below for the wrong reason.
    expect(startIdx, 'the `function Workspace(` anchor moved -- re-anchor the slice, do not relax it').toBeGreaterThan(-1)
    expect(endIdx, 'the `export default function App() {` anchor moved -- re-anchor the slice').toBeGreaterThan(-1)
    expect(startIdx, 'the slice anchors are inverted').toBeLessThan(endIdx)

    const slice = appSrc.slice(startIdx, endIdx)
    expect(slice.length, 'the Workspace slice is empty').toBeGreaterThan(0)

    const names = [...extractStateBindings(slice), ...extractRefBindings(slice)]
    expect(names.length, 'a mis-anchored slice must fail loudly, not pass by finding nothing').toBeGreaterThanOrEqual(30)
  })

  // Regression control on the EXTRACTION RULE, not on AUDITED_ATOMS: re-extracts from
  // App.tsx and checks the two setter-less bindings survive. An `[x, setX]` pair regex
  // drops both -- this fails it (and, via the phantom half, guard_everyWorkspaceAtomHasAnAuditRow too).
  it('guard_theTwoSetterLessAtomsAreInThePopulation', () => {
    const appSrc = readSrc('src/App.tsx')
    const startIdx = appSrc.indexOf(WORKSPACE_START)
    const endIdx = appSrc.indexOf(APP_START)
    const slice = appSrc.slice(startIdx, endIdx)
    const extracted = new Set(extractStateBindings(slice))

    const required = ['{ path: bootPath, search: bootSearch }', 'seed']
    const missing = required.filter((b) => !extracted.has(b))
    expect(missing, `setter-less atoms missing from the extracted set: ${missing.join(' | ')}`).toEqual([])
  })

  // ROUTE-06-02 QA. Every citation is text, so no row carries a line number to remap when
  // App.tsx shifts. This is what makes that safe: an unresolvable or ambiguous needle fails
  // here, loudly, instead of silently pointing at whatever now sits on the old line.
  it('guard_everyCitationResolvesToExactlyOneSite', () => {
    const audited = requireAudited()
    const appSrc = readSrc('src/App.tsx')
    const cited = audited.filter((a) => a.citation !== undefined)
    // Floor: a shape change that dropped every citation would make the loop vacuous.
    expect(cited.length, 'the audit must still carry citations').toBeGreaterThanOrEqual(18)
    for (const a of cited) {
      const { line } = resolveCitation(appSrc, a.citation as Citation, `${a.name}'s citation`)
      expect(line, `${a.name}'s citation resolved to no line`).toBeGreaterThan(0)
    }
  })

  // Prose drifts the same way structured fields did. Symbol anchors (App.tsx#switchClient)
  // survive an edit above them; `App.tsx:698` does not.
  it('guard_noAuditNoteCitesAnAppTsxLineNumber', () => {
    const audited = requireAudited()
    const offenders = audited.filter((a) => /App\.tsx:\d/.test(a.note)).map((a) => a.name)
    expect(offenders, `notes citing an App.tsx line number instead of App.tsx#Symbol: ${offenders.join(' | ')}`).toEqual(
      [],
    )
    // Control needle: the assertion above must be able to match at all.
    expect(/App\.tsx:\d/.test('cleared at App.tsx:702'), 'the line-number needle matches nothing').toBe(true)
  })

  // Anchored on the STATEMENT the comment sits above, then walked upward -- so the guard
  // survives any line shift, and the two content assertions below stay independent of the
  // anchor rather than restating it.
  it('guard_filingIsAuditedAsDeliberate', () => {
    const audited = requireAudited()
    const entry = audited.find((a) => a.name === 'filing')
    expect(entry, 'AUDITED_ATOMS has no row for `filing`').toBeDefined()
    expect(entry!.verdict, '`filing` must be verdict `deliberate`').toBe('deliberate')

    const appSrc = readSrc('src/App.tsx')
    const { body, openBraceIdx } = switchClientBody(appSrc)
    const anchorIdx = body.indexOf('setFilingError(null)')
    expect(anchorIdx, 'switchClient no longer clears filingError -- the cited block has no anchor').toBeGreaterThan(-1)
    const anchorLine = lineNumberAt(appSrc, openBraceIdx + 1 + anchorIdx)
    const lines = appSrc.split('\n')

    // The contiguous comment block immediately above the anchor.
    const block: string[] = []
    for (let i = anchorLine - 2; i >= 0 && (lines[i] ?? '').trim().startsWith('//'); i--) block.unshift(lines[i] as string)
    expect(block.length, 'floor: no comment block above setFilingError(null) -- the decision is undocumented').toBeGreaterThan(0)

    const text = block.join('\n')
    expect(text, 'the block must state the decision itself, not merely be a comment').toContain('deliberately NOT')
    expect(text, 'the block must give the in-flight reason, which is what makes the non-clearing deliberate').toContain(
      'still in flight',
    )
    // The row's own citation must point into that block, not somewhere else entirely.
    const cited = resolveCitation(appSrc, entry!.citation as Citation, "filing's citation")
    expect(block.map((l) => l.trim()), 'filing\'s citation must land inside the block it claims').toContain(cited.text)
  })
})

describe('AC-1: switchClient still clears extractionJobId (already shipped, not re-implemented)', () => {
  it('guard_switchClientStillClearsTheExtractionJob', () => {
    requireAudited()
    const appSrc = readSrc('src/App.tsx')
    const { body } = switchClientBody(appSrc)
    expect(body.length, 'switchClient body is empty -- the anchor is broken').toBeGreaterThan(0)
    expect(body, 'AC-1: switchClient must still clear extractionJobId').toContain('setExtractionJobId(null)')
    expect(body, 'AC-1: switchClient must still leave auditPrefilter to navigate(\'dashboard\')').not.toContain(
      'setAuditPrefilter',
    )
  })
})

// ROUTE-06-08 AC-4. docs/routing.md carries its own copy of the atom table -- a PR body is
// not a durable location -- so this pins the copy to AUDITED_ATOMS instead of trusting a
// human to keep both in sync. Compares name + verdict + switchClient-reset only: Routes
// renders as a shorthand ("all 13") in the doc and would be a brittle needle for no benefit.
describe('ROUTE-06-08 AC-4: the docs table matches AUDITED_ATOMS', () => {
  it('guard_theDocsTableMatchesTheAuditedAtoms', () => {
    const audited = requireAudited()
    const docSrc = readSrc('../../docs/routing.md')
    const rowRe = /^\|\s*`([^`]+)`\s*\|\s*(yes|no)\s*\|[^|]*\|\s*`([a-z-]+)`\s*\|$/gm
    const docRows = new Map<string, { reset: boolean; verdict: string }>()
    let m: RegExpExecArray | null
    while ((m = rowRe.exec(docSrc))) docRows.set(m[1], { reset: m[2] === 'yes', verdict: m[3] })
    expect(docRows.size, 'the doc table extraction found no rows -- the row shape moved').toBeGreaterThan(30)

    const missing = audited.filter((a) => !docRows.has(a.name)).map((a) => a.name)
    const phantom = [...docRows.keys()].filter((n) => !audited.some((a) => a.name === n))
    expect(missing, `atoms in AUDITED_ATOMS with no docs/routing.md row: ${missing.join(' | ')}`).toEqual([])
    expect(phantom, `docs/routing.md rows naming no AUDITED_ATOMS atom: ${phantom.join(' | ')}`).toEqual([])

    const mismatched = audited
      .filter((a) => {
        const row = docRows.get(a.name)
        return row !== undefined && (row.verdict !== a.verdict || row.reset !== a.resetBySwitchClient)
      })
      .map((a) => a.name)
    expect(mismatched, `docs/routing.md disagrees with AUDITED_ATOMS on: ${mismatched.join(' | ')}`).toEqual([])
  })
})
