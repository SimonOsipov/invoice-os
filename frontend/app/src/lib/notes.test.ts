import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import { createAuthedFetch } from './authedFetch'
import { draftTotals } from './invoiceDraft'
import type { InvoiceLineItem } from './invoices'
import {
  getInvoiceNotes,
  isDuplicateNumberError,
  issueNote,
  noteDraftLines,
  NOTE_COPY,
  fmtNairaExact,
  noteLineError,
  noteSign,
  noteTypeBadge,
  signedNoteTotal,
  suggestedNoteNumber,
} from './notes'

afterEach(() => vi.unstubAllGlobals())

describe('suggestedNoteNumber', () => {
  it('suggestedNoteNumber counts notes of both types', () => {
    expect(suggestedNoteNumber('credit_note', 'INV-1', 0)).toBe('CN-INV-1-1')
    expect(suggestedNoteNumber('debit_note', 'INV-1', 1)).toBe('DN-INV-1-2')
  })
})

describe('noteLineError', () => {
  it('noteLineError caps a credit note by exact decimal', () => {
    expect(noteLineError('credit_note', 'quantity', '10.001', '10.000')).not.toBeNull()
    expect(noteLineError('credit_note', 'quantity', '10', '10.000')).toBeNull()
    expect(noteLineError('credit_note', 'unit_price', '100.01', '100.00')).not.toBeNull()
    expect(noteLineError('credit_note', 'unit_price', '100.00', '100.00')).toBeNull()
    // Read as text, '9.9' > '10'; as a number it is below the cap.
    expect(noteLineError('credit_note', 'quantity', '9.9', '10')).toBeNull()
  })

  it('noteLineError caps a credit note only when the original is known', () => {
    expect(noteLineError('credit_note', 'quantity', '1000000', null)).toBeNull()
    expect(noteLineError('credit_note', 'unit_price', '1000000', null)).toBeNull()
    expect(noteLineError('credit_note', 'quantity', '1000000', 'n/a')).toBeNull()
    // Exact decimal: a double cannot tell these apart.
    expect(noteLineError('credit_note', 'unit_price', '0.30000000000000001', '0.3')).not.toBeNull()
    expect(noteLineError('credit_note', 'quantity', '10.0000', '10')).toBeNull()
    // A bad value is refused even when the original is null.
    expect(noteLineError('credit_note', 'quantity', '0', null)).not.toBeNull()
  })

  it('noteLineError refuses an exponent without throwing or expanding it', () => {
    for (const v of ['1e999999999', '1e99999999', '1e2', '1E2', '1e-2']) {
      for (const field of ['quantity', 'unit_price'] as const) {
        expect(() => noteLineError('debit_note', field, v, null), `${field} ${v}`).not.toThrow()
        expect(noteLineError('debit_note', field, v, null), `${field} ${v}`).not.toBeNull()
      }
    }
  })

  // Rows mirror internal/invoice/note_amounts.go noteQuantity / noteUnitPrice.
  it.each([
    ['quantity', '1.2345', false],
    ['quantity', '1.2340', true],
    ['quantity', '1.234', true],
    ['quantity', '+1', false],
    ['quantity', '.5', false],
    ['quantity', '1.', false],
    ['quantity', '', false],
    ['quantity', ' 1', false],
    ['quantity', '007', true],
    ['quantity', '0', false],
    ['quantity', '-1', false],
    ['quantity', '99999999999.999', true],
    ['quantity', '100000000000', false],
    ['unit_price', '10.123', false],
    ['unit_price', '10.100', true],
    ['unit_price', '10.12', true],
    ['unit_price', '+1', false],
    ['unit_price', '.5', false],
    ['unit_price', '0', true],
    ['unit_price', '-0', true],
    ['unit_price', '-0.01', false],
    ['unit_price', '999999999999.99', true],
    ['unit_price', '1000000000000', false],
    ['quantity', '1\n', false],
    ['quantity', '1 ', false],
    ['quantity', '\t1', false],
    ['quantity', '１', false],
    ['quantity', '00.5', true],
    ['quantity', '-0', false],
    ['quantity', '0.0004', false],
    ['quantity', '0.001', true],
    ['quantity', '99999999999.9990', true],
    ['quantity', '99999999999.9999', false],
    ['quantity', '100000000000.0', false],
    ['quantity', '0099999999999', true],
    ['quantity', '000100000000000', false],
    ['quantity', '1.0000000000000000000000', true],
    ['quantity', '--1', false],
    ['quantity', '1_000', false],
    ['quantity', '1,5', false],
    ['quantity', '00', false],
    ['quantity', '1e5', false],
    ['unit_price', '-00', true],
    ['unit_price', '0.0', true],
    ['unit_price', '0.000', true],
    ['unit_price', '0.001', false],
    ['unit_price', '00.99', true],
    ['unit_price', '1.', false],
    ['unit_price', '1\n', false],
    ['unit_price', '999999999999.990', true],
    ['unit_price', '999999999999.999', false],
    ['unit_price', '0001000000000000', false],
  ] as const)('noteLineError matches the route: %s %j accepted=%s', (field, value, accepted) => {
    expect(noteLineError('debit_note', field, value, null) === null).toBe(accepted)
  })

  it('fmtNairaExact keeps kobo, groups thousands, signs before the naira', () => {
    expect(fmtNairaExact('100.50')).toBe('₦100.50')
    expect(fmtNairaExact('0.40')).toBe('₦0.40')
    expect(fmtNairaExact('1234567.00')).toBe('₦1,234,567.00')
    expect(fmtNairaExact('-100.00')).toBe('-₦100.00')
    expect(fmtNairaExact('-1000.05')).toBe('-₦1,000.05')
    expect(fmtNairaExact(null)).toBe('—')
    // Wire shapes beyond StringFixed(2): 13 digits, one or no decimals, small and negative kobo.
    expect(fmtNairaExact('9999999999999.99')).toBe('₦9,999,999,999,999.99')
    expect(fmtNairaExact('100.5')).toBe('₦100.50')
    expect(fmtNairaExact('100')).toBe('₦100.00')
    expect(fmtNairaExact('999.99')).toBe('₦999.99')
    expect(fmtNairaExact('1000.00')).toBe('₦1,000.00')
    expect(fmtNairaExact('0.05')).toBe('₦0.05')
    expect(fmtNairaExact('-0.40')).toBe('-₦0.40')
    expect(fmtNairaExact('0.00')).toBe('₦0.00')
    expect(fmtNairaExact('100.505')).toBe('₦100.505')
  })

  it('noteLineError names the field it refuses', () => {
    expect(noteLineError('credit_note', 'quantity', '11', '10')).toBe(NOTE_COPY.quantityOverOriginal)
    expect(noteLineError('credit_note', 'unit_price', '101', '100')).toBe(NOTE_COPY.unitPriceOverOriginal)
    expect(noteLineError('debit_note', 'quantity', '0', '10')).toBe(NOTE_COPY.quantityPositive)
    expect(noteLineError('debit_note', 'unit_price', '-1', '10')).toBe(NOTE_COPY.unitPriceInvalid)
  })

  it('noteLineError leaves a debit note uncapped', () => {
    expect(noteLineError('debit_note', 'quantity', '11', '10.000')).toBeNull()
    expect(noteLineError('debit_note', 'unit_price', '500.00', '100.00')).toBeNull()
  })

  it('noteLineError refuses bad values on both types', () => {
    for (const type of ['credit_note', 'debit_note'] as const) {
      for (const bad of ['0', '-1', 'abc', '']) {
        expect(noteLineError(type, 'quantity', bad, '10'), `${type} quantity ${bad}`).not.toBeNull()
      }
      for (const bad of ['-1', 'abc', '']) {
        expect(noteLineError(type, 'unit_price', bad, '100'), `${type} price ${bad}`).not.toBeNull()
      }
      expect(noteLineError(type, 'unit_price', '0', '100')).toBeNull()
    }
  })
})

// The seven vectors of Decision 15, the same as TestNoteAmounts_Vectors (internal/invoice/note_amounts_test.go nmVectors).
const STD = 'STANDARD_VAT'
type V = { name: string; lines: [string, string, string | null, string | null][]; want: [string, string, string] }
const VECTORS: V[] = [
  { name: 'flat1', lines: [['1.5', '33.33', null, null]], want: ['50.00', '3.75', '53.75'] },
  { name: 'flat2', lines: [['1.5', '33.33', null, null], ['1.5', '33.33', null, null]], want: ['99.99', '7.50', '107.49'] },
  { name: 'tie', lines: [['1', '10.06', STD, '7.5'], ['1', '10.06', STD, '7.5'], ['1', '10.06', STD, '7.5']], want: ['30.18', '2.26', '32.44'] },
  { name: 'rem', lines: [['1', '10.07', STD, '7.5'], ['1', '10.09', STD, '7.5'], ['1', '10.08', STD, '7.5']], want: ['30.24', '2.27', '32.51'] },
  {
    name: 'mixed',
    lines: [['1', '10.06', STD, '7.5'], ['1', '5.00', 'ZERO_VAT', '0'], ['2', '10.06', STD, '7.5'], ['1', '4.00', null, null]],
    want: ['39.18', '2.26', '41.44'],
  },
  { name: 'nopct', lines: [['1', '10.00', STD, null]], want: ['10.00', '0.00', '10.00'] },
  { name: 'nopctmix', lines: [['1', '10.06', STD, '7.5'], ['1', '5.00', STD, null]], want: ['15.06', '0.75', '15.81'] },
]

describe('noteDraftLines', () => {
  it('the live total matches the stored total', () => {
    expect(VECTORS).toHaveLength(7)
    for (const v of VECTORS) {
      const picked = v.lines.map(([quantity, unit_price, tax_category, tax_percent], i) => ({
        line: { id: `l${i}`, line_no: i + 1, description: 'x', tax_category, tax_percent } as InvoiceLineItem,
        quantity,
        unit_price,
      }))
      const t = draftTotals(noteDraftLines(picked))
      expect([t.subtotal, t.vat, t.total], v.name).toEqual(v.want)
    }
  })
})

describe('noteDraftLines near the route bounds', () => {
  it('the live total stays exact at the largest quantity and price the route takes', () => {
    const line = { id: 'l', line_no: 1, description: 'x', tax_category: null, tax_percent: null } as InvoiceLineItem
    const t = draftTotals(noteDraftLines([{ line, quantity: '99999999999.999', unit_price: '999999999999.99' }]))
    // exact product 99999999999998000000000.00001, rounded to 2dp
    expect(t.subtotal).toBe('99999999999998000000000.00')
    const small = draftTotals(noteDraftLines([{ line, quantity: '0.001', unit_price: '0.01' }]))
    expect(small.subtotal).toBe('0.00')
    const tidy = draftTotals(noteDraftLines([{ line, quantity: '1.500', unit_price: '33.330' }]))
    expect(tidy.subtotal).toBe('50.00')
  })
})

describe('signedNoteTotal', () => {
  it('signedNoteTotal', () => {
    expect(signedNoteTotal('credit_note', '100.00').startsWith('-₦')).toBe(true)
    expect(signedNoteTotal('debit_note', '100.00').startsWith('₦')).toBe(true)
    expect(signedNoteTotal('credit_note', null)).toBe('—')
    expect(signedNoteTotal('credit_note', '100.00')).not.toMatch(/[−+]/)
    expect(signedNoteTotal('debit_note', '100.00')).not.toMatch(/[−+]/)
    expect(signedNoteTotal('debit_note', null)).toBe('—')
    expect(signedNoteTotal('credit_note', '1234567.00')).toBe('-₦1,234,567')
  })

  it('signedNoteTotal shows a kobo total in whole naira, as the invoice total beside it does', () => {
    expect(signedNoteTotal('debit_note', '53.75')).toBe('₦54')
    expect(signedNoteTotal('credit_note', '0.40')).toBe('-₦0')
  })
})

describe('isDuplicateNumberError', () => {
  it('isDuplicateNumberError names one error', () => {
    expect(isDuplicateNumberError(new ApiError('http', 'duplicate invoice number', 409))).toBe(true)
    expect(isDuplicateNumberError(new ApiError('http', 'a note needs an accepted invoice with an IRN', 409))).toBe(false)
    expect(isDuplicateNumberError(new ApiError('http', 'duplicate invoice number', 400))).toBe(false)
    expect(isDuplicateNumberError(new Error('duplicate invoice number'))).toBe(false)
    expect(isDuplicateNumberError({ status: 409, message: 'duplicate invoice number' })).toBe(false)
  })
})

describe('noteSign and noteTypeBadge', () => {
  it('noteSign and noteTypeBadge carry the contract of CRDDBTNOTE-04', () => {
    expect(noteSign('credit_note')).toBe(-1)
    for (const other of ['debit_note', 'commercial_invoice', undefined, 'unknown'] as const) {
      expect(noteSign(other as never), String(other)).toBe(1)
    }
    expect(noteTypeBadge('credit_note')).toEqual({ label: 'CREDIT', title: 'Credit note' })
    expect(noteTypeBadge('debit_note')).toEqual({ label: 'DEBIT', title: 'Debit note' })
    for (const other of ['commercial_invoice', undefined, 'unknown'] as const) {
      expect(noteTypeBadge(other as never), String(other)).toBeNull()
    }
  })
})

describe('the note routes', () => {
  it('issueNote and getInvoiceNotes hit their routes', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200, json: () => Promise.resolve({}) })
    vi.stubGlobal('fetch', fetchMock)
    const af = createAuthedFetch(() => 'tok', vi.fn())
    const body = {
      document_type: 'credit_note' as const,
      invoice_number: 'CN-INV-1-1',
      issue_date: '2026-10-30T00:00:00Z',
      lines: [{ line_id: 'l1', quantity: '1.000', unit_price: '100.00' }],
    }
    await issueNote(af, 'http://x', 'inv-1', body)
    await getInvoiceNotes(af, 'http://x', 'inv-1')
    const [postUrl, postInit] = fetchMock.mock.calls[0]!
    expect(postUrl).toBe('http://x/api/invoice/v1/invoices/inv-1/notes')
    expect(postInit.method).toBe('POST')
    expect(JSON.parse(postInit.body)).toEqual(body)
    const [getUrl, getInit] = fetchMock.mock.calls[1]!
    expect(getUrl).toBe('http://x/api/invoice/v1/invoices/inv-1/notes')
    expect(getInit?.method ?? 'GET').toBe('GET')
  })
})
