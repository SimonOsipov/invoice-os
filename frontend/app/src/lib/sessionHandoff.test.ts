// The hand-off code helpers.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import { APP_PERSONAS, type Me, type Session } from '../auth'
import { trackWorkspaceCreated } from '../chat'
import { SIGN_OUT_PENDING_KEY } from './revoke'
import { HANDOFF_PARAM, HANDOFF_TTL_MS, InviteRefusedError, createOwnWorkspace, handoffPersona, joinInvite, type JoinOffer, type ProvisionBody, isLiveHandoffSession, readHandoffCode, redeemHandoff, registrationAnswers } from './sessionHandoff'

const CODE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ'
const STATE = 'ZYXWVUTSRQPONMLKJIHGFEDCBAzyxwvutsrqponmlk_'
const GATEWAY = 'https://gw.test'
const ME: Me = {
  tenant: { id: '33333333-3333-3333-3333-333333333333', name: 'Adaeze Ventures', kind: 'firm' },
  user: { id: 'd0000000-0000-0000-0000-000000000009', role: 'authenticated', display_name: 'Adaeze Nwankwo', email: 'adaeze.nwankwo@example.com' },
}
const IN_HOUSE_ME: Me = { ...ME, tenant: { ...ME.tenant, kind: 'in_house' } }

function jwt(exp: number): string {
  const b64 = (o: object) => btoa(JSON.stringify(o)).replace(/=+$/, '')
  return `${b64({ alg: 'RS256' })}.${b64({ sub: ME.user.id, exp })}.sig`
}
const NOW = 1_800_000_000_000
// Segments are built the way GoTrue does: UTF-8 bytes, base64url, no padding.
function tokenWith(claims: object, exp = NOW / 1000 + 3600): string {
  const b64 = (o: object) =>
    btoa(Array.from(new TextEncoder().encode(JSON.stringify(o)), (b) => String.fromCharCode(b)).join(''))
      .replace(/\+/g, '-')
      .replace(/\//g, '_')
      .replace(/=+$/, '')
  return `${b64({ alg: 'RS256' })}.${b64({ sub: ME.user.id, exp, ...claims })}.sig`
}
const NON_ASCII = ['Soci\u00e9t\u00e9 G\u00e9n\u00e9rale', 'Ad\u00e9b\u00e1y\u1ecd\u0300', 'Acme \u{1F680}', '\u682a\u5f0f\u4f1a\u793e \u6771\u4eac']
const LIVE = jwt(NOW / 1000 + 3600)
const EXPIRED = jwt(NOW / 1000 - 1)

vi.mock('../chat', () => ({ trackWorkspaceCreated: vi.fn() }))

afterEach(() => {
  vi.mocked(trackWorkspaceCreated).mockClear()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('readHandoffCode (D9)', () => {
  it('readHandoffCode accepts only 43 base64url characters', () => {
    expect(HANDOFF_PARAM).toBe('handoff')
    expect(CODE).toHaveLength(43)
    const rows: [string, string | null][] = [
      [`?handoff=${CODE}`, CODE],
      [`?persona=firm&handoff=${CODE}`, CODE],
      [`?handoff=${'_-'.repeat(21)}A`, `${'_-'.repeat(21)}A`],
      [`?handoff=${CODE.slice(1)}`, null],
      [`?handoff=${CODE}A`, null],
      [`?handoff=${CODE.slice(1)}+`, null],
      [`?handoff=${CODE.slice(1)}=`, null],
      ['?handoff=', null],
      ['?handoff=short', null],
      ['', null],
      ['?persona=firm', null],
    ]
    for (const [search, want] of rows) {
      expect(readHandoffCode(search), search).toBe(want)
    }
  })
})

describe('handoffPersona (D8)', () => {
  it('handoffPersona takes subject and tenant from /me', () => {
    expect(handoffPersona(ME)).toEqual({
      ...APP_PERSONAS.firm,
      name: '',
      initials: '',
      email: '',
      subject: ME.user.id,
      tenantId: ME.tenant.id,
    })
    expect(handoffPersona(ME).mode).toBe('firm')
  })

  it('handoffPersona takes in-house mode from tenants.kind', () => {
    const persona = handoffPersona(IN_HOUSE_ME)
    expect(persona.mode).toBe('inhouse')
    expect(persona).toEqual({
      ...APP_PERSONAS.firm,
      name: '',
      initials: '',
      email: '',
      mode: 'inhouse',
      subject: ME.user.id,
      tenantId: ME.tenant.id,
    })
  })
})

describe('redeemHandoff (D9, D25 step 5)', () => {
  it('redeemHandoff calls exchange then /me with the token', async () => {
    const calls: { url: string; method: string; auth: string | null; body: string | undefined }[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string, init: { method?: string; headers: Headers; body?: string }) => {
        calls.push({ url, method: init.method ?? 'GET', auth: init.headers.get('Authorization'), body: init.body })
        const body = url.endsWith('/auth/exchange') ? { access_token: LIVE } : ME
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
      }),
    )
    const result = await redeemHandoff(GATEWAY, CODE, STATE).catch((e: unknown) => e)
    expect(calls.map((c) => `${c.method} ${c.url}`)).toEqual([
      `POST ${GATEWAY}/auth/exchange`,
      `GET ${GATEWAY}/api/tenancy/v1/me`,
    ])
    expect(JSON.parse(calls[0].body ?? 'null')).toEqual({ code: CODE, state: STATE })
    expect(calls[0].auth).toBeNull()
    expect(calls[1].auth).toBe(`Bearer ${LIVE}`)
    expect(result).toEqual({ persona: handoffPersona(ME), token: LIVE, me: ME, verified: true, handoff: true })
  })

  it('redeemHandoff retries /me once after a transient 503 session check', async () => {
    let meCalls = 0
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) => {
        if (url.endsWith('/auth/exchange')) {
          return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ access_token: LIVE }) })
        }
        meCalls += 1
        return meCalls === 1
          ? Promise.resolve({ ok: false, status: 503, statusText: 'Service Unavailable', json: () => Promise.resolve({ error: 'session check unavailable' }) })
          : Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(ME) })
      }),
    )
    // Fake timers let the fix back off by any delay without a real wait.
    vi.useFakeTimers()
    try {
      const pending = redeemHandoff(GATEWAY, CODE, STATE).catch((e: unknown) => e)
      await vi.runAllTimersAsync()
      expect(await pending).toEqual({ persona: handoffPersona(ME), token: LIVE, me: ME, verified: true, handoff: true })
      expect(meCalls).toBe(2)
    } finally {
      vi.useRealTimers()
    }
  })

  describe('the /me retry (BUG-125)', () => {
    const INVITE = { id: 'i1', workspace: 'Acme', role: 'member', inviter: null, expires_at: '2030-01-01T00:00:00Z' }
    const reply = (status: number, body: unknown) =>
      Promise.resolve({ ok: status < 400, status, statusText: String(status), json: () => Promise.resolve(body) })
    // Each /me call takes the next status; the last repeats. A hook can run inside the call.
    function stubMe(statuses: number[], onMe?: (n: number) => void) {
      const counts = { exchange: 0, me: 0, mine: 0 }
      vi.stubGlobal(
        'fetch',
        vi.fn((url: string) => {
          if (url.endsWith('/auth/exchange')) {
            counts.exchange += 1
            return reply(200, { access_token: LIVE })
          }
          if (url.endsWith('/invitations/mine')) {
            counts.mine += 1
            return reply(200, { invitations: [INVITE] })
          }
          counts.me += 1
          onMe?.(counts.me)
          const status = statuses[Math.min(counts.me, statuses.length) - 1]
          return status === 200 ? reply(200, ME) : reply(status, { error: 'x' })
        }),
      )
      return counts
    }
    // Resolves to the outcome, or the thrown value; `settled` tells whether it has finished yet.
    function run() {
      const state = { settled: false }
      const pending = redeemHandoff(GATEWAY, CODE, STATE).catch((e: unknown) => e).finally(() => { state.settled = true })
      return { pending, state }
    }
    const isApiError = (e: unknown, status: number) => e instanceof ApiError && e.status === status

    afterEach(() => {
      vi.useRealTimers()
    })

    it('redeemHandoff rejects with the 503 after /me answers 503 on every try', async () => {
      vi.useFakeTimers()
      const counts = stubMe([503])
      const { pending } = run()
      await vi.runAllTimersAsync()
      expect(isApiError(await pending, 503)).toBe(true)
      expect(counts.me).toBe(3)
      expect(counts.exchange).toBe(1)
    })

    it('redeemHandoff backs off 500 ms then 1000 ms between /me tries', async () => {
      vi.useFakeTimers()
      const counts = stubMe([503, 503, 200])
      const { pending } = run()
      await vi.advanceTimersByTimeAsync(499)
      expect(counts.me).toBe(1)
      await vi.advanceTimersByTimeAsync(1)
      expect(counts.me).toBe(2)
      await vi.advanceTimersByTimeAsync(999)
      expect(counts.me).toBe(2)
      await vi.advanceTimersByTimeAsync(1)
      expect(await pending).toMatchObject({ verified: true, handoff: true, token: LIVE })
      expect(counts.me).toBe(3)
    })

    it('redeemHandoff rejects promptly when the 15 s signal aborts during the backoff', async () => {
      vi.useFakeTimers()
      const controller = new AbortController()
      vi.spyOn(AbortSignal, 'timeout').mockReturnValue(controller.signal)
      const counts = stubMe([503])
      const { pending, state } = run()
      await vi.advanceTimersByTimeAsync(100)
      expect(counts.me).toBe(1)
      const reason = new DOMException('timed out', 'TimeoutError')
      controller.abort(reason)
      await vi.advanceTimersByTimeAsync(0)
      expect(state.settled).toBe(true)
      expect(await pending).toBe(reason)
      expect(vi.getTimerCount()).toBe(0)
      expect(counts.me).toBe(1)
    })

    it('redeemHandoff rejects with the 503 at once when the signal aborted while /me answered', async () => {
      vi.useFakeTimers()
      const controller = new AbortController()
      vi.spyOn(AbortSignal, 'timeout').mockReturnValue(controller.signal)
      const counts = stubMe([503], () => controller.abort())
      const { pending, state } = run()
      await vi.advanceTimersByTimeAsync(0)
      expect(state.settled).toBe(true)
      expect(isApiError(await pending, 503)).toBe(true)
      expect(vi.getTimerCount()).toBe(0)
      expect(counts.me).toBe(1)
    })

    it('redeemHandoff rejects at once, with one /me call, on a non-503 /me failure', async () => {
      const statuses = [400, 401, 404, 429, 500, 502, 504]
      expect(statuses.length).toBeGreaterThan(0)
      for (const status of statuses) {
        vi.useFakeTimers()
        const counts = stubMe([status])
        const { pending, state } = run()
        await vi.advanceTimersByTimeAsync(0)
        expect(state.settled, `${status} settled before any backoff`).toBe(true)
        expect(isApiError(await pending, status), String(status)).toBe(true)
        expect(counts.me, String(status)).toBe(1)
        expect(vi.getTimerCount(), String(status)).toBe(0)
        vi.useRealTimers()
        vi.unstubAllGlobals()
      }
    })

    it('redeemHandoff rejects at once on a 401 that follows a 503', async () => {
      vi.useFakeTimers()
      const counts = stubMe([503, 401])
      const { pending } = run()
      await vi.runAllTimersAsync()
      expect(isApiError(await pending, 401)).toBe(true)
      expect(counts.me).toBe(2)
    })

    it('redeemHandoff still takes the invitations path on a 403, with or without a 503 before it', async () => {
      for (const statuses of [[403], [503, 403]]) {
        vi.useFakeTimers()
        const counts = stubMe(statuses)
        const { pending } = run()
        await vi.runAllTimersAsync()
        const offer = (await pending) as JoinOffer
        expect(offer.kind, String(statuses)).toBe('join')
        expect(offer.invites, String(statuses)).toEqual([INVITE])
        expect(counts.me, String(statuses)).toBe(statuses.length)
        expect(counts.mine, String(statuses)).toBe(1)
        vi.useRealTimers()
        vi.unstubAllGlobals()
      }
    })

    // Hand-off chain with a refresh; /me takes the next status, the last repeats.
    function stubRenewal(first: string, meStatuses: number[]) {
      let me = 0
      vi.stubGlobal(
        'fetch',
        vi.fn((url: string) => {
          if (url.endsWith('/auth/exchange')) {
            return reply(200, { access_token: first, renewable: true })
          }
          if (url.endsWith('/auth/refresh')) {
            return reply(200, { access_token: LIVE })
          }
          if (url.endsWith('/invitations/mine')) {
            return reply(200, { invitations: [] })
          }
          if (url.endsWith('/workspaces') || url.endsWith('/invitations/accept')) {
            return reply(200, { tenant: ME.tenant })
          }
          me += 1
          const status = meStatuses[Math.min(me, meStatuses.length) - 1]
          return status === 200 ? reply(200, ME) : reply(status, { error: 'x' })
        }),
      )
    }
    const verified = (s: unknown) => expect(s).toMatchObject({ persona: handoffPersona(ME), token: LIVE, me: ME, verified: true, handoff: true })

    it('redeemHandoff_withInviteRetriesA503OnTheReadAfterRenewal', async () => {
      stubRenewal(LIVE, [503, 200])
      vi.useFakeTimers()
      const pending = redeemHandoff(GATEWAY, CODE, STATE, NOW, { token: 'AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCdE', name: null }).catch((e: unknown) => e)
      await vi.runAllTimersAsync()
      verified(await pending)
    })

    it('redeemHandoff_registrationRetriesA503OnTheReadAfterRenewal', async () => {
      const registered = tokenWith({ user_metadata: { registration: { workspace_name: 'Adaeze Ventures', display_name: 'Adaeze Nwankwo', kind: 'in_house' } } })
      stubRenewal(registered, [403, 503, 200])
      vi.useFakeTimers()
      const pending = redeemHandoff(GATEWAY, CODE, STATE).catch((e: unknown) => e)
      await vi.runAllTimersAsync()
      verified(await pending)
    })
  })

  function stubExchange(answer: unknown) {
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) => {
        const body = url.endsWith('/auth/exchange') ? answer : ME
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
      }),
    )
  }

  function stubMe(me: unknown) {
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string) => {
        const body = url.endsWith('/auth/exchange') ? { access_token: LIVE } : me
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) })
      }),
    )
  }

  it('redeemHandoff takes the mode from /me', async () => {
    stubMe(IN_HOUSE_ME)
    const s = (await redeemHandoff(GATEWAY, CODE, STATE)) as Session
    expect(s.persona.mode).toBe('inhouse')
    expect(s.handoff).toBe(true)
    expect(s.me).toEqual(IN_HOUSE_ME)
  })

  it('redeemHandoff rejects a /me without a known kind', async () => {
    const withKind = (kind: unknown) => ({ ...ME, tenant: { ...ME.tenant, kind } })
    const rows: [string, unknown][] = [
      ['kind absent', { ...ME, tenant: { id: ME.tenant.id, name: ME.tenant.name } }],
      ['bogus', withKind('bogus')],
      ['toString', withKind('toString')],
      ['array', withKind(['firm'])],
    ]
    for (const [name, me] of rows) {
      stubMe(me)
      await expect(redeemHandoff(GATEWAY, CODE, STATE), name).rejects.toThrow(/malformed/)
    }
    // Control: the same /me with a known kind resolves.
    stubMe(withKind('firm'))
    await expect(redeemHandoff(GATEWAY, CODE, STATE)).resolves.toMatchObject({ handoff: true })
  })

  // The token may have sat in the gateway's store for up to HandoffTTL (60 s, internal/gateway/handoff.go).
  it('redeemHandoff keeps the refresh token with its receipt time', async () => {
    stubExchange({ access_token: LIVE, renewable: true })

    const s = (await redeemHandoff(GATEWAY, CODE, STATE, 5000)) as Session

    expect(s.renewal).toEqual({ receivedAt: 5000 - 60_000 })
    expect(s).toEqual({
      persona: handoffPersona(ME),
      token: LIVE,
      me: ME,
      verified: true,
      handoff: true,
      renewal: { receivedAt: 5000 - 60_000 },
    })
  })

  // Anything but renewable: true is a session the cookie cannot renew.
  it('redeemHandoff without renewable true sets no renewal', async () => {
    const rows: [string, object][] = [
      ['absent', { access_token: LIVE }],
      ['false', { access_token: LIVE, renewable: false }],
      ['string', { access_token: LIVE, renewable: 'true' }],
      ['numeric', { access_token: LIVE, renewable: 1 }],
      ['null', { access_token: LIVE, renewable: null }],
    ]
    for (const [name, answer] of rows) {
      stubExchange(answer)
      const s = (await redeemHandoff(GATEWAY, CODE, STATE, 5000)) as Session
      expect(s.token, name).toBe(LIVE)
      expect(s.handoff, name).toBe(true)
      expect(s.renewal, name).toBeUndefined()
    }
  })
})

// A tenant-less first sign-in provisions the workspace its token's answers name.
describe('redeemHandoff provisions the registered workspace', () => {
  const ANSWERS = { workspace_name: 'Adaeze Ventures', display_name: 'Adaeze Nwankwo', kind: 'in_house' }
  const TOKEN = tokenWith({ user_metadata: { registration: ANSWERS } })
  const TOKEN2 = tokenWith({ tenant_id: ME.tenant.id }, NOW / 1000 + 7200)
  const FORBIDDEN = { status: 403, body: { error: 'forbidden' } }
  type Reply = { status: number; body?: unknown }
  type Call = { url: string; method: string; auth: string | null; body: unknown; signal: AbortSignal | null | undefined }

  function stubChain(r: { exchange?: Reply; me?: Reply[]; mine?: Reply; workspaces?: Reply; refresh?: Reply }): Call[] {
    const calls: Call[] = []
    const me = [...(r.me ?? [FORBIDDEN, { status: 200, body: IN_HOUSE_ME }])]
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string, init: { method?: string; headers: Headers; body?: string; signal?: AbortSignal | null }) => {
        calls.push({
          url: url.replace(GATEWAY, ''),
          method: init.method ?? 'GET',
          auth: init.headers.get('Authorization'),
          body: init.body === undefined ? undefined : JSON.parse(init.body),
          signal: init.signal,
        })
        const path = url.replace(GATEWAY, '')
        const reply: Reply =
          path === '/auth/exchange'
            ? (r.exchange ?? { status: 200, body: { access_token: TOKEN, renewable: true } })
            : path === '/api/tenancy/v1/me'
              ? (me.shift() ?? { status: 500 })
              : path === '/api/tenancy/v1/invitations/mine'
                ? (r.mine ?? { status: 200, body: { invitations: [] } })
                : path === '/api/tenancy/v1/workspaces'
                ? (r.workspaces ?? { status: 201, body: { tenant: IN_HOUSE_ME.tenant } })
                : (r.refresh ?? { status: 200, body: { access_token: TOKEN2 } })
        return Promise.resolve({ ok: reply.status < 400, status: reply.status, statusText: String(reply.status), json: () => Promise.resolve(reply.body ?? {}) })
      }),
    )
    return calls
  }
  const trace = (calls: Call[]) => calls.map((c) => `${c.method} ${c.url}`)
  const MINE = 'GET /api/tenancy/v1/invitations/mine'
  const CHAIN = [
    'POST /auth/exchange',
    'GET /api/tenancy/v1/me',
    MINE,
    'POST /api/tenancy/v1/workspaces',
    'POST /auth/refresh',
    'GET /api/tenancy/v1/me',
  ]

  it("redeemHandoff provisions from the token's answers, refreshes and re-reads /me", async () => {
    const calls = stubChain({})
    const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
    expect(trace(calls)).toEqual(CHAIN)
    expect(calls[3].body).toEqual(ANSWERS)
    expect(calls[3].auth).toBe(`Bearer ${TOKEN}`)
    expect(calls[4].body).toBeUndefined()
    expect(calls[4].auth).toBeNull()
    expect(calls[1].auth).toBe(`Bearer ${TOKEN}`)
    expect(calls[5].auth).toBe(`Bearer ${TOKEN2}`)
    expect((s as Session).persona.mode).toBe('inhouse')

    for (const text of ['Adaeze Ventures', ...NON_ASCII]) {
      const sent = { workspace_name: text, display_name: `${text} Admin`, kind: 'firm' }
      const utf8 = stubChain({ exchange: { status: 200, body: { access_token: tokenWith({ user_metadata: { registration: sent } }), renewable: true } } })
      await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
      expect.soft(utf8[3]?.body, text).toStrictEqual(sent)
    }
  })

  it('provision_a201SendsWorkspaceCreatedOnce', async () => {
    const calls = stubChain({})
    const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000)
    expect(trace(calls)).toEqual(CHAIN)
    expect(s).toMatchObject({ persona: handoffPersona(IN_HOUSE_ME), token: TOKEN2, me: IN_HOUSE_ME, renewal: { receivedAt: 5000 } })
    expect(trackWorkspaceCreated).toHaveBeenCalledTimes(1)
  })

  it('provision_a409SendsNothing', async () => {
    stubChain({ workspaces: { status: 409, body: { error: 'already has a workspace' } } })
    const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000)
    expect(s).toMatchObject({ token: TOKEN2, handoff: true })
    expect(trackWorkspaceCreated).not.toHaveBeenCalled()
  })

  it('provision_aRejectedPostSendsNothing', async () => {
    const rows: [string, () => void, object][] = [
      ['500', () => stubChain({ workspaces: { status: 500 } }), { status: 500 }],
      [
        'network TypeError',
        () => {
          stubChain({})
          const inner = fetch
          vi.stubGlobal('fetch', (url: string, init: never) => (url.endsWith('/workspaces') ? Promise.reject(new TypeError('Failed to fetch')) : inner(url, init)))
        },
        { kind: 'network', status: null },
      ],
    ]
    for (const [name, arrange, want] of rows) {
      arrange()
      const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000).then(() => null, (e: unknown) => e)
      expect(err, name).toBeInstanceOf(ApiError)
      expect(err, name).toMatchObject(want)
      expect(trackWorkspaceCreated, name).not.toHaveBeenCalled()
    }
  })

  it('provision_aFailedRefreshAfterA201SendsOnce', async () => {
    const calls = stubChain({ refresh: { status: 400, body: { error: 'bad' } } })
    const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000).then(() => null, (e: unknown) => e)
    expect(trace(calls)).toEqual(CHAIN.slice(0, 5))
    expect(err).toBeInstanceOf(Error)
    expect((err as { status?: number }).status).not.toBe(403)
    expect(trackWorkspaceCreated).toHaveBeenCalledTimes(1)
  })

  it('join_sendsNoWorkspaceCreated: a redemption without answers', async () => {
    stubChain({ exchange: { status: 200, body: { access_token: tokenWith({}), renewable: true } }, me: [FORBIDDEN], mine: { status: 200, body: { invitations: [], removed: false, live_membership: false } } })
    await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
    expect(trackWorkspaceCreated).not.toHaveBeenCalled()
  })

  it('redeemHandoff keeps the refreshed tokens after provisioning', async () => {
    const calls = stubChain({})
    const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
    expect(trace(calls)).toEqual(CHAIN)
    expect(s).toEqual({
      persona: handoffPersona(IN_HOUSE_ME),
      token: TOKEN2,
      me: IN_HOUSE_ME,
      verified: true,
      handoff: true,
      renewal: { receivedAt: 5000 },
    })
  })

  it('redeemHandoff shares one 15 s abort signal across the whole chain', async () => {
    const timeout = vi.spyOn(AbortSignal, 'timeout')
    const calls = stubChain({})
    await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
    expect(trace(calls)).toEqual(CHAIN)
    expect(timeout).toHaveBeenCalledTimes(1)
    expect(timeout).toHaveBeenCalledWith(15000)
    expect(calls[0].signal).toBeInstanceOf(AbortSignal)
    expect(calls.every((c) => c.signal === calls[0].signal)).toBe(true)
  })

  it('redeemHandoff treats a 409 as provisioned and re-reads /me', async () => {
    const landed = stubChain({ workspaces: { status: 409, body: { error: 'already has a workspace' } } })
    const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
    expect(trace(landed)).toEqual(CHAIN)
    expect(s).toMatchObject({ token: TOKEN2, handoff: true, renewal: { receivedAt: 5000 } })

    const blocked = stubChain({ workspaces: { status: 409 }, me: [FORBIDDEN, FORBIDDEN] })
    const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
    expect(trace(blocked)).toEqual(CHAIN)
    expect(err).toBeInstanceOf(ApiError)
    expect(err).toMatchObject({ status: 403 })
  })

  it('redeemHandoff without answers makes no provisioning call', async () => {
    const rows: [string, object][] = [
      ['no user_metadata', {}],
      ['no registration', { user_metadata: {} }],
      ['blank workspace name', { user_metadata: { registration: { ...ANSWERS, workspace_name: '' } } }],
      ['whitespace-only workspace name', { user_metadata: { registration: { ...ANSWERS, workspace_name: '   ' } } }],
      ['whitespace-only display name', { user_metadata: { registration: { ...ANSWERS, display_name: '\t ' } } }],
      ['unknown kind', { user_metadata: { registration: { ...ANSWERS, kind: 'bogus' } } }],
    ]
    for (const [name, claims] of rows) {
      const calls = stubChain({ exchange: { status: 200, body: { access_token: tokenWith(claims), renewable: true } }, me: [FORBIDDEN], mine: { status: 200, body: { invitations: [], removed: false, live_membership: false } } })
      const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
      expect(trace(calls), name).toEqual(['POST /auth/exchange', 'GET /api/tenancy/v1/me', MINE])
      expect(err, name).toMatchObject({ kind: 'join', invites: [], answers: null, firstWorkspace: true })
    }
  })

  it('redeemHandoff does not provision when /me fails for another reason or succeeds', async () => {
    const rows: [string, Reply[], string[]][] = [
      ['/me 500', [{ status: 500 }], CHAIN.slice(0, 2)],
      ['/me 401', [{ status: 401, body: { error: 'unauthorized' } }], CHAIN.slice(0, 2)],
      ['/me 200: a returning account', [{ status: 200, body: IN_HOUSE_ME }], CHAIN.slice(0, 2)],
    ]
    for (const [name, me, want] of rows) {
      const calls = stubChain({ me })
      const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
      expect(trace(calls), name).toEqual(want)
      if (name.includes('200')) {
        expect(s, name).toMatchObject({ token: TOKEN, renewal: { receivedAt: 5000 - HANDOFF_TTL_MS } })
      } else {
        expect(s, name).toBeInstanceOf(ApiError)
      }
    }
  })

  it('redeemHandoff rejects when provisioning or refresh fails', async () => {
    const NO_REFRESH = { status: 200, body: { access_token: TOKEN } }
    const rows: [string, Parameters<typeof stubChain>[0], string[]][] = [
      ['workspaces 400', { workspaces: { status: 400, body: { error: 'bad' } } }, CHAIN.slice(0, 4)],
      ['workspaces 500', { workspaces: { status: 500 } }, CHAIN.slice(0, 4)],
      ['refresh 401', { refresh: { status: 401, body: { error: 'invalid refresh token' } } }, CHAIN.slice(0, 5)],
      ['exchange without renewable', { exchange: NO_REFRESH }, CHAIN.slice(0, 4)],
      ['exchange with renewable false', { exchange: { status: 200, body: { access_token: TOKEN, renewable: false } } }, CHAIN.slice(0, 4)],
      ['refresh answering without tokens', { refresh: { status: 200, body: {} } }, CHAIN.slice(0, 5)],
      ['second /me 500', { me: [FORBIDDEN, { status: 500 }] }, CHAIN],
      ['second /me malformed', { me: [FORBIDDEN, { status: 200, body: {} }] }, CHAIN],
    ]
    for (const [name, r, want] of rows) {
      const calls = stubChain(r)
      const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000).then(
        () => null,
        (e: unknown) => e,
      )
      expect(trace(calls), name).toEqual(want)
      expect(err, name).toBeInstanceOf(Error)
      expect((err as { status?: number }).status, name).not.toBe(403)
    }
  })
})

// An invitee's hand-off accepts the held invite instead of provisioning.
describe('redeemHandoff with a held invite (D11, D12)', () => {
  const INVITE = 'AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCdE'
  const ANSWERS = { workspace_name: 'Adaeze Ventures', display_name: 'Adaeze Nwankwo', kind: 'in_house' }
  // The tenant-less first token; a complete registration must not make the invite branch provision.
  const FIRST = tokenWith({ user_metadata: { registration: ANSWERS } })
  const RENEWED = tokenWith({ tenant_id: ME.tenant.id }, NOW / 1000 + 7200)
  // Copied from the Go constants in internal/tenancy/accept.go.
  const MSG_INVALID = 'this invite is no longer valid' // msgInviteNotValid
  const MSG_ALREADY_MEMBER = 'you already belong to a workspace' // msgAlreadyMember
  const MSG_OTHER_ADDRESS = 'this invite was sent to a different email address' // msgWrongAddress
  type Reply = { status: number; body?: unknown }
  type Call = { url: string; method: string; auth: string | null; body: unknown; signal: AbortSignal | null | undefined }

  function stubInvite(r: { exchange?: Reply; accept?: Reply; refresh?: Reply; me?: Reply; acceptNetworkError?: boolean } = {}): Call[] {
    const calls: Call[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string, init: { method?: string; headers: Headers; body?: string; signal?: AbortSignal | null }) => {
        const path = url.replace(GATEWAY, '')
        const auth = init.headers.get('Authorization')
        calls.push({ url: path, method: init.method ?? 'GET', auth, body: init.body === undefined ? undefined : JSON.parse(init.body), signal: init.signal })
        if (path === '/api/tenancy/v1/invitations/accept' && r.acceptNetworkError) {
          return Promise.reject(new TypeError('Failed to fetch'))
        }
        const reply: Reply =
          path === '/auth/exchange'
            ? (r.exchange ?? { status: 200, body: { access_token: FIRST, renewable: true } })
            : path === '/api/tenancy/v1/invitations/accept'
              ? (r.accept ?? { status: 200, body: { tenant: ME.tenant, user: { id: ME.user.id, role: 'admin' } } })
              : path === '/api/tenancy/v1/me'
                ? (r.me ?? (auth === `Bearer ${FIRST}` ? { status: 403, body: { error: 'forbidden' } } : { status: 200, body: ME }))
                : path === '/auth/refresh'
                  ? (r.refresh ?? { status: 200, body: { access_token: RENEWED } })
                  : { status: 500 }
        return Promise.resolve({ ok: reply.status < 400, status: reply.status, statusText: String(reply.status), json: () => Promise.resolve(reply.body ?? {}) })
      }),
    )
    return calls
  }
  const trace = (calls: Call[]) => calls.map((c) => `${c.method} ${c.url}`)
  const ACCEPT = 'POST /api/tenancy/v1/invitations/accept'
  const CHAIN = ['POST /auth/exchange', ACCEPT, 'POST /auth/refresh', 'GET /api/tenancy/v1/me']

  it('redeemHandoff_withInviteAcceptsThenRenews', async () => {
    const timeout = vi.spyOn(AbortSignal, 'timeout')
    const calls = stubInvite()
    const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000, { token: INVITE, name: null }).catch((e: unknown) => e)
    expect(trace(calls)).toEqual(CHAIN)
    expect(calls[1].body).toEqual({ token: INVITE })
    expect(calls[1].auth).toBe(`Bearer ${FIRST}`)
    expect(calls[2].body).toBeUndefined()
    expect(calls[2].auth).toBeNull()
    expect(calls[3].auth).toBe(`Bearer ${RENEWED}`)
    expect(timeout).toHaveBeenCalledTimes(1)
    expect(calls.every((c) => c.signal === calls[0].signal)).toBe(true)
    expect(s).toEqual({
      persona: handoffPersona(ME),
      token: RENEWED,
      me: ME,
      verified: true,
      handoff: true,
      renewal: { receivedAt: 5000 },
    })
  })

  it('acceptInviteAndRead sends the held name', async () => {
    const withName = stubInvite()
    await redeemHandoff(GATEWAY, CODE, STATE, 5000, { token: INVITE, name: 'Ada' }).catch((e: unknown) => e)
    expect(withName[1].body).toEqual({ token: INVITE, display_name: 'Ada' })
    const noName = stubInvite()
    await redeemHandoff(GATEWAY, CODE, STATE, 5000, { token: INVITE, name: null }).catch((e: unknown) => e)
    expect(noName[1].body).toEqual({ token: INVITE })
  })

  it('redeemHandoff_withInviteNeverProvisions', async () => {
    const calls = stubInvite()
    await redeemHandoff(GATEWAY, CODE, STATE, 5000, { token: INVITE, name: null }).catch((e: unknown) => e)
    expect(calls.filter((c) => c.url === '/api/tenancy/v1/workspaces')).toEqual([])
    expect(trace(calls), 'control: the invite chain ran').toEqual(CHAIN)
    const firstMe = calls.findIndex((c) => c.url === '/api/tenancy/v1/me')
    const accept = calls.findIndex((c) => c.url === '/api/tenancy/v1/invitations/accept')
    expect(accept, 'accept is called').toBeGreaterThan(-1)
    expect(firstMe, '/me comes after the accept').toBeGreaterThan(accept)
  })

  it('redeemHandoff_refusedInviteThrowsItsOutcome', async () => {
    const rows: [string, number, string, string][] = [
      ['404 invalid', 404, MSG_INVALID, 'invalid'],
      ['409 already a member', 409, MSG_ALREADY_MEMBER, 'already-member'],
      ['403 other address', 403, MSG_OTHER_ADDRESS, 'other-address'],
    ]
    for (const [name, status, error, outcome] of rows) {
      const calls = stubInvite({ accept: { status, body: { error } } })
      const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000, { token: INVITE, name: null }).catch((e: unknown) => e)
      expect(err, name).toBeInstanceOf(InviteRefusedError)
      expect((err as InviteRefusedError).outcome, name).toBe(outcome)
      expect(trace(calls), `${name}: no refresh and no /me`).toEqual(['POST /auth/exchange', ACCEPT])
    }
  })

  // Status and message must both match tenancy's (a gateway 403 says "forbidden").
  it('redeemHandoff_otherRefusalsAreNotInviteOutcomes', async () => {
    const rows: [string, number, unknown][] = [
      ['403 forbidden', 403, { error: 'forbidden' }],
      ['404 not found', 404, { error: 'not found' }],
      ['409 another message', 409, { error: 'already has a workspace' }],
      ['404 with the 409 message', 404, { error: MSG_ALREADY_MEMBER }],
      ['409 with the 403 message', 409, { error: MSG_OTHER_ADDRESS }],
      ['403 with the 404 message', 403, { error: MSG_INVALID }],
      ['400 with the 404 message', 400, { error: MSG_INVALID }],
      ['404 with no body', 404, undefined],
      ['404 with the message in another key', 404, { message: MSG_INVALID }],
      ['500', 500, { error: 'internal error' }],
    ]
    for (const [name, status, body] of rows) {
      const calls = stubInvite({ accept: { status, body } })
      const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000, { token: INVITE, name: null }).catch((e: unknown) => e)
      expect(trace(calls), `${name}: stopped at the accept`).toEqual(['POST /auth/exchange', ACCEPT])
      expect(err, name).toBeInstanceOf(ApiError)
      expect(err, name).not.toBeInstanceOf(InviteRefusedError)
      expect((err as ApiError).status, name).toBe(status)
    }
    const control = stubInvite({ accept: { status: 404, body: { error: MSG_INVALID } } })
    const refused = await redeemHandoff(GATEWAY, CODE, STATE, 5000, { token: INVITE, name: null }).catch((e: unknown) => e)
    expect(trace(control), 'control: the exact pair is refused').toEqual(['POST /auth/exchange', ACCEPT])
    expect(refused).toBeInstanceOf(InviteRefusedError)
  })

  it('redeemHandoff_withInviteRejectsWhenRenewalFails', async () => {
    const rows: [string, Parameters<typeof stubInvite>[0], string[]][] = [
      ['accept network error', { acceptNetworkError: true }, CHAIN.slice(0, 2)],
      ['exchange without renewable', { exchange: { status: 200, body: { access_token: FIRST } } }, CHAIN.slice(0, 2)],
      ['exchange with renewable false', { exchange: { status: 200, body: { access_token: FIRST, renewable: false } } }, CHAIN.slice(0, 2)],
      ['refresh 401', { refresh: { status: 401, body: { error: 'invalid refresh token' } } }, CHAIN.slice(0, 3)],
      ['refresh answering without tokens', { refresh: { status: 200, body: {} } }, CHAIN.slice(0, 3)],
      ['/me 500', { me: { status: 500 } }, CHAIN],
      ['/me malformed', { me: { status: 200, body: {} } }, CHAIN],
    ]
    for (const [name, r, want] of rows) {
      const calls = stubInvite(r)
      const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000, { token: INVITE, name: null }).then(
        () => null,
        (e: unknown) => e,
      )
      expect(trace(calls), name).toEqual(want)
      expect(err, name).toBeInstanceOf(Error)
      expect(err, name).not.toBeInstanceOf(InviteRefusedError)
    }
  })

  it('redeemHandoff_withoutInviteNeverCallsAccept', async () => {
    const calls = stubInvite({ exchange: { status: 200, body: { access_token: LIVE, renewable: true } }, me: { status: 200, body: ME } })
    await redeemHandoff(GATEWAY, CODE, STATE, 5000, null)
    expect(trace(calls)).toEqual(['POST /auth/exchange', 'GET /api/tenancy/v1/me'])
  })
})

describe('join offers', () => {
  const ANSWERS: ProvisionBody = { workspace_name: 'Adaeze Ventures', display_name: 'Adaeze Nwankwo', kind: 'in_house' }
  const BARE = tokenWith({})
  const WITH_ANSWERS = tokenWith({ user_metadata: { registration: ANSWERS } })
  const RENEWED = tokenWith({ tenant_id: ME.tenant.id }, NOW / 1000 + 7200)
  const FORBIDDEN = { status: 403, body: { error: 'forbidden' } }
  const MSG_INVALID = 'this invite is no longer valid' // msgInviteNotValid
  const MSG_ALREADY_MEMBER = 'you already belong to a workspace' // msgAlreadyMember
  const item = (id: string) => ({ id, workspace: `WS ${id}`, role: 'admin', inviter: id === 'b' ? null : 'Ada', expires_at: '2026-10-20T00:00:00Z' })
  const THREE = [item('a'), item('b'), item('c')]
  type Reply = { status: number; body?: unknown }
  type Call = { url: string; method: string; auth: string | null; body: unknown; signal: AbortSignal | null | undefined }

  function stub(r: { exchange?: Reply; me?: Reply[]; mine?: Reply; accept?: Reply; workspaces?: Reply; refresh?: Reply; acceptNetworkError?: boolean; acceptSeq?: Reply[]; workspacesSeq?: Reply[]; refreshSeq?: Reply[] } = {}): Call[] {
    const calls: Call[] = []
    const me = [...(r.me ?? [FORBIDDEN])]
    const acceptSeq = [...(r.acceptSeq ?? [])]
    const workspacesSeq = [...(r.workspacesSeq ?? [])]
    const refreshSeq = [...(r.refreshSeq ?? [])]
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string, init: { method?: string; headers: Headers; body?: string; signal?: AbortSignal | null }) => {
        const path = url.replace(GATEWAY, '')
        calls.push({ url: path, method: init.method ?? 'GET', auth: init.headers.get('Authorization'), body: init.body === undefined ? undefined : JSON.parse(init.body), signal: init.signal })
        if (path.endsWith('/accept') && r.acceptNetworkError) return Promise.reject(new TypeError('Failed to fetch'))
        const reply: Reply = path === '/auth/exchange'
          ? (r.exchange ?? { status: 200, body: { access_token: BARE, renewable: true } })
          : path === '/api/tenancy/v1/me'
            ? (me.shift() ?? { status: 200, body: ME })
            : path === '/api/tenancy/v1/invitations/mine'
              ? (r.mine ?? { status: 200, body: { invitations: THREE } })
              : path === '/api/tenancy/v1/workspaces'
                ? (workspacesSeq.shift() ?? r.workspaces ?? { status: 201, body: {} })
                : path === '/auth/refresh'
                  ? (refreshSeq.shift() ?? r.refresh ?? { status: 200, body: { access_token: RENEWED } })
                  : (acceptSeq.shift() ?? r.accept ?? { status: 200, body: {} })
        return Promise.resolve({ ok: reply.status < 400, status: reply.status, statusText: String(reply.status), json: () => Promise.resolve(reply.body ?? {}) })
      }),
    )
    return calls
  }
  const paths = (calls: Call[]) => calls.map((c) => c.url)
  const offer = (over: Partial<JoinOffer> = {}): JoinOffer => ({ kind: 'join', token: BARE, renewable: true, invites: THREE, answers: null, ...over })

  it('redeemHandoff_403WithInvitesResolvesAJoinOffer', async () => {
    const calls = stub()
    const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000)
    expect(s).toEqual({ kind: 'join', token: BARE, renewable: true, invites: THREE, answers: null })
    expect(paths(calls)).toEqual(['/auth/exchange', '/api/tenancy/v1/me', '/api/tenancy/v1/invitations/mine'])
    expect(calls[2].auth).toBe(`Bearer ${BARE}`)
    expect(calls[2].signal).toBe(calls[0].signal)
  })

  it('redeemHandoff: live_membership false gets the first-workspace offer', async () => {
    for (const body of [
      { invitations: [], live_membership: false },
      { invitations: [], removed: false, live_membership: false },
    ]) {
      const calls = stub({ mine: { status: 200, body } })
      const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000)
      expect(s).toEqual({ kind: 'join', token: BARE, renewable: true, invites: [], answers: null, firstWorkspace: true })
      expect(paths(calls)).toEqual(['/auth/exchange', '/api/tenancy/v1/me', '/api/tenancy/v1/invitations/mine'])
    }
  })

  it('redeemHandoff: a live member with no answers keeps the no-workspace error', async () => {
    const rows: [string, Record<string, unknown>][] = [
      ['true', { live_membership: true }],
      ['missing', {}],
      ['string false', { live_membership: 'false' }],
      ['null', { live_membership: null }],
      ['number 0', { live_membership: 0 }],
    ]
    for (const [name, extra] of rows) {
      const calls = stub({ mine: { status: 200, body: { invitations: [], removed: false, ...extra } } })
      const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
      expect(err, name).toBeInstanceOf(ApiError)
      expect(err, name).toMatchObject({ status: 403 })
      expect(paths(calls), name).toEqual(['/auth/exchange', '/api/tenancy/v1/me', '/api/tenancy/v1/invitations/mine'])
    }
  })

  it('redeemHandoff: a removed person, invites and answers ignore live_membership', async () => {
    stub({ mine: { status: 200, body: { invitations: [], removed: true, live_membership: false } } })
    const removed = await redeemHandoff(GATEWAY, CODE, STATE, 5000)
    expect(removed).toMatchObject({ kind: 'join', invites: [], answers: null })
    expect((removed as JoinOffer).firstWorkspace).toBeUndefined()

    stub({ mine: { status: 200, body: { invitations: THREE, removed: false, live_membership: true } } })
    expect(await redeemHandoff(GATEWAY, CODE, STATE, 5000)).toMatchObject({ kind: 'join', invites: THREE })

    const calls = stub({ exchange: { status: 200, body: { access_token: WITH_ANSWERS, renewable: true } }, mine: { status: 200, body: { invitations: [], removed: false, live_membership: true } } })
    const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000)
    expect(paths(calls).filter((x) => x === '/api/tenancy/v1/workspaces')).toHaveLength(1)
    expect(s).toMatchObject({ handoff: true, token: RENEWED })
  })

  it("a removed person's offer carries no firstWorkspace flag", async () => {
    stub({ mine: { status: 200, body: { invitations: [], removed: true } } })
    const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000)
    expect(s).toMatchObject({ kind: 'join', invites: [], answers: null })
    expect((s as JoinOffer).firstWorkspace).toBeUndefined()
  })

  it('createOwnWorkspace: a retry after a failed read answers 409 and reaches the workspace', async () => {
    const calls = stub({
      workspacesSeq: [{ status: 201, body: {} }, { status: 409, body: { error: 'this account already has a workspace' } }],
      refreshSeq: [{ status: 200, body: { access_token: RENEWED } }, { status: 200, body: { access_token: RENEWED } }],
      me: [{ status: 500 }, { status: 200, body: ME }],
    })
    const o = offer({ invites: [], firstWorkspace: true, renewable: true })
    const body: ProvisionBody = { workspace_name: 'Acme', display_name: 'Ada', kind: 'firm' }
    await expect(createOwnWorkspace(GATEWAY, o, body, 5000)).rejects.toBeInstanceOf(ApiError)
    const s = await createOwnWorkspace(GATEWAY, o, body, 5000)
    expect(s).toMatchObject({ token: RENEWED, renewal: { receivedAt: 5000 } })
    const refreshes = calls.filter((c) => c.url === '/auth/refresh')
    expect(refreshes.map((c) => c.body)).toEqual([undefined, undefined])
    expect(calls.filter((c) => c.url === '/api/tenancy/v1/workspaces').map((c) => c.body)).toEqual([body, body])
  })

  it('createOwnWorkspace: a create after another tab created reaches the workspace', async () => {
    stub({ workspaces: { status: 409, body: { error: 'this account already has a workspace' } }, me: [{ status: 200, body: ME }] })
    const s = await createOwnWorkspace(GATEWAY, offer({ invites: [], firstWorkspace: true }), { workspace_name: 'Acme', display_name: 'Ada', kind: 'firm' }, 5000)
    expect(s).toMatchObject({ token: RENEWED, me: ME, handoff: true })
  })

  // A failed lookup rejects with a non-403 error, which App maps to signin=failed.
  it('redeemHandoff_403WithAFailedListLookupRejectsAsFailed', async () => {
    const rows: [string, Reply][] = [
      ['403', FORBIDDEN],
      ['500', { status: 500 }],
      ['null list', { status: 200, body: { invitations: null } }],
      ['item without id', { status: 200, body: { invitations: [item('a'), { ...item('b'), id: undefined }] } }],
    ]
    for (const [name, mine] of rows) {
      const calls = stub({ mine })
      const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
      expect(err, name).toBeInstanceOf(Error)
      expect(err, name).not.toBeInstanceOf(ApiError)
      expect(paths(calls), name).toEqual(['/auth/exchange', '/api/tenancy/v1/me', '/api/tenancy/v1/invitations/mine'])
    }
  })

  it('redeemHandoff_answersAndInvitesOfferBoth', async () => {
    const calls = stub({ exchange: { status: 200, body: { access_token: WITH_ANSWERS, renewable: true } } })
    const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000)
    expect(s).toMatchObject({ kind: 'join', answers: ANSWERS, invites: THREE })
    expect(paths(calls)).not.toContain('/api/tenancy/v1/workspaces')
  })

  it('redeemHandoff_answersWithAnEmptyListProvision', async () => {
    const calls = stub({ exchange: { status: 200, body: { access_token: WITH_ANSWERS, renewable: true } }, mine: { status: 200, body: { invitations: [] } } })
    const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000)
    expect(paths(calls)).toContain('/api/tenancy/v1/workspaces')
    expect(s).toMatchObject({ handoff: true, token: RENEWED })
  })

  it('redeemHandoff: a removed person with no invites gets the no-workspace offer', async () => {
    for (const exchange of [WITH_ANSWERS, BARE]) {
      const calls = stub({ exchange: { status: 200, body: { access_token: exchange, renewable: true } }, mine: { status: 200, body: { invitations: [], removed: true } } })
      const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000)
      expect(s).toMatchObject({ kind: 'join', invites: [], answers: exchange === WITH_ANSWERS ? ANSWERS : null })
      expect(paths(calls)).not.toContain('/api/tenancy/v1/workspaces')
    }
  })

  it('redeemHandoff: a fresh registrant still provisions', async () => {
    for (const body of [{ invitations: [] }, { invitations: [], removed: false }]) {
      const calls = stub({ exchange: { status: 200, body: { access_token: WITH_ANSWERS, renewable: true } }, mine: { status: 200, body } })
      await redeemHandoff(GATEWAY, CODE, STATE, 5000)
      expect(paths(calls).filter((x) => x === '/api/tenancy/v1/workspaces')).toHaveLength(1)
    }
  })

  it('redeemHandoff: a removed person with invites gets the Join offer', async () => {
    stub({ mine: { status: 200, body: { invitations: THREE, removed: true } } })
    expect(await redeemHandoff(GATEWAY, CODE, STATE, 5000)).toMatchObject({ kind: 'join', invites: THREE })
  })

  it('redeemHandoff_answersWithAFailedListNeverProvision', async () => {
    const rows: [string, Reply][] = [
      ['500', { status: 500 }],
      ['malformed', { status: 200, body: { invitations: 'x' } }],
    ]
    for (const [name, mine] of rows) {
      const calls = stub({ exchange: { status: 200, body: { access_token: WITH_ANSWERS, renewable: true } }, mine })
      const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
      expect(err, name).toBeInstanceOf(Error)
      expect(err, name).not.toBeInstanceOf(ApiError)
      expect(paths(calls), name).not.toContain('/api/tenancy/v1/workspaces')
    }
  })

  it('createOwnWorkspace_provisionsWithTheOfferAnswers', async () => {
    const timeout = vi.spyOn(AbortSignal, 'timeout')
    const calls = stub({ me: [{ status: 200, body: IN_HOUSE_ME }] })
    const s = await createOwnWorkspace(GATEWAY, offer({ token: WITH_ANSWERS, answers: ANSWERS }), ANSWERS, 5000)
    expect(paths(calls)).toEqual(['/api/tenancy/v1/workspaces', '/auth/refresh', '/api/tenancy/v1/me'])
    expect(calls[0].body).toEqual(ANSWERS)
    expect(calls[0].auth).toBe(`Bearer ${WITH_ANSWERS}`)
    expect(timeout).toHaveBeenCalledWith(15000)
    expect(s).toMatchObject({ token: RENEWED, handoff: true, renewal: { receivedAt: 5000 } })
  })

  it('redeemHandoff_heldInviteNeverAsksTheList', async () => {
    const calls = stub({ exchange: { status: 200, body: { access_token: WITH_ANSWERS, renewable: true } }, me: [{ status: 200, body: ME }] })
    await redeemHandoff(GATEWAY, CODE, STATE, 5000, { token: 'AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCdE', name: null })
    expect(paths(calls)).toEqual(['/auth/exchange', '/api/tenancy/v1/invitations/accept', '/auth/refresh', '/api/tenancy/v1/me'])
  })

  it('joinInvite_acceptsRefreshesAndReadsMe', async () => {
    const timeout = vi.spyOn(AbortSignal, 'timeout')
    const calls = stub({ me: [{ status: 200, body: ME }] })
    const s = await joinInvite(GATEWAY, offer(), 'a b', 'Ada', 5000)
    expect(paths(calls)).toEqual(['/api/tenancy/v1/invitations/a%20b/accept', '/auth/refresh', '/api/tenancy/v1/me'])
    expect(calls[0].method).toBe('POST')
    expect(calls[0].auth).toBe(`Bearer ${BARE}`)
    expect(calls[1].body).toBeUndefined()
    expect(calls[2].auth).toBe(`Bearer ${RENEWED}`)
    expect(timeout).toHaveBeenCalledWith(15000)
    expect(s).toEqual({ persona: handoffPersona(ME), token: RENEWED, me: ME, verified: true, handoff: true, renewal: { receivedAt: 5000 } })
  })

  it('joinInvite sends the trimmed name', async () => {
    const calls = stub({ me: [{ status: 200, body: ME }] })
    await joinInvite(GATEWAY, offer(), 'a', ' Ada ', 5000)
    expect(calls[0].body).toEqual({ display_name: 'Ada' })
  })

  it('joinInvite sends the name trimmed of U+0085', async () => {
    const calls = stub({ me: [{ status: 200, body: ME }] })
    await joinInvite(GATEWAY, offer(), 'a', '\u0085 Ada \u0085', 5000)
    expect(calls[0].body).toEqual({ display_name: 'Ada' })
  })

  it('joinInvite_refusalWithAWorkspaceOpensIt', async () => {
    const calls = stub({ accept: { status: 404, body: { error: MSG_INVALID } }, me: [{ status: 200, body: ME }] })
    const s = await joinInvite(GATEWAY, offer(), 'a', 'Ada', 5000)
    expect(paths(calls)).toEqual(['/api/tenancy/v1/invitations/a/accept', '/auth/refresh', '/api/tenancy/v1/me'])
    expect(s).toMatchObject({ token: RENEWED, me: ME, handoff: true })
  })

  it('joinInvite_refusalWithoutAWorkspaceReportsTheOutcome', async () => {
    const rows: [string, Reply, Partial<JoinOffer>, string][] = [
      ['404', { status: 404, body: { error: MSG_INVALID } }, {}, 'invalid'],
      ['409', { status: 409, body: { error: MSG_ALREADY_MEMBER } }, {}, 'already-member'],
      ['no refresh token', { status: 404, body: { error: MSG_INVALID } }, { renewable: false }, 'invalid'],
    ]
    for (const [name, accept, over, outcome] of rows) {
      stub({ accept, me: [FORBIDDEN] })
      const err = await joinInvite(GATEWAY, offer(over), 'a', 'Ada', 5000).catch((e: unknown) => e)
      expect(err, name).toBeInstanceOf(InviteRefusedError)
      expect((err as InviteRefusedError).outcome, name).toBe(outcome)
    }
  })

  it('joinInvite_otherFailuresRethrow', async () => {
    const rows: [string, Reply | 'network'][] = [
      ['500', { status: 500, body: { error: 'internal error' } }],
      ['404 other message', { status: 404, body: { error: 'not found' } }],
      ['409 other message', { status: 409, body: { error: 'conflict' } }],
      ['network', 'network'],
    ]
    for (const [name, accept] of rows) {
      const calls = stub(accept === 'network' ? { acceptNetworkError: true } : { accept })
      const err = await joinInvite(GATEWAY, offer(), 'a', 'Ada', 5000).catch((e: unknown) => e)
      expect(err, name).toBeInstanceOf(ApiError)
      expect(err, name).not.toBeInstanceOf(InviteRefusedError)
      expect(paths(calls), name).toEqual(['/api/tenancy/v1/invitations/a/accept'])
    }
  })

  const UNAUTH: Reply = { status: 401, body: { error: 'unauthorized' } }

  it('joinInvite_a401RenewsOnceAndRetriesWithTheNewToken', async () => {
    const calls = stub({ acceptSeq: [UNAUTH, { status: 200, body: {} }], me: [{ status: 200, body: ME }] })
    const s = await joinInvite(GATEWAY, offer(), 'a', 'Ada', 5000)
    expect(paths(calls)).toEqual(['/api/tenancy/v1/invitations/a/accept', '/auth/refresh', '/api/tenancy/v1/invitations/a/accept', '/auth/refresh', '/api/tenancy/v1/me'])
    expect(calls[0].auth).toBe(`Bearer ${BARE}`)
    expect(calls[2].auth).toBe(`Bearer ${RENEWED}`)
    expect(calls[3].body).toBeUndefined()
    expect(s).toMatchObject({ token: RENEWED, handoff: true })
  })

  it('joinInvite_theRenewedTokenReachesTheOfferAfterARefusedRow', async () => {
    const RENEWED2 = tokenWith({}, NOW / 1000 + 7300)
    const renewed = (access_token: string): Reply => ({ status: 200, body: { access_token } })
    const calls = stub({
      acceptSeq: [UNAUTH, { status: 404, body: { error: MSG_INVALID } }, { status: 200, body: {} }],
      refreshSeq: [renewed(RENEWED), renewed(RENEWED2), renewed(RENEWED)],
      me: [FORBIDDEN, { status: 200, body: ME }],
    })
    const o = offer()
    const err = await joinInvite(GATEWAY, o, 'a', 'Ada', 5000).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(InviteRefusedError)
    expect(o).toMatchObject({ token: RENEWED2 })

    await joinInvite(GATEWAY, o, 'b', 'Ada', 5000)
    const accepts = calls.filter((c) => c.url.endsWith('/accept'))
    expect(accepts[2].auth).toBe(`Bearer ${RENEWED2}`)
    expect(calls.filter((c) => c.url === '/auth/refresh').map((c) => c.body)).toEqual([undefined, undefined, undefined])
  })

  it('joinInvite_a401TwiceRethrowsAfterOneRenewal', async () => {
    const calls = stub({ accept: UNAUTH })
    const err = await joinInvite(GATEWAY, offer(), 'a', 'Ada', 5000).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err).toMatchObject({ status: 401 })
    expect(paths(calls)).toEqual(['/api/tenancy/v1/invitations/a/accept', '/auth/refresh', '/api/tenancy/v1/invitations/a/accept'])
  })

  it('joinInvite_a401WithoutARefreshTokenRethrows', async () => {
    for (const renewable of [false]) {
      const calls = stub({ accept: UNAUTH })
      const err = await joinInvite(GATEWAY, offer({ renewable }), 'a', 'Ada', 5000).catch((e: unknown) => e)
      expect(err).toMatchObject({ status: 401 })
      expect(paths(calls)).toEqual(['/api/tenancy/v1/invitations/a/accept'])
    }
  })

  const UNAVAILABLE: Reply = { status: 503, body: { error: 'x' } }
  const OK_ME: Reply = { status: 200, body: ME }
  const meCalls = (calls: Call[]) => calls.filter((c) => c.url === '/api/tenancy/v1/me').length

  it('joinInvite_andCreateOwnWorkspaceRetryA503OnTheReadAfterRenewal', async () => {
    vi.useFakeTimers()
    try {
      const joinCalls = stub({ me: [UNAVAILABLE, OK_ME] })
      const joined = joinInvite(GATEWAY, offer(), 'a', 'Ada', 5000).catch((e: unknown) => e)
      await vi.runAllTimersAsync()
      expect(await joined).toMatchObject({ token: RENEWED, me: ME, handoff: true })
      expect(meCalls(joinCalls)).toBe(2)
      expect(joinCalls.filter((c) => c.url.endsWith('/accept'))).toHaveLength(1)

      const createCalls = stub({ me: [UNAVAILABLE, OK_ME] })
      const created = createOwnWorkspace(GATEWAY, offer({ invites: [], firstWorkspace: true }), { workspace_name: 'Acme', display_name: 'Ada', kind: 'firm' }, 5000).catch((e: unknown) => e)
      await vi.runAllTimersAsync()
      expect(await created).toMatchObject({ token: RENEWED, me: ME, handoff: true })
      expect(meCalls(createCalls)).toBe(2)
      expect(createCalls.filter((c) => c.url === '/api/tenancy/v1/workspaces')).toHaveLength(1)
    } finally {
      vi.useRealTimers()
    }
  })

  it('joinInvite_refusalReadRetriesA503BeforeOpeningTheWorkspace', async () => {
    vi.useFakeTimers()
    try {
      const calls = stub({ accept: { status: 404, body: { error: MSG_INVALID } }, me: [UNAVAILABLE, OK_ME] })
      const pending = joinInvite(GATEWAY, offer(), 'a', 'Ada', 5000).catch((e: unknown) => e)
      await vi.runAllTimersAsync()
      expect(await pending).toMatchObject({ token: RENEWED, me: ME, handoff: true })
      expect(meCalls(calls)).toBe(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it('joinInvite_refusalReadThatStays503WaitsTheBackoffThenReportsTheRefusal', async () => {
    vi.useFakeTimers()
    try {
      const calls = stub({ accept: { status: 409, body: { error: MSG_ALREADY_MEMBER } }, me: [UNAVAILABLE, UNAVAILABLE, UNAVAILABLE] })
      let settled = false
      const pending = joinInvite(GATEWAY, offer(), 'a', 'Ada', 5000).catch((e: unknown) => e).finally(() => { settled = true })
      await vi.advanceTimersByTimeAsync(1499)
      expect(settled).toBe(false)
      await vi.advanceTimersByTimeAsync(1)
      const err = await pending
      expect(err).toBeInstanceOf(InviteRefusedError)
      expect((err as InviteRefusedError).outcome).toBe('already-member')
      expect(meCalls(calls)).toBe(3)
    } finally {
      vi.useRealTimers()
    }
  })

  it('joinInvite_readAfterRenewalRejectsAtOnceOnA401Or500', async () => {
    for (const status of [401, 500]) {
      const calls = stub({ me: [{ status }] })
      const err = await joinInvite(GATEWAY, offer(), 'a', 'Ada', 5000).catch((e: unknown) => e)
      expect(err, String(status)).toMatchObject({ status })
      expect(meCalls(calls), String(status)).toBe(1)

      const refused = stub({ accept: { status: 404, body: { error: MSG_INVALID } }, me: [{ status }] })
      const out = await joinInvite(GATEWAY, offer(), 'a', 'Ada', 5000).catch((e: unknown) => e)
      expect(out, `refusal ${status}`).toBeInstanceOf(InviteRefusedError)
      expect(meCalls(refused), `refusal ${status}`).toBe(1)
    }
  })

  it('createOwnWorkspace_a201SendsWorkspaceCreatedOnce', async () => {
    stub({ me: [{ status: 200, body: IN_HOUSE_ME }] })
    await createOwnWorkspace(GATEWAY, offer({ token: WITH_ANSWERS, answers: ANSWERS, firstWorkspace: true }), ANSWERS, 5000)
    expect(trackWorkspaceCreated).toHaveBeenCalledTimes(1)
  })

  it('provision_a401RetrySendsOnce', async () => {
    stub({
      exchange: { status: 200, body: { access_token: WITH_ANSWERS, renewable: true } },
      mine: { status: 200, body: { invitations: [] } },
      workspacesSeq: [UNAUTH, { status: 201, body: {} }],
      me: [FORBIDDEN, { status: 200, body: IN_HOUSE_ME }],
    })
    await redeemHandoff(GATEWAY, CODE, STATE, 5000)
    expect(trackWorkspaceCreated).toHaveBeenCalledTimes(1)
  })

  it('join_sendsNoWorkspaceCreated: joinInvite and a held invite', async () => {
    stub({ me: [{ status: 200, body: ME }] })
    await joinInvite(GATEWAY, offer(), 'a', 'Ada', 5000)
    stub({ exchange: { status: 200, body: { access_token: WITH_ANSWERS, renewable: true } }, me: [{ status: 200, body: ME }] })
    await redeemHandoff(GATEWAY, CODE, STATE, 5000, { token: 'AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCdE', name: null })
    expect(trackWorkspaceCreated).not.toHaveBeenCalled()
  })

  it('createOwnWorkspace_a401RenewsOnceAndRetries', async () => {
    const calls = stub({ workspacesSeq: [UNAUTH, { status: 201, body: {} }], me: [{ status: 200, body: IN_HOUSE_ME }] })
    await createOwnWorkspace(GATEWAY, offer({ token: WITH_ANSWERS, answers: ANSWERS }), ANSWERS, 5000)
    expect(paths(calls)).toEqual(['/api/tenancy/v1/workspaces', '/auth/refresh', '/api/tenancy/v1/workspaces', '/auth/refresh', '/api/tenancy/v1/me'])
    expect(calls[2].auth).toBe(`Bearer ${RENEWED}`)
  })

  it('createOwnWorkspace_a401TwiceRethrows', async () => {
    stub({ workspaces: UNAUTH })
    const err = await createOwnWorkspace(GATEWAY, offer({ token: WITH_ANSWERS, answers: ANSWERS }), ANSWERS, 5000).catch((e: unknown) => e)
    expect(err).toMatchObject({ status: 401 })
  })

  it('redeemHandoff_offerKeepsWireOrderNotIdOrder', async () => {
    const wire = [item('c'), item('a'), item('b')]
    stub({ mine: { status: 200, body: { invitations: wire } } })
    const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000)
    expect((s as JoinOffer).invites.map((i) => i.id)).toEqual(['c', 'a', 'b'])
  })

  it('redeemHandoff_malformedInvitesAreNotAnOffer', async () => {
    const bad: [string, unknown][] = [
      ['null item', [item('a'), null]],
      ['numeric role', [{ ...item('a'), role: 1 }]],
      ['missing workspace', [{ ...item('a'), workspace: undefined }]],
      ['undefined inviter', [{ ...item('a'), inviter: undefined }]],
      ['numeric inviter', [{ ...item('a'), inviter: 7 }]],
      ['missing expiry', [{ ...item('a'), expires_at: undefined }]],
      ['empty id', [{ ...item('a'), id: '' }]],
    ]
    for (const [name, invitations] of bad) {
      stub({ mine: { status: 200, body: { invitations } } })
      const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
      expect(err, name).not.toBeInstanceOf(ApiError)
      expect(err, name).toBeInstanceOf(Error)
    }
    for (const body of [null, [], 'x', { invitations: {} }]) {
      stub({ mine: { status: 200, body } })
      const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
      expect(err, JSON.stringify(body)).not.toBeInstanceOf(ApiError)
      expect(err, JSON.stringify(body)).toBeInstanceOf(Error)
    }
    stub({ mine: { status: 200, body: { invitations: [item('a'), item('b')] } } })
    expect(await redeemHandoff(GATEWAY, CODE, STATE, 5000), 'control: a well-formed list is an offer').toMatchObject({ kind: 'join' })
  })

  it('redeemHandoff_nonForbiddenMeNeverAsksTheList', async () => {
    const calls = stub({ exchange: { status: 200, body: { access_token: WITH_ANSWERS, renewable: true } }, me: [{ status: 500 }] })
    const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
    expect(err).toMatchObject({ status: 500 })
    expect(paths(calls)).toEqual(['/auth/exchange', '/api/tenancy/v1/me'])
  })

  it('redeemHandoff_answersWithAnUnreachableListNeverProvision', async () => {
    const calls = stub({ exchange: { status: 200, body: { access_token: WITH_ANSWERS, renewable: true } }, me: [FORBIDDEN, { status: 200, body: ME }] })
    const inner = globalThis.fetch as unknown as (u: string, i: unknown) => Promise<unknown>
    vi.stubGlobal('fetch', (u: string, i: unknown) => (u.endsWith('/invitations/mine') ? Promise.reject(new TypeError('Failed to fetch')) : inner(u, i)))
    const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(Error)
    expect(err).not.toBeInstanceOf(ApiError)
    expect(paths(calls)).not.toContain('/api/tenancy/v1/workspaces')
  })

  it('createOwnWorkspace sends the given body', async () => {
    for (const body of [{ workspace_name: 'Acme', display_name: 'Ada' }, { workspace_name: 'Acme', display_name: 'Ada', kind: 'firm' as const }]) {
      const calls = stub({ me: [{ status: 200, body: ME }] })
      await createOwnWorkspace(GATEWAY, offer({ answers: null }), body, 5000)
      const posts = calls.filter((c) => c.url.endsWith('/api/tenancy/v1/workspaces'))
      expect(posts).toHaveLength(1)
      expect(posts[0].body).toEqual(body)
    }
  })

  it('joinInvite_sharesOneSignalAcrossTheChain', async () => {
    const calls = stub({ me: [{ status: 200, body: ME }] })
    await joinInvite(GATEWAY, offer(), 'a', 'Ada', 5000)
    expect(calls).toHaveLength(3)
    expect(calls[1].signal).toBe(calls[0].signal)
    expect(calls[2].signal).toBe(calls[0].signal)
  })

  it('joinInvite_refusalNeedsTheMatchingStatusAndMessage', async () => {
    const rows: [string, Reply][] = [
      ['409 with the invalid message', { status: 409, body: { error: MSG_INVALID } }],
      ['404 with the already-member message', { status: 404, body: { error: MSG_ALREADY_MEMBER } }],
      ['403 other-address message', { status: 403, body: { error: 'this invite was sent to a different email address' } }],
    ]
    for (const [name, accept] of rows) {
      const calls = stub({ accept })
      const err = await joinInvite(GATEWAY, offer(), 'a', 'Ada', 5000).catch((e: unknown) => e)
      expect(err, name).toBeInstanceOf(ApiError)
      expect(err, name).not.toBeInstanceOf(InviteRefusedError)
      expect(paths(calls), name).toEqual(['/api/tenancy/v1/invitations/a/accept'])
    }
  })

  it('joinInvite_refusalWithAFailedReReadStillReportsTheOutcome', async () => {
    const rows: [string, Parameters<typeof stub>[0]][] = [
      ['refresh 401', { refresh: { status: 401, body: { error: 'invalid refresh token' } } }],
      ['/me 500', { me: [{ status: 500 }] }],
      ['/me malformed', { me: [{ status: 200, body: { tenant: {} } }] }],
    ]
    for (const [name, over] of rows) {
      stub({ accept: { status: 409, body: { error: MSG_ALREADY_MEMBER } }, ...over })
      const err = await joinInvite(GATEWAY, offer(), 'a', 'Ada', 5000).catch((e: unknown) => e)
      expect(err, name).toBeInstanceOf(InviteRefusedError)
      expect((err as InviteRefusedError).outcome, name).toBe('already-member')
    }
  })

  it('joinInvite_successWithoutARefreshTokenRejects', async () => {
    const calls = stub()
    await expect(joinInvite(GATEWAY, offer({ renewable: false }), 'a', 'Ada', 5000)).rejects.toThrow('no refresh token after joining the invite')
    expect(paths(calls)).toEqual(['/api/tenancy/v1/invitations/a/accept'])
  })
})

describe('registrationAnswers', () => {
  const NAMES = { workspace_name: 'Adaeze Ventures', display_name: 'Adaeze Nwankwo' }
  const withReg = (registration: unknown) => tokenWith({ user_metadata: { registration } })

  it('registrationAnswers accepts only complete answers', () => {
    const accepted: [string, string, unknown][] = [
      ['kind firm', withReg({ ...NAMES, kind: 'firm' }), { ...NAMES, kind: 'firm' }],
      ['kind in_house', withReg({ ...NAMES, kind: 'in_house' }), { ...NAMES, kind: 'in_house' }],
      ['kind absent', withReg(NAMES), NAMES],
      ['extra keys dropped', withReg({ ...NAMES, email: 'x@example.com' }), NAMES],
    ]
    for (const [name, token, want] of accepted) {
      expect(registrationAnswers(token), name).toStrictEqual(want)
    }
    for (const text of ['Adaeze Ventures', ...NON_ASCII]) {
      const sent = { workspace_name: text, display_name: `${text} Admin`, kind: 'in_house' }
      expect.soft(registrationAnswers(withReg(sent)), text).toStrictEqual(sent)
    }
    const refused: [string, string][] = [
      ['workspace name absent', withReg({ display_name: NAMES.display_name })],
      ['display name absent', withReg({ workspace_name: NAMES.workspace_name })],
      ['workspace name blank', withReg({ ...NAMES, workspace_name: '' })],
      ['display name blank', withReg({ ...NAMES, display_name: '' })],
      ['workspace name whitespace only', withReg({ ...NAMES, workspace_name: ' \t ' })],
      ['display name whitespace only', withReg({ ...NAMES, display_name: '   ' })],
      ['null kind', withReg({ ...NAMES, kind: null })],
      ['registration null', withReg(null)],
      ['registration an array', withReg([NAMES])],
      ['numeric name', withReg({ ...NAMES, workspace_name: 7 })],
      ['unknown kind', withReg({ ...NAMES, kind: 'bogus' })],
      ['numeric kind', withReg({ ...NAMES, kind: 1 })],
      ['registration not an object', withReg('Adaeze Ventures')],
      ['no registration', tokenWith({ user_metadata: {} })],
      ['no user_metadata', tokenWith({})],
      ['malformed token', 'not-a-jwt'],
      ['empty token', ''],
    ]
    for (const [name, token] of refused) {
      expect(registrationAnswers(token), name).toBeNull()
    }
  })
})

describe('isLiveHandoffSession (D9, D18)', () => {
  it('isLiveHandoffSession', () => {
    const persona: Session = { persona: APP_PERSONAS.firm, token: LIVE, me: ME, verified: true }
    const rows: [string, Session | null, boolean][] = [
      ['no session', null, false],
      ['persona session', persona, false],
      ['expired hand-off', { ...persona, token: EXPIRED, handoff: true }, false],
      ['live hand-off', { ...persona, handoff: true }, true],
    ]
    for (const [name, session, want] of rows) {
      expect(isLiveHandoffSession(session, NOW), name).toBe(want)
    }
  })
})

// SECAUD-17-11: the gateway cookie holds the refresh token; the exchange answers `renewable`, and every renewal is a cookie-mode refresh with no body.
describe('cookie-mode hand-off (SECAUD-17-11)', () => {
  const ANSWERS = { workspace_name: 'Adaeze Ventures', display_name: 'Adaeze Nwankwo', kind: 'in_house' }
  const WITH_ANSWERS = tokenWith({ user_metadata: { registration: ANSWERS } })
  const BARE = tokenWith({})
  const RENEWED = tokenWith({ tenant_id: ME.tenant.id }, NOW / 1000 + 7200)
  const COOKIE_INIT = { credentials: 'include', session: 'cookie' }
  const FORBIDDEN = { status: 403, body: { error: 'forbidden' } }
  const UNAUTH = { status: 401, body: { error: 'unauthorized' } }
  type Reply = { status: number; body?: unknown }
  type Call = { path: string; method: string; auth: string | null; body: unknown; credentials: string | undefined; session: string | null }

  function stub(r: { exchange?: Reply; me?: Reply[]; mine?: Reply; workspaces?: Reply[]; accept?: Reply; refresh?: Reply } = {}): Call[] {
    const calls: Call[] = []
    const me = [...(r.me ?? [{ status: 200, body: ME }])]
    const workspaces = [...(r.workspaces ?? [])]
    vi.stubGlobal(
      'fetch',
      vi.fn((url: string, init: { method?: string; headers: Headers; body?: string; credentials?: string }) => {
        const path = url.replace(GATEWAY, '')
        calls.push({
          path,
          method: init.method ?? 'GET',
          auth: init.headers.get('Authorization'),
          body: init.body === undefined ? undefined : JSON.parse(init.body),
          credentials: init.credentials,
          session: init.headers.get('X-Auth-Session'),
        })
        const reply: Reply =
          path === '/auth/exchange'
            ? (r.exchange ?? { status: 200, body: { access_token: LIVE, renewable: true } })
            : path === '/api/tenancy/v1/me'
              ? (me.shift() ?? { status: 200, body: ME })
              : path === '/api/tenancy/v1/invitations/mine'
                ? (r.mine ?? { status: 200, body: { invitations: [] } })
                : path === '/api/tenancy/v1/workspaces'
                  ? (workspaces.shift() ?? { status: 201, body: {} })
                  : path === '/auth/refresh'
                    ? (r.refresh ?? { status: 200, body: { access_token: RENEWED } })
                    : (r.accept ?? { status: 200, body: {} })
        return Promise.resolve({ ok: reply.status < 400, status: reply.status, statusText: String(reply.status), json: () => Promise.resolve(reply.body ?? {}) })
      }),
    )
    return calls
  }
  const refreshes = (calls: Call[]) => calls.filter((c) => c.path === '/auth/refresh')
  const REFRESH_CALL = { path: '/auth/refresh', method: 'POST', auth: null, body: undefined, ...COOKIE_INIT }

  function memoryStorage(initial: Record<string, string> = {}) {
    const m = new Map(Object.entries(initial))
    const storage = {
      getItem: vi.fn((k: string) => m.get(k) ?? null),
      setItem: vi.fn((k: string, v: string) => void m.set(k, String(v))),
      removeItem: vi.fn((k: string) => void m.delete(k)),
      clear: vi.fn(() => m.clear()),
    }
    vi.stubGlobal('localStorage', storage)
    return storage
  }

  it('redeem_usesCookieModeAndReadsRenewable', async () => {
    const calls = stub()

    const s = (await redeemHandoff(GATEWAY, CODE, STATE, 5000)) as Session

    expect(calls[0]).toEqual({ path: '/auth/exchange', method: 'POST', auth: null, body: { code: CODE, state: STATE }, ...COOKIE_INIT })
    expect(s.renewal).toEqual({ receivedAt: 5000 - HANDOFF_TTL_MS })
    expect(JSON.stringify(s)).not.toContain('refresh_token')

    // Anything but boolean true sets no renewal; a refresh_token in the body is not a renewal.
    const rows: [string, object][] = [
      ['absent', { access_token: LIVE }],
      ['false', { access_token: LIVE, renewable: false }],
      ['string "true"', { access_token: LIVE, renewable: 'true' }],
      ['1', { access_token: LIVE, renewable: 1 }],
      ['refresh_token only', { access_token: LIVE, refresh_token: 'R0' }],
    ]
    for (const [name, body] of rows) {
      const c = stub({ exchange: { status: 200, body } })
      const out = (await redeemHandoff(GATEWAY, CODE, STATE, 5000)) as Session
      expect(c[0]?.credentials, name).toBe('include')
      expect(out.token, name).toBe(LIVE)
      expect(out.handoff, name).toBe(true)
      expect(out.renewal, name).toBeUndefined()
    }
  })

  it('provision_renewsThroughTheCookie', async () => {
    const calls = stub({
      exchange: { status: 200, body: { access_token: WITH_ANSWERS, renewable: true } },
      me: [FORBIDDEN, { status: 200, body: IN_HOUSE_ME }],
    })

    const out = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)

    expect(calls.map((c) => `${c.method} ${c.path}`)).toEqual([
      'POST /auth/exchange',
      'GET /api/tenancy/v1/me',
      'GET /api/tenancy/v1/invitations/mine',
      'POST /api/tenancy/v1/workspaces',
      'POST /auth/refresh',
      'GET /api/tenancy/v1/me',
    ])
    expect(calls[4]).toEqual(REFRESH_CALL)
    expect(calls[5]?.auth).toBe(`Bearer ${RENEWED}`)
    const s = out as Session
    expect(s).toMatchObject({ token: RENEWED, handoff: true, renewal: { receivedAt: 5000 } })
    expect(JSON.stringify(s)).not.toContain('refresh_token')

    // The Create screen of a first workspace: a 401 renews once through the cookie and retries.
    const offer: JoinOffer = { kind: 'join', token: BARE, renewable: true, invites: [], answers: null, firstWorkspace: true }
    const c2 = stub({ workspaces: [UNAUTH, { status: 201, body: {} }] })
    const made = await createOwnWorkspace(GATEWAY, offer, { workspace_name: 'Adaeze Ventures', display_name: 'Adaeze' }, 5000).catch((e: unknown) => e as Session)
    expect(c2.map((c) => `${c.method} ${c.path}`)).toEqual([
      'POST /api/tenancy/v1/workspaces',
      'POST /auth/refresh',
      'POST /api/tenancy/v1/workspaces',
      'POST /auth/refresh',
      'GET /api/tenancy/v1/me',
    ])
    expect(refreshes(c2)).toEqual([REFRESH_CALL, REFRESH_CALL])
    expect(c2[2]?.auth).toBe(`Bearer ${RENEWED}`)
    expect(made.renewal).toEqual({ receivedAt: 5000 })
  })

  it('create_withoutRenewalNeverRefreshes', async () => {
    const body = { workspace_name: 'Adaeze Ventures', display_name: 'Adaeze' }
    const make = (renewable: boolean): JoinOffer => ({ kind: 'join', token: BARE, renewable, invites: [], answers: null, firstWorkspace: true })
    // Control: a renewable offer refreshes once on the 401.
    const live = stub({ workspaces: [UNAUTH, { status: 201, body: {} }] })
    await createOwnWorkspace(GATEWAY, make(true), body, 5000).catch(() => undefined)
    expect(refreshes(live).length).toBeGreaterThan(0)
    expect(refreshes(live)[0]).toEqual(REFRESH_CALL)

    const dead = stub({ workspaces: [UNAUTH] })
    await expect(createOwnWorkspace(GATEWAY, make(false), body, 5000)).rejects.toMatchObject({ status: 401 })
    expect(dead.map((c) => c.path)).toEqual(['/api/tenancy/v1/workspaces'])

    // The same for a join accept.
    const joined = stub({ accept: UNAUTH })
    const inv = { id: 'a', workspace: 'WS a', role: 'admin', inviter: 'Ada', expires_at: '2026-10-20T00:00:00Z' }
    await expect(joinInvite(GATEWAY, { ...make(false), invites: [inv] }, 'a', 'Ada', 5000)).rejects.toMatchObject({ status: 401 })
    expect(refreshes(joined)).toEqual([])
  })

  it('inviteAccept_renewsThroughTheCookie', async () => {
    const calls = stub({ exchange: { status: 200, body: { access_token: BARE, renewable: true } } })

    const s = (await redeemHandoff(GATEWAY, CODE, STATE, 5000, { token: 'invite-token', name: null }).catch((e: unknown) => e)) as Session

    expect(calls.map((c) => `${c.method} ${c.path}`)).toEqual([
      'POST /auth/exchange',
      'POST /api/tenancy/v1/invitations/accept',
      'POST /auth/refresh',
      'GET /api/tenancy/v1/me',
    ])
    expect(calls[2]).toEqual(REFRESH_CALL)
    expect(s).toMatchObject({ token: RENEWED, handoff: true, renewal: { receivedAt: 5000 } })

    // Not renewable: nothing to renew through, so the accept cannot read /me.
    const dead = stub({ exchange: { status: 200, body: { access_token: BARE, renewable: false } } })
    await expect(redeemHandoff(GATEWAY, CODE, STATE, 5000, { token: 'invite-token', name: null })).rejects.toBeInstanceOf(Error)
    expect(refreshes(dead)).toEqual([])
  })

  it('redeem_okClearsThePendingMarker', async () => {
    const storage = memoryStorage({ [SIGN_OUT_PENDING_KEY]: '1' })
    stub()
    await redeemHandoff(GATEWAY, CODE, STATE, 5000)
    expect(storage.getItem(SIGN_OUT_PENDING_KEY)).toBeNull()

    // A refused exchange keeps it.
    const kept = memoryStorage({ [SIGN_OUT_PENDING_KEY]: '1' })
    stub({ exchange: { status: 400, body: { error: 'bad code' } } })
    await expect(redeemHandoff(GATEWAY, CODE, STATE, 5000)).rejects.toMatchObject({ status: 400 })
    expect(kept.getItem(SIGN_OUT_PENDING_KEY)).toBe('1')
  })
})
