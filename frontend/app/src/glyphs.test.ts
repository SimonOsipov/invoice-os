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
import type { ReactElement } from 'react'

import * as glyphs from './glyphs'

// The value the story pins NAV_ICON_SIZE to (AC-1) — a LOCAL literal, not an import, since
// the real export doesn't exist yet. Once the executor adds `export const NAV_ICON_SIZE =
// 17`, this assertion still passes unchanged: it never depended on the symbol, only on the
// value.
const EXPECTED_NAV_ICON_SIZE = 17

const NAV_DEFS: glyphs.NavDef[] = [
  glyphs.NAV_DASHBOARD,
  glyphs.NAV_INVOICES,
  glyphs.NAV_VALIDATION,
  glyphs.NAV_WORKFLOWS,
  glyphs.NAV_RULES,
  glyphs.NAV_CLIENTS,
  glyphs.NAV_APPROVALS,
  glyphs.NAV_CUSTOMERS,
  glyphs.NAV_REPORTS,
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
    // nav item is wrong (e.g. "clients"/"validation") instead of a bare index.
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
