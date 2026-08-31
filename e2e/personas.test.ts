// e2e/personas.test.ts — the persona axis guards G1-G5 (PERSONA-01-01, Backlog task-270).
// RED-then-GREEN, Test-first: yes. At t0 the import of ./personas does not resolve (no
// personas.ts exists yet), so every row below fails on the same uninformative collection
// error (Phase A). Phase B (empty-stub personas.ts/personaSession.ts, no data filled in)
// proves each row instead fails on its OWN assertion -- the only live proof that the
// vacuity guards below actually fire, since a guard that passes on empty input is the exact
// defect this subtask exists to prevent. Phase C (Stage 3, the executor) fills in the real
// registry data; all rows go GREEN with no change to this file.
//
// Path resolution follows e2e/api/no-db-access.test.ts:12 and e2e/package.test.ts:11:
// import.meta.url, never process.cwd() -- CI invokes vitest via `pnpm --filter` (cwd `e2e/`)
// but a developer may run from the repo root, and only import.meta.url is cwd-independent.
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { existsSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  PERSONAS,
  PERSONA_IDS,
  SURFACES,
  BOUNDARY_MATRIX,
  signInUrl,
  accepts,
  type Grade,
  type PersonaId,
  type Destination,
} from './personas'

// The legal coverage grades, mirroring personas.ts's Grade union (row 8 / G4 below). Typed
// as Grade[] so a value that is not a union member cannot be listed here; a member ADDED to
// the union and not listed here goes red at row 8 the first time a cell uses it, which is
// the loud direction to fail in.
const LEGAL_GRADES: Grade[] = ['drives', 'nav-only']

// --- G6b expectation sets (PERSONA-01-07, task-276) ------------------------------------
//
// Literal, HAND-WRITTEN sets living in THIS test file, never in e2e/personas.ts -- a
// downgrade or a promotion becomes a visible diff to the guard's OWN expectations rather
// than a quiet data change ([coverage-honesty]). Derived from the tree (each persona's
// `coverage` array in personas.ts) and confirmed against it, not copied out of the
// implementation plan.

// The complete nav-only set. Each entry carries its own one-line reason.
// firm:NAV_CUSTOMERS left this set in BUG-01-08 -- driven by invoice-surfaces.spec.ts now.
// firm:NAV_SETTINGS left it with the Roles tab -- driven by topology/roles.spec.ts now, and
// leaving it here while a firm spec drives the surface would be a false coverage claim.
const EXPECTED_NAV_ONLY = new Set<string>([
  'firm:NAV_RULES', // Core AC 2 scopes the sweep to in-house; the firm side is finding F-D
])

// The 8 in-house cells (Core AC 2, including inhouse:NAV_APPROVALS -> Core AC 3 and
// inhouse:NAV_WORKFLOWS -> Core AC 4) plus firm:NAV_WORKFLOWS (Core AC 4). Given exactly
// two grades and G6a's set equality, this set is LOGICALLY REDUNDANT: every cell not in
// EXPECTED_NAV_ONLY must already be graded 'drives'. It is kept for two reasons that are
// NOT logical necessity: (1) it is the only place Core ACs 2/3/4 are asserted BY NAME, so a
// failure here names the AC rather than a bare set difference; (2) it becomes load-bearing
// the day a third grade lands. Do not read this comment, or row 17 below, as claiming this
// is an independent check of anything G6a/row 16 don't already guarantee.
//
// A FIXED floor at those ACs, never a census of `drives` cells: firm x {Customers, Reports,
// Settings} were each promoted out of EXPECTED_NAV_ONLY afterwards and stay absent here on
// purpose -- row 16's set equality is what pins their grade.
const EXPECTED_DRIVES_MIN = new Set<string>([
  'inhouse:NAV_DASHBOARD',
  'inhouse:NAV_INVOICES',
  'inhouse:NAV_VALIDATION',
  'inhouse:NAV_WORKFLOWS', // Core AC 4
  'inhouse:NAV_RULES',
  'inhouse:NAV_APPROVALS', // Core AC 3
  'inhouse:NAV_REPORTS',
  'inhouse:NAV_SETTINGS',
  'firm:NAV_WORKFLOWS', // Core AC 4
  'firm:NAV_APPROVALS', // APPR-12-05 (task-530): firm CLIENT group gains Approvals
])

const REPO_ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const LANDING_AUTH = join(REPO_ROOT, 'frontend/landing/src/auth.ts')
const SIDEBAR = join(REPO_ROOT, 'frontend/app/src/components/Sidebar.tsx')
const SIDEBAR_TEST = join(REPO_ROOT, 'frontend/app/src/components/Sidebar.test.tsx')
const GLYPHS = join(REPO_ROOT, 'frontend/app/src/glyphs.tsx')
const APP_TSX = join(REPO_ROOT, 'frontend/app/src/App.tsx')
const TYPES_TS = join(REPO_ROOT, 'frontend/app/src/types.ts')
const PERSONAS_SRC = join(REPO_ROOT, 'e2e/personas.ts')
const PERSONAS_TEST_SRC = join(REPO_ROOT, 'e2e/personas.test.ts')
const APP_SESSION_SRC = join(REPO_ROOT, 'frontend/app/src/lib/session.ts')
const OPS_SESSION_SRC = join(REPO_ROOT, 'frontend/ops-console/src/session.ts')
const SUPPORT_SESSION_SRC = join(REPO_ROOT, 'frontend/support-console/src/session.ts')

// process.env hygiene (targets.test.ts:9-11's idiom, extended to all three destination
// vars): snapshot before each test and restore after, so row 1/2's mutations can never leak
// into a sibling test regardless of run order within this file.
const ENV_VARS = ['APP_URL', 'OPS_CONSOLE_URL', 'SUPPORT_CONSOLE_URL'] as const
let envSnapshot: Record<string, string | undefined>

beforeEach(() => {
  envSnapshot = Object.fromEntries(ENV_VARS.map((v) => [v, process.env[v]]))
})

afterEach(() => {
  for (const v of ENV_VARS) {
    if (envSnapshot[v] === undefined) delete process.env[v]
    else process.env[v] = envSnapshot[v]
  }
})

// --- G1/G2 extraction helpers (frontend/landing/src/auth.ts) ---------------------------

// G1: the LANDING_PERSONAS roster's `id:` values. Slice from the array's declaration to the
// next column-0 `]` (the only one in the file) and extract every quoted `id:` value in that
// slice.
function extractLandingPersonaIds(src: string): string[] {
  const startIdx = src.indexOf('export const LANDING_PERSONAS')
  if (startIdx === -1) {
    throw new Error('G1: `export const LANDING_PERSONAS` anchor not found in frontend/landing/src/auth.ts -- the anchors moved, update e2e/personas.test.ts')
  }
  const endIdx = src.indexOf('\n]', startIdx)
  if (endIdx === -1) {
    throw new Error('G1: closing column-0 `]` not found after LANDING_PERSONAS -- the anchors moved, update e2e/personas.test.ts')
  }
  const slice = src.slice(startIdx, endIdx)
  return [...slice.matchAll(/\bid:\s*['"]([^'"]+)['"]/g)].map((m) => m[1])
}

// G2: the `LandingPersona` interface's `id` property union -- NOT `frontend/app/src/auth.ts`'s
// unrelated 2-member `PersonaId` type (a wrong-file trap: [FIX-3] in task-270's plan). Slice
// from the interface's declaration to its closing column-0 `}`, isolate the single `id:` line,
// and extract only the quoted members on THAT line -- scoping to one line is what keeps the
// `target: 'app' | 'ops' | 'support'` property out.
function extractLandingPersonaIdUnion(src: string): string[] {
  const startIdx = src.indexOf('export interface LandingPersona')
  if (startIdx === -1) {
    throw new Error('G2: `export interface LandingPersona` anchor not found in frontend/landing/src/auth.ts -- the anchors moved, update e2e/personas.test.ts')
  }
  const endIdx = src.indexOf('\n}', startIdx)
  if (endIdx === -1) {
    throw new Error('G2: closing column-0 `}` not found after LandingPersona -- the anchors moved, update e2e/personas.test.ts')
  }
  const slice = src.slice(startIdx, endIdx)
  const idLine = slice.match(/^\s*id:\s*(.+)$/m)
  if (!idLine) {
    throw new Error('G2: no `id:` line found inside the LandingPersona interface -- the anchors moved, update e2e/personas.test.ts')
  }
  return [...idLine[1].matchAll(/['"]([^'"]+)['"]/g)].map((m) => m[1])
}

// --- G3 / G3-neg extraction helpers (frontend/app/src/components/Sidebar.tsx + glyphs.tsx) ---

// Alias map for the two SidebarNavItem locals that wrap a NAV_* spread with a computed
// `badge` (invoicesItem -> NAV_INVOICES, approvalsItem -> NAV_APPROVALS) -- derived from the
// whole file so a rename cannot rot it, per task-270's plan.
function buildSidebarAliasMap(sidebarSrc: string): Record<string, string> {
  const map: Record<string, string> = {}
  for (const m of sidebarSrc.matchAll(/const\s+(\w+):\s*SidebarNavItem\s*=\s*\{\s*\.\.\.(NAV_[A-Z_]+)/g)) {
    map[m[1]] = m[2]
  }
  return map
}

// The shared token-resolution logic: given a slice of source containing one or more
// `items: [...]` arrays, returns every resolved NAV_* constant name (direct token, or via the
// alias map). Named and parameterized on source text (not inlined into the G3 case) so row 7
// (G3-neg) can run the identical logic over an inline fixture string.
function resolveNavConstsFromGroupsSlice(groupsSlice: string, aliasMap: Record<string, string>): string[] {
  const itemsBodies = [...groupsSlice.matchAll(/items:\s*\[([^\]]*)\]/g)].map((m) => m[1])
  const resolved: string[] = []
  for (const body of itemsBodies) {
    for (const raw of body.split(',')) {
      const token = raw.trim()
      if (!token) continue
      if (/^NAV_[A-Z_]+$/.test(token)) {
        resolved.push(token)
      } else if (token in aliasMap) {
        resolved.push(aliasMap[token])
      } else {
        throw new Error(`G3: unresolved sidebar nav token "${token}" -- no NAV_* form and not in the alias map, update e2e/personas.test.ts`)
      }
    }
  }
  return resolved
}

// G3: slices Sidebar.tsx between its `navGroups` and `activeNav` anchors, splits on the
// `isFirm ? [...] : [...]` seam (a `]` immediately followed by `: [`, which never occurs
// inside an `items:[...]` array itself), and resolves each branch's tokens.
function extractSidebarNavConsts(sidebarSrc: string): { firm: string[]; inhouse: string[] } {
  const startIdx = sidebarSrc.indexOf('const navGroups')
  if (startIdx === -1) {
    throw new Error('G3: `const navGroups` anchor not found in Sidebar.tsx -- the anchors moved, update e2e/personas.test.ts')
  }
  const endIdx = sidebarSrc.indexOf('let activeNav', startIdx)
  if (endIdx === -1) {
    throw new Error('G3: `let activeNav` anchor not found after navGroups -- the anchors moved, update e2e/personas.test.ts')
  }
  const groupsSlice = sidebarSrc.slice(startIdx, endIdx)

  const parts = groupsSlice.split(/\]\s*:\s*\[/)
  if (parts.length !== 2) {
    throw new Error(`G3: expected exactly 2 parts split on the isFirm ternary seam, got ${parts.length} -- the anchors moved, update e2e/personas.test.ts`)
  }

  const aliasMap = buildSidebarAliasMap(sidebarSrc)
  return {
    firm: resolveNavConstsFromGroupsSlice(parts[0], aliasMap),
    inhouse: resolveNavConstsFromGroupsSlice(parts[1], aliasMap),
  }
}

// Labels from glyphs.tsx: `export const NAV_X: NavDef = { ... label: '...' ... }`, handling
// both the one-line and multi-line defs.
function extractGlyphLabels(glyphsSrc: string): Map<string, string> {
  const map = new Map<string, string>()
  for (const m of glyphsSrc.matchAll(/export const (NAV_[A-Z_]+):\s*NavDef\s*=\s*\{[\s\S]*?label:\s*['"]([^'"]+)['"]/g)) {
    map.set(m[1], m[2])
  }
  return map
}

// --- G13 extraction helpers (the three live product session gates) --------------------
//
// accepts() (personas.ts) reads PERSONAS[id].destination -- a field in THIS registry, hand
// -maintained alongside BOUNDARY_MATRIX in the same file. Row 10 (G5) cross-checks
// BOUNDARY_MATRIX against accepts(), and row 3 cross-checks accepts() against a hardcoded
// ACCEPTED set -- but every one of those three things (BOUNDARY_MATRIX, PERSONAS, ACCEPTED)
// lives in e2e/, typed by hand. None of them is read from the frontend session-gate source
// the registry claims to mirror (personas.ts:144-149's own comment: "Mirrors the three live
// product gates"). A registry that quietly drifted from shouldAutoSignIn / OPS_OPERATORS /
// SUPPORT_OPERATORS -- say a future frontend change widens or narrows who a destination
// accepts, with nobody remembering to update e2e/personas.ts -- would leave G5 and row 3
// green throughout, because both sides of every existing check are e2e-side data. These
// helpers read the actual frontend source, the same way G1-G3 read auth.ts/Sidebar.tsx.

// frontend/app/src/lib/session.ts's shouldAutoSignIn: `personaParam === 'x' || ...`. Slice
// the function body (declaration to the next column-0 `}`) and pull every string compared
// with `===`, rather than hardcoding {'firm','inhouse'} here -- a hardcoded set would just be
// a FOURTH hand-typed copy of the same claim.
function extractShouldAutoSignInIds(src: string): string[] {
  const startIdx = src.indexOf('function shouldAutoSignIn')
  if (startIdx === -1) {
    throw new Error('G13: `function shouldAutoSignIn` anchor not found in frontend/app/src/lib/session.ts -- the anchors moved, update e2e/personas.test.ts')
  }
  const endIdx = src.indexOf('\n}', startIdx)
  if (endIdx === -1) {
    throw new Error('G13: closing column-0 `}` not found after shouldAutoSignIn -- the anchors moved, update e2e/personas.test.ts')
  }
  const body = src.slice(startIdx, endIdx)
  const ids = [...body.matchAll(/===\s*['"]([^'"]+)['"]/g)].map((m) => m[1])
  if (ids.length === 0) {
    throw new Error('G13: no `=== \'...\'` comparisons found inside shouldAutoSignIn -- the anchors moved, update e2e/personas.test.ts')
  }
  return ids
}

// frontend/{ops,support}-console/src/session.ts's OPS_OPERATORS / SUPPORT_OPERATORS: a
// `{ id: { name: ..., org: ... } } as const` map. Slice the object body (declaration to the
// next column-0 `}`) and pull every top-level (2-space-indented) key.
function extractOperatorKeys(src: string, constName: string, srcLabel: string): string[] {
  const startIdx = src.indexOf(`export const ${constName}`)
  if (startIdx === -1) {
    throw new Error(`G13: \`export const ${constName}\` anchor not found in ${srcLabel} -- the anchors moved, update e2e/personas.test.ts`)
  }
  const endIdx = src.indexOf('\n}', startIdx)
  if (endIdx === -1) {
    throw new Error(`G13: closing column-0 \`}\` not found after ${constName} -- the anchors moved, update e2e/personas.test.ts`)
  }
  const body = src.slice(startIdx, endIdx)
  const keys = [...body.matchAll(/^ {2}(\w+):\s*\{/gm)].map((m) => m[1])
  if (keys.length === 0) {
    throw new Error(`G13: no top-level keys found inside ${constName} -- the anchors moved, update e2e/personas.test.ts`)
  }
  return keys
}

// --- G6c extraction helpers (e2e/personas.ts's own Grade union and Cell interface) -----
//
// A naive whole-file scan for "pending"/"planned"/"todo" goes RED on the UNMUTATED tree:
// personas.ts:63-66 is a COMMENT explaining that no such state exists, and a substring scan
// cannot tell a comment from a live field. Scoped instead to the two places a third grade
// could actually be expressed: the `Grade` type's own right-hand side, and the `Cell`
// interface's field list.

// `export type Grade = 'drives' | 'nav-only'` is a single line; slice its right-hand side
// and extract every quoted member.
function extractGradeUnionMembers(src: string): string[] {
  const match = src.match(/^export type Grade\s*=\s*(.+)$/m)
  if (!match) {
    throw new Error('G6c: `export type Grade =` anchor not found in e2e/personas.ts -- the anchors moved, update e2e/personas.test.ts')
  }
  return [...match[1].matchAll(/['"]([^'"]+)['"]/g)].map((m) => m[1])
}

// `export interface Cell { ... }`: slice from its declaration to the next column-0 `}` and
// pull every top-level field name.
function extractCellFieldNames(src: string): string[] {
  const startIdx = src.indexOf('export interface Cell {')
  if (startIdx === -1) {
    throw new Error('G6c: `export interface Cell {` anchor not found in e2e/personas.ts -- the anchors moved, update e2e/personas.test.ts')
  }
  const endIdx = src.indexOf('\n}', startIdx)
  if (endIdx === -1) {
    throw new Error('G6c: closing column-0 `}` not found after Cell -- the anchors moved, update e2e/personas.test.ts')
  }
  const body = src.slice(startIdx, endIdx)
  return [...body.matchAll(/^\s*(\w+):/gm)].map((m) => m[1])
}

// `export type PlatformCtx = { ... }`: the same declaration-to-column-0-`}` slice as
// extractCellFieldNames above, over types.ts. A05-8 needs the BODY rather than the whole
// file because three of the dead `filter` sites spell neither `ctx.filter` nor
// `setFilter` literally -- the member declaration itself is the only text they share.
function extractPlatformCtxMemberNames(typesSrc: string): string[] {
  const startIdx = typesSrc.indexOf('export type PlatformCtx = {')
  if (startIdx === -1) {
    throw new Error('A05-8: `export type PlatformCtx = {` anchor not found in types.ts -- the anchors moved, update e2e/personas.test.ts')
  }
  const endIdx = typesSrc.indexOf('\n}', startIdx)
  if (endIdx === -1) {
    throw new Error('A05-8: closing column-0 `}` not found after PlatformCtx -- the anchors moved, update e2e/personas.test.ts')
  }
  const body = typesSrc.slice(startIdx, endIdx)
  return [...body.matchAll(/^\s*(\w+)\??:/gm)].map((m) => m[1])
}

describe('personas.ts registry, sign-in seam, and guards (PERSONA-01-01, task-270)', () => {
  it('row 0 (AC-1) -- personas.ts imports nothing from @playwright/test', () => {
    const src = readFileSync(PERSONAS_SRC, 'utf8')
    expect(src.length, 'e2e/personas.ts has no content to scan').toBeGreaterThan(0)

    const fromImport = /from\s+['"]@playwright\/test['"]/
    const requireImport = /require\(\s*['"]@playwright\/test['"]\s*\)/
    expect(fromImport.test(src), "found `from '@playwright/test'` in e2e/personas.ts").toBe(false)
    expect(requireImport.test(src), "found `require('@playwright/test')` in e2e/personas.ts").toBe(false)
  })

  it('row 1 (AC-1) -- signInUrl builds the landing hand-off URL for each persona', () => {
    process.env.APP_URL = 'https://app.example.test///'
    process.env.OPS_CONSOLE_URL = 'https://ops.example.test/'
    process.env.SUPPORT_CONSOLE_URL = 'https://support.example.test'

    expect(signInUrl('firm')).toBe('https://app.example.test?persona=firm')
    expect(signInUrl('inhouse')).toBe('https://app.example.test?persona=inhouse')
    expect(signInUrl('developer')).toBe('https://ops.example.test?persona=developer')
    expect(signInUrl('support')).toBe('https://support.example.test?persona=support')
  })

  it('row 2 (AC-1) -- signInUrl throws naming the missing variable', () => {
    delete process.env.APP_URL
    expect(() => signInUrl('firm')).toThrow(/APP_URL/)
  })

  it('row 3 (AC-1) -- accepts mirrors the three product gates', () => {
    // Hardcoded, not read off PERSONA_IDS/DESTINATIONS -- those are registry exports and
    // must not be trusted as the source of the very pairs being used to test the registry
    // (an empty registry would otherwise make this loop run zero times and pass vacuously).
    const ALL_PERSONA_IDS: PersonaId[] = ['developer', 'support', 'firm', 'inhouse']
    const ALL_DESTINATIONS: Destination[] = ['app', 'ops', 'support']
    const ACCEPTED = new Set(['app:firm', 'app:inhouse', 'ops:developer', 'support:support'])

    expect(ACCEPTED.size, 'accepted pairs (vacuity guard)').toBe(4)

    const wrong: string[] = []
    for (const destination of ALL_DESTINATIONS) {
      for (const id of ALL_PERSONA_IDS) {
        const expected = ACCEPTED.has(`${destination}:${id}`)
        if (accepts(destination, id) !== expected) {
          wrong.push(`${destination}x${id}: expected ${expected}`)
        }
      }
    }
    expect(wrong, wrong.join('\n')).toEqual([])
  })

  it('row 4 (AC-4, G1) -- registry ids match LANDING_PERSONAS', () => {
    const ids = extractLandingPersonaIds(readFileSync(LANDING_AUTH, 'utf8'))
    expect(ids.length, 'landing persona ids extracted (vacuity guard)').toBeGreaterThanOrEqual(4)
    expect(new Set(PERSONA_IDS)).toEqual(new Set(ids))
  })

  it('row 5 (AC-4, G2) -- registry ids match the LandingPersona id union', () => {
    const members = extractLandingPersonaIdUnion(readFileSync(LANDING_AUTH, 'utf8'))
    expect(members.length, 'LandingPersona id union members extracted (vacuity guard)').toBeGreaterThanOrEqual(4)
    expect(new Set(PERSONA_IDS)).toEqual(new Set(members))
  })

  it('row 6 (AC-2, G3) -- every sidebar nav surface is catalogued', () => {
    const sidebarSrc = readFileSync(SIDEBAR, 'utf8')
    const glyphsSrc = readFileSync(GLYPHS, 'utf8')
    const { firm, inhouse } = extractSidebarNavConsts(sidebarSrc)
    const labels = extractGlyphLabels(glyphsSrc)
    const distinct = [...new Set([...firm, ...inhouse])]

    // Floors, not exact counts: NAV_AUDIT took firm to 11 and in-house to 9, and glyphs.tsx
    // now exports eleven NAV_* consts. Keeping these as >= is what lets a new nav item land
    // without editing this row -- the real assertions are the missing/mismatched lists below.
    expect(firm.length, 'firm nav items (vacuity guard)').toBeGreaterThanOrEqual(10)
    expect(inhouse.length, 'in-house nav items (vacuity guard)').toBeGreaterThanOrEqual(8)
    expect(distinct.length, 'distinct navConsts (vacuity guard)').toBeGreaterThanOrEqual(10)
    expect(labels.size, 'NAV_* -> label pairs from glyphs.tsx (vacuity guard)').toBeGreaterThanOrEqual(10)

    const missing: string[] = []
    const mismatched: string[] = []
    for (const navConst of distinct) {
      const entry = SURFACES.find((s) => s.navConst === navConst)
      if (!entry) {
        missing.push(navConst)
        continue
      }
      const expectedLabel = labels.get(navConst)
      if (expectedLabel !== undefined && entry.label !== expectedLabel) {
        mismatched.push(`${navConst}: SURFACES has "${entry.label}", glyphs.tsx says "${expectedLabel}"`)
      }
    }
    expect(missing, `navConsts missing from SURFACES: ${missing.join(', ')}`).toEqual([])
    expect(mismatched, `label mismatches: ${mismatched.join('; ')}`).toEqual([])
  })

  it('row 7 (AC-2, G3-neg) -- an uncatalogued nav surface fails the guard', () => {
    // Inline fixture exercising the SAME resolver as row 6, not a copy inlined into the G3
    // case -- proves the extractor itself (not just today's real Sidebar.tsx) tells a real
    // surface (NAV_DASHBOARD) apart from a fictional one (NAV_FAKE).
    const FIXTURE = `
      const navGroups = isFirm
        ? [
            { key: 'client', label: 'Acme', scope: 'CLIENT', items: [NAV_DASHBOARD, NAV_FAKE] },
          ]
        : [
            { key: 'workspace', label: 'Workspace', scope: 'Acme', items: [NAV_DASHBOARD] },
          ]
    `
    const resolved = resolveNavConstsFromGroupsSlice(FIXTURE, {})
    expect(resolved.length, 'fixture tokens resolved (vacuity guard)').toBeGreaterThanOrEqual(2)

    const uncatalogued = resolved.filter((navConst) => !SURFACES.some((s) => s.navConst === navConst))
    // Paired positive/negative: NAV_FAKE must be flagged, and a real catalogued surface
    // (NAV_DASHBOARD) must NOT be -- a guard that flags everything (e.g. because SURFACES is
    // empty) would satisfy a bare `toContain('NAV_FAKE')` without actually distinguishing
    // catalogued from uncatalogued, which is the exact failure this row exists to catch.
    expect(uncatalogued).toContain('NAV_FAKE')
    expect(uncatalogued).not.toContain('NAV_DASHBOARD')
  })

  it('row 8 (AC-3, G4) -- every coverage cell names a spec that exists and mentions it', () => {
    const cells = Object.values(PERSONAS).flatMap((p) => (p.coverage ?? []).map((cell) => ({ cell, specToken: p.specToken })))
    expect(cells.length, 'coverage cells found (vacuity guard)').toBeGreaterThanOrEqual(4)

    const failures: string[] = []
    for (const { cell, specToken } of cells) {
      // G4 checks that a cell's claim is filed against a REAL spec, at either grade -- the
      // existsSync + label + specToken checks below all run for `nav-only` exactly as they
      // do for `drives`, because a `nav-only` claim is just as capable of naming a file
      // that does not exist or that never mentions the surface. This used to hard-reject
      // any grade but 'drives', which was written when `drives` was the only grade in
      // production use; PERSONA-01-03 files the first real `nav-only` cells (firm x
      // Customers/Rules/Reports/Settings, covered by topology/persona-surfaces.spec.ts's
      // roster test), and that rejection would have turned them red for no defect.
      //
      // Pinning WHICH cells are permitted to be `nav-only` is deliberately NOT done here:
      // that is G6b's job in [PERSONA-01-07] (EXPECTED_NAV_ONLY). A second, weaker copy of
      // that guard living in this row would drift from it. LEGAL_GRADES mirrors the Grade
      // union in personas.ts -- no third `pending`/`planned` state ([no-pending-grade]).
      if (!LEGAL_GRADES.includes(cell.grade)) {
        failures.push(`${cell.navConst}: grade '${cell.grade}' is not one of ${LEGAL_GRADES.join('|')}`)
      }
      const specPath = join(REPO_ROOT, cell.coveredBy)
      if (!existsSync(specPath)) {
        failures.push(`${cell.navConst}: ${cell.coveredBy} does not exist`)
        continue
      }
      const specSrc = readFileSync(specPath, 'utf8')
      const surface = SURFACES.find((s) => s.navConst === cell.navConst)
      if (!surface) {
        failures.push(`${cell.navConst}: not present in SURFACES, cannot verify its label is mentioned`)
      } else if (!specSrc.includes(surface.label)) {
        failures.push(`${cell.navConst}: ${cell.coveredBy} does not mention label "${surface.label}"`)
      }
      if (!specSrc.includes(specToken)) {
        failures.push(`${cell.navConst}: ${cell.coveredBy} does not mention specToken "${specToken}"`)
      }
    }
    expect(failures, failures.join('\n')).toEqual([])
  })

  it('row 9 (AC-4, G4-vac) -- the coverage map is not empty', () => {
    const cells = Object.values(PERSONAS).flatMap((p) => p.coverage ?? [])
    expect(cells.length).toBeGreaterThanOrEqual(4)
  })

  it('row 10 (AC-5, G5) -- the boundary matrix classifies every cell exactly once', () => {
    expect(BOUNDARY_MATRIX.length, 'boundary matrix rows (vacuity guard)').toBe(12)

    const seen = new Set<string>()
    const duplicates: string[] = []
    const disagreements: string[] = []
    const badVerdicts: string[] = []
    for (const row of BOUNDARY_MATRIX) {
      const key = `${row.destination}:${row.persona}`
      if (seen.has(key)) duplicates.push(key)
      seen.add(key)

      if (row.verdict !== 'accepts' && row.verdict !== 'refuses') {
        badVerdicts.push(`${key}: verdict "${row.verdict}"`)
        continue
      }
      const expected = accepts(row.destination, row.persona)
      if ((row.verdict === 'accepts') !== expected) {
        disagreements.push(`${key}: matrix says ${row.verdict}, accepts() says ${expected}`)
      }
    }
    expect(duplicates, `duplicate (destination,persona) pairs: ${duplicates.join(', ')}`).toEqual([])
    expect(badVerdicts, badVerdicts.join('\n')).toEqual([])
    expect(disagreements, disagreements.join('\n')).toEqual([])
  })

  // --- QA-added coverage (task-270 Stage 4, Mode B): the one-way dependency rule --------
  //
  // Row 0 only guards the @playwright/test half of the HARD CONSTRAINT in task-270's
  // implementation plan: personas.ts must not transitively import topology/targets.ts or
  // smoke/apps.ts either, because BOTH resolve a deploy target at module scope and throw on
  // import when unset -- exactly what CI's env-less `test:unit` job hits. A regression here
  // fails silently at import time (a bare "Cannot find module" or "X_URL is not set" collection
  // error with no row-level attribution), so it is worth its own named assertion rather than
  // relying on the accidental blast radius of another row. personas.test.ts itself is checked
  // too: if a future edit re-imports either module directly into the test file (say, to reuse
  // a helper), the same import-time throw would take out every row in this file at once.
  it('row 11 (one-way dependency) -- personas.ts and personas.test.ts never import topology/targets or smoke/apps', () => {
    const personasSrc = readFileSync(PERSONAS_SRC, 'utf8')
    const testSrc = readFileSync(PERSONAS_TEST_SRC, 'utf8')
    expect(personasSrc.length, 'e2e/personas.ts has no content to scan').toBeGreaterThan(0)
    expect(testSrc.length, 'e2e/personas.test.ts has no content to scan').toBeGreaterThan(0)

    const FORBIDDEN = [
      { label: 'topology/targets', pattern: /from\s+['"][^'"]*\btopology\/targets['"]/ },
      { label: 'smoke/apps', pattern: /from\s+['"][^'"]*\bsmoke\/apps['"]/ },
    ]
    const files = [
      ['e2e/personas.ts', personasSrc],
      ['e2e/personas.test.ts', testSrc],
    ] as const

    const violations: string[] = []
    for (const [label, src] of files) {
      for (const { label: forbiddenLabel, pattern } of FORBIDDEN) {
        if (pattern.test(src)) violations.push(`${label} imports ${forbiddenLabel}`)
      }
    }
    expect(violations, violations.join('\n')).toEqual([])
  })

  // --- QA-added coverage (task-270 Stage 4, Mode B): the alias map survives a rename ----
  //
  // G3's alias map (buildSidebarAliasMap) is DERIVED from Sidebar.tsx's whole-file text
  // rather than hardcoded to the two names ('invoicesItem'/'approvalsItem') it happens to
  // resolve today -- task-270's plan calls this out explicitly as the reason a rename cannot
  // rot the guard. That claim was, until now, only ever exercised against today's real
  // variable names; this proves the SAME resolver derives the alias from arbitrarily-named
  // wrapper locals, independent of what Sidebar.tsx currently calls them.
  it('row 12 (G3 alias map) -- the alias map derivation survives a rename of the wrapper locals', () => {
    const FIXTURE = `
      const renamedInvoicesWrapper: SidebarNavItem = {
        ...NAV_INVOICES,
        badge: null,
      }
      const renamedApprovalsWrapper: SidebarNavItem = {
        ...NAV_APPROVALS,
        badge: null,
      }
      const navGroups = isFirm
        ? [
            { key: 'client', label: 'Acme', scope: 'CLIENT', items: [NAV_DASHBOARD, renamedInvoicesWrapper] },
          ]
        : [
            { key: 'workspace', label: 'Workspace', scope: 'Acme', items: [NAV_DASHBOARD, renamedApprovalsWrapper] },
          ]
    `
    const aliasMap = buildSidebarAliasMap(FIXTURE)
    expect(Object.keys(aliasMap).length, 'aliases derived from the fixture (vacuity guard)').toBeGreaterThanOrEqual(2)
    expect(aliasMap.renamedInvoicesWrapper, 'a renamed NAV_INVOICES wrapper should still resolve').toBe('NAV_INVOICES')
    expect(aliasMap.renamedApprovalsWrapper, 'a renamed NAV_APPROVALS wrapper should still resolve').toBe('NAV_APPROVALS')

    const parts = FIXTURE.split(/\]\s*:\s*\[/)
    expect(parts.length, 'fixture ternary seam split (vacuity guard)').toBe(2)
    const firmTokens = resolveNavConstsFromGroupsSlice(parts[0], aliasMap)
    const inhouseTokens = resolveNavConstsFromGroupsSlice(parts[1], aliasMap)
    expect(firmTokens, 'firm branch should resolve the renamed invoices wrapper').toContain('NAV_INVOICES')
    expect(inhouseTokens, 'in-house branch should resolve the renamed approvals wrapper').toContain('NAV_APPROVALS')
  })

  // --- QA-added coverage (task-271 Stage 4, Mode B): BOUNDARY_MATRIX vs the live gates ---
  //
  // G5 (row 10) proves BOUNDARY_MATRIX agrees with accepts(); accepts() just reads
  // PERSONAS[id].destination, a field in this SAME hand-maintained registry. So G5 (and row
  // 3's hardcoded ACCEPTED set) check the e2e-side data against itself, not against the
  // frontend session gates the registry claims to mirror. This row reads the three actual
  // gates -- shouldAutoSignIn, OPS_OPERATORS, SUPPORT_OPERATORS -- so a future change to any
  // of them that nobody mirrors into e2e/personas.ts fails HERE, not silently.
  it('row 13 (BOUNDARY_MATRIX vs the live product gates) -- the matrix cannot drift from shouldAutoSignIn/OPS_OPERATORS/SUPPORT_OPERATORS', () => {
    const appAccepted = new Set(extractShouldAutoSignInIds(readFileSync(APP_SESSION_SRC, 'utf8')))
    const opsAccepted = new Set(
      extractOperatorKeys(readFileSync(OPS_SESSION_SRC, 'utf8'), 'OPS_OPERATORS', 'frontend/ops-console/src/session.ts'),
    )
    const supportAccepted = new Set(
      extractOperatorKeys(readFileSync(SUPPORT_SESSION_SRC, 'utf8'), 'SUPPORT_OPERATORS', 'frontend/support-console/src/session.ts'),
    )

    expect(appAccepted.size, 'shouldAutoSignIn accepted ids (vacuity guard)').toBeGreaterThanOrEqual(2)
    expect(opsAccepted.size, 'OPS_OPERATORS keys (vacuity guard)').toBeGreaterThanOrEqual(1)
    expect(supportAccepted.size, 'SUPPORT_OPERATORS keys (vacuity guard)').toBeGreaterThanOrEqual(1)

    const PRODUCT_GATE: Record<Destination, Set<string>> = {
      app: appAccepted,
      ops: opsAccepted,
      support: supportAccepted,
    }

    expect(BOUNDARY_MATRIX.length, 'boundary matrix rows (vacuity guard)').toBe(12)
    const disagreements: string[] = []
    for (const row of BOUNDARY_MATRIX) {
      const productAccepts = PRODUCT_GATE[row.destination].has(row.persona)
      const matrixAccepts = row.verdict === 'accepts'
      if (productAccepts !== matrixAccepts) {
        disagreements.push(
          `${row.destination}:${row.persona}: matrix says "${row.verdict}", live product gate says "${productAccepts ? 'accepts' : 'refuses'}"`,
        )
      }
    }
    expect(disagreements, disagreements.join('\n')).toEqual([])
  })

  // Negative control for G13's extractors: proves they can tell an accepted id apart from a
  // rejected one, rather than (say) matching every quoted string in the file regardless of
  // context. Exercises the SAME functions as row 13, over inline fixtures -- not a copy.
  it('row 14 (G13-neg) -- the live-gate extractors distinguish an accepted id from a rejected one', () => {
    // Column-0 braces, matching the real files' shape (both extractors slice to the next
    // column-0 `}`) -- an indented fixture would silently miss its own closing brace.
    const authFixture = [
      'export function shouldAutoSignIn(personaParam: string | null): boolean {',
      "  return personaParam === 'alpha' || personaParam === 'beta'",
      '}',
    ].join('\n')
    const ids = extractShouldAutoSignInIds(authFixture)
    expect(ids).toContain('alpha')
    expect(ids).toContain('beta')
    expect(ids).not.toContain('gamma')

    const operatorsFixture = [
      'export const FAKE_OPERATORS = {',
      "  alpha: { name: 'Alpha', org: 'Test' },",
      '} as const',
    ].join('\n')
    const keys = extractOperatorKeys(operatorsFixture, 'FAKE_OPERATORS', 'fixture')
    expect(keys).toContain('alpha')
    expect(keys).not.toContain('beta')
  })

  // --- G6 (PERSONA-01-07, task-276): the guard that closes the door on Core AC 1 --------
  //
  // G6 guards data subtasks 01-06 already landed on this branch, so it is GREEN on the
  // unmutated tree today (17 rendered pairs == 17 coverage cells) -- a trivially-failing
  // import would prove nothing here. `Test-first: yes` is discharged by MUTATION-VERIFY
  // instead: each assertion below was proven by making the exact break it exists to catch,
  // confirming RED with the right message, then reverting. See task-276's implementation
  // notes for the captured output of all seven mutations.

  it('row 15 (G6a) -- the rendered (surface, persona) pairs and the coverage cells are the same set', () => {
    const sidebarSrc = readFileSync(SIDEBAR, 'utf8')
    const { firm, inhouse } = extractSidebarNavConsts(sidebarSrc)

    // Persona<->mode mapping is a HARDCODED LITERAL, never read off PERSONA_IDS -- row 3's
    // established reason: registry exports must not supply the pairs used to test the
    // registry.
    const rendered = new Set<string>([
      ...firm.map((navConst) => `firm:${navConst}`),
      ...inhouse.map((navConst) => `inhouse:${navConst}`),
    ])

    // Vacuity first, so a broken extraction diagnoses itself before the coverage diff does.
    // APPR-12-05 (task-530): firm 10 + in-house 8 = 18 distinct rendered pairs.
    expect(firm.length, 'firm nav items (vacuity guard)').toBeGreaterThanOrEqual(10)
    expect(inhouse.length, 'in-house nav items (vacuity guard)').toBeGreaterThanOrEqual(8)
    expect(rendered.size, 'rendered (surface, persona) pairs (vacuity guard)').toBeGreaterThanOrEqual(18)

    // Flattened over ALL FOUR personas (not just the two app ones): a stray cell filed
    // against `developer`/`support` -- which render no sidebar at all -- must surface as an
    // `extra` rather than being excluded from the comparison by construction.
    const celled: string[] = []
    for (const id of PERSONA_IDS) {
      for (const cell of PERSONAS[id].coverage ?? []) {
        celled.push(`${id}:${cell.navConst}`)
      }
    }

    const celledSet = new Set(celled)
    const uncovered = [...rendered].filter((key) => !celledSet.has(key))
    expect(uncovered, `rendered pairs with no coverage cell: ${uncovered.join(', ')}`).toEqual([])

    const extra = celled.filter((key) => !rendered.has(key))
    expect(extra, `coverage cells naming a pair the sidebar does not render: ${extra.join(', ')}`).toEqual([])

    const duplicateKeys = [...new Set(celled.filter((key, i) => celled.indexOf(key) !== i))]
    expect(duplicateKeys, `duplicate coverage cell keys: ${duplicateKeys.join(', ')}`).toEqual([])
  })

  it('row 16 (G6b) -- the nav-only set equals EXPECTED_NAV_ONLY exactly', () => {
    // 3 = firm:NAV_RULES plus both Audit cells. Drops back to 1 when audit.spec.ts drives them.
    expect(EXPECTED_NAV_ONLY.size, 'EXPECTED_NAV_ONLY entries (vacuity guard)').toBe(1)

    const actualNavOnly = new Set<string>()
    for (const id of PERSONA_IDS) {
      for (const cell of PERSONAS[id].coverage ?? []) {
        if (cell.grade === 'nav-only') actualNavOnly.add(`${id}:${cell.navConst}`)
      }
    }

    // Two directions, reported separately: an unexpected nav-only cell is a silent
    // downgrade; an EXPECTED_NAV_ONLY entry missing from the actual set is a promotion that
    // left the guard's own expectations stale.
    const unexpectedNavOnly = [...actualNavOnly].filter((key) => !EXPECTED_NAV_ONLY.has(key))
    expect(unexpectedNavOnly, `graded nav-only but not in EXPECTED_NAV_ONLY: ${unexpectedNavOnly.join(', ')}`).toEqual([])

    const missingFromActual = [...EXPECTED_NAV_ONLY].filter((key) => !actualNavOnly.has(key))
    expect(missingFromActual, `in EXPECTED_NAV_ONLY but not graded nav-only: ${missingFromActual.join(', ')}`).toEqual([])
  })

  it('row 17 (G6b) -- the named drives minimum holds', () => {
    expect(EXPECTED_DRIVES_MIN.size, 'EXPECTED_DRIVES_MIN entries (vacuity guard)').toBeGreaterThanOrEqual(10)

    const actualDrives = new Set<string>()
    for (const id of PERSONA_IDS) {
      for (const cell of PERSONAS[id].coverage ?? []) {
        if (cell.grade === 'drives') actualDrives.add(`${id}:${cell.navConst}`)
      }
    }

    const notDriven = [...EXPECTED_DRIVES_MIN].filter((key) => !actualDrives.has(key))
    expect(notDriven, `the Core AC 2/3/4 floor requires drives: ${notDriven.join(', ')}`).toEqual([])
  })

  it('row 18 (G6c) -- the coverage map admits no third grade', () => {
    const src = readFileSync(PERSONAS_SRC, 'utf8')

    const gradeMembers = extractGradeUnionMembers(src)
    expect(gradeMembers.length, 'Grade union members (vacuity guard)').toBe(2)
    expect(new Set(gradeMembers)).toEqual(new Set(LEGAL_GRADES))

    const fieldNames = extractCellFieldNames(src)
    expect(fieldNames.length, 'Cell interface fields (vacuity guard)').toBe(3)
    expect(new Set(fieldNames)).toEqual(new Set(['navConst', 'grade', 'coveredBy']))
  })

  // --- APPR-12-05 (Backlog task-530, Mode A) -- the nav flip's catalogue guards ----------
  //
  // Floors already raised in rows 6/15/17 above cover the general vacuity counts. These
  // rows (A05-1 through A05-9, the story's own spec ids) check the SPECIFIC firm:
  // NAV_APPROVALS transition. Authored against the CURRENT Sidebar.tsx/App.tsx/types.ts --
  // this commit does not touch them -- so A05-1/A05-2/A05-7/A05-8 fail on their target
  // assertion today; the executor's source edits turn them green. A05-3/A05-9 are
  // green-before guards that must stay green throughout. A05-4 pins the e2e/personas.ts
  // registry cell this same commit adds, so it is green from this commit on. A05-5/A05-6
  // (the exact firm/in-house roster) are NOT new tests here -- they are the existing
  // `sidebarRoster()` `.toEqual(FIRM_ROSTER)` / `.toEqual(INHOUSE_ROSTER)` assertions in
  // e2e/topology/persona-surfaces.spec.ts's "sidebar roster" test, which now run against
  // the raised FIRM_ROSTER constant; that spec is Playwright-only and cannot run locally.

  it('A05-1 -- Sidebar.tsx still parses and the firm branch resolves the new NAV_APPROVALS token', () => {
    const sidebarSrc = readFileSync(SIDEBAR, 'utf8')
    // extractSidebarNavConsts throws if the G3 anchors ('const navGroups' / 'let activeNav')
    // moved -- a parse failure and a missing-token failure are different defects, and this
    // row only asserts the latter once the former has provably not happened.
    const { firm, inhouse } = extractSidebarNavConsts(sidebarSrc)
    expect(firm.length, 'firm nav items parsed (vacuity guard)').toBeGreaterThan(0)
    expect(firm, 'the firm CLIENT group must resolve NAV_APPROVALS').toContain('NAV_APPROVALS')
    expect(inhouse, 'in-house already resolves NAV_APPROVALS (unchanged by this story)').toContain('NAV_APPROVALS')
  })

  it('A05-2 -- firm:NAV_APPROVALS is bidirectionally covered: rendered and celled agree', () => {
    const sidebarSrc = readFileSync(SIDEBAR, 'utf8')
    const { firm } = extractSidebarNavConsts(sidebarSrc)
    const rendered = firm.includes('NAV_APPROVALS')
    const celled = (PERSONAS.firm.coverage ?? []).some((c) => c.navConst === 'NAV_APPROVALS')

    // Both directions checked separately, mirroring G6a's own uncovered/extra split (row
    // 15) -- a failure here names WHICH side is missing rather than a bare mismatch.
    expect(celled, 'e2e/personas.ts must already claim firm:NAV_APPROVALS coverage (this commit adds the cell)').toBe(true)
    expect(rendered, 'Sidebar.tsx must render NAV_APPROVALS in the firm branch').toBe(true)
  })

  it('A05-3 -- nav-only did not grow: the new firm cell is graded drives, not nav-only (green-before guard)', () => {
    // 3 = firm:NAV_RULES plus both Audit cells. Drops back to 1 when audit.spec.ts drives them.
    expect(EXPECTED_NAV_ONLY.size, 'EXPECTED_NAV_ONLY entries (vacuity guard)').toBe(1)
    expect(EXPECTED_NAV_ONLY.has('firm:NAV_APPROVALS'), 'firm:NAV_APPROVALS must never enter the nav-only set').toBe(false)

    const cell = (PERSONAS.firm.coverage ?? []).find((c) => c.navConst === 'NAV_APPROVALS')
    expect(cell?.grade, 'the new firm cell must be graded drives, not nav-only (Decision V4)').toBe('drives')
  })

  it('A05-4 -- the firm NAV_APPROVALS coverage cell is named exactly (Decision V4)', () => {
    const cell = (PERSONAS.firm.coverage ?? []).find((c) => c.navConst === 'NAV_APPROVALS')
    expect(cell, 'e2e/personas.ts firm coverage must carry a NAV_APPROVALS cell').toBeDefined()
    // Pointed at persona-surfaces.spec.ts, NOT invoice-surfaces.spec.ts (which does not
    // cover it until task-532) -- the same-commit rule at personas.ts:108 without a
    // forward reference (architect sweep V4).
    expect(cell?.grade).toBe('drives')
    expect(cell?.coveredBy).toBe('e2e/topology/persona-surfaces.spec.ts')
  })

  it('A05-7 -- the NavId alias type is gone from types.ts and its App.tsx import', () => {
    const typesSrc = readFileSync(TYPES_TS, 'utf8')
    const appSrc = readFileSync(APP_TSX, 'utf8')
    expect(typesSrc.length, 'types.ts has no content to scan').toBeGreaterThan(0)
    expect(appSrc.length, 'App.tsx has no content to scan').toBeGreaterThan(0)

    expect(/\bNavId\b/.test(typesSrc), 'types.ts still references NavId').toBe(false)
    expect(/\bNavId\b/.test(appSrc), 'App.tsx still references NavId').toBe(false)
  })

  it('A05-8 -- ctx.filter and setFilter are gone from the App/Sidebar/types surface', () => {
    // TWO halves, because neither alone sees all eleven deleted sites.
    //
    // Half 1, ANCHORED: the PlatformCtx body itself. `types.ts`'s `filter: string`,
    // `Sidebar.tsx`'s `const { ..., filter } = ctx` destructure and `Sidebar.test.tsx`'s
    // `filter: ''` fixture field spell neither `ctx.filter` nor `setFilter` literally, so
    // half 2 cannot see them -- but delete the MEMBER and the first two stop compiling.
    // Anchored to the type body, so it can never match a `.filter(` call or a CSS
    // `filter:` value elsewhere in the file (architect sweep V7).
    const ctxMembers = extractPlatformCtxMemberNames(readFileSync(TYPES_TS, 'utf8'))
    expect(ctxMembers.length, 'PlatformCtx members parsed (vacuity guard)').toBeGreaterThan(20)
    expect(ctxMembers, 'PlatformCtx must carry no `filter` member').not.toContain('filter')
    expect(ctxMembers, 'PlatformCtx must carry no `setFilter` member').not.toContain('setFilter')

    // Half 2, LITERAL: scoped to \bsetFilter\b and ctx\.filter\b ONLY -- a bare `filter`
    // scan drowns in `.filter(` array calls and CSS `filter:` values (30+ legitimate hits
    // under frontend/app/src).
    const SET_FILTER = /\bsetFilter\b/
    const CTX_FILTER = /ctx\.filter\b/
    const files: readonly [string, string][] = [
      ['frontend/app/src/types.ts', TYPES_TS],
      ['frontend/app/src/App.tsx', APP_TSX],
      ['frontend/app/src/components/Sidebar.tsx', SIDEBAR],
      ['frontend/app/src/components/Sidebar.test.tsx', SIDEBAR_TEST],
    ]

    const hits: string[] = []
    for (const [label, path] of files) {
      const src = readFileSync(path, 'utf8')
      if (SET_FILTER.test(src)) hits.push(`${label}: setFilter`)
      if (CTX_FILTER.test(src)) hits.push(`${label}: ctx.filter`)
    }
    expect(hits, `dead filter members still present: ${hits.join(', ')}`).toEqual([])
  })

  it('A05-8-neg -- the setFilter/ctx.filter scan does not false-positive on .filter( calls or CSS filter: values (non-vacuity control)', () => {
    const FIXTURE = `
      const list = items.filter((x) => x.active)
      const style = { filter: 'none' }
      const clients = ctx.entities.filter((c) => c.entityId != null)
    `
    expect(/\bsetFilter\b/.test(FIXTURE), 'must not fire on .filter( calls').toBe(false)
    expect(/ctx\.filter\b/.test(FIXTURE), 'must not fire on unrelated ctx.<x>.filter( chains').toBe(false)

    // Paired positive: the same two patterns DO fire on the real dead members, so the
    // scan is not vacuously always-false.
    const POSITIVE = `const [filter, setFilter] = useState('all')\nctx.filter === 'Pending'`
    expect(/\bsetFilter\b/.test(POSITIVE)).toBe(true)
    expect(/ctx\.filter\b/.test(POSITIVE)).toBe(true)

    // The anchored half's own control: it must SEE a reinstated member, and must not be
    // fooled by a `.filter(` call or a CSS `filter:` inside the same body.
    const DIRTY = "export type PlatformCtx = {\n  view: View\n  filter: string\n  setFilter: (f: string) => void\n}\n"
    expect(extractPlatformCtxMemberNames(DIRTY)).toEqual(['view', 'filter', 'setFilter'])
    const CLEAN = "export type PlatformCtx = {\n  view: View\n  rows: Row[] // rows.filter((r) => r.ok)\n  style: { filter: 'none' }\n}\n"
    expect(extractPlatformCtxMemberNames(CLEAN)).toEqual(['view', 'rows', 'style'])
  })

  it('A05-9 -- Sidebar.tsx keeps the `let activeNav` slice anchor G3 depends on (green-before guard)', () => {
    // After Sidebar.tsx:125's override is deleted, `let activeNav` is never reassigned --
    // but it must KEEP its `let` binding and exact text, because G3 (row 6 above) slices
    // Sidebar.tsx from indexOf('const navGroups') to indexOf('let activeNav'). A
    // `prefer-const` tidy-up would silently break that anchor, and this repo ships no
    // ESLint config to catch it (architect sweep V8/notes).
    const sidebarSrc = readFileSync(SIDEBAR, 'utf8')
    expect(/\blet\s+activeNav\b/.test(sidebarSrc), 'Sidebar.tsx must declare `let activeNav` -- G3 slices on this exact anchor').toBe(true)
    expect(/\bconst\s+activeNav\b/.test(sidebarSrc), 'a `prefer-const` tidy-up must not rename this to `const activeNav`').toBe(false)
  })

  it('A05-9-neg -- the activeNav anchor check tells `let` apart from `const` (non-vacuity control)', () => {
    const LET_FIXTURE = "let activeNav: string = view === 'create' ? 'invoices' : view"
    const CONST_FIXTURE = "const activeNav: string = view === 'create' ? 'invoices' : view"
    expect(/\blet\s+activeNav\b/.test(LET_FIXTURE)).toBe(true)
    expect(/\bconst\s+activeNav\b/.test(LET_FIXTURE)).toBe(false)
    expect(/\blet\s+activeNav\b/.test(CONST_FIXTURE)).toBe(false)
    expect(/\bconst\s+activeNav\b/.test(CONST_FIXTURE)).toBe(true)
  })

  // --- EXTR-09-08 (task-775) -- the document fork moves no coverage ----------------------
  //
  // The wizard's document path is a STEP inside the create flow, not a nav destination, so
  // this story must leave the catalogue and the coverage map exactly where it found them.
  // G3/G6 above go red either way; this row states the claim rather than leaving it implied,
  // so a later story that quietly adds a Documents surface meets a named expectation.
  it('EXTR09-P-1 -- the document fork adds no nav surface and no coverage cell', () => {
    // Hand-written literal, never SURFACES.map(...) -- row 3's rule: registry exports must
    // not supply the expectation used to test the registry.
    expect(SURFACES.map((s) => s.navConst), 'the app SPA nav catalogue is unchanged by EXTR-09').toEqual([
      'NAV_DASHBOARD',
      'NAV_INVOICES',
      'NAV_VALIDATION',
      'NAV_WORKFLOWS',
      'NAV_RULES',
      'NAV_APPROVALS',
      'NAV_CUSTOMERS',
      'NAV_REPORTS',
      'NAV_CLIENTS',
      'NAV_AUDIT',
      'NAV_SETTINGS',
    ])

    const cells = PERSONA_IDS.flatMap((id) => PERSONAS[id].coverage ?? [])
    expect(cells.length, 'coverage cells across all four personas (vacuity guard)').toBe(20)

    // The fork's own vocabulary, in either half of the map. `Import` is deliberately absent
    // from this list: the wizard has always been reached from Invoices, and no surface is
    // named for it.
    const forkWords = /document|extract|upload/i
    const surfaceHits = SURFACES.filter((s) => forkWords.test(s.navConst) || forkWords.test(s.label))
    expect(surfaceHits, `EXTR-09 must add no nav surface: ${surfaceHits.map((s) => s.navConst).join(', ')}`).toEqual([])
    const cellHits = cells.filter((c) => forkWords.test(c.navConst))
    expect(cellHits, `EXTR-09 must add no coverage cell: ${cellHits.map((c) => c.navConst).join(', ')}`).toEqual([])
  })

  it('EXTR09-P-1-neg -- the fork-word scan fires on a surface that would move the map (non-vacuity control)', () => {
    const forkWords = /document|extract|upload/i
    expect(forkWords.test('NAV_DOCUMENTS'), 'the scan must see a Documents surface').toBe(true)
    expect(forkWords.test('Extraction'), 'the scan must see an Extraction label').toBe(true)
    expect(forkWords.test('NAV_INVOICES'), 'and must not fire on the shipped catalogue').toBe(false)
  })
})
