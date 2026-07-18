// Adversarial (QA Mode B, M4-20-04) — SEED_SUBMISSIONS is a plain exported data
// constant (data.tsx), genuinely pure and independent of any component render, so it
// gets direct unit coverage here rather than only being exercised indirectly through
// the Phase 3.5 screenshot gate. These pin invariants a silent seed edit could break
// without any test catching it: the dead-letter count Submissions.tsx and Sidebar.tsx
// both read directly from `state === 'dead-letter'` filtering, so a seed change that
// added/removed a dead-letter row would drift the sidebar badge and the stat tile out
// of sync with nothing failing.
import { describe, expect, it } from 'vitest'

import { SEED_SUBMISSIONS } from './data'

describe('SEED_SUBMISSIONS', () => {
  it('seed_has_exactly_ten_rows: SEED_SUBMISSIONS.length is 10 (proto:785-794)', () => {
    expect(SEED_SUBMISSIONS.length).toBe(10)
  })

  it('seed_has_exactly_one_dead_letter_row: exactly one row has state dead-letter (dlCount === 1, consequence of the seed swap)', () => {
    const deadLetter = SEED_SUBMISSIONS.filter((j) => j.state === 'dead-letter')
    expect(deadLetter.length).toBe(1)
  })

  it('seed_ids_are_unique: no two rows share an id, and there are ten distinct ids (guards the uniqueness check against a vacuous pass on an emptied array)', () => {
    const ids = SEED_SUBMISSIONS.map((j) => j.id)
    expect(ids.length).toBe(10)
    expect(new Set(ids).size).toBe(ids.length)
  })

  it('seed_raw_amounts_are_all_positive: every row raw is a positive number, and the first row is byte-exact (guards every() against a vacuous pass on an emptied array)', () => {
    expect(SEED_SUBMISSIONS.length).toBeGreaterThan(0)
    expect(SEED_SUBMISSIONS.every((j) => typeof j.raw === 'number' && j.raw > 0)).toBe(true)
    expect(SEED_SUBMISSIONS[0]!.raw).toBe(4120000)
  })

  it('seed_btin_carries_no_tin_prefix: btin values are bare digit-dash strings, not "TIN <digits>" (the operator-era shape), and the first row is byte-exact (guards every() against a vacuous pass on an emptied array)', () => {
    expect(SEED_SUBMISSIONS.length).toBeGreaterThan(0)
    expect(SEED_SUBMISSIONS.every((j) => !j.btin.startsWith('TIN '))).toBe(true)
    expect(SEED_SUBMISSIONS[0]!.btin).toBe('20184412-0001')
  })
})
