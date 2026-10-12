// The NRS code-list search over the deployed gateway: the D2 contract of GET /v1/code-lists/{list}.
import { test, expect } from '@playwright/test'
import { login, PERSONAS, rawFetch, searchCodes } from './client'
import { assertErrorEnvelope } from './contract-helpers'

// internal/invoice/codes.go's codeSearchLists.
const LISTS = ['states', 'lgas', 'hs-codes', 'services-codes', 'invoice-quantity-codes', 'tax-categories']
// internal/invoice/codes.go's CodeSearchHandler messages.
const UNKNOWN_LIST = 'unknown code list'
const LIMIT_INT = 'limit must be an integer'
const LIMIT_MIN = 'limit must be >= 1'

test.describe('code-list search contract (API E2E, over the deployed gateway)', () => {
  let token: string

  // A PR environment seeds the code lists about 30 s after boot.
  test.beforeAll(async () => {
    test.setTimeout(150_000)
    token = await login(PERSONAS.A)
    await expect
      .poll(async () => (await searchCodes(token, 'states', { q: 'NG-LA' })).codes.map((c) => c.code), { timeout: 120_000 })
      .toContain('NG-LA')
  })

  const get = (path: string, auth = true) =>
    rawFetch(`/api/invoice/v1/code-lists/${path}`, auth ? { headers: { Authorization: `Bearer ${token}` } } : {})

  test('code lists: an allowed list answers code, label and percent', async () => {
    const tax = await searchCodes(token, 'tax-categories', { q: 'STANDARD_VAT' })
    expect(tax.codes[0]).toMatchObject({ code: 'STANDARD_VAT', percent: '7.5' })
    expect(tax.codes[0].label).not.toBe('')

    const states = await searchCodes(token, 'states', { q: 'Lagos' })
    expect(states.codes[0]).toMatchObject({ code: 'NG-LA', percent: null })
  })

  test('code lists: every allowed list returns codes', async () => {
    for (const list of LISTS) {
      const { codes } = await searchCodes(token, list, { q: '' })
      expect(codes.length, `${list} returned no codes`).toBeGreaterThanOrEqual(1)
    }
  })

  test('code lists: unknown list 404, bad limit 400, no token 401', async () => {
    const unknown = await get('nope')
    assertErrorEnvelope(unknown, 404, 'unknown list')
    expect((unknown.body as { error: string }).error).toBe(UNKNOWN_LIST)

    const zero = await get('states?limit=0')
    assertErrorEnvelope(zero, 400, 'limit 0')
    expect((zero.body as { error: string }).error).toBe(LIMIT_MIN)

    const word = await get('states?limit=abc')
    assertErrorEnvelope(word, 400, 'limit abc')
    expect((word.body as { error: string }).error).toBe(LIMIT_INT)

    assertErrorEnvelope(await get('states', false), 401, 'no token')
  })
})
