import { describe, expect, it, vi } from 'vitest'

import { CODE_LIST_OF, CODE_LISTS, commitCode, searchCodes, type CodeEntry } from './codeLists'

const entry = (code: string): CodeEntry => ({ code, label: code, percent: null })

describe('codeLists', () => {
  it('searchCodes_buildsTheEncodedUrl', async () => {
    const fetch = vi.fn().mockResolvedValue({ codes: [entry('NG-LA')] })
    const got = await searchCodes(fetch, 'https://gw.test', 'lgas', 'Aba N&', 20)
    expect(fetch).toHaveBeenCalledWith('https://gw.test/api/invoice/v1/code-lists/lgas?q=Aba%20N%26&limit=20')
    expect(got).toEqual([entry('NG-LA')])
  })

  it('commitCode_exactMatchOnly', () => {
    const results = [entry('NG-LA'), entry('NG-LAG')]
    expect(commitCode('NG-LA', results)).toBe(results[0])
    expect(commitCode('ng-la', results)).toBeNull()
    expect(commitCode('NG-L', results)).toBeNull()
    expect(commitCode('NG-LA ', results)).toBeNull()
  })

  it('codeListOf_coversEveryListBackedField', () => {
    expect(CODE_LIST_OF).toEqual({
      hsn_code: 'hs-codes',
      isic_code: 'services-codes',
      price_unit: 'invoice-quantity-codes',
      supplier_state: 'states',
      buyer_state: 'states',
      supplier_lga: 'lgas',
      buyer_lga: 'lgas',
      tax_category: 'tax-categories',
    })
    for (const list of Object.values(CODE_LIST_OF)) expect(CODE_LISTS).toContain(list)
    for (const k of ['supplier_country', 'buyer_country', 'tax_currency_code']) expect(CODE_LIST_OF).not.toHaveProperty(k)
  })
})
