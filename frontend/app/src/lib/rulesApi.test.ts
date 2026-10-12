import { describe, expect, it, vi } from 'vitest'

import { INVOICE_FIELDS } from './invoiceFields'
import {
  checksFor,
  createCustomRule,
  deleteCustomRule,
  expressionRuleInput,
  formError,
  getGoldenRules,
  GUIDED_FIELDS,
  guidedRuleInput,
  guidedShapeOf,
  listCustomRules,
  payloadPath,
  ruleInputOf,
  ruleJSON,
  ruleKeyLabel,
  rulesInScope,
  scopeLabel,
  updateCustomRule,
  type CustomRule,
  type CustomRuleInput,
  type ExpressionState,
  type GuidedState,
} from './rulesApi'

const BASE = 'https://gw.test'

function rule(over: Partial<CustomRule> = {}): CustomRule {
  return {
    id: '3f2a0000-0000-0000-0000-000000000001',
    key: 'custom:3f2a0000-0000-0000-0000-000000000001',
    revision: 2,
    revision_id: 'rev-2',
    entity_id: null,
    type: 'required',
    target: 'buyer.email',
    params: {},
    severity: 'warning',
    when: null,
    message: 'Buyer email is needed',
    enabled: true,
    updated_at: '2026-10-12T10:00:00Z',
    updated_by: 'u1',
    ...over,
  }
}

function guided(over: Partial<GuidedState>): GuidedState {
  return {
    field: 'buyer_email',
    check: 'required',
    pattern: '',
    values: '',
    min: '',
    max: '',
    message: 'm',
    severity: 'error',
    entityId: null,
    ...over,
  }
}

function input(over: Partial<CustomRuleInput>): CustomRuleInput {
  return { entity_id: null, type: 'required', target: 'buyer.email', params: {}, severity: 'error', when: null, message: 'm', enabled: true, ...over }
}

describe('rulesApi', () => {
  it('rulesApi_fetchersHitTheRoutes', async () => {
    const reply = { sentinel: true }
    const af = vi.fn().mockResolvedValue(reply)
    const body = input({})

    expect(await getGoldenRules(af, BASE)).toBe(reply)
    expect(af).toHaveBeenLastCalledWith(`${BASE}/api/validation/v1/rules`)

    const rules = [rule()]
    af.mockResolvedValueOnce({ custom_rules: rules })
    expect(await listCustomRules(af, BASE)).toBe(rules)
    expect(af).toHaveBeenLastCalledWith(`${BASE}/api/invoice/v1/custom-rules`)

    expect(await createCustomRule(af, BASE, body)).toBe(reply)
    expect(af).toHaveBeenLastCalledWith(`${BASE}/api/invoice/v1/custom-rules`, { method: 'POST', body })

    expect(await updateCustomRule(af, BASE, 'r1', body)).toBe(reply)
    expect(af).toHaveBeenLastCalledWith(`${BASE}/api/invoice/v1/custom-rules/r1`, { method: 'PUT', body })

    af.mockResolvedValueOnce(undefined)
    await expect(deleteCustomRule(af, BASE, 'r1')).resolves.toBeUndefined()
    expect(af).toHaveBeenLastCalledWith(`${BASE}/api/invoice/v1/custom-rules/r1`, { method: 'DELETE' })
  })

  it('rulesInScope_firmPlusActiveClient', () => {
    const firm = rule({ id: 'f', entity_id: null })
    const a = rule({ id: 'a', entity_id: 'A' })
    const b = rule({ id: 'b', entity_id: 'B' })

    expect(rulesInScope([firm, a, b], 'A')).toEqual([firm, a])
    expect(rulesInScope([firm, a, b], null)).toEqual([firm])
  })

  it('guidedRuleInput_emitsEachTypedShape', () => {
    const cases: [GuidedState, Partial<CustomRuleInput>][] = [
      [guided({}), { type: 'required', target: 'buyer.email', params: {} }],
      [guided({ check: 'regex', pattern: '^[^@]+@[^@]+$' }), { type: 'format/regex', target: 'buyer.email', params: { pattern: '^[^@]+@[^@]+$' } }],
      [guided({ field: 'currency', check: 'enum', values: ' NGN , USD,, ' }), { type: 'enum', target: 'currency', params: { values: ['NGN', 'USD'] } }],
      [guided({ field: 'total', check: 'range', max: '500000000' }), { type: 'range', target: 'total', params: { max: 500000000 } }],
      [guided({ field: 'total', check: 'range', min: '1', max: '9.5' }), { type: 'range', target: 'total', params: { min: 1, max: 9.5 } }],
      [guided({ field: 'total', check: 'range', min: '0' }), { type: 'range', target: 'total', params: { min: 0 } }],
      [guided({ field: 'supplier_postal_zone' }), { type: 'required', target: 'supplier.postal_zone', params: {} }],
    ]
    for (const [state, want] of cases) {
      expect(guidedRuleInput(state)).toEqual(input(want))
    }
    expect(payloadPath('invoice_number')).toBe('invoice_number')
  })

  it('guidedRuleInput_scopeAndSeverity', () => {
    expect(guidedRuleInput(guided({ entityId: null })).entity_id).toBeNull()
    expect(guidedRuleInput(guided({ entityId: 'ent-1' })).entity_id).toBe('ent-1')
    expect(guidedRuleInput(guided({ severity: 'warning' })).severity).toBe('warning')
    expect(guidedRuleInput(guided({ severity: 'error' })).severity).toBe('error')
    expect(guidedRuleInput(guided({})).enabled).toBe(true)
  })

  it('guidedShapeOf_roundTripsEveryGuidedShape', () => {
    let pairs = 0
    for (const f of GUIDED_FIELDS) {
      for (const check of checksFor(f.type)) {
        const s = guided({
          field: f.key,
          check,
          pattern: check === 'regex' ? '^x$' : '',
          values: check === 'enum' ? 'a, b' : '',
          min: check === 'range' ? '1.5' : '',
          max: check === 'range' ? '9' : '',
          severity: pairs % 2 ? 'warning' : 'error',
          entityId: pairs % 3 ? 'ent-1' : null,
        })
        expect(guidedShapeOf(guidedRuleInput(s)), `${f.key} ${check}`).toEqual(s)
        pairs++
      }
    }
    expect(pairs).toBeGreaterThan(40)
    const oneBound = guided({ field: 'total', check: 'range', max: '7' })
    expect(guidedShapeOf(guidedRuleInput(oneBound))).toEqual(oneBound)
  })

  it('guidedShapeOf_nullForNonGuidedRules', () => {
    expect(guidedShapeOf(input({ type: 'cel', target: 'total', params: { expr: 'true' } }))).toBeNull()
    expect(guidedShapeOf(input({ when: "has(invoice.invoice_kind) && invoice.invoice_kind == 'B2B'" }))).toBeNull()
    expect(guidedShapeOf(input({ type: 'tax_math', target: 'vat', params: {} }))).toBeNull()
    expect(guidedShapeOf(input({ target: 'line_items' }))).toBeNull()
    expect(guidedShapeOf(input({ type: 'range', target: 'buyer.email', params: { max: 1 } }))).toBeNull()
    expect(guidedShapeOf(input({ type: 'range', target: 'total', params: { exclusive_min: 1 } }))).toBeNull()
    expect(guidedShapeOf(input({ severity: 'info' }))).toBeNull()
    expect(guidedShapeOf(input({ type: 'cel', target: 'total', params: {} }))).toBeNull()
    expect(guidedShapeOf(input({ target: 'quantity' }))).toBeNull()
    // Extra or mistyped params, and a check the field's type does not offer.
    expect(guidedShapeOf(input({ params: { note: 1 } }))).toBeNull()
    expect(guidedShapeOf(input({ type: 'format/regex', params: { pattern: '^x$', flags: 'i' } }))).toBeNull()
    expect(guidedShapeOf(input({ type: 'format/regex', params: { pattern: 5 } }))).toBeNull()
    expect(guidedShapeOf(input({ type: 'enum', target: 'currency', params: { values: ['NGN'], list: 'x' } }))).toBeNull()
    expect(guidedShapeOf(input({ type: 'enum', target: 'currency', params: { values: ['NGN', 1] } }))).toBeNull()
    expect(guidedShapeOf(input({ type: 'enum', target: 'currency', params: { values: 'NGN' } }))).toBeNull()
    expect(guidedShapeOf(input({ type: 'range', target: 'total', params: { min: '1' } }))).toBeNull()
    expect(guidedShapeOf(input({ type: 'range', target: 'total', params: { max: 1, min: null } }))).toBeNull()
    expect(guidedShapeOf(input({ type: 'enum', target: 'total', params: { values: ['1'] } }))).toBeNull()
    expect(guidedShapeOf(input({ type: 'format/regex', target: 'invoice_kind', params: { pattern: '^x$' } }))).toBeNull()
    expect(guidedShapeOf(input({ type: 'range', target: 'issue_date', params: { min: 1 } }))).toBeNull()
    // On-path control: the same builders accept a shape the form offers.
    expect(guidedShapeOf(input({ type: 'range', target: 'total', params: { min: 1 } }))).not.toBeNull()
  })

  it('expressionRuleInput_emitsCel', () => {
    const expr = '!has(invoice.total) || invoice.total < 5.0'
    const s: ExpressionState = { target: 'total', expr, message: 'too big', severity: 'warning', entityId: 'ent-1' }

    expect(expressionRuleInput(s)).toEqual({
      entity_id: 'ent-1',
      type: 'cel',
      target: 'total',
      params: { expr },
      severity: 'warning',
      when: null,
      message: 'too big',
      enabled: true,
    })
    expect(expressionRuleInput({ ...s, target: '' }).target).toBe('')
    expect(expressionRuleInput({ ...s, message: '  m ' }).message).toBe('m')
  })

  it('formError_refusesIncompleteForms', () => {
    const range = { field: 'total', check: 'range' } as const
    const refused: GuidedState[] = [
      guided({ check: 'regex', pattern: '' }),
      guided({ check: 'enum', values: ' , ' }),
      guided({ ...range }),
      guided({ ...range, min: ' ' }),
      guided({ ...range, min: 'abc' }),
      guided({ ...range, min: '1', max: 'abc' }),
      guided({ ...range, max: 'Infinity' }),
      guided({ ...range, min: '1e999' }),
      guided({ message: '' }),
      guided({ message: '   ' }),
      guided({ message: 'x'.repeat(501) }),
    ]
    for (const s of refused) expect(formError(s), JSON.stringify(s)).toEqual(expect.any(String))

    const expr: ExpressionState = { target: '', expr: 'true', message: 'm', severity: 'error', entityId: null }
    expect(formError({ ...expr, expr: '  ' })).toEqual(expect.any(String))
    expect(formError({ ...expr, message: '😀'.repeat(501) })).toEqual(expect.any(String))

    const complete: (GuidedState | ExpressionState)[] = [
      guided({}),
      guided({ check: 'regex', pattern: '^x$' }),
      guided({ check: 'enum', values: 'a' }),
      guided({ ...range, max: '0' }),
      guided({ message: '😀'.repeat(500) }),
      expr,
    ]
    for (const s of complete) expect(formError(s), JSON.stringify(s)).toBeNull()
  })

  it('formError_countsTheMessageAfterTrimming', () => {
    expect(formError(guided({ message: `${'x'.repeat(500)} ` }))).toBeNull()
    expect(formError(guided({ message: ` ${'x'.repeat(501)} ` }))).toEqual(expect.any(String))
    expect(guidedRuleInput(guided({ message: '  hi ' })).message).toBe('hi')
  })

  it('checksFor_offersTheD6Checks', () => {
    expect(checksFor('text')).toEqual(['required', 'regex', 'enum'])
    expect(checksFor('code')).toEqual(['required', 'enum'])
    expect(checksFor('date')).toEqual(['required'])
    expect(checksFor('time')).toEqual(['required'])
    expect(checksFor('money')).toEqual(['required', 'range'])
    expect(checksFor('quantity')).toEqual(['required', 'range'])
    expect(checksFor('percent')).toEqual(['required', 'range'])

    expect(GUIDED_FIELDS.length).toBeGreaterThan(20)
    expect(GUIDED_FIELDS.some((f) => f.line)).toBe(false)
    expect(GUIDED_FIELDS).toEqual(INVOICE_FIELDS.filter((f) => !f.line))
  })

  it('ruleInputOf_roundTripsAndFlips', () => {
    for (const enabled of [true, false]) {
      const r = rule({ entity_id: 'ent-1', type: 'enum', target: 'currency', params: { values: ['NGN'] }, severity: 'error', when: 'x', enabled })
      const body = ruleInputOf(r)

      expect(Object.keys(body).sort()).toEqual(['enabled', 'entity_id', 'message', 'params', 'severity', 'target', 'type', 'when'])
      for (const k of Object.keys(body) as (keyof CustomRuleInput)[]) expect(body[k], k).toEqual(r[k])

      const flipped = { ...body, enabled: !r.enabled }
      const differing = (Object.keys(body) as (keyof CustomRuleInput)[]).filter((k) => flipped[k] !== body[k])
      expect(differing).toEqual(['enabled'])
    }
  })

  it('ruleKeyLabel_andScopeLabel', () => {
    expect(ruleKeyLabel('custom:3f2a0000-0000-0000-0000-000000000001')).toBe('Custom rule')
    expect(ruleKeyLabel('buyer-tin-format')).toBe('buyer-tin-format')

    expect(scopeLabel({ entity_id: null }, 'firm')).toBe('FIRM')
    expect(scopeLabel({ entity_id: 'A' }, 'firm')).toBe('CLIENT')
    expect(scopeLabel({ entity_id: 'A' }, 'inhouse')).toBe('CUSTOM')
    expect(scopeLabel({ entity_id: null }, 'inhouse')).toBe('CUSTOM')
  })

  it('ruleJSON_isTheServerObject', () => {
    const r = rule({ message: 'say "hi" \\ there', type: 'format/regex', params: { pattern: '^\\d+"$' } })

    expect(JSON.parse(ruleJSON(r))).toEqual(r)
    expect(ruleJSON(r)).toBe(JSON.stringify(r, null, 2))
  })
})
