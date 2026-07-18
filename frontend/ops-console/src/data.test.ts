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

// Adversarial (QA Mode B, M4-20-07) — QUOTA/BILL_ITEMS/PAST_INVOICES/INVOICE_STATUS are
// plain exported data.tsx constants like the blocks above, so they get the same direct
// unit coverage. Billing.tsx keys its two `.map()` renders on `b.label` / `p.id`
// (never `amount`, which mixes the string literal `'included'` with computed and
// literal ₦ figures) — these pin the uniqueness that prevents a duplicate-key
// console.error, plus the D1 (sidebar-only pct/widthPct/detail) and D2 (itemized rows
// deliberately do not sum to the total) rulings from task-144's architecture pass as
// regression guards.
import { computeQuota, naira, nairaC, SCALE_PLAN, spendTotals } from './charts'
import { BILL_ITEMS, INVOICE_STATUS, PAST_INVOICES, QUOTA } from './data'

describe('BILL_ITEMS', () => {
  it('bill_items_has_exactly_four_rows_with_unique_labels: guards key={b.label} against a duplicate-key console.error', () => {
    expect(BILL_ITEMS.length).toBe(4)
    const labels = BILL_ITEMS.map((b) => b.label)
    expect(new Set(labels).size).toBe(4)
  })

  it('bill_items_amounts_are_byte_exact_and_evidence_exports_is_the_string_included_not_a_number: pins computeBillLine/naira wiring (proto:1029-1034)', () => {
    expect(BILL_ITEMS.map((b) => b.amount)).toEqual(['₦1,200,000', '₦1,872,800', '₦344,988', 'included'])
    const evidence = BILL_ITEMS.find((b) => b.label === 'Evidence exports')!
    expect(evidence.amount).toBe('included')
    expect(typeof evidence.amount).toBe('string')
    expect(Number.isNaN(Number(evidence.amount))).toBe(true)
  })

  it('bill_items_do_not_sum_to_the_total_row_D2: Σ(baseFee, cleared, overage) is ₦3,417,788, which is NOT spendTotals().proj (₦5.08M) — the itemized rows and the seeded spend series are unlinked streams, and the total row must not be "fixed" to reconcile them', () => {
    const numeric = BILL_ITEMS.filter((b) => b.amount !== 'included').map((b) => Number(b.amount.replace(/[₦,]/g, '')))
    const sum = numeric.reduce((a, b) => a + b, 0)
    expect(sum).toBe(3417788)
    expect(naira(sum)).not.toBe(nairaC(spendTotals().proj))
  })
})

describe('PAST_INVOICES', () => {
  it('past_invoices_has_exactly_four_rows_with_unique_ids: guards key={p.id} against a duplicate-key console.error', () => {
    expect(PAST_INVOICES.length).toBe(4)
    const ids = PAST_INVOICES.map((p) => p.id)
    expect(new Set(ids).size).toBe(4)
  })

  it('past_invoices_has_exactly_one_open_row_and_three_paid_rows', () => {
    const kinds = PAST_INVOICES.map((p) => p.kind)
    expect(kinds.filter((k) => k === 'open').length).toBe(1)
    expect(kinds.filter((k) => k === 'paid').length).toBe(3)
  })

  it('past_invoices_paid_amounts_are_literal_full_digit_strings_not_compact_pins_the_intentional_formatting_mismatch: the OPEN row is compact, the three PAID rows are full-digit literals (proto:1037-1040) — do not unify', () => {
    const paid = PAST_INVOICES.filter((p) => p.kind === 'paid')
    expect(paid.map((p) => p.amount)).toEqual(['₦3,184,200', '₦2,940,500', '₦2,712,300'])
  })

  it('past_invoices_open_row_is_FB_2026_07_and_its_amount_is_computed_via_spendTotals_proj_not_a_literal', () => {
    const open = PAST_INVOICES.find((p) => p.kind === 'open')!
    expect(open.id).toBe('FB-2026-07')
    expect(open.amount).toBe(nairaC(spendTotals().proj))
    expect(open.amount).toBe('₦5.08M')
  })
})

describe('INVOICE_STATUS', () => {
  it('invoice_status_is_keyed_by_exactly_the_two_kind_values', () => {
    expect(Object.keys(INVOICE_STATUS).sort()).toEqual(['open', 'paid'])
  })

  it('invoice_status_labels_and_text_colors_are_byte_exact: paid is green/PAID, open is amber/OPEN (proto:1035)', () => {
    expect(INVOICE_STATUS.paid.label).toBe('PAID')
    expect(INVOICE_STATUS.paid.text).toBe('var(--status-green-text)')
    expect(INVOICE_STATUS.open.label).toBe('OPEN')
    expect(INVOICE_STATUS.open.text).toBe('var(--status-amber-text)')
  })
})

describe('QUOTA', () => {
  it('quota_seed_is_byte_exact', () => {
    expect(QUOTA).toEqual({
      used: 48214,
      includedWidth: '83%',
      overWidth: '17%',
      clearedInvoices: 46820,
      evidenceExports: 1020,
    })
  })

  it('quota_bar_segment_widths_are_literals_summing_to_100_percent_not_computeQuota_widthPct_D1: the meter is a two-segment flex track (proto:474-476), so computeQuota().widthPct (a single clamped fill, 100) is the wrong input — 83%/17% are literals consistent with the true 82.96%/17.04% share', () => {
    const included = Number(QUOTA.includedWidth.replace('%', ''))
    const over = Number(QUOTA.overWidth.replace('%', ''))
    expect(included + over).toBe(100)
    const q = computeQuota(QUOTA.used, SCALE_PLAN.includedRequests)
    expect(q.widthPct).toBe(100)
    expect(QUOTA.includedWidth).not.toBe(`${q.widthPct}%`)
  })

  it('quota_pct_and_detail_belong_to_the_sidebar_widget_not_this_screen_D1: computeQuota(48214, 40000).pct/.detail render nowhere in Billing.tsx — only .over (8,214) is consumed', () => {
    const q = computeQuota(QUOTA.used, SCALE_PLAN.includedRequests)
    expect(q.pct).toBe(120)
    expect(q.detail).toBe('48.2K / 40K included · 8.2K over')
    expect(q.over).toBe(8214)
  })
})

// Adversarial (QA Mode B, M4-20-08) — STATUS_COMPONENTS/INCIDENTS/STATUS_TONE are plain
// exported data.tsx constants like the seed blocks above, so they get the same direct
// unit coverage rather than only being exercised indirectly through the Phase 3.5
// screenshot gate. Status.tsx's nested `strip.map` inside `components.map` is this
// story's highest console-error risk (task-145 A4): the outer list is keyed on
// `c.name` and the incident list on `inc.date`, so a silent seed edit that duplicated
// either would collide a React key and red the smoke build on `console.error`. These
// pin the seed invariants: exactly six components, exactly one DEGRADED, unique names,
// bad (red) cells confined to the degraded component at its documented indices, and
// exactly three incidents with unique dates.
import { INCIDENTS, STATUS_COMPONENTS, STATUS_TONE } from './data'

describe('STATUS_COMPONENTS', () => {
  it('status_components_has_exactly_six_rows_in_proto_order: proto:1045-1052 lists API gateway first and Evidence store last', () => {
    expect(STATUS_COMPONENTS.length).toBe(6)
    expect(STATUS_COMPONENTS[0]!.name).toBe('API gateway')
    expect(STATUS_COMPONENTS[5]!.name).toBe('Evidence store')
  })

  it('status_component_names_are_unique: no two rows share a name — this is the outer .map key, so a collision would red the smoke build on a duplicate-key console.error', () => {
    const names = STATUS_COMPONENTS.map((c) => c.name)
    expect(names.length).toBe(6)
    expect(new Set(names).size).toBe(names.length)
  })

  it('status_has_exactly_one_degraded_component_and_it_is_the_tax_authority_connection: the other five are OPERATIONAL/green (guards filter()/every() against a vacuous pass on an emptied array)', () => {
    expect(STATUS_COMPONENTS.length).toBeGreaterThan(0)
    const degraded = STATUS_COMPONENTS.filter((c) => c.status === 'DEGRADED')
    expect(degraded.length).toBe(1)
    expect(degraded[0]!.name).toBe('Tax-authority connection (FIRS/MBS)')
    expect(degraded[0]!.tone).toBe('amber')
    const operational = STATUS_COMPONENTS.filter((c) => c.status === 'OPERATIONAL')
    expect(operational.length).toBe(5)
    expect(operational.every((c) => c.tone === 'green')).toBe(true)
  })

  it('every_strip_has_exactly_ninety_cells: upStrip(seed, badIdx) returns 90 entries for all six seeds actually wired into this screen', () => {
    expect(STATUS_COMPONENTS.every((c) => c.strip.length === 90)).toBe(true)
  })

  it('bad_red_cells_are_confined_to_the_degraded_component_at_indices_86_through_89: the other five components (badIdx=[]) carry zero red cells; pins the seed/badIdx wiring in data.tsx itself, not just upStrip in isolation', () => {
    const RED = 'var(--status-red-text)'
    const [gateway, validation, pipeline, tax, webhook, evidence] = STATUS_COMPONENTS
    for (const c of [gateway!, validation!, pipeline!, webhook!, evidence!]) {
      expect(c.strip.some((d) => d.fill === RED)).toBe(false)
    }
    const redIndices = tax!.strip.reduce<number[]>((acc, d, i) => (d.fill === RED ? [...acc, i] : acc), [])
    expect(redIndices).toEqual([86, 87, 88, 89])
  })

  it('status_tone_lookup_resolves_for_every_component: STATUS_TONE[c.tone] is never undefined (guards the badge-colour lookup Status.tsx performs per row)', () => {
    expect(STATUS_COMPONENTS.every((c) => STATUS_TONE[c.tone] !== undefined)).toBe(true)
  })
})

describe('INCIDENTS', () => {
  it('incidents_has_exactly_three_rows_in_proto_order: proto:1055-1058 order is Jul 14, Jul 02, Jun 21', () => {
    expect(INCIDENTS.length).toBe(3)
    expect(INCIDENTS.map((i) => i.date)).toEqual(['Jul 14', 'Jul 02', 'Jun 21'])
  })

  it('incident_dates_are_unique: no two rows share a date — this is the .map key', () => {
    const dates = INCIDENTS.map((i) => i.date)
    expect(new Set(dates).size).toBe(dates.length)
  })

  it('incident_statuses_match_tone_by_the_prototype_rule: MONITORING is amber, both RESOLVED rows are green (guards filter()/every() against a vacuous pass on an emptied array)', () => {
    expect(INCIDENTS.length).toBeGreaterThan(0)
    const monitoring = INCIDENTS.filter((i) => i.status === 'MONITORING')
    expect(monitoring.length).toBe(1)
    expect(monitoring[0]!.tone).toBe('amber')
    const resolved = INCIDENTS.filter((i) => i.status === 'RESOLVED')
    expect(resolved.length).toBe(2)
    expect(resolved.every((i) => i.tone === 'green')).toBe(true)
  })

  it('incident_tone_lookup_resolves_for_every_row: STATUS_TONE[i.tone] is never undefined', () => {
    expect(INCIDENTS.every((i) => STATUS_TONE[i.tone] !== undefined)).toBe(true)
  })
})
