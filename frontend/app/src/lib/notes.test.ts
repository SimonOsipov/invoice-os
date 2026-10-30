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

describe('signedNoteTotal', () => {
  it('signedNoteTotal', () => {
    expect(signedNoteTotal('credit_note', '100.00').startsWith('-₦')).toBe(true)
    expect(signedNoteTotal('debit_note', '100.00').startsWith('₦')).toBe(true)
    expect(signedNoteTotal('credit_note', null)).toBe('—')
    expect(signedNoteTotal('credit_note', '100.00')).not.toMatch(/[−+]/)
    expect(signedNoteTotal('debit_note', '100.00')).not.toMatch(/[−+]/)
  })
})

describe('isDuplicateNumberError', () => {
  it('isDuplicateNumberError names one error', () => {
    expect(isDuplicateNumberError(new ApiError('http', 'duplicate invoice number', 409))).toBe(true)
    expect(isDuplicateNumberError(new ApiError('http', 'a note needs an accepted invoice with an IRN', 409))).toBe(false)
    expect(isDuplicateNumberError(new ApiError('http', 'duplicate invoice number', 400))).toBe(false)
    expect(isDuplicateNumberError(new Error('duplicate invoice number'))).toBe(false)
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
