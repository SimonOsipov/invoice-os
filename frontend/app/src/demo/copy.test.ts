// Structural invariants DEMO-06-03's Object.values(copy) scan depends on: every export
// is a non-empty string, never a function -- a template function would escape the scan.
import { describe, expect, it } from 'vitest'

import * as copy from './copy'

describe('demo copy shape', () => {
  const entries = Object.entries(copy)

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

  const scanned = Object.entries(copy).filter(([name]) => !EXCLUDED.includes(name))

  it('scans at least 12 exports', () => {
    expect(scanned.length).toBeGreaterThanOrEqual(12)
  })

  it.each(scanned)('%s carries no account/session vocabulary', (_name, value) => {
    expect(value as string).not.toMatch(BANNED)
  })
})
