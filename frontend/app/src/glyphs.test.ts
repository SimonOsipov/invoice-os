// RED specs (task-327, BUG-01-01, AC-1..AC-3, Test-first) — pin the shared nav icon size
// and icon-column style before the executor adds NAV_ICON_SIZE / navIconColStyle to
// glyphs.tsx. Neither export exists yet: importing either by name would fail typecheck
// (tsc --noEmit over src/, tsconfig include: ["src"]) at MODULE LOAD — a compile error,
// which this repo's convention rejects as a valid RED reason. Every assertion below reads
// through what already exists instead: the ten live NAV_* exports (direct), and the glyphs
// module's own namespace object (for the not-yet-existing navIconColStyle, cast through an
// index signature so a missing key resolves to `undefined` at runtime rather than failing
// to typecheck). So each failure here is an ASSERTION failure (wrong size, or an undefined
// style object), never an import/compile error.
import { describe, expect, it } from 'vitest'
import { createElement, type ReactElement } from 'react'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

import * as glyphs from './glyphs'
import { Icon } from './icons'

// The value the story pins NAV_ICON_SIZE to (AC-1) — a LOCAL literal, not an import, since
// the real export doesn't exist yet. Once the executor adds `export const NAV_ICON_SIZE =
// 17`, this assertion still passes unchanged: it never depended on the symbol, only on the
// value.
const EXPECTED_NAV_ICON_SIZE = 17

const NAV_DEFS: glyphs.NavDef[] = [
  glyphs.NAV_DASHBOARD,
  glyphs.NAV_INVOICES,
  glyphs.NAV_WORKFLOWS,
  glyphs.NAV_RULES,
  glyphs.NAV_CLIENTS,
  glyphs.NAV_APPROVALS,
  glyphs.NAV_CUSTOMERS,
  glyphs.NAV_REPORTS,
  glyphs.NAV_AUDIT,
  glyphs.NAV_SETTINGS,
]

// .props.size is readable off the pre-built JSX element with no mounting (icons.tsx:14's
// Icon is a plain function component). size defaults to 16 when omitted from the JSX, so an
// element authored without an explicit size reads `undefined` here — read raw, never
// coalesced, so a glyph that drops its size prop fails loudly instead of quietly matching
// the default.
function glyphSize(def: glyphs.NavDef): number | undefined {
  return (def.glyph as ReactElement<{ size?: number }>).props.size
}

describe('nav glyph sizes (task-327 AC-1)', () => {
  it('every NAV_* constant glyph renders at the shared nav icon size', () => {
    // Keyed by def.id rather than a plain array so a mismatch's toEqual diff names WHICH
    // nav item is wrong (e.g. "clients"/"reports") instead of a bare index.
    const actual = Object.fromEntries(NAV_DEFS.map((def) => [def.id, glyphSize(def)]))
    const expected = Object.fromEntries(NAV_DEFS.map((def) => [def.id, EXPECTED_NAV_ICON_SIZE]))
    expect(actual).toEqual(expected)
  })
})

describe('navIconColStyle (task-327 AC-3)', () => {
  it('is at least as wide as the glyph and centres it', () => {
    const navIconColStyle = (
      glyphs as unknown as Record<string, { width?: number; flex?: string; justifyContent?: string } | undefined>
    ).navIconColStyle
    expect(navIconColStyle, 'navIconColStyle is not exported by glyphs.tsx yet').toBeDefined()
    expect(navIconColStyle!.width).toBeGreaterThanOrEqual(EXPECTED_NAV_ICON_SIZE)
    expect(navIconColStyle!.flex).toBe('none')
    expect(navIconColStyle!.justifyContent).toBe('center')
  })
})

describe('shieldGlyph ([nav-glyphs-forked-not-resized], task-327 AC-2)', () => {
  it('is not resized by the nav normalisation', () => {
    expect((glyphs.shieldGlyph as ReactElement<{ size?: number }>).props.size).toBe(16)
  })
})

// Adversarial coverage added post-GREEN (QA, task-327). The RED tests above pin sizes
// against a LOCAL literal rather than the (then nonexistent) NAV_ICON_SIZE export; these
// import the real symbols now that they're safe to, closing gaps the literal-pinned tests
// don't cover.

describe('NAV_ICON_COL / NAV_ICON_SIZE invariant', () => {
  it('the icon column is never smaller than the glyph it holds', () => {
    // Ties the two real exports directly, unlike the size test above (pinned to a local
    // literal) -- if NAV_ICON_SIZE ever grows past NAV_ICON_COL this fails on its own,
    // without depending on any glyph's size drifting out of step first.
    expect(glyphs.NAV_ICON_COL).toBeGreaterThanOrEqual(glyphs.NAV_ICON_SIZE)
  })
})

describe('nav labels stay byte-identical to main (task-327 out-of-scope guard, AC-6)', () => {
  it('every NAV_* label matches its pre-fix value exactly', () => {
    // e2e/personas.test.ts G3 only cross-checks glyphs.tsx against Sidebar.tsx's OWN
    // navGroups -- Sidebar spreads NavDef.label straight through, so the two can never
    // disagree with each other regardless of what the text says. This pins the actual
    // strings, so a renamed label (in glyphs.tsx AND Sidebar, kept "consistent") still fails.
    const EXPECTED_LABELS: Record<string, string> = {
      dashboard: 'Overview',
      invoices: 'Invoices',
      workflows: 'Workflows',
      rules: 'Rules',
      clients: 'Clients',
      approvals: 'Approvals',
      customers: 'Customers',
      reports: 'Reports',
      audit: 'Audit',
      settings: 'Settings',
    }
    const actual = Object.fromEntries(NAV_DEFS.map((def) => [def.id, def.label]))
    expect(actual).toEqual(EXPECTED_LABELS)
  })
})

describe('glyphSize() reads the raw prop, not Icon\'s size=16 default', () => {
  it('returns undefined for a glyph authored without an explicit size', () => {
    // createElement(), not JSX -- this is a .ts file. Mirrors what an under-specified
    // `<Icon paths={[...]} />` in glyphs.tsx would produce: no `size` key in props at all.
    const noSizeGlyph: glyphs.NavDef = { id: 'dashboard', label: 'Overview', glyph: createElement(Icon, { paths: ['M0 0'] }) }
    expect(glyphSize(noSizeGlyph)).toBeUndefined()
  })

  it('every live NAV_* glyph carries an explicit size prop', () => {
    // Redundant with the AC-1 equality test today (undefined !== 17 already fails it), but
    // pinned independently so a future edit to that test can't silently drop the guarantee.
    for (const def of NAV_DEFS) {
      expect(glyphSize(def), `${def.id}'s glyph has no explicit size prop`).toBeDefined()
    }
  })
})

describe('Sidebar wires navIconColStyle onto the icon wrapper, not just defines it (task-327 AC-3)', () => {
  const SIDEBAR_SRC = readFileSync(join(dirname(fileURLToPath(import.meta.url)), 'components/Sidebar.tsx'), 'utf8')

  it('the nav icon span spreads navIconColStyle', () => {
    // Structural guard only. navIconColStyle could exist with the right shape (tests above)
    // and still be a constant nobody uses -- this fails that case even though it can't
    // prove the resulting geometry; that half is persona-surfaces.spec.ts Test 5's job
    // (no DOM layer here to render the span and measure it, docs/e2e-convention.md:71-73).
    expect(SIDEBAR_SRC).toMatch(/<span style=\{\{\s*\.\.\.navIconColStyle/)
  })
})
