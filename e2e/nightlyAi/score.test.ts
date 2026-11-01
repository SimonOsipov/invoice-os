import { describe, expect, it } from 'vitest'

import { accuracy, scoreDocument, type KeyRow } from './score'

const FIELDS = ['invoice_number', 'issue_date', 'buyer_tin', 'buyer_name', 'currency', 'subtotal', 'vat', 'total']

function row(overrides: Record<string, string[]> = {}): KeyRow {
  const fields: Record<string, string[]> = {}
  for (const f of FIELDS) fields[f] = [`${f}-want`]
  return { file: 'nightly_tin_01.pdf', kind: 'tin', fields: { ...fields, ...overrides } }
}

const values = (overrides: Record<string, string | null> = {}) => ({ ...Object.fromEntries(FIELDS.map((f) => [f, `${f}-want`])), ...overrides })

describe('scoreDocument', () => {
  it('hits on a listed value, with surrounding spaces', () => {
    const r = scoreDocument(row({ buyer_tin: ['99999999-1111'] }), values({ buyer_tin: '  99999999-1111 ' }))
    expect(r.misses.map((m) => m.field)).not.toContain('buyer_tin')
  })

  it('misses a TIN one digit off', () => {
    const r = scoreDocument(row({ buyer_tin: ['99999999-1111'] }), values({ buyer_tin: '99999999-1112' }))
    expect(r.misses).toEqual([{ field: 'buyer_tin', want: ['99999999-1111'], got: '99999999-1112' }])
  })

  it('treats an empty list as read-nothing: null and blank hit, a value misses', () => {
    const outcome = (got: string | null) => scoreDocument(row({ vat: [] }), values({ vat: got })).misses.some((m) => m.field === 'vat')
    expect([outcome(null), outcome(''), outcome('75.00')]).toEqual([false, false, true])
  })

  it('a failed document misses only its non-empty cells', () => {
    const r = scoreDocument(row({ vat: [], subtotal: [] }), null)
    expect([r.hits, r.cells]).toEqual([2, 8])
    expect(r.misses).toHaveLength(6)
  })
})

describe('accuracy', () => {
  it('divides hits by all cells', () => {
    const all = row()
    const perfect = scoreDocument(all, values())
    const oneOff = scoreDocument(all, values({ total: 'x' }))
    expect(perfect.hits + oneOff.hits).toBe(15)
    expect(accuracy([perfect, oneOff])).toBe(0.9375)
  })

  it('is 0 with no documents', () => {
    expect(accuracy([])).toBe(0)
  })
})
