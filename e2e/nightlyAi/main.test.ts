import { mkdtempSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

import { run, type CallsPage, type Deps, type TotalsPage } from './main'
import type { KeyRow } from './score'

const T = new Date('2026-11-01T02:00:00.000Z')
const TENANT = '7c1f5c52-0000-4000-8000-00000000a001'
// Persona A's tenant, e2e/topology/targets.ts TENANTS.a.
const PERSONA_A_TENANT = '11111111-1111-1111-1111-111111111111'
const FIXTURE = new URL('../../internal/tools/nightlysummary/testdata/ai/ai.json', import.meta.url)
const FIELDS = ['invoice_number', 'issue_date', 'buyer_tin', 'buyer_name', 'currency', 'subtotal', 'vat', 'total']
const STEPS = ['extraction', 'suggest-mapping', 'explain', 'check-mapping', 'accuracy']

const keyRow = (file: string): KeyRow => ({ file, kind: 'text', fields: Object.fromEntries(FIELDS.map((f) => [f, [`${f}-want`]])) })
const KEY = [keyRow('corpus_inline_labels.pdf'), keyRow('nightly_text_01.pdf')]

type Row = { tenant_id: string; created_at: string; client: string; purpose: string; outcome: string; cost: string }
type Opts = {
  skipRows?: string[]
  rowAgeMs?: number
  pageSize?: number
  outcomes?: Record<string, string>
  stray?: Row[]
  totals?: (query: Record<string, string>) => Promise<TotalsPage>
  provision?: () => Promise<never>
}

function fake(opts: Opts = {}) {
  const rows: Row[] = [...(opts.stray ?? [])]
  const calls: Record<string, number> = {}
  const listQueries: Record<string, string>[] = []
  const totalsQueries: Record<string, string>[] = []
  const uploads: string[] = []
  const count = (name: string) => (calls[name] = (calls[name] ?? 0) + 1)
  const emit = (pair: string, cost = '0.0001') => {
    if (opts.skipRows?.includes(pair)) return
    const [client, purpose] = pair.split('/')
    rows.push({ tenant_id: TENANT, created_at: new Date(T.getTime() - (opts.rowAgeMs ?? 0)).toISOString(), client, purpose, outcome: opts.outcomes?.[pair] ?? 'ok', cost })
  }
  const deps: Deps = {
    now: () => T,
    sleep: async () => {},
    log: () => {},
    readKey: () => KEY,
    readPdf: () => new Uint8Array([1]),
    provisionRealAccount: opts.provision ?? (async () => ({ email: 'a@example.com', password: 'pw', workspaceName: 'w', displayName: 'd' })),
    provisionStaffAccount: async () => ({ email: 's@example.com', password: 'pw', userId: 'u' }),
    signInSession: async (email: string) => ({ access_token: email.startsWith('s@') ? 'staff-token' : 'user-token', refresh_token: 'r' }),
    me: async () => ({ tenant: { id: TENANT } }) as never,
    createEntity: async () => ({ id: 'entity-1' }) as never,
    createInvoice: async () => ({ id: 'invoice-1' }) as never,
    validateInvoice: async () => ({}) as never,
    explainViolation: async () => {
      count('explain')
      emit('ai/explain')
      return {} as never
    },
    suggestMapping: async () => {
      count('suggest')
      emit('ai/spreadsheet')
      return {} as never
    },
    checkMapping: async () => {
      count('check')
      emit('jev/mapping_check')
      return {} as never
    },
    previewCsv: async () => 'csv-document',
    uploadPdf: async (_token, file) => {
      uploads.push(file)
      emit('ai/document')
      emit('jev/document_type', '0')
      return `doc:${file}`
    },
    getExtractions: async (_token, documentId) => ({ jobs: [{ id: `job:${documentId}`, state: 'succeeded' }] }) as never,
    getExtractionDetail: async (_token, jobId) => {
      const file = jobId.slice('job:doc:'.length)
      const bad = file === 'nightly_text_01.pdf'
      return { fields: FIELDS.map((f) => ({ name: f, value: bad && f === 'total' ? 'wrong' : `${f}-want` })) } as never
    },
    listCalls: async (token, query) => {
      expect(token).toBe('staff-token')
      listQueries.push(query)
      const matching = rows.filter((r) => r.tenant_id === query.tenant && Date.parse(r.created_at) >= Date.parse(query.from))
      const start = query.cursor ? Number(query.cursor) : 0
      const size = opts.pageSize ?? 100
      const page = matching.slice(start, start + size)
      const more = start + size < matching.length
      return { calls: page, page: { has_more: more, next_cursor: more ? String(start + size) : null } } satisfies CallsPage
    },
    listTotals: async (token, query) => {
      expect(token).toBe('staff-token')
      totalsQueries.push(query)
      if (opts.totals) return opts.totals(query)
      if (!query.cursor) return { totals: [{ cost: '0.000300' }], page: { has_more: true, next_cursor: 'p2' } }
      return { totals: [{ cost: '0.000200' }], page: { has_more: false, next_cursor: null } }
    },
  }
  return { deps, rows, calls, listQueries, totalsQueries, uploads }
}

const outFile = () => join(mkdtempSync(join(tmpdir(), 'nightly-ai-')), 'ai.json')
const read = (file: string) => JSON.parse(readFileSync(file, 'utf8')) as { ok: boolean; cost_usd: string | null; steps: { name: string; status: string; detail?: string }[] }
const status = (file: string, name: string) => read(file).steps.find((s) => s.name === name)!

describe('run', () => {
  it('calls each route once and maps each purpose to its step', async () => {
    const f = fake()
    const out = outFile()
    expect(await run(f.deps, out)).toBe(0)
    expect(f.calls).toEqual({ suggest: 1, explain: 1, check: 1 })
    expect(f.uploads.sort()).toEqual(KEY.map((k) => k.file).sort())
    expect(read(out).steps.map((s) => s.status)).toEqual(['passed', 'passed', 'passed', 'passed', 'passed'])

    for (const [pair, step] of [['ai/document', 'extraction'], ['ai/spreadsheet', 'suggest-mapping'], ['ai/explain', 'explain'], ['jev/mapping_check', 'check-mapping']] as const) {
      const g = fake({ skipRows: [pair] })
      const o = outFile()
      await run(g.deps, o)
      const failed = read(o).steps.filter((s) => s.status === 'failed')
      expect(failed.map((s) => s.name), `without ${pair}`).toEqual([step])
      expect(failed[0].detail).toContain(pair)
    }
  })

  it('writes the section of the shared fixture', async () => {
    const out = outFile()
    await run(fake().deps, out)
    expect(readFileSync(out, 'utf8')).toBe(readFileSync(FIXTURE, 'utf8'))
  })

  it('writes all steps failed on a setup error, with no token or address in the file', async () => {
    const jwt = ['eyJhbGciOi', 'eyJzdWIiOi', 'c2lnbmF0dXJl'].join('.')
    const f = fake({
      provision: async () => {
        throw new Error(`register answered 500 for jane.doe@corp.test with ${jwt}`)
      },
    })
    const out = outFile()
    expect(await run(f.deps, out)).toBe(0)
    const text = readFileSync(out, 'utf8')
    const section = read(out)
    expect(section.ok).toBe(false)
    expect(section.steps.map((s) => [s.name, s.status])).toEqual(STEPS.map((n) => [n, 'failed']))
    expect(text).not.toContain(jwt)
    expect(text).not.toContain('jane.doe@corp.test')
    expect(text).toContain('setup failed')
  })

  it('returns 0 when it wrote the file, though a step failed', async () => {
    const out = outFile()
    expect(await run(fake({ outcomes: { 'ai/explain': 'unavailable' } }).deps, out)).toBe(0)
    expect(read(out).ok).toBe(false)
    expect(status(out, 'explain').status).toBe('failed')
  })

  it('writes null cost on a failed totals read and changes no step', async () => {
    const out = outFile()
    await run(fake({ totals: async () => { throw new Error('boom') } }).deps, out)
    expect(read(out)).toMatchObject({ ok: true, cost_usd: null })
  })

  it('sums every page of the totals and sends the cursor', async () => {
    const f = fake()
    const out = outFile()
    await run(f.deps, out)
    expect(read(out).cost_usd).toBe('0.000500')
    expect(f.totalsQueries.map((q) => q.cursor)).toEqual([undefined, 'p2'])
  })

  it('writes null cost when a later page fails, not the first page sum', async () => {
    const out = outFile()
    await run(fake({ totals: async (q) => { if (q.cursor) throw new Error('page 2'); return { totals: [{ cost: '0.5' }], page: { has_more: true, next_cursor: 'p2' } } } }).deps, out)
    expect(read(out).cost_usd).toBeNull()
  })

  it('writes null cost on an endless cursor, after 50 pages', async () => {
    const f = fake({ totals: async () => ({ totals: [{ cost: '0.1' }], page: { has_more: true, next_cursor: 'again' } }) })
    const out = outFile()
    await run(f.deps, out)
    expect(f.totalsQueries).toHaveLength(50)
    expect(read(out)).toMatchObject({ ok: true, cost_usd: null })
  })

  it('reads the rows of the tenant it created', async () => {
    const f = fake()
    await run(f.deps, outFile())
    expect(f.listQueries.length).toBeGreaterThan(0)
    for (const q of f.listQueries) expect(q.tenant).toBe(TENANT)
    expect(f.listQueries.some((q) => q.tenant === PERSONA_A_TENANT)).toBe(false)
  })

  it('sets from 60 s behind its clock', async () => {
    const f = fake()
    await run(f.deps, outFile())
    for (const q of f.listQueries) expect(q.from).toBe('2026-11-01T01:59:00.000Z')
  })

  it('keeps a row dated 30 s behind the clock and loses one 61 s behind', async () => {
    const near = outFile()
    await run(fake({ rowAgeMs: 30_000 }).deps, near)
    expect(status(near, 'extraction').status).toBe('passed')
    const far = outFile()
    await run(fake({ rowAgeMs: 61_000 }).deps, far)
    expect(status(far, 'extraction')).toMatchObject({ status: 'failed' })
    expect(status(far, 'extraction').detail).toContain('ai/document')
  })

  it('ignores a stray row of another tenant', async () => {
    const stray = { tenant_id: PERSONA_A_TENANT, created_at: T.toISOString(), client: 'ai', purpose: 'document', outcome: 'unavailable', cost: '0' }
    const out = outFile()
    await run(fake({ stray: [stray] }).deps, out)
    expect(status(out, 'extraction').status).toBe('passed')
  })

  it('finds a required row on page two', async () => {
    const f = fake({ pageSize: 1 })
    const out = outFile()
    await run(f.deps, out)
    expect(read(out).steps.map((s) => s.status)).toEqual(['passed', 'passed', 'passed', 'passed', 'passed'])
    expect(f.listQueries.some((q) => q.cursor !== undefined)).toBe(true)
  })

  it('fails the accuracy step under the threshold', async () => {
    const f = fake()
    f.deps.getExtractionDetail = async () => ({ fields: FIELDS.map((n) => ({ name: n, value: 'wrong' })) }) as never
    const out = outFile()
    await run(f.deps, out)
    expect(status(out, 'accuracy').status).toBe('failed')
    expect(status(out, 'accuracy').detail).toContain('provisional')
  })
})
