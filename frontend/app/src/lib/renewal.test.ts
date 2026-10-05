// The renewal module. Clock and storage are injected; fetch is a controllable stub.
import { afterEach, describe, expect, it, vi } from 'vitest'

import { APP_PERSONAS, type Me, type Session } from '../auth'
import { SessionEndedError, createRenewer, deadline, isRenewalDue, renewAt } from './renewal'
import { handoffPersona } from './session'

const BASE = 'https://gw.test'
const HOUR = 3_600_000
const TENANT = '33333333-3333-3333-3333-333333333333'
const OTHER_TENANT = '44444444-4444-4444-4444-444444444444'
const SUB = 'd0000000-0000-0000-0000-000000000009'
const OTHER_SUB = 'd0000000-0000-0000-0000-000000000008'
const ME: Me = { tenant: { id: TENANT, name: 'Adaeze Ventures', kind: 'firm' }, user: { id: SUB, role: 'authenticated', display_name: 'Adaeze Nwankwo', email: 'adaeze.nwankwo@example.com' } }
const OTHER_ME: Me = { tenant: { id: OTHER_TENANT, name: 'Other Co', kind: 'firm' }, user: { id: OTHER_SUB, role: 'authenticated', display_name: 'Adaeze Nwankwo', email: 'adaeze.nwankwo@example.com' } }

// Server times sit far from the local clock: the lifetime is exp − iat, measured from receipt.
const IAT = 1_700_000_000
const EXP = IAT + 3600
const RECEIVED = 1_800_000_000_000
const RENEW_AT = RECEIVED + 2_880_000
const DEADLINE = RECEIVED + 3_600_000

const b64url = (s: string) => btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
const jwt = (claims: object) => `${b64url('{"alg":"RS256"}')}.${b64url(JSON.stringify(claims))}.sig`
const claims = (mark: string) => ({ sub: SUB, iat: IAT, exp: EXP, app_metadata: { tenant_id: TENANT }, mark })

const A0 = jwt(claims('A0'))
const A1 = jwt(claims('A1'))
const A5 = jwt(claims('A5'))
const A7 = jwt(claims('A7'))
const A9 = jwt(claims('A9'))
const B0 = jwt({ ...claims('B0'), sub: OTHER_SUB, app_metadata: { tenant_id: OTHER_TENANT } })

function hsession(o: { token?: string; refresh?: string | null; receivedAt?: number; me?: Me } = {}): Session {
  const { token = A0, refresh = 'R0', receivedAt = RECEIVED, me = ME } = o
  return {
    persona: handoffPersona(me),
    token,
    me,
    verified: true,
    handoff: true,
    ...(refresh === null ? {} : { renewal: { refreshToken: refresh, receivedAt } }),
  }
}

type Reply = { status: number; body?: unknown } | Error
const ok = (access: unknown, refresh: unknown): Reply => ({ status: 200, body: { access_token: access, refresh_token: refresh } })
const REFUSED_401: Reply = { status: 401, body: { error: 'invalid or expired refresh token' } }
const UNAVAILABLE_502: Reply = { status: 502, body: { error: 'renewal is unavailable' } }

interface Call {
  url: string
  method: string
  auth: string | null
  body: unknown
}

// `reply` answers at once; with no reply, each request waits for `settle`.
function fakeFetch(reply: Reply | null) {
  const net = { calls: [] as Call[], reply, waiting: [] as ((r: Reply) => void)[], settle: (r: Reply) => net.waiting.shift()?.(r) }
  vi.stubGlobal(
    'fetch',
    vi.fn((url: string, init: { method?: string; headers: Headers; body?: string }) => {
      net.calls.push({
        url,
        method: init.method ?? 'GET',
        auth: init.headers.get('Authorization'),
        body: init.body === undefined ? undefined : JSON.parse(init.body),
      })
      return new Promise((resolve, reject) => {
        const answer = (r: Reply) => {
          if (r instanceof Error) {
            reject(r)
            return
          }
          resolve({
            ok: r.status >= 200 && r.status < 300,
            status: r.status,
            statusText: '',
            json: () => (r.body === undefined ? Promise.reject(new SyntaxError('no body')) : Promise.resolve(r.body)),
          })
        }
        if (net.reply) answer(net.reply)
        else net.waiting.push(answer)
      })
    }),
  )
  return net
}

function setup(o: { now: number; tracked?: Session | null; stored?: Session | null; reply?: Reply }) {
  const tracked = o.tracked === undefined ? hsession() : o.tracked
  const store = { session: o.stored === undefined ? tracked : o.stored }
  const clock = { now: o.now }
  const net = fakeFetch(o.reply ?? null)
  const load = vi.fn(() => store.session)
  const onRenewed = vi.fn<(next: Session) => void>()
  const onEnded = vi.fn<(end: { keepStorage: boolean }) => void>()
  const renewer = createRenewer({ base: BASE, now: () => clock.now, load, onRenewed, onEnded })
  renewer.track(tracked)
  return { renewer, store, clock, net, load, onRenewed, onEnded }
}

// Captures fresh()'s outcome without an unhandled rejection.
function outcome(v: string | null | Promise<string | null>): Promise<{ value?: string | null; error?: unknown }> {
  return Promise.resolve(v).then(
    (value) => ({ value }),
    (error: unknown) => ({ error }),
  )
}

const flush = () => new Promise((r) => setTimeout(r, 0))

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe('renewAt / deadline / isRenewalDue (AC-1)', () => {
  it("renewAt and deadline come from the token's own lifetime", () => {
    const s = hsession({ token: jwt({ ...claims('A0'), iat: 0, exp: 3600 }), receivedAt: 10_000 })
    expect(renewAt(s)).toBe(2_890_000)
    expect(deadline(s)).toBe(3_610_000)
    // exp − iat, not exp: the same lifetime at a real iat gives the same times.
    const real = hsession({ token: jwt({ ...claims('A0'), iat: 1000, exp: 4600 }), receivedAt: 10_000 })
    expect(renewAt(real)).toBe(2_890_000)
    expect(deadline(real)).toBe(3_610_000)
    expect(renewAt(hsession())).toBe(RENEW_AT)
    expect(deadline(hsession())).toBe(DEADLINE)

    const none = hsession({ refresh: null })
    expect(renewAt(none)).toBeNull()
    expect(deadline(none)).toBeNull()
    const opaque = hsession({ token: 'opaque-token' })
    expect(renewAt(opaque)).toBeNull()
    expect(deadline(opaque)).toBeNull()
  })

  it('a token without iat or exp is due', () => {
    // Control: readable times at receipt are not due.
    expect(isRenewalDue(hsession(), RECEIVED)).toBe(false)
    const rows: [string, string][] = [
      ['exp only', jwt({ sub: SUB, exp: EXP, app_metadata: { tenant_id: TENANT } })],
      ['iat only', jwt({ sub: SUB, iat: IAT, app_metadata: { tenant_id: TENANT } })],
      ['string times', jwt({ ...claims('A0'), iat: String(IAT), exp: String(EXP) })],
      ['string iat', jwt({ ...claims('A0'), iat: String(IAT) })],
      ['opaque', 'opaque-token'],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [name, token] of rows) {
      expect(isRenewalDue(hsession({ token }), RECEIVED), name).toBe(true)
    }
  })
})

describe('createRenewer', () => {
  it('not yet due: no request', async () => {
    const h = setup({ now: RENEW_AT - 1, reply: ok(A1, 'R1') })
    await expect(Promise.resolve(h.renewer.fresh())).resolves.toBe(A0)
    expect(h.net.calls).toHaveLength(0)
    expect(h.load).not.toHaveBeenCalled()
    expect(h.onRenewed).not.toHaveBeenCalled()
  })

  it('due: one refresh, then the new token', async () => {
    const timeout = vi.spyOn(AbortSignal, 'timeout')
    const h = setup({ now: RENEW_AT, reply: ok(A1, 'R1') })
    await expect(Promise.resolve(h.renewer.fresh())).resolves.toBe(A1)
    expect(h.net.calls).toEqual([{ url: `${BASE}/auth/refresh`, method: 'POST', auth: null, body: { refresh_token: 'R0' } }])
    // A hung gateway is a transient failure after 15 s, not a request that never ends.
    expect(timeout).toHaveBeenCalledWith(15_000)
    expect(vi.mocked(fetch).mock.calls[0]?.[1]?.signal).toBe(timeout.mock.results[0]?.value)
    expect(h.onRenewed).toHaveBeenCalledTimes(1)
    expect(h.onRenewed).toHaveBeenCalledWith({ ...hsession(), token: A1, renewal: { refreshToken: 'R1', receivedAt: RENEW_AT } })
    expect(h.onEnded).not.toHaveBeenCalled()
    // The renewed session is tracked and not due.
    expect(h.renewer.fresh()).toBe(A1)
    expect(h.net.calls).toHaveLength(1)
  })

  it('a renewal keeps the tenant kind and the mode it names', async () => {
    const inHouse: Me = { ...ME, tenant: { ...ME.tenant, kind: 'in_house' } }
    const h = setup({ now: RENEW_AT, tracked: hsession({ me: inHouse }), reply: ok(A1, 'R1') })
    await expect(Promise.resolve(h.renewer.fresh())).resolves.toBe(A1)
    expect(h.onRenewed).toHaveBeenCalledTimes(1)
    const next = h.onRenewed.mock.calls[0]?.[0]
    expect(next?.persona.mode).toBe('inhouse')
    expect(next?.me?.tenant.kind).toBe('in_house')
  })

  it('concurrent calls share one renewal', async () => {
    const h = setup({ now: RENEW_AT })
    const answers = Array.from({ length: 5 }, () => outcome(h.renewer.fresh()))
    await flush()
    expect(h.net.calls).toHaveLength(1)
    h.net.settle(ok(A1, 'R1'))
    expect((await Promise.all(answers)).map((a) => a.value)).toEqual([A1, A1, A1, A1, A1])
    expect(h.net.calls).toHaveLength(1)
    expect(h.onRenewed).toHaveBeenCalledTimes(1)
  })

  it('concurrent calls share a failed renewal, and the next call retries once', async () => {
    const h = setup({ now: DEADLINE - 1 })
    const failed = Array.from({ length: 3 }, () => outcome(h.renewer.fresh()))
    await flush()
    expect(h.net.calls).toHaveLength(1)
    h.net.settle(UNAVAILABLE_502)
    expect((await Promise.all(failed)).map((a) => a.value)).toEqual([A0, A0, A0])

    const retried = Array.from({ length: 3 }, () => outcome(h.renewer.fresh()))
    await flush()
    expect(h.net.calls).toHaveLength(2)
    h.net.settle(ok(A1, 'R1'))
    expect((await Promise.all(retried)).map((a) => a.value)).toEqual([A1, A1, A1])
    expect(h.net.calls).toHaveLength(2)
    expect(h.onRenewed).toHaveBeenCalledTimes(1)
    expect(h.onEnded).not.toHaveBeenCalled()
  })

  it('concurrent calls share a refusal: one request, one end', async () => {
    const h = setup({ now: RENEW_AT })
    const answers = Array.from({ length: 3 }, () => outcome(h.renewer.fresh()))
    await flush()
    h.net.settle(REFUSED_401)
    const settled = await Promise.all(answers)
    expect(settled).toHaveLength(3)
    for (const a of settled) {
      expect(a.error).toBeInstanceOf(SessionEndedError)
    }
    expect(h.net.calls).toHaveLength(1)
    expect(h.onEnded).toHaveBeenCalledTimes(1)
  })

  it('a renewal that outlives a track of another session still ends the session once', async () => {
    const h = setup({ now: RENEW_AT })
    const seat = h.store.session
    const first = outcome(h.renewer.fresh())
    await flush()
    h.renewer.track({ persona: APP_PERSONAS.firm, token: 'other', me: ME, verified: true })
    h.renewer.track(seat)
    const second = outcome(h.renewer.fresh())
    await flush()
    expect(h.net.calls.length).toBeGreaterThan(0)
    while (h.net.waiting.length > 0) {
      h.net.settle(REFUSED_401)
    }
    expect((await first).error).toBeInstanceOf(SessionEndedError)
    expect((await second).error).toBeInstanceOf(SessionEndedError)
    expect(h.onEnded).toHaveBeenCalledTimes(1)
  })

  it('re-tracking the same session keeps the renewal in flight', async () => {
    const h = setup({ now: RENEW_AT })
    const first = outcome(h.renewer.fresh())
    await flush()
    h.renewer.track(h.store.session)
    const second = outcome(h.renewer.fresh())
    await flush()
    expect(h.net.calls).toHaveLength(1)
    h.net.settle(ok(A1, 'R1'))
    expect((await first).value).toBe(A1)
    expect((await second).value).toBe(A1)
    expect(h.onRenewed).toHaveBeenCalledTimes(1)
  })

  it.each([
    ['a refused renewal ends the session once', 401],
    ['a 400 is refused too', 400],
  ])('%s', async (_name, status) => {
    const h = setup({ now: RENEW_AT, reply: { status, body: { error: 'refused' } } })
    const first = await outcome(h.renewer.fresh())
    expect(first.error).toBeInstanceOf(SessionEndedError)
    expect(h.onEnded).toHaveBeenCalledTimes(1)
    expect(h.onEnded).toHaveBeenCalledWith({ keepStorage: false })

    const second = await outcome(h.renewer.fresh())
    expect(second.error).toBeInstanceOf(SessionEndedError)
    expect(h.net.calls).toHaveLength(1)
    expect(h.onEnded).toHaveBeenCalledTimes(1)
    expect(h.onRenewed).not.toHaveBeenCalled()
  })

  it('transient before the deadline keeps the session', async () => {
    const rows: [string, Reply][] = [
      ['502', UNAVAILABLE_502],
      ['429', { status: 429, body: { error: 'too many requests' } }],
      ['network', new TypeError('Failed to fetch')],
      ['timeout', new DOMException('signal timed out', 'TimeoutError')],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [name, reply] of rows) {
      const h = setup({ now: DEADLINE - 1, reply })
      await expect(Promise.resolve(h.renewer.fresh()), name).resolves.toBe(A0)
      expect(h.net.calls, name).toHaveLength(1)
      expect(h.onEnded, name).not.toHaveBeenCalled()
      expect(h.onRenewed, name).not.toHaveBeenCalled()
      // Kept: the next call tries again.
      await expect(Promise.resolve(h.renewer.fresh()), name).resolves.toBe(A0)
      expect(h.net.calls, name).toHaveLength(2)
    }
  })

  // The stored refresh token may still be valid, so storage is kept for the next boot.
  it('transient at the deadline ends it', async () => {
    const rows: [string, number, Session][] = [
      ['at the deadline', DEADLINE, hsession()],
      ['after the deadline', DEADLINE + HOUR, hsession()],
      // No readable times means no deadline to be before.
      ['unreadable times, at receipt', RECEIVED, hsession({ token: 'opaque-token' })],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [name, now, tracked] of rows) {
      const h = setup({ now, tracked, reply: UNAVAILABLE_502 })
      const r = await outcome(h.renewer.fresh())
      expect(r.error, name).toBeInstanceOf(SessionEndedError)
      expect(h.net.calls, name).toHaveLength(1)
      expect(h.onEnded, name).toHaveBeenCalledTimes(1)
      expect(h.onEnded, name).toHaveBeenCalledWith({ keepStorage: true })
      expect(h.onRenewed, name).not.toHaveBeenCalled()
    }
  })

  it('a renewed token for another tenant ends it', async () => {
    const { app_metadata: _drop, ...noMetadata } = claims('A1')
    const { iat: _iat, ...noIat } = claims('A1')
    const { exp: _exp, ...noExp } = claims('A1')
    const rows: [string, string][] = [
      ['another tenant', jwt({ ...claims('A1'), app_metadata: { tenant_id: OTHER_TENANT } })],
      ['no app_metadata', jwt(noMetadata)],
      ['no tenant_id', jwt({ ...claims('A1'), app_metadata: {} })],
      ['no iat', jwt(noIat)],
      ['no exp', jwt(noExp)],
      ['opaque', 'opaque-A1'],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [name, token] of rows) {
      const h = setup({ now: RENEW_AT, reply: ok(token, 'R1') })
      const r = await outcome(h.renewer.fresh())
      expect(r.error, name).toBeInstanceOf(SessionEndedError)
      expect(h.onEnded, name).toHaveBeenCalledTimes(1)
      expect(h.onEnded, name).toHaveBeenCalledWith({ keepStorage: false })
      expect(h.onRenewed, name).not.toHaveBeenCalled()
    }
    // Control: the session's own tenant renews.
    const good = setup({ now: RENEW_AT, reply: ok(A1, 'R1') })
    await expect(Promise.resolve(good.renewer.fresh())).resolves.toBe(A1)
  })

  it('storage absent or another subject ends it without a request', async () => {
    const rows: [string, Session | null][] = [
      ['absent', null],
      ['another subject', hsession({ me: OTHER_ME, token: B0, refresh: 'RB' })],
      ['another user, same tenant', hsession({ me: { tenant: ME.tenant, user: { id: OTHER_SUB, role: 'authenticated', display_name: 'Adaeze Nwankwo', email: 'adaeze.nwankwo@example.com' } }, refresh: 'RC' })],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [name, stored] of rows) {
      const h = setup({ now: RENEW_AT, stored, reply: ok(A1, 'R1') })
      const r = await outcome(h.renewer.fresh())
      expect(r.error, name).toBeInstanceOf(SessionEndedError)
      expect(h.load, name).toHaveBeenCalled()
      expect(h.net.calls, name).toHaveLength(0)
      expect(h.onEnded, name).toHaveBeenCalledTimes(1)
      expect(h.onEnded, name).toHaveBeenCalledWith({ keepStorage: true })
      expect(h.onRenewed, name).not.toHaveBeenCalled()
    }
  })

  it('a newer stored session is adopted', async () => {
    const rows: [string, number][] = [
      ['received now', RENEW_AT],
      ['due 1 ms after now', RECEIVED + 1],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [name, receivedAt] of rows) {
      const stored = hsession({ token: A9, refresh: 'R9', receivedAt })
      const h = setup({ now: RENEW_AT, stored, reply: ok(A1, 'R1') })
      await expect(Promise.resolve(h.renewer.fresh()), name).resolves.toBe(A9)
      expect(h.net.calls, name).toHaveLength(0)
      expect(h.onRenewed, name).toHaveBeenCalledTimes(1)
      expect(h.onRenewed, name).toHaveBeenCalledWith(stored)
      expect(h.onEnded, name).not.toHaveBeenCalled()
      // Adopted means tracked: the next call answers the stored token plainly.
      expect(h.renewer.fresh(), name).toBe(A9)
    }
  })

  it('a stored copy with the same refresh token renews, even when it reads not due', async () => {
    const stored = hsession({ receivedAt: RENEW_AT })
    const h = setup({ now: RENEW_AT, stored, reply: ok(A1, 'R1') })
    await expect(Promise.resolve(h.renewer.fresh())).resolves.toBe(A1)
    expect(h.net.calls.map((c) => c.body)).toEqual([{ refresh_token: 'R0' }])
  })

  it('a due stored session renews with its own refresh token', async () => {
    const stored = hsession({ token: A5, refresh: 'R5', receivedAt: RECEIVED })
    const h = setup({ now: RENEW_AT, stored, reply: ok(A1, 'R1') })
    await expect(Promise.resolve(h.renewer.fresh())).resolves.toBe(A1)
    expect(h.net.calls.map((c) => c.body)).toEqual([{ refresh_token: 'R5' }])
    expect(h.onRenewed).toHaveBeenCalledWith({ ...stored, token: A1, renewal: { refreshToken: 'R1', receivedAt: RENEW_AT } })
  })

  it('a session without renewal never renews', () => {
    const expired = jwt({ ...claims('P0'), exp: 1 })
    const rows: [string, Session][] = [
      ['persona', { persona: APP_PERSONAS.firm, token: expired, me: ME, verified: true }],
      ['hand-off without renewal', hsession({ token: expired, refresh: null })],
      ['persona, opaque token', { persona: APP_PERSONAS.firm, token: 'opaque-token', me: ME, verified: true }],
      ['persona, null token', { persona: APP_PERSONAS.firm, token: null, me: null, verified: false }],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [name, tracked] of rows) {
      const h = setup({ now: DEADLINE + 10 * HOUR, tracked, reply: ok(A1, 'R1') })
      expect(h.renewer.fresh(), name).toBe(tracked.token)
      expect(h.load, name).not.toHaveBeenCalled()
      expect(h.net.calls, name).toHaveLength(0)
    }
  })

  it('nothing tracked resolves null', () => {
    const h = setup({ now: RENEW_AT, tracked: null, reply: ok(A1, 'R1') })
    expect(h.renewer.fresh()).toBeNull()
    // A due session, then sign-out.
    const out = setup({ now: RENEW_AT, reply: ok(A1, 'R1') })
    out.renewer.track(null)
    expect(out.renewer.fresh()).toBeNull()
    expect(out.net.calls).toHaveLength(0)
    expect(out.load).not.toHaveBeenCalled()
  })

  it('the injected clock alone decides', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(DEADLINE + 10 * HOUR)
    const wall = vi.spyOn(Date, 'now')
    const early = setup({ now: RENEW_AT - 1, reply: ok(A1, 'R1') })
    expect(early.renewer.fresh()).toBe(A0)
    expect(early.net.calls).toHaveLength(0)
    expect(isRenewalDue(hsession(), RENEW_AT - 1)).toBe(false)
    const adopt = setup({ now: RENEW_AT, stored: hsession({ token: A9, refresh: 'R9', receivedAt: RENEW_AT }), reply: ok(A1, 'R1') })
    await expect(Promise.resolve(adopt.renewer.fresh())).resolves.toBe(A9)
    expect(adopt.net.calls).toHaveLength(0)
    const beforeDeadline = setup({ now: DEADLINE - 1, reply: UNAVAILABLE_502 })
    await expect(Promise.resolve(beforeDeadline.renewer.fresh())).resolves.toBe(A0)
    expect(beforeDeadline.onEnded).not.toHaveBeenCalled()

    vi.setSystemTime(RECEIVED - 10 * HOUR)
    expect(isRenewalDue(hsession(), RENEW_AT)).toBe(true)
    const due = setup({ now: RENEW_AT, reply: ok(A1, 'R1') })
    await expect(Promise.resolve(due.renewer.fresh())).resolves.toBe(A1)
    expect(due.onRenewed).toHaveBeenCalledWith({ ...hsession(), token: A1, renewal: { refreshToken: 'R1', receivedAt: RENEW_AT } })
    const atDeadline = setup({ now: DEADLINE, reply: UNAVAILABLE_502 })
    expect((await outcome(atDeadline.renewer.fresh())).error).toBeInstanceOf(SessionEndedError)
    expect(wall).not.toHaveBeenCalled()
    // Control: the spy sees a default-argument read.
    isRenewalDue(hsession())
    expect(wall).toHaveBeenCalledTimes(1)
  })

  it('nothing due returns a plain value', () => {
    const rows: [string, Session, string][] = [
      ['not yet due', hsession(), A0],
      ['no renewal', hsession({ refresh: null }), A0],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [name, tracked, want] of rows) {
      const h = setup({ now: RENEW_AT - 1, tracked, reply: ok(A1, 'R1') })
      const v = h.renewer.fresh()
      expect(v, name).toBe(want)
      expect(typeof (v as { then?: unknown } | null)?.then, name).toBe('undefined')
    }
  })

  it('sign-out during a renewal discards the answer', async () => {
    const rows: [string, Reply][] = [
      ['200', ok(A1, 'R1')],
      ['401', REFUSED_401],
      ['502', UNAVAILABLE_502],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [name, reply] of rows) {
      const h = setup({ now: RENEW_AT })
      const pending = outcome(h.renewer.fresh())
      await flush()
      h.renewer.track(null)
      h.net.settle(reply)
      expect((await pending).error, name).toBeInstanceOf(SessionEndedError)
      expect(h.onRenewed, name).not.toHaveBeenCalled()
      expect(h.onEnded, name).not.toHaveBeenCalled()
      expect(h.net.calls, name).toHaveLength(1)
      expect(h.renewer.fresh(), name).toBeNull()
      expect(h.net.calls, name).toHaveLength(1)
    }
  })

  it('a new session during a renewal discards the old answer', async () => {
    const other = hsession({ me: OTHER_ME, token: B0, refresh: 'RB', receivedAt: RENEW_AT })
    const rows: [string, Reply][] = [
      ['200', ok(A1, 'R1')],
      ['401', REFUSED_401],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [name, reply] of rows) {
      const h = setup({ now: RENEW_AT })
      const pending = outcome(h.renewer.fresh())
      await flush()
      h.renewer.track(other)
      h.store.session = other
      h.net.settle(reply)
      expect((await pending).error, name).toBeInstanceOf(SessionEndedError)
      expect(h.onRenewed, name).not.toHaveBeenCalled()
      expect(h.onEnded, name).not.toHaveBeenCalled()
      expect(h.net.calls, name).toHaveLength(1)
      expect(h.renewer.fresh(), name).toBe(B0)
      expect(h.net.calls, name).toHaveLength(1)
    }
    // A due new session does not join the discarded flight: it sends its own request.
    const dueOther = hsession({ me: OTHER_ME, token: B0, refresh: 'RB' })
    const B1 = jwt({ ...claims('B1'), sub: OTHER_SUB, app_metadata: { tenant_id: OTHER_TENANT } })
    const h = setup({ now: RENEW_AT })
    const pending = outcome(h.renewer.fresh())
    await flush()
    h.renewer.track(dueOther)
    h.store.session = dueOther
    const own = outcome(h.renewer.fresh())
    await flush()
    expect(h.net.calls.map((c) => c.body)).toEqual([{ refresh_token: 'R0' }, { refresh_token: 'RB' }])
    h.net.settle(ok(A1, 'R1'))
    h.net.settle(ok(B1, 'RB1'))
    expect((await pending).error).toBeInstanceOf(SessionEndedError)
    expect((await own).value).toBe(B1)
    expect(h.onRenewed).toHaveBeenCalledTimes(1)
    expect(h.onRenewed).toHaveBeenCalledWith({ ...dueOther, token: B1, renewal: { refreshToken: 'RB1', receivedAt: RENEW_AT } })
  })

  it('a new session after an end renews normally', async () => {
    const h = setup({ now: RENEW_AT, reply: REFUSED_401 })
    const ended = await outcome(h.renewer.fresh())
    // Only a different session clears the latch: sign-out and re-tracking the ended one do not.
    const endedSession = h.store.session
    h.renewer.track(null)
    h.renewer.track(endedSession)
    expect((await outcome(h.renewer.fresh())).error).toBeInstanceOf(SessionEndedError)
    h.renewer.track(endedSession)
    expect((await outcome(h.renewer.fresh())).error).toBeInstanceOf(SessionEndedError)
    expect(h.net.calls).toHaveLength(1)
    const next = hsession({ token: A7, refresh: 'R7', receivedAt: RECEIVED })
    h.renewer.track(next)
    h.store.session = next
    h.net.reply = ok(A1, 'R1')
    await expect(Promise.resolve(h.renewer.fresh())).resolves.toBe(A1)
    expect(h.net.calls.map((c) => c.body)).toEqual([{ refresh_token: 'R0' }, { refresh_token: 'R7' }])
    expect(h.onRenewed).toHaveBeenCalledWith({ ...next, token: A1, renewal: { refreshToken: 'R1', receivedAt: RENEW_AT } })
    expect(ended.error).toBeInstanceOf(SessionEndedError)
    expect(h.onEnded).toHaveBeenCalledTimes(1)
    // A different session in between cleared the latch, so the old one may try again.
    h.renewer.track(endedSession)
    expect((await outcome(h.renewer.fresh())).value).toBe(A1)
    expect(h.net.calls).toHaveLength(3)
  })

  it('storage cleared mid-flight writes nothing', async () => {
    const other = hsession({ me: OTHER_ME, token: B0, refresh: 'RB' })
    const foreign = jwt({ ...claims('A1'), app_metadata: { tenant_id: OTHER_TENANT } })
    // Whatever the answer, storage switched under it is left alone.
    const rows: [string, Session | null, Reply, number][] = [
      ['cleared, 200', null, ok(A1, 'R1'), RENEW_AT],
      ['another subject, 200', other, ok(A1, 'R1'), RENEW_AT],
      ['another subject, 401', other, REFUSED_401, RENEW_AT],
      ['cleared, 400', null, { status: 400, body: { error: 'refresh_token is required' } }, RENEW_AT],
      ['another subject, other tenant', other, ok(foreign, 'R1'), RENEW_AT],
      ['cleared, 502 at the deadline', null, UNAVAILABLE_502, DEADLINE],
      ['another subject, 502 before the deadline', other, UNAVAILABLE_502, DEADLINE - 1],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [name, stored, reply, now] of rows) {
      const h = setup({ now })
      const pending = outcome(h.renewer.fresh())
      await flush()
      h.store.session = stored
      h.net.settle(reply)
      expect((await pending).error, name).toBeInstanceOf(SessionEndedError)
      expect(h.net.calls, name).toHaveLength(1)
      expect(h.onRenewed, name).not.toHaveBeenCalled()
      expect(h.onEnded, name).toHaveBeenCalledTimes(1)
      expect(h.onEnded, name).toHaveBeenCalledWith({ keepStorage: true })
    }
  })

  it('a malformed 200 is transient', async () => {
    const rows: [string, unknown][] = [
      ['no refresh_token', { access_token: A1 }],
      ['numeric refresh_token', { access_token: A1, refresh_token: 7 }],
      ['empty refresh_token', { access_token: A1, refresh_token: '' }],
      ['no access_token', { refresh_token: 'R1' }],
      ['empty access_token', { access_token: '', refresh_token: 'R1' }],
      ['numeric access_token', { access_token: 7, refresh_token: 'R1' }],
      ['null body', null],
    ]
    expect(rows.length).toBeGreaterThan(0)
    for (const [name, body] of rows) {
      const h = setup({ now: DEADLINE - 1, reply: { status: 200, body } })
      await expect(Promise.resolve(h.renewer.fresh()), name).resolves.toBe(A0)
      expect(h.net.calls, name).toHaveLength(1)
      expect(h.onRenewed, name).not.toHaveBeenCalled()
      expect(h.onEnded, name).not.toHaveBeenCalled()
    }
    // Transient, not ignored: at the deadline it ends the session.
    const late = setup({ now: DEADLINE, reply: { status: 200, body: { access_token: A1 } } })
    expect((await outcome(late.renewer.fresh())).error).toBeInstanceOf(SessionEndedError)
    expect(late.onEnded).toHaveBeenCalledTimes(1)
    expect(late.onRenewed).not.toHaveBeenCalled()
  })
})
