import { describe, expect, it } from 'vitest'
import { SIZE_SENTENCE, TYPE_SENTENCE } from './copy'
import { MAX_UPLOAD_BYTES } from './fields'
import { canSend, fileVerdict, kindOf } from './flow'

const file = (name: string, size = 10, type = '') => new File([new Uint8Array(size)], name, { type })

describe('fileVerdict', () => {
  it('fileVerdict_refusesAnUnknownExtension', () => {
    expect(fileVerdict(file('invoice.txt'))).toBe(TYPE_SENTENCE)
  })

  it('fileVerdict_refusesAPdfTypeWithNoExtension', () => {
    expect(fileVerdict(file('invoice', 10, 'application/pdf'))).toBe(TYPE_SENTENCE)
  })

  it('fileVerdict_refusesOneByteOverTheCap', () => {
    expect(fileVerdict({ name: 'a.csv', size: MAX_UPLOAD_BYTES + 1 })).toBe(SIZE_SENTENCE)
  })

  it('fileVerdict_acceptsExactlyTheCap', () => {
    const f = { name: 'a.pdf', size: MAX_UPLOAD_BYTES }
    expect(fileVerdict(f)).toBe('')
    expect(kindOf(f)).toBe('document')
  })

  it('kindOf_readsTheExtensionCaseBlind', () => {
    expect(kindOf({ name: 'A.XLSX' })).toBe('spreadsheet')
    expect(kindOf({ name: 'b.Docx' })).toBe('document')
  })
})

describe('canSend', () => {
  const sheet = file('a.csv')
  const placed = { invoice_number: 'Inv' }
  const ready = { file: sheet, email: 'a@example.com', ticked: true, fields: placed }

  it('canSend_needsTheTick', () => {
    expect(canSend(ready)).toBe(true)
    expect(canSend({ ...ready, ticked: false })).toBe(false)
  })

  it('canSend_needsAnEmail', () => {
    expect(canSend({ ...ready, email: '  ' })).toBe(false)
  })

  it.each([
    ['ada@adebayofoods', false],
    ['a@b', false],
    ['a@b.c', false],
    ['a b@x.io', false],
    ['a@@x.io', false],
    ['a@x.io', true],
    ['  a@x.io ', true],
  ])('canSend_needsAWellShapedAddress %j', (email, want) => {
    expect(canSend({ file: file('a.pdf'), email, ticked: true, fields: null })).toBe(want)
  })

  it('canSend_aSpreadsheetNeedsTheInvoiceNumber', () => {
    const unplaced = { ...ready, fields: { invoice_number: null } }
    expect(canSend(unplaced)).toBe(false)
    expect(canSend({ ...unplaced, file: file('a.pdf') })).toBe(true)
  })
})
