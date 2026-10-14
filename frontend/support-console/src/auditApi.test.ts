import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { stripComments } from '@invoice-os/api-client/strip-comments'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SESSION_KEY } from './auth'
import { ApiError } from '@invoice-os/api-client'
import { auditStamp, BUNDLE_INVOICE_LIMIT, defaultBundlePeriod, downloadBundle, fetchAuditEvents, fetchBundlePreview, fetchStaffEntities, staffChangeLink, utcDay, type StaffChange } from './auditApi'

const GW = 'https://gw.test'
const ROUTE = `${GW}/api/invoice/v1/staff/audit-log`

let fetchMock: ReturnType<typeof vi.fn>
beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', GW + '/')
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => (k === SESSION_KEY ? JSON.stringify({ v: 2, token: 'T', refresh_token: 'R' }) : null),
  })
  fetchMock = vi.fn(async () => new Response(JSON.stringify({ events: [], page: { limit: 50, has_more: false, next_cursor: null } }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

const lastQuery = () => Object.fromEntries(new URL(fetchMock.mock.calls.at(-1)![0]).searchParams)

describe('auditApi', () => {
  it('fetchAuditEvents builds the query from the filters', async () => {
    await fetchAuditEvents({})
    expect(fetchMock.mock.calls[0][0]).toBe(`${ROUTE}?limit=50`)

    const id = '11111111-1111-1111-1111-111111111111'
    await fetchAuditEvents({ tenant: id, event: 'invoice.created', actor: 'system', from: '2026-10-01', to: '2026-10-03' }, 'CUR')
    expect(lastQuery()).toEqual({
      limit: '50',
      tenant: id,
      event: 'invoice.created',
      actor: 'system',
      from: '2026-10-01T00:00:00.000Z',
      to: '2026-10-03T23:59:59.999Z',
      cursor: 'CUR',
    })

    await fetchAuditEvents({ tenant: '', event: '', actor: '', from: '', to: '' }, '')
    expect(fetchMock.mock.calls.at(-1)![0]).toBe(`${ROUTE}?limit=50`)
  })

  it('auditStamp and utcDay read UTC', () => {
    expect(auditStamp('2026-10-14T09:12:03.123456Z')).toBe('2026-10-14 09:12:03 UTC')
    expect(utcDay('2026-10-14T09:12:03.123456Z')).toBe('2026-10-14')
    expect(utcDay('2026-10-14T01:00:00+07:00')).toBe('2026-10-13')
    expect(auditStamp('2026-10-14T01:00:00+07:00')).toBe('2026-10-13 18:00:00 UTC')
  })

  // Source scan: a renamed Go json tag is invisible to every runtime test here (the fetch is mocked).
  it('StaffAuditRow keys equal the Go audit.StaffRow json tags', () => {
    const go = stripComments(readFileSync(fileURLToPath(new URL('../../../internal/audit/staff_read.go', import.meta.url)), 'utf8'))
    const goBody = /type\s+StaffRow\s+struct\s*\{([^{}]*)\}/.exec(go)?.[1] ?? ''
    const goKeys = [...goBody.matchAll(/`json:"([^",]+)[^"]*"/g)].map((m) => m[1])
    const ts = stripComments(readFileSync(fileURLToPath(new URL('./auditApi.ts', import.meta.url)), 'utf8'))
    const tsBody = /export interface StaffAuditRow\s*\{([^{}]*)\}/.exec(ts)?.[1] ?? ''
    const tsKeys = [...tsBody.matchAll(/^\s*([a-z_]+)\??\s*:/gm)].map((m) => m[1])
    expect(goKeys).toContain('actor_kind')
    expect(goKeys.length).toBeGreaterThanOrEqual(9)
    expect([...tsKeys].sort()).toEqual([...goKeys].sort())
  })

  // Same scan for nullability and the page envelope; a nullable Go key typed non-null reaches the screen as a crash.
  it('StaffAuditRow, StaffAuditPage and AuditPageInfo agree with the Go structs on keys and nullability', () => {
    const read = (rel: string) => stripComments(readFileSync(fileURLToPath(new URL(rel, import.meta.url)), 'utf8'))
    const goFields = (src: string, name: string) => {
      const body = new RegExp(`type\\s+${name}\\s+struct\\s*\\{([^{}]*)\\}`).exec(src)?.[1] ?? ''
      return [...body.matchAll(/^\s*\w+\s+(\*?)[\w.[\]]+\s+`json:"([^",]+)[^"]*"/gm)].map((m): [string, boolean] => [m[2], m[1] === '*']).sort()
    }
    const tsFields = (src: string, name: string) => {
      const body = new RegExp(`export interface ${name}\\s*\\{([^{}]*)\\}`).exec(src)?.[1] ?? ''
      return [...body.matchAll(/^\s*([a-z_]+)\??\s*:\s*(.+)$/gm)].map((m): [string, boolean] => [m[1], /\bnull\b/.test(m[2])]).sort()
    }
    const staff = read('../../../internal/audit/staff_read.go')
    const reader = read('../../../internal/audit/reader.go')
    const ts = read('./auditApi.ts')
    const pairs: [Array<[string, boolean]>, Array<[string, boolean]>][] = [
      [goFields(staff, 'StaffRow'), tsFields(ts, 'StaffAuditRow')],
      [goFields(staff, 'StaffPage'), tsFields(ts, 'StaffAuditPage')],
      [goFields(reader, 'PageInfo'), tsFields(ts, 'AuditPageInfo')],
    ]
    expect(pairs.map(([g]) => g.length)).toEqual([9, 2, 3])
    expect(pairs[0][0]).toContainEqual(['tenant_name', true])
    expect(pairs[2][0]).toContainEqual(['next_cursor', true])
    for (const [g, t] of pairs) expect(t).toEqual(g)
  })

  it('staffChangeLink maps a tenant row and refuses a global one', () => {
    const c: StaffChange = { id: '1', actor: 'A', event: 'tenant.demo_set', rule_set_version_id: null, tenant_id: 'T1', tenant_name: 'N', subject_id: null, payload: {}, created_at: '2026-10-14T23:30:00Z' }
    expect(staffChangeLink(c)).toEqual({ tenant: 'T1', event: 'tenant.demo_set', day: '2026-10-14', focusAt: '2026-10-14T23:30:00Z' })
    expect(staffChangeLink({ ...c, tenant_id: null, tenant_name: null })).toBeNull()
  })

  it('staffChangeLink takes the UTC day of a timestamp with an offset', () => {
    const c: StaffChange = { id: '1', actor: 'A', event: 'e', rule_set_version_id: null, tenant_id: 'T1', tenant_name: null, subject_id: null, payload: {}, created_at: '2026-10-15T01:30:00+02:00' }
    expect(staffChangeLink(c)?.day).toBe('2026-10-14')
    expect(staffChangeLink(c)?.focusAt).toBe('2026-10-15T01:30:00+02:00')
  })

  // Same scans as above for StaffChange; a nullable Go key typed non-null reaches the screen as a crash.
  it('StaffChange keys equal the Go audit.StaffChange json tags', () => {
    const read = (rel: string) => stripComments(readFileSync(fileURLToPath(new URL(rel, import.meta.url)), 'utf8'))
    const goBody = new RegExp('type\\s+StaffChange\\s+struct\\s*\\{([^{}]*)\\}').exec(read('../../../internal/audit/staff_read.go'))?.[1] ?? ''
    const go = [...goBody.matchAll(/^\s*\w+\s+(\*?)[\w.[\]]+\s+`json:"([^",]+)[^"]*"/gm)].map((m): [string, boolean] => [m[2], m[1] === '*']).sort()
    const tsBody = /export interface StaffChange\s*\{([^{}]*)\}/.exec(read('./auditApi.ts'))?.[1] ?? ''
    const ts = [...tsBody.matchAll(/^\s*([a-z_]+)\??\s*:\s*(.+)$/gm)].map((m): [string, boolean] => [m[1], /\bnull\b/.test(m[2])]).sort()
    expect(go).toContainEqual(['rule_set_version_id', true])
    expect(go.length).toBeGreaterThanOrEqual(9)
    expect(ts).toEqual(go)
  })

  it('bundle helpers build the staff paths', async () => {
    const T = '22222222-2222-2222-2222-222222222222'
    const E = '33333333-3333-3333-3333-333333333333'
    fetchMock.mockImplementation(async () => new Response(JSON.stringify({ entities: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    expect(await fetchStaffEntities(T)).toEqual([])
    expect(fetchMock.mock.calls.at(-1)![0]).toBe(`${GW}/api/invoice/v1/staff/tenants/${T}/entities`)

    await fetchBundlePreview(T, { entityId: E, from: '2026-09-01', to: '2026-09-30' })
    const url = new URL(fetchMock.mock.calls.at(-1)![0])
    expect(url.pathname).toBe(`/api/invoice/v1/staff/tenants/${T}/evidence-bundle/preview`)
    expect(Object.fromEntries(url.searchParams)).toEqual({ entity_id: E, from: '2026-09-01T00:00:00.000Z', to: '2026-09-30T23:59:59.999Z' })
  })

  describe('downloadBundle', () => {
    const REQ = { entityId: '33333333-3333-3333-3333-333333333333', from: '2026-09-01', to: '2026-09-30' }
    const zip = (disposition: string | null) =>
      new Response(new Blob(['PK']), { status: 200, headers: disposition ? { 'Content-Disposition': disposition } : {} })

    it('reads the filename and lifts the server message', async () => {
      fetchMock.mockResolvedValueOnce(zip('attachment; filename=ASComply_evidence_x_20260901_20260930.zip'))
      const got = await downloadBundle('T', REQ, 'fallback.zip')
      expect(got.filename).toBe('ASComply_evidence_x_20260901_20260930.zip')
      expect(await got.blob.text()).toBe('PK')
      const [url, init] = fetchMock.mock.calls.at(-1)!
      expect(url).toBe(`${GW}/api/invoice/v1/staff/tenants/T/evidence-bundle?entity_id=${REQ.entityId}&from=2026-09-01T00%3A00%3A00.000Z&to=2026-09-30T23%3A59%3A59.999Z`)
      expect(init.headers).toEqual({ Authorization: 'Bearer T' })

      fetchMock.mockResolvedValueOnce(zip('attachment; filename="quoted.zip"'))
      expect((await downloadBundle('T', REQ, 'fallback.zip')).filename).toBe('fallback.zip')
      fetchMock.mockResolvedValueOnce(zip(null))
      expect((await downloadBundle('T', REQ, 'fallback.zip')).filename).toBe('fallback.zip')

      fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ error: '3 invoices exceeds the bundle limit of 2' }), { status: 400, headers: { 'Content-Type': 'application/json' } }))
      const err = await downloadBundle('T', REQ, 'f.zip').catch((e) => e)
      expect(err).toBeInstanceOf(ApiError)
      expect(err.status).toBe(400)
      expect(err.message).toBe('3 invoices exceeds the bundle limit of 2')

      fetchMock.mockResolvedValueOnce(new Response('<html>', { status: 502, statusText: 'Bad Gateway' }))
      expect(await downloadBundle('T', REQ, 'f.zip').catch((e) => e.message)).toBe('Bad Gateway')
    })

    it('rejects 401 with no session token and sends no request', async () => {
      vi.stubGlobal('localStorage', { getItem: () => null })
      const calls = fetchMock.mock.calls.length
      const err = await downloadBundle('T', REQ, 'f.zip').catch((e) => e)
      expect(err.status).toBe(401)
      expect(fetchMock.mock.calls.length).toBe(calls)
    })
  })

  it('defaultBundlePeriod is the last 30 UTC days', () => {
    expect(defaultBundlePeriod(new Date('2026-10-14T03:00:00Z'))).toEqual({ from: '2026-09-14', to: '2026-10-14' })
    expect(defaultBundlePeriod(new Date('2026-10-14T23:59:59Z'))).toEqual({ from: '2026-09-14', to: '2026-10-14' })
    expect(defaultBundlePeriod(new Date('2026-03-01T00:30:00Z'))).toEqual({ from: '2026-01-30', to: '2026-03-01' })
  })

  it('BUNDLE_INVOICE_LIMIT equals the Go maxBundleInvoices', () => {
    const go = readFileSync(fileURLToPath(new URL('../../../internal/archive/archive.go', import.meta.url)), 'utf8')
    expect(Number(/const maxBundleInvoices = (\d+)/.exec(go)?.[1])).toBe(BUNDLE_INVOICE_LIMIT)
  })

  // Source scan: a renamed Go json tag is invisible to every runtime test here (the fetch is mocked).
  it('bundle wire types mirror the Go structs field for field', () => {
    const go = (f: string) => stripComments(readFileSync(fileURLToPath(new URL(`../../../internal/archive/${f}`, import.meta.url)), 'utf8'))
    const ts = stripComments(readFileSync(fileURLToPath(new URL('./auditApi.ts', import.meta.url)), 'utf8'))
    const jsonTagsOf = (source: string, structName: string): string[] => {
      const start = source.indexOf(`type ${structName} struct {`)
      if (start === -1) return []
      const body = source.slice(start, source.indexOf('}', start))
      return [...body.matchAll(/json:"([^",]+)/g)].map((m) => m[1])
    }
    // Each interface's own body, so a tag cannot pass on an unrelated declaration.
    const interfaceBodiesOf = (source: string, names: string[]): string[] =>
      names.flatMap((name) => {
        const head = `export interface ${name} {`
        const open = source.indexOf(head)
        if (open === -1) return []
        const end = source.indexOf('}', open + head.length)
        return end === -1 ? [] : [source.slice(open + head.length, end)]
      })
    const keysOf = (body: string): string[] => [...body.matchAll(/^\s*([a-z_]+)\??\s*:/gm)].map((m) => m[1])

    const pairs: [string, string, string][] = [
      [go('preview.go'), 'Preview', 'BundlePreview'],
      [go('manifest.go'), 'manifestEntity', 'BundleEntity'],
      [go('manifest.go'), 'manifestPeriod', 'BundlePeriod'],
      [go('manifest.go'), 'manifestCounts', 'BundleCounts'],
      [go('staff.go'), 'StaffEntity', 'StaffEntity'],
    ]
    const goTags = pairs.map(([src, struct]) => jsonTagsOf(src, struct))
    const tsKeys = pairs.map(([, , iface]) => keysOf(interfaceBodiesOf(ts, [iface])[0] ?? ''))
    expect(goTags.flat()).toHaveLength(21)
    expect(goTags.flat()).toEqual(expect.arrayContaining(['over_limit', 'body_files']))
    expect(goTags.flat()).not.toContain('not_a_real_tag')
    // Negative control: a struct the scan cannot find yields no tags, so the count above would fail.
    expect(jsonTagsOf(go('preview.go'), 'NotARealStruct')).toEqual([])
    expect(keysOf(interfaceBodiesOf(ts, ['NotARealInterface'])[0] ?? '')).not.toContain('not_a_real_tag')
    pairs.forEach(([, struct], i) => expect([...tsKeys[i]].sort(), struct).toEqual([...goTags[i]].sort()))

    // A nullable Go key typed non-null reaches the screen as a crash.
    const nullable = (src: string, struct: string) => /\*string\s+`json:"tin"/.test(src.slice(src.indexOf(`type ${struct} struct {`), src.indexOf('}', src.indexOf(`type ${struct} struct {`))))
    expect(nullable(pairs[1][0], 'manifestEntity')).toBe(true)
    expect(nullable(pairs[4][0], 'StaffEntity')).toBe(true)
    for (const iface of ['BundleEntity', 'StaffEntity']) expect(interfaceBodiesOf(ts, [iface])[0]).toMatch(/tin:\s*string \| null/)
  })
})
