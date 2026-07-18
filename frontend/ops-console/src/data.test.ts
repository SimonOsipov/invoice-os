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

// Adversarial (QA Mode B, M4-20-06) — API_KEYS/WEBHOOKS/DELIVERIES/REQ_LOG/RATE_LIMIT are
// plain exported data.tsx constants like SEED_SUBMISSIONS above, so they get the same
// direct unit coverage. DELIVERIES and REQ_LOG each have a natural-key collision
// (`event`: 'invoice.cleared' x3; `m + ep`: 'POST /v2/invoices' x4) that task-143's
// architecture pass (A6) flagged as this subtask's highest console-error risk — keying
// a `.map()` render on either natural key reintroduces a React duplicate-key
// console.error, which e2e/smoke/smoke.spec.ts reds the build on. These pin both the
// collision itself (so nobody "simplifies" the `id` field away believing it's
// redundant) and the `id`-based uniqueness that actually prevents it.
import { API_KEYS, DELIVERIES, RATE_LIMIT, REQ_LOG, WEBHOOKS } from './data'

describe('API_KEYS', () => {
  it('api_keys_has_exactly_two_rows_with_distinct_env_tags: LIVE and SANDBOX, no third env', () => {
    expect(API_KEYS.length).toBe(2)
    expect(new Set(API_KEYS.map((k) => k.tag))).toEqual(new Set(['LIVE', 'SANDBOX']))
  })

  it('api_keys_ids_are_unique: no two keys share an id', () => {
    const ids = API_KEYS.map((k) => k.id)
    expect(new Set(ids).size).toBe(ids.length)
  })

  it('api_keys_mask_is_exactly_twenty_middle_dots: every mask contains exactly 20 U+00B7 characters, not asterisks or the U+2022 bullet (guards every() against a vacuous pass on an emptied array)', () => {
    expect(API_KEYS.length).toBeGreaterThan(0)
    expect(API_KEYS.every((k) => (k.mask.match(/·/g) ?? []).length === 20)).toBe(true)
    expect(API_KEYS.every((k) => !k.mask.includes('*') && !k.mask.includes('•'))).toBe(true)
  })

  it('api_keys_mask_suffix_matches_full_suffix: each key\'s masked trailing 4 chars equal its full-value trailing 4 chars, pinning the live/sandbox seed byte-exact (proto:993-994)', () => {
    const live = API_KEYS.find((k) => k.id === 'live')!
    const sandbox = API_KEYS.find((k) => k.id === 'sandbox')!
    expect(live.mask.slice(-4)).toBe('72e4')
    expect(live.full.slice(-4)).toBe('72e4')
    expect(sandbox.mask.slice(-4)).toBe('9f4c')
    expect(sandbox.full.slice(-4)).toBe('9f4c')
  })

  it('api_keys_live_card_border_is_green_sandbox_is_neutral: pins the asymmetric card borderColor (proto:992-995) — LIVE gets --status-green-border, SANDBOX gets the neutral --line-1, not its own amber', () => {
    const live = API_KEYS.find((k) => k.id === 'live')!
    const sandbox = API_KEYS.find((k) => k.id === 'sandbox')!
    expect(live.borderColor).toBe('var(--status-green-border)')
    expect(sandbox.borderColor).toBe('var(--line-1)')
  })
})

describe('WEBHOOKS', () => {
  it('webhooks_has_exactly_two_rows_with_distinct_urls_and_envs', () => {
    expect(WEBHOOKS.length).toBe(2)
    expect(new Set(WEBHOOKS.map((w) => w.url)).size).toBe(2)
    expect(new Set(WEBHOOKS.map((w) => w.env))).toEqual(new Set(['LIVE', 'SANDBOX']))
  })

  it('webhooks_event_chip_composite_keys_are_unique_even_though_event_names_repeat_across_endpoints: invoice.cleared and invoice.rejected each occur in both webhooks, but `${url}:${event}` never collides', () => {
    const composite = WEBHOOKS.flatMap((w) => w.events.map((ev) => w.url + ':' + ev))
    expect(new Set(composite).size).toBe(composite.length)
    // the collision this composite key exists to defuse:
    const bareNames = WEBHOOKS.flatMap((w) => w.events)
    expect(bareNames.filter((e) => e === 'invoice.cleared').length).toBe(2)
    expect(bareNames.filter((e) => e === 'invoice.rejected').length).toBe(2)
  })
})

describe('DELIVERIES', () => {
  it('deliveries_has_exactly_five_rows_with_unique_ids: dlv_1..dlv_5, no index-key or duplicate id', () => {
    expect(DELIVERIES.length).toBe(5)
    const ids = DELIVERIES.map((d) => d.id)
    expect(new Set(ids).size).toBe(5)
  })

  it('deliveries_natural_key_event_collides_three_times: pins the exact regression a key={d.event} render would reintroduce — a duplicate-key console.error that reds e2e/smoke/smoke.spec.ts', () => {
    const events = DELIVERIES.map((d) => d.event)
    expect(events.filter((e) => e === 'invoice.cleared').length).toBe(3)
  })

  it('deliveries_codes_are_the_literal_seed_values_httpCodeColor_is_applied_to_at_render: pins the 5 raw HTTP codes byte-exact (200 x4, 500 x1)', () => {
    const codes = DELIVERIES.map((d) => d.code)
    expect(codes).toEqual([200, 200, 500, 200, 200])
  })
})

describe('REQ_LOG', () => {
  it('req_log_has_exactly_six_rows_with_unique_ids: req_1..req_6, no index-key or duplicate id', () => {
    expect(REQ_LOG.length).toBe(6)
    const ids = REQ_LOG.map((r) => r.id)
    expect(new Set(ids).size).toBe(6)
  })

  it('req_log_natural_key_method_plus_endpoint_collides_four_times: pins the exact regression a key={r.m + r.ep} render would reintroduce', () => {
    const combos = REQ_LOG.map((r) => r.m + ' ' + r.ep)
    expect(combos.filter((c) => c === 'POST /v2/invoices').length).toBe(4)
  })

  it('req_log_method_chips_are_only_get_or_post: no third HTTP method sneaks onto the surface — METHOD_BG/METHOD_FG only define GET/POST, so a third method would render an undefined chip colour (guards every() against a vacuous pass on an emptied array)', () => {
    expect(REQ_LOG.length).toBeGreaterThan(0)
    expect(REQ_LOG.every((r) => r.m === 'GET' || r.m === 'POST')).toBe(true)
  })
})

describe('RATE_LIMIT', () => {
  it('rate_limit_is_keyed_by_exactly_the_two_env_values: sandbox and live, no third key', () => {
    expect(Object.keys(RATE_LIMIT).sort()).toEqual(['live', 'sandbox'])
  })

  it('rate_limit_width_is_pinned_literal_not_derived_from_current_over_limit: live is 341/500 = 68.2%, but the seed pins width at "68%" — task-143 A1/A4 forbid computing it because a naive `${(current/limit*100)}%` template would instead produce "68.2%" and silently drift from the design', () => {
    expect(RATE_LIMIT.live.current).toBe('341')
    expect(RATE_LIMIT.live.limit).toBe('500')
    expect(RATE_LIMIT.live.width).toBe('68%')
    expect(RATE_LIMIT.live.width).not.toBe('68.2%')
  })

  it('rate_limit_sandbox_row_is_byte_exact', () => {
    expect(RATE_LIMIT.sandbox).toEqual({
      current: '58',
      limit: '100',
      width: '58%',
      color: 'var(--accent)',
      detail: 'Sandbox throughput · resets each second',
    })
  })
})
