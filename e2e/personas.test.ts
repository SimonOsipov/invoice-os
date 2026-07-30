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

const REPO_ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const LANDING_AUTH = join(REPO_ROOT, 'frontend/landing/src/auth.ts')
const SIDEBAR = join(REPO_ROOT, 'frontend/app/src/components/Sidebar.tsx')
const GLYPHS = join(REPO_ROOT, 'frontend/app/src/glyphs.tsx')
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

    expect(firm.length, 'firm nav items (vacuity guard)').toBeGreaterThanOrEqual(9)
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
})
