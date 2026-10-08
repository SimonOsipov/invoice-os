// The hand-off code helpers.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import { APP_PERSONAS, type Me, type Session } from '../auth'
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

afterEach(() => {
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
    stubExchange({ access_token: LIVE, refresh_token: 'R0' })

    const s = (await redeemHandoff(GATEWAY, CODE, STATE, 5000)) as Session

    expect(s.renewal).toEqual({ refreshToken: 'R0', receivedAt: 5000 - 60_000 })
    expect(s).toEqual({
      persona: handoffPersona(ME),
      token: LIVE,
      me: ME,
      verified: true,
      handoff: true,
      renewal: { refreshToken: 'R0', receivedAt: 5000 - 60_000 },
    })
  })

  // A missing, empty or non-string refresh token is a pre-renewal gateway's answer.
  it('redeemHandoff without a refresh token sets no renewal', async () => {
    const rows: [string, object][] = [
      ['absent', { access_token: LIVE }],
      ['empty', { access_token: LIVE, refresh_token: '' }],
      ['numeric', { access_token: LIVE, refresh_token: 42 }],
      ['null', { access_token: LIVE, refresh_token: null }],
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
            ? (r.exchange ?? { status: 200, body: { access_token: TOKEN, refresh_token: 'R0' } })
            : path === '/api/tenancy/v1/me'
              ? (me.shift() ?? { status: 500 })
              : path === '/api/tenancy/v1/invitations/mine'
                ? (r.mine ?? { status: 200, body: { invitations: [] } })
                : path === '/api/tenancy/v1/workspaces'
                ? (r.workspaces ?? { status: 201, body: { tenant: IN_HOUSE_ME.tenant } })
                : (r.refresh ?? { status: 200, body: { access_token: TOKEN2, refresh_token: 'R1' } })
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
    expect(calls[4].body).toEqual({ refresh_token: 'R0' })
    expect(calls[4].auth).toBeNull()
    expect(calls[1].auth).toBe(`Bearer ${TOKEN}`)
    expect(calls[5].auth).toBe(`Bearer ${TOKEN2}`)
    expect((s as Session).persona.mode).toBe('inhouse')

    for (const text of ['Adaeze Ventures', ...NON_ASCII]) {
      const sent = { workspace_name: text, display_name: `${text} Admin`, kind: 'firm' }
      const utf8 = stubChain({ exchange: { status: 200, body: { access_token: tokenWith({ user_metadata: { registration: sent } }), refresh_token: 'R0' } } })
      await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
      expect.soft(utf8[3]?.body, text).toStrictEqual(sent)
    }
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
      renewal: { refreshToken: 'R1', receivedAt: 5000 },
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
    expect(s).toMatchObject({ token: TOKEN2, handoff: true, renewal: { refreshToken: 'R1', receivedAt: 5000 } })

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
      const calls = stubChain({ exchange: { status: 200, body: { access_token: tokenWith(claims), refresh_token: 'R0' } }, me: [FORBIDDEN] })
      const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
      expect(trace(calls), name).toEqual(['POST /auth/exchange', 'GET /api/tenancy/v1/me', MINE])
      expect(err, name).toBeInstanceOf(ApiError)
      expect(err, name).toMatchObject({ status: 403 })
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
        expect(s, name).toMatchObject({ token: TOKEN, renewal: { refreshToken: 'R0', receivedAt: 5000 - HANDOFF_TTL_MS } })
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
      ['exchange without a refresh token', { exchange: NO_REFRESH }, CHAIN.slice(0, 4)],
      ['exchange with an empty refresh token', { exchange: { status: 200, body: { access_token: TOKEN, refresh_token: '' } } }, CHAIN.slice(0, 4)],
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
            ? (r.exchange ?? { status: 200, body: { access_token: FIRST, refresh_token: 'R0' } })
            : path === '/api/tenancy/v1/invitations/accept'
              ? (r.accept ?? { status: 200, body: { tenant: ME.tenant, user: { id: ME.user.id, role: 'admin' } } })
              : path === '/api/tenancy/v1/me'
                ? (r.me ?? (auth === `Bearer ${FIRST}` ? { status: 403, body: { error: 'forbidden' } } : { status: 200, body: ME }))
                : path === '/auth/refresh'
                  ? (r.refresh ?? { status: 200, body: { access_token: RENEWED, refresh_token: 'R1' } })
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
    const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000, INVITE).catch((e: unknown) => e)
    expect(trace(calls)).toEqual(CHAIN)
    expect(calls[1].body).toEqual({ token: INVITE })
    expect(calls[1].auth).toBe(`Bearer ${FIRST}`)
    expect(calls[2].body).toEqual({ refresh_token: 'R0' })
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
      renewal: { refreshToken: 'R1', receivedAt: 5000 },
    })
  })

  it('redeemHandoff_withInviteNeverProvisions', async () => {
    const calls = stubInvite()
    await redeemHandoff(GATEWAY, CODE, STATE, 5000, INVITE).catch((e: unknown) => e)
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
      const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000, INVITE).catch((e: unknown) => e)
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
      const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000, INVITE).catch((e: unknown) => e)
      expect(trace(calls), `${name}: stopped at the accept`).toEqual(['POST /auth/exchange', ACCEPT])
      expect(err, name).toBeInstanceOf(ApiError)
      expect(err, name).not.toBeInstanceOf(InviteRefusedError)
      expect((err as ApiError).status, name).toBe(status)
    }
    const control = stubInvite({ accept: { status: 404, body: { error: MSG_INVALID } } })
    const refused = await redeemHandoff(GATEWAY, CODE, STATE, 5000, INVITE).catch((e: unknown) => e)
    expect(trace(control), 'control: the exact pair is refused').toEqual(['POST /auth/exchange', ACCEPT])
    expect(refused).toBeInstanceOf(InviteRefusedError)
  })

  it('redeemHandoff_withInviteRejectsWhenRenewalFails', async () => {
    const rows: [string, Parameters<typeof stubInvite>[0], string[]][] = [
      ['accept network error', { acceptNetworkError: true }, CHAIN.slice(0, 2)],
      ['exchange without a refresh token', { exchange: { status: 200, body: { access_token: FIRST } } }, CHAIN.slice(0, 2)],
      ['exchange with an empty refresh token', { exchange: { status: 200, body: { access_token: FIRST, refresh_token: '' } } }, CHAIN.slice(0, 2)],
      ['refresh 401', { refresh: { status: 401, body: { error: 'invalid refresh token' } } }, CHAIN.slice(0, 3)],
      ['refresh answering without tokens', { refresh: { status: 200, body: {} } }, CHAIN.slice(0, 3)],
      ['/me 500', { me: { status: 500 } }, CHAIN],
      ['/me malformed', { me: { status: 200, body: {} } }, CHAIN],
    ]
    for (const [name, r, want] of rows) {
      const calls = stubInvite(r)
      const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000, INVITE).then(
        () => null,
        (e: unknown) => e,
      )
      expect(trace(calls), name).toEqual(want)
      expect(err, name).toBeInstanceOf(Error)
      expect(err, name).not.toBeInstanceOf(InviteRefusedError)
    }
  })

  it('redeemHandoff_withoutInviteNeverCallsAccept', async () => {
    const calls = stubInvite({ exchange: { status: 200, body: { access_token: LIVE, refresh_token: 'R0' } }, me: { status: 200, body: ME } })
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
          ? (r.exchange ?? { status: 200, body: { access_token: BARE, refresh_token: 'R0' } })
          : path === '/api/tenancy/v1/me'
            ? (me.shift() ?? { status: 200, body: ME })
            : path === '/api/tenancy/v1/invitations/mine'
              ? (r.mine ?? { status: 200, body: { invitations: THREE } })
              : path === '/api/tenancy/v1/workspaces'
                ? (workspacesSeq.shift() ?? r.workspaces ?? { status: 201, body: {} })
                : path === '/auth/refresh'
                  ? (refreshSeq.shift() ?? r.refresh ?? { status: 200, body: { access_token: RENEWED, refresh_token: 'R1' } })
                  : (acceptSeq.shift() ?? r.accept ?? { status: 200, body: {} })
        return Promise.resolve({ ok: reply.status < 400, status: reply.status, statusText: String(reply.status), json: () => Promise.resolve(reply.body ?? {}) })
      }),
    )
    return calls
  }
  const paths = (calls: Call[]) => calls.map((c) => c.url)
  const offer = (over: Partial<JoinOffer> = {}): JoinOffer => ({ kind: 'join', token: BARE, refreshToken: 'R0', invites: THREE, answers: null, ...over })

  it('redeemHandoff_403WithInvitesResolvesAJoinOffer', async () => {
    const calls = stub()
    const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000)
    expect(s).toEqual({ kind: 'join', token: BARE, refreshToken: 'R0', invites: THREE, answers: null })
    expect(paths(calls)).toEqual(['/auth/exchange', '/api/tenancy/v1/me', '/api/tenancy/v1/invitations/mine'])
    expect(calls[2].auth).toBe(`Bearer ${BARE}`)
    expect(calls[2].signal).toBe(calls[0].signal)
  })

  it('redeemHandoff_403WithNoInvitesKeepsTheNoWorkspaceError', async () => {
    const calls = stub({ mine: { status: 200, body: { invitations: [] } } })
    const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err).toMatchObject({ status: 403 })
    expect(paths(calls)).toEqual(['/auth/exchange', '/api/tenancy/v1/me', '/api/tenancy/v1/invitations/mine'])
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
    const calls = stub({ exchange: { status: 200, body: { access_token: WITH_ANSWERS, refresh_token: 'R0' } } })
    const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000)
    expect(s).toMatchObject({ kind: 'join', answers: ANSWERS, invites: THREE })
    expect(paths(calls)).not.toContain('/api/tenancy/v1/workspaces')
  })

  it('redeemHandoff_answersWithAnEmptyListProvision', async () => {
    const calls = stub({ exchange: { status: 200, body: { access_token: WITH_ANSWERS, refresh_token: 'R0' } }, mine: { status: 200, body: { invitations: [] } } })
    const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000)
    expect(paths(calls)).toContain('/api/tenancy/v1/workspaces')
    expect(s).toMatchObject({ handoff: true, token: RENEWED })
  })

  it('redeemHandoff_answersWithAFailedListNeverProvision', async () => {
    const rows: [string, Reply][] = [
      ['500', { status: 500 }],
      ['malformed', { status: 200, body: { invitations: 'x' } }],
    ]
    for (const [name, mine] of rows) {
      const calls = stub({ exchange: { status: 200, body: { access_token: WITH_ANSWERS, refresh_token: 'R0' } }, mine })
      const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
      expect(err, name).toBeInstanceOf(Error)
      expect(err, name).not.toBeInstanceOf(ApiError)
      expect(paths(calls), name).not.toContain('/api/tenancy/v1/workspaces')
    }
  })

  it('createOwnWorkspace_provisionsWithTheOfferAnswers', async () => {
    const timeout = vi.spyOn(AbortSignal, 'timeout')
    const calls = stub({ me: [{ status: 200, body: IN_HOUSE_ME }] })
    const s = await createOwnWorkspace(GATEWAY, offer({ token: WITH_ANSWERS, answers: ANSWERS }), 5000)
    expect(paths(calls)).toEqual(['/api/tenancy/v1/workspaces', '/auth/refresh', '/api/tenancy/v1/me'])
    expect(calls[0].body).toEqual(ANSWERS)
    expect(calls[0].auth).toBe(`Bearer ${WITH_ANSWERS}`)
    expect(timeout).toHaveBeenCalledWith(15000)
    expect(s).toMatchObject({ token: RENEWED, handoff: true, renewal: { refreshToken: 'R1', receivedAt: 5000 } })
  })

  it('redeemHandoff_heldInviteNeverAsksTheList', async () => {
    const calls = stub({ exchange: { status: 200, body: { access_token: WITH_ANSWERS, refresh_token: 'R0' } }, me: [{ status: 200, body: ME }] })
    await redeemHandoff(GATEWAY, CODE, STATE, 5000, 'AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_AbCdE')
    expect(paths(calls)).toEqual(['/auth/exchange', '/api/tenancy/v1/invitations/accept', '/auth/refresh', '/api/tenancy/v1/me'])
  })

  it('joinInvite_acceptsRefreshesAndReadsMe', async () => {
    const timeout = vi.spyOn(AbortSignal, 'timeout')
    const calls = stub({ me: [{ status: 200, body: ME }] })
    const s = await joinInvite(GATEWAY, offer(), 'a b', 5000)
    expect(paths(calls)).toEqual(['/api/tenancy/v1/invitations/a%20b/accept', '/auth/refresh', '/api/tenancy/v1/me'])
    expect(calls[0].method).toBe('POST')
    expect(calls[0].auth).toBe(`Bearer ${BARE}`)
    expect(calls[1].body).toEqual({ refresh_token: 'R0' })
    expect(calls[2].auth).toBe(`Bearer ${RENEWED}`)
    expect(timeout).toHaveBeenCalledWith(15000)
    expect(s).toEqual({ persona: handoffPersona(ME), token: RENEWED, me: ME, verified: true, handoff: true, renewal: { refreshToken: 'R1', receivedAt: 5000 } })
  })

  it('joinInvite_refusalWithAWorkspaceOpensIt', async () => {
    const calls = stub({ accept: { status: 404, body: { error: MSG_INVALID } }, me: [{ status: 200, body: ME }] })
    const s = await joinInvite(GATEWAY, offer(), 'a', 5000)
    expect(paths(calls)).toEqual(['/api/tenancy/v1/invitations/a/accept', '/auth/refresh', '/api/tenancy/v1/me'])
    expect(s).toMatchObject({ token: RENEWED, me: ME, handoff: true })
  })

  it('joinInvite_refusalWithoutAWorkspaceReportsTheOutcome', async () => {
    const rows: [string, Reply, Partial<JoinOffer>, string][] = [
      ['404', { status: 404, body: { error: MSG_INVALID } }, {}, 'invalid'],
      ['409', { status: 409, body: { error: MSG_ALREADY_MEMBER } }, {}, 'already-member'],
      ['no refresh token', { status: 404, body: { error: MSG_INVALID } }, { refreshToken: undefined }, 'invalid'],
    ]
    for (const [name, accept, over, outcome] of rows) {
      stub({ accept, me: [FORBIDDEN] })
      const err = await joinInvite(GATEWAY, offer(over), 'a', 5000).catch((e: unknown) => e)
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
      const err = await joinInvite(GATEWAY, offer(), 'a', 5000).catch((e: unknown) => e)
      expect(err, name).toBeInstanceOf(ApiError)
      expect(err, name).not.toBeInstanceOf(InviteRefusedError)
      expect(paths(calls), name).toEqual(['/api/tenancy/v1/invitations/a/accept'])
    }
  })

  const UNAUTH: Reply = { status: 401, body: { error: 'unauthorized' } }

  it('joinInvite_a401RenewsOnceAndRetriesWithTheNewToken', async () => {
    const calls = stub({ acceptSeq: [UNAUTH, { status: 200, body: {} }], me: [{ status: 200, body: ME }] })
    const s = await joinInvite(GATEWAY, offer(), 'a', 5000)
    expect(paths(calls)).toEqual(['/api/tenancy/v1/invitations/a/accept', '/auth/refresh', '/api/tenancy/v1/invitations/a/accept', '/auth/refresh', '/api/tenancy/v1/me'])
    expect(calls[0].auth).toBe(`Bearer ${BARE}`)
    expect(calls[2].auth).toBe(`Bearer ${RENEWED}`)
    expect(calls[3].body).toEqual({ refresh_token: 'R1' })
    expect(s).toMatchObject({ token: RENEWED, handoff: true })
  })

  it('joinInvite_theRotatedPairReachesTheOfferAfterARefusedRow', async () => {
    const RENEWED2 = tokenWith({}, NOW / 1000 + 7300)
    const renewed = (access_token: string, refresh_token: string): Reply => ({ status: 200, body: { access_token, refresh_token } })
    const calls = stub({
      acceptSeq: [UNAUTH, { status: 404, body: { error: MSG_INVALID } }, { status: 200, body: {} }],
      refreshSeq: [renewed(RENEWED, 'R1'), renewed(RENEWED2, 'R2'), renewed(RENEWED, 'R3')],
      me: [FORBIDDEN, { status: 200, body: ME }],
    })
    const o = offer()
    const err = await joinInvite(GATEWAY, o, 'a', 5000).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(InviteRefusedError)
    expect(o).toMatchObject({ token: RENEWED2, refreshToken: 'R2' })

    await joinInvite(GATEWAY, o, 'b', 5000)
    const accepts = calls.filter((c) => c.url.endsWith('/accept'))
    expect(accepts[2].auth).toBe(`Bearer ${RENEWED2}`)
    expect(calls.filter((c) => c.url === '/auth/refresh').map((c) => c.body)).toEqual([{ refresh_token: 'R0' }, { refresh_token: 'R1' }, { refresh_token: 'R2' }])
  })

  it('joinInvite_a401TwiceRethrowsAfterOneRenewal', async () => {
    const calls = stub({ accept: UNAUTH })
    const err = await joinInvite(GATEWAY, offer(), 'a', 5000).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err).toMatchObject({ status: 401 })
    expect(paths(calls)).toEqual(['/api/tenancy/v1/invitations/a/accept', '/auth/refresh', '/api/tenancy/v1/invitations/a/accept'])
  })

  it('joinInvite_a401WithoutARefreshTokenRethrows', async () => {
    for (const refreshToken of [undefined, '']) {
      const calls = stub({ accept: UNAUTH })
      const err = await joinInvite(GATEWAY, offer({ refreshToken }), 'a', 5000).catch((e: unknown) => e)
      expect(err).toMatchObject({ status: 401 })
      expect(paths(calls)).toEqual(['/api/tenancy/v1/invitations/a/accept'])
    }
  })

  it('createOwnWorkspace_a401RenewsOnceAndRetries', async () => {
    const calls = stub({ workspacesSeq: [UNAUTH, { status: 201, body: {} }], me: [{ status: 200, body: IN_HOUSE_ME }] })
    await createOwnWorkspace(GATEWAY, offer({ token: WITH_ANSWERS, answers: ANSWERS }), 5000)
    expect(paths(calls)).toEqual(['/api/tenancy/v1/workspaces', '/auth/refresh', '/api/tenancy/v1/workspaces', '/auth/refresh', '/api/tenancy/v1/me'])
    expect(calls[2].auth).toBe(`Bearer ${RENEWED}`)
  })

  it('createOwnWorkspace_a401TwiceRethrows', async () => {
    stub({ workspaces: UNAUTH })
    const err = await createOwnWorkspace(GATEWAY, offer({ token: WITH_ANSWERS, answers: ANSWERS }), 5000).catch((e: unknown) => e)
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
    const calls = stub({ exchange: { status: 200, body: { access_token: WITH_ANSWERS, refresh_token: 'R0' } }, me: [{ status: 500 }] })
    const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
    expect(err).toMatchObject({ status: 500 })
    expect(paths(calls)).toEqual(['/auth/exchange', '/api/tenancy/v1/me'])
  })

  it('redeemHandoff_answersWithAnUnreachableListNeverProvision', async () => {
    const calls = stub({ exchange: { status: 200, body: { access_token: WITH_ANSWERS, refresh_token: 'R0' } }, me: [FORBIDDEN, { status: 200, body: ME }] })
    const inner = globalThis.fetch as unknown as (u: string, i: unknown) => Promise<unknown>
    vi.stubGlobal('fetch', (u: string, i: unknown) => (u.endsWith('/invitations/mine') ? Promise.reject(new TypeError('Failed to fetch')) : inner(u, i)))
    const err = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(Error)
    expect(err).not.toBeInstanceOf(ApiError)
    expect(paths(calls)).not.toContain('/api/tenancy/v1/workspaces')
  })

  it('createOwnWorkspace_withoutAnswersThrowsBeforeAnyCall', async () => {
    const calls = stub()
    await expect(createOwnWorkspace(GATEWAY, offer({ answers: null }), 5000)).rejects.toThrow('no registration answers')
    expect(calls).toEqual([])
  })

  it('joinInvite_sharesOneSignalAcrossTheChain', async () => {
    const calls = stub({ me: [{ status: 200, body: ME }] })
    await joinInvite(GATEWAY, offer(), 'a', 5000)
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
      const err = await joinInvite(GATEWAY, offer(), 'a', 5000).catch((e: unknown) => e)
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
      const err = await joinInvite(GATEWAY, offer(), 'a', 5000).catch((e: unknown) => e)
      expect(err, name).toBeInstanceOf(InviteRefusedError)
      expect((err as InviteRefusedError).outcome, name).toBe('already-member')
    }
  })

  it('joinInvite_successWithoutARefreshTokenRejects', async () => {
    const calls = stub()
    await expect(joinInvite(GATEWAY, offer({ refreshToken: undefined }), 'a', 5000)).rejects.toThrow('no refresh token after joining the invite')
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
