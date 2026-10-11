import { describe, expect, it, vi } from 'vitest'

import { EXPLAIN_COPY, explainViolation, fixPatch, type ExplainFix } from './explain'
import type { InvoiceLineItem } from './invoices'

const line = (n: number, id: string, over: Partial<InvoiceLineItem> = {}): InvoiceLineItem => ({
  id,
  line_no: n,
  description: `d${n}`,
  quantity: '1',
  unit_price: `${n}.00`,
  line_total: `${n}.00`,
  line_tax: '0.50',
  ...over,
})

const lineFix = (l: number, value = '5.00'): ExplainFix => ({ field: 'unit_price', label: 'Unit price', line: l, current: '-5.00', value })

describe('explain', () => {
  it('explainViolation_postsRuleKeyAndPath', async () => {
    const f = vi.fn().mockResolvedValue({ status: 'unavailable', explanation: null, fix: null })
    const res = await explainViolation(f, 'https://g', 'id1', { rule_key: 'vat-standard-rate' })
    expect(f).toHaveBeenCalledWith('https://g/api/invoice/v1/invoices/id1/explain', {
      method: 'POST',
      body: { rule_key: 'vat-standard-rate', path: '' },
    })
    expect(res.status).toBe('unavailable')
  })

  it('fixPatch_headerFixSendsOneKey', () => {
    const p = fixPatch([line(1, 'a')], { field: 'currency', label: 'Currency', line: null, current: 'USD', value: 'NGN' })
    expect(p).toEqual({ currency: 'NGN' })
    expect(p).not.toHaveProperty('line_items')
  })

  it('fixPatch_lineFixKeepsEveryOtherLine', () => {
    const lines = [line(3, 'c'), line(1, 'a'), line(2, 'b', { unit_price: '-5.00' })]
    const p = fixPatch(lines, lineFix(2))
    const stored = (l: InvoiceLineItem) => ({
      id: l.id,
      description: l.description,
      quantity: l.quantity,
      unit_price: l.unit_price,
      line_total: l.line_total,
      line_tax: l.line_tax,
    })
    expect(p?.line_items?.map((l) => l.id)).toEqual(['a', 'b', 'c'])
    expect(p?.line_items).toEqual([stored(lines[1]), { ...stored(lines[2]), unit_price: '5.00' }, stored(lines[0])])
  })

  it('fixPatch_nullFieldsStayNull', () => {
    const p = fixPatch([line(1, 'a', { line_tax: null }), line(2, 'b')], lineFix(2))
    expect(p?.line_items?.[0].line_tax).toBeNull()
  })

  it('fixPatch_absentLineIsNull', () => {
    expect(fixPatch([line(1, 'a'), line(2, 'b')], lineFix(3))).toBeNull()
  })

  it('fixPatch_doesNotMutateInputs', () => {
    const lines = [line(2, 'b'), line(1, 'a')]
    const fix = lineFix(1)
    const before = structuredClone({ lines, fix })
    lines.forEach(Object.freeze)
    Object.freeze(lines)
    Object.freeze(fix)
    expect(() => fixPatch(lines, fix)).not.toThrow()
    expect({ lines, fix }).toEqual(before)
  })

  it('explainCopy_mentionsNoAI', () => {
    for (const v of Object.values(EXPLAIN_COPY)) {
      expect(v).not.toMatch(/\bAI\b/)
      expect(v).not.toMatch(/provider/i)
    }
  })
})
