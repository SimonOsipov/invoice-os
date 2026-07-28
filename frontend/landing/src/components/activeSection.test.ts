// RED-then-GREEN spec (LAND-01-02) — pins activeNavHref()'s branch contract before it exists:
// last section crossed (top <= threshold) wins; null when nothing crossed or the last-crossed
// section has no nav link. Fixtures mirror the real landing page's 8 sections / 6 nav hrefs.
import { describe, expect, it } from 'vitest'

import { activeNavHref } from './activeSection'

// DOM order matches the shipped landing page: top, how, modules, compliance, accountants,
// developers, pricing, demo. Only #top and #demo have no nav link.
const SECTIONS = [
  { id: 'top', top: 0 },
  { id: 'how', top: 400 },
  { id: 'modules', top: 900 },
  { id: 'compliance', top: 1400 },
  { id: 'accountants', top: 1900 },
  { id: 'developers', top: 2400 },
  { id: 'pricing', top: 2900 },
  { id: 'demo', top: 3400 },
] as const

const NAV_HREFS = ['#how', '#modules', '#compliance', '#accountants', '#developers', '#pricing'] as const

// Production threshold is headerH(65) + 1.
const THRESHOLD = 66

describe('activeNavHref', () => {
  it('U1: every section below the threshold (visitor above the first section) returns null', () => {
    const sections = SECTIONS.map((s) => ({ id: s.id, top: s.top + 10_000 }))
    expect(activeNavHref(sections, NAV_HREFS, THRESHOLD)).toBeNull()
  })

  it('U2: the last section crossed so far is the active one, even with more sections below', () => {
    const sections = [
      { id: 'top', top: -500 },
      { id: 'how', top: 50 },
      { id: 'modules', top: 900 },
      { id: 'compliance', top: 1400 },
    ]
    expect(activeNavHref(sections, NAV_HREFS, THRESHOLD)).toBe('#how')
  })

  it('U3: a section whose top exactly equals the threshold counts as crossed (inclusive <=)', () => {
    const sections = [
      { id: 'top', top: -500 },
      { id: 'how', top: THRESHOLD },
    ]
    expect(activeNavHref(sections, NAV_HREFS, THRESHOLD)).toBe('#how')
  })

  it('U4: the last crossed section has no nav link, so the result is null even though an earlier section did', () => {
    const sections = [
      { id: 'top', top: -900 },
      { id: 'how', top: -400 },
      { id: 'demo', top: 10 },
    ]
    expect(activeNavHref(sections, NAV_HREFS, THRESHOLD)).toBeNull()
  })

  it('U5: scrolled to the last nav section returns it, one pixel further (into the non-nav tail) returns null', () => {
    const scrolledToPricing = [
      { id: 'developers', top: -50 },
      { id: 'pricing', top: 0 },
      { id: 'demo', top: 500 },
    ]
    expect(activeNavHref(scrolledToPricing, NAV_HREFS, THRESHOLD)).toBe('#pricing')

    const scrolledPastIntoDemo = [
      { id: 'developers', top: -550 },
      { id: 'pricing', top: -500 },
      { id: 'demo', top: -1 },
    ]
    expect(activeNavHref(scrolledPastIntoDemo, NAV_HREFS, THRESHOLD)).toBeNull()
  })

  it('U6: an empty sections array returns null', () => {
    expect(activeNavHref([], NAV_HREFS, THRESHOLD)).toBeNull()
  })

  it('U6: an empty navHrefs array returns null even when a section has crossed', () => {
    const sections = [
      { id: 'top', top: -500 },
      { id: 'how', top: 0 },
    ]
    expect(activeNavHref(sections, [], THRESHOLD)).toBeNull()
  })

  it('U7: selection follows array (DOM) order, not sorted top values', () => {
    // #modules is listed after #how but has a smaller top (crossed "more"). The last
    // *array* entry that crossed must win — #modules — not the smallest-top entry.
    const outOfOrderTops = [
      { id: 'top', top: -900 },
      { id: 'how', top: 20 },
      { id: 'modules', top: -50 },
    ]
    expect(activeNavHref(outOfOrderTops, NAV_HREFS, THRESHOLD)).toBe('#modules')
  })

  it('threshold of exactly 0 is honored, not silently swapped for a nonzero default', () => {
    // Every U1-U7 row uses THRESHOLD (66), so none of them would notice a `threshold ||
    // someDefault` bug — 0 is the one input value JS truthiness gets wrong. Confirmed by
    // mutation: a `threshold || 66` implementation passes all of U1-U7 and only fails here.
    const sections = [
      { id: 'how', top: 0 },
      { id: 'modules', top: 1 },
    ]
    expect(activeNavHref(sections, NAV_HREFS, 0)).toBe('#how')
  })

  it('sub-pixel tops are compared exactly, not rounded to the nearest integer', () => {
    // getBoundingClientRect().top is a float in production (e.g. 65.15625), and every
    // existing row above uses whole-number tops. A section at threshold + 0.4 must NOT
    // count as crossed: Math.round(threshold + 0.4) === threshold would wrongly say it has.
    // Confirmed by mutation: a Math.round(s.top) implementation passes all of U1-U7.
    const sections = [
      { id: 'how', top: -12.34 },
      { id: 'modules', top: THRESHOLD + 0.4 },
    ]
    expect(activeNavHref(sections, NAV_HREFS, THRESHOLD)).toBe('#how')
  })

  it('does not mutate the sections or navHrefs arrays (or their elements) it receives', () => {
    // The caller's arrays are typed `readonly`; freezing them turns any in-place mutation
    // (e.g. a `.sort()` on the parameter itself instead of a copy) into a thrown TypeError
    // rather than a silently-corrupted array the caller might reuse.
    const sections = [
      { id: 'how', top: 20 },
      { id: 'modules', top: -50 },
    ]
    const navHrefs = ['#how', '#modules']
    sections.forEach((s) => Object.freeze(s))
    Object.freeze(sections)
    Object.freeze(navHrefs)
    expect(activeNavHref(sections, navHrefs, THRESHOLD)).toBe('#modules')
  })
})
