// The NRS code-list search over the deployed gateway: the D2 contract of GET /v1/code-lists/{list}.
import { test, expect } from '@playwright/test'
import { login, PERSONAS, rawFetch, searchCodes } from './client'
import { assertErrorEnvelope, CODE_LISTS, codeListsReady } from './contract-helpers'

// internal/invoice/codes.go's CodeSearchHandler messages.
const UNKNOWN_LIST = 'unknown code list'
const LIMIT_INT = 'limit must be an integer'
const LIMIT_MIN = 'limit must be >= 1'
const QUERY_LONG = 'q must be at most 100 characters'
const MAX_LIMIT = 50

test.describe('code-list search contract (API E2E, over the deployed gateway)', () => {
  let token: string

  test.beforeAll(async () => {
    test.setTimeout(150_000)
    token = await login(PERSONAS.A)
    await codeListsReady(token)
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
    for (const list of CODE_LISTS) {
      const { codes } = await searchCodes(token, list, { q: '' })
      expect(codes.length, `${list} returned no codes`).toBeGreaterThanOrEqual(1)
      expect(codes[0].label, `${list} first label is not blank`).not.toBe('')
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

  test('code lists: tax percent only when numeric, exact code first, a miss answers an empty list', async () => {
    const { codes } = await searchCodes(token, 'tax-categories', { q: '', limit: MAX_LIMIT })
    expect(codes.length, 'tax-categories holds codes').toBeGreaterThan(0)
    expect(codes.find((c) => c.code === 'ZERO_VAT')?.percent, 'a zero rate is a number').toBe('0.0')
    expect(codes.some((c) => c.percent === null), 'a category with no published rate answers null').toBe(true)

    const exact = await searchCodes(token, 'states', { q: 'NG-LA' })
    expect(exact.codes[0].code).toBe('NG-LA')

    const miss = await get('states?q=zzzzqqqq')
    expect(miss.status).toBe(200)
    expect(miss.body).toEqual({ codes: [] })
  })

  test('code lists: q over 100 characters 400, limit clamps to 50', async () => {
    const long = await get(`states?q=${'a'.repeat(101)}`)
    assertErrorEnvelope(long, 400, 'q 101 chars')
    expect((long.body as { error: string }).error).toBe(QUERY_LONG)

    const clamped = await searchCodes(token, 'lgas', { q: '', limit: 1000 })
    expect(clamped.codes, 'lgas holds more than the clamp').toHaveLength(MAX_LIMIT)
    expect((await searchCodes(token, 'lgas', { q: '', limit: 1 })).codes).toHaveLength(1)
  })
})
