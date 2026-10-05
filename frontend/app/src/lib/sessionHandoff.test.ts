// The hand-off code helpers.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'

import { APP_PERSONAS, type Me, type Session } from '../auth'
import { HANDOFF_PARAM, HANDOFF_TTL_MS, handoffPersona, isLiveHandoffSession, readHandoffCode, redeemHandoff, registrationAnswers } from './sessionHandoff'

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
    const s = await redeemHandoff(GATEWAY, CODE, STATE)
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

    const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000)

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
      const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000)
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

  function stubChain(r: { exchange?: Reply; me?: Reply[]; workspaces?: Reply; refresh?: Reply }): Call[] {
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
              : path === '/api/tenancy/v1/workspaces'
                ? (r.workspaces ?? { status: 201, body: { tenant: IN_HOUSE_ME.tenant } })
                : (r.refresh ?? { status: 200, body: { access_token: TOKEN2, refresh_token: 'R1' } })
        return Promise.resolve({ ok: reply.status < 400, status: reply.status, statusText: String(reply.status), json: () => Promise.resolve(reply.body ?? {}) })
      }),
    )
    return calls
  }
  const trace = (calls: Call[]) => calls.map((c) => `${c.method} ${c.url}`)
  const CHAIN = [
    'POST /auth/exchange',
    'GET /api/tenancy/v1/me',
    'POST /api/tenancy/v1/workspaces',
    'POST /auth/refresh',
    'GET /api/tenancy/v1/me',
  ]

  it("redeemHandoff provisions from the token's answers, refreshes and re-reads /me", async () => {
    const calls = stubChain({})
    const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
    expect(trace(calls)).toEqual(CHAIN)
    expect(calls[2].body).toEqual(ANSWERS)
    expect(calls[2].auth).toBe(`Bearer ${TOKEN}`)
    expect(calls[3].body).toEqual({ refresh_token: 'R0' })
    expect(calls[3].auth).toBeNull()
    expect(calls[1].auth).toBe(`Bearer ${TOKEN}`)
    expect(calls[4].auth).toBe(`Bearer ${TOKEN2}`)
    expect((s as Session).persona.mode).toBe('inhouse')

    for (const text of ['Adaeze Ventures', ...NON_ASCII]) {
      const sent = { workspace_name: text, display_name: `${text} Admin`, kind: 'firm' }
      const utf8 = stubChain({ exchange: { status: 200, body: { access_token: tokenWith({ user_metadata: { registration: sent } }), refresh_token: 'R0' } } })
      await redeemHandoff(GATEWAY, CODE, STATE, 5000).catch((e: unknown) => e)
      expect.soft(utf8[2]?.body, text).toStrictEqual(sent)
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
      expect(trace(calls), name).toEqual(['POST /auth/exchange', 'GET /api/tenancy/v1/me'])
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
      ['workspaces 400', { workspaces: { status: 400, body: { error: 'bad' } } }, CHAIN.slice(0, 3)],
      ['workspaces 500', { workspaces: { status: 500 } }, CHAIN.slice(0, 3)],
      ['refresh 401', { refresh: { status: 401, body: { error: 'invalid refresh token' } } }, CHAIN.slice(0, 4)],
      ['exchange without a refresh token', { exchange: NO_REFRESH }, CHAIN.slice(0, 3)],
      ['exchange with an empty refresh token', { exchange: { status: 200, body: { access_token: TOKEN, refresh_token: '' } } }, CHAIN.slice(0, 3)],
      ['refresh answering without tokens', { refresh: { status: 200, body: {} } }, CHAIN.slice(0, 4)],
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
