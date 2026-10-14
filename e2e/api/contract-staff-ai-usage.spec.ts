// The staff AI usage contract over the wire: the call list holds this spec's two AI calls with every field, filters and
// paging hold, the totals equal the list, and a customer token or a write method is refused.
// Acts only on rows the spec creates: a fresh entity, one upload, one suggest-mapping call and one check-mapping call in PERSONAS.A's tenant.
import { test, expect } from '@playwright/test'
import { apiBase, checkMapping, createEntity, login, PERSONAS, provisionStaffAccount, rawFetch, signInSession, suggestMapping } from './client'
import { freshTin } from './fixtures'
import { assertErrorEnvelope } from './contract-helpers'

const STAFF_CALLS = '/api/invoice/v1/staff/ai-calls'
const STAFF_TOTALS = '/api/invoice/v1/staff/ai-calls/totals'

// ai.Model, internal/platform/ai/client.go.
const AI_MODEL = 'google/gemini-3.5-flash-lite'
// ai.PurposeSpreadsheet, internal/platform/ai/client.go.
const AI_PURPOSE = 'spreadsheet'
// jev.PurposeMappingCheck, internal/platform/jev/client.go.
const JEV_PURPOSE = 'mapping_check'
// The fake clients' outcome, internal/platform/ai/fake.go and internal/platform/jev/fake.go.
const FAKE_OUTCOME = 'fake'
// parseFilter, internal/aicalls/staff_read.go.
const INVALID_TENANT = 'invalid tenant'
const LIMIT_MESSAGE = 'limit must be >= 1'
const RANGE_MESSAGE = 'from must not be after to'
const FAILED_MESSAGE = 'failed must be true or false'
const CURSOR_MESSAGE = 'cursor is malformed'
// maxLimit, internal/aicalls/staff_read.go.
const MAX_LIMIT = 100

const DAY_MS = 86_400_000
const IMPORT_CSV = ['Invoice No,Issue Date,Buyer TIN,Buyer,Currency,Subtotal,VAT,Total,Item,Qty,Unit Price', 'INV-SUPP-10,2026-01-15,87654321-0002,SUPP-10 Buyer,NGN,1000.00,75.00,1075.00,Item 1,1,100.00'].join('\n')

type Call = {
  id: string
  tenant_id: string
  tenant_name: string
  created_at: string
  client: string
  purpose: string
  model: string
  input_tokens: number
  output_tokens: number
  cost: string
  latency_ms: number
  attempts: number
  outcome: string
}
type Total = { day: string; tenant_id: string; tenant_name: string; calls: number; input_tokens: number; output_tokens: number; cost: string; failed: number }
type PageInfo = { limit: number; has_more: boolean; next_cursor: string | null }
type CallsPage = { calls: Call[]; page: PageInfo }
type TotalsPage = { totals: Total[]; page: PageInfo }

const bearer = (token: string): Record<string, string> => ({ Authorization: `Bearer ${token}` })

// Decimal strings compared by integer scaling: a float sum could not equal the database's numeric sum.
function sumEquals(total: string, parts: string[]): boolean {
  const fracLen = (v: string): number => (v.split('.')[1] ?? '').length
  const scale = Math.max(fracLen(total), ...parts.map(fracLen))
  const scaled = (v: string): bigint => {
    const [whole, frac = ''] = v.split('.')
    return BigInt(whole + frac.padEnd(scale, '0'))
  }
  return scaled(total) === parts.reduce((acc, v) => acc + scaled(v), 0n)
}

async function previewDocument(token: string, csv: string): Promise<string> {
  const form = new FormData()
  form.set('file', new Blob([csv], { type: 'text/csv' }), 'import.csv')
  const res = await fetch(`${apiBase()}/api/invoice/v1/imports/preview`, { method: 'POST', headers: bearer(token), body: form })
  const body = (await res.json()) as { document_id?: string }
  expect(typeof body.document_id, `preview carries a document_id (status ${res.status})`).toBe('string')
  return body.document_id as string
}

test.describe('staff AI usage (API E2E, over the deployed gateway)', () => {
  let customer: string
  let staff: string
  let start: string
  let aiId: string
  let jevId: string
  let aiCreated: string

  const staffGet = (path: string) => rawFetch(path, { headers: bearer(staff) })

  async function calls(query: Record<string, string>): Promise<CallsPage> {
    const res = await staffGet(`${STAFF_CALLS}?${new URLSearchParams(query)}`)
    expect(res.status, `staff calls ${JSON.stringify(query)}`).toBe(200)
    return res.body as CallsPage
  }

  async function totals(query: Record<string, string>): Promise<TotalsPage> {
    const res = await staffGet(`${STAFF_TOTALS}?${new URLSearchParams(query)}`)
    expect(res.status, `staff totals ${JSON.stringify(query)}`).toBe(200)
    return res.body as TotalsPage
  }

  async function allCalls(query: Record<string, string>): Promise<Call[]> {
    const out: Call[] = []
    let cursor: string | null = null
    for (let pages = 0; pages < 50; pages++) {
      const page: CallsPage = await calls({ ...query, limit: String(MAX_LIMIT), ...(cursor ? { cursor } : {}) })
      out.push(...page.calls)
      if (!page.page.has_more) return out
      cursor = page.page.next_cursor
    }
    throw new Error('the call list did not end within 50 pages')
  }

  const forA = (): Record<string, string> => ({ tenant: PERSONAS.A.tenantId, from: start })

  test.beforeAll(async () => {
    // ceiling: assumes the runner and gateway clocks agree within 5 s; widen the margin if this spec flakes on a missing row.
    start = new Date(Date.now() - 5_000).toISOString()
    customer = await login(PERSONAS.A)
    const account = await provisionStaffAccount('staff-ai-usage')
    staff = (await signInSession(account.email, account.password)).access_token

    const tin = freshTin()
    const entity = await createEntity(customer, { name: `Zz SUPP-10 ${tin}`, tin })
    const documentId = await previewDocument(customer, IMPORT_CSV)
    await suggestMapping(customer, { entity_id: entity.id, document_id: documentId })
    await checkMapping(customer, { document_id: documentId, mapping: { invoice_number: 'Invoice No' } })

    // The recorder writes after the response, so the rows land in the background.
    await expect
      .poll(
        async () => {
          const found = (await calls({ ...forA(), limit: String(MAX_LIMIT) })).calls
          const ai = found.find((c) => c.client === 'ai' && c.purpose === AI_PURPOSE)
          aiId = ai?.id ?? ''
          aiCreated = ai?.created_at ?? ''
          jevId = found.find((c) => c.client === 'jev' && c.purpose === JEV_PURPOSE)?.id ?? ''
          return aiId !== '' && jevId !== ''
        },
        { message: "the spec's ai and jev rows are listed", timeout: 15_000 },
      )
      .toBe(true)
  })

  test("staff ai calls: the spec's calls are listed with every field; filters and paging hold; refusals", async () => {
    const list = (await calls({ ...forA(), limit: String(MAX_LIMIT) })).calls
    const ai = list.find((c) => c.id === aiId)
    const jev = list.find((c) => c.id === jevId)
    expect(ai, 'the ai row is listed').toBeDefined()
    expect(jev, 'the jev row is listed').toBeDefined()
    // Fake clients report zero usage, so tokens are 0 and cost is "0".
    expect(ai, 'the ai row').toMatchObject({ tenant_id: PERSONAS.A.tenantId, tenant_name: PERSONAS.A.name, client: 'ai', purpose: AI_PURPOSE, model: AI_MODEL, input_tokens: 0, output_tokens: 0, cost: '0', attempts: 1, outcome: FAKE_OUTCOME })
    expect(jev, 'the jev row').toMatchObject({ tenant_id: PERSONAS.A.tenantId, tenant_name: PERSONAS.A.name, client: 'jev', purpose: JEV_PURPOSE, input_tokens: 0, output_tokens: 0, cost: '0', attempts: 1, outcome: FAKE_OUTCOME })
    for (let i = 1; i < list.length; i++) {
      expect(Date.parse(list[i].created_at), `row ${i} is not newer than row ${i - 1}`).toBeLessThanOrEqual(Date.parse(list[i - 1].created_at))
    }

    const byPurpose = (await calls({ ...forA(), purpose: JEV_PURPOSE, limit: String(MAX_LIMIT) })).calls
    expect(byPurpose.map((c) => c.id), 'the purpose filter holds the jev row').toContain(jevId)
    for (const c of byPurpose) expect([c.client, c.purpose], `row ${c.id} under purpose=${JEV_PURPOSE}`).toEqual(['jev', JEV_PURPOSE])
    const byOutcome = (await calls({ ...forA(), outcome: FAKE_OUTCOME, limit: String(MAX_LIMIT) })).calls.map((c) => c.id)
    expect(byOutcome, 'the outcome filter holds both rows').toEqual(expect.arrayContaining([aiId, jevId]))

    expect(list.length, 'two pages need two rows').toBeGreaterThanOrEqual(2)
    const one = await calls({ ...forA(), limit: '1' })
    expect(one.page.has_more, 'page 1 has more').toBe(true)
    expect(one.page.next_cursor, 'page 1 names a cursor').toBeTruthy()
    const two = await calls({ ...forA(), limit: '1', cursor: one.page.next_cursor! })
    expect([one.calls.length, two.calls.length], 'each page holds one row').toEqual([1, 1])
    expect([one.calls[0].id, two.calls[0].id], 'the two pages are the first two rows: no overlap, no skip').toEqual(list.slice(0, 2).map((c) => c.id))

    const refuse = async (query: string, message: string) => {
      const res = await staffGet(`${STAFF_CALLS}?${query}`)
      assertErrorEnvelope(res, 400, query)
      expect((res.body as { error: string }).error, query).toBe(message)
    }
    await refuse('tenant=nope', INVALID_TENANT)
    await refuse('limit=0', LIMIT_MESSAGE)
    await refuse(`from=${encodeURIComponent('2030-01-02T00:00:00Z')}&to=${encodeURIComponent('2030-01-01T00:00:00Z')}`, RANGE_MESSAGE)
    await refuse('failed=maybe', FAILED_MESSAGE)
    await refuse('cursor=garbage', CURSOR_MESSAGE)
  })

  test('staff ai calls: failed=true keeps only failed calls', async () => {
    const failed = (await calls({ ...forA(), failed: 'true', limit: String(MAX_LIMIT) })).calls
    for (const c of failed) expect(c.outcome, `row ${c.id} under failed=true`).not.toBe('ok')
    expect(failed.map((c) => c.id), 'the fake rows are non-ok, so both are listed').toEqual(expect.arrayContaining([aiId, jevId]))
  })

  test('staff ai totals equal the call list they summarise', async () => {
    // The spec's own row names the UTC day: a run that crosses midnight must not read the wrong one.
    const dayStart = Math.floor(Date.parse(aiCreated) / DAY_MS) * DAY_MS
    const today = new Date(dayStart).toISOString().slice(0, 10)
    const todayQuery = { tenant: PERSONAS.A.tenantId, from: new Date(dayStart).toISOString(), to: new Date(dayStart + DAY_MS - 1).toISOString() }

    // The list sits between two totals reads: a row landing mid-read makes them differ, so read again.
    let t1: TotalsPage | undefined
    let list: Call[] = []
    for (let attempt = 0; attempt < 3; attempt++) {
      t1 = await totals({ ...todayQuery, limit: String(MAX_LIMIT) })
      list = await allCalls(todayQuery)
      const t2 = await totals({ ...todayQuery, limit: String(MAX_LIMIT) })
      if (JSON.stringify(t1.totals) === JSON.stringify(t2.totals)) break
      t1 = undefined
    }
    expect(t1, 'two equal totals reads bracket the list').toBeDefined()

    const row = t1!.totals.find((t) => t.day === today && t.tenant_id === PERSONAS.A.tenantId)
    expect(row, "today's totals row for tenant A").toBeDefined()
    expect(row!.calls, 'calls equal the list length').toBe(list.length)
    expect(row!.input_tokens, 'tokens in').toBe(list.reduce((n, c) => n + c.input_tokens, 0))
    expect(row!.output_tokens, 'tokens out').toBe(list.reduce((n, c) => n + c.output_tokens, 0))
    expect(sumEquals(row!.cost, list.map((c) => c.cost)), 'cost equals the summed list').toBe(true)
    expect(row!.failed, 'failed equals the non-ok rows').toBe(list.filter((c) => c.outcome !== 'ok').length)

    const yesterday = await totals({ ...todayQuery, to: new Date(dayStart - 1).toISOString(), limit: String(MAX_LIMIT) })
    expect(yesterday.totals.map((t) => t.day), 'a read ending yesterday holds no row for today').not.toContain(today)

    const week = { tenant: PERSONAS.A.tenantId, from: new Date(dayStart - 7 * DAY_MS).toISOString() }
    const keyOf = (t: Total): string => `${t.day}|${t.tenant_id}`
    const whole = (await totals({ ...week, limit: String(MAX_LIMIT) })).totals.map(keyOf)
    const paged: string[] = []
    let cursor: string | null = null
    for (let pages = 0; pages < 20; pages++) {
      const page: TotalsPage = await totals({ ...week, limit: '1', ...(cursor ? { cursor } : {}) })
      paged.push(...page.totals.map(keyOf))
      if (!page.page.has_more) break
      cursor = page.page.next_cursor
    }
    expect(new Set(paged).size, 'no group repeats across pages').toBe(paged.length)
    expect(paged, 'limit=1 pages reach the groups of one limit=100 read, in order').toEqual(whole)
  })

  test('a customer token is refused on both staff ai paths', async () => {
    for (const path of [STAFF_CALLS, STAFF_TOTALS]) {
      const res = await rawFetch(path, { headers: bearer(customer) })
      assertErrorEnvelope(res, 403, `customer ${path}`)
      expect(res.body, `customer ${path}`).toEqual({ error: 'forbidden' })
    }
  })

  test('no write method exists on the staff ai paths', async () => {
    const before = (await allCalls(forA())).map((c) => c.id)
    for (const path of [STAFF_CALLS, STAFF_TOTALS]) {
      for (const method of ['POST', 'PUT', 'PATCH', 'DELETE']) {
        const res = await rawFetch(path, { method, headers: bearer(staff) })
        expect(res.status, `${method} ${path}`).toBeGreaterThanOrEqual(400)
        expect(res.status, `${method} ${path}`).toBeLessThan(500)
      }
    }
    expect((await allCalls(forA())).map((c) => c.id), 'the same ids as before the writes').toEqual(before)
  })
})
