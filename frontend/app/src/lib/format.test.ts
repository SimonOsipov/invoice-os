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

import { fmtDateTime } from './format'

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
