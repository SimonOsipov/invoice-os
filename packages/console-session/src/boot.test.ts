import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { resolveConsoleBoot, type ConsoleBoot } from './boot'
import {
  b64url,
  BASE64URL_43_RE,
  type Call,
  CODE,
  customerToken,
  GW,
  installFetch,
  installStorage,
  LANDING,
  NOW,
  OPS_KEY,
  recordRaw,
  reply,
  type MemoryStorage,
  spyTimeouts,
  staffToken,
  STATE_A,
  STATE_KEY,
  stateRaw,
  timeoutError,
} from './testkit'

const TIMEOUT_MS = 15000 // AbortSignal.timeout in redeemHandoff and RENEW_TIMEOUT_MS (frontend/app/src/lib)
const EXCHANGE = `${GW}/auth/exchange`
const REFRESH = `${GW}/auth/refresh`
const FRONT_DOOR = new RegExp(`^${LANDING}/\\?state=([A-Za-z0-9_-]{43})&console=ops$`)
const withOutcome = (o: string) => new RegExp(`^${LANDING}/\\?state=([A-Za-z0-9_-]{43})&console=ops&signin=${o}$`)

const OLD_TOKEN = staffToken('old', NOW / 1000 + 10 * 365 * 24 * 3600)
const OLD_RECORD = recordRaw(OLD_TOKEN, 'R-old')
const pair = (token: string, refresh: string) => reply(200, { access_token: token, refresh_token: refresh })

let store: { local: MemoryStorage; session: MemoryStorage }
let timeouts: ReturnType<typeof spyTimeouts>

beforeEach(() => {
  vi.spyOn(console, 'warn').mockImplementation(() => undefined)
  timeouts = spyTimeouts()
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

interface Setup {
  local?: string
  state?: string
  answer?: (c: Call) => Response | Error
  search?: string
  target?: 'ops' | 'support'
  gateway?: string | null
  landing?: string | null
}

async function run(o: Setup = {}) {
  store = installStorage({
    local: o.local === undefined ? {} : { [OPS_KEY]: o.local },
    session: o.state === undefined ? {} : { [STATE_KEY]: o.state },
  })
  const net = installFetch((c) => (o.answer ?? (() => new Error('unexpected request')))(c))
  const boot = await resolveConsoleBoot({
    search: o.search ?? '',
    storageKey: OPS_KEY,
    target: o.target ?? 'ops',
    gateway: o.gateway === undefined ? GW : o.gateway,
    landing: o.landing === undefined ? LANDING : o.landing,
    now: NOW,
  })
  return { boot, calls: net.calls }
}

function leaveUrl(boot: ConsoleBoot): string {
  expect(boot.kind).toBe('leave')
  return boot.kind === 'leave' ? boot.url : ''
}

// The state landing must hold is the one the tab stored.
const heldBlob = (): { v: number; s: string; at: number } | null => {
  const raw = store.session.getItem(STATE_KEY)
  return raw === null ? null : (JSON.parse(raw) as { v: number; s: string; at: number })
}
const heldState = (): string | null => heldBlob()?.s ?? null

describe('standalone (AC-4)', () => {
  it('boot_standaloneOpensWithNoRequest', async () => {
    const searches = ['', '?auth=start', `?handoff=${CODE}`]
    for (const search of searches) {
      const { boot, calls } = await run({ landing: null, search, local: OLD_RECORD, state: stateRaw(STATE_A, NOW) })
      expect(boot, search).toEqual({ kind: 'open', session: null })
      expect(calls, search).toHaveLength(0)
      for (const s of [store.local, store.session]) {
        expect(s.getItem, search).not.toHaveBeenCalled()
        expect(s.setItem, search).not.toHaveBeenCalled()
        expect(s.removeItem, search).not.toHaveBeenCalled()
      }
    }
  })
})

describe('?auth=start (AC-5)', () => {
  it('boot_authStartMintsAFreshStateAndLeavesReady', async () => {
    const { boot, calls } = await run({ search: '?auth=start', state: stateRaw(STATE_A, NOW - 10_000), local: OLD_RECORD })
    const url = leaveUrl(boot)
    const m = withOutcome('ready').exec(url)
    expect(m, url).not.toBeNull()
    const state = m?.[1]
    expect(state).not.toBe(STATE_A)
    expect(heldState()).toBe(state)
    expect(heldBlob()).toEqual({ v: 1, s: state, at: NOW })
    expect(calls).toHaveLength(0)
  })
})

describe('hand-off redemption (AC-6, AC-7)', () => {
  it('boot_handoffStaffIsStoredAndOpens', async () => {
    const access = staffToken('exchanged')
    const { boot, calls } = await run({
      search: `?handoff=${CODE}`,
      state: stateRaw(STATE_A, NOW - 1000),
      answer: () => pair(access, 'R-exchanged'),
    })
    expect(boot).toEqual({ kind: 'open', session: { token: access, refreshToken: 'R-exchanged' } })
    expect(calls).toHaveLength(1)
    expect(calls[0]).toMatchObject({ url: EXCHANGE, method: 'POST', body: { code: CODE, state: STATE_A } })
    expect(JSON.parse(store.local.getItem(OPS_KEY) ?? 'null')).toEqual({ v: 2, token: access, refresh_token: 'R-exchanged' })
    expect(store.session.getItem(STATE_KEY)).toBeNull()
    expect(timeouts).toHaveBeenCalledWith(TIMEOUT_MS)
  })

  it('boot_handoffNonStaffStoresNothing', async () => {
    const { boot, calls } = await run({
      search: `?handoff=${CODE}`,
      state: stateRaw(STATE_A, NOW - 1000),
      answer: () => pair(customerToken('exchanged'), 'R-exchanged'),
    })
    const url = leaveUrl(boot)
    expect(withOutcome('not-staff').test(url), url).toBe(true)
    expect(calls).toHaveLength(1)
    expect(store.local.setItem).not.toHaveBeenCalled()
    expect(store.local.getItem(OPS_KEY)).toBeNull()
    expect(heldState()).toBe(withOutcome('not-staff').exec(url)?.[1])
  })

  it('boot_handoffFailureKeepsAStoredRecord', async () => {
    const stored = recordRaw(OLD_TOKEN, 'R-keep')
    const failures: [string, Response | Error][] = [
      ['exchange 400', reply(400, { error: 'invalid or expired code' })],
      ['network', new TypeError('Failed to fetch')],
      ['timeout', timeoutError()],
    ]
    for (const [name, answer] of failures) {
      const { boot, calls } = await run({ search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), local: stored, answer: () => answer })
      const url = leaveUrl(boot)
      expect(withOutcome('failed').test(url), `${name}: ${url}`).toBe(true)
      expect(calls.map((c) => c.url), name).toEqual([EXCHANGE])
      expect(store.local.getItem(OPS_KEY), name).toBe(stored)
      expect(store.local.setItem, name).not.toHaveBeenCalled()
      expect(store.local.removeItem, name).not.toHaveBeenCalled()
    }
  })

  it('boot_handoffWithoutStateRedeemsNothing', async () => {
    const renewed = staffToken('renewed')
    const scenarios: [string, Setup][] = [
      ['no live state', { search: `?handoff=${CODE}` }],
      ['state at the TTL', { search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 600_000) }],
      ['code of 42 chars', { search: `?handoff=${CODE.slice(1)}`, state: stateRaw(STATE_A, NOW - 1000) }],
      ['code with +', { search: `?handoff=${CODE.slice(1)}%2B`, state: stateRaw(STATE_A, NOW - 1000) }],
      ['two codes', { search: `?handoff=${CODE}&handoff=${STATE_A}`, state: stateRaw(STATE_A, NOW - 1000) }],
    ]
    for (const [name, setup] of scenarios) {
      const { boot, calls } = await run({ ...setup, local: OLD_RECORD, answer: () => pair(renewed, 'R-renewed') })
      expect(calls.filter((c) => c.url === EXCHANGE), name).toHaveLength(0)
      expect(calls.filter((c) => c.url === REFRESH), name).toHaveLength(1)
      expect(calls[0]?.body, name).toEqual({ refresh_token: 'R-old' })
      expect(boot, name).toEqual({ kind: 'open', session: { token: renewed, refreshToken: 'R-renewed' } })
    }
    // No record either: the front door, with no outcome.
    const { boot, calls } = await run({ search: `?handoff=${CODE}` })
    expect(calls).toHaveLength(0)
    expect(FRONT_DOOR.test(leaveUrl(boot))).toBe(true)
  })
})

describe('renewal of a stored record (AC-8, AC-9)', () => {
  it('boot_storedRecordAlwaysRenewsFirst', async () => {
    const renewed = staffToken('renewed')
    const { boot, calls } = await run({ local: OLD_RECORD, answer: () => pair(renewed, 'R-renewed') })
    expect(calls).toHaveLength(1)
    expect(calls[0]).toMatchObject({ url: REFRESH, method: 'POST', body: { refresh_token: 'R-old' } })
    expect(boot).toEqual({ kind: 'open', session: { token: renewed, refreshToken: 'R-renewed' } })
    expect(JSON.parse(store.local.getItem(OPS_KEY) ?? 'null')).toEqual({ v: 2, token: renewed, refresh_token: 'R-renewed' })
    expect(timeouts).toHaveBeenCalledWith(TIMEOUT_MS)
  })

  it('boot_expiredStoredTokenRenews', async () => {
    const expired = staffToken('expired', NOW / 1000 - 3600)
    const renewed = staffToken('renewed')
    const { boot, calls } = await run({ local: recordRaw(expired, 'R-old'), answer: () => pair(renewed, 'R-renewed') })
    expect(calls.map((c) => c.url)).toEqual([REFRESH])
    expect(boot).toEqual({ kind: 'open', session: { token: renewed, refreshToken: 'R-renewed' } })
    expect(JSON.parse(store.local.getItem(OPS_KEY) ?? 'null')).toEqual({ v: 2, token: renewed, refresh_token: 'R-renewed' })
  })

  it('boot_refusedRenewalClearsAndLeaves', async () => {
    for (const status of [400, 401]) {
      const { boot, calls } = await run({ local: OLD_RECORD, answer: () => reply(status, { error: 'invalid or expired refresh token' }) })
      expect(calls, String(status)).toHaveLength(1)
      expect(store.local.getItem(OPS_KEY), String(status)).toBeNull()
      const url = leaveUrl(boot)
      expect(FRONT_DOOR.test(url), `${status}: ${url}`).toBe(true)
      expect(url).not.toContain('signin=')
      expect(heldState()).toBe(FRONT_DOOR.exec(url)?.[1])
    }
  })

  it('boot_destaffedRenewalLeavesNotStaff', async () => {
    const { boot, calls } = await run({ local: OLD_RECORD, answer: () => pair(customerToken('renewed'), 'R-renewed') })
    expect(calls).toHaveLength(1)
    expect(store.local.getItem(OPS_KEY)).toBeNull()
    expect(store.local.setItem).not.toHaveBeenCalled()
    const url = leaveUrl(boot)
    expect(withOutcome('not-staff').test(url), url).toBe(true)
  })

  it('boot_transientRenewalKeepsTheRecord', async () => {
    const answers: [string, Response | Error][] = [
      ['network error', new TypeError('Failed to fetch')],
      ['timeout', timeoutError()],
      ['429', reply(429, { error: 'too many requests' })],
      ['502', reply(502, { error: 'renewal is unavailable' })],
      ['503', reply(503, { error: 'unavailable' })],
      ['200 with only an access token', reply(200, { access_token: staffToken('renewed') })],
      ['200 with an empty refresh token', reply(200, { access_token: staffToken('renewed'), refresh_token: '' })],
      ['200 with a non-JSON body', new Response('<html>', { status: 200 })],
    ]
    expect(answers.length).toBeGreaterThan(0)
    for (const [name, answer] of answers) {
      const { boot, calls } = await run({ local: OLD_RECORD, answer: () => answer })
      expect(calls, name).toHaveLength(1)
      expect(store.local.getItem(OPS_KEY), name).toBe(OLD_RECORD)
      expect(store.local.removeItem, name).not.toHaveBeenCalled()
      expect(store.local.setItem, name).not.toHaveBeenCalled()
      const url = leaveUrl(boot)
      expect(withOutcome('failed').test(url), `${name}: ${url}`).toBe(true)
    }
  })

  it('boot_forgedV2RecordIsRefused', async () => {
    // Unsigned JWT with the staff claim; only the gateway's refresh answer can open the console.
    const forged = `${b64url({ alg: 'none' })}.${b64url({ sub: 'x', exp: NOW / 1000 + 3600, app_metadata: { staff: true } })}.`
    const { boot, calls } = await run({ local: recordRaw(forged, 'R-forged'), answer: () => reply(401, { error: 'invalid or expired refresh token' }) })
    expect(calls.map((c) => c.url)).toEqual([REFRESH])
    expect(boot.kind).toBe('leave')
    expect(store.local.getItem(OPS_KEY)).toBeNull()
  })
})

describe('front door and missing gateway (AC-10)', () => {
  it('boot_nothingStoredLeavesByTheFrontDoor', async () => {
    const { boot, calls } = await run({})
    const url = leaveUrl(boot)
    const m = FRONT_DOOR.exec(url)
    expect(m, url).not.toBeNull()
    expect(calls).toHaveLength(0)
    expect(heldBlob()).toEqual({ v: 1, s: m?.[1], at: NOW })

    // A state minted under a minute ago is reused (ensure, not mint).
    const held = await run({ state: stateRaw(STATE_A, NOW - 30_000) })
    expect(leaveUrl(held.boot)).toBe(`${LANDING}/?state=${STATE_A}&console=ops`)

    const support = await run({ target: 'support' })
    expect(leaveUrl(support.boot)).toMatch(new RegExp(`^${LANDING}/\\?state=[A-Za-z0-9_-]{43}&console=support$`))
  })

  it('boot_noGatewayNeverOpens', async () => {
    const withCode = await run({ gateway: null, search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000) })
    expect(withCode.calls).toHaveLength(0)
    expect(withOutcome('failed').test(leaveUrl(withCode.boot))).toBe(true)

    const withRecord = await run({ gateway: null, local: OLD_RECORD })
    expect(withRecord.calls).toHaveLength(0)
    expect(FRONT_DOOR.test(leaveUrl(withRecord.boot))).toBe(true)
  })
})

describe('URLs (AC-13)', () => {
  it('urls_carryNoTokenOrCode', async () => {
    const access = staffToken('exchanged')
    const refresh = 'R-secret-refresh'
    const secrets = [access, refresh, 'R-old', OLD_TOKEN, CODE, 'eyJ']
    const scenarios: Setup[] = [
      { search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), answer: () => pair(customerToken('x'), refresh) },
      { search: `?handoff=${CODE}`, state: stateRaw(STATE_A, NOW - 1000), answer: () => reply(400, { error: access }) },
      { search: `?handoff=${CODE}`, gateway: null, state: stateRaw(STATE_A, NOW - 1000) },
      { search: '?auth=start', local: OLD_RECORD },
      { local: OLD_RECORD, answer: () => reply(401, { error: 'invalid or expired refresh token' }) },
      { local: OLD_RECORD, answer: () => pair(customerToken('x'), refresh) },
      { local: OLD_RECORD, answer: () => reply(503) },
      { search: `?handoff=${CODE}` },
      {},
    ]
    const urls: string[] = []
    for (const s of scenarios) {
      const { boot } = await run(s)
      if (boot.kind === 'leave') urls.push(boot.url)
    }
    expect(urls).toHaveLength(scenarios.length)
    for (const url of urls) {
      expect(url.startsWith(`${LANDING}/?state=`), url).toBe(true)
      for (const secret of secrets) expect(url, secret).not.toContain(secret)
      expect(url.match(/state=([^&]+)/)?.[1]).toMatch(BASE64URL_43_RE)
    }
  })
})
