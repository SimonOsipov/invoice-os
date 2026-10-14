import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { stripComments } from '@invoice-os/api-client/strip-comments'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SESSION_KEY } from './auth'
import { auditStamp, fetchAuditEvents, utcDay } from './auditApi'

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
})
