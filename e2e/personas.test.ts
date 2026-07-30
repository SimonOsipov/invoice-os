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
  type PersonaId,
  type Destination,
} from './personas'

const REPO_ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const LANDING_AUTH = join(REPO_ROOT, 'frontend/landing/src/auth.ts')
const SIDEBAR = join(REPO_ROOT, 'frontend/app/src/components/Sidebar.tsx')
const GLYPHS = join(REPO_ROOT, 'frontend/app/src/glyphs.tsx')
const PERSONAS_SRC = join(REPO_ROOT, 'e2e/personas.ts')

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
      if (cell.grade !== 'drives') {
        failures.push(`${cell.navConst}: expected grade 'drives', got '${cell.grade}'`)
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
})
