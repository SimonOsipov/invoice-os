// AUTH-05-08 QA Mode B: adversarial coverage for the hand-off helpers and record.
import { readFileSync } from 'node:fs'
import path from 'node:path'

import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@invoice-os/api-client'
import { APP_PERSONAS, type Me, type Session } from '../auth'
import { SESSION_KEY, parseStoredSession, serializeSession } from './session'
import { handoffPersona, isLiveHandoffSession, redeemHandoff } from './sessionHandoff'

const CODE = 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ'
const STATE = 'ZYXWVUTSRQPONMLKJIHGFEDCBAzyxwvutsrqponmlk_'
const GATEWAY = 'https://gw.test'
const ME: Me = {
  tenant: { id: '33333333-3333-3333-3333-333333333333', name: 'Adaeze Ventures' },
  user: { id: 'd0000000-0000-0000-0000-000000000009', role: 'authenticated' },
}
const NOW = 1_800_000_000_000
function jwt(exp: number): string {
  const b64 = (o: object) => btoa(JSON.stringify(o)).replace(/=+$/, '')
  return `${b64({ alg: 'RS256' })}.${b64({ sub: ME.user.id, exp })}.sig`
}
const LIVE = jwt(NOW / 1000 + 3600)

type Answer = { status: number; body: unknown } | 'network'
function stubFetch(exchange: Answer, me: Answer) {
  const urls: string[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string) => {
      urls.push(url)
      const a = url.endsWith('/auth/exchange') ? exchange : me
      if (a === 'network') return Promise.reject(new TypeError('Failed to fetch'))
      return Promise.resolve({ ok: a.status < 400, status: a.status, statusText: String(a.status), json: () => Promise.resolve(a.body) })
    }),
  )
  return urls
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('redeemHandoff adversarial', () => {
  it('an exchange refusal never calls /me and rejects with its ApiError', async () => {
    const rows: [Answer, number | null][] = [
      [{ status: 400, body: { error: 'invalid or expired code' } }, 400],
      [{ status: 502, body: { error: 'x' } }, 502],
      ['network', null],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [answer, status] of rows) {
      const urls = stubFetch(answer, { status: 200, body: ME })
      const err = await redeemHandoff(GATEWAY, CODE, STATE).catch((e: unknown) => e)
      expect(err).toBeInstanceOf(ApiError)
      expect((err as ApiError).status).toBe(status)
      expect(urls).toEqual([`${GATEWAY}/auth/exchange`])
    }
  })

  it('a /me 403 rejects with status 403 after the exchange', async () => {
    const urls = stubFetch({ status: 200, body: { access_token: LIVE } }, { status: 403, body: { error: 'forbidden' } })
    const err = await redeemHandoff(GATEWAY, CODE, STATE).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(403)
    expect(urls).toEqual([`${GATEWAY}/auth/exchange`, `${GATEWAY}/api/tenancy/v1/me`])
  })

  it('a /me 200 with no tenant rejects', async () => {
    stubFetch({ status: 200, body: { access_token: LIVE } }, { status: 200, body: { user: ME.user } })
    await expect(redeemHandoff(GATEWAY, CODE, STATE)).rejects.toBeInstanceOf(TypeError)
  })

  // Pinned, advisory: the gateway always answers a string (D4), so the client does not check it.
  it('pinned: a non-string access_token is passed through unchecked', async () => {
    stubFetch({ status: 200, body: { access_token: 12345 } }, { status: 200, body: ME })
    const s = await redeemHandoff(GATEWAY, CODE, STATE)
    expect(s.token).toBe(12345)
    expect(s.handoff).toBe(true)
  })
})

describe('isLiveHandoffSession adversarial', () => {
  const base: Session = { persona: handoffPersona(ME), token: LIVE, me: ME, verified: true, handoff: true }
  it('the expiry boundary is the token exp', () => {
    const exp = NOW / 1000 + 10
    const s = { ...base, token: jwt(exp) }
    expect(isLiveHandoffSession(s, exp * 1000 - 1)).toBe(true)
    expect(isLiveHandoffSession(s, exp * 1000)).toBe(false)
  })

  // Pinned, advisory: isTokenExpired reads an unreadable token as unexpired, so such a record is live.
  it('pinned: a hand-off record with a null or opaque token counts as live', () => {
    expect(isLiveHandoffSession({ ...base, token: null }, NOW)).toBe(true)
    expect(isLiveHandoffSession({ ...base, token: 'opaque' }, NOW)).toBe(true)
  })

  it('a truthy non-true handoff is not a hand-off session', () => {
    const s = { ...base, handoff: 'true' } as unknown as Session
    expect(isLiveHandoffSession(s, NOW)).toBe(false)
  })
})

describe('hand-off record parse adversarial', () => {
  const record = (extra: object) => JSON.stringify({ v: 1, personaId: 'firm', token: LIVE, me: ME, verified: true, ...extra })

  // Pinned: only `handoff === true` is a hand-off record; any other value parses as the persona record.
  it('pinned: handoff false, "true" or 1 parses as a persona session', () => {
    const rows: unknown[] = [false, 'true', 1, null]
    expect(rows.length).toBeGreaterThan(0)
    for (const v of rows) {
      const s = parseStoredSession(record({ handoff: v }))
      expect(s, String(v)).not.toBeNull()
      expect(s?.persona, String(v)).toEqual(APP_PERSONAS.firm)
      expect(s && 'handoff' in s, String(v)).toBe(false)
      expect(isLiveHandoffSession(s, NOW), String(v)).toBe(false)
    }
  })

  it('a hand-off record takes identity from me, not personaId', () => {
    const s = parseStoredSession(record({ handoff: true, personaId: 'inhouse' }))
    expect(s?.persona.subject).toBe(ME.user.id)
    expect(s?.persona.tenantId).toBe(ME.tenant.id)
    expect(s?.persona.mode).toBe('firm')
    expect(s?.handoff).toBe(true)
  })

  it('a hand-off record still needs a known personaId and the base shape', () => {
    const rows: [string, object][] = [
      ['unknown personaId', { personaId: 'ghost' }],
      ['wrong version', { v: 2 }],
      ['numeric token', { token: 5 }],
      ['string verified', { verified: 'yes' }],
      ['tenant id null', { me: { ...ME, tenant: { id: null, name: 'x' } } }],
      ['me an array', { me: [] }],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [name, extra] of rows) {
      vi.spyOn(console, 'warn').mockImplementation(() => {})
      expect(parseStoredSession(record({ handoff: true, ...extra })), name).toBeNull()
      vi.restoreAllMocks()
    }
  })

  it('a hand-off session serializes handoff true and never the code or a state', () => {
    const raw = serializeSession({ persona: handoffPersona(ME), token: LIVE, me: ME, verified: true, handoff: true })
    expect(JSON.parse(raw).handoff).toBe(true)
    expect(raw).not.toContain(CODE)
    expect(raw).not.toContain(STATE)
    expect(SESSION_KEY).toBe('invoice-os.session')
  })
})

describe('session.ts and sessionHandoff.ts import cycle', () => {
  it('either module loads first in a fresh registry', async () => {
    const orders = [
      ['./sessionHandoff', './session'],
      ['./session', './sessionHandoff'],
    ]
    expect(orders.length).toBeGreaterThan(0)
    for (const order of orders) {
      vi.resetModules()
      for (const m of order) await import(m)
      const h = await import('./sessionHandoff')
      const s = await import('./session')
      expect(isLiveHandoffSession({ persona: h.handoffPersona(ME), token: LIVE, me: ME, verified: true, handoff: true }, NOW)).toBe(true)
      expect(h.isLiveHandoffSession({ persona: APP_PERSONAS.firm, token: LIVE, me: ME, verified: true, handoff: true }, NOW)).toBe(true)
      expect(s.parseStoredSession(JSON.stringify({ v: 1, personaId: 'firm', token: LIVE, me: ME, verified: true, handoff: true }))?.persona.subject).toBe(ME.user.id)
    }
  })
})

// D20 (AC-16): the comments the change made false are corrected.
describe('D20 comment corrections', () => {
  const src = (f: string) => readFileSync(path.join(process.cwd(), 'src', f), 'utf8')

  it('session.ts states the D18 precedence and the handoff field', () => {
    const s = src('lib/session.ts')
    expect(s).not.toContain('A valid param WINS over a stored session.')
    expect(s).toContain('a live hand-off session wins over the')
    expect(s).toMatch(/verified: boolean, handoff\?: true \}/)
  })

  it('App.tsx states that a live hand-off session is not dropped', () => {
    const s = src('App.tsx')
    expect(s).not.toContain('boots with NO session even when one is stored: the user just chose')
    expect(s).toContain('unless that stored session is a live hand-off session (D18)')
  })

  it('SignIn.tsx names the hand-off in the SignInLoading comment', () => {
    const s = src('components/SignIn.tsx')
    expect(s).not.toContain('Loading splash shown while a landing deep-link (?persona=) auto-sign-in is in flight')
    expect(s).toMatch(/`\?handoff=` redemption/)
  })
})
