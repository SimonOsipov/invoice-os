import { describe, expect, it } from 'vitest'

import {
  addSuggested,
  customRulesFor,
  customRulesKey,
  GOLDEN_RULES,
  GOLDEN_SOURCE_REF,
  GOLDEN_VERSIONS,
  openSuggestions,
  removeCustom,
  ruleFromSuggestion,
  ruleJSON,
  SEED_CUSTOM_RULES,
  SUGGESTED_RULES,
  SUGGESTION_MESSAGE,
  tenantSlug,
  toggleCustom,
  type CustomRule,
  type Suggestion,
} from './rules'

const suggestion: Suggestion = SUGGESTED_RULES[0]

describe('customRulesKey / customRulesFor', () => {
  it('buckets by entity id so two clients never share a custom set', () => {
    expect(customRulesKey('ent-a')).not.toBe(customRulesKey('ent-b'))
  })

  it('collapses every in-house read onto one bucket (in-house has no entity row)', () => {
    expect(customRulesKey(null)).toBe('workspace')
  })

  it('an untouched client reads the seed set, an edited one reads its own', () => {
    const edited: CustomRule[] = []
    const store = { 'ent-a': edited }
    expect(customRulesFor(store, 'ent-a')).toBe(edited)
    expect(customRulesFor(store, 'ent-b')).toBe(SEED_CUSTOM_RULES)
  })
})

describe('openSuggestions', () => {
  it('drops a suggestion once a custom rule carries its key', () => {
    const custom = [ruleFromSuggestion(suggestion)]
    const open = openSuggestions(SUGGESTED_RULES, custom)
    expect(open).toHaveLength(SUGGESTED_RULES.length - 1)
    expect(open.map((s) => s.key)).not.toContain(suggestion.key)
  })

  it('matches on key, not identity — a hand-written rule with the same key also hides it', () => {
    const impostor: CustomRule = { ...ruleFromSuggestion(suggestion), message: 'written by hand', enabled: true }
    expect(openSuggestions(SUGGESTED_RULES, [impostor]).map((s) => s.key)).not.toContain(suggestion.key)
  })

  it('offers everything when the client has no custom rules at all', () => {
    expect(openSuggestions(SUGGESTED_RULES, [])).toHaveLength(SUGGESTED_RULES.length)
  })
})

describe('ruleFromSuggestion', () => {
  // §2.6: adopting a suggestion creates a rule for REVIEW, never a live one. If this
  // ever flipped, a rule inferred from history would start rejecting invoices the
  // moment somebody clicked Add.
  it('is disabled and stamped as unreviewed', () => {
    const r = ruleFromSuggestion(suggestion)
    expect(r.enabled).toBe(false)
    expect(r.message).toBe(SUGGESTION_MESSAGE)
  })

  it('carries the suggestion’s type, field, severity and params through unchanged', () => {
    const r = ruleFromSuggestion(suggestion)
    expect(r).toMatchObject({ key: suggestion.key, type: suggestion.type, field: suggestion.field, severity: suggestion.severity })
    expect(r.params).toEqual(suggestion.params)
  })
})

describe('custom-rule reducers', () => {
  it('addSuggested appends, and is idempotent on a second click', () => {
    const once = addSuggested([], suggestion)
    expect(once).toHaveLength(1)
    const twice = addSuggested(once, suggestion)
    expect(twice).toHaveLength(1)
    // Same array back, so React sees no change and nothing re-renders either.
    expect(twice).toBe(once)
  })

  it('toggleCustom flips exactly one rule and leaves the rest alone', () => {
    const before = SEED_CUSTOM_RULES
    const after = toggleCustom(before, 'wht.required.services')
    expect(after.find((r) => r.key === 'wht.required.services')!.enabled).toBe(true)
    expect(after.filter((r) => r.key !== 'wht.required.services')).toEqual(
      before.filter((r) => r.key !== 'wht.required.services'),
    )
    // Never mutates the seed set — every client reads it until first edited.
    expect(before.find((r) => r.key === 'wht.required.services')!.enabled).toBe(false)
  })

  it('removeCustom drops only the named rule', () => {
    const after = removeCustom(SEED_CUSTOM_RULES, 'po.number.required')
    expect(after).toHaveLength(SEED_CUSTOM_RULES.length - 1)
    expect(after.map((r) => r.key)).not.toContain('po.number.required')
  })

  it('a removed suggestion-backed rule comes back on offer', () => {
    const added = addSuggested([], suggestion)
    expect(openSuggestions(SUGGESTED_RULES, added).map((s) => s.key)).not.toContain(suggestion.key)
    expect(openSuggestions(SUGGESTED_RULES, removeCustom(added, suggestion.key)).map((s) => s.key)).toContain(suggestion.key)
  })
})

describe('tenantSlug', () => {
  it('kebabs a company name and never leaves a leading or trailing dash', () => {
    expect(tenantSlug('Lagos Freight & Logistics Ltd')).toBe('lagos-freight-logistics-ltd')
    expect(tenantSlug('  Adeyemi & Sons  ')).toBe('adeyemi-sons')
  })
})

describe('ruleJSON', () => {
  // The regex rule's pattern contains backslashes; hand-built JSON (the Support
  // Console's approach) emits `"^\d{8}-\d{4}$"`, which is not parseable JSON.
  it('stays parseable for a rule whose params contain backslashes', () => {
    const regexRule = GOLDEN_RULES.find((r) => r.key === 'buyer.tin.format')!
    const parsed = JSON.parse(ruleJSON(regexRule, GOLDEN_SOURCE_REF, true))
    expect(parsed.params.pattern).toBe('^\\d{8}-\\d{4}$')
  })

  it('states the golden source ref and always-on status for an inherited rule', () => {
    const parsed = JSON.parse(ruleJSON(GOLDEN_RULES[0], GOLDEN_SOURCE_REF, true))
    expect(parsed.source).toBe('ascomply/golden:NG-MBS@v8')
    expect(parsed.enabled).toBe(true)
  })

  it('states the tenant source ref and the live flag for a custom rule', () => {
    const rule = SEED_CUSTOM_RULES.find((r) => r.key === 'wht.required.services')!
    const parsed = JSON.parse(ruleJSON(rule, `tenant:${tenantSlug('Kano Textile Mills Plc')}`, rule.enabled))
    expect(parsed.source).toBe('tenant:kano-textile-mills-plc')
    expect(parsed.enabled).toBe(false)
  })

  it('keys params by a snake_cased label rather than dropping them', () => {
    const rule = SEED_CUSTOM_RULES.find((r) => r.key === 'invoice.value.cap')!
    const parsed = JSON.parse(ruleJSON(rule, 'tenant:x', true))
    expect(parsed.params).toEqual({ max: '₦500,000,000', on_breach: 'warn + route to director approval' })
  })
})

describe('golden ruleset', () => {
  it('the rail’s live-version rule count is derived from the rules actually shipped', () => {
    const live = GOLDEN_VERSIONS.find((v) => v.kind === 'active')!
    expect(live.meta).toContain(`${GOLDEN_RULES.length} rules`)
    expect(live.tag).toBe('IN USE')
  })

  it('has no enabled flag to toggle — read-only is structural, not a UI decision', () => {
    for (const r of GOLDEN_RULES) expect(r).not.toHaveProperty('enabled')
  })

  it('every rule key is unique across golden and the seed custom set', () => {
    const keys = [...GOLDEN_RULES, ...SEED_CUSTOM_RULES].map((r) => r.key)
    expect(new Set(keys).size).toBe(keys.length)
  })
})
