import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const exec = vi.hoisted(() => vi.fn<(...args: unknown[]) => string>(() => 'unused'))
vi.mock('node:child_process', () => ({ execFileSync: exec }))

type Seen = { url: string; headers: Record<string, string> }
const seen: Seen[] = []
vi.mock('@invoice-os/api-client/client', () => ({
  apiFetch: (url: string, init?: { headers?: Record<string, string> }) => {
    seen.push({ url, headers: init?.headers ?? {} })
    return Promise.resolve({ access_token: 'a.b.c', code: 'code' })
  },
  ApiError: class ApiError extends Error {},
}))

const HEADER = 'X-Mock-Issuer-Token'
const NIGHTLY = 'https://gateway-nightly-944095690109.africa-south1.run.app'
let status = 204
let fetchStatuses: number[] = []

type Client = typeof import('./client')
async function load(gateway: string): Promise<Client> {
  vi.stubEnv('GATEWAY_URL', gateway)
  vi.stubEnv('APP_URL', 'https://app.test')
  vi.resetModules()
  return import('./client')
}

const fetchMock = vi.fn()
beforeEach(() => {
  seen.length = 0
  fetchStatuses = []
  status = 204
  exec.mockReset()
  fetchMock.mockReset()
  fetchMock.mockImplementation((url: string, init?: { headers?: Record<string, string> }) => {
    seen.push({ url, headers: init?.headers ?? {} })
    const s = fetchStatuses.shift() ?? status
    const body = s === 204 ? null : s === 401 ? '{"error":"unauthorized"}' : '{"access_token":"a.b.c","refresh_token":"r","code":"c"}'
    return Promise.resolve(new Response(body, { status: s }))
  })
  vi.stubGlobal('fetch', fetchMock)
})
afterEach(() => {
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})

const PERSONA = { subject: 's', tenantId: 't' }
const GRANT = { user_id: 'u', tenant_id: 't', role: 'reviewer' as const, display_name: 'd', email: 'e@example.com' }
const headerOf = (s: Seen) => s.headers[HEADER]

describe('mockIssuerHeaders', () => {
  it('each mock-route helper sends X-Mock-Issuer-Token from MOCK_ISSUER_TOKEN', async () => {
    vi.stubEnv('MOCK_ISSUER_TOKEN', 'tok-1')
    const c = await load(NIGHTLY)
    await c.login(PERSONA)
    await c.grantMembership(GRANT)
    await c.setInvitationToken('t', 'i', 'x')
    await c.setDeletionLinkToken('t', 'u', 'x')
    await c.setReadinessConfirmToken('e@example.com', 'x')
    expect(seen.map((s) => s.url.replace(NIGHTLY, ''))).toEqual([
      '/auth/login', '/auth/mock/member', '/auth/mock/invitation-token', '/auth/mock/deletion-link-token', '/auth/mock/readiness-confirm-token',
    ])
    expect(seen.map(headerOf)).toEqual(Array(5).fill('tok-1'))
    expect(exec).not.toHaveBeenCalled()
  })

  it('MOCK_ISSUER_TOKEN is read on every call', async () => {
    vi.stubEnv('MOCK_ISSUER_TOKEN', 'tok-a')
    const c = await load(NIGHTLY)
    await c.login(PERSONA)
    vi.stubEnv('MOCK_ISSUER_TOKEN', 'tok-b')
    await c.login(PERSONA)
    expect(seen.map(headerOf)).toEqual(['tok-a', 'tok-b'])
  })

  it('non-mock helpers send no X-Mock-Issuer-Token', async () => {
    vi.stubEnv('MOCK_ISSUER_TOKEN', 'tok-1')
    const c = await load(NIGHTLY)
    status = 200
    await c.signInSession('e@example.com', 'pw')
    await c.me('a.b.c').catch(() => undefined)
    await c.registerFresh().catch(() => undefined)
    expect(seen.length).toBeGreaterThan(0)
    expect(seen.map(headerOf).filter(Boolean)).toEqual([])
  })

  it.each([
    ['a PR preview', 'https://gateway-pr-7-944095690109.africa-south1.run.app'],
    ['a non-preview gateway', 'https://gateway.test'],
  ])('off the nightly gateway (%s) no header and no secret read', async (_n, gateway) => {
    vi.stubEnv('MOCK_ISSUER_TOKEN', '')
    const c = await load(gateway)
    await c.login(PERSONA)
    await c.grantMembership(GRANT)
    expect(seen).toHaveLength(2)
    expect(seen.map(headerOf)).toEqual([undefined, undefined])
    expect(exec).not.toHaveBeenCalled()
  })

  it('on the nightly gateway the token comes from the nightly secret once', async () => {
    vi.stubEnv('MOCK_ISSUER_TOKEN', '')
    exec.mockReturnValue('tok-2\n')
    const c = await load(NIGHTLY)
    await c.login(PERSONA)
    await c.login(PERSONA)
    expect(exec).toHaveBeenCalledTimes(1)
    expect((exec.mock.calls[0] as unknown as [string, string[]])[1]).toContain('nightly-mock-issuer-token')
    expect(seen.map(headerOf)).toEqual(['tok-2', 'tok-2'])
  })

  it('a failed secret read names both sources and is retried', async () => {
    vi.stubEnv('MOCK_ISSUER_TOKEN', '')
    exec.mockImplementationOnce(() => {
      throw new Error('gcloud: not signed in')
    })
    exec.mockReturnValue('tok-6\n')
    const c = await load(NIGHTLY)
    await expect(c.grantMembership(GRANT)).rejects.toThrow(/MOCK_ISSUER_TOKEN.*gcloud|gcloud.*MOCK_ISSUER_TOKEN/)
    expect(fetchMock).not.toHaveBeenCalled()
    await c.grantMembership(GRANT)
    expect(seen.map(headerOf)).toEqual(['tok-6'])
  })

  it('a refusal error does not carry the token', async () => {
    vi.stubEnv('MOCK_ISSUER_TOKEN', 'tok-3')
    const c = await load(NIGHTLY)
    status = 401
    const err = await c.grantMembership(GRANT).then(() => undefined, (e: Error) => e)
    expect(err?.message).toContain('member grant answered 401')
    expect(err?.message).not.toContain('tok-3')
  })
})
