// Structural invariants DEMO-06-03's Object.values(copy) scan depends on: every STRING
// export is a non-empty string, never a function -- a template function would escape the
// scan. DEMO-06-05 adds BUSY_MS/TOAST_MS (numeric timing, not rendered copy) beside the
// strings they time -- excluded from both scans below by type, not by name.
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

import * as copy from './copy'

describe('demo copy shape', () => {
  const entries = Object.entries(copy).filter(([, value]) => typeof value === 'string')

  it('exports at least one string', () => {
    expect(entries.length).toBeGreaterThan(0)
  })

  it.each(entries)('%s is a non-empty string', (_name, value) => {
    expect(typeof value).toBe('string')
    expect((value as string).length).toBeGreaterThan(0)
  })
})

// AC-8: no switcher string reads as account/session vocabulary. Two exports are excluded
// by name -- see the reason beside each -- and the exclusion list itself is pinned to
// length 2 so a future export cannot be quietly added to it (orchestrator correction,
// Implementation Notes on task-590/591).
describe('no switcher string uses account or session vocabulary (AC-8)', () => {
  const BANNED = /sign in|signin|password|e-?mail|account|log ?in|session/i

  const EXCLUDED: readonly string[] = [
    // The design's own NEGATION -- "This is not account switching -- no password, no
    // email" -- necessarily contains the banned words because it is telling the
    // presenter this is NOT account switching.
    'POPOVER_NOTE',
    // Invoice-detail copy (DEMO-06-06), not switcher copy.
    'BLOCKED_BY_ROLE_PREFIX',
  ]

  it('excludes exactly two exports, both named and reasoned above', () => {
    expect(EXCLUDED.length).toBe(2)
  })

  const scanned = Object.entries(copy).filter(([name, value]) => !EXCLUDED.includes(name) && typeof value === 'string')

  it('scans at least 12 exports', () => {
    expect(scanned.length).toBeGreaterThanOrEqual(12)
  })

  it.each(scanned)('%s carries no account/session vocabulary', (_name, value) => {
    expect(value as string).not.toMatch(BANNED)
  })
})

// Row 2 (AC-3, fence). No roster literal exists under src/demo at HEAD, so this cannot
// go red today -- a regression fence, not a red-first oracle (row 1 in
// PersonaPopover.test.tsx is AC-3's red-first coverage).
describe('no roster literal lives under src/demo (AC-3 fence)', () => {
  const DEMO_DIR = dirname(fileURLToPath(import.meta.url))
  const SEEDED_NAMES = ['Chinedu Okafor', 'Musa Danjuma', 'Zainab Lawal', 'Halima Yusuf']

  function nonTestSourceFiles(ext: RegExp): string[] {
    return readdirSync(DEMO_DIR).filter((f) => ext.test(f) && !f.includes('.test.'))
  }

  const files = nonTestSourceFiles(/\.tsx?$/)

  it('walks at least 5 source files', () => {
    expect(files.length).toBeGreaterThanOrEqual(5)
  })

  it.each(files)('%s carries no seeded display name', (file) => {
    const contents = readFileSync(join(DEMO_DIR, file), 'utf8')
    for (const name of SEEDED_NAMES) {
      expect(contents).not.toContain(name)
    }
  })
})

// Row 7 (AC-5, fence). Nothing under src/demo mentions 'invited' at HEAD -- a regression
// fence; row 18 in PersonaPopover.test.tsx is the behavioural red-first half. Widened to
// *.{ts,tsx}, matching row 2's sibling roster-literal fence -- *.tsx alone missed a plant
// in a .ts file (flag.ts/copy.ts/identity.ts are half the directory).
describe('no invited branch exists (AC-5 fence)', () => {
  const DEMO_DIR = dirname(fileURLToPath(import.meta.url))
  const files = readdirSync(DEMO_DIR).filter((f) => /\.tsx?$/.test(f) && !f.includes('.test.'))

  it('walks at least 5 source files', () => {
    expect(files.length).toBeGreaterThanOrEqual(5)
  })

  it.each(files)('%s contains no "invited" occurrence', (file) => {
    const contents = readFileSync(join(DEMO_DIR, file), 'utf8')
    expect(contents.toLowerCase()).not.toContain('invited')
  })

  it.each(files)("%s does not carry the design's invited reason text", (file) => {
    const contents = readFileSync(join(DEMO_DIR, file), 'utf8')
    expect(contents).not.toContain('Invitation not accepted')
  })
})
