// RED spec (M5-09-03, task-253) — pins fmtDateTime before the executor implements its
// body. format.ts has no other test file today; this covers only the one new export,
// not a retroactive backfill for fmt/fmtPlain/fmtShort/pad2/fmtDate.
//
// fmtDateTime's stub throws 'not implemented' (mirrors invoices.test.ts's stub
// convention), so F-1 fails on that throw — the correct RED reason, not a compile
// error. The exact-string assertion is deliberately loose (contains a date and a
// ':'-separated time) rather than pinned to one locale-formatted string:
// toLocaleString('en-NG') output varies with the Node ICU build.
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { describe, expect, it } from 'vitest'

import { fmtDateTime, fmtTime, fmtTimeWAT, toDateInputValue } from './format'

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

// fmtTime specs. T-1..T-5 were written RED against a throwing stub (Mode A); T-6..T-9 are
// the Mode B adversarial pass.
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

// --- QA Mode B (task-672): adversarial coverage on top of T-1..T-5. Those are untouched. ---

describe('fmtTime: adversarial', () => {
  const HHMM = /^\d{2}:\d{2}$/

  it('T-6: a Z-suffixed input is read as UTC and rendered in the local zone', () => {
    // Under the ambient TZ only the shape and the stability can be asserted -- the hour of
    // a Z-suffixed input is machine-dependent, which is why the file header forbids pinning
    // one. Asserting a clock therefore needs an explicit zone, as T-5 does.
    const rendered = fmtTime('2026-07-01T14:32:07Z')
    expect(rendered).not.toBe('—')
    expect(rendered).toMatch(HHMM)
    expect(fmtTime('2026-07-01T14:32:07Z')).toBe(rendered)

    // Half-hour offsets included: a minute field copied from the UTC input rather than the
    // local Date reads 20:32 under Kolkata.
    const original = process.env.TZ
    try {
      for (const [tz, clock] of [
        ['UTC', '14:32'],
        ['Asia/Kolkata', '20:02'],
        ['America/Los_Angeles', '07:32'],
      ] as const) {
        process.env.TZ = tz
        expect(fmtTime('2026-07-01T14:32:07Z'), tz).toBe(clock)
      }
    } finally {
      if (original === undefined) delete process.env.TZ
      else process.env.TZ = original
    }
  })

  it('T-7: a leap second is unparseable and takes the em-dash, not a wrapped clock', () => {
    // ECMA-262 caps seconds at 59, so :60 is Invalid Date -- it must NOT roll over to
    // '00:00' via a normalised Date.
    expect(fmtTime('2026-06-30T23:59:60Z')).toBe('—')
    expect(fmtTime('2026-06-30T23:59:60')).toBe('—')
    // Control: the last representable instant before it does render, so the guard above is
    // about the leap second and not about the whole minute being rejected.
    expect(fmtTime('2026-06-30T23:59:59.999')).toBe('23:59')
  })

  it('T-8: never throws and never leaks NaN, on any hostile string', () => {
    const hostile = [
      '',
      '   ',
      'not-a-date',
      '0000-00-00',
      '2026-13-45T99:99:99',
      '275760-09-14',
      '1e309',
      'Invalid Date',
      '2026-07-01T14:32:07+05:30',
      '2026-07-01',
      '2026-07-01T14:32:07.123456789Z',
      'x'.repeat(10_000),
      '2026-07-01T14:32:07 ',
      '٢٠٢٦-٠٧-٠١',
    ]
    expect(hostile.length).toBeGreaterThan(0)
    let rendered = 0
    for (const input of hostile) {
      let out = ''
      expect(() => {
        out = fmtTime(input)
      }, input).not.toThrow()
      expect(out === '—' || HHMM.test(out), `${JSON.stringify(input)} -> ${out}`).toBe(true)
      expect(out, input).not.toContain('NaN')
      if (out !== '—') rendered += 1
    }
    // Anti-vacuity: an implementation returning '—' unconditionally would satisfy every
    // assertion above. Some of these inputs are valid and must render.
    expect(rendered).toBeGreaterThan(0)
  })

  it('T-9: fmtTime and fmtDateTime agree on what is unrenderable', () => {
    // Both guard `!iso` then isNaN. A divergence means one of them started building a clock
    // from an input the other rejects.
    for (const input of [null, undefined, '', '   ', 'not-a-date', '2026-06-30T23:59:60Z']) {
      expect(fmtTime(input) === '—', String(input)).toBe(fmtDateTime(input) === '—')
    }
    // Control: both render the same valid input.
    expect(fmtTime('2026-07-01T14:32:07')).not.toBe('—')
    expect(fmtDateTime('2026-07-01T14:32:07')).not.toBe('—')
  })
})

// fmtTimeWAT lives here rather than in extractionReview.ts because T-4 above already records
// the ICU hazard it exists to dodge. Written RED (EXTR-11-04, Mode A) against a throwing stub.

describe('fmtTimeWAT', () => {
  const INSTANT = '2026-08-30T10:42:07Z' // 11:42 in Lagos

  it('W-1: renders the Lagos wall clock for an instant', () => {
    expect(fmtTimeWAT(INSTANT)).toBe('11:42')
  })

  it('W-2: the same instant in every host zone, and the host zone really moved', () => {
    const original = process.env.TZ
    const zones = ['UTC', 'America/New_York', 'Australia/Sydney']
    const hostClock = new Set<string>()
    try {
      for (const tz of zones) {
        process.env.TZ = tz
        // Control: fmtTime reads the host clock, so these MUST differ. Without it a runner
        // that ignores a runtime TZ change makes the invariance below vacuous.
        hostClock.add(fmtTime(INSTANT))
        expect(fmtTimeWAT(INSTANT), tz).toBe('11:42')
      }
    } finally {
      if (original === undefined) delete process.env.TZ
      else process.env.TZ = original
    }
    expect(hostClock.size, 'process.env.TZ did not take effect — the invariance proves nothing').toBe(zones.length)
  })

  it('W-3: midnight in Lagos is 00:xx, never 24:xx', () => {
    // A bare hour12:false yields '24:10' on some ICU builds; hourCycle 'h23' is what forbids it.
    expect(fmtTimeWAT('2026-08-29T23:10:00Z')).toBe('00:10')
  })

  it('W-4: unparseable input yields the em-dash rather than throwing', () => {
    // Intl.DateTimeFormat.format(new Date('x')) throws RangeError, which would blank the
    // whole document toolbar. Every sibling formatter in this file guards the same way.
    expect(fmtTimeWAT(null)).toBe('—')
    expect(fmtTimeWAT(undefined)).toBe('—')
    expect(fmtTimeWAT('')).toBe('—')
    expect(fmtTimeWAT('not-a-date')).toBe('—')
  })

  it('W-5: format.ts names Africa/Lagos and hourCycle h23 explicitly', () => {
    const src = readFileSync(fileURLToPath(new URL('./format.ts', import.meta.url)), 'utf8')
    expect(src, 'the scan read the wrong file').toContain('export function fmtTimeWAT(')

    expect(src, "the ' WAT' suffix is only true if the formatter names the zone").toContain("'Africa/Lagos'")
    expect(src, 'a small-ICU build falls back to a 12-hour default without it').toContain("hourCycle: 'h23'")
  })
})
