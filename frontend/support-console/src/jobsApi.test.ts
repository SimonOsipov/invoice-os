import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

import { stripComments } from '@invoice-os/api-client/strip-comments'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { SESSION_KEY } from './auth'
import { fetchJobs, JOB_STATES, jobAge, jobStateStyle } from './jobsApi'

const GW = 'https://gw.test'
const ROUTE = `${GW}/api/submission/v1/staff/jobs`

let fetchMock: ReturnType<typeof vi.fn>
beforeEach(() => {
  vi.stubEnv('VITE_GATEWAY_URL', GW + '/')
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => (k === SESSION_KEY ? JSON.stringify({ v: 2, token: 'T', refresh_token: 'R' }) : null),
  })
  fetchMock = vi.fn(async () => new Response(JSON.stringify({ jobs: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

describe('jobsApi', () => {
  it('fetchJobs builds the query from the filters', async () => {
    const id = '11111111-1111-1111-1111-111111111111'
    await fetchJobs(undefined, undefined)
    await fetchJobs('queued', undefined)
    await fetchJobs('dead_lettered', id)
    expect(fetchMock.mock.calls.map((c) => c[0])).toEqual([ROUTE, `${ROUTE}?state=queued`, `${ROUTE}?state=dead_lettered&tenant=${id}`])
  })

  it('every job state has a label and a tone', () => {
    const want: Record<string, [string, string]> = {
      queued: ['QUEUED', 'muted'],
      submitting: ['SUBMITTING', 'green'],
      pending: ['PENDING', 'amber'],
      accepted: ['ACCEPTED', 'green'],
      rejected: ['REJECTED', 'red'],
      failed: ['FAILED', 'red'],
      dead_lettered: ['DEAD-LETTER', 'red'],
      cancelled: ['CANCELLED', 'muted'],
    }
    expect(Object.keys(want).sort()).toEqual([...JOB_STATES].sort())
    for (const s of JOB_STATES) {
      const [label, tone] = want[s]
      expect(jobStateStyle(s), s).toEqual({ label, bg: `var(--status-${tone}-bg)`, border: `var(--status-${tone}-border)`, text: `var(--status-${tone}-text)` })
    }
    expect(jobStateStyle('weird_state')).toEqual({ label: 'WEIRD_STATE', bg: 'var(--status-muted-bg)', border: 'var(--status-muted-border)', text: 'var(--status-muted-text)' })
  })

  it('jobAge buckets seconds, minutes, hours and days', () => {
    const now = Date.parse('2026-10-14T12:00:00Z')
    const ago = (s: number) => new Date(now - s * 1000).toISOString()
    expect([45, 12 * 60, 5 * 3600, 3 * 86400].map((s) => jobAge(ago(s), now))).toEqual(['45s', '12m', '5h', '3d'])
    expect([59, 60, 3599, 3600, 86399, 86400].map((s) => jobAge(ago(s), now))).toEqual(['59s', '1m', '59m', '1h', '23h', '1d'])
  })

  // Source scan: a renamed Go json tag is invisible to every runtime test here (the fetch is mocked).
  it('StaffJob keys equal the Go staffjobs.Job json tags', () => {
    const go = stripComments(readFileSync(fileURLToPath(new URL('../../../internal/staffjobs/store.go', import.meta.url)), 'utf8'))
    const goBody = /type\s+Job\s+struct\s*\{([^{}]*)\}/.exec(go)?.[1] ?? ''
    const goKeys = [...goBody.matchAll(/`json:"([^",]+)[^"]*"/g)].map((m) => m[1])
    const ts = stripComments(readFileSync(fileURLToPath(new URL('./jobsApi.ts', import.meta.url)), 'utf8'))
    const tsBody = /export interface StaffJob\s*\{([^{}]*)\}/.exec(ts)?.[1] ?? ''
    const tsKeys = [...tsBody.matchAll(/^\s*([a-z_]+)\??\s*:/gm)].map((m) => m[1])
    expect(goKeys).toContain('max_attempts')
    expect(goKeys.length).toBeGreaterThanOrEqual(13)
    expect([...tsKeys].sort()).toEqual([...goKeys].sort())
  })
})
