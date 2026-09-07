// `node` environment (vitest.config.ts default) -- five static source scans, nothing
// renders. Idiom-twin is lib/routeWriterGuard.test.ts, not the App.*.test.tsx family:
// every file in that family renders the app component and needs a jsdom URL reset this
// file does not, since it never mounts anything.
//
// AUDITED_ATOMS (App.atomAudit.data.ts) is the audit table's data.

import { readFileSync } from 'node:fs'
import path from 'node:path'

import { describe, expect, it } from 'vitest'

import { AUDITED_ATOMS, type AuditedAtom } from './App.atomAudit.data'

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

const WORKSPACE_START = 'function Workspace('
const APP_START = 'export default function App() {'

// Verbatim bracket binding after `useState` -- never an `[x, setX]` pair regex, which
// drops the two setter-less atoms (App.tsx:317's boot snapshot and :329's `seed`).
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

  it('guard_filingIsAuditedAsDeliberate', () => {
    const audited = requireAudited()
    const entry = audited.find((a) => a.name === 'filing')
    expect(entry, 'AUDITED_ATOMS has no row for `filing`').toBeDefined()
    expect(
      entry!.verdict,
      '`filing` must be verdict `deliberate` -- App.tsx:662-665 explains why it is not cleared',
    ).toBe('deliberate')

    const appSrc = readSrc('src/App.tsx')
    const { openBraceIdx, closeIdx } = switchClientBody(appSrc)
    const bodyStartLine = lineNumberAt(appSrc, openBraceIdx)
    const bodyEndLine = lineNumberAt(appSrc, closeIdx)
    const citationLine = entry!.citationLine ?? -1
    expect(citationLine, 'filing\'s row needs a citationLine inside switchClient').toBeGreaterThanOrEqual(bodyStartLine)
    expect(citationLine, 'filing\'s citationLine must fall before switchClient\'s close').toBeLessThanOrEqual(bodyEndLine)
    const citedText = appSrc.split('\n')[citationLine - 1] ?? ''
    expect(citedText.trim().startsWith('//'), `citationLine ${citationLine} is not a comment line`).toBe(true)
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
