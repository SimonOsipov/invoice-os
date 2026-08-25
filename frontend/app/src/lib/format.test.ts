// RED spec (M5-09-03, task-253) — pins fmtDateTime before the executor implements its
// body. format.ts has no other test file today; this covers only the one new export,
// not a retroactive backfill for fmt/fmtPlain/fmtShort/pad2/fmtDate.
//
// fmtDateTime's stub throws 'not implemented' (mirrors invoices.test.ts's stub
// convention), so F-1 fails on that throw — the correct RED reason, not a compile
// error. The exact-string assertion is deliberately loose (contains a date and a
// ':'-separated time) rather than pinned to one locale-formatted string:
// toLocaleString('en-NG') output varies with the Node ICU build.
import { describe, expect, it } from 'vitest'

import { fmtDateTime, fmtTime, toDateInputValue } from './format'

describe('fmtDateTime', () => {
  it('F-1: renders date and time, and guards bad input', () => {
    const rendered = fmtDateTime('2026-07-01T14:32:07Z')
    expect(rendered).not.toBe('—')
    expect(rendered).toContain(':')

    expect(fmtDateTime(null)).toBe('—')
    expect(fmtDateTime(undefined)).toBe('—')
    expect(fmtDateTime('')).toBe('—')
    expect(fmtDateTime('not-a-date')).toBe('—')
  })
})

// --- QA Mode B (task-253): adversarial coverage on top of F-1 above. F-1 is untouched. ---

describe('fmtDateTime: additional guards (adversarial)', () => {
  it('F-2: a date-only ISO string (no explicit time) still renders a time component, never the em-dash fallback', () => {
    const rendered = fmtDateTime('2026-07-01')
    expect(rendered).not.toBe('—')
    expect(rendered).toContain(':')
  })

  it('F-3: a valid RFC3339 timestamp with milliseconds renders normally', () => {
    const rendered = fmtDateTime('2026-07-01T14:32:07.123Z')
    expect(rendered).not.toBe('—')
    expect(rendered).toContain(':')
  })

  it('F-4: whitespace-only input falls back to the em-dash, same as empty string', () => {
    // '  ' is truthy (fails the `!iso` guard) but `new Date('  ')` is Invalid Date --
    // exercises the isNaN(getTime()) branch specifically, not the falsy-string branch.
    expect(fmtDateTime('   ')).toBe('—')
  })
})

// RED specs (task-391, BUG-03-02, Mode A) -- toDateInputValue's stub throws, so every
// case below fails on that throw, not on a missing import.
describe('toDateInputValue', () => {
  it('D-1: an RFC3339 midnight-UTC timestamp yields its bare date part', () => {
    expect(toDateInputValue('2026-02-04T00:00:00Z')).toBe('2026-02-04')
  })

  it('D-2: null, undefined and empty string all yield empty string', () => {
    expect(toDateInputValue(null)).toBe('')
    expect(toDateInputValue(undefined)).toBe('')
    expect(toDateInputValue('')).toBe('')
  })

  it('D-3: a bare date passes through unchanged', () => {
    expect(toDateInputValue('2026-02-04')).toBe('2026-02-04')
  })

  it('D-4: an unrecognised string is returned raw, never blanked', () => {
    expect(toDateInputValue('not-a-date')).toBe('not-a-date')
  })

  it('D-5: does not shift across a timezone', () => {
    // A `new Date(...).toLocaleDateString()` implementation reads this same input as
    // '2026-02-03' under America/Los_Angeles (UTC-8) -- the whole reason the real
    // implementation must not construct a Date at all. No existing spec in this repo
    // mutates process.env.TZ; restore it here so later tests in this file are unaffected.
    const original = process.env.TZ
    process.env.TZ = 'America/Los_Angeles'
    try {
      expect(toDateInputValue('2026-02-04T00:00:00Z')).toBe('2026-02-04')
    } finally {
      if (original === undefined) delete process.env.TZ
      else process.env.TZ = original
    }
  })
})

// RED specs (AUDIT-09-01, Mode A) -- fmtTime's stub throws 'not implemented', so every
// case below fails on that throw, not on a missing export.
//
// TIMEZONE: no TZ is pinned in this repo (vitest.config.ts is three lines; no playwright
// config sets one). Every input here is OFFSET-LESS, which ECMA-262 parses as LOCAL time,
// so a local getHours()/getMinutes() round-trips it exactly in every timezone. A
// 'Z'-suffixed input has no timezone-stable HH:MM and is deliberately never asserted.
describe('fmtTime', () => {
  it('T-1: renders a local offset-less timestamp as exact 24h HH:MM', () => {
    expect(fmtTime('2026-07-01T14:32:07')).toBe('14:32')
  })

  it('T-2: null, undefined, empty and unparseable input all yield the em-dash', () => {
    expect(fmtTime(null)).toBe('—')
    expect(fmtTime(undefined)).toBe('—')
    expect(fmtTime('')).toBe('—')
    expect(fmtTime('not-a-date')).toBe('—')
    expect(fmtTime('   ')).toBe('—')
  })

  it('T-3: single-digit hours and minutes are zero-padded to two places', () => {
    expect(fmtTime('2026-07-01T04:05:00')).toBe('04:05')
  })

  it('T-4: midnight and the last minute of the day stay on a 24-hour cycle', () => {
    // Guards toLocaleTimeString('en-NG'): most ICU builds resolve it to a 12-hour cycle,
    // which renders these as '12:00 AM' / '11:59 PM'.
    expect(fmtTime('2026-07-01T00:00:00')).toBe('00:00')
    expect(fmtTime('2026-07-01T23:59:00')).toBe('23:59')
  })

  it('T-5: the offset-less form is timezone-invariant', () => {
    // Parse and render are both local, so they cancel. Restores TZ the way D-5 does.
    const original = process.env.TZ
    try {
      for (const tz of ['America/Los_Angeles', 'Pacific/Kiritimati', 'UTC']) {
        process.env.TZ = tz
        expect(fmtTime('2026-07-01T14:32:07'), tz).toBe('14:32')
      }
    } finally {
      if (original === undefined) delete process.env.TZ
      else process.env.TZ = original
    }
  })
})
