import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { stripComments } from '@invoice-os/api-client/strip-comments'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { fetchAiCalls, formatCost, formatCount, formatLatency, isFailed } from './aiUsageApi'
import { SESSION_KEY } from './auth'

const GW = 'https://gw.test'
const ROUTE = `${GW}/api/invoice/v1/staff/ai-calls`

let fetchMock: ReturnType<typeof vi.fn>
beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', GW + '/')
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => (k === SESSION_KEY ? JSON.stringify({ v: 2, token: 'T', refresh_token: 'R' }) : null),
  })
  fetchMock = vi.fn(async () => new Response(JSON.stringify({ calls: [], page: { limit: 50, has_more: false, next_cursor: null } }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

const lastQuery = () => Object.fromEntries(new URL(fetchMock.mock.calls.at(-1)![0]).searchParams)
const read = (rel: string) => stripComments(readFileSync(fileURLToPath(new URL(rel, import.meta.url)), 'utf8'))
// Json tags of `type <name> struct {…}` in a Go source; [] when the struct is absent.
const goKeys = (src: string, name: string) => {
  const body = new RegExp(`type\\s+${name}\\s+struct\\s*\\{([^{}]*)\\}`).exec(src)?.[1] ?? ''
  return [...body.matchAll(/`json:"([^",]+)[^"]*"/g)].map((m) => m[1]).sort()
}
const tsKeys = (src: string, name: string) => {
  const body = new RegExp(`export interface ${name}\\s*\\{([^{}]*)\\}`).exec(src)?.[1] ?? ''
  return [...body.matchAll(/^\s*([a-z_]+)\??\s*:/gm)].map((m) => m[1]).sort()
}

describe('aiUsageApi', () => {
  it('fetchAiCalls builds the query from the filters', async () => {
    await fetchAiCalls({}, false)
    expect(fetchMock.mock.calls[0][0]).toBe(`${ROUTE}?limit=50`)

    const id = '11111111-1111-1111-1111-111111111111'
    await fetchAiCalls({ tenant: id, purpose: 'document', outcome: 'unavailable', from: '2026-10-01', to: '2026-10-03' }, true, 'CUR')
    expect(lastQuery()).toEqual({
      limit: '50',
      tenant: id,
      purpose: 'document',
      outcome: 'unavailable',
      failed: 'true',
      from: '2026-10-01T00:00:00.000Z',
      to: '2026-10-03T23:59:59.999Z',
      cursor: 'CUR',
    })

    await fetchAiCalls({ tenant: '', purpose: '', outcome: '', from: '', to: '' }, false, '')
    expect(fetchMock.mock.calls.at(-1)![0]).toBe(`${ROUTE}?limit=50`)
    expect(lastQuery()).not.toHaveProperty('failed')
  })

  it("formatters keep the server's digits", () => {
    expect(formatCost('0.0014741')).toBe('$0.0014741')
    expect(formatCost('0')).toBe('$0')
    expect(formatCount(3447)).toBe('3,447')
    expect(formatLatency(40000)).toBe('40,000 ms')
    expect(isFailed('fake')).toBe(true)
    expect(isFailed('ok')).toBe(false)
  })

  // Source scan: a renamed Go json tag is invisible to every runtime test here (the fetch is mocked).
  it('AiCall keys equal the Go aicalls.Call json tags', () => {
    const go = read('../../../internal/aicalls/staff_read.go')
    const ts = read('./aiUsageApi.ts')
    expect(goKeys(go, 'Call')).toContain('latency_ms')
    expect(goKeys(go, 'Call').length).toBeGreaterThanOrEqual(13)
    expect(goKeys(go, 'NotARealStruct')).toEqual([])
    expect(tsKeys(ts, 'AiCall')).toEqual(goKeys(go, 'Call'))
    expect(tsKeys(ts, 'AiCallsPage')).toEqual(goKeys(go, 'Page'))
    expect(tsKeys(ts, 'AiTotal')).toEqual(goKeys(go, 'Total'))
  })
})
