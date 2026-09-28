// The hand-off code helpers.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Me, type Session } from '../auth'
import { HANDOFF_PARAM, handoffPersona, isLiveHandoffSession, readHandoffCode, redeemHandoff } from './sessionHandoff'

const CODE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ'
const STATE = 'ZYXWVUTSRQPONMLKJIHGFEDCBAzyxwvutsrqponmlk_'
const GATEWAY = 'https://gw.test'
const ME: Me = {
  tenant: { id: '33333333-3333-3333-3333-333333333333', name: 'Adaeze Ventures' },
  user: { id: 'd0000000-0000-0000-0000-000000000009', role: 'authenticated' },
}

function jwt(exp: number): string {
  const b64 = (o: object) => btoa(JSON.stringify(o)).replace(/=+$/, '')
  return `${b64({ alg: 'RS256' })}.${b64({ sub: ME.user.id, exp })}.sig`
}
const NOW = 1_800_000_000_000
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
    expect(rows.length).toBeGreaterThan(0)
    for (const [search, want] of rows) {
      expect(readHandoffCode(search), search).toBe(want)
    }
  })
})

describe('handoffPersona (D8)', () => {
  it('handoffPersona takes subject and tenant from /me', () => {
    expect(handoffPersona(ME)).toEqual({
      ...APP_PERSONAS.firm,
      subject: ME.user.id,
      tenantId: ME.tenant.id,
    })
    expect(handoffPersona(ME).mode).toBe('firm')
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

  // D15, D24: a missing, empty or non-string refresh token is an AUTH-05-era answer.
  it('redeemHandoff without a refresh token sets no renewal', async () => {
    const rows: [string, object][] = [
      ['absent', { access_token: LIVE }],
      ['empty', { access_token: LIVE, refresh_token: '' }],
      ['numeric', { access_token: LIVE, refresh_token: 42 }],
      ['null', { access_token: LIVE, refresh_token: null }],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [name, answer] of rows) {
      stubExchange(answer)
      const s = await redeemHandoff(GATEWAY, CODE, STATE, 5000)
      expect(s.token, name).toBe(LIVE)
      expect(s.handoff, name).toBe(true)
      expect(s.renewal, name).toBeUndefined()
    }
  })
})

describe('isLiveHandoffSession (D9, D18)', () => {
  it('isLiveHandoffSession', () => {
    const persona: Session = { persona: APP_PERSONAS.firm, token: LIVE, me: ME, verified: true }
    const renewal = { refreshToken: 'R0', receivedAt: NOW - 2 * 3_600_000 }
    const rows: [string, Session | null, boolean][] = [
      ['no session', null, false],
      ['persona session', persona, false],
      ['expired hand-off', { ...persona, token: EXPIRED, handoff: true }, false],
      ['live hand-off', { ...persona, handoff: true }, true],
      // The boot renews it, as resolveBootSession keeps it.
      ['expired hand-off with a renewal', { ...persona, token: EXPIRED, handoff: true, renewal }, true],
      ['expired persona session with a renewal', { ...persona, token: EXPIRED, renewal }, false],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [name, session, want] of rows) {
      expect(isLiveHandoffSession(session, NOW), name).toBe(want)
    }
  })
})
