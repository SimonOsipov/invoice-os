import { describe, expect, it } from 'vitest'

import { csvCell } from './csvCell'

describe('csvCell', () => {
  it('csvCell_neutralisesAHyperlinkFormula', () => {
    expect(csvCell('=HYPERLINK("https://example.test","x")')).toBe(`"'=HYPERLINK(""https://example.test"",""x"")"`)
  })

  it.each(['=1', '+1', '-1', '@x', "'=1", '=', '+', '-', '@', "'"])(
    'csvCell_prefixesEachFormulaLead: %j',
    (input) => {
      expect(csvCell(input)).toBe(`'${input}`)
    },
  )

  it('csvCell_prefixesATabOrCarriageReturnLeadThenQuotes', () => {
    expect(csvCell('\t=1')).toBe("'\t=1")
    expect(csvCell('\r=1')).toBe(`"'\r=1"`)
  })

  it('csvCell_leavesOtherCellsAsBefore', () => {
    expect(['Acme', 'a=b', ' =1', 'a,b', 'say "hi"', '—', ''].map(csvCell)).toEqual([
      'Acme', 'a=b', ' =1', '"a,b"', '"say ""hi"""', '—', '',
    ])
  })
})
